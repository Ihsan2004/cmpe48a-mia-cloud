import { EventEmitter } from 'stream';
import { createClient } from 'redis';
import config from '../config';
import { loggerFactory } from '../util/logger';

const logger = loggerFactory('system', 'redis-broker');

/**
 * This class must not have any instance/local state, and should only be used as a singleton.
 * Uses FIFO queue (RPUSH + BLPOP)
 */
export class RedisMessageBroker extends EventEmitter {
  private meetbot: ReturnType<typeof createClient> | null = null;

  constructor() {
    super();

    if (config.isRedisEnabled) {
      this.meetbot = createClient({
        url: config.redisUri,
        name: 'backend-meetbot',
      });
      this.meetbot.on('error', (err) =>
        logger.error('Redis client error', { error: err })
      );
      Promise.all([
        this.meetbot.connect(),
      ]).then(() => {
        logger.info('Redis message broker connected');
      });

      logger.info('Redis message broker initialized', { redisUri: config.redisUri });
    } else {
      logger.info('Redis message broker disabled - Redis is disabled');
    }
  }

  /**
   * Return a job to the head of the meetbot Redis queue.
   * **Client needs to actively pull items.**
   *
   *  This function returns a message to the head of the meetbot Redis queue
   *  This is useful when a job is rejected by the JobStore, and needs to be retried.
   *
   * @param {message} message - The publish message.
   */
  async returnMeetingbotJobs(message: string) {
    // Set/update data, even if the item is already in the queue.
    return await this.meetbot?.lPush(
      config.redisQueueName,
      message
    );
  }

  /**
   * Get a job from meetbot Redis queue with custom timeout.
   * **Client needs to actively pull items.**
   *
   * @param timeout - Timeout in seconds for blocking operation
   * @return The message acquired from meetbot Redis queue
   */
  async getMeetingbotJobsWithTimeout(timeout: number) {
    return await this.meetbot?.blPop(
      config.redisQueueName,
      timeout
    );
  }

  /**
   * Check if the Redis client is connected and ready
   * @return boolean indicating connection status
   */
  isConnected(): boolean {
    return this.meetbot?.isOpen ?? false;
  }

  /**
   * This function accompanies container shutdown and closes redis connection to free up server resources
   * @return void
   */
  async quitClientGracefully() {
    try {
      if (this.meetbot?.isOpen) {
        await this.meetbot?.quit();
      }
      logger.info('Closed Redis connection');
    } catch(quitError) {
      logger.error('Error while closing Redis connection', { error: quitError });
    }
  }
}
