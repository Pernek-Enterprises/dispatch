#!/usr/bin/env node

/**
 * Dispatch Foreman — deterministic orchestration loop
 * 
 * Event-driven: reacts to CLI notifications via named pipe.
 * Polls only for: new task intake + health checks (every 30-60s).
 * 
 * No LLM calls inline — LLM needs are queued as jobs.
 */

import { load, save, lockModel, unlockModel, lockAgent, unlockAgent, isModelFree, isAgentFree, loadConfig, ROOT } from './lib/state.js';
import { createJob, listJobs, moveJob, readResult, writeResult, getJobMeta } from './lib/jobs.js';
import { parseWorkflow, getNextStep } from './lib/workflows.js';
import { callModel } from './lib/dispatch-llm.js';
import { createPipe, listenPipe } from './lib/pipe.js';
import { log } from './lib/log.js';
import { existsSync, mkdirSync } from 'fs';
import { join } from 'path';

// --- Config (all from config.json — nothing hardcoded) ---
let config;
try {
  config = loadConfig('config');
} catch (err) {
  console.error(`\n  ${err.message}\n`);
  console.error('  To get started:');
  console.error('    cp config.json.example config.json');
  console.error('    cp models.json.example models.json');
  console.error('    cp agents.json.example agents.json');
  console.error('  Then edit each file for your installation.\n');
  process.exit(1);
}
const POLL_INTERVAL = config.pollIntervalMs || 30000;
const MAX_LOOP_DEFAULT = config.maxLoopIterations || 3;

// --- Ensure directories ---
for (const dir of ['jobs/pending', 'jobs/active', 'jobs/done', 'jobs/failed', 'artifacts', 'logs']) {
  mkdirSync(join(ROOT, dir), { recursive: true });
}

// --- State ---
let state = load();

function saveState() {
  save(state);
}

// =============================================================================
// Event Handlers (from CLI via named pipe)
// =============================================================================

async function handleCliEvent(msg) {
  log.info(`CLI event: ${msg.type}`, { jobId: msg.jobId });

  switch (msg.type) {
    case 'done':
      await handleJobDone(msg.jobId, msg.taskId);
      break;
    case 'fail':
      await handleJobFail(msg.jobId, msg.taskId, msg.reason);
      break;
    case 'ask':
      await handleJobAsk(msg.jobId, msg.taskId, msg.question, msg.escalate);
      break;
    default:
      log.warn(`Unknown CLI event type: ${msg.type}`);
  }

  // After handling, try to dispatch pending jobs
  await dispatchPending();
  saveState();
}

async function handleJobDone(jobId, taskId) {
  const meta = getJobMeta(jobId, 'active');
  if (!meta) {
    log.warn(`Job ${jobId} not found in active/`);
    return;
  }

  const result = readResult(jobId);
  log.info(`Job done: ${jobId} (step: ${meta.step})`, { result: result?.substring(0, 100) });

  // Release locks
  if (meta.model) unlockModel(state, meta.model);
  if (meta.agent && meta.agent !== 'stefan') unlockAgent(state, meta.agent);

  // Move to done
  moveJob(jobId, 'active', 'done');

  // Advance workflow
  await advanceWorkflow(meta, result);
}

async function handleJobFail(jobId, taskId, reason) {
  const meta = getJobMeta(jobId, 'active');
  if (!meta) {
    log.warn(`Job ${jobId} not found in active/`);
    return;
  }

  log.warn(`Job failed: ${jobId} — ${reason}`);

  // Release locks
  if (meta.model) unlockModel(state, meta.model);
  if (meta.agent && meta.agent !== 'stefan') unlockAgent(state, meta.agent);

  // Move to failed
  moveJob(jobId, 'active', 'failed');

  // TODO: create escalation notification to Stefan
  log.info(`Task ${taskId} needs attention — step ${meta.step} failed`);
}

async function handleJobAsk(jobId, taskId, question, escalate) {
  log.info(`Question from ${jobId}: ${question}`);

  if (escalate) {
    // TODO: send notification to Stefan via Telegram
    log.info(`Escalated question to Stefan: ${question}`);
    return;
  }

  // Create an answer job (will be dispatched when model is free)
  const meta = getJobMeta(jobId, 'active');
  createJob({
    task: taskId,
    workflow: meta?.workflow || 'unknown',
    step: `answer-for-${meta?.step || 'unknown'}`,
    agent: null, // no agent needed — direct LLM call
    model: '9b',
    type: 'answer',
    priority: 'high', // answers should be fast
    timeout: 60,
    prompt: `An agent working on task "${taskId}" has a question:\n\n${question}\n\nProvide a clear, actionable answer.`
  });
}

// =============================================================================
// Workflow Advancement
// =============================================================================

async function advanceWorkflow(completedJob, result) {
  const { task, workflow: workflowName, step } = completedJob;
  const workflow = parseWorkflow(workflowName);
  if (!workflow) {
    log.warn(`Workflow ${workflowName} not found`);
    return;
  }

  const nextStepName = getNextStep(workflow, step, result);

  if (!nextStepName) {
    // Last step — task complete
    log.info(`Task ${task} complete (workflow: ${workflowName})`);
    
    // Update task status
    if (state.tasks[task]) {
      state.tasks[task].status = 'complete';
    }

    // TODO: trigger memory write + session cleanup
    // TODO: notification
    return;
  }

  const nextStep = workflow.steps[nextStepName];
  if (!nextStep) {
    log.error(`Step ${nextStepName} not found in workflow ${workflowName}`);
    return;
  }

  // Check loop iteration
  const taskState = state.tasks[task] || {};
  const iterations = taskState.iteration || {};
  const stepIterations = (iterations[nextStepName] || 0) + 1;
  const maxIterations = nextStep.max_iterations || MAX_LOOP_DEFAULT;

  if (stepIterations > maxIterations) {
    log.warn(`Max iterations (${maxIterations}) reached for ${nextStepName} on task ${task} — escalating`);
    // TODO: escalate to Stefan
    return;
  }

  // Update task state
  state.tasks[task] = {
    ...taskState,
    workflow: workflowName,
    currentStep: nextStepName,
    status: 'active',
    iteration: { ...iterations, [nextStepName]: stepIterations }
  };

  // Build prompt with artifacts from previous steps
  const artifactDir = join(ROOT, 'artifacts', task);
  let artifactNote = '';
  if (existsSync(artifactDir)) {
    const artifacts = nextStep.artifacts_in || [];
    if (artifacts.length > 0) {
      artifactNote = `\n\n## Artifacts from previous steps\nAvailable in: ${artifactDir}\nFiles: ${artifacts.join(', ')}`;
    }
  }

  const prompt = `# Task: ${task}\n\n## Step: ${nextStepName}\n\n${nextStep.prompt}${artifactNote}\n\n## How to communicate\n- When done: \`dispatch done "summary"\`\n- To attach files: \`dispatch done --artifact file.md "summary"\`\n- If you need help: \`dispatch ask "question"\`\n- If blocked: \`dispatch fail "reason"\``;

  // Handle human steps
  if (nextStep.agent === 'stefan') {
    log.info(`Task ${task} waiting for human (step: ${nextStepName})`);
    // TODO: send notification to Stefan
    createJob({
      task,
      workflow: workflowName,
      step: nextStepName,
      agent: 'stefan',
      model: null,
      type: 'human',
      priority: 'normal',
      timeout: 0, // no timeout for human steps
      prompt
    });
    return;
  }

  // Create next job
  createJob({
    task,
    workflow: workflowName,
    step: nextStepName,
    agent: nextStep.agent,
    model: nextStep.model,
    type: 'work',
    priority: 'normal',
    timeout: nextStep.timeout,
    iteration: stepIterations,
    prompt
  });

  log.info(`Created job for step ${nextStepName} (agent: ${nextStep.agent}, model: ${nextStep.model})`);
}

// =============================================================================
// Dispatch Logic
// =============================================================================

async function dispatchPending() {
  const pending = listJobs('pending');
  if (pending.length === 0) return;

  for (const job of pending) {
    // Quick LLM jobs (triage, parse, answer) — direct API call
    if (['triage', 'parse', 'answer'].includes(job.type)) {
      if (job.model && !isModelFree(state, job.model)) continue;

      log.info(`Dispatching LLM job: ${job.id} (type: ${job.type}, model: ${job.model})`);

      if (job.model) lockModel(state, job.model, job.id);
      moveJob(job.id, 'pending', 'active');
      saveState();

      try {
        const result = await callModel(job.model, job.prompt);
        writeResult(job.id, 'active', result);
        
        if (job.model) unlockModel(state, job.model);
        moveJob(job.id, 'active', 'done');

        // If this was a triage job, advance workflow
        if (job.type === 'triage') {
          await handleTriageResult(job, result);
        }
      } catch (err) {
        log.error(`LLM job ${job.id} failed: ${err.message}`);
        if (job.model) unlockModel(state, job.model);
        moveJob(job.id, 'active', 'failed');
      }

      saveState();
      continue;
    }

    // Human jobs — just notify, don't lock anything
    if (job.type === 'human') {
      log.info(`Human job: ${job.id} — waiting for Stefan`);
      moveJob(job.id, 'pending', 'active');
      // TODO: send notification to Stefan
      saveState();
      continue;
    }

    // Agent work jobs — need model + agent free
    if (job.type === 'work') {
      if (job.model && !isModelFree(state, job.model)) continue;
      if (job.agent && !isAgentFree(state, job.agent)) continue;

      log.info(`Dispatching work job: ${job.id} (agent: ${job.agent}, model: ${job.model})`);

      if (job.model) lockModel(state, job.model, job.id);
      if (job.agent) lockAgent(state, job.agent, job.id);
      moveJob(job.id, 'pending', 'active');
      saveState();

      // TODO: spawn or send to OpenClaw session
      // For now, log that it's ready for dispatch
      log.info(`Job ${job.id} is active — agent ${job.agent} should pick it up`);

      // Don't dispatch more work jobs in the same cycle
      // (one at a time to respect model locks)
      break;
    }
  }
}

async function handleTriageResult(triageJob, result) {
  // Parse triage result — expect workflow name
  const workflows = ['coding-easy', 'coding-complex', 'code-review', 'research', 'general'];
  const picked = workflows.find(w => result.toLowerCase().includes(w));

  if (!picked) {
    log.warn(`Triage couldn't determine workflow from result. Using 'general'.`);
  }

  const workflowName = picked || 'general';
  const workflow = parseWorkflow(workflowName);
  if (!workflow) {
    log.error(`Workflow ${workflowName} not found`);
    return;
  }

  // Get first step
  const firstStepName = Object.keys(workflow.steps)[0];
  const firstStep = workflow.steps[firstStepName];
  if (!firstStep) return;

  const task = triageJob.task;

  // Initialize task state
  state.tasks[task] = {
    workflow: workflowName,
    currentStep: firstStepName,
    status: 'active',
    iteration: {},
    created: new Date().toISOString()
  };

  // Create first job
  const prompt = `# Task: ${task}\n\n## Step: ${firstStepName}\n\n${firstStep.prompt}\n\n## How to communicate\n- When done: \`dispatch done "summary"\`\n- To attach files: \`dispatch done --artifact file.md "summary"\`\n- If you need help: \`dispatch ask "question"\`\n- If blocked: \`dispatch fail "reason"\``;

  createJob({
    task,
    workflow: workflowName,
    step: firstStepName,
    agent: firstStep.agent,
    model: firstStep.model,
    type: firstStep.agent === 'stefan' ? 'human' : 'work',
    priority: 'normal',
    timeout: firstStep.timeout,
    prompt
  });

  log.info(`Triage: task ${task} → workflow ${workflowName}, first step: ${firstStepName}`);
}

// =============================================================================
// Health Check (polling)
// =============================================================================

function healthCheck() {
  const active = listJobs('active');
  const now = Date.now();

  for (const job of active) {
    if (job.type === 'human') continue; // No timeout for human steps
    if (!job.timeout || job.timeout === 0) continue;

    const created = new Date(job.created).getTime();
    const deadline = created + (job.timeout * 1000);

    if (now > deadline) {
      log.warn(`Job ${job.id} timed out (timeout: ${job.timeout}s)`);

      // Release locks
      if (job.model) unlockModel(state, job.model);
      if (job.agent && job.agent !== 'stefan') unlockAgent(state, job.agent);

      // Move to failed
      moveJob(job.id, 'active', 'failed');
      writeResult(job.id, 'failed', `TIMEOUT: Job exceeded ${job.timeout}s deadline`);

      // TODO: notify Stefan
      log.info(`Task ${job.task} step ${job.step} timed out — needs attention`);
    }
  }

  saveState();
}

// =============================================================================
// Main
// =============================================================================

async function main() {
  log.info('Foreman starting', { root: ROOT, pollInterval: POLL_INTERVAL });

  // Initialize model locks from config
  const models = loadConfig('models');
  for (const modelId of Object.keys(models)) {
    if (!state.models[modelId]) {
      state.models[modelId] = { busy: false, job: null, since: null };
    }
  }

  // Initialize agent locks from config
  const agents = loadConfig('agents');
  for (const agentId of Object.keys(agents)) {
    if (!state.agents[agentId]) {
      state.agents[agentId] = { busy: false, job: null, since: null };
    }
  }

  saveState();

  // Create named pipe for CLI communication
  createPipe();
  log.info('Named pipe created — listening for CLI events');

  // Listen for CLI events (event-driven)
  listenPipe(async (msg) => {
    try {
      await handleCliEvent(msg);
    } catch (err) {
      log.error(`Error handling CLI event: ${err.message}`);
    }
  });

  // Initial dispatch of any pending jobs
  await dispatchPending();
  saveState();

  // Poll for health checks + new work (every POLL_INTERVAL)
  setInterval(async () => {
    try {
      healthCheck();
      await dispatchPending();
      saveState();
    } catch (err) {
      log.error(`Poll cycle error: ${err.message}`);
    }
  }, POLL_INTERVAL);

  log.info('Foreman running');
}

// Handle shutdown gracefully
process.on('SIGINT', () => {
  log.info('Foreman shutting down');
  saveState();
  process.exit(0);
});

process.on('SIGTERM', () => {
  log.info('Foreman shutting down');
  saveState();
  process.exit(0);
});

main().catch(err => {
  log.error(`Fatal: ${err.message}`);
  process.exit(1);
});
