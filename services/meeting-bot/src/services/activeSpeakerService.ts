/**
 * ActiveSpeakerService — Hysteresis timer + speaker state management (Faz 6.2).
 *
 * Receives raw SpeakerDetection results from the browser-injected JS
 * (300ms polling interval, set up in Faz 6.3) and stabilizes them:
 *
 *   - WebRTC sources: instant start (startThresholdSamples=1),
 *     stop after WEBRTC_STOP_SAMPLES consecutive null detections (~600ms).
 *   - DOM sources: start after SWITCH_DELAY_MS (600ms) hysteresis timer.
 *   - If the candidate changes before the timer expires, the timer resets.
 *   - If no detection arrives (null), the current speaker is preserved
 *     for up to SILENCE_TIMEOUT_MS (2000ms) to bridge short pauses.
 */

import { Logger } from 'winston';
import { loggerFactory } from '../util/logger';
import { SpeakerDetection } from './speakerDetectors/types';

export interface SpeakerChangeEvent {
  previousSpeaker: string | null;
  newSpeaker: string;
  source: string;
  timestamp: number;
}

export type SpeakerChangeCallback = (event: SpeakerChangeEvent) => void;

/** Time window during which a speaker was active (for overlap scoring, Faz 6.5). */
export interface SpeakerWindow {
  speaker: string;
  source: string;     // detection source that identified this speaker
  startTime: number;  // seconds from meeting start
  endTime: number;
}

/** Result of resolving a speaker for an STT segment (Faz 6.5). */
export interface ResolvedSpeaker {
  speaker: string;
  source: string;
}

export class ActiveSpeakerService {
  private currentSpeaker: string | null = null;
  private currentSource: string | null = null;
  private candidateSpeaker: string | null = null;
  private candidateSource: string | null = null;
  private candidateFirstSeen: number = 0;
  private lastDetectionTime: number = 0;
  private readonly meetingId: string;
  private readonly logger: Logger;
  private meetingStartTime: number | null = null; // epoch ms — set via setTimeOrigin() when first audio reaches ElevenLabs

  // New speaker candidate must persist this long before switching
  private readonly SWITCH_DELAY_MS: number;
  // How long to keep current speaker after detections stop (bridges short pauses)
  private readonly SILENCE_TIMEOUT_MS: number;

  // WebRTC stop-hysteresis (reduced from Attendee's 4 to 2 for faster transitions).
  // Counts consecutive polling cycles where the current WebRTC speaker is NOT
  // detected. Only clears the speaker after WEBRTC_STOP_SAMPLES consecutive misses.
  // Prevents jitter from brief audio gaps (breathing, pauses between words).
  private webrtcMissCount: number = 0;
  private readonly WEBRTC_STOP_SAMPLES = 2; // ~2 × 300ms = ~600ms stop delay

  // Speaker windows for overlap scoring (Faz 6.5)
  private speakerWindows: SpeakerWindow[] = [];
  private readonly WINDOW_RETENTION_S = 600; // keep last 10 minutes

  private onChangeCallback: SpeakerChangeCallback | null = null;

  constructor(
    meetingId: string,
    switchDelayMs: number = 800,
    silenceTimeoutMs: number = 2000,
  ) {
    this.meetingId = meetingId;
    this.logger = loggerFactory(meetingId, 'activeSpeaker');
    this.SWITCH_DELAY_MS = switchDelayMs;
    this.SILENCE_TIMEOUT_MS = silenceTimeoutMs;
  }

  /**
   * Register a callback for speaker changes.
   * Called whenever currentSpeaker transitions to a different person.
   */
  onSpeakerChange(callback: SpeakerChangeCallback): void {
    this.onChangeCallback = callback;
  }

  /**
   * Set the time origin for speaker window timestamps.
   * Must be called when the first PCM byte is sent to ElevenLabs,
   * so speaker windows and ElevenLabs word timestamps share the same t=0.
   */
  setTimeOrigin(epochMs: number): void {
    if (this.meetingStartTime !== null) return; // already set
    this.meetingStartTime = epochMs;
    this.logger.info('Time origin set (speaker windows synced to audio stream)');
  }

  /**
   * Process a detection result from the browser-injected JS.
   * Called every polling interval (~300ms).
   */
  processDetection(detection: SpeakerDetection | null): void {
    const now = Date.now();

    if (!detection) {
      // WebRTC stop-hysteresis (Attendee pattern: stopThresholdSamples=4).
      // Don't clear speaker immediately on null — count consecutive misses first.
      // This prevents jitter from brief audio gaps (breathing, word pauses).
      if (this.currentSource?.startsWith('webrtc')) {
        this.webrtcMissCount++;
        if (this.webrtcMissCount < this.WEBRTC_STOP_SAMPLES) {
          // Still within stop threshold — keep current speaker, record window
          this.recordSpeakerWindow();
          return;
        }
        // Stop threshold exceeded — fall through to silence timeout check
      }

      // No signal — preserve current speaker within silence timeout
      if (
        this.currentSpeaker &&
        this.lastDetectionTime > 0 &&
        now - this.lastDetectionTime > this.SILENCE_TIMEOUT_MS
      ) {
        // Silence timeout exceeded — clear speaker
        this.logger.info('Silence timeout, clearing speaker', { previousSpeaker: this.currentSpeaker });
        this.currentSpeaker = null;
        this.currentSource = null;
        this.candidateSpeaker = null;
        this.webrtcMissCount = 0;
      }
      return;
    }

    this.lastDetectionTime = now;
    const detectedName = detection.name;
    const isWebrtc = detection.source.startsWith('webrtc');

    // Same speaker continues — stable, reset candidate and miss counter
    if (detectedName === this.currentSpeaker) {
      this.candidateSpeaker = null;
      this.webrtcMissCount = 0;
      this.recordSpeakerWindow();
      return;
    }

    // Different speaker detected — start/continue candidate timer
    if (detectedName !== this.candidateSpeaker) {
      // New candidate — start timer
      this.candidateSpeaker = detectedName;
      this.candidateSource = detection.source;
      this.candidateFirstSeen = now;
      this.webrtcMissCount = 0;

      // WebRTC source is raw audio level — no UI lag, so switch immediately
      // on first detection (Attendee uses startThresholdSamples=1).
      // DOM sources need the full hysteresis timer due to CSS transition delays.
      if (isWebrtc) {
        // Fall through to the switch block below (delay=0 effectively)
      } else {
        return;
      }
    }

    // Same candidate seen again (or WebRTC instant switch) — check if timer expired
    const effectiveDelay = this.candidateSource?.startsWith('webrtc') ? 0 : this.SWITCH_DELAY_MS;
    if (now - this.candidateFirstSeen >= effectiveDelay) {
      // Timer expired — switch speaker
      const previousSpeaker = this.currentSpeaker;
      this.currentSpeaker = detectedName;
      this.currentSource = this.candidateSource;
      this.candidateSpeaker = null;
      this.candidateSource = null;
      this.webrtcMissCount = 0;

      this.logger.info('Speaker changed', { previousSpeaker: previousSpeaker || '(none)', newSpeaker: detectedName, source: detection.source });

      this.recordSpeakerWindow();

      if (this.onChangeCallback) {
        this.onChangeCallback({
          previousSpeaker,
          newSpeaker: detectedName,
          source: detection.source,
          timestamp: now,
        });
      }
    }
    // Timer not expired yet — keep waiting
  }

  /**
   * Get the current stabilized speaker name.
   * Returns 'Speaker' as fallback if no speaker is detected.
   */
  getCurrentSpeaker(): string {
    return this.currentSpeaker || 'Speaker';
  }

  /**
   * Get the current speaker name or null if nobody is speaking.
   */
  getCurrentSpeakerOrNull(): string | null {
    return this.currentSpeaker;
  }

  /**
   * Get the source signal that identified the current speaker.
   */
  getCurrentSource(): string | null {
    return this.currentSource;
  }

  /**
   * Resolve the most likely speaker for a given STT segment time range (Faz 6.5).
   * Uses overlap scoring: find which speaker talked the longest during [segStart, segEnd].
   * Returns both speaker name AND the source that identified them, so metadata stays
   * consistent (fixes P3: source was from getCurrentSource() which could be a different speaker).
   * Falls back to getCurrentSpeaker() if no windows overlap the segment.
   */
  resolveSpeakerForSegment(segStart: number, segEnd: number): ResolvedSpeaker {
    if (this.speakerWindows.length === 0) {
      // No speaker windows recorded yet — no data to attribute from
      return { speaker: 'Speaker', source: 'fallback' };
    }

    // Calculate overlap duration per speaker, tracking the source from windows
    const overlapMap = new Map<string, { duration: number; source: string }>();

    for (const w of this.speakerWindows) {
      const overlapStart = Math.max(w.startTime, segStart);
      const overlapEnd = Math.min(w.endTime, segEnd);
      if (overlapEnd > overlapStart) {
        const duration = overlapEnd - overlapStart;
        const existing = overlapMap.get(w.speaker);
        if (existing) {
          existing.duration += duration;
          // Keep the source from the longest-overlapping window for this speaker
        } else {
          overlapMap.set(w.speaker, { duration, source: w.source });
        }
      }
    }

    if (overlapMap.size === 0) {
      // No overlap found — don't attribute to stale getCurrentSpeaker()
      return { speaker: 'Speaker', source: 'fallback' };
    }

    // Find speaker with most overlap
    let bestSpeaker = '';
    let bestDuration = 0;
    let bestSource = 'unknown';
    for (const [speaker, data] of overlapMap) {
      if (data.duration > bestDuration) {
        bestSpeaker = speaker;
        bestDuration = data.duration;
        bestSource = data.source;
      }
    }

    // Minimum threshold: overlap must cover at least 10% of segment duration.
    const segDuration = segEnd - segStart;
    if (segDuration > 0 && bestDuration < segDuration * 0.1) {
      return { speaker: 'Speaker', source: 'fallback' };
    }

    return bestSpeaker
      ? { speaker: bestSpeaker, source: bestSource }
      : { speaker: 'Speaker', source: 'fallback' };
  }

  /**
   * Get all speaker windows overlapping a time range (for chunk splitting, Phase 3).
   * Returns windows sorted chronologically, trimmed to the requested range.
   */
  getWindowsForRange(startTime: number, endTime: number): SpeakerWindow[] {
    const result: SpeakerWindow[] = [];
    for (const w of this.speakerWindows) {
      // Check overlap: window overlaps range if w.start < endTime AND w.end > startTime
      if (w.startTime < endTime && w.endTime > startTime) {
        result.push({
          speaker: w.speaker,
          source: w.source,
          startTime: Math.max(w.startTime, startTime),
          endTime: Math.min(w.endTime, endTime),
        });
      }
    }
    return result;
  }

  /**
   * Record current speaker into time windows (called on each processDetection).
   * Extends the last window if same speaker, creates new window otherwise.
   */
  private recordSpeakerWindow(): void {
    if (!this.currentSpeaker || this.meetingStartTime === null) return;

    const now = (Date.now() - this.meetingStartTime) / 1000; // seconds from audio start
    const last = this.speakerWindows[this.speakerWindows.length - 1];

    if (last && last.speaker === this.currentSpeaker) {
      last.endTime = now; // extend current window
    } else {
      this.speakerWindows.push({
        speaker: this.currentSpeaker,
        source: this.currentSource || 'unknown',
        startTime: now,
        endTime: now,
      });
    }

    // Prune old windows (keep last 10 minutes)
    const cutoff = now - this.WINDOW_RETENTION_S;
    this.speakerWindows = this.speakerWindows.filter(w => w.endTime > cutoff);
  }

  /**
   * Reset all state (e.g. when leaving a meeting).
   */
  reset(): void {
    this.currentSpeaker = null;
    this.currentSource = null;
    this.candidateSpeaker = null;
    this.candidateSource = null;
    this.candidateFirstSeen = 0;
    this.lastDetectionTime = 0;
    this.webrtcMissCount = 0;
    this.meetingStartTime = null;
    this.speakerWindows = [];
  }
}
