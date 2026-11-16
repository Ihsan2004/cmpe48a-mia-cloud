import { Page } from 'playwright';
import { AbstractMeetBot, JoinParams } from './AbstractMeetBot';

export class MeetBotBase extends AbstractMeetBot {
  protected page: Page;
  protected slightlySecretId: string; // Use any hard-to-guess identifier
  join(params: JoinParams): Promise<void> {
    throw new Error('Function not implemented.');
  }
}
