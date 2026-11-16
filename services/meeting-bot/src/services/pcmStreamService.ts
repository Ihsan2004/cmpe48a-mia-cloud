import { Logger } from 'winston';
import * as fs from 'fs';
import * as path from 'path';

export interface PcmChunkHandler {
  onPcmChunk(chunk: Buffer): void;
  onStreamEnd(): void;
  onUtteranceEnd?(): void;
}

/**
 * Debug handler that writes raw PCM chunks to a file for verification.
 * Verify with: ffplay -f s16le -ar 16000 -ac 1 <output_file>
 * Only used when STT_DEBUG_PCM_DUMP=true
 */
export class DebugPcmDumpHandler implements PcmChunkHandler {
  private fd: number | null = null;
  private totalBytes = 0;
  private chunkCount = 0;
  private logger: Logger;
  private filePath: string;

  constructor(meetingId: string, logger: Logger) {
    this.logger = logger;
    const dumpDir = path.join(process.cwd(), 'dist', '_pcm_debug');
    if (!fs.existsSync(dumpDir)) {
      fs.mkdirSync(dumpDir, { recursive: true });
    }
    this.filePath = path.join(dumpDir, `pcm-${meetingId}-${Date.now()}.raw`);
    this.fd = fs.openSync(this.filePath, 'w');
    this.logger.info('DebugPcmDumpHandler: writing PCM to file', { filePath: this.filePath });
  }

  onPcmChunk(chunk: Buffer): void {
    if (this.fd === null) return;
    fs.writeSync(this.fd, chunk);
    this.totalBytes += chunk.length;
    this.chunkCount++;
    if (this.chunkCount % 50 === 0) { // Log every ~10 seconds (50 * 200ms)
      this.logger.info('DebugPcmDumpHandler: progress', {
        chunkCount: this.chunkCount,
        totalBytes: this.totalBytes,
        durationSec: Math.round(this.totalBytes / 32000), // 16kHz * 2 bytes = 32000 bytes/sec
      });
    }
  }

  onStreamEnd(): void {
    if (this.fd !== null) {
      fs.closeSync(this.fd);
      this.fd = null;
      this.logger.info('DebugPcmDumpHandler: file closed', {
        filePath: this.filePath,
        totalBytes: this.totalBytes,
        chunkCount: this.chunkCount,
        durationSec: Math.round(this.totalBytes / 32000),
      });
      this.logger.info(`DebugPcmDumpHandler: verify with: ffplay -f s16le -ar 16000 -ac 1 ${this.filePath}`);
    }
  }
}

/**
 * Meeting-scoped PCM stream distributor.
 * Observer pattern — handlers register to receive raw 16kHz mono 16-bit PCM chunks.
 * In Faz 2, ElevenLabsSttService will register as a handler.
 */
export class PcmStreamService {
  private readonly meetingId: string;
  private handlers: PcmChunkHandler[] = [];
  private ended = false;
  private logger: Logger;

  constructor(meetingId: string, logger: Logger) {
    this.meetingId = meetingId;
    this.logger = logger;

    // Auto-register debug PCM dump handler if enabled
    if (process.env.STT_DEBUG_PCM_DUMP === 'true') {
      this.registerHandler(new DebugPcmDumpHandler(meetingId, logger));
    }
  }

  registerHandler(handler: PcmChunkHandler): void {
    this.handlers.push(handler);
    this.logger.info('PcmStreamService: handler registered', {
      meetingId: this.meetingId,
      handlerCount: this.handlers.length,
    });
  }

  removeHandler(handler: PcmChunkHandler): void {
    const idx = this.handlers.indexOf(handler);
    if (idx !== -1) {
      this.handlers.splice(idx, 1);
      this.logger.info('PcmStreamService: handler removed', {
        meetingId: this.meetingId,
        handlerCount: this.handlers.length,
      });
    }
  }

  emit(chunk: Buffer): void {
    if (this.ended) return;
    for (const handler of this.handlers) {
      try {
        handler.onPcmChunk(chunk);
      } catch (err) {
        this.logger.error('PcmStreamService: handler.onPcmChunk error', {
          meetingId: this.meetingId,
          error: err instanceof Error ? err.message : String(err),
        });
      }
    }
  }

  end(): void {
    if (this.ended) return;
    this.ended = true;
    this.logger.info('PcmStreamService: stream ended', { meetingId: this.meetingId });
    for (const handler of this.handlers) {
      try {
        handler.onStreamEnd();
      } catch (err) {
        this.logger.error('PcmStreamService: handler.onStreamEnd error', {
          meetingId: this.meetingId,
          error: err instanceof Error ? err.message : String(err),
        });
      }
    }
  }
}
