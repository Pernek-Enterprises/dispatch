import { loadConfig } from "../config.js";
import { sendPipe } from "../pipe.js";

export function answerCmd(args: string[]): void {
  let jobId = "";
  const rest: string[] = [];

  for (let i = 0; i < args.length; i++) {
    if (args[i] === "--job" && args[i + 1]) { jobId = args[++i]; }
    else { rest.push(args[i]); }
  }

  if (!jobId) { console.error("Error: --job is required"); process.exit(1); }
  const answer = rest.join(" ");
  if (!answer) { console.error("Error: answer text is required"); process.exit(1); }

  const cfg = loadConfig();
  sendPipe(cfg.pipePath, { type: "answer", jobId, message: answer });
  console.log(`✓ Answer sent for job ${jobId}`);
}
