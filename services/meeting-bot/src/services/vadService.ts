import { Logger } from 'winston';
import * as path from 'path';
import * as fs from 'fs';
import { PcmChunkHandler } from './pcmStreamService';

// WebRTC VAD — fast, lightweight C library (default export)
// eslint-disable-next-line @typescript-eslint/no-var-requires
const WebRtcVad = require('webrtcvad').default || require('webrtcvad');

interface VadConfig {
  webrtcAggressiveness: 0 | 1 | 2 | 3;
  sileroThreshold: number;
  utteranceTailMs: number;
  pendingFrameLookback: boolean;
}

interface VadStats {
  totalFrames: number;
  speechFrames: number;
  silenceFrames: number;
  sileroChecks: number;
  falsePositivesCaught: number;
}

const VAD_DEFAULTS: VadConfig = {
  webrtcAggressiveness: 2,
  sileroThreshold: 0.5,
  utteranceTailMs: 600,
  pendingFrameLookback: true,
};

// 20ms frame @ 16kHz, 16-bit mono = 320 samples * 2 bytes = 640 bytes
const FRAME_SIZE = 640;
const FRAME_SAMPLES = 320;
const FRAME_MS = 20;
const SAMPLE_RATE = 16000;

// Silero expects 1536 samples per frame (96ms at 16kHz)
const SILERO_FRAME_SAMPLES = 1536;
const FRAMES_FOR_SILERO = Math.ceil(SILERO_FRAME_SAMPLES / FRAME_SAMPLES); // 5 frames

/**
 * Hybrid Voice Activity Detection service.
 *
 * WebRTC VAD runs on every frame (fast, ~85% accuracy).
 * Silero VAD runs only on silence→speech transitions (slow but ~97% accuracy).
 *
 * Pipeline: PcmStreamService → VadService → ElevenLabsSttService
 * Only speech frames are forwarded to STT — silence is filtered.
 */
export class VadService implements PcmChunkHandler {
  private webrtcVad: any;
  private sileroVad: any | null = null;
  private readonly sttHandler: PcmChunkHandler;
  private readonly logger: Logger;
  private readonly meetingId: string;
  private readonly config: VadConfig;

  // State
  private lastIsSpeech = false;
  private silenceFrameCount = 0;
  private pendingFrame: Buffer | null = null;

  // Ring buffer for Silero confirmation (needs 1536 samples = ~5 WebRTC frames)
  private recentFrames: Buffer[] = [];

  // Async frame queue (Silero is async, onPcmChunk is sync)
  private frameQueue: Buffer[] = [];
  private carryBuffer: Buffer = Buffer.alloc(0);
  private processing = false;

  // Stats
  private stats: VadStats = {
    totalFrames: 0,
    speechFrames: 0,
    silenceFrames: 0,
    sileroChecks: 0,
    falsePositivesCaught: 0,
  };

  constructor(
    meetingId: string,
    sttHandler: PcmChunkHandler,
    logger: Logger,
    config?: Partial<VadConfig>,
  ) {
    this.meetingId = meetingId;
    this.sttHandler = sttHandler;
    this.logger = logger;
    this.config = { ...VAD_DEFAULTS, ...config };

    // Initialize WebRTC VAD (synchronous, C binding)
    // Constructor: new VAD(sampleRate, aggressivenessLevel)
    this.webrtcVad = new WebRtcVad(SAMPLE_RATE, this.config.webrtcAggressiveness);
  }

  /**
   * Load Silero ONNX model. Must be called before processing starts.
   * First call takes ~200ms (model load), subsequent calls are instant.
   */
  async initialize(): Promise<void> {
    try {
      // eslint-disable-next-line @typescript-eslint/no-var-requires
      const ort = require('onnxruntime-node');
      // Silero class is not exported from the main package, access internal module
      // eslint-disable-next-line @typescript-eslint/no-var-requires
      const { Silero } = require('@ricky0123/vad-node/dist/_common/models');
      const modelPath = path.resolve(
        path.dirname(require.resolve('@ricky0123/vad-node')),
        'silero_vad.onnx',
      );
      const modelFetcher = () => fs.readFileSync(modelPath);
      this.sileroVad = await Silero.new(ort, modelFetcher);
      this.logger.info('VadService: Silero model loaded', { meetingId: this.meetingId });
    } catch (err) {
      this.logger.warn('VadService: Silero load failed, using WebRTC VAD only', {
        meetingId: this.meetingId,
        error: err instanceof Error ? err.message : String(err),
      });
      // Fallback: WebRTC-only mode (no Silero confirmation)
      this.sileroVad = null;
    }

    this.logger.info('VadService: initialized', {
      meetingId: this.meetingId,
      webrtcAggressiveness: this.config.webrtcAggressiveness,
      sileroAvailable: this.sileroVad !== null,
      utteranceTailMs: this.config.utteranceTailMs,
      pendingFrameLookback: this.config.pendingFrameLookback,
    });
  }

  // --- PcmChunkHandler interface ---

  onPcmChunk(chunk: Buffer): void {
    // Split 200ms chunk into 20ms frames
    let offset = 0;

    while (offset + FRAME_SIZE <= chunk.length) {
      this.frameQueue.push(chunk.subarray(offset, offset + FRAME_SIZE));
      offset += FRAME_SIZE;
    }

    // Handle remainder bytes (carry buffer)
    if (offset < chunk.length) {
      this.carryBuffer = Buffer.concat([this.carryBuffer, chunk.subarray(offset)]);
      while (this.carryBuffer.length >= FRAME_SIZE) {
        this.frameQueue.push(this.carryBuffer.subarray(0, FRAME_SIZE));
        this.carryBuffer = this.carryBuffer.subarray(FRAME_SIZE);
      }
    }

    // Process queue (non-blocking)
    this.processQueue();
  }

  onStreamEnd(): void {
    this.sttHandler.onStreamEnd();
    this.logStats();
  }

  /**
   * Flush remaining frames in queue before STT drain.
   * Call this BEFORE sttService.drain() in shutdown sequence.
   */
  async flush(): Promise<void> {
    // Wait for any in-flight processing to finish
    while (this.processing) {
      await new Promise(r => setTimeout(r, 10));
    }
    // Process remaining frames
    if (this.frameQueue.length > 0) {
      await this.processQueue();
    }
    this.logger.info('VadService: queue flushed', {
      meetingId: this.meetingId,
      remainingFrames: this.frameQueue.length,
    });
  }

  // --- Internal processing ---

  private async processQueue(): Promise<void> {
    if (this.processing) return;
    this.processing = true;
    try {
      while (this.frameQueue.length > 0) {
        const frame = this.frameQueue.shift()!;
        await this.processFrame(frame);
      }
    } finally {
      this.processing = false;
    }
  }

  private async processFrame(frame: Buffer): Promise<void> {
    this.stats.totalFrames++;

    // WebRTC VAD: fast check on every frame
    let webrtcSpeech: boolean;
    try {
      webrtcSpeech = this.webrtcVad.process(frame);
    } catch {
      // If WebRTC VAD fails, pass through to STT (safe fallback)
      this.sttHandler.onPcmChunk(frame);
      return;
    }

    if (!webrtcSpeech) {
      // Silence detected
      this.stats.silenceFrames++;
      this.silenceFrameCount++;

      // Pending frame trick: keep this frame in case next one is speech
      if (this.config.pendingFrameLookback) {
        this.pendingFrame = frame;
      }

      // Utterance tail: has silence exceeded threshold?
      if (this.lastIsSpeech && this.silenceFrameCount * FRAME_MS >= this.config.utteranceTailMs) {
        this.lastIsSpeech = false;
        // Clear stale speech frames — next transition gets fresh Silero input
        this.recentFrames.length = 0;
        // Signal utterance end to STT (triggers ElevenLabs commit)
        this.sttHandler.onUtteranceEnd?.();
      }
      return; // Don't send to STT
    }

    // Speech detected by WebRTC
    this.stats.speechFrames++;
    this.silenceFrameCount = 0;

    // Keep ring buffer for Silero confirmation
    this.recentFrames.push(frame);
    if (this.recentFrames.length > FRAMES_FOR_SILERO) {
      this.recentFrames.shift();
    }

    if (!this.lastIsSpeech) {
      // Transition: silence → speech — confirm with Silero if available
      if (this.sileroVad) {
        this.stats.sileroChecks++;
        try {
          const sileroFrame = this.buildSileroFrame();
          const { isSpeech } = await this.sileroVad.process(sileroFrame);
          if (isSpeech < this.config.sileroThreshold) {
            // WebRTC false positive (e.g. AC noise, paper rustling)
            this.stats.falsePositivesCaught++;
            return; // Don't send to STT
          }
        } catch {
          // Silero failed — trust WebRTC and continue
        }
      }

      // Real speech started
      this.lastIsSpeech = true;

      // Send pending frame retroactively (saves first syllable)
      if (this.pendingFrame) {
        this.sttHandler.onPcmChunk(this.pendingFrame);
        this.pendingFrame = null;
      }
    }

    // Forward speech frame to STT
    this.sttHandler.onPcmChunk(frame);
  }

  /**
   * Build a Float32Array frame for Silero from the ring buffer of recent PCM frames.
   * Silero expects 1536 samples (96ms at 16kHz) as normalized Float32 [-1, 1].
   */
  private buildSileroFrame(): Float32Array {
    const result = new Float32Array(SILERO_FRAME_SAMPLES);
    let writeIdx = 0;

    // Collect samples from recent frames (Int16LE → Float32 normalized)
    for (const buf of this.recentFrames) {
      for (let i = 0; i < buf.length && writeIdx < SILERO_FRAME_SAMPLES; i += 2) {
        result[writeIdx++] = buf.readInt16LE(i) / 32768;
      }
    }
    // Remaining slots stay 0 (silence padding if not enough frames yet)
    return result;
  }

  // --- Stats ---

  private logStats(): void {
    const speechRatio = this.stats.speechFrames / Math.max(this.stats.totalFrames, 1);
    const savedRatio = 1 - speechRatio;
    this.logger.info('VadService: session stats', {
      meetingId: this.meetingId,
      totalFrames: this.stats.totalFrames,
      speechFrames: this.stats.speechFrames,
      silenceFrames: this.stats.silenceFrames,
      speechRatio: `${(speechRatio * 100).toFixed(1)}%`,
      savedRatio: `${(savedRatio * 100).toFixed(1)}%`,
      sileroChecks: this.stats.sileroChecks,
      falsePositivesCaught: this.stats.falsePositivesCaught,
    });
  }

  getStats(): VadStats {
    return { ...this.stats };
  }
}
