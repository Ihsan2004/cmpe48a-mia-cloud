/**
 * Zoom — active speaker detection via DOM signals (Faz 6.1).
 *
 * STATUS: PLACEHOLDER — No open-source implementation exists.
 *
 * Researched repos with no Zoom speaker detection:
 *   - Meeting-BaaS/meet-teams-bot: no Zoom speakersObserver
 *   - meetingbot/meetingbot: getSpeakerTimeframes() returns []
 *   - screenappai/meeting-bot: no speaker detection
 *
 * Observations from user (Feb 2026 screenshot):
 *   - Participants panel shows mic icon that fills with gray when speaking
 *   - Participant name: ".participants-item__display-name" (Recall.ai blog)
 *
 * Priority chain (to be verified via DevTools):
 *   1. Participants panel mic icon fill state (mic fills gray when speaking)
 *   2. Speaker view enlarged tile (active speaker is larger)
 *
 * TODO: DevTools verification needed in real Zoom web client meeting:
 *   1. Open Zoom meeting with 2+ people via web client (not desktop app)
 *   2. Open participants panel
 *   3. Right-click on mic icon WHILE someone is speaking
 *   4. Compare DOM/CSS with when NOT speaking:
 *      - SVG fill change? (stroke → filled)
 *      - CSS class addition? (e.g. '--speaking', '--active')
 *      - Opacity/background-color change?
 *   5. Update selectors below with real values
 */

import { PlatformDetector } from './types';

const zoomDetector: PlatformDetector = {
  platform: 'zoom',

  // Open participants panel: click the "Participants" button
  openPanelSelectors: [
    'button[aria-label="Participants"]',
    'button[aria-label="Katılımcılar"]',
    '.footer-button-base__button[aria-label*="participant" i]',
  ],

  // Injected JS — PLACEHOLDER: returns null until selectors are verified
  detectorJs: `
    window.__miaDetectSpeaker = function detectActiveSpeakerZoom() {
      // ---------------------------------------------------------------
      // PLACEHOLDER: No verified selectors available.
      //
      // Known information from Recall.ai blog + user screenshots:
      //   - Participant names are in ".participants-item__display-name"
      //   - Mic icon fills with gray when speaking
      //   - No open-source repo has implemented Zoom speaker detection
      //
      // When selectors are verified via DevTools, implement:
      //   1. Find all participant items in the panel
      //   2. For each item, check if mic icon indicates speaking
      //   3. Get the name from .participants-item__display-name
      //   4. Return { name, source: 'zoom-panel-mic' }
      // ---------------------------------------------------------------

      return null;
    };
  `,
};

export default zoomDetector;
