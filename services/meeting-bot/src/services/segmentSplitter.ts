/**
 * Segment Splitter — Post-processing chunk splitting by speaker transitions.
 *
 * Takes a committed transcript chunk from ElevenLabs (with word-level timestamps)
 * and speaker timeline windows from ActiveSpeakerService, then splits the chunk
 * at speaker transition points.
 *
 * This has ZERO impact on STT accuracy — ElevenLabs already processed the full
 * audio context. We only split the result for correct speaker attribution.
 */

import { TranscriptSegment, TranscriptWord } from './sttService';
import { SpeakerWindow } from './activeSpeakerService';

/** Timestamp fuzzy match tolerance (seconds). */
const DRIFT_TOLERANCE_S = 0.5;

/**
 * Minimum duration (seconds) for a split segment to be kept as standalone.
 * Segments shorter than this with no real content (only punctuation/spacing)
 * are merged into the previous segment. Real short interjections ("evet", "tamam")
 * are always kept regardless of duration.
 */
const MIN_SEGMENT_DURATION_S = 0.3;

export interface SplitSegment extends TranscriptSegment {
  /** Split index within the original chunk (0-based). Used for idempotency key. */
  _splitIndex: number;
}

/**
 * Find which speaker was active at a given timestamp by checking speaker windows.
 * Uses ±DRIFT_TOLERANCE_S fuzzy matching for edge cases.
 */
function findSpeakerAtTime(
  time: number,
  windows: SpeakerWindow[],
): { speaker: string; source: string } | null {
  // Exact match first
  for (const w of windows) {
    if (time >= w.startTime && time <= w.endTime) {
      return { speaker: w.speaker, source: w.source };
    }
  }
  // Fuzzy match — find nearest window within tolerance
  let nearest: SpeakerWindow | null = null;
  let nearestDist = Infinity;
  for (const w of windows) {
    const dist = Math.min(Math.abs(time - w.startTime), Math.abs(time - w.endTime));
    if (dist < nearestDist && dist <= DRIFT_TOLERANCE_S) {
      nearest = w;
      nearestDist = dist;
    }
  }
  return nearest ? { speaker: nearest.speaker, source: nearest.source } : null;
}

/**
 * Simplify raw detection source to category label.
 */
export function simplifySource(rawSource: string, speaker: string): string {
  if (speaker === 'Speaker') return 'fallback';
  if (rawSource.startsWith('webrtc')) return 'webrtc';
  if (rawSource.startsWith('gmeet-')) return 'dom';
  return rawSource || 'unknown';
}

/**
 * Split a transcript segment at speaker transition boundaries.
 *
 * @param segment - The original committed segment from ElevenLabs (with words[])
 * @param windows - Speaker timeline windows for the segment's time range
 * @returns Array of split segments. Returns single-element array if no split needed.
 */
export function splitSegmentBySpeaker(
  segment: TranscriptSegment,
  windows: SpeakerWindow[],
): SplitSegment[] {
  const words = segment.metadata?.words;

  // No words or no windows → can't split, return as-is
  if (!words || words.length === 0 || windows.length === 0) {
    return [{
      ...segment,
      _splitIndex: 0,
    }];
  }

  // Only real words matter for speaker attribution (skip spacing)
  const wordItems = words.filter(w => w.type === 'word');
  if (wordItems.length === 0) {
    return [{
      ...segment,
      _splitIndex: 0,
    }];
  }

  // Single speaker window → no transition, no split needed
  if (windows.length === 1) {
    const w = windows[0];
    return [{
      ...segment,
      speaker: w.speaker,
      _splitIndex: 0,
      metadata: {
        ...segment.metadata,
        speaker_source: simplifySource(w.source, w.speaker),
      },
    }];
  }

  // --- Multi-speaker: assign each word to a speaker ---
  type TaggedWord = TranscriptWord & { speaker: string; source: string };
  const tagged: TaggedWord[] = [];

  for (const word of words) {
    const match = findSpeakerAtTime(word.start, windows);
    tagged.push({
      ...word,
      speaker: match?.speaker || 'Speaker',
      source: match?.source || 'fallback',
    });
  }

  // --- Group consecutive words by speaker ---
  type WordGroup = { speaker: string; source: string; words: TranscriptWord[] };
  const groups: WordGroup[] = [];
  let currentGroup: WordGroup | null = null;

  for (const tw of tagged) {
    // Spacing always belongs to the current group
    if (tw.type === 'spacing') {
      if (currentGroup) {
        currentGroup.words.push({ text: tw.text, start: tw.start, end: tw.end, type: tw.type });
      }
      continue;
    }

    if (!currentGroup || tw.speaker !== currentGroup.speaker) {
      // New speaker group — trim trailing spacing from previous group
      if (currentGroup && currentGroup.words.length > 0) {
        while (currentGroup.words.length > 0 && currentGroup.words[currentGroup.words.length - 1].type === 'spacing') {
          currentGroup.words.pop();
        }
      }
      currentGroup = { speaker: tw.speaker, source: tw.source, words: [] };
      groups.push(currentGroup);
    }
    currentGroup.words.push({ text: tw.text, start: tw.start, end: tw.end, type: tw.type });
  }

  // Trim trailing spacing from last group
  if (currentGroup && currentGroup.words.length > 0) {
    while (currentGroup.words.length > 0 && currentGroup.words[currentGroup.words.length - 1].type === 'spacing') {
      currentGroup.words.pop();
    }
  }

  // Filter out empty groups
  const validGroups = groups.filter(g => g.words.some(w => w.type === 'word'));

  if (validGroups.length === 0) {
    return [{
      ...segment,
      _splitIndex: 0,
    }];
  }

  // --- Merge tiny empty-content groups into previous group ---
  // If a group has no real word content (only spacing/punctuation artifacts)
  // and is very short, merge it back. Real interjections are always kept.
  const mergedGroups: WordGroup[] = [];
  for (const group of validGroups) {
    const realWords = group.words.filter(w => w.type === 'word');
    const duration = realWords.length > 0
      ? realWords[realWords.length - 1].end - realWords[0].start
      : 0;
    const hasContent = realWords.some(w => w.text.trim().length > 0);

    if (!hasContent && duration < MIN_SEGMENT_DURATION_S && mergedGroups.length > 0) {
      // Merge into previous group
      const prev = mergedGroups[mergedGroups.length - 1];
      prev.words.push(...group.words);
    } else {
      mergedGroups.push(group);
    }
  }

  if (mergedGroups.length === 0) {
    return [{
      ...segment,
      _splitIndex: 0,
    }];
  }

  // --- Create split segments ---
  return mergedGroups.map((group, index) => {
    const groupWords = group.words;
    const realWords = groupWords.filter(w => w.type === 'word');
    const text = groupWords.map(w => w.text).join('');
    const startTime = realWords[0].start;
    const endTime = realWords[realWords.length - 1].end;

    return {
      text: text.trim(),
      start_time: startTime,
      end_time: endTime,
      language: segment.language,
      stt_provider: segment.stt_provider,
      speaker: group.speaker,
      _splitIndex: index,
      metadata: {
        language: segment.language,
        words: groupWords,
        speaker_source: simplifySource(group.source, group.speaker),
      },
    } as SplitSegment;
  }).filter(s => s.text.length > 0);
}
