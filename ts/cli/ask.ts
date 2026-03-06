import { loadConfig } from "../config.js";
import { sendPipe } from "../pipe.js";

export function askCmd(args: string[]): void {
  let jobId = process.env.DISPATCH_JOB_ID ?? "";
  let escalate = false;
  const rest: string[] = [];

  for (let i = 0; i < args.length; i++) {
    if ((args[i] === "--job" || args[i] === "-j") && args[i + 1]) { jobId = args[++i]; }
    else if (args[i] === "--escalate") { escalate = true; }
    else { rest.push(args[i]); }
  }

  if (!jobId) { console.error("dispatch: --job is required"); process.exit(1); }
  const question = rest.join(" ");
  if (!question) { console.error("dispatch: question text is required"); process.exit(1); }

  const cfg = loadConfig();
  sendPipe(cfg.pipePath, { type: "ask", jobId, question, escalate });
  console.log(`✓ Question sent for job ${jobId}`);
}
