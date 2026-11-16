import { Logger } from 'winston';
import { KnownError, WaitingAtLobbyRetryError } from '../error';
import { getErrorType } from '../util/logger';
import config from '../config';
import { PcmStreamService } from '../services/pcmStreamService';
import { ElevenLabsSttService } from '../services/sttService';
import { VadService } from '../services/vadService';
import { AudioProcessingPipeline } from '../services/audioProcessing';
import { TranscriptWriter } from '../services/transcriptWriter';
import { SttLifecycle } from '../bots/AbstractMeetBot';
import { ActiveSpeakerService } from '../services/activeSpeakerService';
import { splitSegmentBySpeaker, simplifySource } from '../services/segmentSplitter';

export interface MeetingJoinParams {
  meeting_url: string;
  bot_name: string;
  meeting_id: string;
  language?: string;
  force?: boolean;
}

export type MeetingProvider = 'google' | 'microsoft' | 'zoom';

export interface MeetingJoinRedisParams extends MeetingJoinParams {
  provider?: MeetingProvider;
  api_key?: string;
}

/** Detect provider from meeting URL hostname (strict match) */
export function detectProvider(url: string): MeetingProvider | null {
  try {
    const parsed = new URL(url);
    const host = parsed.hostname.toLowerCase();
    if (host === 'meet.google.com') return 'google';
    if (host === 'teams.microsoft.com' || host === 'teams.live.com') return 'microsoft';
    if (host === 'zoom.us' || host.endsWith('.zoom.us')) return 'zoom';
    return null;
  } catch {
    return null;
  }
}

/**
 * Validate meeting_id is safe for use in file paths and object keys.
 * Only allows alphanumeric, hyphens, and underscores.
 */
const SAFE_MEETING_ID_RE = /^[a-zA-Z0-9_-]+$/;
export function validateMeetingId(meetingId: string): boolean {
  return SAFE_MEETING_ID_RE.test(meetingId) && meetingId.length <= 256;
}

const sleep = (ms: number): Promise<void> =>
  new Promise((r) => setTimeout(r, ms));

export const joinMeetWithRetry = async (
  processor: (params: MeetingJoinParams) => Promise<void>,
  meetingUrl: string,
  botName: string,
  meetingId: string,
  retryCount: number,
  language: string | undefined,
  logger: Logger
) => {
  try {
    await processor({
      meeting_url: meetingUrl,
      bot_name: botName,
      meeting_id: meetingId,
      language,
    });
  } catch (error) {
    // Never retry WaitingAtLobbyRetryError - if user doesn't admit bot, retrying won't help
    if (error instanceof WaitingAtLobbyRetryError) {
      logger.error('WaitingAtLobbyRetryError - not retrying:', error.message);
      throw error;
    }

    if (error instanceof KnownError && !error.retryable) {
      logger.error('KnownError is not retryable:', error.name, error.message);
      throw error;
    }

    if (error instanceof KnownError && error.retryable && (retryCount + 1) >= error.maxRetries) {
      logger.error(`KnownError: ${error.maxRetries} tries consumed:`, error.name, error.message);
      throw error;
    }

    retryCount += 1;
    await sleep(retryCount * 30000);
    if (retryCount <= config.retryCount) {
      if (retryCount) {
        logger.warn(`Retry attempt: ${retryCount}/${config.retryCount}`);
      }
      await joinMeetWithRetry(processor, meetingUrl, botName, meetingId, retryCount, language, logger);
    } else {
      throw error;
    }
  }
};

export const processMeetingJoin = async (
  processor: (params: MeetingJoinParams) => Promise<void>,
  meetingUrl: string,
  botName: string,
  meetingId: string,
  language: string | undefined,
  logger: Logger
) => {
  try {
    logger.info('LogBasedMetric Bot has started recording meeting.');
    await joinMeetWithRetry(processor, meetingUrl, botName, meetingId, 0, language, logger);
    logger.info('LogBasedMetric Bot has finished recording meeting successfully.');
  } catch (error) {
    const errorType = getErrorType(error);
    if (error instanceof KnownError) {
      logger.error('KnownError bot is permanently exiting:', { error, meetingId });
    } else {
      logger.error('Error joining meeting after multiple retries:', { error, meetingId });
    }
    logger.error(`LogBasedMetric Bot has permanently failed. [errorType: ${errorType}]`);
  }
};

/**
 * Create and connect an ElevenLabs STT pipeline with direct DB writer.
 * Registers sttService as PcmChunkHandler on pcmStreamService.
 * Returns a composite SttLifecycle (drain/disconnect/finalFlush) or undefined if:
 *   - ElevenLabs API key is not configured
 *   - Connection fails (non-fatal — recording continues without STT)
 */
export async function createSttPipeline(
  pcmStreamService: PcmStreamService,
  meetingId: string,
  meetingUrl: string,
  language: string | undefined,
  logger: Logger,
): Promise<SttLifecycle | undefined> {
  if (!config.elevenLabsApiKey) {
    logger.info('ElevenLabs API key not configured — STT disabled');
    return undefined;
  }

  try {
    // Mutable reference for speaker service — set later via setActiveSpeakerService (Faz 6.4)
    let speakerServiceRef: ActiveSpeakerService | null = null;

    // [P2] Misconfiguration guard: flag açık ama DB URL yok
    if (config.transcriptWriteEnabled && !config.databaseUrl) {
      logger.warn('TRANSCRIPT_WRITE_ENABLED=true but DATABASE_URL is empty — transcript DB writes disabled');
    }

    let writer: TranscriptWriter | undefined;
    if (config.transcriptWriteEnabled && config.databaseUrl) {
      const w = new TranscriptWriter(meetingId, meetingUrl, logger);
      const schemaOk = await w.validateSchema();
      if (schemaOk) {
        writer = w;
      } else {
        logger.warn('TranscriptWriter disabled — schema validation failed. Segments will be logged only.');
      }
    }

    // When local VAD is enabled, force manual commit strategy to prevent
    // double commits (local VAD onUtteranceEnd + ElevenLabs built-in VAD)
    const effectiveCommitStrategy = config.vadEnabled ? 'manual' : config.sttCommitStrategy;
    if (config.vadEnabled && config.sttCommitStrategy !== 'manual') {
      logger.info('VAD enabled — overriding STT_COMMIT_STRATEGY to manual', {
        original: config.sttCommitStrategy,
      });
    }

    const sttService = new ElevenLabsSttService(logger, {
      apiKey: config.elevenLabsApiKey,
      language: config.sttLanguage || language,
      modelId: config.elevenLabsModelId,
      region: config.elevenLabsRegion,
      commitStrategy: effectiveCommitStrategy,
      onSegment: (segment) => {
        // Speaker-aware chunk splitting (Phase 3):
        // 1. Get speaker timeline windows for the segment's time range
        // 2. Split the chunk at speaker transition boundaries
        // 3. Write each sub-segment with correct speaker attribution
        //
        // If no speaker service or no windows, falls back to single-speaker behavior.
        //
        // TODO(Faz 6.5): VAD time axis mismatch — when VAD is enabled, STT segment
        // timestamps are compressed-PCM-time while speaker windows use wall-clock time.
        // Currently VAD_ENABLED=false so this is not an active issue.

        const windows = speakerServiceRef
          ? speakerServiceRef.getWindowsForRange(segment.start_time, segment.end_time)
          : [];

        const splitSegments = splitSegmentBySpeaker(segment, windows);

        // Fallback: if splitter returned segments without speaker attribution
        // (no windows available), use resolveSpeakerForSegment for single-speaker fallback
        for (const splitSeg of splitSegments) {
          if (splitSeg.speaker === undefined || splitSeg.speaker === 'Speaker') {
            if (speakerServiceRef) {
              const resolved = speakerServiceRef.resolveSpeakerForSegment(splitSeg.start_time, splitSeg.end_time);
              splitSeg.speaker = resolved.speaker;
              splitSeg.metadata = { ...splitSeg.metadata, speaker_source: simplifySource(resolved.source, resolved.speaker) };
            }
          }
        }

        const splitCount = splitSegments.length;
        if (splitCount > 1) {
          logger.info(`[SegmentSplitter] Chunk split: ${(segment.end_time - segment.start_time).toFixed(1)}s → ${splitCount} segments`, {
            splits: splitSegments.map(s => `${s.speaker} (${s.start_time.toFixed(1)}-${s.end_time.toFixed(1)}s)`),
          });
        }

        for (const splitSeg of splitSegments) {
          if (writer) {
            writer.writeSegment(splitSeg, splitSeg._splitIndex).catch(err => {
              logger.error('TranscriptWriter: async write error', {
                error: err instanceof Error ? err.message : String(err),
              });
            });
          } else {
            logger.info('STT segment (no writer)', {
              text: splitSeg.text.substring(0, 60),
              speaker: splitSeg.speaker,
              start: splitSeg.start_time,
              end: splitSeg.end_time,
            });
          }
        }
      },
      onPartial: (text) => {
        logger.debug('STT partial transcript', { text: text.substring(0, 100) });
      },
    });

    // Build handler chain: PCM → [AudioProcessing] → [VAD] → STT
    // Each layer is optional and wraps the next handler.
    let vadService: VadService | undefined;
    let pipelineHandler = sttService as import('../services/pcmStreamService').PcmChunkHandler;

    // VAD layer
    if (config.vadEnabled) {
      try {
        vadService = new VadService(meetingId, sttService, logger, {
          webrtcAggressiveness: config.vadWebrtcAggressiveness,
          sileroThreshold: config.vadSileroThreshold,
          utteranceTailMs: config.vadUtteranceTailMs,
          pendingFrameLookback: config.vadPendingFrameLookback,
        });
        await vadService.initialize();
        pipelineHandler = vadService;
      } catch (vadErr) {
        logger.warn('VadService init failed — falling back to direct STT (no VAD)', {
          meetingId,
          error: vadErr instanceof Error ? vadErr.message : String(vadErr),
        });
        vadService = undefined;
      }
    }

    // Audio processing layer (NoiseGate and/or AGC)
    if (config.noiseGateEnabled || config.agcEnabled) {
      pipelineHandler = new AudioProcessingPipeline(pipelineHandler, {
        noiseGateEnabled: config.noiseGateEnabled,
        noiseGateDb: config.noiseGateThresholdDb,
        agcEnabled: config.agcEnabled,
        agcTargetRms: config.agcTargetRms,
        agcMaxGain: config.agcMaxGain,
      });
      logger.info('Audio processing enabled', {
        noiseGate: config.noiseGateEnabled ? `${config.noiseGateThresholdDb}dB` : 'off',
        agc: config.agcEnabled ? `target=${config.agcTargetRms} max=${config.agcMaxGain}` : 'off',
      });
    }

    pcmStreamService.registerHandler(pipelineHandler);

    try {
      await sttService.connect();
    } catch (connectErr) {
      pcmStreamService.removeHandler(pipelineHandler);
      throw connectErr;
    }

    logger.info('ElevenLabs STT pipeline connected', {
      meetingId,
      writerEnabled: !!writer,
      vadEnabled: config.vadEnabled,
    });

    return {
      drain: async (timeoutMs: number) => {
        // Flush VAD queue first so remaining speech frames reach STT
        if (vadService) await vadService.flush();
        return sttService.drain(timeoutMs);
      },
      disconnect: () => sttService.disconnect(),
      isDisabled: () => sttService.isDisabled(),
      finalFlush: () => writer ? writer.finalFlush() : Promise.resolve(),
      getUsageMetrics: () => {
        const sttMetrics = sttService.getUsageMetrics();
        const writerStats = writer ? writer.getStats() : { totalWritten: 0 };
        return { ...sttMetrics, segmentCount: writerStats.totalWritten };
      },
      setActiveSpeakerService: (service: ActiveSpeakerService) => {
        speakerServiceRef = service;
        // Sync speaker window time origin with ElevenLabs audio t=0
        sttService.onFirstAudioSent((epochMs) => {
          service.setTimeOrigin(epochMs);
        });
        logger.info('ActiveSpeakerService wired to STT pipeline', { meetingId });
      },
    };
  } catch (err) {
    logger.error('Failed to connect ElevenLabs STT service (non-fatal, recording continues)', {
      error: err instanceof Error ? err.message : String(err),
    });
    return undefined;
  }
}

/**
 * Create a validated TranscriptWriter for a meeting.
 * Returns undefined if writing is disabled, DB is not configured, or schema validation fails.
 */
export async function createTranscriptWriter(
  meetingId: string,
  meetingUrl: string,
  logger: Logger,
): Promise<TranscriptWriter | undefined> {
  if (!config.transcriptWriteEnabled) return undefined;

  if (!config.databaseUrl) {
    logger.warn('TRANSCRIPT_WRITE_ENABLED=true but DATABASE_URL is empty — transcript DB writes disabled');
    return undefined;
  }

  const writer = new TranscriptWriter(meetingId, meetingUrl, logger);
  const schemaOk = await writer.validateSchema();
  if (!schemaOk) {
    logger.warn('TranscriptWriter disabled — schema validation failed. Segments will be logged only.');
    return undefined;
  }
  return writer;
}

