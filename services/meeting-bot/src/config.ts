import dotenv from 'dotenv';
import { UploaderType } from './types';
import { loggerFactory } from './util/logger';
dotenv.config();

const configLogger = loggerFactory('system', 'config');

const ENVIRONMENTS = [
  'production',
  'staging',
  'development',
  'cli',
  'test',
] as const;

export type Environment = (typeof ENVIRONMENTS)[number];
export const NODE_ENV: Environment = ENVIRONMENTS.includes(
  process.env.NODE_ENV as Environment
)
  ? (process.env.NODE_ENV as Environment)
  : 'staging';

configLogger.info('NODE_ENV loaded', { NODE_ENV: process.env.NODE_ENV });

const requiredSettings = [
  'MIA_BOT_API_KEY',
];
const missingSettings = requiredSettings.filter((s) => !process.env[s]);
if (missingSettings.length > 0) {
  missingSettings.forEach((ms) =>
    configLogger.error('ENV setting is missing', { setting: ms })
  );
}

const constructRedisUri = () => {
  const host = process.env.REDIS_HOST || 'redis';
  const port = process.env.REDIS_PORT || 6379;
  const username = process.env.REDIS_USERNAME;
  const password = process.env.REDIS_PASSWORD;

  if (username && password) {
    return `redis://${username}:${password}@${host}:${port}`;
  } else if (password) {
    return `redis://:${password}@${host}:${port}`;
  } else {
    return `redis://${host}:${port}`;
  }
};

export default {
  port: process.env.PORT || 3000,
  // Mia backend configuration
  miaBackendUrl: process.env.MIA_BACKEND_URL || '', // optional this phase
  miaBotApiKey: process.env.MIA_BOT_API_KEY || '',
  // Unset MAX_RECORDING_DURATION_MINUTES to use default upper limit on duration
  maxRecordingDuration: process.env.MAX_RECORDING_DURATION_MINUTES ?
    Number(process.env.MAX_RECORDING_DURATION_MINUTES) :
    180, // There's an upper limit on meeting duration 3 hours
  chromeExecutablePath: process.env.CHROME_PATH || '/usr/bin/google-chrome', // We use Google Chrome with Playwright for recording
  inactivityLimit: process.env.MEETING_INACTIVITY_MINUTES ? Number(process.env.MEETING_INACTIVITY_MINUTES) : 1,
  activateInactivityDetectionAfter: process.env.INACTIVITY_DETECTION_START_DELAY_MINUTES ? Number(process.env.INACTIVITY_DETECTION_START_DELAY_MINUTES) :  1,
  joinWaitTime: process.env.JOIN_WAIT_TIME_MINUTES ? Number(process.env.JOIN_WAIT_TIME_MINUTES) : 10,
  // Number of retries for transient errors (not applied to WaitingAtLobbyRetryError)
  retryCount: process.env.RETRY_COUNT ? Number(process.env.RETRY_COUNT) : 2,
  redisQueueName: process.env.REDIS_QUEUE_NAME ?? 'jobs:meetbot:list',
  redisUri: constructRedisUri(),
  // Notification: Webhook (disabled by default)
  notifyWebhookEnabled: process.env.NOTIFY_WEBHOOK_ENABLED === 'true',
  notifyWebhookUrl: process.env.NOTIFY_WEBHOOK_URL,
  // Optional secret to sign payloads (HMAC-SHA256). If set, signature will be sent in X-Webhook-Signature header
  notifyWebhookSecret: process.env.NOTIFY_WEBHOOK_SECRET,
  // Notification: Redis (disabled by default). Uses same REDIS connection but selectable DB and list
  notifyRedisEnabled: process.env.NOTIFY_REDIS_ENABLED === 'true',
  // If not provided, uses redisUri with specified database selection
  notifyRedisUri: process.env.NOTIFY_REDIS_URI, // optional override
  notifyRedisDb: process.env.NOTIFY_REDIS_DB ? Number(process.env.NOTIFY_REDIS_DB) : 1, // must not default to 0
  notifyRedisList: process.env.NOTIFY_REDIS_LIST ?? 'jobs:meetbot:recordings',
  uploaderFileExtension: process.env.UPLOADER_FILE_EXTENSION ? process.env.UPLOADER_FILE_EXTENSION : '.webm',
  isRedisEnabled: process.env.REDIS_CONSUMER_ENABLED === 'true',
  s3CompatibleStorage: {
    endpoint: process.env.S3_ENDPOINT,
    region: process.env.S3_REGION,
    accessKeyId: process.env.S3_ACCESS_KEY_ID,
    secretAccessKey: process.env.S3_SECRET_ACCESS_KEY,
    bucket: process.env.S3_BUCKET_NAME,
    forcePathStyle: process.env.S3_USE_MINIO_COMPATIBILITY === 'true',
  },
  // Object storage provider selection: 's3' (default) or 'azure'
  storageProvider: (process.env.STORAGE_PROVIDER === 'azure' ? 'azure' : 's3') as 's3' | 'azure',
  azureBlobStorage: {
    // Either provide full connection string OR account + key/SAS OR managed identity
    connectionString: process.env.AZURE_STORAGE_CONNECTION_STRING,
    accountName: process.env.AZURE_STORAGE_ACCOUNT,
    accountKey: process.env.AZURE_STORAGE_ACCOUNT_KEY, // optional when using connection string
    sasToken: process.env.AZURE_STORAGE_SAS_TOKEN, // starts with ?sv=...
    useManagedIdentity: process.env.AZURE_USE_MANAGED_IDENTITY === 'true',
    container: process.env.AZURE_STORAGE_CONTAINER,
    blobPrefix: process.env.AZURE_BLOB_PREFIX || '',
    signedUrlTtlSeconds: process.env.AZURE_SIGNED_URL_TTL_SECONDS ? Number(process.env.AZURE_SIGNED_URL_TTL_SECONDS) : 3600,
    uploadConcurrency: process.env.AZURE_UPLOAD_CONCURRENCY ? Number(process.env.AZURE_UPLOAD_CONCURRENCY) : 4,
  },
  uploaderType: 's3' as UploaderType,
  // STT pipeline (Faz 1)
  sttEnabled: process.env.STT_ENABLED === 'true',
  pcmChunkMs: Number(process.env.PCM_CHUNK_MS) || 200,
  pcmSampleRate: 16000,
  pulseMonitorDevice: process.env.PULSE_MONITOR_DEVICE || 'virtual_output.monitor',
  // ElevenLabs Scribe v2 Realtime (Faz 2)
  elevenLabsApiKey: process.env.ELEVENLABS_API_KEY || '',
  elevenLabsModelId: process.env.ELEVENLABS_MODEL_ID || 'scribe_v2_realtime',
  elevenLabsRegion: process.env.ELEVENLABS_REGION || '',  // '' = global, 'us' | 'eu' | 'in'
  sttCommitStrategy: process.env.STT_COMMIT_STRATEGY || 'vad',  // 'vad' (ElevenLabs auto-commit on silence) | 'manual' (requires custom VAD)
  sttLanguage: process.env.STT_LANGUAGE || '',  // '' = auto-detect
  // Transcript DB writer (Faz 3)
  transcriptWriteEnabled: process.env.TRANSCRIPT_WRITE_ENABLED === 'true',
  databaseUrl: process.env.DATABASE_URL || '',
  databaseSsl: process.env.DATABASE_SSL !== 'false',  // default true (Supabase)
  // VAD — Voice Activity Detection (Faz 4)
  vadEnabled: process.env.VAD_ENABLED === 'true',
  vadWebrtcAggressiveness: (Number(process.env.VAD_WEBRTC_AGGRESSIVENESS) || 2) as 0 | 1 | 2 | 3,
  vadSileroThreshold: Number(process.env.VAD_SILERO_THRESHOLD) || 0.5,
  vadUtteranceTailMs: Number(process.env.VAD_UTTERANCE_TAIL_MS) || 600,
  vadPendingFrameLookback: process.env.VAD_PENDING_FRAME_LOOKBACK !== 'false',
  // Audio Processing — NoiseGate + AGC (Faz 5)
  noiseGateEnabled: process.env.NOISE_GATE_ENABLED === 'true',
  noiseGateThresholdDb: Number(process.env.NOISE_GATE_THRESHOLD_DB) || -40,
  agcEnabled: process.env.AGC_ENABLED === 'true',
  agcTargetRms: Number(process.env.AGC_TARGET_RMS) || 0.1,
  agcMaxGain: Number(process.env.AGC_MAX_GAIN) || 3.0,
  // Silence Safety Net — Sliding Window (Faz 3a/3b)
  silenceWindowMs: Number(process.env.SILENCE_WINDOW_MS) || 300000,  // 5 min
  silenceRatioThreshold: Number(process.env.SILENCE_RATIO_THRESHOLD) || 0.90,
  // Active Speaker Detection (Faz 6)
  activeSpeakerEnabled: process.env.ACTIVE_SPEAKER_ENABLED === 'true',
  speakerPollingIntervalMs: Number(process.env.SPEAKER_POLLING_INTERVAL_MS) || 300,
  speakerSwitchDelayMs: Number(process.env.SPEAKER_SWITCH_DELAY_MS) || 800,
  // Backend Signals (Faz 7 — Signal Integration)
  miaBackendSignalsEnabled: process.env.MIA_BACKEND_SIGNALS_ENABLED !== 'false', // default true
  miaHeartbeatIntervalMs: Number(process.env.MIA_HEARTBEAT_INTERVAL_MS) || 30000,
  // In-container meeting runner concurrency (default 1 to isolate Xvfb :99 & PulseAudio monitor)
  maxConcurrentMeetings: Number(process.env.MAX_CONCURRENT_MEETINGS) || 1,
  meetingDockerImage: process.env.MEETING_DOCKER_IMAGE || 'ihsan674/mia-meeting-bot:latest',
  // (Faz 9 configs removed — single STT connection replaces per-participant pool)
  // Authenticated join credentials (optional — falls back to guest if not set)
  botAuthEmail: process.env.BOT_AUTH_EMAIL || '',
  botAuthPassword: process.env.BOT_AUTH_PASSWORD || '',
};
