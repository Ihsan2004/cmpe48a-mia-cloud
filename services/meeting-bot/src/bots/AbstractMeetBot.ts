import { IUploader } from '../middleware/disk-uploader';
import { PcmStreamService } from '../services/pcmStreamService';
import { ActiveSpeakerService } from '../services/activeSpeakerService';
import { MiaBackendClient } from '../services/miaBackendClient';

/** Minimal lifecycle interface for STT services — bot manages drain/disconnect/flush */
export interface SttLifecycle {
  drain(timeoutMs: number): Promise<void>;
  disconnect(): void;
  isDisabled(): boolean;
  /** Flush remaining accumulated segments (called after drain, before upload) */
  finalFlush(): Promise<void>;
  /** Returns accumulated STT usage metrics for cost tracking. */
  getUsageMetrics(): { audioDurationSeconds: number; bytesProcessed: number; reconnectCount: number; segmentCount: number };
  /** Wire up speaker detection after bot joins (Faz 6.4). */
  setActiveSpeakerService?: (service: ActiveSpeakerService) => void;
}

export interface BotLaunchParams {
  provider: 'google' | 'microsoft' | 'zoom';
  url: string;
  name: string;
  meetingId: string;
  language?: string;
}

export interface JoinParams {
  url: string;
  name: string;
  meetingId: string;
  language?: string;
  uploader: IUploader;
  pcmStreamService?: PcmStreamService;
  sttService?: SttLifecycle;
  miaBackendClient?: MiaBackendClient | null;
}

export abstract class AbstractMeetBot {
  abstract join(params: JoinParams): Promise<void>;
}
