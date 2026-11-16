import express, { Request, Response } from 'express';
import { MeetingJoinParams, detectProvider, validateMeetingId } from './common';
import { containerManager } from '../services/globalContainerManager';
import config from '../config';
import { loggerFactory } from '../util/logger';

const logger = loggerFactory('system', 'api-microsoft');

const router = express.Router();

const joinMicrosoftTeams = async (req: Request, res: Response) => {
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

  // Verify URL matches this provider endpoint
  if (detectProvider(meeting_url) !== 'microsoft') {
    return res.status(400).json({
      success: false,
      error: 'meeting_url does not match Microsoft Teams. Use POST /meetings/join for auto-detection.',
      data: { meeting_id }
    });
  }

  try {
    const result = await containerManager.spawnMeeting({
      meetingId: meeting_id,
      meetingUrl: meeting_url,
      botName: bot_name,
      language,
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

    logger.info('Microsoft Teams container spawned', { meeting_id });

    return res.status(202).json({
      success: true,
      message: 'Microsoft Teams join request accepted and container started',
      data: { meeting_id, status: 'processing' }
    });

  } catch (error) {
    logger.error('Error spawning Microsoft Teams container', { meeting_id, error: error instanceof Error ? error.message : String(error) });

    return res.status(500).json({
      success: false,
      error: error instanceof Error ? error.message : 'Unknown error occurred',
      data: { meeting_id }
    });
  }
};

router.post('/join', joinMicrosoftTeams);

export default router;
