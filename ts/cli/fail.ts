import { loadConfig } from "../config.js";
import { sendPipe } from "../pipe.js";

export function failCmd(args: string[]): void {
  let jobId = process.env.DISPATCH_JOB_ID ?? "";
  const rest: string[] = [];

  for (let i = 0; i < args.length; i++) {
    if ((args[i] === "--job" || args[i] === "-j") && args[i + 1]) { jobId = args[++i]; }
    else { rest.push(args[i]); }
  }

  if (!jobId) { console.error("dispatch: --job is required"); process.exit(1); }
  const reason = rest.join(" ");
  if (!reason) { console.error("dispatch: reason is required"); process.exit(1); }

  const cfg = loadConfig();
  sendPipe(cfg.pipePath, { type: "fail", jobId, reason });
  console.log(`✓ Failure reported for job ${jobId}`);
}
