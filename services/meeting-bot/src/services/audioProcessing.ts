/**
 * Audio processing utilities for the STT pipeline (Faz 5).
 *
 * NoiseGate: cuts sounds below a dB threshold (AC hum, fan noise, background).
 * AGC: normalizes volume — reduces gap between quiet and loud speakers.
 * AudioProcessingPipeline: wraps NoiseGate + AGC as a single PcmChunkHandler.
 *
 * Pipeline order: PCM → NoiseGate → AGC → VadService → ElevenLabs STT
 */

import { PcmChunkHandler } from './pcmStreamService';

/**
 * Simple noise gate that mutes PCM frames below a dB threshold.
 * Default -40dB filters typical background noise without touching speech.
 */
export class NoiseGate {
  private readonly thresholdDb: number;

  constructor(thresholdDb: number = -40) {
    this.thresholdDb = thresholdDb;
  }

  /**
   * Process a raw PCM 16-bit LE mono frame.
   * Returns the original frame if above threshold, silence otherwise.
   */
  process(frame: Buffer): Buffer {
    // Calculate RMS of the PCM 16-bit frame
    const samples = new Int16Array(frame.buffer, frame.byteOffset, frame.length / 2);
    let sumSquares = 0;
    for (let i = 0; i < samples.length; i++) {
      const normalized = samples[i] / 32768;
      sumSquares += normalized * normalized;
    }
    const rms = Math.sqrt(sumSquares / samples.length);
    const db = 20 * Math.log10(Math.max(rms, 1e-10));

    // Below threshold → return silence
    if (db < this.thresholdDb) {
      return Buffer.alloc(frame.length);
    }
    return frame;
  }
}

/**
 * Automatic Gain Control — normalizes audio level across speakers.
 * Quiet speakers get amplified, loud speakers get attenuated.
 * Smooth gain transitions prevent pop/click artifacts.
 */
export class AutomaticGainControl {
  private readonly targetRms: number;
  private readonly maxGain: number;
  private readonly smoothingFactor: number;
  private currentGain: number = 1.0;

  constructor(targetRms: number = 0.1, maxGain: number = 3.0, smoothingFactor: number = 0.01) {
    this.targetRms = targetRms;
    this.maxGain = maxGain;
    this.smoothingFactor = smoothingFactor;
  }

  /**
   * Process a raw PCM 16-bit LE mono frame.
   * Returns a gain-adjusted copy of the frame.
   */
  process(frame: Buffer): Buffer {
    const samples = new Int16Array(frame.buffer, frame.byteOffset, frame.length / 2);

    // Calculate RMS
    let sumSquares = 0;
    for (let i = 0; i < samples.length; i++) {
      sumSquares += (samples[i] / 32768) ** 2;
    }
    const rms = Math.sqrt(sumSquares / samples.length);

    // Desired gain (capped at maxGain to prevent over-amplification)
    const desiredGain = rms > 0 ? Math.min(this.targetRms / rms, this.maxGain) : 1.0;

    // Smooth transition (sudden gain changes cause pop/click sounds)
    this.currentGain += (desiredGain - this.currentGain) * this.smoothingFactor;

    // Apply gain with clipping protection
    const output = Buffer.alloc(frame.length);
    const outSamples = new Int16Array(output.buffer, output.byteOffset, output.length / 2);
    for (let i = 0; i < samples.length; i++) {
      outSamples[i] = Math.max(-32768, Math.min(32767, Math.round(samples[i] * this.currentGain)));
    }
    return output;
  }
}

export interface AudioProcessingConfig {
  noiseGateEnabled: boolean;
  noiseGateDb: number;
  agcEnabled: boolean;
  agcTargetRms: number;
  agcMaxGain: number;
}

/**
 * Wraps NoiseGate + AGC as a single PcmChunkHandler.
 * Each processor can be independently enabled/disabled.
 * Sits between PcmStreamService and VadService/STT in the pipeline.
 */
export class AudioProcessingPipeline implements PcmChunkHandler {
  private readonly noiseGate: NoiseGate | null;
  private readonly agc: AutomaticGainControl | null;
  private readonly nextHandler: PcmChunkHandler;

  constructor(nextHandler: PcmChunkHandler, cfg: AudioProcessingConfig) {
    this.noiseGate = cfg.noiseGateEnabled ? new NoiseGate(cfg.noiseGateDb) : null;
    this.agc = cfg.agcEnabled ? new AutomaticGainControl(cfg.agcTargetRms, cfg.agcMaxGain) : null;
    this.nextHandler = nextHandler;
  }

  onPcmChunk(chunk: Buffer): void {
    let processed = chunk;
    if (this.noiseGate) processed = this.noiseGate.process(processed);
    if (this.agc) processed = this.agc.process(processed);
    this.nextHandler.onPcmChunk(processed);
  }

  onStreamEnd(): void {
    this.nextHandler.onStreamEnd();
  }

  onUtteranceEnd?(): void {
    this.nextHandler.onUtteranceEnd?.();
  }
}
