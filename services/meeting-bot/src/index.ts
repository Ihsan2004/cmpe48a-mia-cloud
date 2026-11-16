// Ensure global Web Crypto API is available (needed by Azure SDK, polyfill for older Node versions)
import './shims/crypto-polyfill';
import http from 'http';
import app, { redisConsumerService, setGracefulShutdown } from './app';
import { containerManager } from './services/globalContainerManager';
import messageBroker from './connect/messageBroker';
import config from './config';
import { closePool } from './lib/database';
import { loggerFactory } from './util/logger';

const logger = loggerFactory('system', 'server');

const port = 3000;

// Create Express server
const server = http.createServer(app);

server.listen(port, () => {
  logger.info('Server is running', { url: `http://localhost:${port}` });
});

// Flag to prevent multiple shutdown attempts
let shutdownInProgress = false;

const initiateGracefulShutdown = async () => {
  if (shutdownInProgress) {
    logger.info('Shutdown already in progress, ignoring signal');
    return;
  }

  shutdownInProgress = true;
  logger.info('Initiating graceful shutdown');

  try {
    // Set the graceful shutdown flag
    setGracefulShutdown(1);

    // Stop all meeting containers gracefully
    await containerManager.shutdown();

    // Now proceed with application shutdown
    gracefulShutdownApp();
  } catch (error) {
    logger.error('Error during graceful shutdown', { error });
    // Force exit if graceful shutdown fails
    process.exit(1);
  }
};

process.on('uncaughtException', (err) => {
  logger.error('Uncaught Exception', { error: err });
});

process.on('unhandledRejection', (reason, promise) => {
  logger.error('Unhandled Rejection', { reason });
});

process.on('SIGTERM', () => {
  logger.info('SIGTERM signal received, starting graceful shutdown');
  initiateGracefulShutdown();
});

process.on('SIGINT', () => {
  logger.info('SIGINT signal received, starting graceful shutdown');
  initiateGracefulShutdown();
});

process.on('SIGABRT', () => {
  logger.info('SIGABRT signal received, starting graceful shutdown');
  initiateGracefulShutdown();
});

export const gracefulShutdownApp = () => {
  // Complete existing requests, close database connections, etc.
  server.close(async () => {
    logger.info('HTTP server closed, exiting application');

    // Only shutdown Redis services if Redis is enabled
    if (config.isRedisEnabled) {
      await redisConsumerService.shutdown();
      await messageBroker.quitClientGracefully();
    } else {
      logger.info('Redis services not running, skipping Redis shutdown');
    }

    await closePool();
    logger.info('Exiting');
    process.exit(0);
  });
};
