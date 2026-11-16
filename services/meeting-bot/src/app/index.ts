import express, { Request, Response } from 'express';
import client from 'prom-client';
import config from '../config';
import googleRouter from './google';
import microsoftRouter from './microsoft';
import zoomRouter from './zoom';
import { RedisConsumerService } from '../connect/RedisConsumerService';
import { MeetingJoinParams, detectProvider, validateMeetingId } from './common';
import { containerManager } from '../services/globalContainerManager';
import { loggerFactory } from '../util/logger';

const logger = loggerFactory('system', 'api');

const app = express();

app.use(express.json());

export const redisConsumerService = new RedisConsumerService();

let gracefulShutdown = 0;

app.get('/isbusy', async (req, res) => {
  const running = containerManager.getRunningCount();
  const max = containerManager.getMaxConcurrent();
  return res.status(200).json({
    success: true,
    data: containerManager.isFull() ? 1 : 0,
    running,
    max,
    available: max - running,
  });
});

app.get('/health', async (req, res) => {
  const dockerAvailable = await containerManager.isDockerAvailable();
  const statusCode = dockerAvailable ? 200 : 503;
  return res.status(statusCode).json({
    status: dockerAvailable ? 'healthy' : 'degraded',
    docker: dockerAvailable,
    timestamp: new Date().toISOString(),
    uptime: process.uptime(),
    meetings: {
      running: containerManager.getRunningCount(),
      max: containerManager.getMaxConcurrent(),
    },
  });
});

// Metrics
const concurrentMeetings = new client.Gauge({
  name: 'concurrent_meetings',
  help: 'Number of currently running meeting containers',
});

const maxMeetings = new client.Gauge({
  name: 'max_concurrent_meetings',
  help: 'Maximum allowed concurrent meeting containers',
});

app.get('/metrics', async (req, res) => {
  concurrentMeetings.set(containerManager.getRunningCount());
  maxMeetings.set(containerManager.getMaxConcurrent());
  res.set('Content-Type', client.register.contentType);
  res.end(await client.register.metrics());
});

// Unified meeting join endpoint
const joinMeeting = async (req: Request, res: Response) => {
  // API key auth
  const apiKey = req.headers['x-bot-api-key'] as string;
  if (!apiKey || apiKey !== config.miaBotApiKey) {
    return res.status(401).json({ success: false, error: 'Unauthorized' });
  }

  const {
    meeting_url,
    bot_name,
    meeting_id,
    language,
    force,
  }: MeetingJoinParams = req.body;

  // Validate required fields
  if (!meeting_url || !bot_name || !meeting_id) {
    return res.status(400).json({
      success: false,
      error: 'Missing required fields: meeting_url, bot_name, meeting_id'
    });
  }

  // Validate meeting_id format (prevent path traversal)
  if (!validateMeetingId(meeting_id)) {
    return res.status(400).json({
      success: false,
      error: 'Invalid meeting_id format. Only alphanumeric characters, hyphens, and underscores are allowed.'
    });
  }

  // Detect provider from URL
  const provider = detectProvider(meeting_url);
  if (!provider) {
    return res.status(400).json({
      success: false,
      error: 'Unsupported meeting URL. Supported providers: Google Meet, Microsoft Teams, Zoom',
      data: { meeting_id }
    });
  }

  try {
    const result = await containerManager.spawnMeeting({
      meetingId: meeting_id,
      meetingUrl: meeting_url,
      botName: bot_name,
      language,
      force,
    });

    if (!result.accepted) {
      if (result.reason === 'duplicate') {
        return res.status(409).json({
          success: false,
          error: `Meeting ${meeting_id} is already running`,
          data: { meeting_id }
        });
      }
      const running = containerManager.getRunningCount();
      const max = containerManager.getMaxConcurrent();
      return res.status(429).json({
        success: false,
        error: `Maximum concurrent meetings reached (${running}/${max})`,
        data: { meeting_id }
      });
    }

    logger.info('Meeting container spawned', { provider, meeting_id });

    return res.status(202).json({
      success: true,
      message: 'Meeting join request accepted and container started',
      data: { meeting_id, provider, status: 'processing' }
    });

  } catch (error) {
    logger.error('Error spawning container', { meeting_id, error: error instanceof Error ? error.message : String(error) });

    return res.status(500).json({
      success: false,
      error: error instanceof Error ? error.message : 'Unknown error occurred',
      data: { meeting_id }
    });
  }
};

app.post('/meetings/join', joinMeeting);

// Legacy per-provider endpoints
app.use('/google', googleRouter);
app.use('/microsoft', microsoftRouter);
app.use('/zoom', zoomRouter);

export const setGracefulShutdown = (val: number) =>
  gracefulShutdown = val;

export const getGracefulShutdown = () => gracefulShutdown;

// Start Redis consumer service only if Redis is enabled
if (config.isRedisEnabled) {
  redisConsumerService.start().catch((error) => {
    logger.error('Failed to start Redis consumer service', { error });
  });
} else {
  logger.info('Redis consumer service not started - Redis is disabled');
}

export default app;
