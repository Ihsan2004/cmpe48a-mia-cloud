/**
 * Shared types for platform-specific speaker detection (Faz 6).
 *
 * Each platform detector is a plain JavaScript string that gets injected into
 * the browser via page.evaluate(). The injected function returns a
 * SpeakerDetection result (or null) which is sent back to Node.js via
 * page.exposeFunction().
 */

/** Result returned by the injected browser-side detection function. */
export interface SpeakerDetection {
  /** Display name of the detected speaker (as shown in the meeting UI). */
  name: string;
  /**
   * Which signal produced this detection.
   * Used for logging/debugging — helps identify which priority level fired.
   */
  source: string;
}

/**
 * A platform detector module must export this shape.
 * `detectorJs` is the raw JS source that will be injected into the browser.
 * It must define `window.__miaDetectSpeaker` as a function returning
 * { name: string, source: string } | null.
 */
export interface PlatformDetector {
  /** Human-readable platform name for logs. */
  platform: string;
  /**
   * Raw JavaScript source to inject via page.evaluate().
   * Must assign window.__miaDetectSpeaker = function() { ... }
   * The function must return { name, source } | null.
   */
  detectorJs: string;
  /**
   * Optional JavaScript to inject EARLY via page.addInitScript(),
   * before page navigation. Used for WebRTC monkey-patches that must
   * be in place before the meeting app establishes connections (Faz 6.5).
   */
  earlyInjectJs?: string;
  /**
   * Action to open the participants panel before detection starts.
   * Each platform has a different button/shortcut to open the panel.
   * Returns the CSS selector(s) to click, or null if no action needed.
   */
  openPanelSelectors: string[];
}
