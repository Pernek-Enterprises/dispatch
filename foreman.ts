/**
 * foreman.ts — Main event loop and workflow state machine.
 * Port of cmd/foreman.go with the Pi SDK runner substituted for subprocess Pi.
 */

import * as fs from "fs";
import * as path from "path";
import * as os from "os";
import { ROOT, loadConfig, type Config } from "./config.js";
import {
  listJobs, moveJob, getJobMeta, writeResult, writeJobMeta, createJob, type Job,
} from "./jobs.js";
import { loadWorkflow, getNextStep, getRole, getDestroyAgents, type Workflow, type Step } from "./workflows.js";
import { State, type TaskState } from "./state.js";
import { createPipe, listenPipe, type PipeMessage } from "./pipe.js";
import { notifyReady, notifyFailure, notifyMaxIterations, notifyDeliverableRetry, notifyTriageAction } from "./escalate.js";
import { runTriage } from "./triage.js";
import { loadProject, runHooks, type Project, type HookContext } from "./project.js";
import { dispatchJob, abortSession } from "./runner.js";
import { log } from "./log.js";
import { loadSystemPromptPublic } from "./prompts.js";

// ─── System prompt loading ──────────────────────────────────────────────────

// Use shared system prompt loader
const loadSystemPrompt = loadSystemPromptPublic;

function loadDestroyPrompt(workflowName: string): string {
  const promptPath = path.join(ROOT, "workflows", workflowName, "destroy.prompt.md");
  if (fs.existsSync(promptPath)) return fs.readFileSync(promptPath, "utf8").trim();
  return `This task is complete. Before you finish:
1. Write a brief summary of what you did
2. Note any lessons learned
3. Clean up any temporary files

When done, call \`task_done\` with "cleanup complete".`;
}

// ─── Workflow advancement ────────────────────────────────────────────────────

/** Check that all declared artifactsOut files exist. Returns list of missing files. */
function checkDeliverables(job: Job, wf: Workflow): string[] {
  const step = wf.steps[job.step];
  if (!step?.artifactsOut?.length) return [];
  const artifactDir = path.join(ROOT, "artifacts", job.task);
  return step.artifactsOut.filter(f => !fs.existsSync(path.join(artifactDir, f)));
}

function advanceWorkflow(cfg: Config, st: State, completedJob: Job, result: string, project?: Project): void {
  let wf: Workflow;
  try { wf = loadWorkflow(completedJob.workflow); }
  catch (e) { log.warn(`Workflow ${completedJob.workflow} not found: ${e}`); return; }

  // Destroy step completing?
  if (completedJob.step.startsWith("_destroy:")) {
    handleDestroyComplete(cfg, st, completedJob, wf);
    return;
  }

  // ─── Deliverables gate ───────────────────────────────────────────────────
  // Check all declared artifactsOut files exist before advancing.
  const missingFiles = checkDeliverables(completedJob, wf);
  if (missingFiles.length > 0) {
    const maxRetries = cfg.maxDeliverableRetries ?? 3;
    const attempt = st.incrementDeliverableRetries(completedJob.task, completedJob.step);
    log.warn(`Deliverables missing for ${completedJob.id} (attempt ${attempt}/${maxRetries}): ${missingFiles.join(", ")}`);
    notifyDeliverableRetry(cfg, completedJob.id, completedJob.task, completedJob.step, missingFiles, attempt, maxRetries);

    if (attempt >= maxRetries) {
      log.warn(`Max deliverable retries (${maxRetries}) for ${completedJob.step} on task ${completedJob.task} — failing`);
      notifyFailure(cfg, completedJob.id, completedJob.task, `Step '${completedJob.step}' failed to produce required files after ${maxRetries} attempts: ${missingFiles.join(", ")}`);
      return;
    }

    // Re-dispatch the same step with a retry prompt appended
    const retryJob: Job = {
      ...completedJob,
      id: completedJob.id, // reuse same job so model/lock context is consistent
      prompt: (completedJob.prompt ?? "") + `\n\n---\n\n**⚠️ Retry ${attempt}/${maxRetries}: Missing deliverables**\n\nYour previous run did not produce all required files.\nMissing: ${missingFiles.map(f => `\`${f}\``).join(", ")}\n\nPlease create the missing files and call \`task_done\` again.`,
    };
    if (completedJob.model) st.lockModel(completedJob.model, completedJob.id);
    moveJob(completedJob.id, "done", "active");
    dispatchJobFromMeta(cfg, retryJob);
    return;
  }
  st.clearDeliverableRetries(completedJob.task, completedJob.step);

  const nextStepName = getNextStep(wf, completedJob.step, result);
  if (!nextStepName) {
    const currentStep = wf.steps[completedJob.step];
    // Branch step with no keyword match → escalate to human rather than silently going terminal
    if (currentStep?.branch && Object.keys(currentStep.branch).length > 0) {
      log.warn(`Task ${completedJob.task}: branch step '${completedJob.step}' result contained no routing keyword — escalating to human`);
      notifyReady(cfg, completedJob.id, completedJob.task);
      return;
    }
    log.info(`Task ${completedJob.task} reached terminal step — starting destroy phase`);
    startDestroy(cfg, st, completedJob.task, wf);
    return;
  }

  const nextStep = wf.steps[nextStepName];
  if (!nextStep) {
    log.error(`Step ${nextStepName} not found in workflow ${completedJob.workflow}`);
    return;
  }

  // Run branch-specific hooks (e.g. after_review_accepted, after_review_denied)
  if (project) {
    const dispatchRoot = process.env.DISPATCH_ROOT ?? path.join(os.homedir(), ".dispatch");
    const currentStep = wf.steps[completedJob.step];
    if (currentStep?.branch) {
      const upper = result.toUpperCase();
      for (const keyword of Object.keys(currentStep.branch)) {
        if (upper.includes(keyword.toUpperCase())) {
          const hookCtx: HookContext = {
            project,
            taskId: completedJob.task,
            step: completedJob.step,
            artifactDir: path.join(dispatchRoot, "artifacts", completedJob.task),
            workspace: project.workspace ?? path.join(dispatchRoot, "artifacts", completedJob.task),
          };
          runHooks(`after_${completedJob.step}_${keyword.toLowerCase()}`, hookCtx);
          break;
        }
      }
    }
  }

  // Iteration tracking
  let ts = st.getTask(completedJob.task);
  if (!ts) {
    ts = {
      workflow: completedJob.workflow,
      currentStep: nextStepName,
      status: "active",
      iteration: {},
      created: new Date().toISOString(),
    };
  }

  const stepIter = (ts.iteration[nextStepName] ?? 0) + 1;
  const maxIter = nextStep.maxIterations ?? cfg.maxLoopIterations ?? 3;

  if (stepIter > maxIter) {
    log.warn(`Max iterations (${maxIter}) for ${nextStepName} on task ${completedJob.task} — escalating`);
    notifyMaxIterations(cfg, completedJob.task, nextStepName, maxIter);
    return;
  }

  ts.currentStep = nextStepName;
  ts.status = "active";
  ts.iteration[nextStepName] = stepIter;
  st.setTask(completedJob.task, ts);

  const role = getRole(nextStep);
  const systemPrompt = loadSystemPrompt(role);
  const artifactDir = path.join(ROOT, "artifacts", completedJob.task);
  const artifactNote = nextStep.artifactsIn?.length
    ? `\n\n## Artifacts\nAvailable in: ${artifactDir}\nFiles: ${nextStep.artifactsIn.join(", ")}`
    : "";

  const stepPrompt = `# Task: ${completedJob.task}\n\n## Step: ${nextStepName}\n\n${nextStep.prompt ?? ""}${artifactNote}`;

  const jobType = (nextStep.agent === "stefan" || nextStep.type === "human") ? "human" : "work";

  createJob({
    task: completedJob.task,
    workflow: completedJob.workflow,
    step: nextStepName,
    agent: role,
    model: nextStep.model,
    type: jobType,
    priority: "normal",
    timeout: nextStep.timeout ?? 120,
    iteration: stepIter,
    loop: nextStep.loop,
    maxLoopIterations: nextStep.maxLoopIterations,
    prompt: systemPrompt + "\n\n---\n\n" + stepPrompt,
  });

  log.info(`Created job for step ${nextStepName} (role=${role}, model=${nextStep.model})`);
}

// ─── Destroy phase ────────────────────────────────────────────────────────────

function startDestroy(cfg: Config, st: State, taskId: string, wf: Workflow): void {
  const agents = getDestroyAgents(wf);
  if (!agents.length) {
    runDestroyActions(cfg, st, taskId, wf);
    return;
  }

  const destroyPrompt = loadDestroyPrompt(wf.name);

  let ts = st.getTask(taskId);
  if (!ts) {
    ts = { workflow: wf.name, currentStep: "_destroy", status: "destroying", iteration: {} };
  } else {
    ts.status = "destroying";
    ts.currentStep = "_destroy";
  }
  st.setTask(taskId, ts);

  for (const agentName of agents) {
    const model = getAgentModel(wf, agentName);
    const prompt = `# Task: ${taskId} — Destroy Phase\n\n${destroyPrompt}\n\n## Context\nWorkflow: ${wf.name}\nYour role: ${agentName}\nArtifacts dir: ${path.join(ROOT, "artifacts", taskId)}`;

    createJob({
      task: taskId,
      workflow: wf.name,
      step: `_destroy:${agentName}`,
      agent: agentName,
      model,
      type: "work",
      priority: "high",
      timeout: wf.destroy.timeout ?? 300,
      prompt,
    });
    log.info(`Created destroy job for agent ${agentName} on task ${taskId}`);
  }
}

function handleDestroyComplete(cfg: Config, st: State, completedJob: Job, wf: Workflow): void {
  const taskId = completedJob.task;

  // Check if other destroy jobs are still pending/active for this task
  for (const folder of ["pending", "active"]) {
    for (const j of listJobs(folder)) {
      if (j.task === taskId && j.step.startsWith("_destroy:") && j.id !== completedJob.id) {
        log.info(`Destroy: still waiting for ${j.step} on task ${taskId}`);
        return;
      }
    }
  }

  log.info(`All agents completed destroy for task ${taskId} — running cleanup`);
  runDestroyActions(cfg, st, taskId, wf);
}

function runDestroyActions(cfg: Config, st: State, taskId: string, wf: Workflow): void {
  for (const action of wf.destroy.actions) {
    if (action === "cleanup_jobs") {
      for (const folder of ["pending", "active"]) {
        for (const j of listJobs(folder)) {
          if (j.task === taskId) moveJob(j.id, folder, "done");
        }
      }
      log.info(`Destroy: cleaned up job files for task ${taskId}`);
    } else {
      log.info(`Destroy action ${action} for task ${taskId}`);
    }
  }

  const ts = st.getTask(taskId);
  if (ts) { ts.status = "complete"; ts.currentStep = "done"; st.setTask(taskId, ts); }
  log.info(`Task ${taskId} fully complete`);
}

function getAgentModel(wf: Workflow, agentName: string): string {
  let model = "";
  for (const step of Object.values(wf.steps)) {
    if ((step.agent === agentName || step.role === agentName) && step.model) model = step.model;
  }
  return model;
}

// ─── Event handlers ───────────────────────────────────────────────────────────

function handleDone(cfg: Config, st: State, msg: PipeMessage): void {
  const meta = getJobMeta(msg.jobId!, "active");
  if (!meta) { log.warn(`Job ${msg.jobId} not found in active/`); return; }

  const result = msg.message || readResult(msg.jobId!, "active") || "";
  if (result) writeResult(msg.jobId!, "active", result);

  log.info(`Job done: ${msg.jobId} (step=${meta.step})`);
  if (meta.model) st.unlockModel(meta.model);
  moveJob(msg.jobId!, "active", "done");
  advanceWorkflow(cfg, st, meta, result);
}

function handleFail(cfg: Config, st: State, msg: PipeMessage): void {
  const meta = getJobMeta(msg.jobId!, "active");
  if (!meta) { log.warn(`Job ${msg.jobId} not found in active/`); return; }

  log.warn(`Job failed: ${msg.jobId} — ${msg.reason}`);
  if (msg.reason) writeResult(msg.jobId!, "active", `FAILED: ${msg.reason}`);
  if (meta.model) st.unlockModel(meta.model);
  moveJob(msg.jobId!, "active", "failed");
  notifyFailure(cfg, msg.jobId!, meta.task, msg.reason ?? "unknown");
}

function handleAsk(cfg: Config, st: State, msg: PipeMessage): void {
  log.info(`Question from ${msg.jobId}: ${msg.question}`);

  if (msg.escalate) {
    const taskId = msg.taskId ?? getJobMeta(msg.jobId!, "active")?.task ?? "";
    notifyReady(cfg, msg.jobId!, taskId);
    return;
  }

  const meta = getJobMeta(msg.jobId!, "active");
  const taskId = msg.taskId ?? meta?.task ?? "";
  createJob({
    task: taskId,
    workflow: "answer",
    step: "answer",
    model: "local-9b",
    type: "answer",
    priority: "high",
    timeout: 60,
    prompt: `An agent has a question:\n\n${msg.question}\n\nProvide a clear, actionable answer.`,
  });
}

function handleAnswer(cfg: Config, st: State, msg: PipeMessage): void {
  log.info(`Answer received for ${msg.jobId}: ${msg.message}`);
  const meta = getJobMeta(msg.jobId!, "active");
  if (!meta) { log.warn(`Job ${msg.jobId} not found in active/`); return; }

  if (meta.type === "human") {
    log.info(`Human job ${msg.jobId} answered — advancing workflow`);
    if (meta.model) st.unlockModel(meta.model);
    const answer = msg.message ?? "";
    writeResult(msg.jobId!, "active", answer);
    // Write answer to artifacts dir so next agent steps can read it
    // File: {step}_answer.md — step-scoped so multiple human steps don't collide
    if (answer) {
      const artifactDir = path.join(ROOT, "artifacts", meta.task);
      fs.mkdirSync(artifactDir, { recursive: true });
      fs.writeFileSync(
        path.join(artifactDir, `${meta.step}_answer.md`),
        answer + "\n",
        "utf8",
      );
      log.info(`Wrote answer to artifacts/${meta.task}/${meta.step}_answer.md`);
    }
    moveJob(msg.jobId!, "active", "done");
    advanceWorkflow(cfg, st, meta, answer);
    return;
  }

  // Re-dispatch work job with answer appended
  meta.prompt = (meta.prompt ?? "") + `\n\n---\n\n**Human answered your question:**\n\n${msg.message}\n\nContinue with your work.`;
  writeJobMeta(msg.jobId!, "active", meta);
  dispatchJobFromMeta(cfg, meta);
}

function readResult(id: string, folder: string): string {
  const p = path.join(ROOT, "jobs", folder, `${id}.result.md`);
  return fs.existsSync(p) ? fs.readFileSync(p, "utf8") : "";
}

// ─── Dispatcher ───────────────────────────────────────────────────────────────

function loadJobProject(job: Job): Project | undefined {
  if (!job.project) return undefined;
  try { return loadProject(job.project); }
  catch (e) { log.warn(`Could not load project "${job.project}": ${e}`); return undefined; }
}

function dispatchJobFromMeta(cfg: Config, job: Job): void {
  const role = job.agent;
  const systemPrompt = loadSystemPrompt(role);
  const project = loadJobProject(job);

  dispatchJob(cfg, job, systemPrompt, {
    onDone: async (jobId, summary) => {
      const meta = getJobMeta(jobId, "active");
      if (!meta) return;
      writeResult(jobId, "active", summary);
      if (meta.model) st.unlockModel(meta.model);
      moveJob(jobId, "active", "done");
      // Run project hooks before advancing (hooks fire before next step dispatches)
      if (project?.hooks) {
        const dispatchRoot = process.env.DISPATCH_ROOT ?? path.join(os.homedir(), ".dispatch");
        const hookCtx: HookContext = {
          project,
          taskId: meta.task,
          step: meta.step,
          artifactDir: path.join(dispatchRoot, "artifacts", meta.task),
          workspace: project.workspace ?? path.join(dispatchRoot, "artifacts", meta.task),
        };
        runHooks(`after_${meta.step}`, hookCtx);
      }
      advanceWorkflow(cfg, st, meta, summary, project);
      st.save();
    },
    onAsk: async (jobId, question, escalate) => {
      handleAsk(cfg, st, { type: "ask", jobId, question, escalate });
    },
    onFail: async (jobId, reason) => {
      const meta = getJobMeta(jobId, "active");
      if (!meta) return;

      // ─── Agentic triage ──────────────────────────────────────────────────
      // Before escalating to human, ask the 27B model to diagnose and
      // recommend a recovery action. Runs once per job, only if model is free.
      const triageModelKey = cfg.triage?.model ?? "local-27b";
      const canTriage = cfg.triage?.enabled !== false &&
                        !st.hasBeenTriaged(jobId) &&
                        st.isModelFree(triageModelKey);

      if (canTriage) {
        st.markTriaged(jobId);
        st.save();
        log.info(`Running triage for job ${jobId} (reason: ${reason})`);

        const dispatchRoot = process.env.DISPATCH_ROOT ?? path.join(os.homedir(), ".dispatch");
        const triageAction = await runTriage(cfg, meta, reason, dispatchRoot);

        switch (triageAction.action) {
          case "retry": {
            log.info(`Triage recommends retry for ${jobId}: ${triageAction.reason}`);
            notifyTriageAction(cfg, jobId, meta.task, "retry", triageAction.reason);
            const retryJob: Job = {
              ...meta,
              prompt: (meta.prompt ?? "") + `\n\n---\n\n**🔍 Triage note:** ${triageAction.modifiedPrompt}`,
            };
            if (meta.model) st.lockModel(meta.model, jobId);
            // Job stays in active/ — re-dispatch it
            dispatchJobFromMeta(cfg, retryJob);
            st.save();
            return;
          }
          case "done": {
            log.info(`Triage says job ${jobId} is done: ${triageAction.reason}`);
            notifyTriageAction(cfg, jobId, meta.task, "done", triageAction.reason);
            writeResult(jobId, "active", triageAction.summary);
            if (meta.model) st.unlockModel(meta.model);
            moveJob(jobId, "active", "done");
            advanceWorkflow(cfg, st, meta, triageAction.summary, project);
            st.save();
            return;
          }
          case "skip": {
            log.info(`Triage says skip step ${meta.step} for task ${meta.task}: ${triageAction.reason}`);
            notifyTriageAction(cfg, jobId, meta.task, "skip", triageAction.reason);
            writeResult(jobId, "active", `SKIPPED: ${triageAction.reason}`);
            if (meta.model) st.unlockModel(meta.model);
            moveJob(jobId, "active", "done");
            advanceWorkflow(cfg, st, meta, `SKIPPED: ${triageAction.reason}`, project);
            st.save();
            return;
          }
          case "escalate":
          default:
            log.info(`Triage recommends escalation for ${jobId}: ${triageAction.reason}`);
            // fall through to normal failure handling
        }
      }

      // Normal failure path
      writeResult(jobId, "active", `FAILED: ${reason}`);
      if (meta.model) st.unlockModel(meta.model);
      moveJob(jobId, "active", "failed");
      notifyFailure(cfg, jobId, meta.task, reason);
      st.save();
    },
  }, project);  // ← pass project so runner gets context block + workspace CWD
}

// Module-level state ref (set in startForeman)
let st: State;

function dispatchPending(cfg: Config): void {
  const pending = listJobs("pending");
  for (const job of pending) {
    if (job.type === "human") {
      log.info(`Human job: ${job.id} — waiting for action`);
      moveJob(job.id, "pending", "active");
      notifyReady(cfg, job.id, job.task);
      continue;
    }

    if (job.type === "work") {
      if (job.model && !st.isModelFree(job.model)) continue;
      log.info(`Dispatching work: ${job.id} (model=${job.model}, role=${job.agent})`);
      if (job.model) st.lockModel(job.model, job.id);
      moveJob(job.id, "pending", "active");
      dispatchJobFromMeta(cfg, job);
    }
    // "triage" / "answer" / "parse" handled by LLM direct call (future work — skip for now)
  }
}

function healthCheck(cfg: Config): void {
  const active = listJobs("active");
  const now = Date.now();

  for (const job of active) {
    if (job.type === "human" || !job.timeout) continue;
    const created = new Date(job.created).getTime();
    const deadline = created + job.timeout * 1000;

    if (now > deadline) {
      log.warn(`Job ${job.id} timed out (timeout=${job.timeout}s)`);
      abortSession(job.id).catch(() => {});
      if (job.model) st.unlockModel(job.model);
      writeResult(job.id, "active", `TIMEOUT: exceeded ${job.timeout}s deadline`);
      moveJob(job.id, "active", "failed");
    }
  }
}

// ─── Foreman entry point ──────────────────────────────────────────────────────

export async function startForeman(): Promise<void> {
  const cfg = loadConfig();
  st = State.load();
  st.save();

  log.info(`Foreman starting (root=${ROOT}, poll=${cfg.pollIntervalMs}ms)`);

  createPipe(cfg.pipePath);
  log.info(`Listening on pipe: ${cfg.pipePath}`);

  // Initial dispatch
  dispatchPending(cfg);
  st.save();

  // Pipe listener (feeds events)
  listenPipe(cfg.pipePath, (msg) => {
    log.info(`CLI event: ${msg.type} (job=${msg.jobId})`);
    switch (msg.type) {
      case "done":    handleDone(cfg, st, msg); break;
      case "fail":    handleFail(cfg, st, msg); break;
      case "ask":     handleAsk(cfg, st, msg); break;
      case "answer":  handleAnswer(cfg, st, msg); break;
      case "new_task": log.info(`New task: ${msg.taskId}`); break;
      default:         log.warn(`Unknown event: ${msg.type}`);
    }
    dispatchPending(cfg);
    st.save();
  }).catch((e) => log.error(`Pipe listener error: ${e}`));

  // Poll loop
  const pollTimer = setInterval(() => {
    healthCheck(cfg);
    dispatchPending(cfg);
    st.save();
  }, cfg.pollIntervalMs);

  // Graceful shutdown
  for (const sig of ["SIGINT", "SIGTERM"]) {
    process.on(sig, () => {
      log.info(`Shutting down (signal: ${sig})`);
      clearInterval(pollTimer);
      st.save();
      // Unblock any pending open(FIFO, O_RDONLY) in the libuv threadpool.
      // fs.createReadStream on a named FIFO causes Node's threadpool to call
      // open(O_RDONLY) which blocks until a writer appears. process.exit() can't
      // complete while a threadpool thread is blocked in a syscall. Opening the
      // write end briefly (O_WRONLY|O_NONBLOCK) unblocks it immediately.
      try {
        const wfd = fs.openSync(cfg.pipePath, fs.constants.O_WRONLY | fs.constants.O_NONBLOCK);
        fs.closeSync(wfd);
      } catch { /* no listener open yet — fine */ }
      try { fs.unlinkSync(cfg.pipePath); } catch {}
      process.exit(0);
    });
  }

  log.info("Foreman running");
  // Keep process alive
  await new Promise<void>(() => {});
}
