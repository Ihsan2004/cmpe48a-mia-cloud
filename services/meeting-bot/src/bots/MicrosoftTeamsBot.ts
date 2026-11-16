import { JoinParams } from './AbstractMeetBot';
import { BotStatus } from '../types';
import config from '../config';
import { WaitingAtLobbyRetryError, UnsupportedMeetingError } from '../error';
import { MeetBotBase } from './MeetBotBase';
import { v4 } from 'uuid';
import { IUploader } from '../middleware/disk-uploader';
import { Logger } from 'winston';
import { retryActionWithWait } from '../util/resilience';
import createBrowserContext from '../lib/chromium';
import { browserLogCaptureCallback } from '../util/logger';
import { MICROSOFT_REQUEST_DENIED } from '../constants';
import { FFmpegRecorder } from '../lib/ffmpegRecorder';
import * as path from 'path';
import * as fs from 'fs';
import { exec, spawn, ChildProcessWithoutNullStreams } from 'child_process';
import { promisify } from 'util';
import { PcmStreamService } from '../services/pcmStreamService';
import { startSpeakerPolling, injectEarlyDetectorJs } from '../services/speakerPollingService';
import { SttLifecycle } from './AbstractMeetBot';

const execAsync = promisify(exec);

export class MicrosoftTeamsBot extends MeetBotBase {
  private _logger: Logger;
  private _correlationId: string;
  private _lastExitReason: string = 'meeting_ended';
  constructor(logger: Logger, correlationId: string) {
    super();
    this.slightlySecretId = v4();
    this._logger = logger;
    this._correlationId = correlationId;
  }
  async join({ url, name, meetingId, language, uploader, pcmStreamService, sttService, miaBackendClient }: JoinParams): Promise<void> {
    const _state: BotStatus[] = ['processing'];

    const handleUpload = async () => {
      this._logger.info('Begin recording upload to server', { meetingId });
      const uploadResult = await uploader.uploadRecordingToRemoteStorage();
      this._logger.info('Recording upload result', { uploadResult, meetingId });
      return uploadResult;
    };

    try {
      const pushState = (st: BotStatus) => _state.push(st);
      await this.joinMeeting({ url, name, meetingId, language, pushState, uploader, pcmStreamService, sttService, miaBackendClient });

      // Stop heartbeat (Faz 3)
      miaBackendClient?.stopHeartbeat();

      // Report STT usage (Faz 4 usage tracking) — after drain/disconnect, metrics are final
      if (miaBackendClient && sttService) {
        const metrics = sttService.getUsageMetrics();
        await miaBackendClient.sendSTTUsage(metrics);
      }

      // Signal: completed BEFORE upload (Faz 5)
      await miaBackendClient?.sendCompletedEvent(this._lastExitReason);

      // Finish the upload from the temp video
      const uploadResult = await handleUpload();

      if (_state.includes('finished') && !uploadResult) {
        _state.splice(_state.indexOf('finished'), 1, 'failed');
        this._logger.error('Recording completed but upload failed', { meetingId });
      } else if (uploadResult) {
        this._logger.info('Recording and upload completed successfully', { meetingId });
      }

      this._logger.info('Bot completed', { provider: 'microsoft', status: _state });
    } catch(error) {
      // Log the actual error that occurred
      this._logger.error('Error in Microsoft Teams bot join process', {
        error: error instanceof Error ? error.message : String(error),
        stack: error instanceof Error ? error.stack : undefined,
        meetingId,
        currentState: _state
      });

      if (!_state.includes('finished'))
        _state.push('failed');

      // Stop heartbeat + send failed signal (Faz 4)
      // Guard: don't send failed if completed was already sent (e.g. upload error after successful meeting)
      miaBackendClient?.stopHeartbeat();
      if (miaBackendClient && !_state.includes('finished')) {
        const reason = error instanceof WaitingAtLobbyRetryError ? 'lobby_timeout' : 'bot_error';
        await miaBackendClient.sendFailedEvent(reason);
      }

      this._logger.error('Bot failed', { provider: 'microsoft', status: _state });

      if (error instanceof WaitingAtLobbyRetryError)
        this._logger.error('Waiting at lobby error', { provider: 'microsoft', message: error.message });

      throw error;
    }
  }

  private async joinMeeting({ url, name, meetingId, language, pushState, uploader, pcmStreamService, sttService, miaBackendClient }: JoinParams & { pushState(state: BotStatus): void }): Promise<void> {
    // First run: Navigate to pre-join screen to trigger Chrome dialogs, then close
    this._logger.info('Pre-warming: Opening browser to trigger first-run dialogs...');
    try {
      const warmupPage = await createBrowserContext(url, this._correlationId, 'microsoft');
      this._logger.info('Pre-warming: Navigating to Teams meeting...');
      await warmupPage.goto(url, { waitUntil: 'networkidle' });

      await warmupPage.waitForTimeout(2000);

      // Try to click "Join from browser" button
      this._logger.info('Pre-warming: Looking for Join from browser button...');
      const joinButtonSelectors = [
        'button[aria-label="Join meeting from this browser"]',
        'button[aria-label="Continue on this browser"]',
        'button[aria-label="Join on this browser"]',
        'button:has-text("Continue on this browser")',
        'button:has-text("Join from browser")',
      ];

      for (const selector of joinButtonSelectors) {
        try {
          await warmupPage.waitForSelector(selector, { timeout: 5000 });
          this._logger.info(`Pre-warming: Found button with selector: ${selector}`);
          await warmupPage.click(selector, { force: true });
          this._logger.info('Pre-warming: Clicked join from browser button');
          break;
        } catch (err) {
          continue;
        }
      }

      // Wait for pre-join screen to load
      this._logger.info('Pre-warming: Waiting for pre-join screen...');
      await warmupPage.waitForTimeout(5000);

      // Close the warmup browser
      this._logger.info('Pre-warming: Closing warmup browser...');
      await warmupPage.context().browser()?.close();
      this._logger.info('Pre-warming complete - dialogs triggered');

      // Wait 10 seconds for Chrome to fully close and save state before opening again
      this._logger.info('Waiting 10 seconds before opening browser for actual meeting...');
      await new Promise(resolve => setTimeout(resolve, 10000));
    } catch (error) {
      this._logger.warn('Pre-warming failed (non-fatal):', error);
    }

    // Second run: Actual meeting join
    this._logger.info('Launching browser for actual meeting...');

    this.page = await createBrowserContext(url, this._correlationId, 'microsoft');

    // Inject early WebRTC interceptor BEFORE navigation (Faz 3)
    // Must be in place before Teams establishes RTCPeerConnections
    await injectEarlyDetectorJs(this.page, 'microsoft-teams', this._logger);

    await this.page.waitForTimeout(1000);

    this._logger.info('Navigating to Microsoft Teams Meeting URL...');
    await this.page.goto(url, { waitUntil: 'networkidle' });

    // Check if Teams redirected to Microsoft login page (org-only meetings)
    let teamsSignedIn = false;
    const currentUrl = this.page.url();
    if (currentUrl.includes('login.microsoftonline.com') || currentUrl.includes('login.live.com')) {
      if (config.botAuthEmail && config.botAuthPassword) {
        this._logger.info('[Teams] Redirected to Microsoft login — credentials configured, attempting sign-in...');
        await this._doTeamsSignin(config.botAuthEmail, config.botAuthPassword);
        teamsSignedIn = true;
        this._logger.info('[Teams] Sign-in complete, waiting for Teams page to settle...');
        await this.page.waitForTimeout(3000);
      } else {
        throw new UnsupportedMeetingError(
          'Meeting requires Microsoft authentication but no credentials configured (BOT_AUTH_EMAIL / BOT_AUTH_PASSWORD)',
          null,
        );
      }
    }

    // Try to find and click "Join from browser" button
    this._logger.info('Waiting for Join meeting from browser button...');
    const joinButtonSelectors = [
      'button[aria-label="Join meeting from this browser"]',
      'button[aria-label="Continue on this browser"]',
      'button[aria-label="Join on this browser"]',
      'button:has-text("Continue on this browser")',
      'button:has-text("Join from browser")',
    ];

    let buttonClicked = false;
    for (const selector of joinButtonSelectors) {
      try {
        this._logger.info(`Trying selector: ${selector}`);
        await this.page.waitForSelector(selector, { timeout: 60000 });
        this._logger.info(`Found button, clicking: ${selector}`);
        await this.page.click(selector, { force: true });
        buttonClicked = true;
        this._logger.info('Successfully clicked join from browser button');
        break;
      } catch (err) {
        this._logger.info(`Selector not found: ${selector}`);
        continue;
      }
    }

    if (!buttonClicked) {
      this._logger.info('Join from browser button not found, proceeding anyway...');
    }

    this._logger.info('Waiting for pre-join screen to load...');

    // Try to fill name if input field exists
    // When signed in, Teams may skip the name input (uses account display name)
    try {
      this._logger.info('Looking for name input field...');
      const nameInput = this.page.locator('input[data-tid="prejoin-display-name-input"]');
      await nameInput.waitFor({ state: 'visible', timeout: teamsSignedIn ? 15000 : 120000 });

      this._logger.info('Found name input field, filling with bot name...');
      await nameInput.fill(name ? name : 'Mia Notetaker');
      await this.page.waitForTimeout(1000);
    } catch (err) {
      if (teamsSignedIn) {
        this._logger.info('[Teams] Name input not found after sign-in (expected — using account name)');
      } else {
        this._logger.info('Name input field not found, skipping...', err?.message);
      }
    }

    // Toggle off camera and mute microphone before joining
    const toggleDevices = async () => {
      try {
        this._logger.info('Attempting to turn off camera and mute microphone...');
        await this.page.waitForTimeout(2000);

        // Turn off camera
        try {
          const cameraSelectors = [
            'input[data-tid="toggle-video"][checked]',
            'input[type="checkbox"][title*="Turn camera off" i]',
            'input[role="switch"][data-tid="toggle-video"]',
            'button[aria-label*="Turn camera off" i]',
            'button[aria-label*="Camera off" i]',
          ];

          for (const selector of cameraSelectors) {
            const cameraButton = this.page.locator(selector).first();
            const isVisible = await cameraButton.isVisible({ timeout: 2000 }).catch(() => false);
            if (isVisible) {
              const label = await cameraButton.getAttribute('aria-label');
              this._logger.info(`Clicking camera toggle: ${label}`);
              await cameraButton.click();
              await this.page.waitForTimeout(500);
              break;
            }
          }
        } catch (err) {
          this._logger.info('Could not toggle camera', err?.message);
        }

        // Mute microphone
        try {
          const micSelectors = [
            'input[data-tid="toggle-mute"]:not([checked])',
            'input[type="checkbox"][title*="Mute mic" i]',
            'input[role="switch"][data-tid="toggle-mute"]',
            'button[aria-label*="Mute microphone" i]',
            'button[aria-label*="Mute mic" i]',
          ];

          for (const selector of micSelectors) {
            const micButton = this.page.locator(selector).first();
            const isVisible = await micButton.isVisible({ timeout: 2000 }).catch(() => false);
            if (isVisible) {
              const label = await micButton.getAttribute('aria-label');
              this._logger.info(`Clicking microphone toggle: ${label}`);
              await micButton.click();
              await this.page.waitForTimeout(500);
              break;
            }
          }
        } catch (err) {
          this._logger.info('Could not toggle microphone', err?.message);
        }

        this._logger.info('Finished toggling camera and microphone');
      } catch (error) {
        this._logger.warn('Error toggling devices', error?.message);
      }
    };

    await toggleDevices();

    // Check if signed-in bot was directly admitted (no pre-join screen)
    let alreadyInMeeting = false;
    if (teamsSignedIn) {
      try {
        const leaveBtn = this.page.getByRole('button', { name: /Leave/i });
        if (await leaveBtn.isVisible({ timeout: 5000 }).catch(() => false)) {
          this._logger.info('[Teams] Already in meeting (direct-admit for signed-in user) — skipping join button');
          alreadyInMeeting = true;
        }
      } catch {
        // Not in meeting yet — proceed to join button
      }
    }

    if (!alreadyInMeeting) {
      this._logger.info('Clicking the join button...');
      await retryActionWithWait(
        'Clicking the join button',
        async () => {
          // Try different possible button texts
          const possibleTexts = [
            'Join now',
            'Join',
            'Ask to join',
            'Join meeting',
          ];

          let buttonClicked = false;

          for (const text of possibleTexts) {
            try {
              const button = this.page.getByRole('button', { name: new RegExp(text, 'i') });
              if (await button.isVisible({ timeout: 3000 }).catch(() => false)) {
                await button.click();
                buttonClicked = true;
                this._logger.info(`Successfully clicked "${text}" button`);
                break;
              }
            } catch (err) {
              this._logger.info(`Unable to click "${text}" button, trying next...`);
            }
          }

          if (!buttonClicked) {
            throw new Error('Unable to find any join button variant');
          }
        },
        this._logger,
        3,
        15000,
      );
    }

    // Do this to ensure meeting bot has joined the meeting
    try {
      const wanderingTime = config.joinWaitTime * 60 * 1000; // Give some time to be let in
      const callButton = this.page.getByRole('button', { name: /Leave/i });
      await callButton.waitFor({ timeout: wanderingTime });
      this._logger.info('Bot is entering the meeting...');
    } catch (error) {
      const bodyText = await this.page.evaluate(() => document.body.innerText);

      const userDenied = (bodyText || '')?.includes(MICROSOFT_REQUEST_DENIED);

      this._logger.error('Cant finish wait at the lobby check', { userDenied, waitingAtLobbySuccess: false, bodyText });

      this._logger.error('Closing the browser on error...', error);
      await this.page.context().browser()?.close();

      // Don't retry lobby errors - if user doesn't admit bot, retrying won't help
      throw new WaitingAtLobbyRetryError('Microsoft Teams Meeting bot could not enter the meeting...', bodyText ?? '', false, 0);
    }

    pushState('joined');

    // Signal: bot admitted → backend sets bot_status = 'recording' (Faz 2)
    if (miaBackendClient) {
      miaBackendClient.sendJoinedEvent().catch(() => {});
      miaBackendClient.startHeartbeat();
    }

    const dismissDeviceChecksAndNotifications = async () => {
      const notificationCheck = async () => {
        try {
          this._logger.info('Waiting for the "Close" button...');
          await this.page.waitForSelector('button[aria-label=Close]', { timeout: 5000 });
          this._logger.info('Clicking the "Close" button...');
          await this.page.click('button[aria-label=Close]', { timeout: 2000 });
        } catch (error) {
          // Log and ignore this error
          this._logger.info('Turn On notification might be missing...', error);
        }
      };

      const deviceCheck = async () => {
        try {
          this._logger.info('Waiting for the "Close" button...');
          await this.page.waitForSelector('button[title="Close"]', { timeout: 5000 });
    
          this._logger.info('Going to click all visible "Close" buttons...');
    
          let closeButtonsClicked = 0;
          let previousButtonCount = -1;
          let consecutiveNoChangeCount = 0;
          const maxConsecutiveNoChange = 2; // Stop if button count doesn't change for 2 consecutive iterations
    
          while (true) {
            const visibleButtons = await this.page.locator('button[title="Close"]:visible').all();
          
            const currentButtonCount = visibleButtons.length;
            
            if (currentButtonCount === 0) {
              break;
            }
            
            // Check if button count hasn't changed (indicating we might be stuck)
            if (currentButtonCount === previousButtonCount) {
              consecutiveNoChangeCount++;
              if (consecutiveNoChangeCount >= maxConsecutiveNoChange) {
                this._logger.warn(`Button count hasn't changed for ${maxConsecutiveNoChange} iterations, stopping`);
                break;
              }
            } else {
              consecutiveNoChangeCount = 0;
            }
            
            previousButtonCount = currentButtonCount;
    
            for (const btn of visibleButtons) {
              try {
                await btn.click({ timeout: 5000 });
                closeButtonsClicked++;
                this._logger.info(`Clicked a "Close" button #${closeButtonsClicked}`);
                
                await this.page.waitForTimeout(2000);
              } catch (err) {
                this._logger.warn('Click failed, possibly already dismissed', { error: err });
              }
            }
          
            await this.page.waitForTimeout(2000);
          }
        } catch (error) {
          // Log and ignore this error
          this._logger.info('Device permissions modals might be missing...', { error });
        }
      };

      await notificationCheck();
      await deviceCheck();
      this._logger.info('Finished dismissing device checks and notifications...');
    };
    await dismissDeviceChecksAndNotifications();

    // Wait for mic to be fully muted and any initial beeps to stop
    this._logger.info('Waiting 5 seconds for audio to stabilize before recording...');
    await this.page.waitForTimeout(5000);

    // Enable captions via callManager for caption-based speaker detection (Faz 3, Step 3.4)
    try {
      await this.page.evaluate((lang: string) => {
        try {
          const cm = (window as any).callManager;
          if (cm) {
            cm.enableClosedCaptions?.();
            cm.setClosedCaptionsLanguage?.(lang);
            console.log('[MiaTeams] Captions enabled via callManager, lang=' + lang);
          } else {
            console.log('[MiaTeams] callManager not available — DSH + DOM still active');
          }
        } catch(e) {
          console.log('[MiaTeams] Caption enabling failed (non-fatal):', e);
        }
      }, language || 'tr'); // Use meeting language, default to Turkish
      this._logger.info('[Teams] Caption enabling attempted via callManager');
    } catch {
      this._logger.info('[Teams] Caption enabling skipped (non-fatal)');
    }

    // Start speaker detection polling (Faz 6.3)
    const activeSpeakerService = await startSpeakerPolling(this.page, 'microsoft-teams', meetingId, this._logger, name);
    if (activeSpeakerService && sttService?.setActiveSpeakerService) {
      sttService.setActiveSpeakerService(activeSpeakerService);
    }

    // Recording the meeting page with ffmpeg
    this._logger.info('Begin recording with ffmpeg...');
    await this.recordMeetingPageWithFFmpeg({
      meetingId, uploader, pcmStreamService, sttService,
    });

    pushState('finished');
  }

  private async recordMeetingPageWithFFmpeg(
    { meetingId, uploader, pcmStreamService, sttService }:
    { meetingId: string, uploader: IUploader, pcmStreamService?: PcmStreamService, sttService?: SttLifecycle }
  ): Promise<void> {
    // Use config max recording duration (3 hours default) - only for safety
    const duration = config.maxRecordingDuration * 60 * 1000;
    this._logger.info(`Recording max duration set to ${duration / 60000} minutes (safety limit only)`);

    // Use the same temp folder as Google Meet bot (has proper permissions)
    const tempFolder = path.join(process.cwd(), 'dist', '_tempvideo');
    const outputPath = path.join(tempFolder, `recording-${meetingId}.mp4`);

    this._logger.info('Starting ffmpeg recording...', { outputPath, duration });

    // Verify PulseAudio is ready before starting FFmpeg
    this._logger.info('Verifying PulseAudio status before starting FFmpeg...');
    try {
      const execAsync = promisify(exec);

      // Check if PulseAudio process is running
      try {
        const { stdout: psOutput } = await execAsync('ps aux | grep pulseaudio | grep -v grep');
        this._logger.info('PulseAudio process status:', psOutput.trim());
      } catch (err) {
        this._logger.error('PulseAudio process not found!', err);
      }

      // Check XDG_RUNTIME_DIR
      this._logger.info('Environment check:', {
        XDG_RUNTIME_DIR: process.env.XDG_RUNTIME_DIR,
        USER: process.env.USER,
        HOME: process.env.HOME
      });

      // Check if PulseAudio socket exists
      try {
        const socketPath = `${process.env.XDG_RUNTIME_DIR}/pulse/native`;
        const { stdout: socketCheck } = await execAsync(`ls -la ${socketPath}`);
        this._logger.info('PulseAudio socket exists:', socketCheck.trim());
      } catch (err) {
        this._logger.error('PulseAudio socket not found!', err);
      }

      // Try to list sources
      const { stdout: paStatus } = await execAsync('pactl list sources short');
      this._logger.info('PulseAudio sources available:', paStatus.trim() || '(empty - no sources found)');

      const expectedDevice = config.pulseMonitorDevice;
      if (!paStatus.includes(expectedDevice)) {
        this._logger.error(`WARNING: ${expectedDevice} not found in PulseAudio sources!`);
        this._logger.info('Attempting to restart PulseAudio and recreate virtual audio device...');

        // Try to restart PulseAudio
        try {
          await execAsync('pulseaudio --kill || true');
          await execAsync('sleep 1');
          await execAsync('pulseaudio -D --exit-idle-time=-1 --log-level=info');
          await execAsync('sleep 2');
          this._logger.info('Restarted PulseAudio');

          // Recreate the null sink
          await execAsync('pactl load-module module-null-sink sink_name=virtual_output sink_properties=device.description="Virtual_Output"');
          await execAsync('pactl set-default-sink virtual_output');
          this._logger.info('Recreated virtual_output sink and monitor');

          // Verify it worked
          const { stdout: newStatus } = await execAsync('pactl list sources short');
          this._logger.info('PulseAudio sources after restart:', newStatus.trim());
        } catch (err) {
          this._logger.error('Failed to restart PulseAudio or recreate virtual audio device:', err);
        }
      }
    } catch (err) {
      this._logger.error('Error checking PulseAudio status:', err);
    }

    // Create and start ffmpeg recorder
    const recorder = new FFmpegRecorder(outputPath, this._logger, config.pulseMonitorDevice);

    // Track FFmpeg status
    let ffmpegFailed = false;
    let ffmpegError: Error | null = null;

    // parec child process for STT PCM capture (runs alongside FFmpeg)
    let parecProcess: ChildProcessWithoutNullStreams | null = null;

    try {
      await recorder.start();
      this._logger.info('FFmpeg recording started successfully');

      // Start parec for PCM capture if STT is enabled
      if (pcmStreamService) {
        const monitorDevice = config.pulseMonitorDevice;
        this._logger.info('Starting parec for PCM capture', { device: monitorDevice });

        parecProcess = spawn('parec', [
          `--device=${monitorDevice}`,
          '--format=s16le',
          '--rate=16000',
          '--channels=1',
        ]);

        const PCM_CHUNK_SIZE = Math.round(config.pcmSampleRate * 2 * (config.pcmChunkMs / 1000)); // 6400 bytes for 200ms @ 16kHz 16-bit mono
        let pcmBuffer = Buffer.alloc(0);

        parecProcess.stdout.on('data', (data: Buffer) => {
          pcmBuffer = Buffer.concat([pcmBuffer, data]);
          while (pcmBuffer.length >= PCM_CHUNK_SIZE) {
            const chunk = pcmBuffer.subarray(0, PCM_CHUNK_SIZE);
            pcmStreamService.emit(chunk);
            pcmBuffer = pcmBuffer.subarray(PCM_CHUNK_SIZE);
          }
        });

        parecProcess.stderr.on('data', (data: Buffer) => {
          this._logger.warn('parec stderr:', data.toString());
        });

        parecProcess.on('error', (err) => {
          this._logger.error('parec process error:', err);
        });

        parecProcess.on('exit', (code) => {
          this._logger.info('parec process exited', { code });
        });

        this._logger.info('parec PCM capture started');
      }

      // Monitor FFmpeg process - if it dies, stop recording immediately
      recorder.onProcessExit((code) => {
        if (code !== 0 && code !== null) {
          this._logger.error('FFmpeg died unexpectedly during recording', { exitCode: code });
          ffmpegFailed = true;
          ffmpegError = new Error(`FFmpeg exited with code ${code} during recording`);
        }
      });

      // Set up browser-based inactivity detection
      let meetingEnded = false;
      await this.page.exposeFunction('miaMeetEnd', (exitReason?: string) => {
        if (exitReason) this._lastExitReason = exitReason;
        this._logger.info('Meeting ended signal received from browser', { exitReason: this._lastExitReason });
        meetingEnded = true;
      });

      // Capture and forward browser console logs to Node.js logger
      this.page.on('console', async msg => {
        try {
          await browserLogCaptureCallback(this._logger, msg);
        } catch(err) {
          this._logger.info('Playwright chrome logger: Failed to log browser messages...', err?.message);
        }
      });

      // Sliding window silence detection (Item 3a + 3b)
      const silenceWindowMs = config.silenceWindowMs;
      const silenceRatioThreshold = config.silenceRatioThreshold;

      const monitorAudioSilence = async () => {
        try {
          this._logger.info('Starting sliding-window silence detection for Microsoft Teams', {
            silenceWindowMs,
            silenceRatioThreshold,
          });

          const silenceWindow: { ts: number; silent: boolean }[] = [];
          const CHECK_INTERVAL_S = 5;
          const MIN_WINDOW_ENTRIES = Math.ceil(silenceWindowMs / (CHECK_INTERVAL_S * 1000)); // window must be fully populated (e.g. 60 for 5min at 5s intervals)
          let silenceWarningLogged = false;

          const checkInterval = setInterval(async () => {
            try {
              const silenceDevice = config.pulseMonitorDevice;
              const { stdout } = await execAsync(
                `timeout 1 parec --device=${silenceDevice} --format=s16le --rate=16000 --channels=1 2>/dev/null | ` +
                'od -An -td2 -v | awk \'BEGIN{max=0} {for(i=1;i<=NF;i++) {val=($i<0)?-$i:$i; if(val>max) max=val}} END{print max}\''
              );

              const peakLevel = parseInt(stdout.trim()) || 0;
              const silenceThreshold = 200;
              const now = Date.now();

              // Push entry and prune old ones
              silenceWindow.push({ ts: now, silent: peakLevel < silenceThreshold });
              const cutoff = now - silenceWindowMs;
              while (silenceWindow.length > 0 && silenceWindow[0].ts < cutoff) {
                silenceWindow.shift();
              }

              this._logger.debug('Audio level check', { peakLevel, threshold: silenceThreshold, windowSize: silenceWindow.length });

              // Check ratio once we have enough data
              if (silenceWindow.length >= MIN_WINDOW_ENTRIES) {
                const silentCount = silenceWindow.filter(s => s.silent).length;
                const ratio = silentCount / silenceWindow.length;

                if (ratio >= silenceRatioThreshold) {
                  // Item 3b: Check participant count before deciding.
                  // Uses WebRTC primary + multi-locale DOM (same signals as Item 5).
                  // If BOTH say "no participants" → exit. This is the last-resort safety net:
                  // if participant detection (Item 5) is also broken, silence safety must still work.
                  const hasParticipants = await this.page.evaluate(() => {
                    // WebRTC primary — same as getWebRTCAudioReceiverCount() in Item 5
                    const webrtcState = (window as any).__miaWebrtc;
                    if (webrtcState) {
                      let receiverCount = 0;
                      for (const pc of webrtcState.peerConnections) {
                        if (pc.connectionState === 'closed') continue;
                        try {
                          for (const r of pc.getReceivers()) {
                            if (r.track && r.track.kind === 'audio' && r.track.readyState === 'live') {
                              receiverCount++;
                            }
                          }
                        } catch (e) {}
                      }
                      if (receiverCount > 0) return true;
                    }

                    // DOM fallback — multi-locale, synced with getContributorsCount() (Item 6a/6b)
                    const PEOPLE_KEYWORDS = /people|participant|katılımcı|kişi|herkesi|teilnehmer|participantes/i;

                    // Method 1: button aria-label with people keyword
                    const buttons = Array.from(document.querySelectorAll('button'));
                    for (const btn of buttons) {
                      const ariaLabel = btn.getAttribute('aria-label') || '';
                      if (PEOPLE_KEYWORDS.test(ariaLabel)) {
                        const match = btn.textContent?.match(/\d+/);
                        if (match && Number(match[0]) >= 2) return true;
                      }
                    }

                    // Method 2: roster-header text count
                    const rosterHeader = document.querySelector('[data-tid="roster-header"]');
                    if (rosterHeader) {
                      const match = rosterHeader.textContent?.match(/\d+/);
                      if (match && Number(match[0]) >= 2) return true;
                    }

                    // Method 3: roster-button badge count (6b)
                    const rosterBtn = document.querySelector('[data-tid="roster-button"]');
                    if (rosterBtn) {
                      const badge = rosterBtn.querySelector('[data-tid="roster-badge"]')
                        || rosterBtn.querySelector('.badge-count');
                      if (badge) {
                        const match = (badge.textContent || '').match(/\d+/);
                        if (match && Number(match[0]) >= 2) return true;
                      }
                      // Fallback: any short number in roster button text itself
                      const match = (rosterBtn.textContent || '').match(/\d+/);
                      if (match && Number(match[0]) >= 2) return true;
                    }

                    // Method 4: Old Teams — stream elements (6a)
                    const oldStreams = Array.from(
                      document.querySelectorAll('[data-cid="calling-participant-stream"]')
                    ).filter((el: any) => el.clientWidth > 0 && el.clientHeight > 0);
                    if (oldStreams.length >= 2) return true;

                    // Method 5: New Teams — video stream elements (6a)
                    const newStreams = Array.from(
                      document.querySelectorAll('[data-stream-type="Video"]')
                    ).filter((el: any) => el.clientWidth > 0 && el.clientHeight > 0);
                    if (newStreams.length >= 2) return true;

                    // Method 6: Live platform (6a)
                    const liveStreams = document.querySelectorAll('[data-tid="menur1j"]');
                    if (liveStreams.length >= 2) return true;

                    return false;
                  }).catch(() => false);

                  if (hasParticipants) {
                    if (!silenceWarningLogged) {
                      this._logger.warn(`[SilenceSafety] ${(ratio * 100).toFixed(0)}% silence over ${silenceWindow.length} checks but participants present — NOT exiting`, { meetingId });
                      silenceWarningLogged = true;
                    }
                  } else {
                    this._logger.warn(`[SilenceSafety] ${(ratio * 100).toFixed(0)}% silence over ${silenceWindow.length} checks, no participants — exiting`, {
                      meetingId,
                      windowEntries: silenceWindow.length,
                      silentCount,
                    });
                    clearInterval(checkInterval);
                    this._lastExitReason = 'silence_safety';
                    meetingEnded = true;
                  }
                } else {
                  silenceWarningLogged = false;
                }
              }
            } catch (err) {
              this._logger.error('Error checking audio level:', err);
            }
          }, CHECK_INTERVAL_S * 1000);

        } catch (error) {
          this._logger.error('Failed to initialize audio silence detection:', error);
          this._logger.warn('Will rely on participant detection only');
        }
      };

      // Start silence monitoring after delay
      setTimeout(() => {
        monitorAudioSilence();
      }, config.activateInactivityDetectionAfter * 60 * 1000);

      // Inject inactivity detection script
      await this.page.evaluate(
        ({ maxDuration }: { maxDuration: number }) => {
          // Teams meeting ended patterns — EN + TR (Faz 9)
          const meetingEndedPatterns = [
            'the meeting has ended', 'you left the meeting', 'meeting ended',
            'call ended', 'you have left the meeting',
            'toplantı sona erdi', 'toplantıdan ayrıldınız',
          ];
          const botRemovedPatterns = [
            'you were removed from the meeting', 'an organizer removed you',
            'sorry, but you were denied access to the meeting',
            'toplantıdan çıkarıldınız',
          ];

          // Page validity check with exit reason (Faz 7/8)
          const checkPageValidity = (): string | null => {
            try {
              const bodyText = (document.body.innerText || '').toLowerCase();
              const dialogText = (document.querySelector('[role="dialog"]')?.textContent || '').toLowerCase();
              const combinedText = `${bodyText} ${dialogText}`;

              for (const pattern of botRemovedPatterns) {
                if (combinedText.includes(pattern)) {
                  console.warn('Teams bot removed pattern detected:', pattern);
                  return 'bot_removed';
                }
              }
              for (const pattern of meetingEndedPatterns) {
                if (combinedText.includes(pattern)) {
                  console.warn('Teams meeting end pattern detected:', pattern);
                  return 'meeting_ended';
                }
              }

              // Check for basic Teams UI elements
              const hasLeaveButton = document.querySelector('button[aria-label*="Leave" i]') !== null ||
                                    document.querySelector('#hangup-button') !== null;
              if (!hasLeaveButton) {
                const hasMeetingContent = document.querySelector('[data-tid="meeting-stage"]') !== null;
                if (!hasMeetingContent) {
                  console.warn('Teams UI elements not found - page may have changed state');
                  return 'page_invalid';
                }
              }
              return null;
            } catch (error) {
              console.error('Error checking Teams page validity:', error);
              return 'page_invalid';
            }
          };

          // Page validity check every 10 seconds
          setInterval(() => {
            const exitReason = checkPageValidity();
            if (exitReason) {
              console.log('Teams page state changed, ending meeting. Reason:', exitReason);
              (window as any).miaMeetEnd(exitReason);
            }
          }, 10000);

          // Max duration timeout - safety limit (3 hours default in production)
          setTimeout(() => {
            console.log(`Max recording duration (${maxDuration / 60000} minutes) reached, ending meeting`);
            (window as any).miaMeetEnd('max_duration');
          }, maxDuration);
          console.log(`Max duration timeout set to ${maxDuration / 60000} minutes (safety limit)`);

          // Item 5: Resilient participant detection (Google Meet parity)
          // WebRTC primary, DOM veto-only, debounce, grace period, participantsEverSeen
          {
            const GRACE_PERIOD_MS = 2 * 60 * 1000;       // 2 min — let WebRTC connections establish
            const WEBRTC_ZERO_DEBOUNCE = 3;               // 3 consecutive checks (15s) before exit
            const DOM_ALONE_OVERRIDE_DEBOUNCE = 6;        // 6 consecutive checks (30s) — trust DOM over stale WebRTC
            const CHECK_INTERVAL_MS = 5000;               // 5 seconds
            const NO_ONE_JOINED_TIMEOUT_MS = 5 * 60 * 1000; // 5 min no-show → exit

            let participantsEverSeen = false;
            let noOneJoinedStartTime: number | null = null;
            let webrtcZeroCount = 0;
            let domAloneOverrideCount = 0;
            let detectionActive = true;
            let lastLoggedState = '';
            let lastParticipantLogTime = 0;
            const participantDetectionStartTime = Date.now();

            // 5a: WebRTC primary signal — same interceptor as Google Meet
            let lastWebrtcDetailLogTime = 0;

            function getWebRTCAudioReceiverCount(): number {
              const webrtcState = (window as any).__miaWebrtc;
              if (!webrtcState) return -1; // -1 = no WebRTC interceptor
              let count = 0;
              const now = Date.now();
              const shouldLogDetail = now - lastWebrtcDetailLogTime > 15000;
              let totalPCs = 0;
              let closedPCs = 0;
              let totalReceivers = 0;
              let nonLiveReceivers = 0;
              for (const pc of webrtcState.peerConnections) {
                totalPCs++;
                if (pc.connectionState === 'closed') { closedPCs++; continue; }
                try {
                  const receivers = pc.getReceivers();
                  for (const r of receivers) {
                    if (r.track && r.track.kind === 'audio') {
                      totalReceivers++;
                      if (r.track.readyState === 'live') {
                        count++;
                      } else {
                        nonLiveReceivers++;
                      }
                    }
                  }
                } catch (e) {}
              }
              if (shouldLogDetail && (count === 0 || totalReceivers !== count)) {
                console.log(`[ParticipantDebug] WebRTC detail — PCs: ${totalPCs} (closed: ${closedPCs}) | Audio receivers: ${totalReceivers} (live: ${count}, ended: ${nonLiveReceivers})`);
                lastWebrtcDetailLogTime = now;
              }
              return count;
            }

            // 5b: DOM-based participant count (Teams-specific selectors)
            // Item 6a/6b: Extended with BaaS parity selectors for Old/New/Live Teams UI
            function getContributorsCount(): number | undefined {
              const PEOPLE_KEYWORDS = /people|participant|katılımcı|kişi|herkesi|teilnehmer|participantes/i;
              try {
                // Method 1: button[aria-label*=People] with count in text
                const buttons = Array.from(document.querySelectorAll('button'));
                for (const btn of buttons) {
                  const ariaLabel = btn.getAttribute('aria-label') || '';
                  if (PEOPLE_KEYWORDS.test(ariaLabel)) {
                    const match = btn.textContent?.match(/\d+/);
                    if (match) {
                      const count = Number(match[0]);
                      if (!isNaN(count) && count > 0) return count;
                    }
                  }
                }

                // Method 2: Teams roster header (panel open)
                const rosterHeader = document.querySelector('[data-tid="roster-header"]');
                if (rosterHeader) {
                  const match = rosterHeader.textContent?.match(/\d+/);
                  if (match) {
                    const count = Number(match[0]);
                    if (!isNaN(count) && count > 0) return count;
                  }
                }

                // Method 3: Roster button badge count (panel closed — badge visible on toolbar)
                const rosterBtn = document.querySelector('[data-tid="roster-button"]');
                if (rosterBtn) {
                  const badge = rosterBtn.querySelector('[data-tid="roster-badge"]')
                    || rosterBtn.querySelector('.badge-count');
                  if (badge) {
                    const match = badge.textContent?.match(/\d+/);
                    if (match) {
                      const count = Number(match[0]);
                      if (!isNaN(count) && count > 0) return count;
                    }
                  }
                  // Fallback: any short number in the roster button area
                  const match = rosterBtn.textContent?.match(/\d+/);
                  if (match) {
                    const count = Number(match[0]);
                    if (!isNaN(count) && count > 0) return count;
                  }
                }

                // Method 4: Old Teams — participant stream elements (BaaS parity)
                // Filter 0x0 phantom elements that Teams creates as invisible duplicates
                const oldStreams = Array.from(
                  document.querySelectorAll('[data-cid="calling-participant-stream"]')
                ).filter((el: any) => el.clientWidth > 0 && el.clientHeight > 0);
                if (oldStreams.length > 0) return oldStreams.length;

                // Method 5: New Teams — video stream elements (BaaS parity)
                const newStreams = Array.from(
                  document.querySelectorAll('[data-stream-type="Video"]')
                ).filter((el: any) => el.clientWidth > 0 && el.clientHeight > 0);
                if (newStreams.length > 0) return newStreams.length;

                // Method 6: Live platform (BaaS parity)
                const liveStreams = document.querySelectorAll('[data-tid="menur1j"]');
                if (liveStreams.length > 0) return liveStreams.length;

                return undefined; // selectors not found
              } catch (e) {
                return undefined;
              }
            }

            // 5e: Unified decision flow — runs every CHECK_INTERVAL_MS
            let participantCheckTimer: ReturnType<typeof setTimeout>;
            function runParticipantCheck(): void {
              participantCheckTimer = setTimeout(function check() {
                if (!detectionActive) return;

                const now = Date.now();
                const elapsed = now - participantDetectionStartTime;

                // --- Gather signals ---
                const receiverCount = getWebRTCAudioReceiverCount();
                let domCount: number | undefined;
                try {
                  domCount = getContributorsCount();
                } catch (e) {}

                // Update participantsEverSeen from any source
                if (receiverCount > 0) participantsEverSeen = true;
                if (typeof domCount === 'number' && domCount >= 2) participantsEverSeen = true;

                // Log on state change + once per minute
                const currentState = `r=${receiverCount},d=${domCount ?? 'x'},seen=${participantsEverSeen}`;
                if (currentState !== lastLoggedState) {
                  console.log('Participant detection — WebRTC receivers:', receiverCount,
                    '| DOM count:', domCount ?? 'failed',
                    '| everSeen:', participantsEverSeen,
                    '| zeroCount:', webrtcZeroCount);
                  lastLoggedState = currentState;
                  lastParticipantLogTime = now;
                } else if (now - lastParticipantLogTime > 60000) {
                  console.log('Participant detection (periodic) — WebRTC receivers:', receiverCount,
                    '| DOM count:', domCount ?? 'failed',
                    '| everSeen:', participantsEverSeen);
                  lastParticipantLogTime = now;
                }

                // 5c: Grace period — track but don't exit
                if (elapsed < GRACE_PERIOD_MS) {
                  if (elapsed > GRACE_PERIOD_MS - CHECK_INTERVAL_MS - 1000) {
                    console.log(`[ParticipantDebug] Grace period ending in ~${Math.round((GRACE_PERIOD_MS - elapsed) / 1000)}s — WebRTC: ${receiverCount}, DOM: ${domCount ?? 'failed'}, everSeen: ${participantsEverSeen}`);
                  }
                  runParticipantCheck();
                  return;
                }

                // --- No WebRTC interceptor: DOM-only fallback ---
                if (receiverCount === -1) {
                  if (typeof domCount === 'number' && domCount >= 2) {
                    noOneJoinedStartTime = null;
                    webrtcZeroCount = 0;
                    runParticipantCheck();
                    return;
                  }
                  if (typeof domCount === 'undefined') {
                    // DOM parse failure — debounce
                    webrtcZeroCount++;
                    if (webrtcZeroCount < WEBRTC_ZERO_DEBOUNCE) {
                      runParticipantCheck();
                      return;
                    }
                    if (!participantsEverSeen) {
                      if (noOneJoinedStartTime === null) {
                        noOneJoinedStartTime = now;
                        console.log('No participants detected yet (DOM-only, parse failing). Starting no-one-joined countdown (5 min).');
                      }
                      if (now - noOneJoinedStartTime >= NO_ONE_JOINED_TIMEOUT_MS) {
                        console.log('No one joined for 5 minutes after grace period (DOM-only). Ending meeting.');
                        detectionActive = false;
                        (window as any).miaMeetEnd('no_one_joined');
                        return;
                      }
                    }
                    runParticipantCheck();
                    return;
                  }
                  // DOM explicitly says count < 2
                  if (!participantsEverSeen) {
                    if (noOneJoinedStartTime === null) {
                      noOneJoinedStartTime = now;
                      console.log('No participants detected yet (DOM-only). Starting no-one-joined countdown (5 min).');
                    }
                    if (now - noOneJoinedStartTime >= NO_ONE_JOINED_TIMEOUT_MS) {
                      console.log('No one joined for 5 minutes after grace period (DOM-only). Ending meeting.');
                      detectionActive = false;
                      (window as any).miaMeetEnd('no_one_joined');
                      return;
                    }
                  } else {
                    console.log('All participants left (DOM-only, count:', domCount, '). Ending meeting.');
                    detectionActive = false;
                    (window as any).miaMeetEnd('alone');
                    return;
                  }
                  runParticipantCheck();
                  return;
                }

                // --- WebRTC: participants present ---
                if (receiverCount > 0) {
                  noOneJoinedStartTime = null;
                  webrtcZeroCount = 0;

                  // Reverse DOM veto: DOM says bot is alone but WebRTC receivers linger
                  if (participantsEverSeen && typeof domCount === 'number' && domCount < 2) {
                    domAloneOverrideCount++;
                    if (domAloneOverrideCount >= DOM_ALONE_OVERRIDE_DEBOUNCE) {
                      console.log(`[ParticipantDebug] DOM OVERRIDE — WebRTC: ${receiverCount} but DOM: ${domCount} for ${domAloneOverrideCount} consecutive checks (${domAloneOverrideCount * CHECK_INTERVAL_MS / 1000}s). Trusting DOM — everyone left.`);
                      detectionActive = false;
                      (window as any).miaMeetEnd('alone');
                      return;
                    }
                    if (domAloneOverrideCount === 1 || domAloneOverrideCount % 3 === 0) {
                      console.log(`[ParticipantDebug] DOM alone override building: ${domAloneOverrideCount}/${DOM_ALONE_OVERRIDE_DEBOUNCE} — WebRTC: ${receiverCount}, DOM: ${domCount}`);
                    }
                  } else {
                    if (domAloneOverrideCount > 0) {
                      console.log(`[ParticipantDebug] DOM alone override reset (was ${domAloneOverrideCount}) — DOM: ${domCount ?? 'failed'}`);
                    }
                    domAloneOverrideCount = 0;
                  }

                  runParticipantCheck();
                  return;
                }

                // --- WebRTC: 0 receivers — debounce ---
                webrtcZeroCount++;
                console.log(`[ParticipantDebug] WebRTC 0 — debounce ${webrtcZeroCount}/${WEBRTC_ZERO_DEBOUNCE} | DOM: ${domCount ?? 'failed'}`);
                if (webrtcZeroCount < WEBRTC_ZERO_DEBOUNCE) {
                  runParticipantCheck();
                  return;
                }

                // --- WebRTC 0 debounced (15s) ---

                // 5b: DOM veto — if DOM explicitly sees participants, don't exit
                if (typeof domCount === 'number' && domCount >= 2) {
                  console.log(`[ParticipantDebug] DOM VETO — WebRTC: 0 (debounced ${webrtcZeroCount}x) but DOM sees ${domCount} participants. Resetting debounce.`);
                  webrtcZeroCount = 0;
                  runParticipantCheck();
                  return;
                }

                // 5d: Scenario A — No one ever joined
                if (!participantsEverSeen) {
                  if (noOneJoinedStartTime === null) {
                    noOneJoinedStartTime = now;
                    console.log('No participants detected yet. Starting no-one-joined countdown (5 min).');
                  }
                  if (now - noOneJoinedStartTime >= NO_ONE_JOINED_TIMEOUT_MS) {
                    console.log('No one joined for 5 minutes after grace period. Ending meeting.');
                    detectionActive = false;
                    (window as any).miaMeetEnd('no_one_joined');
                    return;
                  }
                  runParticipantCheck();
                  return;
                }

                // 5d: Scenario B — Everyone left (WebRTC 0 debounced, DOM didn't veto)
                console.log('All participants left — WebRTC 0 (debounced). Ending meeting.');
                detectionActive = false;
                (window as any).miaMeetEnd('alone');
              }, CHECK_INTERVAL_MS);
            }

            // 5f: Start immediately — grace period is handled internally
            runParticipantCheck();
          }
        },
        {
          maxDuration: duration,
        }
      );

      // Wait for either timeout, meeting end, or FFmpeg failure
      const startTime = Date.now();
      while (!meetingEnded && !ffmpegFailed && (Date.now() - startTime) < duration) {
        await new Promise(resolve => setTimeout(resolve, 1000));
      }

      this._logger.info('Recording period ended', {
        meetingEnded,
        ffmpegFailed,
        recordedDuration: Math.floor((Date.now() - startTime) / 1000) + 's'
      });

      // If FFmpeg failed during recording, throw the error
      if (ffmpegFailed && ffmpegError) {
        throw ffmpegError;
      }

    } catch (error) {
      // If recorder.start() failed or any other error occurred, mark FFmpeg as failed
      this._logger.error('Error during recording:', error);
      ffmpegFailed = true;
      ffmpegError = error instanceof Error ? error : new Error(String(error));
      // Re-throw to be caught by outer try/catch in joinMeeting
      throw error;
    } finally {
      // Stop parec if running
      if (parecProcess && !parecProcess.killed) {
        this._logger.info('Stopping parec PCM capture...');
        parecProcess.kill('SIGTERM');
        parecProcess = null;
      }
      if (pcmStreamService) {
        pcmStreamService.end();
      }

      // Graceful STT shutdown — drain pending transcripts, disconnect, flush segments
      if (sttService) {
        if (!sttService.isDisabled()) {
          try {
            this._logger.info('STT: draining pending transcripts...');
            await sttService.drain(5000);
          } catch (err) {
            this._logger.error('STT: drain error (non-fatal)', {
              error: err instanceof Error ? err.message : String(err),
            });
          }
        }
        // Always disconnect to ensure WebSocket is closed (even if disabled mid-session)
        sttService.disconnect();
        this._logger.info('STT: disconnected');
        try {
          await sttService.finalFlush();
        } catch (err) {
          this._logger.error('STT: finalFlush error (non-fatal)', {
            error: err instanceof Error ? err.message : String(err),
          });
        }
      }

      // Always stop ffmpeg
      this._logger.info('Stopping ffmpeg recording...');
      await recorder.stop();

      // Upload the recorded file
      this._logger.info('Uploading recorded file...', { outputPath });

      let uploadSuccess = false;
      if (fs.existsSync(outputPath)) {
        const fileBuffer = fs.readFileSync(outputPath);
        await uploader.saveDataToTempFile(fileBuffer);

        // Clean up the temporary file
        fs.unlinkSync(outputPath);
        this._logger.info('Recording uploaded and temporary file cleaned up');
        uploadSuccess = true;
      } else {
        this._logger.error('Recording file not found!', { outputPath });
      }

      // Close browser
      this._logger.info('Closing the browser...');
      await this.page.context().browser()?.close();

      // Log final status
      if (ffmpegFailed) {
        this._logger.error('Recording failed due to FFmpeg error', { meetingId });
      } else if (!uploadSuccess) {
        this._logger.error('Recording completed but file upload failed', { meetingId });
      } else {
        this._logger.info('Recording completed successfully', { meetingId });
      }
    }
  }

  /**
   * Perform Microsoft sign-in when Teams redirects to login.microsoftonline.com.
   * Handles: email step → password step → "Stay signed in?" → redirect back to Teams.
   */
  private async _doTeamsSignin(email: string, password: string): Promise<void> {
    const SIGNIN_TIMEOUT = 20_000;

    this._logger.info('[TeamsSignin] Starting sign-in flow', { email });

    // "Pick an account" screen → click "Use another account"
    try {
      const useAnother = this.page.locator('#otherTile, text=/use another account/i').first();
      if (await useAnother.isVisible({ timeout: 2000 })) {
        this._logger.info('[TeamsSignin] "Pick an account" screen detected, clicking "Use another account"');
        await useAnother.click({ timeout: 5000 });
        await this.page.waitForTimeout(2000);
      }
    } catch {
      // Not on account picker — continue
    }

    // Email step
    const emailInput = this.page.locator('input[type="email"], input[name="loginfmt"]').first();
    this._logger.info('[TeamsSignin] Waiting for email input field...');
    await emailInput.waitFor({ state: 'visible', timeout: SIGNIN_TIMEOUT });

    this._logger.info('[TeamsSignin] Filling email');
    await emailInput.fill(email, { timeout: 10_000 });

    // Click "Next"
    const submitBtn = this.page.locator('input[type="submit"], button[type="submit"]').first();
    await submitBtn.click({ timeout: 5000 });

    this._logger.info('[TeamsSignin] Email submitted, waiting for password step...');

    // Password step
    const passwordInput = this.page.locator('input[type="password"], input[name="passwd"]').first();
    try {
      await passwordInput.waitFor({ state: 'visible', timeout: 15_000 });
    } catch {
      // Check if we left Microsoft login (SSO/ADFS redirect) — still need to reach Teams
      const currentUrl = this.page.url();
      if (!currentUrl.includes('login.microsoftonline.com') && !currentUrl.includes('login.live.com')) {
        this._logger.info('[TeamsSignin] Left Microsoft login after email (SSO redirect) — waiting for Teams redirect...');
        await this.page.waitForURL(
          (url) => url.href.includes('teams.microsoft.com') || url.href.includes('teams.live.com'),
          { timeout: SIGNIN_TIMEOUT },
        );
        this._logger.info('[TeamsSignin] SSO redirect complete, back on Teams');
        return;
      }
      throw new Error('Password field did not appear after email submission');
    }

    this._logger.info('[TeamsSignin] Filling password');
    await passwordInput.fill(password, { timeout: 10_000 });

    // Click "Sign in"
    const signInBtn = this.page.locator('input[type="submit"], button[type="submit"]').first();
    await signInBtn.click({ timeout: 5000 });

    this._logger.info('[TeamsSignin] Password submitted, waiting for redirect...');

    // "Stay signed in?" prompt — click "Yes" to keep session
    try {
      const staySignedIn = this.page.locator('input[type="submit"][value="Yes"], button:has-text("Yes")').first();
      await staySignedIn.waitFor({ state: 'visible', timeout: 5000 });
      this._logger.info('[TeamsSignin] "Stay signed in?" prompt — clicking Yes');
      await staySignedIn.click({ timeout: 5000 });
    } catch {
      this._logger.info('[TeamsSignin] No "Stay signed in?" prompt (skipped or auto-redirected)');
    }

    // Wait for redirect back to Teams
    await this.page.waitForURL(
      (url) => url.href.includes('teams.microsoft.com') || url.href.includes('teams.live.com'),
      { timeout: SIGNIN_TIMEOUT },
    );
    this._logger.info('[TeamsSignin] Sign-in complete, back on Teams');
  }
}
