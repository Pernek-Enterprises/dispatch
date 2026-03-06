import * as fs from "fs";
import * as readline from "readline";
import * as child_process from "child_process";

export interface PipeMessage {
  type: string;         // "done" | "fail" | "ask" | "answer" | "new_task"
  jobId?: string;
  taskId?: string;
  message?: string;
  question?: string;
  reason?: string;
  escalate?: boolean;
  artifacts?: string[];
}

/** Create a named FIFO pipe at path. Removes stale pipe first. */
export function createPipe(pipePath: string): void {
  try { fs.unlinkSync(pipePath); } catch { /* ok */ }
  child_process.execFileSync("mkfifo", [pipePath]);
}

/**
 * Continuously listen on a named FIFO pipe and call handler for each message.
 * Re-opens on EOF (writer disconnected) — same behaviour as Go implementation.
 */
export async function listenPipe(
  pipePath: string,
  handler: (msg: PipeMessage) => void
): Promise<void> {
  while (true) {
    await new Promise<void>((resolve) => {
      let stream: fs.ReadStream;
      try {
        stream = fs.createReadStream(pipePath);
      } catch {
        setTimeout(resolve, 200);
        return;
      }

      const rl = readline.createInterface({ input: stream, crlfDelay: Infinity });

      rl.on("line", (line) => {
        const trimmed = line.trim();
        if (!trimmed) return;
        try {
          const msg = JSON.parse(trimmed) as PipeMessage;
          handler(msg);
        } catch { /* skip malformed */ }
      });

      rl.on("close", resolve);
      stream.on("error", () => resolve());
    });

    // Brief pause before reopening (allows OS to clean up)
    await sleep(100);
  }
}

/** Send a message to the foreman via named pipe. */
export function sendPipe(pipePath: string, msg: PipeMessage): void {
  const data = JSON.stringify(msg) + "\n";
  const fd = fs.openSync(pipePath, "w");
  try {
    fs.writeSync(fd, data);
  } finally {
    fs.closeSync(fd);
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}
