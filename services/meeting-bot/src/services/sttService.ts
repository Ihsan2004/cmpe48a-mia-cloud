import WebSocket from 'ws';
import { Logger } from 'winston';
import { PcmChunkHandler } from './pcmStreamService';

// --- Types ---

export interface TranscriptWord {
  text: string;
  start: number;   // seconds from session start
  end: number;
  type: 'word' | 'spacing';
}

export interface TranscriptSegment {
  text: string;
  start_time: number;   // seconds from meeting start (adjusted)
  end_time: number;
  language: string;
  stt_provider: 'elevenlabs';
  /** Speaker name resolved by ActiveSpeakerService (Faz 6.4). Defaults to 'Speaker'. */
  speaker?: string;
  metadata: {
    language: string;
    words?: TranscriptWord[];
    /** How speaker was determined: 'dom_detection' | 'unknown' */
    speaker_source?: string;
  };
}

// --- Session Manager (reconnect timestamp consistency) ---

class SttSessionManager {
  private cumulativeOffset: number = 0;
  private lastConfirmedEndTime: number = 0;
  private sessionPcmBytesSent: number = 0;

  onDisconnect(): void {
    // New session's timestamp offset = total PCM duration sent so far
    this.cumulativeOffset += this.sessionPcmBytesSent / (16000 * 2); // bytes -> seconds
    this.sessionPcmBytesSent = 0;
  }

  trackBytesSent(bytes: number): void {
    this.sessionPcmBytesSent += bytes;
  }

  adjustTimestamp(rawTimestamp: number): number {
    return rawTimestamp + this.cumulativeOffset;
  }

  isDuplicate(adjustedStart: number): boolean {
    return adjustedStart < this.lastConfirmedEndTime;
  }

  confirmSegment(adjustedEndTime: number): void {
    this.lastConfirmedEndTime = adjustedEndTime;
  }

  /** Total audio duration sent across all sessions (seconds). */
  getTotalAudioDurationSeconds(): number {
    return this.cumulativeOffset + this.sessionPcmBytesSent / (16000 * 2);
  }

  /** Total PCM bytes sent across all sessions (cumulative offset converted back + current session). */
  getTotalBytesProcessed(): number {
    // cumulativeOffset is in seconds; convert back to bytes for past sessions
    return Math.round(this.cumulativeOffset * 16000 * 2) + this.sessionPcmBytesSent;
  }
}

// --- ElevenLabs STT Service ---

export interface ElevenLabsSttConfig {
  apiKey: string;
  language?: string;
  modelId?: string;
  region?: string;
  commitStrategy?: string;
  onSegment: (segment: TranscriptSegment) => void;
  onPartial?: (text: string) => void;
}

export class ElevenLabsSttService implements PcmChunkHandler {
  private ws: WebSocket | null = null;
  private sessionManager = new SttSessionManager();
  private logger: Logger;
  private config: Required<Pick<ElevenLabsSttConfig, 'apiKey' | 'modelId' | 'region' | 'commitStrategy'>> & ElevenLabsSttConfig;
  private onSegment: (segment: TranscriptSegment) => void;
  private onPartial?: (text: string) => void;

  // Reconnection state
  private reconnectAttempts = 0;
  private maxReconnectAttempts = 5;
  private reconnectTimer: NodeJS.Timeout | null = null;
  private disabled = false;
  private connected = false;
  private sessionStarted = false;

  // Replay buffer: last 500ms of PCM for reconnect
  private replayBuffer: Buffer[] = [];
  private replayBufferMaxBytes = 16000; // 500ms @ 16kHz 16-bit mono = 16000 bytes
  private replayBufferBytes = 0;

  // Drain state
  private drainResolve: (() => void) | null = null;
  private pendingCommit = false;

  // First audio callback — used to sync ActiveSpeakerService time origin
  private firstAudioSent = false;
  private firstAudioEpochMs: number = 0;
  private onFirstAudioSentCallback: ((epochMs: number) => void) | null = null;

  constructor(logger: Logger, sttConfig: ElevenLabsSttConfig) {
    this.logger = logger;
    this.config = {
      ...sttConfig,
      modelId: sttConfig.modelId || 'scribe_v2_realtime',
      region: sttConfig.region || '',
      commitStrategy: sttConfig.commitStrategy || 'manual',
    };
    this.onSegment = sttConfig.onSegment;
    this.onPartial = sttConfig.onPartial;
  }

  /**
   * Register a callback that fires once when the first PCM chunk is sent to ElevenLabs.
   * Used to sync ActiveSpeakerService time origin with ElevenLabs audio t=0.
   */
  onFirstAudioSent(callback: (epochMs: number) => void): void {
    if (this.firstAudioSent) {
      // Audio already started — fire immediately with the recorded epoch
      callback(this.firstAudioEpochMs);
      return;
    }
    this.onFirstAudioSentCallback = callback;
  }

  // --- PcmChunkHandler interface ---

  onPcmChunk(chunk: Buffer): void {
    if (this.disabled || !this.ws || this.ws.readyState !== WebSocket.OPEN || !this.sessionStarted) return;

    // Fire first-audio callback to sync time origins
    if (!this.firstAudioSent) {
      this.firstAudioSent = true;
      this.firstAudioEpochMs = Date.now();
      if (this.onFirstAudioSentCallback) {
        this.onFirstAudioSentCallback(this.firstAudioEpochMs);
      }
    }

    // Update replay buffer (circular, keep last ~500ms)
    this.replayBuffer.push(chunk);
    this.replayBufferBytes += chunk.length;
    while (this.replayBufferBytes > this.replayBufferMaxBytes && this.replayBuffer.length > 1) {
      const removed = this.replayBuffer.shift()!;
      this.replayBufferBytes -= removed.length;
    }

    // Track bytes for timestamp offset calculation
    this.sessionManager.trackBytesSent(chunk.length);

    // Send as base64 JSON message (Scribe v2 Realtime protocol)
    const message = JSON.stringify({
      message_type: 'input_audio_chunk',
      audio_base_64: chunk.toString('base64'),
      commit: false,
    });

    try {
      this.ws.send(message);
    } catch (err) {
      this.logger.error('ElevenLabs STT: failed to send PCM chunk', {
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }

  onStreamEnd(): void {
    // PCM stream ended — lifecycle (drain + disconnect) is managed by the bot
    this.logger.info('ElevenLabs STT: PCM stream ended');
  }

  onUtteranceEnd(): void {
    // Called by VAD (Faz 4) when silence detected — trigger commit
    if (this.disabled || !this.ws || this.ws.readyState !== WebSocket.OPEN) return;

    const commitMsg = JSON.stringify({
      message_type: 'input_audio_chunk',
      audio_base_64: '',
      commit: true,
    });

    try {
      this.ws.send(commitMsg);
    } catch (err) {
      this.logger.error('ElevenLabs STT: failed to send commit', {
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }

  // --- Lifecycle ---

  async connect(): Promise<void> {
    if (this.disabled) return;

    const wsUrl = this.buildWsUrl();
    this.logger.info('ElevenLabs STT: connecting', { url: wsUrl.replace(/xi-api-key=[^&]+/, 'xi-api-key=***') });

    return new Promise<void>((resolve, reject) => {
      let settled = false;
      const settle = (fn: () => void) => {
        if (!settled) {
          settled = true;
          fn();
        }
      };

      try {
        this.ws = new WebSocket(wsUrl, {
          headers: {
            'xi-api-key': this.config.apiKey,
          },
        });

        const connectTimeout = setTimeout(() => {
          this.logger.error('ElevenLabs STT: connection timeout');
          this.ws?.close();
          settle(() => reject(new Error('WebSocket connection timeout')));
        }, 10000);

        this.ws.on('open', () => {
          this.connected = true;
          clearTimeout(connectTimeout);
          this.logger.info('ElevenLabs STT: WebSocket connected, waiting for session_started');
          // Don't resolve yet — wait for session_started
        });

        this.ws.on('message', (data: WebSocket.Data) => {
          try {
            const raw = data.toString();
            this.handleMessage(raw, () => settle(() => resolve()));
          } catch (err) {
            this.logger.error('ElevenLabs STT: message parse error', {
              error: err instanceof Error ? err.message : String(err),
            });
          }
        });

        this.ws.on('close', (code, reason) => {
          const wasConnected = this.connected;
          this.connected = false;
          this.sessionStarted = false;
          this.logger.info('ElevenLabs STT: WebSocket closed', { code, reason: reason.toString() });

          // If we haven't resolved/rejected yet (e.g. close before session_started), reject
          clearTimeout(connectTimeout);
          settle(() => reject(new Error(`WebSocket closed before session_started (code: ${code})`)));

          if (wasConnected && !this.disabled) {
            this.sessionManager.onDisconnect();
            this.scheduleReconnect();
          }
        });

        this.ws.on('error', (err) => {
          this.logger.error('ElevenLabs STT: WebSocket error', {
            error: err.message,
          });
          clearTimeout(connectTimeout);
          settle(() => reject(err));
        });

      } catch (err) {
        settle(() => reject(err));
      }
    });
  }

  disconnect(): void {
    this.disabled = true;
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.ws) {
      try {
        if (this.ws.readyState === WebSocket.OPEN) {
          // Send end_of_stream before closing
          this.ws.send(JSON.stringify({ message_type: 'end_of_stream' }));
        }
        this.ws.close();
      } catch (err) {
        // Ignore close errors
      }
      this.ws = null;
    }
    this.connected = false;
    this.sessionStarted = false;
    this.logger.info('ElevenLabs STT: disconnected');
  }

  async drain(timeoutMs: number): Promise<void> {
    if (this.disabled || !this.ws || this.ws.readyState !== WebSocket.OPEN) return;

    // Send final commit to flush remaining audio
    const commitMsg = JSON.stringify({
      message_type: 'input_audio_chunk',
      audio_base_64: '',
      commit: true,
    });

    try {
      this.ws.send(commitMsg);
      this.pendingCommit = true;
    } catch (err) {
      return;
    }

    // Wait for the committed transcript or timeout
    return new Promise<void>((resolve) => {
      this.drainResolve = resolve;

      setTimeout(() => {
        if (this.drainResolve) {
          this.logger.info('ElevenLabs STT: drain timeout, proceeding with available segments');
          this.drainResolve = null;
          resolve();
        }
      }, timeoutMs);
    });
  }

  isDisabled(): boolean {
    return this.disabled;
  }

  /** Returns accumulated usage metrics for reporting to backend. */
  getUsageMetrics(): { audioDurationSeconds: number; bytesProcessed: number; reconnectCount: number } {
    return {
      audioDurationSeconds: this.sessionManager.getTotalAudioDurationSeconds(),
      bytesProcessed: this.sessionManager.getTotalBytesProcessed(),
      reconnectCount: this.reconnectAttempts,
    };
  }

  // --- Private methods ---

  private buildWsUrl(): string {
    const baseUrls: Record<string, string> = {
      '': 'wss://api.elevenlabs.io',
      'us': 'wss://api.us.elevenlabs.io',
      'eu': 'wss://api.eu.residency.elevenlabs.io',
      'in': 'wss://api.in.residency.elevenlabs.io',
    };
    const base = baseUrls[this.config.region] || baseUrls[''];
    const params = new URLSearchParams({
      model_id: this.config.modelId,
      audio_format: 'pcm_16000',
      include_timestamps: 'true',
      commit_strategy: this.config.commitStrategy,
    });
    if (this.config.language) {
      params.set('language_code', this.config.language);
    }
    return `${base}/v1/speech-to-text/realtime?${params}`;
  }

  private handleMessage(raw: string, initialResolve?: (value: void) => void): void {
    const msg = JSON.parse(raw);

    switch (msg.message_type) {
      case 'session_started':
        this.sessionStarted = true;
        // Reset reconnect counter only after full handshake (session_started), not on open
        this.reconnectAttempts = 0;
        this.logger.info('ElevenLabs STT: session started', { sessionId: msg.session_id });
        // Replay buffered PCM chunks after reconnect
        this.replayBufferedChunks();
        // Resolve initial connect() promise
        if (initialResolve) {
          initialResolve();
        }
        break;

      case 'partial_transcript':
        if (msg.text) {
          this.onPartial?.(msg.text);
        }
        break;

      case 'committed_transcript_with_timestamps':
        this.handleCommittedTranscript(msg);
        // If we're draining and got a committed transcript, resolve drain
        if (this.pendingCommit && this.drainResolve) {
          this.pendingCommit = false;
          this.drainResolve();
          this.drainResolve = null;
        }
        break;

      case 'committed_transcript':
        // Without timestamps — shouldn't happen since include_timestamps=true
        if (this.pendingCommit && this.drainResolve) {
          this.pendingCommit = false;
          this.drainResolve();
          this.drainResolve = null;
        }
        break;

      case 'error':
      case 'auth_error':
      case 'quota_exceeded':
      case 'rate_limited':
      case 'queue_overflow':
      case 'resource_exhausted':
      case 'session_time_limit_exceeded':
      case 'transcriber_error':
        this.logger.error('ElevenLabs STT error', {
          type: msg.message_type,
          error: msg.error || msg.message,
        });
        if (msg.message_type === 'auth_error' || msg.message_type === 'quota_exceeded') {
          this.disableAndWarn();
        }
        break;

      default:
        this.logger.debug('ElevenLabs STT: unknown message type', { type: msg.message_type });
        break;
    }
  }

  // Known STT hallucination patterns (YouTube/platform training data artifacts).
  // STT engines trained on YouTube produce these during silence.
  // Drop these BEFORE sending to backend — prevents:
  //   1. Noise in transcript_segments table
  //   2. Summary trigger deferral (quiet period reset by fake segments)
  private static readonly STT_HALLUCINATION_PATTERNS = [
    'kanalımıza abone',
    'abone olmayı unutmayın',
    'beğenmeyi ve abone',
    'beğen ve abone ol',
    'kanalımızı takip',
    'like and subscribe',
    'subscribe to',
    "don't forget to subscribe",
    'thanks for watching',
    'izlediğiniz için teşekkür',
  ];

  private isSTTHallucination(text: string): boolean {
    const lower = text.toLowerCase().trim();
    return ElevenLabsSttService.STT_HALLUCINATION_PATTERNS.some((p: string) => lower.includes(p));
  }

  private handleCommittedTranscript(msg: {
    text: string;
    language_code: string;
    words?: TranscriptWord[];
  }): void {
    if (!msg.words || msg.words.length === 0) return;

    const wordItems = msg.words.filter(w => w.type === 'word');
    if (wordItems.length === 0) return;

    // Drop STT hallucinations before they reach backend
    if (this.isSTTHallucination(msg.text)) {
      this.logger.warn('ElevenLabs STT: dropped hallucination segment', {
        text: msg.text.substring(0, 80),
      });
      return;
    }

    const rawStart = wordItems[0].start;
    const rawEnd = wordItems[wordItems.length - 1].end;

    // Apply session offset for reconnect consistency
    const adjustedStart = this.sessionManager.adjustTimestamp(rawStart);
    const adjustedEnd = this.sessionManager.adjustTimestamp(rawEnd);

    // Duplicate check (replay-caused overlap)
    if (this.sessionManager.isDuplicate(adjustedStart)) {
      this.logger.debug('ElevenLabs STT: skipping duplicate segment', { adjustedStart, text: msg.text.substring(0, 50) });
      return;
    }
    this.sessionManager.confirmSegment(adjustedEnd);

    const segment: TranscriptSegment = {
      text: msg.text,
      start_time: adjustedStart,
      end_time: adjustedEnd,
      language: msg.language_code,
      stt_provider: 'elevenlabs',
      metadata: {
        language: msg.language_code,
        words: msg.words,
      },
    };

    this.onSegment(segment);
  }

  private replayBufferedChunks(): void {
    if (this.replayBuffer.length === 0) return;

    this.logger.info('ElevenLabs STT: replaying buffered PCM chunks', {
      chunks: this.replayBuffer.length,
      bytes: this.replayBufferBytes,
    });

    for (const chunk of this.replayBuffer) {
      if (!this.ws || this.ws.readyState !== WebSocket.OPEN) break;

      // Don't track replayed bytes — they were already tracked in the previous session
      const message = JSON.stringify({
        message_type: 'input_audio_chunk',
        audio_base_64: chunk.toString('base64'),
        commit: false,
      });

      try {
        this.ws.send(message);
      } catch (err) {
        this.logger.error('ElevenLabs STT: replay chunk send failed', {
          error: err instanceof Error ? err.message : String(err),
        });
        break;
      }
    }

    // Clear replay buffer after replaying
    this.replayBuffer = [];
    this.replayBufferBytes = 0;
  }

  private scheduleReconnect(): void {
    if (this.disabled) return;

    this.reconnectAttempts++;
    if (this.reconnectAttempts > this.maxReconnectAttempts) {
      this.disableAndWarn();
      return;
    }

    // Exponential backoff: 1s, 2s, 4s, 8s, 16s (max 30s)
    const delay = Math.min(1000 * Math.pow(2, this.reconnectAttempts - 1), 30000);
    this.logger.info('ElevenLabs STT: scheduling reconnect', {
      attempt: this.reconnectAttempts,
      maxAttempts: this.maxReconnectAttempts,
      delayMs: delay,
    });

    this.reconnectTimer = setTimeout(async () => {
      try {
        await this.connect();
      } catch (err) {
        this.logger.error('ElevenLabs STT: reconnect failed', {
          attempt: this.reconnectAttempts,
          error: err instanceof Error ? err.message : String(err),
        });
        // scheduleReconnect will be called again from the close handler
      }
    }, delay);
  }

  private disableAndWarn(): void {
    this.disabled = true;
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    // Close the WebSocket to prevent resource leaks
    if (this.ws) {
      try {
        this.ws.close();
      } catch (_) {
        // Ignore close errors
      }
      this.ws = null;
    }
    this.connected = false;
    this.sessionStarted = false;
    this.logger.warn('ElevenLabs STT: DISABLED — unrecoverable error or max reconnect attempts reached. Archive recording continues.');
  }
}
