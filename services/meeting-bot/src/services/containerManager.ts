import { ChildProcess, spawn } from 'child_process';
import fs from 'fs';
import path from 'path';
import { loggerFactory } from '../util/logger';

const logger = loggerFactory('system', 'process-manager');

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

/**
 * Manages meeting bot worker execution inside the current container/VM using
 * detached child process groups (`node dist/cli.js`), removing the legacy
 * Docker-out-of-Docker (`/var/run/docker.sock`) dependency.
 */
export class ContainerManager {
  private meetings: Map<string, MeetingContainer> = new Map();
  private processes: Map<string, ChildProcess> = new Map();
  private pendingIds: Set<string> = new Set();
  private maxConcurrent: number;

  constructor(maxConcurrent: number) {
    this.maxConcurrent = Math.max(1, maxConcurrent || 1);
  }

  private getCliEntryPath(): string {
    return path.resolve(__dirname, '..', 'cli.js');
  }

  private getBotRootDir(): string {
    return path.resolve(__dirname, '..', '..');
  }

  /**
   * Spawn a meeting worker process inside the container/VM.
   * Uses `pendingIds` to serialize duplicate / force-replace operations for a meetingId.
   */
  async spawnMeeting(params: SpawnParams): Promise<SpawnResult> {
    if (this.pendingIds.has(params.meetingId)) {
      logger.warn('Meeting transition already in progress', { meetingId: params.meetingId });
      return { accepted: false, reason: 'duplicate' };
    }

    this.pendingIds.add(params.meetingId);
    try {
      if (this.meetings.has(params.meetingId)) {
        if (!params.force) {
          logger.warn('Meeting already running', { meetingId: params.meetingId });
          return { accepted: false, reason: 'duplicate' };
        }

        logger.info('Force-replacing existing meeting process', { meetingId: params.meetingId });
        const replaced = await this.replaceMeeting(params.meetingId);
        if (!replaced) {
          logger.error('Force-replace timed out, process still active', { meetingId: params.meetingId });
          return { accepted: false, reason: 'duplicate' };
        }
      }

      if (this.meetings.size >= this.maxConcurrent) {
        return { accepted: false, reason: 'full' };
      }

      const processName = `mia-meeting-${params.meetingId.substring(0, 8)}-${Date.now()}`;
      const meetingEntry: MeetingContainer = {
        meetingId: params.meetingId,
        meetingUrl: params.meetingUrl,
        containerId: '',
        containerName: processName,
        status: 'starting',
        startTime: new Date(),
      };
      this.meetings.set(params.meetingId, meetingEntry);

      try {
        const cliEntry = this.getCliEntryPath();
        const env = this.buildEnv(params);

        // detached: true puts node dist/cli.js + Chromium/ffmpeg/parec in one process group
        const child = spawn(process.execPath, [cliEntry], {
          env,
          cwd: this.getBotRootDir(),
          detached: true,
          stdio: 'inherit',
        });

        if (!child.pid) {
          throw new Error('Failed to obtain child PID for meeting worker');
        }

        meetingEntry.containerId = String(child.pid);
        meetingEntry.status = 'running';
        this.processes.set(params.meetingId, child);

        logger.info('Started in-container meeting worker', {
          processName,
          pid: child.pid,
          meetingId: params.meetingId,
        });

        this.monitorProcess(params.meetingId, child);
        return { accepted: true };
      } catch (err) {
        this.meetings.delete(params.meetingId);
        this.processes.delete(params.meetingId);
        logger.error('Failed to spawn meeting worker', {
          meetingId: params.meetingId,
          error: err instanceof Error ? err.message : String(err),
        });
        throw err;
      }
    } finally {
      this.pendingIds.delete(params.meetingId);
    }
  }

  private monitorProcess(meetingId: string, child: ChildProcess): void {
    child.once('exit', (code, signal) => {
      const meeting = this.meetings.get(meetingId);
      if (meeting) {
        meeting.status = code === 0 ? 'completed' : 'error';
        meeting.exitCode = code ?? undefined;
      }
      logger.info('Meeting worker exited', { meetingId, exitCode: code, signal });
      this.processes.delete(meetingId);
      this.meetings.delete(meetingId);
    });

    child.once('error', (err) => {
      logger.error('Meeting worker process error', {
        meetingId,
        error: err instanceof Error ? err.message : String(err),
      });
      this.processes.delete(meetingId);
      this.meetings.delete(meetingId);
    });
  }

  private async replaceMeeting(meetingId: string): Promise<boolean> {
    await this.stopMeeting(meetingId);

    const deadline = Date.now() + 15_000;
    while (this.meetings.has(meetingId) && Date.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, 250));
    }

    return !this.meetings.has(meetingId);
  }

  private buildEnv(params: SpawnParams): NodeJS.ProcessEnv {
    const env: NodeJS.ProcessEnv = { ...process.env };

    delete env.PORT;
    delete env.MAX_CONCURRENT_MEETINGS;
    delete env.MEETING_DOCKER_IMAGE;
    delete env.REDIS_CONSUMER_ENABLED;

    env.MEETING_URL = params.meetingUrl;
    env.BOT_NAME = params.botName;
    env.MEETING_ID = params.meetingId;
    if (params.language) {
      env.MEETING_LANGUAGE = params.language;
    }

    return env;
  }

  isFull(): boolean {
    return this.meetings.size >= this.maxConcurrent;
  }

  getRunningCount(): number {
    return this.meetings.size;
  }

  getMaxConcurrent(): number {
    return this.maxConcurrent;
  }

  getMeeting(meetingId: string): MeetingContainer | undefined {
    return this.meetings.get(meetingId);
  }

  listMeetings(): MeetingContainer[] {
    return Array.from(this.meetings.values());
  }

  /** Check whether any process remains alive in the given process group. */
  private isProcessGroupAlive(pgid: number): boolean {
    try {
      process.kill(-pgid, 0);
      return true;
    } catch {
      return false;
    }
  }

  /** Stop a meeting worker and its entire process group (Chromium/ffmpeg/parec). */
  async stopMeeting(meetingId: string): Promise<boolean> {
    const child = this.processes.get(meetingId);
    if (!child || !child.pid) {
      this.meetings.delete(meetingId);
      return false;
    }

    const pid = child.pid;
    try {
      process.kill(-pid, 'SIGTERM');
    } catch {
      try {
        child.kill('SIGTERM');
      } catch {
        // Process already exited
      }
    }

    // Wait up to 5s for the whole process group (not just leader) to exit
    const deadline = Date.now() + 5000;
    while (Date.now() < deadline) {
      if (child.exitCode !== null && !this.isProcessGroupAlive(pid)) {
        break;
      }
      await new Promise((resolve) => setTimeout(resolve, 200));
    }

    // Force-kill any surviving Chromium/ffmpeg descendants in the group
    if (this.isProcessGroupAlive(pid) || child.exitCode === null) {
      try {
        process.kill(-pid, 'SIGKILL');
      } catch {
        try {
          child.kill('SIGKILL');
        } catch {
          // Process already exited
        }
      }
    }

    return true;
  }

  /** Readiness check: verify CLI entrypoint exists (and display socket on Linux when DISPLAY is set). */
  async isReady(): Promise<boolean> {
    if (!fs.existsSync(this.getCliEntryPath())) {
      return false;
    }
    const display = process.env.DISPLAY;
    if (process.platform === 'linux' && display && display.startsWith(':')) {
      const displayNum = display.slice(1).split('.')[0];
      if (!fs.existsSync(`/tmp/.X11-unix/X${displayNum}`)) {
        return false;
      }
    }
    return true;
  }

  /** Graceful shutdown — terminate all active meeting worker process groups. */
  async shutdown(): Promise<void> {
    const meetingIds = Array.from(this.meetings.keys());
    if (meetingIds.length === 0) {
      logger.info('No active meetings, shutdown complete');
      return;
    }

    logger.info('Shutting down active meeting workers', { count: meetingIds.length });
    await Promise.allSettled(meetingIds.map((id) => this.stopMeeting(id)));
    logger.info('Shutdown complete', { remaining: this.meetings.size });
  }
}
