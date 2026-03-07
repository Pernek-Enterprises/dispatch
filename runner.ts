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
import { type Project, buildContextBlock } from "./project.js";
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

export function dispatchJob(cfg: Config, job: Job, systemPrompt: string, callbacks: RunnerCallbacks, project?: Project): void {
  runSession(cfg, job, systemPrompt, callbacks, 0, project).catch((err) => {
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
const LoopSuccessSchema = Type.Object({
  summary: Type.Optional(Type.String({ description: "Optional: brief note on what made the condition pass" })),
});

// ─── Session runner ───────────────────────────────────────────────────────────

const MAX_LOOP_RECOVERIES = 2;

async function runSession(
  cfg: Config,
  job: Job,
  systemPrompt: string,
  callbacks: RunnerCallbacks,
  recoveryAttempt: number,
  project?: Project,
): Promise<void> {
  const modelCfg = resolveModel(cfg, job.model ?? "local-9b");

  const dispatchRoot = process.env.DISPATCH_ROOT ?? path.join(os.homedir(), ".dispatch");
  const artifactDir = path.join(dispatchRoot, "artifacts", job.task);
  fs.mkdirSync(artifactDir, { recursive: true });

  // CWD: use project workspace if set, else artifacts dir
  const cwd = project?.workspace ?? artifactDir;
  if (project?.workspace && !fs.existsSync(project.workspace)) {
    log.warn(`Project workspace does not exist: ${project.workspace} — falling back to artifacts dir`);
    // Don't fail hard — model may create files in artifacts, hooks will warn
  }

  // Build full prompt: prepend project context block if project is set
  const basePrompt = job.prompt ?? "";
  const fullPrompt = project
    ? `${buildContextBlock(project, job.task)}\n\n---\n\n${basePrompt}`
    : basePrompt;

  const agentDir = path.join(os.homedir(), ".pi", "agent");
  const authStorage = AuthStorage.create(path.join(agentDir, "auth.json"));
  authStorage.setRuntimeApiKey(modelCfg.provider, modelCfg.apiKey ?? "none");

  const modelRegistry = new ModelRegistry(authStorage, path.join(agentDir, "models.json"));
  const model = modelRegistry.find(modelCfg.provider, modelCfg.model);
  if (!model) throw new Error(`Model not found: ${modelCfg.provider}/${modelCfg.model} — add to ~/.pi/agent/models.json`);

  let doneSignalled = false;
  let loopSuccessSignalled = false;
  let loopAborted = false;
  let loopToolName = "";
  const recentCalls: string[] = [];
  // Forward ref — set after createAgentSession so tools can abort the session
  let abortFn: (() => Promise<void>) | undefined;

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

  // ─── Wrapped bash tool ───────────────────────────────────────────────────
  // The Pi SDK bash tool returns isError:true on non-zero exit codes, which
  // causes the model to retry the same command in a loop. Wrap it to always
  // return success (with exit code + output) so the model treats it as info.
  const origBash = createBashTool(cwd) as unknown as AgentTool;
  const wrappedBash: ToolDefinition = {
    name: "bash",
    label: "Bash",
    description: (origBash as { description?: string }).description ?? "Run a bash command",
    parameters: origBash.parameters,
    execute: async (toolCallId, params, signal, onUpdate, _ctx) => {
      try {
        return await origBash.execute(toolCallId, params as Static<TSchema>, signal, onUpdate);
      } catch (e) {
        // Non-zero exit: return output as informational text.
        // Strip "Error: " prefixes and "Command exited with code N" framing so
        // the model reads it as script output rather than a tool failure.
        const raw = String(e);
        const lines = raw.split("\n").filter(l =>
          !l.startsWith("Error: Command aborted") &&
          !l.match(/^Error: Command exited with code \d+/) &&
          l !== ""
        );
        // Remove leading "Error: " from remaining lines (exception wrapping)
        const cleaned = lines.map(l => l.replace(/^Error:\s*/, "")).join("\n").trim();
        const output = cleaned || "(no output)";
        log.info(`[bash] non-zero exit — returning as info (job ${job.id}): ${output.slice(0, 80)}`);
        return {
          content: [{ type: "text" as const, text: `Command output:\n${output}` }],
          details: {},
        };
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
      if (doneSignalled) return { content: [{ type: "text" as const, text: "Already acknowledged." }], details: {} };
      doneSignalled = true;
      log.info(`task_done: job ${job.id} — ${params.summary}`);
      const summary = params.summary;
      setImmediate(() => {
        callbacks.onDone(job.id, summary).catch((e) => log.error(`onDone: ${e}`));
        abortFn?.().catch(() => {}); // abort session so model stops generating
      });
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
      if (doneSignalled) return { content: [{ type: "text" as const, text: "Already acknowledged." }], details: {} };
      log.error(`task_fail: job ${job.id} — ${params.reason}`);
      doneSignalled = true;
      const reason = params.reason;
      setImmediate(() => {
        callbacks.onFail(job.id, reason).catch((e) => log.error(`onFail: ${e}`));
        abortFn?.().catch(() => {});
      });
      return { content: [{ type: "text" as const, text: "Failure recorded." }], details: {} };
    },
  };

  // ─── signal_loop_success ──────────────────────────────────────────────────
  // Registered only when job.loop is set. Ends the TDD loop cleanly.
  const signalLoopSuccessTool: ToolDefinition<typeof LoopSuccessSchema> = {
    name: "signal_loop_success",
    label: "Signal Loop Success",
    description: "Call this when the loop breakout condition is satisfied (e.g. all tests pass). Do NOT call task_done — use this instead.",
    parameters: LoopSuccessSchema,
    execute: async (_id, params, _signal, _onUpdate, _ctx) => {
      if (doneSignalled) return { content: [{ type: "text" as const, text: "Already acknowledged." }], details: {} };
      loopSuccessSignalled = true;
      doneSignalled = true;
      const note = params.summary ?? "Loop condition satisfied";
      log.info(`signal_loop_success: job ${job.id} — ${note}`);
      setImmediate(() => {
        callbacks.onDone(job.id, note).catch((e) => log.error(`onDone (loop): ${e}`));
        abortFn?.().catch(() => {});
      });
      return { content: [{ type: "text" as const, text: "Loop ended. Great work." }], details: {} };
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
    // Exclude edit and bash from tools (replaced by wrapped versions in customTools)
    tools: [createReadTool(cwd), createWriteTool(cwd)],
    customTools: [
      wrappedEdit, wrappedBash,
      taskDoneTool as unknown as ToolDefinition,
      taskAskTool as unknown as ToolDefinition,
      taskFailTool as unknown as ToolDefinition,
      // signal_loop_success only registered for TDD loop steps
      ...(job.loop ? [signalLoopSuccessTool as unknown as ToolDefinition] : []),
    ],
    resourceLoader: loader,
    sessionManager: SessionManager.create(cwd, path.join(dispatchRoot, "sessions")),
    settingsManager: settingsMgr,
  });

  activeSessions.set(job.id, { abort: () => session.abort() });
  abortFn = () => session.abort(); // wire up for task_done/task_fail

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
        loopToolName = e.toolName ?? "unknown";
        log.warn(`Loop detected in job ${job.id}: ${loopToolName} repeated 5× identically`);
        session.abort().catch(() => {}); // abort; recovery handled after prompt() returns
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
    await session.prompt(fullPrompt);

    // ─── TDD loop ────────────────────────────────────────────────────────────
    // If this step has a loop config and the agent didn't signal completion yet,
    // keep firing continuation prompts until signal_loop_success (or task_done)
    // is called, or max iterations is exhausted.
    if (job.loop && !doneSignalled && !loopAborted) {
      const maxIter = job.maxLoopIterations ?? 10;
      let iter = 0;

      while (!doneSignalled && !loopAborted && iter < maxIter) {
        iter++;
        log.info(`TDD loop iteration ${iter}/${maxIter} for job ${job.id} (loop=${job.loop})`);
        const continuationPrompt = buildLoopContinuationPrompt(job.loop, iter, maxIter);
        await session.prompt(continuationPrompt);
      }

      if (!doneSignalled && !loopAborted) {
        log.warn(`TDD loop exhausted (${maxIter} iterations) for job ${job.id}`);
        await callbacks.onFail(job.id, `TDD loop exhausted after ${maxIter} iterations without satisfying the loop condition`);
        return;
      }
    }
  } finally {
    activeSessions.delete(job.id);
    logStream.end();
    session.dispose();
  }

  if (loopAborted && !doneSignalled) {
    if (recoveryAttempt < MAX_LOOP_RECOVERIES) {
      log.warn(`Loop recovery attempt ${recoveryAttempt + 1}/${MAX_LOOP_RECOVERIES} for job ${job.id} (tool: ${loopToolName})`);
      const recoveryJob: Job = { ...job, prompt: (job.prompt ?? "") +
        `\n\n---\n\n` +
        `⚠️ **Recovery attempt ${recoveryAttempt + 1}/${MAX_LOOP_RECOVERIES}:** You were stuck calling \`${loopToolName}\` ` +
        `with the same parameters repeatedly. Stop and reassess what you have done so far.\n\n` +
        `- If your work is complete, call \`task_done\` now.\n` +
        `- If you are genuinely blocked, call \`task_ask\`.\n` +
        `- Otherwise, try a **different** approach — do not repeat the same command.`,
      };
      await runSession(cfg, recoveryJob, systemPrompt, callbacks, recoveryAttempt + 1, project);
      return;
    }
    log.warn(`Max loop recoveries (${MAX_LOOP_RECOVERIES}) exhausted for job ${job.id}`);
    await callbacks.onFail(job.id, `Tool call loop on \`${loopToolName}\` — ${MAX_LOOP_RECOVERIES} recovery attempts failed`);
    return;
  }

  if (!doneSignalled && !loopAborted) {
    log.warn(`Job ${job.id} ended without task_done`);
    await callbacks.onFail(job.id, "Session ended without calling task_done or task_fail");
  }
}

// ─── Loop continuation prompts ────────────────────────────────────────────────

function buildLoopContinuationPrompt(loopType: string, iter: number, maxIter: number): string {
  const iterNote = `(iteration ${iter}/${maxIter})`;
  if (loopType === "tests") {
    return (
      `Some tests are still failing ${iterNote}. ` +
      `Run the test suite, identify what is failing, and continue implementing. ` +
      `When ALL tests pass (exit code 0), call \`signal_loop_success\`. ` +
      `If you are genuinely stuck and cannot proceed, call \`task_ask\` instead.`
    );
  }
  // Generic fallback for custom loop conditions
  return (
    `The loop condition has not been satisfied yet ${iterNote}. ` +
    `Continue working. When the condition is satisfied, call \`signal_loop_success\`. ` +
    `If you are stuck, call \`task_ask\`.`
  );
}
