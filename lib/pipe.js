import { existsSync, unlinkSync, writeFileSync } from 'fs';
import { execSync } from 'child_process';
import { createReadStream } from 'fs';
import { createInterface } from 'readline';
import { loadConfig } from './state.js';

function getPipePath() {
  try {
    const config = loadConfig('config');
    return config.pipePath || '/tmp/dispatch.pipe';
  } catch {
    return process.env.DISPATCH_PIPE || '/tmp/dispatch.pipe';
  }
}

/**
 * Create the named pipe (foreman side).
 */
export function createPipe() {
  const pipePath = getPipePath();
  if (existsSync(pipePath)) {
    try { unlinkSync(pipePath); } catch { /* ignore */ }
  }
  execSync(`mkfifo ${pipePath}`);
  return pipePath;
}

/**
 * Listen for messages on the named pipe.
 * Re-opens after each EOF to keep listening.
 */
export function listenPipe(handler) {
  const pipePath = getPipePath();

  function openPipe() {
    const stream = createReadStream(pipePath, { encoding: 'utf-8' });
    const rl = createInterface({ input: stream });

    rl.on('line', (line) => {
      try {
        const msg = JSON.parse(line);
        handler(msg);
      } catch {
        // Ignore malformed messages
      }
    });

    rl.on('close', () => {
      setImmediate(openPipe);
    });

    stream.on('error', () => {
      setTimeout(openPipe, 100);
    });
  }

  openPipe();
}

/**
 * Send a message through the pipe (CLI side).
 */
export function sendMessage(msg) {
  const pipePath = getPipePath();
  writeFileSync(pipePath, JSON.stringify(msg) + '\n');
}

export { getPipePath };
