import { appendFileSync, mkdirSync } from 'fs';
import { join } from 'path';
import { ROOT } from './state.js';

const LOGS_DIR = join(ROOT, 'logs');
mkdirSync(LOGS_DIR, { recursive: true });

function ts() {
  return new Date().toISOString();
}

function logLine(level, msg, data) {
  const date = new Date().toISOString().split('T')[0];
  const line = `[${ts()}] ${level}: ${msg}${data ? ' ' + JSON.stringify(data) : ''}\n`;
  process.stderr.write(line);
  try {
    appendFileSync(join(LOGS_DIR, `${date}.log`), line);
  } catch { /* ignore */ }
}

export const log = {
  info: (msg, data) => logLine('INFO', msg, data),
  warn: (msg, data) => logLine('WARN', msg, data),
  error: (msg, data) => logLine('ERROR', msg, data)
};
