import Docker from 'dockerode';
import config from '../config';
import { loggerFactory } from '../util/logger';

const logger = loggerFactory('system', 'container-manager');

export interface MeetingContainer {
  meetingId: string;
  meetingUrl: string;
  containerId: string;
  containerName: string;
  status: 'starting' | 'running' | 'completed' | 'error';
  startTime: Date;
  exitCode?: number;
  error?: string;
}

interface SpawnParams {
  meetingId: string;
  meetingUrl: string;
  botName: string;
  language?: string;
  force?: boolean;
}

export type SpawnRejectionReason = 'full' | 'duplicate';
export type SpawnResult =
  | { accepted: true }
  | { accepted: false; reason: SpawnRejectionReason };

export class ContainerManager {
  private docker: Docker;
  private meetings: Map<string, MeetingContainer> = new Map();
  private maxConcurrent: number;
  private cleanupTimer: NodeJS.Timeout | null = null;
  private _recoveryDone: boolean = false;
  private _recoveryPromise: Promise<void>;
  private static readonly CLEANUP_INTERVAL_MS = 60 * 60 * 1000; // 1 hour
  private static readonly MAX_CONTAINER_AGE_MS = 12 * 60 * 60 * 1000; // 12 hours — retain stopped containers for debugging
  private static readonly CONTAINER_LABEL = 'mia-meeting';

  constructor(maxConcurrent: number) {
    this.maxConcurrent = maxConcurrent;
    this.docker = new Docker({ socketPath: '/var/run/docker.sock' });

    // Start periodic cleanup
    this.cleanupTimer = setInterval(() => {
      this.cleanup().catch((err) => {
        logger.error('Cleanup error', { error: err instanceof Error ? err.message : String(err) });
      });
    }, ContainerManager.CLEANUP_INTERVAL_MS);

    // Recover orphaned containers — tracked via promise so spawnMeeting can wait
    this._recoveryPromise = this.recoverOrphansWithRetry()
      .then(() => { this._recoveryDone = true; });
  }

  /**
   * Spawn a new meeting container.
   * Returns { accepted: true } on success, or { accepted: false, reason } on rejection.
   */
  async spawnMeeting(params: SpawnParams): Promise<SpawnResult> {
    // Wait for orphan recovery to finish so meetings map reflects actual state
    if (!this._recoveryDone) {
      await this._recoveryPromise;
    }

    // Prevent duplicate meetings (unless force-replacing)
    if (this.meetings.has(params.meetingId)) {
      if (!params.force) {
        logger.warn('Meeting already running', { meetingId: params.meetingId });
        return { accepted: false, reason: 'duplicate' };
      }

      logger.info('Force-replacing existing meeting container', { meetingId: params.meetingId });
      const replaced = await this.replaceMeeting(params.meetingId);
      if (!replaced) {
        logger.error('Force-replace timed out, container still tracked', { meetingId: params.meetingId });
        return { accepted: false, reason: 'duplicate' };
      }
    }

    if (this.meetings.size >= this.maxConcurrent) {
      return { accepted: false, reason: 'full' };
    }

    const containerName = `mia-meeting-${params.meetingId.substring(0, 8)}-${Date.now()}`;
    let container: Docker.Container | undefined;

    // Reserve slot in map BEFORE any async work to prevent race conditions
    const meetingContainer: MeetingContainer = {
      meetingId: params.meetingId,
      meetingUrl: params.meetingUrl,
      containerId: '',  // filled after createContainer
      containerName,
      status: 'starting',
      startTime: new Date(),
    };
    this.meetings.set(params.meetingId, meetingContainer);

    try {
      // Build environment variables — pass all current env + meeting-specific overrides
      const env = this.buildEnv(params);

      container = await this.docker.createContainer({
        Image: config.meetingDockerImage,
        Cmd: ['node', 'dist/cli.js'],
        Env: env,
        name: containerName,
        Labels: {
          [ContainerManager.CONTAINER_LABEL]: 'true',
          'mia-meeting-id': params.meetingId,
        },
        HostConfig: {
          NetworkMode: 'host',
        },
      });

      meetingContainer.containerId = container.id;
      await container.start();
      meetingContainer.status = 'running';

      logger.info('Started container', { containerName, meetingId: params.meetingId });

      // Monitor in background — don't await
      this.monitorContainer(params.meetingId, container);

      return { accepted: true };
    } catch (err) {
      this.meetings.delete(params.meetingId);

      // Clean up created-but-not-started container to prevent leak
      if (container) {
        container.remove({ force: true }).catch((removeErr) => {
          logger.warn('Failed to remove leaked container', { containerName, error: removeErr instanceof Error ? removeErr.message : String(removeErr) });
        });
      }

      logger.error('Failed to spawn container', { meetingId: params.meetingId, error: err instanceof Error ? err.message : String(err) });
      throw err;
    }
  }

  /**
   * Monitor a container until it exits, then clean up.
   */
  private async monitorContainer(meetingId: string, container: Docker.Container): Promise<void> {
    try {
      const result = await container.wait();
      const meeting = this.meetings.get(meetingId);
      if (meeting) {
        meeting.status = result.StatusCode === 0 ? 'completed' : 'error';
        meeting.exitCode = result.StatusCode;
      }

      logger.info('Container exited, keeping stopped container for debugging', { meetingId, exitCode: result.StatusCode, cleanupAfterHours: ContainerManager.MAX_CONTAINER_AGE_MS / 3600000 });

      // Item 4: Do NOT remove container here — keep it stopped for log inspection.
      // The periodic cleanup() timer will remove containers older than MAX_CONTAINER_AGE_MS.
    } catch (err) {
      logger.error('Monitor error', { meetingId, error: err instanceof Error ? err.message : String(err) });
    } finally {
      // Free the meeting slot so new meetings can be spawned.
      // The Docker container stays in stopped state for debugging.
      this.meetings.delete(meetingId);
    }
  }

  /**
   * Stop an existing meeting container and wait for it to be removed from the tracking map.
   * Used by force-join to evict a stale/stuck container before spawning a new one.
   * Returns true if the slot was freed, false on timeout.
   */
  private async replaceMeeting(meetingId: string): Promise<boolean> {
    const stopped = await this.stopMeeting(meetingId);
    if (!stopped) {
      logger.warn('Could not stop existing container for replacement', { meetingId });
      // Container might not exist in Docker anymore but still tracked — wait for monitor cleanup
    }

    // Wait for monitorContainer's finally block to delete the Map entry
    const maxWaitMs = 15_000;
    const pollIntervalMs = 500;
    const deadline = Date.now() + maxWaitMs;

    while (this.meetings.has(meetingId) && Date.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, pollIntervalMs));
    }

    if (this.meetings.has(meetingId)) {
      // Last resort: force-remove from map (container stop was sent, monitor may be lagging)
      this.meetings.delete(meetingId);
      logger.warn('Force-removed stale map entry after stop', { meetingId });
    }

    return true;
  }

  /**
   * Build environment variables for the meeting container.
   * Passes all orchestrator env vars + meeting-specific overrides.
   */
  private buildEnv(params: SpawnParams): string[] {
    const env: Record<string, string> = {};

    // Copy all current process env vars (includes API keys, DB URLs, etc.)
    for (const [key, value] of Object.entries(process.env)) {
      if (value !== undefined) {
        env[key] = value;
      }
    }

    // Remove orchestrator-specific vars
    delete env['PORT'];
    delete env['MAX_CONCURRENT_MEETINGS'];
    delete env['MEETING_DOCKER_IMAGE'];
    delete env['REDIS_CONSUMER_ENABLED'];

    // Add meeting-specific vars
    env['MEETING_URL'] = params.meetingUrl;
    env['BOT_NAME'] = params.botName;
    env['MEETING_ID'] = params.meetingId;
    if (params.language) {
      env['MEETING_LANGUAGE'] = params.language;
    }

    return Object.entries(env).map(([k, v]) => `${k}=${v}`);
  }

  /**
   * Recover orphaned containers with retry.
   * Docker socket may not be ready at startup — retry up to 3 times with backoff.
   */
  private async recoverOrphansWithRetry(): Promise<void> {
    const MAX_RETRIES = 3;
    const RETRY_DELAY_MS = 3000;

    for (let attempt = 1; attempt <= MAX_RETRIES; attempt++) {
      try {
        await this.recoverOrphans();
        return; // success
      } catch (err) {
        logger.warn('Orphan recovery attempt failed', { attempt, maxRetries: MAX_RETRIES, error: err instanceof Error ? err.message : String(err) });
        if (attempt < MAX_RETRIES) {
          await new Promise((resolve) => setTimeout(resolve, RETRY_DELAY_MS * attempt));
        }
      }
    }

    logger.error('Orphan recovery failed after all retries, orphan containers may not be tracked');
  }

  /**
   * Recover orphaned containers from a previous crash.
   * Re-adds running containers to the meetings map and monitors them.
   */
  private async recoverOrphans(): Promise<void> {
    const containers = await this.docker.listContainers({
      filters: { label: [`${ContainerManager.CONTAINER_LABEL}=true`], status: ['running'] },
    });

    for (const containerInfo of containers) {
      const meetingId = containerInfo.Labels['mia-meeting-id'];
      if (meetingId && !this.meetings.has(meetingId)) {
        const container = this.docker.getContainer(containerInfo.Id);
        const meetingContainer: MeetingContainer = {
          meetingId,
          meetingUrl: '',
          containerId: containerInfo.Id,
          containerName: containerInfo.Names?.[0]?.replace('/', '') || containerInfo.Id.substring(0, 12),
          status: 'running',
          startTime: new Date(containerInfo.Created * 1000),
        };
        this.meetings.set(meetingId, meetingContainer);
        this.monitorContainer(meetingId, container);
        logger.info('Recovered orphan container', { meetingId });
      }
    }
  }

  /**
   * Clean up stopped containers older than 12 hours.
   */
  async cleanup(): Promise<void> {
    try {
      const containers = await this.docker.listContainers({
        all: true,
        filters: { label: [`${ContainerManager.CONTAINER_LABEL}=true`] },
      });

      const now = Date.now();
      let cleaned = 0;

      for (const containerInfo of containers) {
        const meetingId = containerInfo.Labels['mia-meeting-id'] || 'unknown';

        // P2 fix: Skip running containers — their lifecycle is managed by
        // watchdog (heartbeat timeout) and max_duration, not by cleanup.
        if (containerInfo.State === 'running') {
          continue;
        }

        // P1 fix: For stopped containers, measure retention from FinishedAt (exit time),
        // not Created (start time). A 3h meeting should still get 12h of retention after exit.
        let ageMs: number;
        try {
          const container = this.docker.getContainer(containerInfo.Id);
          const inspection = await container.inspect();
          const finishedAt = inspection.State?.FinishedAt;
          if (finishedAt && finishedAt !== '0001-01-01T00:00:00Z') {
            ageMs = now - new Date(finishedAt).getTime();
          } else {
            // Fallback to Created if FinishedAt is not available
            ageMs = now - containerInfo.Created * 1000;
          }
        } catch {
          // Inspect failed — fall back to Created timestamp
          ageMs = now - containerInfo.Created * 1000;
        }

        if (ageMs > ContainerManager.MAX_CONTAINER_AGE_MS) {
          const container = this.docker.getContainer(containerInfo.Id);
          await container.remove({ force: true }).catch(() => {});

          // Remove from tracking map (if still tracked)
          this.meetings.delete(meetingId);
          cleaned++;
          logger.info('Cleaned up stale container', { meetingId, stoppedHoursAgo: Math.round(ageMs / 3600000) });
        }
      }

      if (cleaned > 0) {
        logger.info('Cleanup complete', { removedContainers: cleaned });
      }
    } catch (err) {
      logger.error('Cleanup failed', { error: err instanceof Error ? err.message : String(err) });
    }
  }

  /** Check if all slots are full. */
  isFull(): boolean {
    return this.meetings.size >= this.maxConcurrent;
  }

  /** Get number of currently running meetings. */
  getRunningCount(): number {
    return this.meetings.size;
  }

  /** Get max concurrent meeting limit. */
  getMaxConcurrent(): number {
    return this.maxConcurrent;
  }

  /** Get info for a specific meeting. */
  getMeeting(meetingId: string): MeetingContainer | undefined {
    return this.meetings.get(meetingId);
  }

  /** List all active meetings. */
  listMeetings(): MeetingContainer[] {
    return Array.from(this.meetings.values());
  }

  /** Stop a specific meeting's container. */
  async stopMeeting(meetingId: string): Promise<boolean> {
    const meeting = this.meetings.get(meetingId);
    if (!meeting) return false;

    try {
      const container = this.docker.getContainer(meeting.containerId);
      await container.stop({ t: 10 });
      return true;
    } catch (err) {
      logger.error('Failed to stop meeting', { meetingId, error: err instanceof Error ? err.message : String(err) });
      return false;
    }
  }

  /** Check Docker daemon connectivity. */
  async isDockerAvailable(): Promise<boolean> {
    try {
      await this.docker.ping();
      return true;
    } catch {
      return false;
    }
  }

  /** Graceful shutdown — stop all meeting containers and clean up. */
  async shutdown(): Promise<void> {
    if (this.cleanupTimer) {
      clearInterval(this.cleanupTimer);
      this.cleanupTimer = null;
    }

    const meetingIds = Array.from(this.meetings.keys());
    if (meetingIds.length === 0) {
      logger.info('No active meetings, shutdown complete');
      return;
    }

    logger.info('Shutting down active meetings', { count: meetingIds.length });

    await Promise.allSettled(
      meetingIds.map((id) => this.stopMeeting(id))
    );

    // Wait for monitors to finish (container.wait() resolves after stop)
    // Timeout after 15s to avoid hanging forever
    const waitForEmpty = new Promise<void>((resolve) => {
      const check = () => {
        if (this.meetings.size === 0) return resolve();
        setTimeout(check, 500);
      };
      check();
    });
    const timeout = new Promise<void>((resolve) => setTimeout(resolve, 15000));
    await Promise.race([waitForEmpty, timeout]);

    logger.info('Shutdown complete', { remaining: this.meetings.size });
  }
}
