/**
 * runner.ts — Pi SDK session manager.
 *
 * Core improvement over Go subprocess approach:
 *   1. Wrapped edit tool — "identical content" error → success (ends loops)
 *   2. Loop detection — same tool call 5x → abort + onFail
 *   3. task_done / task_ask / task_fail tools — model signals completion directly
 *   4. No subprocess reaping needed; sessions are in-process
 */

import * as fs from "fs";
import * as path from "path";
import * as os from "os";
import { Type, type Static, type TSchema } from "@sinclair/typebox";
import type { AgentTool } from "@mariozechner/pi-agent-core";
import {
  AuthStorage,
  createAgentSession,
  createBashTool,
  createEditTool,
  createReadTool,
  createWriteTool,
  DefaultResourceLoader,
  ModelRegistry,
  SessionManager,
  SettingsManager,
  type ToolDefinition,
} from "@mariozechner/pi-coding-agent";

import { type Config, resolveModel } from "./config.js";
import { type Job } from "./jobs.js";
import { log } from "./log.js";

export type TaskDoneCallback = (jobId: string, summary: string) => Promise<void>;
export type TaskAskCallback  = (jobId: string, question: string, escalate: boolean) => Promise<void>;
export type TaskFailCallback = (jobId: string, reason: string) => Promise<void>;

export interface RunnerCallbacks {
  onDone: TaskDoneCallback;
  onAsk:  TaskAskCallback;
  onFail: TaskFailCallback;
}

const activeSessions = new Map<string, { abort: () => Promise<void> }>();

export function dispatchJob(cfg: Config, job: Job, systemPrompt: string, callbacks: RunnerCallbacks): void {
  runSession(cfg, job, systemPrompt, callbacks).catch((err) => {
    log.error(`Runner error for ${job.id}: ${err}`);
    callbacks.onFail(job.id, `Runner error: ${err}`).catch(() => {});
  });
}

export async function abortSession(jobId: string): Promise<void> {
  const sess = activeSessions.get(jobId);
  if (sess) { await sess.abort(); activeSessions.delete(jobId); }
}

// ─── Tool schemas ────────────────────────────────────────────────────────────

const DoneSchema = Type.Object({
  summary: Type.String({ description: "Brief summary of completed work" }),
});
const AskSchema = Type.Object({
  question: Type.String({ description: "The question you need answered" }),
  escalate: Type.Optional(Type.Boolean({ description: "Escalate to human (true) or try auto-answer (false)" })),
});
const FailSchema = Type.Object({
  reason: Type.String({ description: "What went wrong and why you cannot continue" }),
});

// ─── Session runner ───────────────────────────────────────────────────────────

async function runSession(
  cfg: Config,
  job: Job,
  systemPrompt: string,
  callbacks: RunnerCallbacks
): Promise<void> {
  const modelCfg = resolveModel(cfg, job.model ?? "local-9b");

  const dispatchRoot = process.env.DISPATCH_ROOT ?? path.join(os.homedir(), ".dispatch");
  const cwd = path.join(dispatchRoot, "artifacts", job.task);
  fs.mkdirSync(cwd, { recursive: true });

  const agentDir = path.join(os.homedir(), ".pi", "agent");
  const authStorage = AuthStorage.create(path.join(agentDir, "auth.json"));
  authStorage.setRuntimeApiKey(modelCfg.provider, modelCfg.apiKey ?? "none");

  const modelRegistry = new ModelRegistry(authStorage, path.join(agentDir, "models.json"));
  const model = modelRegistry.find(modelCfg.provider, modelCfg.model);
  if (!model) throw new Error(`Model not found: ${modelCfg.provider}/${modelCfg.model} — add to ~/.pi/agent/models.json`);

  let doneSignalled = false;
  let loopAborted = false;
  const recentCalls: string[] = [];

  // ─── Wrapped edit tool ───────────────────────────────────────────────────
  // Replace built-in edit tool: normalize "identical content" errors to success.
  // Uses createEditTool(cwd) internally but intercepts its result.
  // Cast via unknown (origEdit has narrower generic params than AgentTool interface)
  const origEdit = createEditTool(cwd) as unknown as AgentTool;
  const wrappedEdit: ToolDefinition = {
    name: "edit",
    label: "Edit File",
    description: (origEdit as { description?: string }).description ?? "Edit a file",
    parameters: origEdit.parameters,
    execute: async (toolCallId, params, signal, onUpdate, _ctx) => {
      try {
        return await origEdit.execute(toolCallId, params as Static<TSchema>, signal, onUpdate);
      } catch (e) {
        const msg = String(e);
        if (msg.includes("identical content") || msg.includes("No changes made")) {
          log.info(`[edit] identical content — returning success (job ${job.id})`);
          return {
            content: [{ type: "text" as const, text: "File already has the correct content. No changes needed." }],
            details: {},
          };
        }
        throw e; // re-throw real errors
      }
    },
  };

  // ─── task_done ────────────────────────────────────────────────────────────
  const taskDoneTool: ToolDefinition<typeof DoneSchema> = {
    name: "task_done",
    label: "Task Done",
    description: "Call this when ALL your work is complete. Provide a summary.",
    parameters: DoneSchema,
    execute: async (_id, params, _signal, _onUpdate, _ctx) => {
      doneSignalled = true;
      log.info(`task_done: job ${job.id} — ${params.summary}`);
      setImmediate(() => callbacks.onDone(job.id, params.summary).catch((e) => log.error(`onDone: ${e}`)));
      return { content: [{ type: "text" as const, text: "Acknowledged. Work complete." }], details: {} };
    },
  };

  // ─── task_ask ─────────────────────────────────────────────────────────────
  const taskAskTool: ToolDefinition<typeof AskSchema> = {
    name: "task_ask",
    label: "Task Ask",
    description: "Ask a question when you are blocked. Set escalate=true to reach the human.",
    parameters: AskSchema,
    execute: async (_id, params, _signal, _onUpdate, _ctx) => {
      log.info(`task_ask: job ${job.id} — escalate=${params.escalate ?? false}`);
      setImmediate(() => callbacks.onAsk(job.id, params.question, params.escalate ?? false).catch((e) => log.error(`onAsk: ${e}`)));
      return { content: [{ type: "text" as const, text: "Question received." }], details: {} };
    },
  };

  // ─── task_fail ────────────────────────────────────────────────────────────
  const taskFailTool: ToolDefinition<typeof FailSchema> = {
    name: "task_fail",
    label: "Task Fail",
    description: "Call this if you cannot complete the task. Explain what went wrong.",
    parameters: FailSchema,
    execute: async (_id, params, _signal, _onUpdate, _ctx) => {
      log.error(`task_fail: job ${job.id} — ${params.reason}`);
      doneSignalled = true; // prevent "no signal" fallback
      setImmediate(() => callbacks.onFail(job.id, params.reason).catch((e) => log.error(`onFail: ${e}`)));
      return { content: [{ type: "text" as const, text: "Failure recorded." }], details: {} };
    },
  };

  // ─── Resource loader ──────────────────────────────────────────────────────
  const settingsMgr = SettingsManager.inMemory({ compaction: { enabled: false } });
  const loader = new DefaultResourceLoader({
    cwd,
    agentDir,
    settingsManager: settingsMgr,
    systemPromptOverride: () => systemPrompt,
  });
  await loader.reload();

  // ─── Create session ───────────────────────────────────────────────────────
  const { session } = await createAgentSession({
    cwd,
    agentDir,
    model,
    thinkingLevel: "off",
    authStorage,
    modelRegistry,
    // Exclude edit from tools (replaced by wrappedEdit in customTools)
    tools: [createReadTool(cwd), createWriteTool(cwd), createBashTool(cwd)],
    customTools: [wrappedEdit, taskDoneTool as unknown as ToolDefinition, taskAskTool as unknown as ToolDefinition, taskFailTool as unknown as ToolDefinition],
    resourceLoader: loader,
    sessionManager: SessionManager.create(cwd, path.join(dispatchRoot, "sessions")),
    settingsManager: settingsMgr,
  });

  activeSessions.set(job.id, { abort: () => session.abort() });

  // ─── Loop detection ───────────────────────────────────────────────────────
  session.subscribe((event) => {
    if (loopAborted || doneSignalled) return;
    if (event.type === "tool_execution_start") {
      const e = event as { toolName?: string; toolInput?: unknown };
      const sig = `${e.toolName ?? "?"}:${JSON.stringify(e.toolInput ?? {})}`;
      recentCalls.push(sig);
      if (recentCalls.length > 20) recentCalls.shift();

      if (recentCalls.length >= 5 && recentCalls.slice(-5).every((c) => c === sig)) {
        loopAborted = true;
        log.warn(`Loop detected in job ${job.id}: ${e.toolName} repeated 5× identically`);
        const reason = `Tool call loop: ${e.toolName} called with identical parameters 5 times`;
        session.abort().then(() => {
          callbacks.onFail(job.id, reason).catch(() => {});
        }).catch(() => {});
      }
    }
  });

  // ─── Session log ──────────────────────────────────────────────────────────
  const logPath = path.join(dispatchRoot, "logs", `pi-${job.id}.log`);
  const logStream = fs.createWriteStream(logPath, { flags: "a" });
  session.subscribe((event) => {
    if (event.type === "message_update") {
      const e = event as { assistantMessageEvent?: { type?: string; delta?: string } };
      if (e.assistantMessageEvent?.delta) logStream.write(e.assistantMessageEvent.delta);
    }
  });

  log.info(`Session start: ${job.model} / ${job.id}`);
  try {
    await session.prompt(job.prompt ?? "");
  } finally {
    activeSessions.delete(job.id);
    logStream.end();
    session.dispose();
  }

  if (!doneSignalled && !loopAborted) {
    log.warn(`Job ${job.id} ended without task_done`);
    await callbacks.onFail(job.id, "Session ended without calling task_done or task_fail");
  }
}
