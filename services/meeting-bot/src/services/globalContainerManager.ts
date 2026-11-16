import { ContainerManager } from './containerManager';
import config from '../config';

// Global singleton — initialized once, imported everywhere
export const containerManager = new ContainerManager(config.maxConcurrentMeetings);
