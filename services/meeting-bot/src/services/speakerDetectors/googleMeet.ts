/**
 * Google Meet — active speaker detection (Faz 6.3 + 6.5 + 6.6).
 *
 * Detection priority chain:
 *   0. WebRTC audio level (Faz 6.5) — getSynchronizationSources / getContributingSources
 *      + protobuf data channel decode for CSRC → participant name mapping.
 *      Requires earlyInjectJs (WebRTC monkey-patches) installed before page.goto().
 *
 *   1. DOM: Participants panel (Faz 6.6 — Meeting Baas aligned, presentation-mode safe)
 *      - backgroundColor blue spectrum + backgroundPositionX animation
 *      - Merged Audio / Adaptive Audio group handling
 *
 *   2. DOM: Mic icon computed style on video tiles (backgroundColor blue spectrum)
 *   3. DOM: Video tile blue border (dominant speaker highlight)
 *
 * Name extraction (DOM fallback):
 *   Panel: aria-label on [role="listitem"] (simple, reliable — Meeting Baas approach)
 *   Tiles: aria-label patterns ("More options for X", "Pin X to your main screen")
 *        + visible text nodes + tile aria-label fallback
 */

import { PlatformDetector } from './types';
import { webrtcInterceptorJs } from './webrtcInterceptor';

const googleMeetDetector: PlatformDetector = {
  platform: 'google-meet',

  // Panel button selectors — Meeting Baas-aligned (old + new Google Meet UI).
  // Panel is critical for DOM fallback detection (presentation mode safe).
  openPanelSelectors: [
    // Old UI (pre-Dec 2025)
    'button[aria-label="Show everyone"]',
    'button[aria-label="People"]',
    'button[aria-label="Participants"]',
    '[data-tooltip="Show everyone"]',
    '[data-tooltip="People"]',
    // Case-insensitive partial matches
    'button[aria-label*="people" i]',
    'button[aria-label*="participants" i]',
    'button[title*="people" i]',
    'button[title*="participants" i]',
    // TR locale
    '[aria-label="Katılımcıları göster"]',
    'button[aria-label*="katılımcı" i]',
    'button[aria-label*="herkesi" i]',
    // New UI (Dec 2025+) — Badge/hover tray style dialog button
    // tryOpenPanel() does text content verification for this selector
    'div[role="button"][aria-haspopup="dialog"]',
  ],

  // WebRTC monkey-patches — injected early via page.addInitScript() (Faz 6.5)
  earlyInjectJs: webrtcInterceptorJs,

  // Injected JS — runs inside the browser context (after join)
  // Faz 6.6: Panel-first detection, Meeting Baas aligned
  detectorJs: `
    // ---------------------------------------------------------------
    // Freeze detection state (Meeting Baas: 8s no mutation → stale)
    // Tracks last DOM change time to detect frozen/hidden pages.
    // ---------------------------------------------------------------
    (function() {
      if (window.__miaDomState) return;
      window.__miaDomState = { lastChangeTime: Date.now() };

      // MutationObserver to track any DOM changes in the page
      try {
        var obs = new MutationObserver(function() {
          window.__miaDomState.lastChangeTime = Date.now();
        });
        obs.observe(document.documentElement, {
          attributes: true, childList: true, subtree: true,
          attributeFilter: ['class', 'aria-label', 'style']
        });
      } catch(e) {}
    })();

    window.__miaDetectSpeaker = function detectActiveSpeakerGMeet() {

      // ---------------------------------------------------------------
      // Freeze / visibility guard (Meeting Baas aligned)
      // Skip DOM detection if page is hidden or no DOM changes for 8s.
      // WebRTC still works in background — only DOM is unreliable.
      // ---------------------------------------------------------------
      if (document.visibilityState === 'hidden') {
        // Only try WebRTC in hidden state — DOM is frozen
        if (typeof window.__miaWebrtcDetectSpeaker === 'function') {
          return window.__miaWebrtcDetectSpeaker();
        }
        return null;
      }
      var FREEZE_THRESHOLD_MS = 8000;
      var domState = window.__miaDomState;
      var domFrozen = domState && (Date.now() - domState.lastChangeTime > FREEZE_THRESHOLD_MS);

      // ---------------------------------------------------------------
      // PRIORITY 0: WebRTC audio level (Faz 6.5)
      // ---------------------------------------------------------------
      if (typeof window.__miaWebrtcDetectSpeaker === 'function') {
        var webrtcResult = window.__miaWebrtcDetectSpeaker();
        if (webrtcResult) return webrtcResult;
      }

      // If DOM is frozen, don't trust DOM signals
      if (domFrozen) return null;

      // ---------------------------------------------------------------
      // Shared: speaking indicator colors (Meeting Baas aligned)
      // ---------------------------------------------------------------
      var SPEAKING_COLORS = [
        'rgb(26, 115, 232)',
        'rgba(26, 115, 232, 0.9)',
        'rgb(11, 87, 208)',
        'rgb(168, 199, 250)'
      ];

      function isBlueColor(color) {
        if (!color) return false;
        for (var i = 0; i < SPEAKING_COLORS.length; i++) {
          if (color === SPEAKING_COLORS[i]) return true;
        }
        return color.indexOf('26, 115, 232') !== -1;
      }

      // ---------------------------------------------------------------
      // Shared: bot self-filtering (Faz 6.6.5)
      // ---------------------------------------------------------------
      function isSelfName(name) {
        if (!name) return false;
        var lower = name.toLowerCase();
        if (lower.indexOf('(you)') !== -1 || lower.indexOf('(siz)') !== -1) return true;
        if (window.__miaBotName && lower === window.__miaBotName.toLowerCase()) return true;
        return false;
      }

      // ---------------------------------------------------------------
      // Shared: check backgroundPositionX animation on element or children
      // Meeting Baas pattern: blue bg alone is NOT enough — need animation.
      // backgroundPositionX !== '0px' means the speaking bars are animating.
      // ---------------------------------------------------------------
      function hasAnimation(el) {
        // Check element itself
        var style = getComputedStyle(el);
        if (style.backgroundImage && style.backgroundImage !== 'none') {
          var bgPos = style.backgroundPositionX;
          if (bgPos && bgPos !== '0px' && bgPos !== '0%') return true;
        }
        // Check children (sprite animation is usually on a child element)
        for (var c = 0; c < el.children.length; c++) {
          var cStyle = getComputedStyle(el.children[c]);
          if (cStyle.backgroundPositionX && cStyle.backgroundPositionX !== '0px' && cStyle.backgroundPositionX !== '0%') {
            return true;
          }
        }
        return false;
      }

      // ---------------------------------------------------------------
      // Shared: check if element tree has speaking indicator
      // Meeting Baas: blue bg + backgroundPositionX animation = speaking.
      // Blue bg without animation = mic is on but NOT speaking.
      // ---------------------------------------------------------------
      function isSpeakingIndicator(el) {
        var toCheck = [el];
        for (var c = 0; c < el.children.length; c++) {
          toCheck.push(el.children[c]);
        }

        for (var j = 0; j < toCheck.length; j++) {
          var style = getComputedStyle(toCheck[j]);
          if (isBlueColor(style.backgroundColor)) {
            // Must have animation — blue alone means unmuted, not speaking
            if (hasAnimation(toCheck[j])) return true;
          }
        }

        // Fallback: CSS class (backward compat, only if animation present)
        if (el.classList.contains('OgVli') && hasAnimation(el)) return true;

        return false;
      }

      // ---------------------------------------------------------------
      // PRIORITY 1: Participants panel (Faz 6.6.3 — Meeting Baas aligned)
      // Panel shows ALL participants regardless of view mode.
      // This is the most reliable DOM signal.
      // ---------------------------------------------------------------
      var panelSelectors = [
        "[aria-label='Participants']",
        "[aria-label='People']",
        "[aria-label='Katılımcılar']",
        "[aria-label='Kullanıcılar']",
        "[aria-label='Teilnehmer']",
        "[aria-label='Participantes']",
      ];
      var panel = null;
      for (var p = 0; p < panelSelectors.length; p++) {
        panel = document.querySelector(panelSelectors[p]);
        if (panel) break;
      }

      if (panel) {
        var listItems = panel.querySelectorAll('[role="listitem"]');
        for (var i = 0; i < listItems.length; i++) {
          var item = listItems[i];
          var ariaName = item.getAttribute('aria-label');
          if (!ariaName) continue;

          // Bot self-filtering (Faz 6.6.5)
          if (isSelfName(ariaName)) continue;

          // Merged Audio handling (Faz 6.6.7 — Meeting Baas aligned)
          // Check each member inside the group for individual speaking indicators,
          // rather than joining all names when the group-level indicator fires.
          if (ariaName === 'Merged audio' || ariaName === 'Adaptive audio group') {
            var subItems = item.querySelectorAll('[role="listitem"]');
            for (var s = 0; s < subItems.length; s++) {
              var subName = subItems[s].getAttribute('aria-label');
              if (!subName || subName === 'Merged audio' || subName === 'Adaptive audio group') continue;
              if (isSelfName(subName)) continue;
              // Check this individual member for speaking indicator
              var subChildren = Array.from(subItems[s].querySelectorAll('*'));
              for (var sc = 0; sc < subChildren.length; sc++) {
                var subBg = getComputedStyle(subChildren[sc]).backgroundColor;
                if (isBlueColor(subBg) && hasAnimation(subChildren[sc])) {
                  return { name: subName.trim(), source: 'gmeet-panel' };
                }
              }
            }
            continue; // No individual member speaking — skip this group
          }

          // Check: speaking indicators on ALL child elements (Meeting Baas pattern)
          // Blue bg + backgroundPositionX animation required
          var speakingFound = false;
          var allChildren = Array.from(item.querySelectorAll('*'));
          for (var j = 0; j < allChildren.length; j++) {
            var bg = getComputedStyle(allChildren[j]).backgroundColor;
            if (isBlueColor(bg) && hasAnimation(allChildren[j])) {
              speakingFound = true;
              break;
            }
          }

          if (speakingFound) {
            return { name: ariaName.trim(), source: 'gmeet-panel' };
          }
        }
      }

      // ---------------------------------------------------------------
      // Helper: extract name from video tile (for Priority 2-3)
      // ---------------------------------------------------------------
      function extractNameFromTile(tile) {
        var ariaEls = tile.querySelectorAll('[aria-label]');
        for (var a = 0; a < ariaEls.length; a++) {
          var label = ariaEls[a].getAttribute('aria-label') || '';
          if (label.indexOf('More options for ') === 0) {
            return label.substring(17).trim();
          }
          var pinMatch = label.match(/^Pin (.+?) to your/);
          if (pinMatch) return pinMatch[1].trim();
        }

        var candidates = tile.querySelectorAll('div, span');
        var bestName = null;
        for (var i = 0; i < candidates.length; i++) {
          var el = candidates[i];
          if (el.offsetHeight === 0 || el.offsetWidth === 0) continue;
          if (el.children.length > 3) continue;

          var text = el.textContent ? el.textContent.trim() : '';
          if (text.length < 2 || text.length > 40) continue;
          if (text.indexOf('\\n') !== -1) continue;

          var lower = text.toLowerCase();
          if (text.indexOf('_') !== -1 && text.indexOf(' ') === -1) continue;
          if (lower === 'you' || lower === 'siz' || lower === 'devices' ||
              lower === 'presentation' || lower === 'sunum' ||
              lower.indexOf('pin') === 0 || lower.indexOf('mic') === 0 ||
              lower.indexOf('mute') === 0 || lower.indexOf('more') === 0 ||
              lower.indexOf('camera') === 0 || lower.indexOf('turn') === 0 ||
              lower.indexOf('background') === 0 || lower.indexOf('reframe') === 0 ||
              lower.indexOf('others might') === 0 || lower.indexOf('you can') === 0) continue;
          if (text.length > 25 && text.split(' ').length > 4) continue;

          var directText = '';
          for (var c = 0; c < el.childNodes.length; c++) {
            if (el.childNodes[c].nodeType === 3) {
              directText += el.childNodes[c].textContent.trim();
            }
          }
          if (directText.length >= 2 && directText.length <= 40 &&
              !(directText.indexOf('_') !== -1 && directText.indexOf(' ') === -1)) {
            bestName = directText;
          } else if (!bestName && text.length >= 2 && text.length <= 40) {
            bestName = text;
          }
        }

        if (!bestName) {
          var tileLabel = tile.getAttribute('aria-label');
          if (tileLabel && tileLabel.length >= 2 && tileLabel.length <= 60) {
            bestName = tileLabel.split(',')[0].trim();
          }
        }

        return bestName;
      }

      // ---------------------------------------------------------------
      // PRIORITY 2: Video tile mic icon (fallback when panel is closed)
      // Same rule: blue bg + animation required
      // ---------------------------------------------------------------
      var allMics = document.querySelectorAll('div[jsname="QgSmzd"]');
      for (var i = 0; i < allMics.length; i++) {
        var mic = allMics[i];
        var tile = mic.closest('[data-participant-id]');
        if (tile && !tile.hasAttribute('data-self-name') && isSpeakingIndicator(mic)) {
          var name = extractNameFromTile(tile);
          if (name && !isSelfName(name)) return { name: name, source: 'gmeet-mic-tile' };
        }
      }

      // ---------------------------------------------------------------
      // PRIORITY 3: Video tile blue border (dominant speaker highlight)
      // ---------------------------------------------------------------
      var tiles = document.querySelectorAll('[data-participant-id]');
      for (var i = 0; i < tiles.length; i++) {
        var tile = tiles[i];
        if (tile.hasAttribute('data-self-name')) continue;

        var checkElements = [tile];
        for (var c = 0; c < Math.min(tile.children.length, 5); c++) {
          checkElements.push(tile.children[c]);
        }

        for (var j = 0; j < checkElements.length; j++) {
          var style = getComputedStyle(checkElements[j]);
          var bc = style.borderColor || '';
          var isActive = isBlueColor(bc) ||
            (style.outline && style.outline.indexOf('26, 115, 232') !== -1);
          if (isActive) {
            var name = extractNameFromTile(tile);
            if (name && !isSelfName(name)) return { name: name, source: 'gmeet-border' };
          }
        }
      }

      return null;
    };
  `,
};

export default googleMeetDetector;
