# Dispatch — Local Agent Orchestration System

**Version:** 0.3
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
- **Workflow definitions** with per-step model + agent + prompt configuration
- **Model-aware scheduling** — knows which models are on which GPUs, prevents contention
- A **`dispatch` CLI** that agents use to communicate back (complete, ask questions, fail)
- **Persistent sessions per task** — all steps within a task share context, foreman manages lifecycle

### Architecture

```
┌──────────────┐     ┌──────────────────────────────────────┐
│  AgenticTodo │     │            ~/dispatch/                │
│  (task mgmt) │────▶│                                      │
│  browser UI  │     │  foreman.js  (deterministic loop)    │
└──────────────┘     │    ↕ reads/writes files              │
                     │                                      │
                     │  jobs/                                │
                     │    pending/   ← queued work           │
                     │    active/    ← in progress           │
                     │    done/      ← completed             │
                     │    failed/    ← timed out / errored   │
                     │                                      │
                     │  workflows/  ← step definitions       │
                     │  state.json  ← locks + sessions       │
                     │  models.json ← endpoint config        │
                     │  agents.json ← agent registry         │
                     │                                      │
                     │  bin/dispatch  ← CLI for agents       │
                     └──────────┬───────────────────────────┘
                                │
                    ┌───────────┴───────────┐
                    │                       │
              Direct API call         OpenClaw session
              (triage/parse/answer)   (agent work)
                    │                       │
                    ▼                       ▼
             ┌────────────┐         ┌──────────────┐
             │ llama-server│         │  Agent (Kit,  │
             │ 9B / 27B    │         │  Hawk, etc.)  │
             └────────────┘         │  with tools   │
                                    └──────────────┘
```

## 3. Core Concepts

### 3.1 Workflows

A workflow defines the steps to complete a type of task. Each step specifies its agent, model, prompt, artifacts, and how the agent communicates back.

**Example: `workflows/coding-easy.md`**

```markdown
# Workflow: coding-easy

Description: Simple coding tasks — bug fixes, small features, straightforward changes.

## Steps

### 1. spec
agent: kit
model: 27b
timeout: 10m

#### Artifacts
- Task description
- Linked issues/PRs (if any)

#### Prompt
Analyze this task and write a technical spec. Identify:
- Root cause (for bugs) or requirements (for features)
- Files likely affected
- Approach and estimated complexity

#### Instructions
When done: `dispatch complete --artifact spec.md`
If you need clarification: `dispatch ask "your question"`
If this task is unclear or too large: `dispatch fail "reason"`

---

### 2. code
agent: kit
model: 9b
timeout: 30m

#### Artifacts
- Spec from step 1

#### Prompt
Implement the spec. Create a branch (`agent/<task-id>-<slug>`), make changes, push.
Write clean, tested code. Commit with clear messages.

#### Instructions
When done: `dispatch complete --artifact diff.patch`
If stuck: `dispatch ask "your question"`
If blocked: `dispatch fail "reason"`

---

### 3. review
agent: hawk
model: 9b
timeout: 15m

#### Artifacts
- Code diff from step 2
- Spec from step 1

#### Prompt
Review this implementation against the spec. Check for:
- Correctness, edge cases, security
- Code quality, naming, structure
- Missing tests

#### Instructions
If approved: `dispatch complete --artifact review.md`
If changes needed: `dispatch request-changes --artifact review.md`
If fundamentally wrong: `dispatch fail "reason"`

---

### 4. fix
agent: kit
model: 27b
timeout: 20m
condition: only if step 3 requested changes

#### Artifacts
- Review feedback from step 3
- Original spec from step 1

#### Prompt
Address the review feedback. Fix the issues identified, push updates.

#### Instructions
When done: `dispatch complete --artifact diff.patch`

---

### 5. approve
agent: stefan
model: null
timeout: none

#### Artifacts
- PR link
- Review summary

#### Prompt
(notification sent to Stefan — human review step)

#### Instructions
Human merges or requests further changes via dispatch CLI or UI.
```

**Key design:**
- Model is per-step, not per-agent — Kit uses 27B for spec/fix, 9B for coding
- Steps reference artifacts from previous steps (foreman passes them along)
- Each step has explicit `dispatch` CLI instructions — agents know exactly how to communicate
- Human steps (agent: stefan) block until human acts
- Conditional steps (fix only if review requested changes)

### 3.2 Jobs

A job represents a single workflow step being executed. It moves through folders:

```
pending/ → active/ → done/
                   → failed/
```

**Job file format** (example: `jobs/pending/001-spec-auth-fix.md`):

```markdown
# Job: 001-spec-auth-fix

task: auth-fix-mobile
workflow: coding-easy
step: 1 (spec)
agent: kit
model: 27b
type: work
priority: normal
created: 2026-03-05T14:00:00Z
deadline: 2026-03-05T14:10:00Z

## Artifacts

Task: Fix auth redirect loop on mobile
Description: Users on iOS Safari get stuck in a redirect loop after OAuth callback.
Category: coding

## Prompt

Analyze this task and write a technical spec. Identify:
- Root cause
- Files likely affected
- Approach and estimated complexity

## Instructions

When done: `dispatch complete --artifact spec.md`
If you need clarification: `dispatch ask "your question"`
If this task is unclear or too large: `dispatch fail "reason"`

## Result

(filled when complete)

## Questions

(agent writes questions here via `dispatch ask`)
```

### 3.3 Job Types

| Type | Dispatch method | Model | Duration |
|------|----------------|-------|----------|
| `triage` | Direct API call to llama-server | 9B | ~5s |
| `work` | OpenClaw session | per step | 1-30 min |
| `parse` | Direct API call to llama-server | 9B | ~5s |
| `answer` | Direct API call to llama-server | 9B | ~5s |
| `human` | Notification to Stefan | none | indefinite |

### 3.4 Agents

**`agents.json`** — agent registry:

```json
{
  "kit": {
    "role": "coder",
    "capabilities": ["code-fix", "code-new"]
  },
  "hawk": {
    "role": "reviewer",
    "capabilities": ["code-review"]
  },
  "stefan": {
    "role": "human",
    "capabilities": ["approve", "decide"],
    "notify": ["discord", "telegram"]
  }
}
```

No default models — models are defined per workflow step. An agent might use 9B for one step and 27B for another.

### 3.5 Sessions

Each **task** gets a persistent OpenClaw session per agent. All steps for the same agent within a task share one session, preserving context.

**Example: task "auth-fix-mobile"**

| Step | Agent | Session |
|------|-------|---------|
| 1. spec | kit | `kit-auth-fix-001` (spawned) |
| 2. code | kit | `kit-auth-fix-001` (reused) |
| 3. review | hawk | `hawk-auth-fix-001` (spawned) |
| 4. fix | kit | `kit-auth-fix-001` (reused — has full context) |
| 5. approve | stefan | no session (human) |

**Lifecycle (managed entirely by foreman):**
1. **Spawn** — first step for an agent on a task → create session
2. **Send** — subsequent steps for same agent + task → send to existing session
3. **Memory** — on task completion → send "write a memory summary of this work" to each session
4. **Destroy** — after memory is written → destroy all sessions for this task

### 3.6 State

**`state.json`** — the lock table + session registry:

```json
{
  "models": {
    "9b": { "busy": false, "job": null, "since": null },
    "27b": { "busy": true, "job": "001-spec-auth-fix", "since": "2026-03-05T14:00:00Z" }
  },
  "agents": {
    "kit": {
      "busy": true,
      "job": "001-spec-auth-fix",
      "since": "2026-03-05T14:00:00Z"
    },
    "hawk": {
      "busy": false,
      "job": null,
      "since": null
    }
  },
  "sessions": {
    "auth-fix-mobile": {
      "kit": { "sessionKey": "kit-auth-fix-001", "created": "2026-03-05T14:00:00Z" },
      "hawk": null
    }
  },
  "tasks": {
    "auth-fix-mobile": {
      "workflow": "coding-easy",
      "currentStep": 1,
      "status": "active",
      "created": "2026-03-05T13:55:00Z"
    }
  }
}
```

## 4. The `dispatch` CLI

A lightweight CLI installed in agent sessions. Agents use it to communicate with the foreman without knowing dispatch internals.

### Commands

```bash
# Mark current step as complete, optionally attach artifacts
dispatch complete
dispatch complete --artifact spec.md
dispatch complete --artifact diff.patch --message "Fixed the redirect loop"

# Request changes (review steps)
dispatch request-changes --artifact review.md

# Ask a question (may be auto-answered by LLM or escalated to human)
dispatch ask "Should I split this into two PRs?"

# Report failure
dispatch fail "Cannot reproduce the bug on latest main"

# Check status
dispatch status

# Get artifact from a previous step
dispatch artifact get spec.md
dispatch artifact list
```

### How it works

The CLI writes to a lightweight communication channel — options:

**Option A: File-based**
- `dispatch complete` → writes a `.complete` marker + result to the job's active file
- `dispatch ask` → appends to the `## Questions` section of the job file
- Foreman picks up on next cycle (≤30s latency)

**Option B: Unix socket / HTTP**
- Foreman runs a tiny local server (e.g. `localhost:9090`)
- CLI POSTs to it → immediate response
- Lower latency, foreman can react instantly

**Recommendation:** Start with Option A (file-based) for simplicity. Move to B if latency matters.

### Agent instructions

Every job includes `## Instructions` that tell the agent exactly which `dispatch` commands to use. The agent doesn't need to know the dispatch system — just run the commands listed.

## 5. Foreman Loop

The foreman is a **deterministic Node.js script**. No LLM calls. Runs every 30 seconds via internal setInterval.

### Each cycle:

```
1. Read state.json (locks, sessions, task progress)

2. Check active/ jobs for signals
   - .complete marker? → read result, move to done/, release locks
   - .failed marker? → move to failed/, release locks
   - New question in job file? → create answer job in pending/
   - Past deadline? → move to failed/, release locks, alert Stefan

3. Process done/ jobs (advance workflows)
   - Read the completed job's task + workflow + step
   - Determine next step:
     → If conditional step and condition not met → skip
     → If next step exists → create new job in pending/
     → If last step → task complete, trigger memory + session cleanup
     → If step requested changes → route to fix step
   - Pass artifacts from completed step to next job

4. Scan pending/ for dispatchable jobs
   - Sort by priority, then creation time
   - For each job:
     → Is the required model free?
     → Is the required agent free?
     → If both free: claim locks, move to active/, dispatch
   - Quick LLM jobs (triage/parse/answer): call API directly, write result, done
   - Agent work jobs: spawn or send to OpenClaw session

5. Write updated state.json

6. Cleanup: archive old done/failed jobs (>7 days)
```

### Dispatching an agent work job:

```javascript
// First step for this agent on this task?
if (!state.sessions[taskId]?.[agent]) {
  // Spawn new session
  const session = await openclawSpawn({
    task: jobContent,
    label: `${agent}-${taskId}`,
    model: modelEndpoint
  });
  state.sessions[taskId][agent] = { sessionKey: session.key };
} else {
  // Send to existing session
  await openclawSend({
    sessionKey: state.sessions[taskId][agent].sessionKey,
    message: jobContent
  });
}
```

### Task completion:

```javascript
// All steps done — cleanup
for (const [agent, session] of Object.entries(state.sessions[taskId])) {
  if (session) {
    await openclawSend({
      sessionKey: session.sessionKey,
      message: "Task complete. Write a memory summary of what you did, decisions made, and lessons learned."
    });
    // Wait for memory write, then destroy
    await openclawDestroy(session.sessionKey);
  }
}
delete state.sessions[taskId];
```

## 6. Model Contention

Both models run simultaneously on the R9700 (Vulkan):
- **9B** on port 8081 (~5.3 GB VRAM)
- **27B** on port 8080 (~15.6 GB VRAM)
- Total: ~21 GB of 32 GB — room for context windows

Each model server handles one request at a time. The foreman's lock table prevents concurrent requests to the same model. If both models are busy, jobs wait in pending/.

Quick LLM jobs (triage/parse/answer) also respect model locks — they're queued like everything else. A triage job needing the 9B waits if an agent is using the 9B.

## 7. Questions & Escalation

### Auto-answer flow:
1. Agent calls `dispatch ask "question"`
2. CLI writes question to job file
3. Foreman sees it → creates an `answer` job (model: 9B)
4. When 9B is free → foreman sends question + task context to model
5. Model responds → foreman writes answer back to job file
6. Agent reads answer (polling or notification via CLI)

### Escalation flow:
1. Answer job's LLM response includes uncertainty ("I'm not sure", "this depends on business requirements")
2. OR question is tagged as high-stakes by the agent (`dispatch ask --escalate "..."`)
3. Foreman creates a `human` job instead → sends notification to Stefan
4. Agent stays in `active/` but paused
5. Stefan responds (via Discord, CLI, or future UI) → foreman writes answer, agent continues

### Timeout flow:
1. Job exceeds its deadline
2. Foreman moves to failed/, releases locks
3. Creates a new triage job: "This step timed out. Here's what the agent was doing. Decide: retry, skip, or escalate."
4. Triage determines next action

## 8. File Structure

```
~/dispatch/
├── foreman.js              ← the deterministic loop
├── bin/
│   └── dispatch            ← CLI for agents
├── state.json              ← locks, sessions, task progress
├── models.json             ← endpoint config
├── agents.json             ← agent registry
├── config.json             ← foreman settings
├── workflows/
│   ├── coding-easy.md
│   ├── coding-complex.md
│   ├── code-review.md
│   ├── research.md
│   └── general.md
├── jobs/
│   ├── pending/
│   ├── active/
│   ├── done/
│   └── failed/
├── artifacts/
│   └── <task-id>/          ← artifacts passed between steps
│       ├── spec.md
│       ├── diff.patch
│       └── review.md
└── logs/
    └── 2026-03-05.log
```

## 9. Implementation Plan

### Phase 0: Clean Slate
- [ ] Stop queue poller on Mac Mini
- [ ] Stop queue poller on Clawdia (if running)
- [ ] Don't delete — disable for rollback

### Phase 1: Core Foreman
- [ ] Dual llama-server already running (9B :8081, 27B :8080) ✅
- [ ] Create `~/dispatch/` file structure on Clawdia
- [ ] Write `models.json`, `agents.json`, `config.json`
- [ ] Build `foreman.js` — core loop:
  - State management (read/write state.json)
  - Job scanning (pending → active → done)
  - Lock management (model + agent)
  - Timeout detection
  - Direct LLM dispatch (triage/parse/answer jobs)
- [ ] Build `bin/dispatch` CLI:
  - `complete`, `fail`, `ask`, `status`, `artifact`
  - File-based communication with foreman
- [ ] Set up foreman as systemd service
- [ ] Test with manually created job files

### Phase 2: Workflows + Triage
- [ ] Write 2-3 workflow templates (coding-easy, code-review, general)
- [ ] Implement triage job type — new task → LLM picks workflow
- [ ] Implement workflow step advancement
- [ ] Implement artifact passing between steps
- [ ] Test: manual task → triage → first work step

### Phase 3: Agent Integration
- [ ] Wire up OpenClaw session management (spawn/send/destroy)
- [ ] Implement session persistence across workflow steps
- [ ] Implement memory write + cleanup on task completion
- [ ] Implement question/answer flow
- [ ] Implement escalation to Stefan
- [ ] End-to-end test: task → triage → work → review → done

### Phase 4: Polish
- [ ] AgenticTodo integration (read tasks, write results)
- [ ] Notification delivery (Discord/Telegram)
- [ ] Web UI for dispatch state
- [ ] Open source packaging

## 10. Success Criteria

- **One file to change** for workflow updates
- **Zero format contracts** — agents use `dispatch` CLI, not JSON
- **No silent failures** — every stuck job gets detected and handled
- **Full visibility** — `ls jobs/` + `cat state.json` tells you everything
- **Model-aware** — no two jobs fight for the same model
- **Context preserved** — agent keeps full history within a task
- **Human in the loop** — Stefan is a first-class step in workflows

## 11. Open Questions

1. ~~Single vs dual llama-server~~ → **Resolved: dual, both running** ✅
2. **CLI communication:** File-based (simple, 30s latency) vs socket (instant)? Start file-based.
3. **Job ID scheme:** Sequential? Timestamp? UUID? → Recommend timestamp-based: `20260305-140000-spec-auth-fix`
4. **Artifact storage:** In the job file or separate `artifacts/<task>/` folder? → Recommend separate folder for binary artifacts.
5. **AgenticTodo integration timing:** Phase 4, or do we need it earlier for task intake?
6. **Notification channel for escalations:** Discord #strategy? Telegram DM?
