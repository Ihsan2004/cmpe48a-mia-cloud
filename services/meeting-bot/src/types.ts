export type AVMediaType = 'audio' | 'video';

export type ContentType =
  | 'video/webm'
  | 'video/mp4'
  | 'video/x-matroska';

export const extensionToContentType: Record<string, ContentType> = {
  '.webm': 'video/webm',
  '.mp4': 'video/mp4',
  '.mkv': 'video/x-matroska',
};

export interface WaitPromise {
  promise: Promise<void>;
  resolveEarly: (value: void | PromiseLike<void>) => void;
}

export type BotStatus = 'processing' | 'joined' | 'finished' | 'failed';

export type WaitingAtLobbyCategory = {
  category: 'WaitingAtLobby',
  subCategory: 'Timeout' | 'StuckInLobby' | 'UserDeniedRequest',
}
export type UnsupportedMeetingCategory = {
  category: 'UnsupportedMeeting',
  subCategory: 'RequiresSignIn' | 'RestrictedMeeting' | 'PrivateMeeting',
}

export const categories = [
  'WaitingAtLobby',
  'Recording',
  'Integration',
  'UnsupportedMeeting',
  'Platform',
] as const;
export const subCategories = [
  'Timeout',
  'StuckInLobby',
  'Start',
  'End',
  'UserDeniedRequest',
  'InactiveIntegration',
  'ReconnectRequired',
  'RequiresSignIn',
  'RestrictedMeeting',
  'PrivateMeeting',
  'BotCrashed',
  'BotNotResponding',
] as const;
export const logCategories: {
  category: typeof categories[number],
  subCategory: typeof subCategories[number][],
}[] = [
  {
    category: 'WaitingAtLobby',
    subCategory: [
      'Timeout',
      'StuckInLobby',
      'UserDeniedRequest'
    ] as const,
  },
  {
    category: 'Recording',
    subCategory: [
      'Start',
      'End',
    ] as const,
  },
  {
    category: 'Integration',
    subCategory: [
      'InactiveIntegration',
      'ReconnectRequired',
    ] as const,
  },
  {
    category: 'UnsupportedMeeting',
    subCategory: [
      'RequiresSignIn',
      'RestrictedMeeting',
      'PrivateMeeting',
    ] as const,
  },
  {
    category: 'Platform',
    subCategory: [
      'BotCrashed',
      'BotNotResponding',
    ] as const,
  },
] as const;
export type LogCategory = typeof logCategories[number]['category'];
export type LogSubCategory<C extends LogCategory> = (typeof logCategories[number] & { category: C })['subCategory'][number];

export type UploaderType = 's3';

// Exit reason — why the bot left the meeting (Faz 8)
export type ExitReason =
  | 'meeting_ended'   // host ended or call ended for everyone
  | 'bot_removed'     // bot was removed/kicked from the meeting
  | 'alone'           // bot is the only participant left (everyone left)
  | 'no_one_joined'   // no participant ever joined within timeout
  | 'silence'         // prolonged silence detected (legacy — binary counter)
  | 'silence_safety'  // sliding-window silence safety net (5min 90% threshold + no participants)
  | 'max_duration'    // MAX_RECORDING_DURATION_MINUTES reached
  | 'page_invalid';   // page navigated away or Meet UI elements disappeared

// Mia-specific types
export interface MiaJoinRequest {
  meeting_url: string;
  bot_name: string;
  meeting_id: string;
  language?: string;
}
