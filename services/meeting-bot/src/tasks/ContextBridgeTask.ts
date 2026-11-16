import { Page } from 'playwright';
import { Task } from '../lib/Task';
import { JoinParams } from '../bots/AbstractMeetBot';
import { WaitPromise } from '../types';
import { IUploader } from '../middleware/disk-uploader';
import { Logger } from 'winston';
import { browserLogCaptureCallback } from '../util/logger';
import { PcmStreamService } from '../services/pcmStreamService';

export class ContextBridgeTask extends Task<null, void> {
  private page: Page;
  private uploader: IUploader;
  private slightlySecretId: string;
  private waitingPromise: WaitPromise;
  private pcmStreamService?: PcmStreamService;
  private onExitReason?: (reason: string) => void;

  constructor(
    page: Page,
    params: JoinParams,
    slightlySecretId: string,
    waitingPromise: WaitPromise,
    uploader: IUploader,
    logger: Logger,
    pcmStreamService?: PcmStreamService,
    onExitReason?: (reason: string) => void,
  ) {
    super(logger);
    this.page = page;
    this.slightlySecretId = slightlySecretId;
    this.waitingPromise = waitingPromise;
    this.uploader = uploader;
    this.pcmStreamService = pcmStreamService;
    this.onExitReason = onExitReason;
  }

  protected async execute(input: null): Promise<void> {
    // Capture and send the browser console logs to Node.js context
    this.page?.on('console', async msg => {
      try {
        await browserLogCaptureCallback(this._logger, msg);
      } catch(err) {
        this._logger.info('Failed to log browser messages...', err?.message);
      }
    });

    await this.page.exposeFunction('miaSendData', async (slightlySecretId: string, data: string) => {
      if (slightlySecretId !== this.slightlySecretId) return;

      const buffer = Buffer.from(data, 'base64');
      await this.uploader.saveDataToTempFile(buffer);
    });

    await this.page.exposeFunction('miaMeetEnd', (slightlySecretId: string, exitReason?: string) => {
      if (slightlySecretId !== this.slightlySecretId) return;
      try {
        if (exitReason && this.onExitReason) this.onExitReason(exitReason);
        this._logger.info('Early signal resolve recording', { exitReason });
        this.waitingPromise.resolveEarly();
      } catch (error) {
        this._logger.error('Could not process meeting end event', error);
      }
    });

    // Expose PCM data bridge for STT pipeline
    if (this.pcmStreamService) {
      const pcmStreamService = this.pcmStreamService;
      await this.page.exposeFunction('miaPcmData', (slightlySecretId: string, base64PcmChunk: string) => {
        if (slightlySecretId !== this.slightlySecretId) return;
        const buffer = Buffer.from(base64PcmChunk, 'base64');
        pcmStreamService.emit(buffer);
      });
    }
  }
}
