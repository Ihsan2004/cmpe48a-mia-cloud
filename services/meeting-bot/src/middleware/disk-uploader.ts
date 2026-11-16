import { Logger } from 'winston';
import { ContentType, extensionToContentType } from '../types';
import fs, { createWriteStream } from 'fs';
import path from 'path';
import { LogAggregator, loggerFactory } from '../util/logger';
import config from '../config';
import { getStorageProvider } from '../uploader/providers/factory';
import { notifyRecordingCompleted, RecordingCompletedPayload } from '../services/notificationService';

const moduleLogger = loggerFactory('system', 'disk-uploader');
moduleLogger.info('Working directory', { cwd: process.cwd() });

const tempFolder = path.join(process.cwd(), 'dist', '_tempvideo');

export interface IUploader {
  uploadRecordingToRemoteStorage(options?: { forceUpload?: boolean }): Promise<boolean>;
  saveDataToTempFile(data: Buffer): Promise<boolean>;
}

// Save to disk and upload in one session
class DiskUploader implements IUploader {
  private _meetingId: string;
  private _tempFileId: string;
  private _logger: Logger;
  private _meetingLink?: string;

  private readonly UPLOAD_CHUNK_SIZE = 50 * 1024 * 1024; // 50 MiB

  private readonly RETRY_UPLOAD_DELAY_BASE_MS = 500;
  private readonly MAX_GLOBAL_FAILURES = 5;

  private contentType: ContentType = extensionToContentType[config.uploaderFileExtension] ?? 'video/webm'; // Default video format
  private fileExtension: string = config.uploaderFileExtension;
  private lastUploadedBlobUrl?: string;
  private lastRecordingId?: string;
  private lastStorageDetails?: Record<string, any>;

  private queue: Buffer[];
  private writing: boolean;
  private diskWriteSuccess: LogAggregator;

  private forceUpload: boolean;

  private constructor(
    meetingId: string,
    tempFileId: string,
    logger: Logger,
    meetingLink?: string
  ) {
    this._meetingId = meetingId;
    this._tempFileId = tempFileId;
    this._logger = logger;
    this._meetingLink = meetingLink;

    this.queue = [];
    this.writing = false;
    this.diskWriteSuccess = new LogAggregator(this._logger, `Success writing temp chunk to disk ${this._meetingId}`);
    this.forceUpload = false;
  }

  public static async initialize(
    meetingId: string,
    tempFileId: string,
    logger: Logger,
    meetingLink?: string
  ) {
    const folderPath = DiskUploader.getFolderPath(meetingId);

    await DiskUploader.setupDirectory(folderPath, meetingId, logger);

    const instance = new DiskUploader(
      meetingId,
      tempFileId,
      logger,
      meetingLink
    );
    return instance;
  }

  private writeChunkToDisk(chunk: Buffer): Promise<void> {
    const filePath = DiskUploader.getFilePath(this._meetingId, this._tempFileId, this.fileExtension);

    return new Promise((resolve, reject) => {
      const stream = createWriteStream(filePath, {
        flags: 'a',
        highWaterMark: 2 * 1024 * 1024,
      });
      const canWrite = stream.write(chunk);
      if (!canWrite) {
        stream.once('drain', () => {
          stream.end(() => resolve());
        });
      } else {
        stream.end(() => resolve());
      }
      stream.on('error', reject);
    });
  }

  private consecutiveWriteFailures = 0;

  private async writeWithRetries() {
    if (this.writing) return;

    this.writing = true;

    while (this.queue.length > 0) {
      const chunk = this.queue.shift();
      let success = false;
      let attempt = 0;
      const maxRetries = 3;
      const delayMs = 250;

      if (chunk) {
        while (!success && attempt <= maxRetries) {
          try {
            await this.writeChunkToDisk(chunk);
            success = true;
            this.consecutiveWriteFailures = 0; // reset on success
          } catch (err) {
            attempt++;
            if (attempt > maxRetries) {
              this.consecutiveWriteFailures++;
              this.queue.unshift(chunk); // put chunk back at front

              if (this.consecutiveWriteFailures >= this.MAX_GLOBAL_FAILURES) {
                this._logger.error(`Abandoning write after ${this.consecutiveWriteFailures} global failures`, this._meetingId, err);
                this.writing = false;
                return; // give up entirely
              }
              this._logger.info('Temporarily exit disk writing on error', this._meetingId, err);
              break; // exit inner retry loop, but keep outer loop running
            }
            this._logger.error(`Attempt to re-write chunk at attempt ${attempt}:`, this._meetingId, err);
            await new Promise((resolve) => setTimeout(resolve, delayMs * attempt));
          }
        }
      }
    }

    this.writing = false;
  }

  private enqueue(chunk: Buffer) {
    this.queue.push(chunk);

    if (!this.writing) {
      // Non blocking queue
      this.writeWithRetries()
        .then(() => {
          this.diskWriteSuccess.log();
        })
        .catch((err) => {
          this._logger.info('Failure during queue processing to write to disk', this._meetingId);
          throw err;
        });
    }
  }

  public async saveDataToTempFile(data: Buffer) {
    try {
      if (this.forceUpload) {
        // Stop disk writes when the upload or data recovery has started!
        this._logger.info('Force upload is enabled. Stopping disk writes...', this._meetingId, this._meetingId);
        return false;
      }
      this.enqueue(data);
      return true;
    } catch(err) {
      this._logger.info('Error: Unable to save the chunk to disk...', this._meetingId, this._meetingId, err);
      return false;
    }
  }

  private static getFolderPath(meetingId: string) {
    const folderPath = path.join(tempFolder, meetingId);
    return folderPath;
  }

  private static getFilePath(meetingId: string, tempFileId: string, fileExtension: string) {
    const fileName = `${tempFileId}${fileExtension}`;
    const folderPath = DiskUploader.getFolderPath(meetingId);
    const filePath = path.join(folderPath, fileName);
    return filePath;
  }

  private delayPromise(ms: number) {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }

  private static async setupDirectory(folderPath: string, meetingId: string, logger: Logger) {
    try {
      if (!fs.existsSync(folderPath)) {
        logger.info('Temp Directory does not exist. Creating...', meetingId);
        await fs.promises.mkdir(folderPath, { recursive: true });
        logger.info('Temp Directory does not exist. Creation success...', meetingId);
      }
      else {
        logger.info('Found the temp directory already...', meetingId);
      }
    } catch (error) {
      logger.error('Failed to create directory', meetingId, error);
      throw error;
    }
  }

  private async deleteTempFileAsync(): Promise<void> {
    try {
      const filePath = DiskUploader.getFilePath(this._meetingId, this._tempFileId, this.fileExtension);
      const absPath = path.resolve(filePath);
      await fs.promises.unlink(absPath);
      this._logger.info(`Temp File deleted from disk: ${absPath}`, this._meetingId);
    } catch (error) {
      this._logger.warn('Could not clean up temp file:', this._meetingId, error);
    }
  }

  private async tempFileExists(): Promise<boolean> {
    try {
      const filePath = DiskUploader.getFilePath(this._meetingId, this._tempFileId, this.fileExtension);
      await fs.promises.access(filePath);
      return true;
    } catch {
      return false;
    }
  }

  private async waitForWritingFlag() {
    const waitPromise = new Promise((resolve) => {
      const waitInterval = setInterval(() => {
        if (this.writing) {
          this._logger.info('Waiting on finish temp file write...', this._meetingId);
        } else {
          clearInterval(waitInterval);
          resolve(true);
        }
      }, 500);
    });

    await waitPromise;
    this._logger.info('Finish wait on temp file write...', this._meetingId);
  }

  private async finalizeDiskWriting() {
    try {
      await this.waitForWritingFlag();

      // Check if the queue is empty
      if (this.queue.length > 0) {
        // Final attempt to finish the disk write
        await this.writeWithRetries();
      }

      return true;
    } catch(err) {
      this._logger.info('Critical: Failed to finalise temp file write...', this._meetingId, err);
      return false;
    }
  }

  private async uploadRecordingToObjectStorage(): Promise<boolean> {
    const provider = getStorageProvider();
    this._logger.info(`Uploading recording to object storage using provider: ${provider.name}...`);

    const filePath = DiskUploader.getFilePath(this._meetingId, this._tempFileId, this.fileExtension);
    const chunkSize = this.UPLOAD_CHUNK_SIZE;

    // Compose overwrite-safe S3 object key
    const timestamp = new Date().toISOString().replace(/[:.]/g, '-');
    const key = `meetings/${this._meetingId}/recording-${timestamp}-${this._tempFileId}${this.fileExtension}`;

    // Validate provider configuration before attempting upload
    provider.validateConfig();

    const maxAttempts = 2;
    for (let attempt = 1; attempt <= maxAttempts; attempt++) {
      try {
        this._logger.info(`Object storage upload attempt ${attempt} of ${maxAttempts} via ${provider.name}.`);
        const startedAt = Date.now();
        const uploadSuccess = await provider.uploadFile({
          filePath,
          key,
          contentType: this.contentType,
          logger: this._logger,
          partSize: chunkSize,
          concurrency: 4,
        });

        if (!uploadSuccess) {
          throw new Error(`Failed to upload recording to ${provider.name}`);
        }
        const durationMs = Date.now() - startedAt;
        this._logger.info(`Object storage upload success via ${provider.name}. Duration: ${durationMs} ms, Size: unknown (streamed). Key: ${key}`);

        // Build blobUrl + storage details for notifications
        try {
          if (provider.name === 's3') {
            const s3cfg = config.s3CompatibleStorage;
            const uploadCfg = {
              endpoint: s3cfg.endpoint,
              region: s3cfg.region!,
              bucket: s3cfg.bucket!,
              forcePathStyle: !!s3cfg.forcePathStyle,
            };
            this.lastUploadedBlobUrl = this.buildS3CompatibleUrl(uploadCfg, key);
            this.lastStorageDetails = {
              provider: 's3',
              bucket: s3cfg.bucket,
              key,
              region: s3cfg.region,
              endpoint: s3cfg.endpoint,
              forcePathStyle: !!s3cfg.forcePathStyle,
              url: this.lastUploadedBlobUrl,
            };
          } else if (provider.name === 'azure') {
            // Prefer signed URL if method is available
            let url: string | undefined;
            if (typeof (provider as any).getSignedUrl === 'function') {
              try {
                url = await (provider as any).getSignedUrl(key, { expiresInSeconds: config.azureBlobStorage.signedUrlTtlSeconds });
              } catch (e) {
                this._logger.warn('Failed to generate signed URL for Azure blob, falling back to public URL (may be inaccessible without SAS)', e as any);
              }
            }
            // Construct canonical URL if no signed URL available
            if (!url) {
              const account = config.azureBlobStorage.accountName;
              const container = config.azureBlobStorage.container;
              if (account && container) {
                url = `https://${account}.blob.core.windows.net/${container}/${encodeURI(key)}`;
              }
            }
            this.lastUploadedBlobUrl = url;
            this.lastStorageDetails = {
              provider: 'azure',
              accountName: config.azureBlobStorage.accountName,
              container: config.azureBlobStorage.container,
              key,
              url: this.lastUploadedBlobUrl,
              signedUrlTtlSeconds: config.azureBlobStorage.signedUrlTtlSeconds,
              blobPrefix: config.azureBlobStorage.blobPrefix,
            };
          }
        } catch (metaErr) {
          this._logger.warn('Unable to compute storage metadata/url for notification', metaErr as any);
        }
        return true;
      } catch (err) {
        if (attempt >= maxAttempts) {
          this._logger.error(`Permanently failed to upload recording to object storage (${provider.name}) after ${maxAttempts} attempts`, err);
          throw err;
        } else {
          this._logger.error(`Failed to upload recording to object storage (${provider.name}) attempt ${attempt} of ${maxAttempts}`, err);
          const delay = this.RETRY_UPLOAD_DELAY_BASE_MS * Math.pow(2, attempt);
          await this.delayPromise(delay);
        }
      }
    }

    return false;
  }

  private buildS3CompatibleUrl(uploadConfig: { endpoint?: string; region: string; bucket: string; forcePathStyle: boolean; }, key: string): string | undefined {
    try {
      const safeKey = encodeURI(key);
      if (uploadConfig.endpoint) {
        const ep = uploadConfig.endpoint.replace(/\/$/, '');
        if (uploadConfig.forcePathStyle) {
          return `${ep}/${uploadConfig.bucket}/${safeKey}`;
        }
        // Virtual-hosted-style with custom endpoint
        const url = new URL(ep);
        // Prepend bucket as subdomain if possible
        return `${url.protocol}//${uploadConfig.bucket}.${url.host}/${safeKey}`;
      }
      // Default AWS endpoint pattern
      return `https://${uploadConfig.bucket}.s3.${uploadConfig.region}.amazonaws.com/${safeKey}`;
    } catch {
      return undefined;
    }
  }

  public async uploadRecordingToRemoteStorage(options?: { forceUpload?: boolean }) {
    try {
      if (typeof options?.forceUpload === 'boolean') {
        this.forceUpload = options.forceUpload;
      }

      if (!await this.tempFileExists()) {
        throw new Error(`Unable to access the temp recording file on disk: ${this._meetingId} ${this._meetingId}`);
      }

      const goodToGo = await this.finalizeDiskWriting();

      if (this.forceUpload) {
        this._logger.info('Force upload is enabled. Ignoring disk writing check results...', { goodToGo });
      } else if (!goodToGo) {
        throw new Error(`Unable to finalise the temp recording file: ${this._meetingId} ${this._meetingId}`);
      }

      // In live-STT mode without S3/Azure credentials configured, clean up the
      // local temp recording so disk space does not accumulate across meetings.
      const hasRemoteStorage =
        (config.storageProvider === 'azure' && Boolean(config.azureBlobStorage.container || config.azureBlobStorage.connectionString)) ||
        (config.storageProvider === 's3' && Boolean(config.s3CompatibleStorage.bucket && config.s3CompatibleStorage.accessKeyId));

      if (!hasRemoteStorage) {
        this._logger.info('Remote object storage not configured — skipping upload and cleaning temp file', {
          meetingId: this._meetingId,
        });
        await this.deleteTempFileAsync();
        return true;
      }

      // Upload recording to object storage (S3 or Azure)
      const uploadResult = await this.uploadRecordingToObjectStorage();

      // Delete temp file after the upload is finished
      await this.deleteTempFileAsync();

      // Send optional notifications on success
      if (uploadResult) {
        try {
          const payload: RecordingCompletedPayload = {
            recordingId: this.lastRecordingId ?? this._tempFileId,
            meetingLink: this._meetingLink,
            status: 'completed',
            blobUrl: this.lastUploadedBlobUrl,
            timestamp: new Date().toISOString(),
            metadata: {
              meetingId: this._meetingId,
              contentType: this.contentType,
              uploaderType: config.uploaderType,
            },
          };
          await notifyRecordingCompleted(payload, this._logger);
        } catch (notifyErr) {
          this._logger.warn('Recording completed notification failed', notifyErr as any);
        }
      }

      return uploadResult;
    } catch (err) {
      this._logger.info('Unable to upload recording to server...', { error: err, meetingId: this._meetingId });
      return false;
    }
  }
}

export default DiskUploader;
