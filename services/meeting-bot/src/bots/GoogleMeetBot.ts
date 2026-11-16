import { JoinParams } from './AbstractMeetBot';
import { BotStatus, WaitPromise } from '../types';
import config from '../config';
import { UnsupportedMeetingError, WaitingAtLobbyRetryError } from '../error';
import { MeetBotBase } from './MeetBotBase';
import { v4 } from 'uuid';
import { IUploader } from '../middleware/disk-uploader';
import { Logger } from 'winston';
import { browserLogCaptureCallback } from '../util/logger';
import { getWaitingPromise } from '../lib/promise';
import { retryActionWithWait } from '../util/resilience';
import createBrowserContext from '../lib/chromium';
import { GOOGLE_LOBBY_MODE_HOST_TEXT, GOOGLE_REQUEST_DENIED, GOOGLE_REQUEST_TIMEOUT } from '../constants';
import { vp9MimeType, webmMimeType } from '../lib/recording';
import { startSpeakerPolling, injectEarlyDetectorJs } from '../services/speakerPollingService';

export class GoogleMeetBot extends MeetBotBase {
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
      await this.joinMeeting({ url, name, meetingId, language, uploader, pcmStreamService, sttService, miaBackendClient, pushState });

      // Stop heartbeat (Faz 3)
      miaBackendClient?.stopHeartbeat();

      // Report STT usage (Faz 4 usage tracking) — after drain/disconnect, metrics are final
      if (miaBackendClient && sttService) {
        const metrics = sttService.getUsageMetrics();
        await miaBackendClient.sendSTTUsage(metrics);
      }

      // Signal: completed BEFORE upload → backend starts post-meeting pipeline immediately (Faz 5)
      // Transcripts are already in DB (written during meeting), so pipeline can start now
      await miaBackendClient?.sendCompletedEvent(this._lastExitReason);

      // Finish the upload from the temp video (runs after completed signal)
      const uploadResult = await handleUpload();

      if (_state.includes('finished') && !uploadResult) {
        _state.splice(_state.indexOf('finished'), 1, 'failed');
      }

      this._logger.info('Bot completed', { provider: 'google', status: _state });
    } catch(error) {
      if (!_state.includes('finished'))
        _state.push('failed');

      // Stop heartbeat + send failed signal (Faz 4)
      // Guard: don't send failed if completed was already sent (e.g. upload error after successful meeting)
      miaBackendClient?.stopHeartbeat();
      if (miaBackendClient && !_state.includes('finished')) {
        const reason = error instanceof WaitingAtLobbyRetryError ? 'lobby_timeout'
          : error instanceof UnsupportedMeetingError ? 'unsupported_meeting'
          : 'bot_error';
        await miaBackendClient.sendFailedEvent(reason);
      }

      this._logger.error('Bot failed', { provider: 'google', status: _state, error: error instanceof Error ? error.message : String(error) });

      if (error instanceof WaitingAtLobbyRetryError) {
        this._logger.error('Waiting at lobby error', { provider: 'google', message: error.message });
      }

      if (error instanceof UnsupportedMeetingError) {
        this._logger.error('Unsupported meeting error', { provider: 'google', message: error.message });
      }

      throw error;
    }
  }

  private async joinMeeting({ url, name, meetingId, language, pushState, uploader, pcmStreamService, sttService, miaBackendClient }: JoinParams & { pushState(state: BotStatus): void }): Promise<void> {
    this._logger.info('Launching browser...');

    this.page = await createBrowserContext(url, this._correlationId, 'google');

    // Inject WebRTC monkey-patches before navigation (Faz 6.5)
    await injectEarlyDetectorJs(this.page, 'google-meet', this._logger);

    this._logger.info('Navigating to Google Meet URL...');
    await this.page.goto(url, { waitUntil: 'networkidle' });

    this._logger.info('Waiting for page to settle...');
    await this.page.waitForTimeout(3000);

    const dismissDeviceCheck = async () => {
      try {
        this._logger.info('Clicking Continue without microphone and camera button...');
        await this.page.getByRole('button', { name: 'Continue without microphone and camera' }).waitFor({ timeout: 5000 });
        await this.page.getByRole('button', { name: 'Continue without microphone and camera' }).click();
        this._logger.info('Dismissed device check dialog.');
      } catch (dismissError) {
        this._logger.info('Continue without microphone and camera button not found — skipping (devices likely available).');
      }
    };

    await dismissDeviceCheck();

    // Debug: log all inputs and buttons on the page to find correct selectors
    try {
      const pageDebugInfo = await this.page.evaluate(() => {
        const inputs = Array.from(document.querySelectorAll('input')).map(el => ({
          type: el.type,
          ariaLabel: el.getAttribute('aria-label'),
          placeholder: el.placeholder,
          name: el.name,
          id: el.id,
        }));
        const buttons = Array.from(document.querySelectorAll('button')).filter(b => (b as HTMLElement).offsetParent !== null).map(el => ({
          text: (el as HTMLElement).innerText?.trim().slice(0, 50),
          ariaLabel: el.getAttribute('aria-label'),
        }));
        return { inputs, buttons, url: window.location.href, bodySnippet: document.body.innerText?.slice(0, 500) };
      });
      this._logger.info('DEBUG pre-join page state', pageDebugInfo);
    } catch (e) {
      this._logger.warn('DEBUG page eval failed', { error: e?.message });
    }

    const verifyItIsOnGoogleMeetPage = async (): Promise<'SIGN_IN_PAGE' | 'GOOGLE_MEET_PAGE' | 'UNSUPPORTED_PAGE' | null> => {
      try {
        const detectSignInPage = async () => {
          let result = false;
          const url = await this.page.url();
          if (url.startsWith('https://accounts.google.com/')) {
            this._logger.info('Google Meet bot is on the sign in page...', { meetingId });
            result = true;
          }
          const signInPage = await this.page.locator('h1', { hasText: 'Sign in' });
          if (await signInPage.count() > 0 && await signInPage.isVisible()) {
            this._logger.info('Google Meet bot is on the page with "Sign in" heading...', { meetingId });
            result = result && true;
          }
          return result;
        };
        const pageUrl = await this.page.url();
        if (!pageUrl.includes('meet.google.com')) {
          const signInPage = await detectSignInPage();
          return signInPage ? 'SIGN_IN_PAGE' : 'UNSUPPORTED_PAGE';
        }
        return 'GOOGLE_MEET_PAGE';
      } catch(e) {
        this._logger.error('Error verifying if Google Meet bot is on the Google Meet page...', { error: e, message: e?.message });
        return null;
      }
    };

    const googleMeetPageStatus = await verifyItIsOnGoogleMeetPage();
    let signedIn = false;

    if (googleMeetPageStatus === 'SIGN_IN_PAGE') {
      if (config.botAuthEmail && config.botAuthPassword) {
        this._logger.info('Meeting requires sign in — credentials configured, attempting login...', { meetingId });
        await this._doGoogleSignin(config.botAuthEmail, config.botAuthPassword);
        signedIn = true;
        // Wait for Meet pre-join page to load after sign-in
        this._logger.info('Sign-in complete, waiting for Meet page to settle...');
        await this.page.waitForTimeout(3000);
        await dismissDeviceCheck();
      } else {
        this._logger.info('Meeting requires sign in but no credentials configured', { googleMeetPageStatus, meetingId });
        throw new UnsupportedMeetingError('Meeting requires sign in', googleMeetPageStatus);
      }
    }

    if (googleMeetPageStatus === 'UNSUPPORTED_PAGE') {
      this._logger.info('Google Meet bot is on the unsupported page...', { googleMeetPageStatus, meetingId });
    }

    // Name field: when signed in, Meet may skip the name input ("Join as <account name>")
    let nameFieldFound = false;
    try {
      this._logger.info('Waiting for the input field to be visible...');
      await retryActionWithWait(
        'Waiting for the input field',
        async () => await this.page.waitForSelector('input[type="text"][aria-label="Your name"]', { timeout: 10000 }),
        this._logger,
        signedIn ? 1 : 2, // fewer retries if signed in (name field may not exist)
        3000,
      );
      nameFieldFound = true;
    } catch {
      if (signedIn) {
        this._logger.info('Name field not found after sign-in (expected — using account name)');
      } else {
        throw new Error('Name input field not found on pre-join page');
      }
    }

    if (nameFieldFound) {
      await this.page.waitForTimeout(1000);
      this._logger.info('Filling the input field with the name...');
      await this.page.fill('input[type="text"][aria-label="Your name"]', name ? name : 'Mia Notetaker');
    }

    this._logger.info('Name filled, clicking join button...');
    await this.page.waitForTimeout(1000);
    
    await retryActionWithWait(
      'Clicking the "Ask to join" button',
      async () => {
        // Using the Order of most probable detection
        const possibleTexts = [
          'Ask to join',
          'Join now',
          'Join anyway',
        ];

        let buttonClicked = false;

        for (const text of possibleTexts) {
          try {
            const button = await this.page.locator('button', { hasText: new RegExp(text.toLocaleLowerCase(), 'i') }).first();
            if (await button.count() > 0) {
              await button.click({ timeout: 5000 });
              buttonClicked = true;
              this._logger.info(`Success clicked using "${text}" action...`);
              break;
            }
          } catch(err) {
            this._logger.warn(`Unable to click using "${text}" action...`);
          }
        }

        // Throws to initiate retries
        if (!buttonClicked) {
          throw new Error('Unable to complete the join action...');
        }
      },
      this._logger,
      2,
      3000,
    );

    // Do this to ensure meeting bot has joined the meeting

    try {
      const wanderingTime = config.joinWaitTime * 60 * 1000; // Give some time to admit the bot

      let waitTimeout: NodeJS.Timeout;
      let waitInterval: NodeJS.Timeout;

      const waitAtLobbyPromise = new Promise<boolean>((resolveWaiting) => {
        waitTimeout = setTimeout(() => {
          clearInterval(waitInterval);
          resolveWaiting(false);
        }, wanderingTime);

        waitInterval = setInterval(async () => {
          try {
            const detectLobbyModeHostWaitingText = async (): Promise<'WAITING_FOR_HOST_TO_ADMIT_BOT' | 'WAITING_REQUEST_TIMEOUT' | 'LOBBY_MODE_NOT_ACTIVE' | 'UNABLE_TO_DETECT_LOBBY_MODE'> => {
              try {
                const lobbyModeHostWaitingText = await this.page.getByText(GOOGLE_LOBBY_MODE_HOST_TEXT);
                if (await lobbyModeHostWaitingText.count() > 0 && await lobbyModeHostWaitingText.isVisible()) {
                  return 'WAITING_FOR_HOST_TO_ADMIT_BOT';
                }

                const lobbyModeRequestTimeoutText = await this.page.getByText(GOOGLE_REQUEST_TIMEOUT);
                if (await lobbyModeRequestTimeoutText.count() > 0 && await lobbyModeRequestTimeoutText.isVisible()) {
                  return 'WAITING_REQUEST_TIMEOUT';
                }

                return 'LOBBY_MODE_NOT_ACTIVE';
              }
              catch (e) {
                this._logger.error('Error detecting lobby mode host waiting text...', { error: e, message: e?.message });
                return 'UNABLE_TO_DETECT_LOBBY_MODE';
              }
            };

            let peopleElement;
            let callButtonElement;
            let botWasDeniedAccess = false;

            try {
              peopleElement = await this.page.waitForSelector('button[aria-label="People"]', { timeout: 5000 });
            } catch(e) {
              this._logger.error(
                'wait error', { error: e }
              );
              //do nothing
            }

            try {
              callButtonElement = await this.page.waitForSelector('button[aria-label="Leave call"]', { timeout: 5000 });
            } catch(e) {
              this._logger.error(
                'wait error', { error: e }
              );
              //do nothing
            }

            if (peopleElement || callButtonElement) {
              // Here check the "lobby mode" that waits for the Host to join the meeting or for the Host to admit the bot
              const lobbyModeHostWaitingText = await detectLobbyModeHostWaitingText();
              if (lobbyModeHostWaitingText === 'WAITING_FOR_HOST_TO_ADMIT_BOT') {
                this._logger.info('Lobbdy Mode: Google Meet Bot is waiting for the host to admit it...', { meetingId });
              } else if (lobbyModeHostWaitingText === 'WAITING_REQUEST_TIMEOUT') {
                this._logger.info('Lobby Mode: Google Meet Bot join request timed out...', { meetingId });
                clearInterval(waitInterval);
                clearTimeout(waitTimeout);
                resolveWaiting(false);
                return;
              } else {
                // Additional check: Verify we can actually see participants (not just UI buttons)
                // The "Leave call" button can exist even in lobby waiting state
                try {
                  const participantCountDetected = await this.page.evaluate(() => {
                    try {
                      // Look for People button with participant count
                      const peopleButton = document.querySelector('button[aria-label^="People"]');
                      if (peopleButton) {
                        const ariaLabel = peopleButton.getAttribute('aria-label');
                        // Check if we can see participant count (e.g., "People - 2 joined")
                        const match = ariaLabel?.match(/People.*?(\d+)/);
                        if (match && parseInt(match[1]) >= 1) {
                          return true;
                        }
                      }

                      // Alternative: Check if participant count is visible in the DOM
                      const allButtons = Array.from(document.querySelectorAll('button'));
                      for (const btn of allButtons) {
                        const label = btn.getAttribute('aria-label');
                        if (label && /People.*?\d+/.test(label)) {
                          return true;
                        }
                      }

                      // Fallback: Check for text that indicates we're in the call
                      const bodyText = document.body.innerText;
                      if (bodyText.includes('You have joined the call') ||
                          bodyText.includes('other person in the call') ||
                          bodyText.includes('people in the call')) {
                        return true;
                      }

                      // Fallback: Check for Leave call button which indicates we're in a call
                      const leaveCallButton = document.querySelector('button[aria-label="Leave call"]');
                      if (leaveCallButton) {
                        // If we have Leave call button AND no lobby mode text, we're likely in the call
                        const hasLobbyText = bodyText.includes('Asking to join') ||
                                            bodyText.includes('You\'re the only one here');
                        if (!hasLobbyText) {
                          return true;
                        }
                      }

                      return false;
                    } catch (e) {
                      return false;
                    }
                  });

                  if (participantCountDetected) {
                    this._logger.info('Google Meet Bot is entering the meeting...', { meetingId });
                    clearInterval(waitInterval);
                    clearTimeout(waitTimeout);
                    resolveWaiting(true);
                    return;
                  } else {
                    this._logger.info('People button found but participant count not visible yet - continuing to wait...', { meetingId });
                    return;
                  }
                } catch (e) {
                  this._logger.error('Error checking participant visibility', { error: e });
                  return;
                }
              }              
            }

            try {
              const deniedText = await this.page.getByText(GOOGLE_REQUEST_DENIED);
              if (await deniedText.count() > 0 && await deniedText.isVisible()) {
                botWasDeniedAccess = true;
              }
            }
            catch(e) {
              //do nothing
            }
            if (botWasDeniedAccess) {
              this._logger.info('Google Meet Bot is denied access to the meeting...', { meetingId });
              clearInterval(waitInterval);
              clearTimeout(waitTimeout);
              resolveWaiting(false);
            }
          } catch(e) {
            this._logger.error(
              'wait error', { error: e }
            );
            // Do nothing
          }
        }, 20000);
      });

      const waitingAtLobbySuccess = await waitAtLobbyPromise;
      if (!waitingAtLobbySuccess) {
        const bodyText = await this.page.evaluate(() => document.body.innerText);

        const userDenied = (bodyText || '')?.includes(GOOGLE_REQUEST_DENIED);

        this._logger.error('Cant finish wait at the lobby check', { userDenied, waitingAtLobbySuccess, bodyText });

        // Don't retry lobby errors - if user doesn't admit bot, retrying won't help
        throw new WaitingAtLobbyRetryError('Google Meet bot could not enter the meeting...', bodyText ?? '', false, 0);
      }
    } catch(lobbyError) {
      this._logger.info('Closing the browser on error...', lobbyError);
      await this.page.context().browser()?.close();

      throw lobbyError;
    }

    pushState('joined');

    // Signal: bot admitted → backend sets bot_status = 'recording' (Faz 2)
    if (miaBackendClient) {
      miaBackendClient.sendJoinedEvent().catch(() => {});
      miaBackendClient.startHeartbeat();
    }

    try {
      this._logger.info('Waiting for the "Got it" button...');
      await this.page.waitForSelector('button:has-text("Got it")', { timeout: 15000 });

      this._logger.info('Going to click all visible "Got it" buttons...');

      let gotItButtonsClicked = 0;
      let previousButtonCount = -1;
      let consecutiveNoChangeCount = 0;
      const maxConsecutiveNoChange = 2; // Stop if button count doesn't change for 2 consecutive iterations

      while (true) {
        const visibleButtons = await this.page.locator('button:visible', {
          hasText: 'Got it',
        }).all();
      
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
            gotItButtonsClicked++;
            this._logger.info(`Clicked a "Got it" button #${gotItButtonsClicked}`);
            
            await this.page.waitForTimeout(2000);
          } catch (err) {
            this._logger.warn('Click failed, possibly already dismissed', { error: err });
          }
        }
      
        await this.page.waitForTimeout(2000);
      }
    } catch (error) {
      // Log and ignore this error
      this._logger.info('"Got it" modals might be missing...', { error });
    }

    // Dismiss "Microphone not found" and "Camera not found" notifications if present
    try {
      this._logger.info('Checking for device notifications (microphone/camera)...');
      const hasDeviceNotification = await this.page.evaluate(() => {
        return document.body.innerText.includes('Microphone not found') ||
               document.body.innerText.includes('Make sure your microphone is plugged in') ||
               document.body.innerText.includes('Camera not found') ||
               document.body.innerText.includes('Make sure your camera is plugged in');
      });

      if (hasDeviceNotification) {
        this._logger.info('Found device notification (microphone/camera), attempting to dismiss...');
        // Try to find and click all close buttons
        const closeButtonsCount = await this.page.evaluate(() => {
          const allButtons = Array.from(document.querySelectorAll('button'));
          const closeButtons = allButtons.filter((btn) => {
            const ariaLabel = btn.getAttribute('aria-label');
            const hasCloseIcon = btn.querySelector('svg') !== null;
            return (ariaLabel?.toLowerCase().includes('close') ||
                    ariaLabel?.toLowerCase().includes('dismiss') ||
                    (hasCloseIcon && btn?.offsetParent !== null && btn.innerText === ''));
          });

          let clickedCount = 0;
          closeButtons.forEach((btn) => {
            if (btn?.offsetParent !== null) {
              btn.click();
              clickedCount++;
            }
          });
          return clickedCount;
        });

        if (closeButtonsCount > 0) {
          this._logger.info(`Successfully dismissed ${closeButtonsCount} device notification(s)`);
          await this.page.waitForTimeout(1000);
        } else {
          this._logger.warn('Could not find close button for device notifications');
        }
      }
    } catch (error) {
      this._logger.info('Error checking/dismissing device notifications...', { error });
    }

    // Start speaker detection polling (Faz 6.3)
    const botDisplayName = name || 'Mia Notetaker';
    const activeSpeakerService = await startSpeakerPolling(this.page, 'google-meet', meetingId, this._logger, botDisplayName);
    if (activeSpeakerService && sttService?.setActiveSpeakerService) {
      sttService.setActiveSpeakerService(activeSpeakerService);
    }

    // Recording the meeting page — returns exit reason (Faz 8)
    this._logger.info('Begin recording...');
    this._lastExitReason = await this.recordMeetingPage({ meetingId, uploader, pcmStreamService, sttService });

    pushState('finished');
  }

  private async recordMeetingPage(
    { meetingId, uploader, pcmStreamService, sttService }:
    { meetingId: string, uploader: IUploader, pcmStreamService?: import('../services/pcmStreamService').PcmStreamService, sttService?: import('./AbstractMeetBot').SttLifecycle }
  ): Promise<string> {
    const duration = config.maxRecordingDuration * 60 * 1000;
    const inactivityLimit = config.inactivityLimit * 60 * 1000;

    // Capture and send the browser console logs to Node.js context
    this.page?.on('console', async msg => {
      try {
        await browserLogCaptureCallback(this._logger, msg);
      } catch(err) {
        this._logger.info('Playwright chrome logger: Failed to log browser messages...', err?.message);
      }
    });

    await this.page.exposeFunction('miaSendData', async (slightlySecretId: string, data: string) => {
      if (slightlySecretId !== this.slightlySecretId) return;

      const buffer = Buffer.from(data, 'base64');
      await uploader.saveDataToTempFile(buffer);
    });

    // Exit reason tracking (Faz 8) — set by browser context, read by Node.js after recording ends
    let _exitReason: string = 'meeting_ended';

    await this.page.exposeFunction('miaMeetEnd', (slightlySecretId: string, exitReason?: string) => {
      if (slightlySecretId !== this.slightlySecretId) return;
      try {
        if (exitReason) _exitReason = exitReason;
        this._logger.info('Attempt to end meeting early...', { exitReason: _exitReason });
        waitingPromise.resolveEarly();
      } catch (error) {
        this._logger.error('Could not process meeting end event', { error });
      }
    });

    // PCM stream for real-time STT (Faz 1.2) — mixed audio → single STT connection
    if (pcmStreamService) {
      await this.page.exposeFunction('miaPcmData', (slightlySecretId: string, base64PcmChunk: string) => {
        if (slightlySecretId !== this.slightlySecretId) return;
        const buffer = Buffer.from(base64PcmChunk, 'base64');
        pcmStreamService.emit(buffer);
      });
    }

    // Inject the MediaRecorder code into the browser context using page.evaluate
    await this.page.evaluate(
      async ({ meetingId, duration, inactivityLimit, slightlySecretId, activateInactivityDetectionAfter, activateInactivityDetectionAfterMinutes, primaryMimeType, secondaryMimeType, sttEnabled, pcmChunkMs, silenceWindowMs, silenceRatioThreshold }:
      { meetingId: string, duration: number, inactivityLimit: number, slightlySecretId: string, activateInactivityDetectionAfter: string, activateInactivityDetectionAfterMinutes: number, primaryMimeType: string, secondaryMimeType: string, sttEnabled: boolean, pcmChunkMs: number, silenceWindowMs: number, silenceRatioThreshold: number }) => {
        let timeoutId: NodeJS.Timeout;
        let inactivityParticipantDetectionTimeout: NodeJS.Timeout;
        let inactivitySilenceDetectionTimeout: NodeJS.Timeout;
        let isOnValidGoogleMeetPageInterval: NodeJS.Timeout;

        const sendChunkToServer = async (chunk: ArrayBuffer) => {
          function arrayBufferToBase64(buffer: ArrayBuffer) {
            let binary = '';
            const bytes = new Uint8Array(buffer);
            for (let i = 0; i < bytes.byteLength; i++) {
              binary += String.fromCharCode(bytes[i]);
            }
            return btoa(binary);
          }
          const base64 = arrayBufferToBase64(chunk);
          await (window as any).miaSendData(slightlySecretId, base64);
        };

        async function startRecording() {
          console.log('Will activate the inactivity detection after', activateInactivityDetectionAfter);

          // Check for the availability of the mediaDevices API
          if (!navigator.mediaDevices || !navigator.mediaDevices.getDisplayMedia) {
            console.error('MediaDevices or getDisplayMedia not supported in this browser.');
            return;
          }
          
          const stream: MediaStream = await (navigator.mediaDevices as any).getDisplayMedia({
            video: true,
            audio: {
              autoGainControl: false,
              channels: 2,
              channelCount: 2,
              echoCancellation: false,
              noiseSuppression: false,
            },
            preferCurrentTab: true,
          });

          // Check if we actually got audio tracks
          const audioTracks = stream.getAudioTracks();
          const hasAudioTracks = audioTracks.length > 0;
          
          if (!hasAudioTracks) {
            console.warn('No audio tracks available for silence detection. Will rely only on presence detection.');
          }

          let options: MediaRecorderOptions = {};
          if (MediaRecorder.isTypeSupported(primaryMimeType)) {
            console.log(`Media Recorder will use ${primaryMimeType} codecs...`);
            options = { mimeType: primaryMimeType };
          }
          else {
            console.warn(`Media Recorder did not find primary mime type codecs ${primaryMimeType}, Using fallback codecs ${secondaryMimeType}`);
            options = { mimeType: secondaryMimeType };
          }

          const mediaRecorder = new MediaRecorder(stream, { ...options });

          mediaRecorder.ondataavailable = async (event: BlobEvent) => {
            if (!event.data.size) {
              console.warn('Received empty chunk...');
              return;
            }
            try {
              const arrayBuffer = await event.data.arrayBuffer();
              sendChunkToServer(arrayBuffer);
            } catch (error) {
              console.error('Error uploading chunk:', error);
            }
          };

          // Start recording with 2-second intervals
          const chunkDuration = 2000;
          mediaRecorder.start(chunkDuration);

          // AudioWorklet for real-time PCM capture (STT pipeline)
          let pcmAudioContext: AudioContext | null = null;
          if (sttEnabled && hasAudioTracks) {
            try {
              const pcmSampleRate = 16000;
              const pcmBufferSize = Math.round(pcmSampleRate * (pcmChunkMs / 1000)); // 200ms @ 16kHz = 3200

              // AudioWorkletProcessor source code — registered as blob URL
              const processorCode = `
                class PcmProcessor extends AudioWorkletProcessor {
                  constructor() {
                    super();
                    this.buffer = new Float32Array(${pcmBufferSize});
                    this.offset = 0;
                  }
                  process(inputs) {
                    const input = inputs[0][0];
                    if (!input) return true;
                    for (let i = 0; i < input.length; i++) {
                      this.buffer[this.offset++] = input[i];
                      if (this.offset >= ${pcmBufferSize}) {
                        const int16 = new Int16Array(${pcmBufferSize});
                        for (let j = 0; j < ${pcmBufferSize}; j++) {
                          int16[j] = Math.max(-32768, Math.min(32767, Math.round(this.buffer[j] * 32767)));
                        }
                        this.port.postMessage(int16, [int16.buffer]);
                        this.offset = 0;
                      }
                    }
                    return true;
                  }
                }
                registerProcessor('pcm-processor', PcmProcessor);
              `;
              const blob = new Blob([processorCode], { type: 'application/javascript' });
              const blobUrl = URL.createObjectURL(blob);

              pcmAudioContext = new AudioContext({ sampleRate: pcmSampleRate });
              if (pcmAudioContext.state === 'suspended') {
                await pcmAudioContext.resume();
              }

              // Use only audio tracks for PCM capture
              const audioOnlyStream = new MediaStream(stream.getAudioTracks());
              const source = pcmAudioContext.createMediaStreamSource(audioOnlyStream);

              await pcmAudioContext.audioWorklet.addModule(blobUrl);
              const workletNode = new AudioWorkletNode(pcmAudioContext, 'pcm-processor');
              source.connect(workletNode);

              workletNode.port.onmessage = (e: MessageEvent) => {
                // e.data = Int16Array, convert to base64 and send to Node.js
                const int16Array: Int16Array = e.data;
                const bytes = new Uint8Array(int16Array.buffer);
                let binary = '';
                for (let i = 0; i < bytes.byteLength; i++) {
                  binary += String.fromCharCode(bytes[i]);
                }
                const base64 = btoa(binary);
                (window as any).miaPcmData(slightlySecretId, base64);
              };

              URL.revokeObjectURL(blobUrl);
              console.log('AudioWorklet PCM capture started', { sampleRate: pcmSampleRate, chunkMs: pcmChunkMs, bufferSize: pcmBufferSize });
            } catch (pcmError) {
              console.error('Failed to start AudioWorklet PCM capture — STT will not work but recording continues', pcmError);
              pcmAudioContext = null;
            }
          } else if (sttEnabled && !hasAudioTracks) {
            console.warn('STT enabled but no audio tracks available — PCM capture skipped');
          }

          let dismissModalsInterval: NodeJS.Timeout;
          let lastDimissError: Error | null = null;

          const stopTheRecording = async (exitReason?: string) => {
            mediaRecorder.stop();
            stream.getTracks().forEach((track) => track.stop());

            // Cleanup AudioWorklet PCM capture
            if (pcmAudioContext) {
              try {
                await pcmAudioContext.close();
                console.log('AudioWorklet PCM capture stopped');
              } catch (e) {
                console.error('Error closing PCM AudioContext', e);
              }
            }

            // Cleanup recording timer
            clearTimeout(timeoutId);

            // Cancel the perpetural checks
            if (inactivityParticipantDetectionTimeout) {
              clearTimeout(inactivityParticipantDetectionTimeout);
            }
            if (inactivitySilenceDetectionTimeout) {
              clearTimeout(inactivitySilenceDetectionTimeout);
            }

            if (loneTest) {
              clearTimeout(loneTest);
            }

            if (isOnValidGoogleMeetPageInterval) {
              clearInterval(isOnValidGoogleMeetPageInterval);
            }

            if (dismissModalsInterval) {
              clearInterval(dismissModalsInterval);
              if (lastDimissError && lastDimissError instanceof Error) {
                console.error('Error dismissing modals:', { lastDimissError, message: lastDimissError?.message });
              }
            }

            // Begin browser cleanup — pass exit reason to Node.js (Faz 8)
            (window as any).miaMeetEnd(slightlySecretId, exitReason || 'meeting_ended');
          };

          let loneTest: NodeJS.Timeout;
          let loneTestDetectionActive = true;
          let participantsEverSeen = false;
          let noOneJoinedStartTime: number | null = null;
          let webrtcZeroCount = 0; // debounce: consecutive checks with 0 receivers
          let domAloneOverrideCount = 0; // debounce: consecutive checks where DOM < 2 but WebRTC > 0
          let lastParticipantLogTime = 0;
          let lastLoggedState = ''; // for state-change-only logging
          const participantDetectionStartTime = Date.now();
          const GRACE_PERIOD_MS = 2 * 60 * 1000; // 2 min — let WebRTC receivers establish
          const NO_ONE_JOINED_TIMEOUT_MS = 5 * 60 * 1000; // 5 min after grace period
          const WEBRTC_ZERO_DEBOUNCE = 3; // 3 consecutive checks (15s) before exit decision
          const DOM_ALONE_OVERRIDE_DEBOUNCE = 6; // 6 consecutive checks (30s) — trust DOM over stale WebRTC
          const CHECK_INTERVAL_MS = 5000; // 5 seconds

          let lastWebrtcDetailLogTime = 0;

          function getWebRTCAudioReceiverCount(): number {
            const webrtcState = (window as any).__miaWebrtc;
            if (!webrtcState) return -1; // -1 = no WebRTC interceptor
            let count = 0;
            const now = Date.now();
            const shouldLogDetail = now - lastWebrtcDetailLogTime > 15000; // detail every 15s
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

          function detectLoneParticipantResilient(): void {
            const re = /^[0-9]+$/;

            function getContributorsCount(): number | undefined {
              // Multi-locale keywords for participant button detection
              const PEOPLE_KEYWORDS = /people|participant|katılımcı|kişi|herkesi|teilnehmer|participantes/i;
              const PEOPLE_COUNT_REGEX = /(\d+)\s*(participant|people|katılımcı|kişi|joined|teilnehmer)/i;

              function findPeopleButton() {
                try {
                  // 1. Direct aria-label selectors (EN + TR + DE + ES)
                  const directSelectors = [
                    'button[aria-label^="People -"]',
                    'button[aria-label*="People"]',
                    'button[aria-label*="Katılımcı" i]',
                    'button[aria-label*="Kişi" i]',
                    'button[aria-label*="herkesi" i]',
                    'button[aria-label*="Teilnehmer" i]',
                    'button[aria-label*="Participantes" i]',
                    // Legacy UI: "Show everyone" label (BaaS parity)
                    'button[aria-label="Show everyone"]',
                    'button[data-tooltip="Show everyone"]',
                  ];
                  for (const sel of directSelectors) {
                    const btn = document.querySelector(sel);
                    if (btn) return btn;
                  }

                  // 2. Try via aria-labelledby pointing to element with people keyword
                  const allBtns = Array.from(document.querySelectorAll('button[aria-labelledby]'));
                  let btn: Element | undefined = allBtns.find(b => {
                    const labelledBy = b.getAttribute('aria-labelledby');
                    if (labelledBy) {
                      const labelElement = document.getElementById(labelledBy);
                      if (labelElement && PEOPLE_KEYWORDS.test(labelElement.textContent?.trim() || '')) {
                        return true;
                      }
                    }
                    return false;
                  });
                  if (btn) return btn;

                  // 3. Try via regex on aria-label (multi-locale count patterns)
                  const allBtnsWithLabel = Array.from(document.querySelectorAll('button[aria-label]'));
                  btn = allBtnsWithLabel.find(b => {
                    const label = b.getAttribute('aria-label') || '';
                    return PEOPLE_COUNT_REGEX.test(label);
                  });
                  if (btn) return btn;

                  // 4. Fallback: icon with "people" text or tooltip match
                  btn = allBtnsWithLabel.find(b => {
                    // Child icon with "people" text
                    const hasIcon = Array.from(b.querySelectorAll('i')).some(i =>
                      i.textContent && i.textContent.trim() === 'people'
                    );
                    if (hasIcon) return true;
                    // Tooltip or data-tooltip match
                    const tooltip = b.getAttribute('data-tooltip') || '';
                    return PEOPLE_KEYWORDS.test(tooltip);
                  });
                  if (btn) return btn;

                  // 5. New UI: dialog button (Dec 2025+)
                  const dialogBtns = Array.from(document.querySelectorAll('div[role="button"][aria-haspopup="dialog"]'));
                  for (const db of dialogBtns) {
                    const combined = [
                      db.getAttribute('aria-label'),
                      db.getAttribute('data-tooltip'),
                      db.textContent?.trim(),
                    ].filter(Boolean).join(' ');
                    if (PEOPLE_KEYWORDS.test(combined)) return db;
                  }

                  // 6. Not found
                  return null;
                } catch (error) {
                  console.log('Error finding people button:', error);
                  return null;
                }
              }

              // Find participant count badge near People button (doesn't require opening panel)
              try {
                const peopleBtn = findPeopleButton();
                // console.log('[Detection] People button found:', !!peopleBtn);

                if (peopleBtn) {
                  // Search INSIDE the button (descendants) and nearby (parent container)
                  const searchRoots = [
                    peopleBtn, // Search inside button itself
                    peopleBtn.parentElement,
                    peopleBtn.parentElement?.parentElement
                  ].filter(Boolean);

                  // console.log('[Detection] Searching', searchRoots.length, 'containers');

                  for (const searchRoot of searchRoots) {
                    if (!searchRoot) continue;

                    // Method 1: Look for data-avatar-count attribute (most reliable)
                    const avatarSpan = searchRoot.querySelector('[data-avatar-count]');
                    if (avatarSpan) {
                      const countAttr = avatarSpan.getAttribute('data-avatar-count');
                      // console.log('[Detection] Method 1 SUCCESS - data-avatar-count:', countAttr);
                      const count = Number(countAttr);
                      if (!isNaN(count) && count > 0) {
                        return count;
                      }
                    }

                    // Method 2: Fallback - Look for number in badge div
                    const badgeDiv = searchRoot.querySelector('div.egzc7c') as HTMLElement;
                    if (badgeDiv) {
                      const text = ((badgeDiv.innerText || badgeDiv.textContent) ?? '').trim();
                      if (text.length > 0 && text.length <= 3 && re.test(text)) {
                        const count = Number(text);
                        if (!isNaN(count) && count > 0) {
                          // console.log('[Detection] Method 2 SUCCESS - Badge text:', text);
                          return count;
                        }
                      }
                    }
                  }

                  // Method 3: Extract count from aria-label (e.g. "People - 3 joined", "Katılımcılar - 3")
                  const btnLabel = peopleBtn.getAttribute('aria-label') || '';
                  const countMatch = btnLabel.match(PEOPLE_COUNT_REGEX);
                  if (countMatch) {
                    const count = Number(countMatch[1]);
                    if (!isNaN(count) && count > 0) return count;
                  }

                  // Method 4: Last resort - search for short numbers in People button area
                  const mainSearchRoot = peopleBtn.parentElement?.parentElement || peopleBtn;
                  const allDivs = Array.from(mainSearchRoot.querySelectorAll('div'));
                  for (const div of allDivs) {
                    const text = ((div as HTMLElement).innerText || div.textContent || '').trim();
                    if (text.length > 0 && text.length <= 3 && re.test(text)) {
                      const isVisible = (div as HTMLElement).offsetParent !== null;
                      if (isVisible) {
                        const count = Number(text);
                        if (!isNaN(count) && count > 0) {
                          // console.log('[Detection] Method 3 SUCCESS - Found number:', count);
                          return count;
                        }
                      }
                    }
                  }
                  // console.log('[Detection] All methods failed to find count');
                } else {
                  // console.log('[Detection] People button NOT found');
                }
              } catch (error) {
                console.log('Error finding participant badge:', error);
              }

              // Method 5: Panel-based — if Participants panel is open, read count from panel
              // When panel is open the badge may be hidden, so we parse panel text/listitems
              try {
                const PANEL_KEYWORDS = /participants?|katılımcılar?|teilnehmer|participantes?/i;
                const CONTRIBUTORS_REGEX = /(?:contributors?|katılımcılar?|teilnehmer|participantes?)\s*(\d+)/i;
                const IN_MEETING_REGEX = /^(?:in the meeting|toplantıda|in der besprechung|en la reunión)$/i;

                // 5a: Find panel by aria-label
                const panelSelectors = [
                  '[aria-label="Participants"]',
                  '[aria-label="People"]',
                  '[aria-label*="Katılımcı" i]',
                  '[aria-label*="Teilnehmer" i]',
                  '[aria-label*="Participantes" i]',
                ];
                let participantsPanel: Element | null = null;
                for (const sel of panelSelectors) {
                  participantsPanel = document.querySelector(sel);
                  if (participantsPanel) break;
                }

                if (participantsPanel) {
                  // 5b: "Contributors X" or equivalent text in panel
                  const panelText = participantsPanel.textContent || '';
                  const contribMatch = panelText.match(CONTRIBUTORS_REGEX);
                  if (contribMatch) {
                    const count = Number(contribMatch[1]);
                    if (!isNaN(count) && count > 0) return count;
                  }

                  // 5c: Count [role="listitem"] elements inside panel (BaaS pattern)
                  const listItems = participantsPanel.querySelectorAll('[role="listitem"]');
                  if (listItems.length > 0) return listItems.length;
                }

                // Method 6: "IN THE MEETING" section header — find count near it
                // Google Meet shows "IN THE MEETING" → "Contributors  1"
                const allElements = Array.from(document.querySelectorAll('span, div, h2, h3'));
                for (const el of allElements) {
                  const text = (el.textContent || '').trim();
                  if (IN_MEETING_REGEX.test(text)) {
                    const parent = el.parentElement;
                    if (parent) {
                      const parentText = parent.textContent || '';
                      const countMatch = parentText.match(CONTRIBUTORS_REGEX);
                      if (countMatch) {
                        const count = Number(countMatch[1]);
                        if (!isNaN(count) && count > 0) return count;
                      }
                      // Fallback: any number near the heading
                      const numMatch = parentText.match(/(\d+)/);
                      if (numMatch) {
                        const count = Number(numMatch[1]);
                        if (!isNaN(count) && count > 0) return count;
                      }
                    }
                  }
                }
              } catch (error) {
                console.log('Error in panel-based participant detection:', error);
              }

              return undefined;
            }
          
            function runParticipantCheck(): void {
              loneTest = setTimeout(function check() {
                if (!loneTestDetectionActive) {
                  if (loneTest) clearTimeout(loneTest);
                  return;
                }

                const now = Date.now();
                const elapsed = now - participantDetectionStartTime;

                // --- Gather signals ---
                const receiverCount = getWebRTCAudioReceiverCount();
                let domCount: number | undefined;
                try {
                  domCount = getContributorsCount();
                } catch (e) {
                  // DOM detection error, treat as failed
                }

                // Update participantsEverSeen from any source
                if (receiverCount > 0) participantsEverSeen = true;
                if (typeof domCount === 'number' && domCount >= 2) participantsEverSeen = true;

                // Log on state change + once per minute as summary
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

                // --- Grace period: track but don't exit ---
                if (elapsed < GRACE_PERIOD_MS) {
                  if (elapsed > GRACE_PERIOD_MS - CHECK_INTERVAL_MS - 1000) {
                    console.log(`[ParticipantDebug] Grace period ending in ~${Math.round((GRACE_PERIOD_MS - elapsed) / 1000)}s — WebRTC: ${receiverCount}, DOM: ${domCount ?? 'failed'}, everSeen: ${participantsEverSeen}`);
                  }
                  runParticipantCheck();
                  return;
                }

                // --- No WebRTC interceptor: DOM-only fallback ---
                if (receiverCount === -1) {
                  // DOM-only mode: same participantsEverSeen + no_one_joined logic
                  if (typeof domCount === 'number' && domCount >= 2) {
                    // DOM sees participants — stay
                    noOneJoinedStartTime = null;
                    webrtcZeroCount = 0; // reuse as DOM-fail debounce counter
                    runParticipantCheck();
                    return;
                  }
                  if (typeof domCount === 'undefined') {
                    // DOM parse failure — we don't know if anyone is here
                    // Don't treat as "everyone left", debounce like WebRTC
                    webrtcZeroCount++;
                    if (webrtcZeroCount < WEBRTC_ZERO_DEBOUNCE) {
                      runParticipantCheck();
                      return;
                    }
                    // Debounced: DOM consistently failing
                    if (!participantsEverSeen) {
                      if (noOneJoinedStartTime === null) {
                        noOneJoinedStartTime = now;
                        console.log('No participants detected yet (DOM-only, parse failing). Starting no-one-joined countdown (5 min).');
                      }
                      if (now - noOneJoinedStartTime >= NO_ONE_JOINED_TIMEOUT_MS) {
                        console.log('No one joined for 5 minutes after grace period (DOM-only). Ending meeting.');
                        loneTestDetectionActive = false;
                        stopTheRecording('no_one_joined');
                        return;
                      }
                    }
                    // participantsEverSeen = true but DOM failing → can't confirm, keep waiting
                    // Safety net: silence detection (Item 3a) will catch truly dead meetings
                    runParticipantCheck();
                    return;
                  }
                  // DOM explicitly returned count < 2 — bot is alone
                  if (!participantsEverSeen) {
                    if (noOneJoinedStartTime === null) {
                      noOneJoinedStartTime = now;
                      console.log('No participants detected yet (DOM-only). Starting no-one-joined countdown (5 min).');
                    }
                    if (now - noOneJoinedStartTime >= NO_ONE_JOINED_TIMEOUT_MS) {
                      console.log('No one joined for 5 minutes after grace period (DOM-only). Ending meeting.');
                      loneTestDetectionActive = false;
                      stopTheRecording('no_one_joined');
                      return;
                    }
                  } else {
                    // Everyone left — DOM explicitly says count < 2
                    console.log('All participants left (DOM-only, count:', domCount, '). Ending meeting.');
                    loneTestDetectionActive = false;
                    stopTheRecording('alone');
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
                  // Trust DOM after 30s of consistent "alone" readings (6 checks)
                  if (participantsEverSeen && typeof domCount === 'number' && domCount < 2) {
                    domAloneOverrideCount++;
                    if (domAloneOverrideCount >= DOM_ALONE_OVERRIDE_DEBOUNCE) {
                      console.log(`[ParticipantDebug] DOM OVERRIDE — WebRTC: ${receiverCount} but DOM: ${domCount} for ${domAloneOverrideCount} consecutive checks (${domAloneOverrideCount * CHECK_INTERVAL_MS / 1000}s). Trusting DOM — everyone left.`);
                      loneTestDetectionActive = false;
                      stopTheRecording('alone');
                      return;
                    }
                    if (domAloneOverrideCount === 1 || domAloneOverrideCount % 3 === 0) {
                      console.log(`[ParticipantDebug] DOM alone override building: ${domAloneOverrideCount}/${DOM_ALONE_OVERRIDE_DEBOUNCE} — WebRTC: ${receiverCount}, DOM: ${domCount}`);
                    }
                  } else {
                    // DOM sees participants or failed — reset override counter
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

                // DOM veto: if DOM explicitly sees participants, don't exit
                // Reset debounce so a fresh 15s streak is needed after veto
                if (typeof domCount === 'number' && domCount >= 2) {
                  console.log(`[ParticipantDebug] DOM VETO — WebRTC: 0 (debounced ${webrtcZeroCount}x) but DOM sees ${domCount} participants. Resetting debounce.`);
                  webrtcZeroCount = 0;
                  runParticipantCheck();
                  return;
                }

                // Scenario A: No one ever joined
                if (!participantsEverSeen) {
                  if (noOneJoinedStartTime === null) {
                    noOneJoinedStartTime = now;
                    console.log('No participants detected yet. Starting no-one-joined countdown (5 min).');
                  }
                  if (now - noOneJoinedStartTime >= NO_ONE_JOINED_TIMEOUT_MS) {
                    console.log('No one joined for 5 minutes after grace period. Ending meeting.');
                    loneTestDetectionActive = false;
                    stopTheRecording('no_one_joined');
                    return;
                  }
                  runParticipantCheck();
                  return;
                }

                // Scenario B: Everyone left — WebRTC is sole authority, DOM didn't veto
                console.log('All participants left — WebRTC 0 (debounced). Ending meeting.');
                loneTestDetectionActive = false;
                stopTheRecording('alone');
              }, CHECK_INTERVAL_MS);
            }

            runParticipantCheck();
          }

          const detectIncrediblySilentMeeting = () => {
            // Only run silence detection if we have audio tracks
            if (!hasAudioTracks) {
              console.warn('Skipping silence detection - no audio tracks available. This may be due to browser permissions or Google Meet audio sharing settings.');
              console.warn('Meeting will rely on presence detection and max duration timeout.');
              return;
            }

            try {
              const audioContext = new AudioContext();
              const mediaSource = audioContext.createMediaStreamSource(stream);
              const analyser = audioContext.createAnalyser();
              analyser.fftSize = 256;
              mediaSource.connect(analyser);

              const dataArray = new Uint8Array(analyser.frequencyBinCount);

              // Sliding window silence detection (Item 3a)
              const silenceWindow: { ts: number; silent: boolean }[] = [];
              let totalChecks = 0;
              let audioActivitySum = 0;
              let lastActivityLogTime = 0;
              let lastWindowEntryTime = 0;
              let silenceWarningLogged = false;

              const silenceThreshold = 10;
              const MIN_WINDOW_ENTRIES = Math.ceil(silenceWindowMs / 1000); // window must be fully populated (e.g. 300 for 5min)

              let monitor = true;

              const monitorSilence = () => {
                try {
                  analyser.getByteFrequencyData(dataArray);

                  const audioActivity = dataArray.reduce((a: number, b: number) => a + b, 0) / dataArray.length;
                  audioActivitySum += audioActivity;
                  totalChecks++;

                  const now = Date.now();

                  // Log once per minute
                  if (now - lastActivityLogTime > 60000) {
                    const avgActivity = (audioActivitySum / totalChecks).toFixed(2);
                    const windowSize = silenceWindow.length;
                    const silentCount = silenceWindow.filter(s => s.silent).length;
                    const ratio = windowSize > 0 ? (silentCount / windowSize * 100).toFixed(0) : '0';
                    console.log(`[SilenceSafety] Avg: ${avgActivity} | Current: ${audioActivity.toFixed(2)} | Window: ${windowSize}s (${ratio}% silent)`);
                    lastActivityLogTime = now;
                  }

                  // Add to sliding window every 1 second (subsample — 100ms polling is too granular)
                  if (now - lastWindowEntryTime >= 1000) {
                    lastWindowEntryTime = now;
                    silenceWindow.push({ ts: now, silent: audioActivity < silenceThreshold });

                    // Prune entries older than window
                    const cutoff = now - silenceWindowMs;
                    while (silenceWindow.length > 0 && silenceWindow[0].ts < cutoff) {
                      silenceWindow.shift();
                    }

                    // Check ratio once we have enough data (Item 3a + 3b combined)
                    if (silenceWindow.length >= MIN_WINDOW_ENTRIES) {
                      const silentCount = silenceWindow.filter(s => s.silent).length;
                      const ratio = silentCount / silenceWindow.length;

                      if (ratio >= silenceRatioThreshold) {
                        // Item 3b: Check participant count before deciding
                        const receiverCount = getWebRTCAudioReceiverCount();

                        if (receiverCount > 0) {
                          // Participants present — do NOT exit (decision table row 1)
                          if (!silenceWarningLogged) {
                            console.warn(`[SilenceSafety] ${(ratio * 100).toFixed(0)}% silence over ${silenceWindow.length}s but ${receiverCount} participant(s) present — NOT exiting`);
                            silenceWarningLogged = true;
                          }
                        } else {
                          // No participants + sustained silence → safety net exit (decision table row 2)
                          console.warn(`[SilenceSafety] ${(ratio * 100).toFixed(0)}% silence over ${silenceWindow.length}s, 0 participants — exiting`, meetingId);
                          console.log('[SilenceSafety] Stats — Avg activity:', (audioActivitySum / totalChecks).toFixed(2), 'Checks:', totalChecks);
                          monitor = false;
                          stopTheRecording('silence_safety');
                        }
                      } else {
                        // Ratio below threshold — reset warning flag
                        silenceWarningLogged = false;
                      }
                    }
                  }

                  if (monitor) {
                    setTimeout(monitorSilence, 100);
                  }
                } catch (error) {
                  console.error('Error in silence monitoring:', error);
                  console.warn('Silence detection failed - will rely on presence detection and max duration timeout.');
                  monitor = false;
                }
              };

              monitorSilence();
            } catch (error) {
              console.error('Failed to initialize silence detection:', error);
              console.warn('Silence detection initialization failed - will rely on presence detection and max duration timeout.');
            }
          };

          /**
           * Perpetual checks for inactivity detection
           */
          // Participant detection starts immediately (2-min grace period handled internally)
          detectLoneParticipantResilient();

          inactivitySilenceDetectionTimeout = setTimeout(() => {
            detectIncrediblySilentMeeting();
          }, activateInactivityDetectionAfterMinutes * 60 * 1000);

          const detectModalsAndDismiss = () => {
            let dismissModalErrorCount = 0;
            const maxDismissModalErrorCount = 10;
            dismissModalsInterval = setInterval(() => {
              try {
                const buttons = document.querySelectorAll('button');
                const dismissButtons = Array.from(buttons).filter((button) => button?.offsetParent !== null && button?.innerText?.includes('Got it'));
                if (dismissButtons.length > 0) {
                  console.log('Found "Got it" button, clicking it...', dismissButtons[0]);
                  dismissButtons[0].click();
                }

                // Dismiss "Microphone not found" and "Camera not found" notifications
                const bodyText = document.body.innerText;
                if (bodyText.includes('Microphone not found') ||
                    bodyText.includes('Make sure your microphone is plugged in') ||
                    bodyText.includes('Camera not found') ||
                    bodyText.includes('Make sure your camera is plugged in')) {
                  console.log('Found device notification (microphone/camera), attempting to dismiss...');
                  // Look for close button (X) near the notification
                  const allButtons = Array.from(document.querySelectorAll('button'));
                  const closeButtons = allButtons.filter((btn) => {
                    const ariaLabel = btn.getAttribute('aria-label');
                    const hasCloseIcon = btn.querySelector('svg') !== null;
                    // Look for close/dismiss buttons
                    return (ariaLabel?.toLowerCase().includes('close') ||
                            ariaLabel?.toLowerCase().includes('dismiss') ||
                            (hasCloseIcon && btn?.offsetParent !== null && btn.innerText === ''));
                  });

                  // Click all visible close buttons to dismiss all notifications
                  closeButtons.forEach((btn) => {
                    if (btn?.offsetParent !== null) {
                      console.log('Clicking close button for device notification...');
                      btn.click();
                    }
                  });
                }
              } catch(error) {
                lastDimissError = error;
                dismissModalErrorCount += 1;
                if (dismissModalErrorCount > maxDismissModalErrorCount) {
                  console.error(`Failed to detect and dismiss "Got it" modals ${maxDismissModalErrorCount} times, will stop trying...`);
                  clearInterval(dismissModalsInterval);
                }
              }
            }, 2000);
          };

          const detectMeetingIsOnAValidPage = () => {
            // Meeting ended patterns — EN + TR (Faz 7)
            const meetingEndedPatterns = [
              'call ended', 'you left the meeting', 'you have left',
              'meeting ended', 'call was ended', 'call is over',
              'meeting is over', 'returning to home screen', 'return to home screen',
              'görüşme sona erdi', 'toplantı sona erdi',
              'görüşmeden ayrıldınız', 'görüşme bitti', 'toplantı bitti',
            ];

            const botRemovedPatterns = [
              'you were removed', 'you\'ve been removed',
              'removed from the meeting', 'removed from the call',
              'you have been removed',
              'toplantıdan çıkarıldınız', 'görüşmeden çıkarıldınız',
            ];

            /** Returns null if page is valid, or an exit reason string if not (Faz 8) */
            const checkPageValidity = (): string | null => {
              try {
                const currentUrl = window.location.href;
                if (!currentUrl.includes('meet.google.com')) {
                  console.warn('No longer on Google Meet page - URL changed to:', currentUrl);
                  return 'page_invalid';
                }

                // Collect text from multiple DOM areas for robust detection
                const bodyText = (document.body.innerText || '').toLowerCase();
                const mainText = (document.querySelector('[role="main"]')?.textContent || '').toLowerCase();
                const dialogText = (document.querySelector('[role="dialog"]')?.textContent || '').toLowerCase();
                const combinedText = `${bodyText} ${mainText} ${dialogText}`;

                // Check bot removed patterns first (more specific)
                for (const pattern of botRemovedPatterns) {
                  if (combinedText.includes(pattern)) {
                    console.warn('Bot removed pattern detected:', pattern, '- ending recording on meeting:', meetingId);
                    return 'bot_removed';
                  }
                }

                // Check meeting ended patterns
                for (const pattern of meetingEndedPatterns) {
                  if (combinedText.includes(pattern)) {
                    console.warn('Meeting end pattern detected:', pattern, '- ending recording on meeting:', meetingId);
                    return 'meeting_ended';
                  }
                }

                // Check for basic Google Meet UI elements
                const hasMeetElements = document.querySelector('button[aria-label="People"]') !== null ||
                                      document.querySelector('button[aria-label="Leave call"]') !== null;

                if (!hasMeetElements) {
                  console.warn('Google Meet UI elements not found - page may have changed state');
                  return 'page_invalid';
                }

                return null; // page is valid
              } catch (error) {
                console.error('Error checking page validity:', error);
                return 'page_invalid';
              }
            };

            // check if we're still on a valid Google Meet page
            isOnValidGoogleMeetPageInterval = setInterval(() => {
              const exitReason = checkPageValidity();
              if (exitReason) {
                console.log('Google Meet page state changed - ending recording on meeting:', meetingId, 'reason:', exitReason);
                clearInterval(isOnValidGoogleMeetPageInterval);
                stopTheRecording(exitReason);
              }
            }, 10000);
          };

          detectModalsAndDismiss();

          detectMeetingIsOnAValidPage();
          
          // Cancel this timeout when stopping the recording
          // Stop recording after `duration` minutes upper limit
          timeoutId = setTimeout(async () => {
            stopTheRecording('max_duration');
          }, duration);
        }

        // Start the recording
        await startRecording();
      },
      {
        meetingId,
        duration,
        inactivityLimit,
        slightlySecretId: this.slightlySecretId,
        activateInactivityDetectionAfterMinutes: config.activateInactivityDetectionAfter,
        activateInactivityDetectionAfter: new Date(new Date().getTime() + config.activateInactivityDetectionAfter * 60 * 1000).toISOString(),
        primaryMimeType: webmMimeType,
        secondaryMimeType: vp9MimeType,
        sttEnabled: config.sttEnabled && !!pcmStreamService,
        pcmChunkMs: config.pcmChunkMs,
        silenceWindowMs: config.silenceWindowMs,
        silenceRatioThreshold: config.silenceRatioThreshold,
      }
    );
  
    this._logger.info('Waiting for recording duration', config.maxRecordingDuration, 'minutes...');
    const processingTime = 0.2 * 60 * 1000;
    const waitingPromise: WaitPromise = getWaitingPromise(processingTime + duration);

    await waitingPromise.promise;

    // End PCM stream — stops new chunks from flowing to handlers
    if (pcmStreamService) {
      pcmStreamService.end();
    }

    // Graceful STT shutdown — drain pending transcripts, disconnect, flush segments
    // Must complete BEFORE joinMeeting() returns so sendCompletedEvent() fires after finalFlush
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

    this._logger.info('Closing the browser...');
    await this.page.context().browser()?.close();

    this._logger.info('All done', { meetingId, exitReason: _exitReason });
    return _exitReason;
  }

  /**
   * Perform Google sign-in when Meet redirects to accounts.google.com.
   * Handles: email step → password step → redirect back to Meet.
   * Reference: ali-gur/mia google_meet.py _do_google_signin_impl
   */
  private async _doGoogleSignin(email: string, password: string): Promise<void> {
    const SIGNIN_TIMEOUT = 20_000;

    this._logger.info('[GoogleSignin] Starting sign-in flow', { email });

    // "Choose an account" screen → click "Use another account"
    try {
      const useAnother = this.page.locator('text=/use another account/i').first();
      if (await useAnother.isVisible({ timeout: 2000 })) {
        this._logger.info('[GoogleSignin] "Choose an account" screen detected, clicking "Use another account"');
        await useAnother.click({ timeout: 5000 });
        await this.page.waitForTimeout(2000);
      }
    } catch {
      // Not on "Choose an account" screen — continue
    }

    // Check if we're on the password step already (returning user)
    const passwordInput = this.page.locator('input[type="password"], input[name="Passwd"]').first();
    try {
      if (await passwordInput.isVisible({ timeout: 1500 })) {
        this._logger.info('[GoogleSignin] Already on password step, filling password');
        await passwordInput.fill(password, { timeout: 10_000 });
        await this.page.getByRole('button', { name: /^next$/i }).first().click({ timeout: 5000 });
        await this.page.waitForURL(
          (url) => url.href.includes('meet.google.com') && !url.href.includes('accounts.google.com'),
          { timeout: SIGNIN_TIMEOUT },
        );
        this._logger.info('[GoogleSignin] Password submitted, back on Meet');
        return;
      }
    } catch {
      this._logger.info('[GoogleSignin] Not on password step, proceeding to email step');
    }

    // Email step: find email/identifier input
    const emailInput = this.page.locator('#identifierId, input[type="email"], input[autocomplete="username"]').first();
    this._logger.info('[GoogleSignin] Waiting for email input field...');
    await emailInput.waitFor({ state: 'visible', timeout: SIGNIN_TIMEOUT });

    this._logger.info('[GoogleSignin] Filling email');
    await emailInput.fill(email, { timeout: 10_000 });
    await this.page.getByRole('button', { name: /^next$/i }).first().click({ timeout: 5000 });

    this._logger.info('[GoogleSignin] Email submitted, waiting for password step...');

    // Wait for password step or SSO redirect
    try {
      await passwordInput.waitFor({ state: 'visible', timeout: 15_000 });
    } catch {
      // Check if we left Google sign-in (SSO redirect) — still need to reach Meet
      const currentUrl = this.page.url();
      if (!currentUrl.includes('accounts.google.com')) {
        this._logger.info('[GoogleSignin] Left sign-in after email (SSO redirect) — waiting for Meet redirect...');
        await this.page.waitForURL(
          (url) => url.href.includes('meet.google.com') && !url.href.includes('accounts.google.com'),
          { timeout: SIGNIN_TIMEOUT },
        );
        this._logger.info('[GoogleSignin] SSO redirect complete, back on Meet');
        return;
      }
      throw new Error('Password field did not appear after email submission');
    }

    this._logger.info('[GoogleSignin] Filling password');
    await passwordInput.fill(password, { timeout: 10_000 });
    await this.page.getByRole('button', { name: /^next$/i }).first().click({ timeout: 5000 });

    // Wait for redirect back to Meet
    await this.page.waitForURL(
      (url) => url.href.includes('meet.google.com') && !url.href.includes('accounts.google.com'),
      { timeout: SIGNIN_TIMEOUT },
    );
    this._logger.info('[GoogleSignin] Sign-in complete, back on Meet');
  }
}
