/**
 * Speaker Polling Service — Browser injection + 300ms polling (Faz 6.3 + 6.5).
 *
 * Orchestrates the full speaker detection lifecycle:
 *   0. (Faz 6.5) Inject WebRTC monkey-patches early via injectEarlyDetectorJs()
 *   1. Open participants panel (Google Meet only — Teams uses video tiles)
 *   2. Inject platform-specific detector JS into browser
 *   3. Start 300ms polling: call __miaDetectSpeaker() → send result to Node.js
 *   4. Periodically re-open panel if it gets closed (30s interval)
 *
 * Usage (inside bot's joinMeeting):
 *   // BEFORE page.goto():
 *   await injectEarlyDetectorJs(page, 'google-meet', logger);
 *   await page.goto(url);
 *   // AFTER join confirmed:
 *   const speakerService = await startSpeakerPolling(page, 'google-meet', meetingId, logger);
 */

import { Page } from 'playwright';
import { Logger } from 'winston';
import config from '../config';
import { ActiveSpeakerService } from './activeSpeakerService';
import { getDetector } from './speakerDetectors';
import { SpeakerDetection } from './speakerDetectors/types';

const PANEL_REOPEN_INTERVAL_MS = 10_000;

/**
 * Inject early WebRTC monkey-patches before page navigation (Faz 6.5).
 *
 * Must be called BEFORE page.goto() so the patches are in place when
 * Google Meet establishes RTCPeerConnections.
 *
 * Safe to call even if the detector has no earlyInjectJs — it's a no-op.
 */
export async function injectEarlyDetectorJs(
  page: Page,
  platform: string,
  logger: Logger,
): Promise<void> {
  // Always inject WebRTC interceptor: needed for participant count detection
  // (Item 1) and active speaker detection (speaker timeline for STT attribution).
  const detector = getDetector(platform);
  if (!detector?.earlyInjectJs) return;

  try {
    await page.addInitScript(detector.earlyInjectJs);
    logger.info(`[SpeakerPolling] Early WebRTC interceptor installed for ${platform}`);
  } catch (err) {
    logger.warn('[SpeakerPolling] Failed to inject early WebRTC interceptor (non-fatal)', {
      error: err instanceof Error ? err.message : String(err),
    });
  }
}

/**
 * Start speaker detection polling on the given page.
 *
 * @returns ActiveSpeakerService if successful, null if detector not available or setup fails.
 */
export async function startSpeakerPolling(
  page: Page,
  platform: 'google-meet' | 'microsoft-teams' | 'zoom',
  meetingId: string,
  logger: Logger,
  botDisplayName?: string,
  /** Override ACTIVE_SPEAKER_ENABLED check — used by fallback mode which always needs polling */
  forceFallback?: boolean,
): Promise<ActiveSpeakerService | null> {
  if (!forceFallback && !config.activeSpeakerEnabled) {
    logger.info('[SpeakerPolling] Disabled via ACTIVE_SPEAKER_ENABLED=false');
    return null;
  }

  const detector = getDetector(platform);
  if (!detector) {
    logger.warn(`[SpeakerPolling] No detector for platform: ${platform}`);
    return null;
  }

  const pollingIntervalMs = config.speakerPollingIntervalMs;
  const service = new ActiveSpeakerService(meetingId, config.speakerSwitchDelayMs);

  // Log speaker changes
  service.onSpeakerChange((event) => {
    logger.info(
      `[SpeakerPolling] Speaker changed: ${event.previousSpeaker || '(none)'} → ${event.newSpeaker} (${event.source})`,
      { meetingId },
    );
  });

  try {
    // 1. Try to open participants panel (best effort, non-fatal)
    await tryOpenPanel(page, detector.openPanelSelectors, logger);

    // 2. Expose callback: browser → Node.js
    await page.exposeFunction(
      'miaActiveSpeaker',
      (detection: SpeakerDetection | null) => {
        service.processDetection(detection);
      },
    );

    // 3. Inject detector JS (defines window.__miaDetectSpeaker)
    await page.evaluate(`(function() { ${detector.detectorJs} })()`);

    // 3a. Inject bot display name for self-filtering (Faz 6.6.5)
    if (botDisplayName) {
      await page.evaluate((name: string) => {
        (window as any).__miaBotName = name;
      }, botDisplayName);
      logger.info(`[SpeakerPolling] Bot name injected for self-filtering: ${botDisplayName}`);
    }

    // 3b. Expose diagnostic callback: browser → Node.js (one-time DOM snapshot)
    await page.exposeFunction(
      'miaDiagnosticLog',
      (data: Record<string, unknown>) => {
        logger.warn('[SpeakerPolling] DOM diagnostic snapshot', { meetingId, ...data });
      },
    );

    // 4. Start polling interval inside browser (with one-time diagnostic)
    // Store interval ID on window for cleanup (Meeting Baas pattern)
    await page.evaluate((intervalMs: number) => {
      let diagnosticSent = false;
      (window as any).__miaPollInterval = setInterval(() => {
        try {
          const result = (window as any).__miaDetectSpeaker();
          (window as any).miaActiveSpeaker(result);

          // One-time diagnostic: dump DOM structure on first null result
          if (!diagnosticSent && !result) {
            diagnosticSent = true;
            try {
              // Collect all relevant DOM info for debugging selectors
              const diag: Record<string, unknown> = {};

              // 1. Panels / containers with aria-label
              const ariaContainers = Array.from(document.querySelectorAll('[aria-label]'))
                .filter((el: Element) => {
                  const label = el.getAttribute('aria-label')?.toLowerCase() || '';
                  return label.includes('people') || label.includes('participant')
                    || label.includes('katılımcı') || label.includes('everyone')
                    || label.includes('roster') || label.includes('panel');
                })
                .map((el: Element) => ({
                  tag: el.tagName,
                  ariaLabel: el.getAttribute('aria-label'),
                  role: el.getAttribute('role'),
                  childCount: el.children.length,
                }));
              diag.ariaContainers = ariaContainers;

              // 2. Role=listitem elements (participants panel items)
              const listItems = document.querySelectorAll('[role="listitem"]');
              diag.listItemCount = listItems.length;
              if (listItems.length > 0) {
                const sample = listItems[0];
                diag.listItemSample = {
                  ariaLabel: sample.getAttribute('aria-label'),
                  innerHTML: sample.innerHTML.substring(0, 200),
                  classes: sample.className,
                };
              }

              // 3. Video tiles — [data-participant-id] or other data attrs
              const participantTiles = document.querySelectorAll('[data-participant-id]');
              diag.participantTileCount = participantTiles.length;

              // Alternative tile selectors
              const altTiles = document.querySelectorAll('[data-requested-participant-id]');
              diag.altTileCount = altTiles.length;

              // 4. [data-self-name] elements
              const selfNames = document.querySelectorAll('[data-self-name]');
              diag.selfNameCount = selfNames.length;

              // 5. jsname="QgSmzd" elements (mic icons in panel)
              const micIcons = document.querySelectorAll('div[jsname="QgSmzd"]');
              diag.micIconCount = micIcons.length;
              if (micIcons.length > 0) {
                diag.micIconSampleClasses = micIcons[0].className;
              }

              // 6. All elements with blue-ish computed styles (sampling first 5 listitem children)
              const blueElements: string[] = [];
              listItems.forEach((item, idx) => {
                if (idx > 2) return;
                const allChildren = item.querySelectorAll('*');
                allChildren.forEach(child => {
                  const bg = getComputedStyle(child).backgroundColor;
                  if (bg && bg !== 'rgba(0, 0, 0, 0)' && bg !== 'transparent') {
                    blueElements.push(bg + ' on ' + child.tagName + '.' + child.className.substring(0, 30));
                  }
                });
              });
              diag.nonTransparentBGs = blueElements.slice(0, 10);

              // 7. Video tile internal structure — name extraction debug
              const videoTiles = document.querySelectorAll('[data-participant-id]');
              diag.videoContainerCount = videoTiles.length;
              const tileDetails: unknown[] = [];
              videoTiles.forEach((tile, idx) => {
                if (idx > 1) return; // sample first 2 tiles
                // Find visible text nodes inside the tile
                const textNodes: string[] = [];
                const walker = document.createTreeWalker(tile, NodeFilter.SHOW_TEXT);
                let node;
                while (node = walker.nextNode()) {
                  const t = node.textContent?.trim();
                  if (t && t.length >= 2 && t.length <= 50) {
                    textNodes.push(t);
                  }
                }
                // Find mic icons inside this tile
                const tileMics = tile.querySelectorAll('div[jsname="QgSmzd"]');
                // Find elements with aria-label inside tile
                const ariaEls = Array.from(tile.querySelectorAll('[aria-label]'))
                  .slice(0, 5)
                  .map((el: Element) => el.getAttribute('aria-label')?.substring(0, 40));
                tileDetails.push({
                  participantId: tile.getAttribute('data-participant-id')?.substring(0, 40),
                  tileAriaLabel: tile.getAttribute('aria-label')?.substring(0, 60),
                  textNodes: textNodes.slice(0, 8),
                  micIconsInTile: tileMics.length,
                  micClasses: tileMics.length > 0 ? tileMics[0].className : null,
                  ariaLabels: ariaEls,
                  borderColor: getComputedStyle(tile).borderColor,
                  childCount: tile.children.length,
                });
              });
              diag.tileDetails = tileDetails;

              // WebRTC interceptor state (Faz 6.5)
              if (typeof (window as any).__miaWebrtcDiag === 'function') {
                diag.webrtc = (window as any).__miaWebrtcDiag();
              } else {
                diag.webrtc = 'interceptor not installed';
              }

              (window as any).miaDiagnosticLog(diag);
            } catch (diagErr) {
              (window as any).miaDiagnosticLog({ error: String(diagErr) });
            }
          }
        } catch (e) {
          (window as any).miaActiveSpeaker(null);
        }
      }, intervalMs);
    }, pollingIntervalMs);

    // 5. Periodic panel re-open (Google Meet panel may close on UI interactions)
    if (detector.openPanelSelectors.length > 0) {
      const panelInterval = setInterval(async () => {
        try {
          // Check if panel is still visible before clicking (avoid toggling)
          const panelVisible = await page.evaluate(() => {
            // Explicit participants panel check (Meeting Baas aligned).
            // Must match KNOWN panel containers — generic [role='list'] [role='listitem']
            // is too broad (chat, settings menus also have listitems).
            const panelSelectors = [
              "[aria-label='Participants']",
              "[aria-label='People']",
              "[aria-label='Katılımcılar']",
              "[aria-label='Kullanıcılar']",
              "[aria-label='Teilnehmer']",
              "[aria-label='Participantes']",
            ];
            for (const sel of panelSelectors) {
              const panel = document.querySelector(sel);
              // Panel must contain at least one listitem to be truly open
              if (panel && panel.querySelector('[role="listitem"]')) return true;
            }
            // Teams roster header
            if (document.querySelector('[data-tid="roster-header"]')) return true;
            return false;
          });

          if (!panelVisible) {
            await tryOpenPanel(page, detector.openPanelSelectors, logger);
          }
        } catch {
          // Page might be navigating or closed — ignore
        }
      }, PANEL_REOPEN_INTERVAL_MS);

      page.on('close', () => clearInterval(panelInterval));
    }

    // Cleanup browser-side polling interval on page close (Meeting Baas pattern)
    page.on('close', () => {
      page.evaluate(() => {
        if ((window as any).__miaPollInterval) {
          clearInterval((window as any).__miaPollInterval);
          (window as any).__miaPollInterval = null;
        }
      }).catch(() => {}); // page already closed — ignore
    });

    logger.info(
      `[SpeakerPolling] Started for ${platform} (interval: ${pollingIntervalMs}ms)`,
      { meetingId },
    );

    return service;
  } catch (error) {
    logger.error(
      `[SpeakerPolling] Failed to start for ${platform}`,
      { meetingId, error: error instanceof Error ? error.message : String(error) },
    );
    return null;
  }
}

/**
 * Try to open the participants panel by clicking the first matching selector.
 * Non-fatal — if no selector matches, speaker detection falls back to
 * video-tile-based signals (Priority 2+).
 */
async function tryOpenPanel(
  page: Page,
  selectors: string[],
  logger: Logger,
): Promise<void> {
  for (const selector of selectors) {
    try {
      const element = await page.$(selector);
      if (element) {
        // New UI dialog button: use positive "people" keyword match (Meeting Baas aligned).
        // Collect all matching dialog buttons, try positive matches first,
        // then icon-only unknowns as last resort.
        if (selector.includes('aria-haspopup')) {
          const allDialogButtons = await page.$$(selector);
          let fallbackButton: typeof element | null = null;

          for (const btn of allDialogButtons) {
            const text = await btn.textContent();
            const ariaLabel = await btn.getAttribute('aria-label');
            const tooltip = await btn.getAttribute('data-tooltip');
            const combined = [text, ariaLabel, tooltip].filter(Boolean).join(' ').toLowerCase();

            // Positive match: contains people/participants keyword
            if (combined.includes('people') || combined.includes('participant') ||
                combined.includes('everyone') || combined.includes('katılımcı') ||
                combined.includes('herkesi') || combined.includes('teilnehmer') ||
                combined.includes('participantes')) {
              await btn.click();
              await page.waitForTimeout(1000);
              logger.info(`[SpeakerPolling] Panel opened via dialog button (positive match): ${combined.trim()}`);
              return;
            }

            // Skip known non-people buttons
            if (combined.includes('chat') || combined.includes('activities') ||
                combined.includes('info') || combined.includes('sohbet') ||
                combined.includes('etkinlik') || combined.includes('bilgi')) {
              continue;
            }

            // Unknown/icon-only button — save as fallback
            if (!fallbackButton) fallbackButton = btn;
          }

          // No positive match — try fallback icon-only button if any
          if (fallbackButton) {
            await fallbackButton.click();
            await page.waitForTimeout(1000);
            logger.info(`[SpeakerPolling] Panel opened via dialog button (fallback — icon-only)`);
            return;
          }
          continue; // No dialog buttons worked — try next selector
        }
        await element.click();
        // Small delay for panel to open
        await page.waitForTimeout(1000);
        logger.info(`[SpeakerPolling] Panel opened via: ${selector}`);
        return;
      }
    } catch {
      // Selector not found or click failed — try next
    }
  }

  // DIAGNOSTIC: None of the selectors matched — dump all button aria-labels
  try {
    const buttonDiag = await page.evaluate(() => {
      const buttons = Array.from(document.querySelectorAll('button'));
      return buttons
        .map(b => ({
          ariaLabel: b.getAttribute('aria-label'),
          text: b.textContent?.trim().substring(0, 40),
          dataAttrs: Array.from(b.attributes)
            .filter(a => a.name.startsWith('data-'))
            .map(a => `${a.name}=${a.value}`)
            .join(', '),
        }))
        .filter(b => b.ariaLabel || b.text);
    });
    logger.warn('[SpeakerPolling] Panel button NOT found. All buttons on page:', {
      selectors,
      buttons: buttonDiag,
    });
  } catch {
    // ignore diagnostic errors
  }
}
