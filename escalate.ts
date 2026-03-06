import * as child_process from "child_process";
import { type Config } from "./config.js";
import { log } from "./log.js";

function findOpenClaw(): string {
  try {
    return child_process.execFileSync("which", ["openclaw"], { encoding: "utf8" }).trim();
  } catch { /* not in PATH */ }
  for (const c of ["/opt/openclaw/bin/openclaw", "/usr/local/bin/openclaw"]) {
    try { child_process.execFileSync("test", ["-f", c]); return c; } catch { /* skip */ }
  }
  return "openclaw";
}

function notify(cfg: Config, jobId: string, taskId: string, body: string): void {
  const target = cfg.notifications.target || cfg.notifications.channel || "";
  const channel = cfg.notifications.escalation;

  if (!target && !channel) {
    log.warn(`No escalation target configured — logging only: [${jobId}] ${body}`);
    return;
  }

  const args = ["message", "send", "--target", target, "--message", body];
  if (channel) args.push("--channel", channel);

  try {
    child_process.execFileSync(findOpenClaw(), args, { encoding: "utf8" });
    log.info(`Escalation delivered for job ${jobId}`);
  } catch (e) {
    log.error(`Escalation failed for ${jobId}: ${e}`);
  }
}

export function notifyReady(cfg: Config, jobId: string, taskId: string): void {
  const msg = [
    `✅ **Task ready for review**`,
    ``,
    `The pipeline has completed and is waiting for your approval.`,
    ``,
    `Run: \`dispatch answer --job ${jobId} "approved"\` to close it out.`,
  ].join("\n");
  notify(cfg, jobId, taskId, `🚨 **Dispatch** — **Task:** ${taskId} | **Job:** ${jobId}\n\n${msg}`);
}

export function notifyFailure(cfg: Config, jobId: string, taskId: string, reason: string): void {
  const body = `🚨 **Dispatch** — **Task:** ${taskId} | **Job:** ${jobId}\n\n❌ **Job failed**\n\n${reason}`;
  notify(cfg, jobId, taskId, body);
}

export function notifyDeliverableRetry(cfg: Config, jobId: string, taskId: string, step: string, missing: string[], attempt: number, maxAttempts: number): void {
  const body = [
    `⚠️ **Dispatch** — **Task:** ${taskId} | **Step:** ${step}`,
    ``,
    `🔄 **Deliverable retry ${attempt}/${maxAttempts}** — agent didn't produce all required files.`,
    ``,
    `Missing: ${missing.map(f => `\`${f}\``).join(", ")}`,
    ``,
    `Re-dispatching automatically...`,
  ].join("\n");
  notify(cfg, jobId, taskId, body);
}

export function notifyMaxIterations(cfg: Config, taskId: string, step: string, maxIter: number): void {
  const body = `🚨 **Dispatch** — **Task:** ${taskId}\n\n🔁 **Review loop exhausted** — step \`${step}\` hit max iterations (${maxIter}).\nNeeds human decision to continue or close.`;
  notify(cfg, "", taskId, body);
}
