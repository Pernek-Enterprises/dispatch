/**
 * triage.ts — Agentic failure recovery.
 *
 * When a job fails, the foreman can optionally ask a 27B model to diagnose
 * what happened and recommend a recovery action. This runs ONCE per job
 * (tracked in state.json) — if triage itself fails, the job escalates to human.
 *
 * Triage uses a direct LLM call (no tools) — structured JSON output only.
 * It never writes files or modifies state directly. The foreman applies
 * triage decisions deterministically.
 */

import * as fs from "fs";
import * as path from "path";
import { type Config, resolveModel } from "./config.js";
import { type Job } from "./jobs.js";
import { log } from "./log.js";

// ─── Action types ─────────────────────────────────────────────────────────────

export type TriageAction =
  | { action: "retry";    reason: string; modifiedPrompt: string }
  | { action: "done";     reason: string; summary: string }
  | { action: "skip";     reason: string }
  | { action: "escalate"; reason: string };

// ─── System prompt ────────────────────────────────────────────────────────────

const TRIAGE_SYSTEM = `You are a diagnostic agent for an AI coding pipeline. A job step has failed or got stuck. Analyze what happened and recommend ONE recovery action.

Respond with ONLY valid JSON — no markdown fences, no explanation, nothing else.

Action types:
  retry:    {"action":"retry","reason":"...","modifiedPrompt":"<instructions to ADD to the agent's prompt — address the specific problem>"}
  done:     {"action":"done","reason":"...","summary":"<task_done summary — use this if work was finished but signal was missed>"}
  skip:     {"action":"skip","reason":"<why this step can be safely skipped>"}
  escalate: {"action":"escalate","reason":"<why a human decision is needed>"}

Decision guide:
- retry  → work is incomplete but a clearer or more specific prompt would fix it
- done   → artifacts/logs show the work was actually completed (agent looped after finishing or forgot to call task_done)
- skip   → step is not critical and the workflow can safely advance without it
- escalate → none of the above; human judgement is genuinely needed`;

// ─── Context builder ──────────────────────────────────────────────────────────

function buildTriageContext(job: Job, failureReason: string, dispatchRoot: string): string {
  const artifactDir = path.join(dispatchRoot, "artifacts", job.task);
  const artifacts = fs.existsSync(artifactDir)
    ? fs.readdirSync(artifactDir).join(", ")
    : "(none)";

  const logPath = path.join(dispatchRoot, "logs", `pi-${job.id}.log`);
  const lastLog = fs.existsSync(logPath)
    ? fs.readFileSync(logPath, "utf8").split("\n").slice(-60).join("\n")
    : "(no log available)";

  const promptTail = (job.prompt ?? "").slice(-600);

  return [
    `## Failed Job`,
    ``,
    `Task: ${job.task}`,
    `Step: ${job.step}`,
    `Role: ${job.agent ?? "unknown"}`,
    `Model: ${job.model ?? "unknown"}`,
    `Failure reason: ${failureReason}`,
    ``,
    `## Artifacts present in ${artifactDir}`,
    artifacts,
    ``,
    `## Last 60 lines of agent log`,
    "```",
    lastLog,
    "```",
    ``,
    `## End of step prompt (last 600 chars)`,
    "```",
    promptTail,
    "```",
  ].join("\n");
}

// ─── Main triage function ─────────────────────────────────────────────────────

export async function runTriage(
  cfg: Config,
  job: Job,
  failureReason: string,
  dispatchRoot: string,
): Promise<TriageAction> {
  if (cfg.triage?.enabled === false) {
    return { action: "escalate", reason: "Triage disabled in config" };
  }

  const triageModelKey = cfg.triage?.model ?? "local-27b";
  let modelCfg;
  try {
    modelCfg = resolveModel(cfg, triageModelKey);
  } catch (e) {
    log.warn(`Triage: cannot resolve model ${triageModelKey}: ${e}`);
    return { action: "escalate", reason: `Triage model not configured: ${e}` };
  }

  const context = buildTriageContext(job, failureReason, dispatchRoot);
  const timeoutMs = cfg.triage?.timeoutMs ?? 90_000;

  log.info(`Triage starting for job ${job.id} (model: ${triageModelKey}, timeout: ${timeoutMs}ms)`);

  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);

  try {
    const res = await fetch(`${modelCfg.endpoint}/chat/completions`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${modelCfg.apiKey ?? "none"}`,
      },
      body: JSON.stringify({
        model: modelCfg.model,
        messages: [
          { role: "system", content: TRIAGE_SYSTEM },
          { role: "user", content: context },
        ],
        temperature: 0.1,
        max_tokens: 512,
      }),
      signal: controller.signal,
    });

    if (!res.ok) {
      throw new Error(`HTTP ${res.status}: ${await res.text()}`);
    }

    const data = await res.json() as { choices?: Array<{ message?: { content?: string } }> };
    const raw = data.choices?.[0]?.message?.content?.trim() ?? "";

    // Strip markdown code fences if model added them
    const json = raw
      .replace(/^```(?:json)?\s*/i, "")
      .replace(/\s*```$/, "")
      .trim();

    const action = JSON.parse(json) as TriageAction;

    if (!["retry", "done", "skip", "escalate"].includes(action.action)) {
      throw new Error(`Unknown action: ${action.action}`);
    }

    log.info(`Triage decision for ${job.id}: ${action.action} — ${action.reason.slice(0, 100)}`);
    return action;

  } catch (e) {
    if ((e as Error).name === "AbortError") {
      log.warn(`Triage timed out for ${job.id} after ${timeoutMs}ms`);
      return { action: "escalate", reason: "Triage timed out" };
    }
    log.warn(`Triage error for ${job.id}: ${e}`);
    return { action: "escalate", reason: `Triage failed: ${e}` };
  } finally {
    clearTimeout(timer);
  }
}
