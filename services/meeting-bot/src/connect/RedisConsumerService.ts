import { MeetingJoinRedisParams, detectProvider, validateMeetingId } from '../app/common';
import messageBroker from './messageBroker';
import config from '../config';
import { containerManager } from '../services/globalContainerManager';
import { loggerFactory } from '../util/logger';

const logger = loggerFactory('system', 'redis-consumer');

export class RedisConsumerService {
  private _isRunning: boolean = false;
  private _isShutdownRequested: boolean = false;
  private reconnectInterval: NodeJS.Timeout | null = null;
  private readonly RECONNECT_DELAY = 5000; // 5 seconds
  private readonly BLPOP_TIMEOUT = 10; // 10 seconds

  async start(): Promise<void> {
    if (this._isRunning) {
      logger.warn('Redis consumer service is already running');
      return;
    }

    // Check if Redis is enabled
    if (!config.isRedisEnabled) {
      logger.info('Redis consumer service not started - Redis is disabled');
      return;
    }

    try {
      // Check if messageBroker is connected
      if (!messageBroker.isConnected()) {
        logger.warn('Message broker not connected, waiting for connection');
        // Wait for connection or handle as needed
      }

      this._isRunning = true;
      this.setShutdownRequested(false);

      logger.info('Redis consumer service started');
      this.startMessageLoop().catch((error) => {
        logger.error('Failed to start message loop', { error });
        throw error;
      });

    } catch (error) {
      logger.error('Failed to start Redis consumer service', { error });
      throw error;
    }
  }

  async shutdown(): Promise<void> {
    if (!this._isRunning) {
      return;
    }

    logger.info('Initiating graceful shutdown of Redis consumer service');
    this.setShutdownRequested(true);
    this._isRunning = false;

    // Clear any pending reconnect attempts
    if (this.reconnectInterval) {
      clearTimeout(this.reconnectInterval);
      this.reconnectInterval = null;
    }

    // Note: Redis connection is managed by messageBroker
    logger.info('Redis consumer service shutdown initiated');
  }

  private async startMessageLoop(): Promise<void> {
    while (this._isRunning && !this.isShutdownRequested()) {
      try {
        // Check container capacity BEFORE attempting to get messages
        if (containerManager.isFull()) {
          await new Promise(resolve => setTimeout(resolve, this.BLPOP_TIMEOUT * 1000));
          continue;
        }

        // Capacity available, now we can safely get a message
        const result = await messageBroker.getMeetingbotJobsWithTimeout(this.BLPOP_TIMEOUT);

        if (result && result.element) {
          await this.processMessage(result.element);
        }

      } catch (error) {
        if (this.isShutdownRequested()) {
          break;
        }

        logger.error('Error in message loop', { error });

        // Attempt to reconnect if connection is lost
        if (!messageBroker.isConnected()) {
          await this.handleReconnection();
        }
      }
    }
  }

  private async processMessage(message: string): Promise<void> {
    let meetingParams: MeetingJoinRedisParams;
    try {
      meetingParams = JSON.parse(message);
    } catch {
      logger.error('Failed to parse message as JSON, dropping', { preview: message.substring(0, 200) });
      return;
    }

    // Validate and strip API key from payload — never let it reach logs
    const { api_key, ...safeParams } = meetingParams;
    if (!api_key || api_key !== config.miaBotApiKey) {
      logger.error('Redis message rejected: invalid or missing api_key', {
        provider: safeParams.provider,
        meetingId: safeParams.meeting_id,
      });
      return;
    }

    // Validate required fields
    if (!safeParams.meeting_url || !safeParams.bot_name || !safeParams.meeting_id) {
      logger.error('Redis message rejected: missing required fields (meeting_url, bot_name, meeting_id)', {
        hasMeetingUrl: !!safeParams.meeting_url,
        hasBotName: !!safeParams.bot_name,
        hasMeetingId: !!safeParams.meeting_id,
      });
      return;
    }

    // Validate meeting_id format (prevent path traversal)
    if (!validateMeetingId(safeParams.meeting_id)) {
      logger.error('Redis message rejected: invalid meeting_id format', {
        meetingId: safeParams.meeting_id,
      });
      return;
    }

    // Resolve provider: explicit field or detect from URL
    const provider = safeParams.provider || detectProvider(safeParams.meeting_url);
    if (!provider) {
      logger.error('Redis message rejected: cannot determine provider', {
        meetingId: safeParams.meeting_id,
        meetingUrl: safeParams.meeting_url,
      });
      return;
    }

    try {
      logger.info('Processing message', { provider, meetingId: safeParams.meeting_id });

      const result = await containerManager.spawnMeeting({
        meetingId: safeParams.meeting_id,
        meetingUrl: safeParams.meeting_url,
        botName: safeParams.bot_name,
        language: safeParams.language,
      });

      if (result.accepted) {
        logger.info('Container spawned', { meetingId: safeParams.meeting_id });
      } else if (result.reason === 'duplicate') {
        logger.warn('Meeting already running, dropping duplicate message', { meetingId: safeParams.meeting_id });
      } else {
        logger.warn('Container rejected, capacity full, returning to queue', { meetingId: safeParams.meeting_id });
        await messageBroker.returnMeetingbotJobs(message);
      }

    } catch (error) {
      logger.error('Error spawning container', { meetingId: safeParams.meeting_id, error: error instanceof Error ? error.message : String(error) });
      // Docker error may be transient — return message to queue for retry
      logger.warn('Returning message to queue after error', { meetingId: safeParams.meeting_id });
      await messageBroker.returnMeetingbotJobs(message).catch((retErr) => {
        logger.error('Failed to return message to queue', { error: retErr instanceof Error ? retErr.message : String(retErr) });
      });
    }
  }

  private async handleReconnection(): Promise<void> {
    if (this.reconnectInterval) {
      return; // Already attempting to reconnect
    }

    logger.info('Attempting to reconnect to Redis');

    this.reconnectInterval = setTimeout(async () => {
      try {
        if (!messageBroker.isConnected()) {
          logger.info('Waiting for messageBroker to reconnect');
        }
      } catch (error) {
        logger.error('Error checking Redis connection', { error });
        // Schedule another reconnection attempt
        this.handleReconnection();
      } finally {
        this.reconnectInterval = null;
      }
    }, this.RECONNECT_DELAY);
  }

  // Getter methods
  get isRunning(): boolean {
    return this._isRunning;
  }

  isShutdownRequested(): boolean {
    return this._isShutdownRequested;
  }

  // Setter methods for controlled state changes
  private setShutdownRequested(value: boolean): void {
    this._isShutdownRequested = value;
  }
}
