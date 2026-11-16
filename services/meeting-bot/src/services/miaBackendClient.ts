import axios, { AxiosInstance } from 'axios';
import { Logger } from 'winston';
import config from '../config';

/**
 * MiaBackendClient — sends lifecycle signals to the Mia backend.
 *
 * Endpoints consumed:
 *   POST /api/meetings/:id/events   { event, reason?, metadata? }
 *   POST /api/meetings/:id/heartbeat
 *   POST /api/webhooks/bot-completion { meeting_id, meeting_url, status, duration, transcript, reason }
 */
export class MiaBackendClient {
  private http: AxiosInstance;
  private heartbeatTimer: ReturnType<typeof setInterval> | null = null;
  private meetingStartedAt: number | null = null;

  constructor(
    private meetingId: string,
    private meetingUrl: string,
    private logger: Logger,
  ) {
    this.http = axios.create({
      baseURL: config.miaBackendUrl,
      timeout: 10_000,
      headers: {
        'Content-Type': 'application/json',
        'x-bot-api-key': config.miaBotApiKey,
      },
    });
  }

  // ── Event signals ──────────────────────────────────────────────

  /** Bot admitted into the meeting → backend sets bot_status = 'recording' */
  async sendJoinedEvent(): Promise<void> {
    this.meetingStartedAt = Date.now();
    await this.postEvent('joined');
  }

  /** Bot could not join or crashed → backend sets bot_status = 'failed' */
  async sendFailedEvent(reason?: string): Promise<void> {
    await this.postEvent('failed', reason);
  }

  /** Meeting ended normally → backend sets bot_status = 'completed' + triggers post-meeting pipeline */
  async sendCompletedEvent(reason?: string): Promise<void> {
    const durationSec = this.meetingStartedAt
      ? Math.round((Date.now() - this.meetingStartedAt) / 1000)
      : 0;

    await this.postCompletion('completed', durationSec, reason);
  }

  // ── Usage reporting ──────────────────────────────────────────────

  /** Report STT usage metrics after meeting ends (fire-and-forget, non-fatal). */
  async sendSTTUsage(metrics: {
    audioDurationSeconds: number;
    bytesProcessed: number;
    segmentCount: number;
    reconnectCount: number;
  }): Promise<void> {
    try {
      await this.http.post(`/api/meetings/${this.meetingId}/stt-usage`, {
        provider: 'elevenlabs',
        model: 'scribe_v2',
        audio_duration_seconds: metrics.audioDurationSeconds,
        audio_bytes_processed: metrics.bytesProcessed,
        segment_count: metrics.segmentCount,
        reconnect_count: metrics.reconnectCount,
      });
      this.logger.info('Signal: STT usage reported', {
        meetingId: this.meetingId,
        audioDurationSeconds: metrics.audioDurationSeconds,
        segmentCount: metrics.segmentCount,
      });
    } catch (err) {
      this.logger.warn('Signal: failed to report STT usage (non-fatal)', {
        meetingId: this.meetingId,
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }

  // ── Heartbeat ──────────────────────────────────────────────────

  /** Start periodic heartbeat (default 30 s). Call after joined event. */
  startHeartbeat(): void {
    if (this.heartbeatTimer) return; // already running

    const intervalMs = config.miaHeartbeatIntervalMs;
    this.logger.info('Heartbeat: starting', { intervalMs, meetingId: this.meetingId });

    // Send first heartbeat immediately
    this.sendHeartbeat();

    this.heartbeatTimer = setInterval(() => {
      this.sendHeartbeat();
    }, intervalMs);
  }

  /** Stop heartbeat timer. Safe to call multiple times. */
  stopHeartbeat(): void {
    if (this.heartbeatTimer) {
      clearInterval(this.heartbeatTimer);
      this.heartbeatTimer = null;
      this.logger.info('Heartbeat: stopped', { meetingId: this.meetingId });
    }
  }

  // ── Internals ──────────────────────────────────────────────────

  private async sendHeartbeat(): Promise<void> {
    try {
      await this.http.post(`/api/meetings/${this.meetingId}/heartbeat`);
    } catch (err) {
      this.logger.warn('Heartbeat: delivery failed (non-fatal)', {
        meetingId: this.meetingId,
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }

  private async postEvent(event: string, reason?: string): Promise<void> {
    try {
      await this.http.post(`/api/meetings/${this.meetingId}/events`, {
        event,
        ...(reason ? { reason } : {}),
      });
      this.logger.info(`Signal: ${event} event sent`, { meetingId: this.meetingId, reason });
    } catch (err) {
      this.logger.error(`Signal: failed to send ${event} event`, {
        meetingId: this.meetingId,
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }

  private async postCompletion(status: string, duration: number, reason?: string): Promise<void> {
    try {
      await this.http.post('/api/webhooks/bot-completion', {
        meeting_id: this.meetingId,
        meeting_url: this.meetingUrl,
        status,
        duration,
        transcript: config.transcriptWriteEnabled,
        reason: reason || 'meeting_ended',
      });
      this.logger.info(`Signal: completion sent`, {
        meetingId: this.meetingId,
        status,
        duration,
        reason,
      });
    } catch (err) {
      this.logger.error('Signal: failed to send completion', {
        meetingId: this.meetingId,
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }
}

/**
 * Factory — returns MiaBackendClient if signals are enabled,
 * otherwise returns null (no-op pattern).
 */
export function createMiaBackendClient(
  meetingId: string,
  meetingUrl: string,
  logger: Logger,
): MiaBackendClient | null {
  if (!config.miaBackendSignalsEnabled) {
    logger.info('MiaBackendClient: signals disabled via config');
    return null;
  }
  if (!config.miaBackendUrl) {
    logger.warn('MiaBackendClient: MIA_BACKEND_URL not set — signals disabled');
    return null;
  }
  return new MiaBackendClient(meetingId, meetingUrl, logger);
}
