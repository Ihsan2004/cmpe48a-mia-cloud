/**
 * Microsoft Teams — active speaker detection via WebRTC + DOM signals (Faz 6.1 + Faz 3).
 *
 * Priority chain:
 *   0. WebRTC: DSH (Dominant Speaker History) from main-channel data channel (Faz 3)
 *   1. WebRTC: Caption author from recognitionResults (Faz 3)
 *   2. WebRTC: CSRC audioLevel (if available) (Faz 3)
 *   3. DOM: Video tile speaking indicators (old/new/live Teams) (Faz 6.1)
 *   4. DOM: Caption author fallback (Faz 6.1)
 *
 * Three Teams interfaces (detected automatically):
 *   - Old Teams:  [data-cid="calling-participant-stream"]
 *   - New Teams:  [data-stream-type="Video"]
 *   - Live Teams: [data-tid="menur1j"]
 *
 * Speaking detection (DOM):
 *   1. participant-speaker-ring: opacity === 1 (old Teams)
 *   2. voice-level-stream-outline: ::before pseudo-element border isBlueish
 *      + opacity === 1, or vdi-frame-occlusion class (new/live Teams)
 *   3. Caption author fallback (requires closed captions enabled)
 *
 * isBlueish check: blue > 180 && blue > red+40 && blue > green+40
 *                  && red < 150 && green < 150
 *
 * Sources:
 *   - Meeting-BaaS/meet-teams-bot (GitHub): all selectors and detection logic
 *   - Attendee (github.com/attendee-labs/attendee): Teams WebRTC payload
 *   - Recall.ai blog: caption selectors (data-tid="closed-caption-renderer-wrapper")
 *   - User screenshots (Feb 2026): blue ring around avatar confirmed
 *
 * NOTE: Teams intercepts right-click and uses ::before pseudo-elements
 * for the blue speaking ring — standard querySelector cannot find it.
 * getComputedStyle(el, '::before') is required.
 */

import { PlatformDetector } from './types';
import { teamsWebrtcInterceptorJs } from './teamsWebrtcPayload';

const microsoftTeamsDetector: PlatformDetector = {
  platform: 'microsoft-teams',

  // Early injection: Teams WebRTC interceptor (DSH + data channel + callManager)
  // Must be injected BEFORE page.goto() so patches are in place when Teams
  // establishes RTCPeerConnections. (Faz 3)
  earlyInjectJs: teamsWebrtcInterceptorJs,

  // Open participants panel (roster): click the "People" button
  // Not strictly required for video-tile-based detection, but useful
  // for roster-based detection in future iterations.
  openPanelSelectors: [
    '[data-tid="roster-button"]',
    '#roster-button',
    'button[aria-label="People"]',
    'button[aria-label="Kişiler"]',
  ],

  // Injected JS — runs inside the browser context via Puppeteer page.evaluate()
  detectorJs: `
    window.__miaDetectSpeaker = function detectActiveSpeakerTeams(skipDomCache) {
      // ---------------------------------------------------------------
      // Priority 0: WebRTC-based detection (DSH + caption + CSRC) — Faz 3
      // ---------------------------------------------------------------
      if (typeof window.__miaTeamsWebrtcDetectSpeaker === 'function') {
        var webrtcResult = window.__miaTeamsWebrtcDetectSpeaker();
        if (webrtcResult) return webrtcResult;
      }

      // ---------------------------------------------------------------
      // Priority 0.5: Cached DOM observer result (event-driven, Faz 4)
      // MutationObserver detects DOM changes faster than polling (~50ms vs ~300ms).
      // skipDomCache=true when called BY the observer itself (avoid circular reads).
      // ---------------------------------------------------------------
      if (!skipDomCache && window.__miaDomObserverSpeaker && (Date.now() - window.__miaDomObserverSpeaker.ts < 2000)) {
        return { name: window.__miaDomObserverSpeaker.name, source: window.__miaDomObserverSpeaker.source };
      }

      // ---------------------------------------------------------------
      // Helper: isBlueish — check if a CSS color is blue enough
      // (Meeting-BaaS verified)
      // ---------------------------------------------------------------
      function isBlueish(color) {
        if (!color) return false;
        color = color.toLowerCase().trim();
        var match = color.match(/\\d+/g);
        if (!match || match.length < 3) return false;
        var r = parseInt(match[0]);
        var g = parseInt(match[1]);
        var b = parseInt(match[2]);
        return b > 180 && b > r + 40 && b > g + 40 && r < 150 && g < 150;
      }

      // ---------------------------------------------------------------
      // Helper: checkElementSpeaking — check element + ::before pseudo
      // for speaking indicators (Meeting-BaaS verified)
      // ---------------------------------------------------------------
      function checkElementSpeaking(el) {
        var style = getComputedStyle(el);
        var beforeStyle = getComputedStyle(el, '::before');

        // Old Teams: participant-speaker-ring opacity
        if (el.getAttribute('data-tid') === 'participant-speaker-ring') {
          return parseFloat(style.opacity) === 1;
        }

        // New Teams: voice-level-stream-outline in Video context
        if (el.getAttribute('data-tid') === 'voice-level-stream-outline') {
          // vdi-frame-occlusion class means speaking
          if (el.classList.contains('vdi-frame-occlusion')) return true;
          // Check ::before pseudo-element border color
          var borderColor = beforeStyle.borderColor || beforeStyle.borderTopColor;
          var borderOpacity = parseFloat(beforeStyle.opacity);
          return isBlueish(borderColor) && borderOpacity === 1;
        }

        return false;
      }

      // ---------------------------------------------------------------
      // Helper: checkIfSpeaking — recursive check on element + children
      // (Meeting-BaaS verified — old Teams needs recursive check)
      // ---------------------------------------------------------------
      function checkIfSpeaking(element) {
        if (checkElementSpeaking(element)) return true;
        var children = element.querySelectorAll('*');
        for (var i = 0; i < children.length; i++) {
          if (checkElementSpeaking(children[i])) return true;
        }
        return false;
      }

      // ---------------------------------------------------------------
      // Helper: parseName — extract participant name from aria-label
      // (Meeting-BaaS verified)
      // ---------------------------------------------------------------
      function parseName(ariaLabel) {
        if (!ariaLabel) return '';
        var blacklist = ['Content shared by', 'Leaving...'];
        for (var i = 0; i < blacklist.length; i++) {
          if (ariaLabel.indexOf(blacklist[i]) !== -1) return '';
        }
        var splits = [
          ', video is on,', ', muted,', ', Context menu is available',
          '(Unverified)', 'left the meeting', 'Leaving...'
        ];
        var result = ariaLabel;
        for (var i = 0; i < splits.length; i++) {
          result = result.split(splits[i])[0];
        }
        return result.trim();
      }

      // ---------------------------------------------------------------
      // Check iframe for Teams embedded content
      // (Teams sometimes renders meeting UI inside an iframe)
      // ---------------------------------------------------------------
      var doc = document;
      var iframes = document.querySelectorAll('iframe');
      for (var f = 0; f < iframes.length; f++) {
        try {
          var iframeDoc = iframes[f].contentDocument || iframes[f].contentWindow.document;
          if (iframeDoc) { doc = iframeDoc; break; }
        } catch(e) { /* cross-origin iframe */ }
      }

      // ---------------------------------------------------------------
      // Detect Teams interface variant and find speaker elements
      // Priority: old > new > live (use whichever has elements)
      // ---------------------------------------------------------------
      var oldElements = doc.querySelectorAll('[data-cid="calling-participant-stream"]');
      var newElements = doc.querySelectorAll('[data-stream-type="Video"]');
      var liveElements = doc.querySelectorAll('[data-tid="menur1j"]');

      var elements = oldElements.length > 0 ? oldElements :
                     newElements.length > 0 ? newElements : liveElements;

      for (var i = 0; i < elements.length; i++) {
        var el = elements[i];

        // Skip invisible/phantom elements (0x0)
        if (el.clientWidth === 0 || el.clientHeight === 0) continue;

        var name = '';
        var isSpeaking = false;

        if (el.hasAttribute('data-cid')) {
          // --- OLD TEAMS ---
          name = parseName(el.getAttribute('aria-label'));
          if (!name) continue;
          // Muted check via aria-label
          if ((el.getAttribute('aria-label') || '').indexOf(', muted,') !== -1) continue;
          isSpeaking = checkIfSpeaking(el);

        } else if (el.getAttribute('data-tid') === 'menur1j') {
          // --- LIVE TEAMS ---
          name = (el.getAttribute('aria-label') || '').split(',')[0].trim();
          if (!name) continue;
          // Muted check via roster-participant-muted icon
          var micIcon = el.querySelector('[data-cid="roster-participant-muted"]');
          if (micIcon) continue; // muted, skip
          var voiceIndicator = el.querySelector('[data-tid="voice-level-stream-outline"]');
          if (voiceIndicator) isSpeaking = checkElementSpeaking(voiceIndicator);

        } else {
          // --- NEW TEAMS ---
          // In new Teams, data-tid on the video element may contain participant info
          var dataTid = el.getAttribute('data-tid') || '';
          // Try aria-label first for display name
          var ariaLabel = el.getAttribute('aria-label') || '';
          name = parseName(ariaLabel) || dataTid;
          if (!name) continue;
          // Muted check via mic icon SVG path
          var micPath = el.querySelector('g.ui-icon__outline path');
          var isMuted = micPath && (micPath.getAttribute('d') || '').indexOf('M12 5v4.879') === 0;
          if (isMuted) continue;
          var voiceIndicator = el.querySelector('[data-tid="voice-level-stream-outline"]');
          if (voiceIndicator) isSpeaking = checkElementSpeaking(voiceIndicator);
        }

        if (isSpeaking && name) {
          return { name: name, source: 'teams-video-tile' };
        }
      }

      // ---------------------------------------------------------------
      // FALLBACK: Caption author (requires closed captions enabled)
      // Verified selectors from Recall.ai blog.
      // ---------------------------------------------------------------
      var captionWrapper = doc.querySelector(
        'div[data-tid="closed-caption-renderer-wrapper"]'
      );
      if (captionWrapper) {
        var captions = captionWrapper.querySelectorAll('.fui-ChatMessageCompact');
        if (captions.length > 0) {
          var lastCaption = captions[captions.length - 1];
          var authorEl = lastCaption.querySelector('span[data-tid="author"]');
          if (authorEl) {
            return { name: authorEl.textContent.trim(), source: 'teams-caption' };
          }
        }
      }

      return null;
    };
  `,
};

export default microsoftTeamsDetector;
