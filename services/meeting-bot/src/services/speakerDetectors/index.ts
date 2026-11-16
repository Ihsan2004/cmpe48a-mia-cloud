/**
 * Speaker detector registry (Faz 6.1).
 *
 * Maps platform names to their detection modules.
 * Each module provides:
 *   - detectorJs: JS to inject into the browser
 *   - openPanelSelectors: selectors to click for opening participants panel
 */

export type { SpeakerDetection, PlatformDetector } from './types';

import googleMeetDetector from './googleMeet';
import microsoftTeamsDetector from './microsoftTeams';
import zoomDetector from './zoom';
import { PlatformDetector } from './types';

const detectors: Record<string, PlatformDetector> = {
  'google-meet': googleMeetDetector,
  'microsoft-teams': microsoftTeamsDetector,
  'zoom': zoomDetector,
};

export function getDetector(platform: string): PlatformDetector | undefined {
  return detectors[platform];
}

export { googleMeetDetector, microsoftTeamsDetector, zoomDetector };
