import { Pool } from 'pg';
import { Logger } from 'winston';
import { TranscriptSegment } from './sttService';
import { getPool } from '../lib/database';
import crypto from 'crypto';
import * as fs from 'fs';
import * as path from 'path';

export class TranscriptWriter {
  private pool: Pool;
  private meetingId: string;
  private meetingUrl: string;
  private logger: Logger;
  private totalWritten = 0;
  private totalFailed = 0;
  private pendingWrites: Set<Promise<void>> = new Set();
  private schemaValid = false;

  constructor(meetingId: string, meetingUrl: string, logger: Logger) {
    this.pool = getPool();
    this.meetingId = meetingId;
    this.meetingUrl = meetingUrl;
    this.logger = logger;
  }

  /**
   * Validate that transcript_segments table has idempotency_key column and unique constraint.
   * Call once before first write. If validation fails, writer disables itself.
   */
  async validateSchema(): Promise<boolean> {
    try {
      const result = await this.pool.query(
        `SELECT column_name FROM information_schema.columns
         WHERE table_schema = 'public' AND table_name = 'transcript_segments' AND column_name = 'idempotency_key'`
      );
      if (result.rows.length === 0) {
        this.logger.error('TranscriptWriter: idempotency_key column missing — run Faz 3.1 migration. Writer disabled.', {
          meetingId: this.meetingId,
        });
        return false;
      }
      this.schemaValid = true;
      return true;
    } catch (err) {
      this.logger.error('TranscriptWriter: schema validation failed — DB unreachable or query error. Writer disabled.', {
        meetingId: this.meetingId,
        error: err instanceof Error ? err.message : String(err),
      });
      return false;
    }
  }

  /**
   * Called from onSegment callback (fire-and-forget via .catch()).
   * Tracks in-flight promise so finalFlush() can await all pending writes.
   */
  writeSegment(segment: TranscriptSegment, splitIndex: number = 0): Promise<void> {
    if (!this.schemaValid) {
      this.logger.debug('TranscriptWriter: skipping write — schema not validated');
      return Promise.resolve();
    }
    const p = this._doWrite(segment, splitIndex);
    this.pendingWrites.add(p);
    p.finally(() => this.pendingWrites.delete(p));
    return p;
  }

  /**
   * Await all in-flight writes, then recover spool file.
   */
  async finalFlush(): Promise<void> {
    // Wait for all in-flight writeSegment() calls (including retries) to settle
    if (this.pendingWrites.size > 0) {
      this.logger.info('TranscriptWriter: waiting for in-flight writes', {
        meetingId: this.meetingId,
        pending: this.pendingWrites.size,
      });
      await Promise.allSettled([...this.pendingWrites]);
    }

    const spoolPath = this.getSpoolPath();
    if (!fs.existsSync(spoolPath)) {
      this.logger.info('TranscriptWriter: finalFlush complete', {
        meetingId: this.meetingId,
        totalWritten: this.totalWritten,
        totalFailed: this.totalFailed,
      });
      return;
    }

    const content = fs.readFileSync(spoolPath, 'utf-8').trim();
    if (!content) return;

    const lines = content.split('\n');
    let recovered = 0;
    const stillFailed: string[] = [];

    for (const line of lines) {
      try {
        const row = JSON.parse(line);
        const result = await this.pool.query(
          `INSERT INTO transcript_segments
            (meeting_id, meeting_url, text, start_time, end_time, stt_provider, metadata, idempotency_key, role, speaker)
           VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
           ON CONFLICT (idempotency_key) DO NOTHING`,
          [row.meeting_id, row.meeting_url, row.text, row.start_time,
           row.end_time, row.stt_provider, row.metadata, row.idempotency_key, row.role, row.speaker || row.role]
        );
        recovered += result.rowCount ?? 0;
      } catch {
        stillFailed.push(line);
      }
    }

    this.logger.info('TranscriptWriter: spool recovery', {
      meetingId: this.meetingId,
      total: lines.length, recovered, stillFailed: stillFailed.length,
    });

    if (stillFailed.length > 0) {
      fs.writeFileSync(spoolPath, stillFailed.join('\n') + '\n');
      // TODO: Faz 4'te S3'e spool upload eklenecek (DinD durability)
      this.logger.warn('TranscriptWriter: spool file retained for recovery', {
        meetingId: this.meetingId,
        path: spoolPath,
        count: stillFailed.length,
      });
    } else {
      fs.unlinkSync(spoolPath);
    }
  }

  getStats(): { totalWritten: number; totalFailed: number } {
    return { totalWritten: this.totalWritten, totalFailed: this.totalFailed };
  }

  private async _doWrite(segment: TranscriptSegment, splitIndex: number = 0): Promise<void> {
    const idempotencyKey = this.buildIdempotencyKey(segment, splitIndex);

    const speakerName = segment.speaker || 'unknown';
    const query = `
      INSERT INTO transcript_segments
        (meeting_id, meeting_url, text, start_time, end_time, stt_provider, metadata, idempotency_key, role, speaker)
      VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
      ON CONFLICT (idempotency_key) DO NOTHING
    `;

    const values = [
      this.meetingId,
      this.meetingUrl,
      segment.text,
      segment.start_time,
      segment.end_time,
      segment.stt_provider,
      JSON.stringify(segment.metadata),
      idempotencyKey,
      speakerName,
      speakerName,
    ];

    for (let attempt = 1; attempt <= 3; attempt++) {
      try {
        const result = await this.pool.query(query, values);
        const inserted = result.rowCount ?? 0;
        this.totalWritten += inserted;

        this.logger.info('TranscriptWriter: segment written', {
          meetingId: this.meetingId,
          inserted,
          totalWritten: this.totalWritten,
          text: segment.text.substring(0, 60),
          startTime: segment.start_time,
        });
        return;
      } catch (err) {
        if (attempt < 3) {
          const delay = 1000 * Math.pow(2, attempt - 1);
          this.logger.warn('TranscriptWriter: retry', {
            attempt, delay, meetingId: this.meetingId,
            error: err instanceof Error ? err.message : String(err),
          });
          await new Promise(r => setTimeout(r, delay));
        } else {
          this.totalFailed++;
          this.logger.error('TranscriptWriter: INSERT failed after 3 retries, spooling to disk', {
            meetingId: this.meetingId,
            totalFailed: this.totalFailed,
            error: err instanceof Error ? err.message : String(err),
          });
          this.spoolToDisk(segment, idempotencyKey);
        }
      }
    }
  }

  private buildIdempotencyKey(seg: TranscriptSegment, splitIndex: number = 0): string {
    const startTimeMs = Math.round(seg.start_time * 1000);
    const hash = crypto.createHash('sha256').update(seg.text).digest('hex').slice(0, 6);
    // splitIndex ensures unique keys when a chunk is split into multiple segments
    return splitIndex > 0
      ? `${this.meetingId}_${startTimeMs}_${hash}_${splitIndex}`
      : `${this.meetingId}_${startTimeMs}_${hash}`;
  }

  private spoolToDisk(segment: TranscriptSegment, idempotencyKey: string): void {
    try {
      const spoolDir = path.dirname(this.getSpoolPath());
      if (!fs.existsSync(spoolDir)) fs.mkdirSync(spoolDir, { recursive: true });

      const spoolSpeaker = segment.speaker || 'unknown';
      const row = {
        idempotency_key: idempotencyKey,
        meeting_id: this.meetingId,
        meeting_url: this.meetingUrl,
        text: segment.text,
        start_time: segment.start_time,
        end_time: segment.end_time,
        stt_provider: segment.stt_provider,
        metadata: JSON.stringify(segment.metadata),
        role: spoolSpeaker,
        speaker: spoolSpeaker,
      };
      fs.appendFileSync(this.getSpoolPath(), JSON.stringify(row) + '\n');
    } catch (err) {
      this.logger.error('TranscriptWriter: spool to disk failed', {
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }

  private getSpoolPath(): string {
    return path.join(process.cwd(), 'dist', '_tempvideo', this.meetingId, 'failed-segments.jsonl');
  }
}
