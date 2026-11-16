/**
 * CLI Entry Point — runs a single meeting in container mode.
 * No Express server — reads params from env vars, joins meeting, exits.
 *
 * Usage: node dist/cli.js
 * Env vars: MEETING_URL, BOT_NAME, MEETING_ID, MEETING_LANGUAGE
 */
import './shims/crypto-polyfill';
import config from './config';
import { detectProvider, createSttPipeline } from './app/common';
import { createCorrelationId, loggerFactory } from './util/logger';
import { encodeFileNameSafebase64 } from './util/strings';
import DiskUploader, { IUploader } from './middleware/disk-uploader';
import { GoogleMeetBot } from './bots/GoogleMeetBot';
import { MicrosoftTeamsBot } from './bots/MicrosoftTeamsBot';
import { ZoomBot } from './bots/ZoomBot';
import { PcmStreamService } from './services/pcmStreamService';
import { SttLifecycle } from './bots/AbstractMeetBot';
import { createMiaBackendClient } from './services/miaBackendClient';
import { closePool } from './lib/database';

const cliLogger = loggerFactory('system', 'cli');

const meetingUrl = process.env.MEETING_URL || '';
const botName = process.env.BOT_NAME || "Mia'nın Asistanı";
const meetingId = process.env.MEETING_ID || '';
const language = process.env.MEETING_LANGUAGE || undefined;

if (!meetingUrl || !meetingId) {
  cliLogger.error('MEETING_URL and MEETING_ID environment variables are required');
  process.exit(1);
}

const provider = detectProvider(meetingUrl);
if (!provider) {
  cliLogger.error('Unsupported meeting URL', { meetingUrl });
  process.exit(1);
}

const correlationId = createCorrelationId({ meetingId, url: meetingUrl });
const logger = loggerFactory(correlationId, provider);

let shutdownInProgress = false;

async function run(): Promise<void> {
  logger.info('CLI mode: starting meeting', { meetingId, provider, meetingUrl });

  const tempFileId = encodeFileNameSafebase64(`${meetingId}0`);
  const uploader: IUploader = await DiskUploader.initialize(meetingId, tempFileId, logger, meetingUrl);
  const miaBackendClient = createMiaBackendClient(meetingId, meetingUrl, logger);

  // Single STT connection: mixed audio → single ElevenLabs WebSocket
  // Speaker attribution handled by ActiveSpeakerService (speaker timeline + overlap scoring)
  let pcmStreamService: PcmStreamService | undefined;
  let sttService: SttLifecycle | undefined;

  if (config.sttEnabled) {
    pcmStreamService = new PcmStreamService(meetingId, logger);
    sttService = await createSttPipeline(pcmStreamService, meetingId, meetingUrl, language, logger);
  }

  const joinParams = { url: meetingUrl, name: botName, meetingId, language, uploader, pcmStreamService, sttService, miaBackendClient };

  switch (provider) {
    case 'google': {
      const bot = new GoogleMeetBot(logger, correlationId);
      await bot.join(joinParams);
      break;
    }
    case 'microsoft': {
      const bot = new MicrosoftTeamsBot(logger, correlationId);
      await bot.join(joinParams);
      break;
    }
    case 'zoom': {
      const bot = new ZoomBot(logger, correlationId);
      await bot.join(joinParams);
      break;
    }
  }
}

async function cleanup(): Promise<void> {
  if (shutdownInProgress) return;
  shutdownInProgress = true;
  logger.info('CLI mode: cleaning up...');
  await closePool();
}

// Signal handling for graceful shutdown
process.on('SIGTERM', async () => {
  logger.info('CLI mode: SIGTERM received');
  await cleanup();
  process.exit(0);
});

process.on('SIGABRT', async () => {
  logger.info('CLI mode: SIGABRT received');
  await cleanup();
  process.exit(0);
});

// Main execution
run()
  .then(async () => {
    logger.info('CLI mode: meeting completed successfully', { meetingId });
    await cleanup();
    process.exit(0);
  })
  .catch(async (error) => {
    logger.error('CLI mode: meeting failed', { meetingId, error: error instanceof Error ? error.message : String(error) });
    await cleanup();
    process.exit(1);
  });
