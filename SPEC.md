# Dispatch — Local Agent Orchestration System

**Version:** 0.1 (Draft)
**Date:** 2026-03-05
**Authors:** Stefan Pernek, Cleo

---

## 1. Problem Statement

The current agent orchestration (AgenticTodo queue → poller → skill → workflow → agent → API) has too many integration points. A format change in any layer silently breaks the pipeline. The system was designed for single-agent + cloud models but now runs on multi-model local hardware (R9700 32GB + RTX 3070 8GB).

**Current pain points:**
- 6+ files to update for a workflow change (API, poller, skill, workflow, agents.md, workers.md)
- Rigid JSON contracts between poller ↔ agent — wrong format = silent failure
- No model-awareness — can't route work to specific GPUs
- No concurrency control — no way to prevent two agents from claiming the same model
- Agents call APIs directly — fragile, breaks when schemas change

## 2. Solution Overview

**Dispatch** is a file-based, local-first agent orchestration system. It replaces the queue poller, skill workflows, and agent coordination with:

- A **deterministic foreman loop** (pure code, no LLM) that manages state via files
- **LLM calls as queued jobs** — triage, parsing, answering questions all go through the same queue as agent work
- **Workflow templates** in plain markdown — one place to edit, one place to update
- **Model-aware scheduling** — knows which models are on which GPUs, prevents contention

### Architecture

```
┌──────────────┐     ┌──────────────────────────────────┐
│  AgenticTodo │     │         ~/dispatch/               │
│  (task mgmt) │────▶│                                    │
│  browser UI  │     │  foreman.js  (deterministic loop)  │
└──────────────┘     │    ↕ reads/writes files            │
                     │                                    │
                     │  jobs/                              │
                     │    pending/   ← new work            │
                     │    active/    ← in progress         │
                     │    done/      ← completed           │
                     │    failed/    ← timed out / errored │
                     │                                    │
                     │  workflows/  ← markdown templates   │
                     │  state.json  ← locks + agent status │
                     │  models.json ← endpoint config      │
                     └──────────┬───────────────────────┘
                                │ spawns agents via
                                │ llama-server API
                                ▼
                     ┌──────────────────────┐
                     │   Local LLM Models   │
                     │  9B  @ :8081 (Vulkan)│
                     │  27B @ :8080 (Vulkan)│
                     └──────────────────────┘
```

## 3. Core Concepts

### 3.1 Job

A job is a markdown file in `jobs/`. It moves through folders as it progresses:

```
pending/ → active/ → done/
                   → failed/
```

**Job file format** (example: `jobs/pending/001-triage-auth-fix.md`):

```markdown
# Job: 001-triage-auth-fix

type: triage
model: 9b
priority: normal
created: 2026-03-05T14:00:00Z
deadline: 2026-03-05T14:10:00Z
source_task: at_task_abc123

## Input

New task from AgenticTodo:
Title: Fix auth redirect loop on mobile
Description: Users on iOS Safari get stuck in a redirect loop after OAuth...
Category: coding
Status: execute

## Workflow

Pick the appropriate workflow from: code-fix, code-review, code-new, research, general
Determine model tier for each step.
Output your decision as a simple plan.

## Result

(filled by the agent/LLM when complete)
```

### 3.2 Job Types

| Type | Purpose | Model | Duration |
|------|---------|-------|----------|
| `triage` | Classify task, pick workflow, plan steps | 9B | ~5s |
| `work` | Actual agent work (code, review, research) | 9B or 27B | 1-30 min |
| `parse` | Interpret agent result, decide next step | 9B | ~5s |
| `answer` | Answer an agent's question | 9B | ~5s |
| `escalate` | Notify Stefan, pause until human input | none | indefinite |

### 3.3 Workflows

Markdown files in `workflows/` that describe what an agent should do. Plain english, no format contracts.

Example `workflows/code-fix.md`:
```markdown
# Code Fix Workflow

## Steps

1. **Understand** (model: 9b, timeout: 5m)
   Read the task description and any linked PR/issue.
   Identify the root cause. Write your analysis.

2. **Fix** (model: 27b, timeout: 20m)
   Implement the fix. Create a branch, make changes, push.
   Write a summary of what you changed and why.

3. **Self-Review** (model: 9b, timeout: 5m)
   Review your own changes. Check for obvious issues.
   If problems found, go back to step 2.

4. **Report** (model: 9b, timeout: 2m)
   Write a completion summary for the task thread.
```

Each step becomes a job. The foreman creates them sequentially as prior steps complete.

### 3.4 State

**`state.json`** — the lock table:

```json
{
  "models": {
    "9b": { "busy": false, "job": null, "since": null },
    "27b": { "busy": true, "job": "003-fix-auth", "since": "2026-03-05T14:05:00Z" }
  },
  "agents": {
    "kit": { "busy": true, "job": "003-fix-auth", "since": "2026-03-05T14:05:00Z" },
    "hawk": { "busy": false, "job": null, "since": null }
  }
}
```

**`models.json`** — endpoint configuration:

```json
{
  "9b": {
    "name": "Qwen3.5-9B",
    "endpoint": "http://localhost:8081/v1",
    "vram_gb": 5.3,
    "gpu": "R9700"
  },
  "27b": {
    "name": "Qwen3.5-27B",
    "endpoint": "http://localhost:8080/v1",
    "gpu": "R9700",
    "vram_gb": 15.6
  }
}
```

### 3.5 Model Contention

The R9700 can only run one model at a time via llama-server. Two approaches:

**Option A: Multiple llama-server instances (if VRAM allows)**
- 9B (5.3 GB) + 27B (15.6 GB) = 20.9 GB — fits in 32 GB
- Two servers on different ports, both Vulkan
- No contention for different model tiers

**Option B: Single server, model swapping**
- One llama-server, swap models when needed
- Slower (model load time) but simpler
- Better if context windows eat too much VRAM

**Recommendation:** Start with Option A. Both models fit comfortably. Fall back to B if memory pressure becomes an issue with large contexts.

## 4. Foreman Loop

The foreman is a **deterministic Node.js script** (~100-200 lines). No LLM calls. Runs every 30 seconds via systemd timer or internal setInterval.

### Each cycle:

```
1. Read state.json (locks)
2. Check active/ jobs for timeouts
   - Past deadline? → move to failed/, release locks
   - Agent heartbeat stale? → move to failed/, release locks
3. Check done/ for completed jobs
   - Read result from job file
   - If job was part of a workflow:
     → Create next step as new job in pending/
     → Or create a "parse" job to interpret the result
   - Release locks (model + agent)
4. Scan pending/ for eligible jobs
   - Sort by priority, then creation time
   - For each job:
     → Does it need a model? Is that model free?
     → Does it need an agent? Is that agent free?
     → If both free: claim locks, move to active/, dispatch
5. Write updated state.json
```

### Dispatching a job:

For LLM jobs (triage, parse, answer): HTTP POST to the model endpoint directly. Read response, write to job file, move to done/.

For agent work jobs: Spawn via OpenClaw session or direct llama-server call with the workflow instructions as system prompt + job input as user message.

### Heartbeat mechanism:

Active agent jobs must have their file's mtime updated periodically (agent writes progress). Foreman checks: if `mtime` is older than `timeout / 2`, agent is probably stuck.

## 5. Agent ↔ Dispatch Communication

**All communication is via the job file.** No APIs, no JSON contracts.

### Dispatch → Agent:
The job file IS the instruction. Agent reads it, does the work.

### Agent → Dispatch:
Agent writes its result into the `## Result` section of the job file, then moves it to `done/` (or signals completion by writing a `.done` marker file).

### Agent asks a question:
Agent writes to a `## Questions` section in its job file:
```markdown
## Questions

Q1: The PR has conflicts with main. Should I rebase or merge?
Status: pending
```

Foreman sees the question on next cycle, creates an `answer` job. When answered:
```markdown
Q1: The PR has conflicts with main. Should I rebase or merge?
Status: answered
Answer: Rebase onto main, resolve conflicts, force push.
```

Agent checks its own file periodically for answers.

### Escalation:
If the `answer` job's LLM response includes uncertainty markers or the question is flagged high-stakes, foreman creates an `escalate` job instead → sends notification to Stefan (Discord/Telegram). Job stays active but paused until human responds.

## 6. Integration Points

### AgenticTodo (later, Phase 4):
- Foreman reads new tasks from AgenticTodo API
- Foreman writes results back via API
- AgenticTodo UI shows dispatch state (read-only from `state.json`)
- This is optional — dispatch works standalone with manual job files

### OpenClaw (optional):
- Can spawn agents via OpenClaw sessions
- Can deliver notifications via OpenClaw channels
- Not required — dispatch can call llama-server directly

### Notifications:
- Escalations → Stefan via Discord/Telegram
- Completions → optional summary to channel
- Failures → alert Stefan

## 7. File Structure

```
~/dispatch/
├── foreman.js              ← the loop (deterministic, no LLM)
├── state.json              ← lock table
├── models.json             ← endpoint config
├── config.json             ← foreman settings (poll interval, timeouts)
├── workflows/
│   ├── code-fix.md
│   ├── code-review.md
│   ├── code-new.md
│   ├── research.md
│   └── general.md
├── jobs/
│   ├── pending/            ← ready to be picked up
│   ├── active/             ← currently being worked on
│   ├── done/               ← completed successfully
│   └── failed/             ← timed out or errored
└── logs/
    └── 2026-03-05.log      ← foreman activity log
```

## 8. Implementation Plan

### Phase 0: Clean Slate
- [ ] Stop queue poller on Mac Mini (`launchctl unload`)
- [ ] Stop queue poller on Clawdia (if running)
- [ ] Don't delete — disable for rollback

### Phase 1: Infrastructure + Foreman
- [ ] Set up dual llama-server (9B on :8081, 27B on :8080) as systemd services
- [ ] Create `~/dispatch/` file structure
- [ ] Write `models.json` and `config.json`
- [ ] Build foreman.js — the core loop
  - File scanning, lock management, timeout detection
  - Job dispatching (HTTP to llama-server)
  - State persistence
- [ ] Set up as systemd service (every 30s or internal loop)
- [ ] Test with manually created job files

### Phase 2: Triage + Workflows
- [ ] Write workflow templates (start with 3-4)
- [ ] Implement triage as a job type — foreman creates triage job for new tasks
- [ ] Test: manual task → triage job → work job → completion

### Phase 3: Agent Integration
- [ ] Wire up agent spawning (OpenClaw sessions or direct LLM calls)
- [ ] Implement question/answer flow
- [ ] Implement escalation to Stefan
- [ ] Test end-to-end: task → triage → work → parse → done

### Phase 4: Polish + Integration
- [ ] AgenticTodo reads dispatch state for browser UI
- [ ] Notification delivery (Discord/Telegram)
- [ ] Open source packaging
- [ ] Second GPU model serving (RTX 3070 for embeddings/reranking)

## 9. Success Criteria

- **One file to change** for workflow updates (the workflow markdown)
- **Zero format contracts** between dispatch and agents
- **No silent failures** — every stuck job gets detected and handled
- **Full state visibility** — `ls jobs/` tells you everything
- **Model-aware** — no two jobs fight for the same GPU
- **< 5 second overhead** per dispatch cycle

## 10. Open Questions

1. **Single vs dual llama-server:** Can we run 9B + 27B simultaneously on the R9700? Need to test VRAM with context windows.
2. **Agent spawning mechanism:** OpenClaw sessions vs direct llama-server API calls vs something else?
3. **AgenticTodo integration timing:** Do we keep the API integration or go fully file-based initially?
4. **Notification channel:** Discord #strategy? Telegram? Both?
5. **Job ID scheme:** Sequential (001, 002...) or timestamp-based or UUID?
