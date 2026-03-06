import * as fs from "fs";
import * as path from "path";
import { ROOT } from "./config.js";

function today(): string {
  return new Date().toISOString().slice(0, 10);
}

function timestamp(): string {
  return new Date().toISOString().replace(/\.\d+Z$/, "Z");
}

function write(level: string, msg: string): void {
  const line = `[${timestamp()}] ${level}: ${msg}`;
  console.error(line);
  try {
    const logDir = path.join(ROOT, "logs");
    fs.mkdirSync(logDir, { recursive: true });
    fs.appendFileSync(path.join(logDir, `${today()}.log`), line + "\n");
  } catch { /* best effort */ }
}

export const log = {
  info:  (msg: string) => write("INFO", msg),
  warn:  (msg: string) => write("WARN", msg),
  error: (msg: string) => write("ERROR", msg),
};
