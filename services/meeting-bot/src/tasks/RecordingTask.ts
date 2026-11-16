import { Page } from 'playwright';
import { Task } from '../lib/Task';
import config from '../config';
import { Logger } from 'winston';
import { vp9MimeType, webmMimeType } from '../lib/recording';

export class RecordingTask extends Task<null, void> {
  private meetingId: string;
  private page: Page;
  private duration: number;
  private inactivityLimit: number;
  private slightlySecretId: string;
  private sttEnabled: boolean;
  private pcmChunkMs: number;

  constructor(
    meetingId: string,
    page: Page,
    duration: number,
    slightlySecretId: string,
    logger: Logger,
    sttEnabled: boolean = false,
    pcmChunkMs: number = 200
  ) {
    super(logger);
    this.meetingId = meetingId;
    this.duration = duration;
    this.inactivityLimit = config.inactivityLimit * 60 * 1000;
    this.page = page;
    this.slightlySecretId = slightlySecretId;
    this.sttEnabled = sttEnabled;
    this.pcmChunkMs = pcmChunkMs;
  }

  protected async execute(): Promise<void> {
    await this.page.evaluate(
      async ({ meetingId, duration, inactivityLimit, slightlySecretId, activateInactivityDetectionAfter, activateInactivityDetectionAfterMinutes, primaryMimeType, secondaryMimeType, sttEnabled, pcmChunkMs }:
        { meetingId: string, duration: number, inactivityLimit: number, slightlySecretId: string, activateInactivityDetectionAfter: string, activateInactivityDetectionAfterMinutes: number, primaryMimeType: string, secondaryMimeType: string, sttEnabled: boolean, pcmChunkMs: number }) => {
        let timeoutId: NodeJS.Timeout;
        let inactivityDetectionTimeout: NodeJS.Timeout;

        /**
         * @summary A simple method to reliably send chunks over exposeFunction
         * @param chunk Array buffer to send
         * @returns void
         */
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
              await sendChunkToServer(arrayBuffer);
            } catch (error) {
              console.error('Error uploading chunk:', error.message, error);
            }
          };

          // Start recording with 2-second intervals
          const chunkDuration = 2000;
          mediaRecorder.start(chunkDuration);

          // --- AudioWorklet PCM capture for STT pipeline ---
          let pcmAudioContext: AudioContext | null = null;
          const hasAudioTracks = stream.getAudioTracks().length > 0;

          if (sttEnabled && hasAudioTracks) {
            try {
              const pcmSampleRate = 16000;
              const pcmBufferSize = Math.round(pcmSampleRate * (pcmChunkMs / 1000)); // 3200 samples for 200ms

              const processorCode = `
                class PcmProcessor extends AudioWorkletProcessor {
                  constructor() {
                    super();
                    this.buffer = new Float32Array(${pcmBufferSize});
                    this.offset = 0;
                  }
                  process(inputs) {
                    const input = inputs[0]?.[0];
                    if (!input) return true;
                    for (let i = 0; i < input.length; i++) {
                      this.buffer[this.offset++] = input[i];
                      if (this.offset >= ${pcmBufferSize}) {
                        const int16 = new Int16Array(${pcmBufferSize});
                        for (let j = 0; j < ${pcmBufferSize}; j++) {
                          int16[j] = Math.max(-32768, Math.min(32767, Math.round(this.buffer[j] * 32767)));
                        }
                        this.port.postMessage(int16, [int16.buffer]);
                        this.buffer = new Float32Array(${pcmBufferSize});
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

              const audioOnlyStream = new MediaStream(stream.getAudioTracks());
              const source = pcmAudioContext.createMediaStreamSource(audioOnlyStream);

              await pcmAudioContext.audioWorklet.addModule(blobUrl);
              URL.revokeObjectURL(blobUrl);

              const workletNode = new AudioWorkletNode(pcmAudioContext, 'pcm-processor');
              source.connect(workletNode);

              workletNode.port.onmessage = (e: MessageEvent) => {
                const int16Array: Int16Array = e.data;
                const bytes = new Uint8Array(int16Array.buffer);
                let binary = '';
                for (let i = 0; i < bytes.byteLength; i++) {
                  binary += String.fromCharCode(bytes[i]);
                }
                const base64 = btoa(binary);
                (window as any).miaPcmData(slightlySecretId, base64);
              };

              console.log('AudioWorklet PCM capture started for Zoom STT', { pcmSampleRate, pcmBufferSize, pcmChunkMs });
            } catch (err) {
              console.error('Failed to start AudioWorklet PCM capture:', err);
            }
          }

          const stopTheRecording = async (exitReason?: string) => {
            console.log('-------- TRIGGER stop the recording', { exitReason });
            mediaRecorder.stop();
            stream.getTracks().forEach((track) => track.stop());

            // Stop PCM capture
            if (pcmAudioContext) {
              try {
                await pcmAudioContext.close();
                console.log('PCM AudioContext closed');
              } catch (err) {
                console.error('Error closing PCM AudioContext:', err);
              }
            }

            // Cleanup recording timer
            clearTimeout(timeoutId);

            // Cancel the perpetural checks
            if (inactivityDetectionTimeout) {
              clearTimeout(inactivityDetectionTimeout);
            }

            // Begin browser cleanup — pass exit reason to Node.js (Faz 8)
            (window as any).miaMeetEnd(slightlySecretId, exitReason || 'meeting_ended');
          };

          let loneTest: NodeJS.Timeout;
          let monitor = true;

          // TODO Create standard detection lib
          const detectLoneParticipant = () => {
            let dom: Document = document;
            const iframe: HTMLIFrameElement | null = document.querySelector('iframe#webclient');
            if (iframe && iframe.contentDocument) {
              console.log('Using iframe for participants detection...');
              dom = iframe.contentDocument;
            }

            loneTest = setInterval(() => {
              try {
                // Detect and click blocking "OK" buttons
                const okButton = Array.from(dom.querySelectorAll('button'))
                    .filter((el) => el?.innerText?.trim()?.match(/^OK/i));
                if (okButton && okButton[0]) {
                  console.log('It appears that meeting has been ended. Click "OK" and verify if meeting is still in progress...', { meetingId });
                  let shouldEndMeeting = false;
                  const meetingEndLabel = dom.querySelector('[aria-label="Meeting is end now"]');
                  if (meetingEndLabel) {
                    shouldEndMeeting = true;
                  }
                  else {
                    const endText = 'This meeting has been ended by host';
                    const divs = dom.querySelectorAll('div');
                    for (const modal of divs) {
                      if (modal.innerText.includes(endText)) {
                        shouldEndMeeting = true;
                        break;
                      }
                    }
                  }
                  okButton[0].click();
                  if (shouldEndMeeting) {
                    console.log('Detected Zoom meeting has been ended by host. End Recording...', { meetingId });
                    clearInterval(loneTest);
                    monitor = false;
                    stopTheRecording('meeting_ended');
                  }
                }

                // Detect number of participants
                const participantsMatch = Array.from(dom.querySelectorAll('button'))
                    .filter((el) => el?.innerText?.trim()?.match(/^\d+/));
                const text = participantsMatch && participantsMatch.length > 0 ? participantsMatch[0].innerText.trim() : null;
                if (!text) {
                  console.error('[ParticipantDebug] Zoom presence detection NOT working — no button with digit found. Meeting:', meetingId);
                  return;
                }

                const regex = new RegExp(/\d+/);
                const participants = text.match(regex);
                if (!participants || participants.length === 0) {
                  console.error('[ParticipantDebug] Zoom participants regex failed on text:', text, '| Meeting:', meetingId);
                  return;
                }
                const pCount = Number(participants[0]);
                if (pCount > 1) {
                  return;
                }
                console.log(`[ParticipantDebug] Zoom participant count: ${pCount} — bot is alone. Meeting:`, meetingId);

                console.log('Detected meeting bot is alone in meeting, ending recording on team:', { meetingId });
                clearInterval(loneTest);
                monitor = false;
                stopTheRecording('alone');
              } catch (error) {
                console.error('Zoom Meeting presence detection failed on team:', { meetingId, message: error.message, error });
              }
            }, 2000); // Detect every 2 seconds
          };

          const detectIncrediblySilentMeeting = () => {
            const audioContext = new AudioContext();
            const mediaSource = audioContext.createMediaStreamSource(stream);
            const analyser = audioContext.createAnalyser();

            /* Use a value suitable for the given use case of silence detection
               |
               |____ Relatively smaller FFT size for faster processing and less sampling
            */
            analyser.fftSize = 256;

            mediaSource.connect(analyser);

            const dataArray = new Uint8Array(analyser.frequencyBinCount);

            // Sliding silence period
            let silenceDuration = 0;

            // Audio gain/volume
            const silenceThreshold = 10;

            const monitorSilence = () => {
              analyser.getByteFrequencyData(dataArray);

              const audioActivity = dataArray.reduce((a, b) => a + b) / dataArray.length;

              if (audioActivity < silenceThreshold) {
                silenceDuration += 100; // Check every 100ms
                if (silenceDuration >= inactivityLimit) {
                  console.warn('Detected silence in Zoom Meeting, ending recording for meeting:', meetingId);
                  monitor = false;
                  clearInterval(loneTest);
                  stopTheRecording('silence');
                }
              } else {
                silenceDuration = 0;
              }

              if (monitor) {
                // Recursively queue the next check
                setTimeout(monitorSilence, 100);
              }
            };

            // Go silence monitor
            monitorSilence();
          };

          // Zoom meeting ended patterns — EN + TR (Faz 9)
          const zoomEndedPatterns = [
            'this meeting has been ended by host', 'the host has ended the meeting',
            'meeting has ended', 'you have been removed',
            'the host removed you from the meeting',
            'toplantı sona erdi', 'toplantıdan çıkarıldınız',
          ];
          const zoomRemovedPatterns = [
            'you have been removed', 'the host removed you from the meeting',
            'toplantıdan çıkarıldınız',
          ];

          // Page validity check every 10 seconds (Faz 7/9)
          let pageCheckInterval: NodeJS.Timeout;
          pageCheckInterval = setInterval(() => {
            try {
              let dom: Document = document;
              const iframe: HTMLIFrameElement | null = document.querySelector('iframe#webclient');
              if (iframe && iframe.contentDocument) {
                dom = iframe.contentDocument;
              }
              const bodyText = (dom.body?.innerText || '').toLowerCase();
              const dialogText = (dom.querySelector('[role="dialog"]')?.textContent || '').toLowerCase();
              const combinedText = `${bodyText} ${dialogText}`;

              for (const pattern of zoomRemovedPatterns) {
                if (combinedText.includes(pattern)) {
                  console.warn('Zoom bot removed pattern detected:', pattern);
                  clearInterval(pageCheckInterval);
                  stopTheRecording('bot_removed');
                  return;
                }
              }
              for (const pattern of zoomEndedPatterns) {
                if (combinedText.includes(pattern)) {
                  console.warn('Zoom meeting end pattern detected:', pattern);
                  clearInterval(pageCheckInterval);
                  stopTheRecording('meeting_ended');
                  return;
                }
              }
            } catch (error) {
              console.error('Error checking Zoom page validity:', error);
            }
          }, 10000);

          /**
           * Perpetual checks for inactivity detection
           */
          inactivityDetectionTimeout = setTimeout(() => {
            detectLoneParticipant();
            detectIncrediblySilentMeeting();
          }, activateInactivityDetectionAfterMinutes * 60 * 1000);

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
        meetingId: this.meetingId,
        duration: this.duration,
        inactivityLimit: this.inactivityLimit,
        slightlySecretId: this.slightlySecretId,
        activateInactivityDetectionAfterMinutes: config.activateInactivityDetectionAfter,
        activateInactivityDetectionAfter: new Date(new Date().getTime() + config.activateInactivityDetectionAfter * 60 * 1000).toISOString(),
        primaryMimeType: webmMimeType,
        secondaryMimeType: vp9MimeType,
        sttEnabled: this.sttEnabled,
        pcmChunkMs: this.pcmChunkMs
      }
    );
  }
}
