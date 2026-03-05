import { existsSync, unlinkSync, writeFileSync } from 'fs';
import { execSync } from 'child_process';
import { createReadStream } from 'fs';
import { createInterface } from 'readline';

const PIPE_PATH = process.env.DISPATCH_PIPE || '/tmp/dispatch.pipe';

/**
 * Create the named pipe (foreman side).
 */
export function createPipe() {
  if (existsSync(PIPE_PATH)) {
    try { unlinkSync(PIPE_PATH); } catch { /* ignore */ }
  }
  execSync(`mkfifo ${PIPE_PATH}`);
  return PIPE_PATH;
}

/**
 * Listen for messages on the named pipe.
 * Each line is a JSON message from the CLI.
 * Calls handler(message) for each message.
 * Re-opens after each EOF to keep listening.
 */
export function listenPipe(handler) {
  function openPipe() {
    const stream = createReadStream(PIPE_PATH, { encoding: 'utf-8' });
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
      // Pipe closed (writer disconnected) — reopen
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
  writeFileSync(PIPE_PATH, JSON.stringify(msg) + '\n');
}

export { PIPE_PATH };
