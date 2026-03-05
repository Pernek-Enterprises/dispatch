# Dispatch — Local Agent Orchestration System

**Version:** 0.4
**Date:** 2026-03-05
**Authors:** Stefan Pernek, Cleo

---

## 1. Problem Statement

The current agent orchestration (AgenticTodo queue → poller → skill → workflow → agent → API) has too many integration points. A format change in any layer silently breaks the pipeline. The system was designed for single-agent + cloud models but now runs on multi-model local hardware.

**Current pain points:**
- 6+ files to update for a workflow change
- Rigid JSON contracts between components — wrong format = silent failure
- No model-awareness — can't route work to specific GPUs
- No concurrency control — agents can't share models safely
- Agents call APIs directly — fragile, breaks when schemas change

## 2. Design Principles

1. **Event-driven, not poll-driven** — the foreman reacts to events (CLI calls, new tasks). Polling only for intake + health checks.
2. **Deterministic foreman** — pure code, no LLM calls inline. LLM calls are queued jobs like everything else.
3. **Workflows define the graph** — explicit steps, explicit branches, simple keyword triggers. No LLM interpretation of flow control.
4. **Agents use a CLI** — three commands: `done`, `ask`, `fail`. No format contracts, no API knowledge.
5. **Files are state** — `ls jobs/` tells you everything. Human-readable, human-debuggable.
6. **Sessions preserve context** — all steps for the same agent + task share one session.

## 3. Architecture

```
                    ┌────────────────────────────────────────┐
                    │            ~/dispatch/                  │
 New tasks ────────▶│                                        │
 (poll/manual)      │  foreman.js                            │
                    │    • event-driven (CLI notifies)        │
                    │    • polls only for intake + health     │
                    │                                        │
                    │  ┌─────────────────────────────────┐   │
                    │  │ jobs/                            │   │
                    │  │   pending/ → active/ → done/     │   │
                    │  │                      → failed/   │   │
                    │  └─────────────────────────────────┘   │
                    │                                        │
                    │  workflows/   agents.json   models.json│
                    │  artifacts/   state.json    config.json │
                    │                                        │
                    │  bin/dispatch  ← CLI (done/ask/fail)   │
                    └────────┬──────────────┬────────────────┘
                             │              │
                   Direct API call    OpenClaw session
                   (triage/parse)     (agent work)
                             │              │
                             ▼              ▼
                      ┌──────────┐   ┌─────────────┐
                      │ 9B :8081 │   │ Agent + tools│
                      │ 27B:8080 │   │ (Kit, Hawk)  │
                      └──────────┘   └─────────────┘
```

## 4. Workflows

A workflow defines an explicit step graph. Each step has an agent, model, prompt, and artifacts. Branch points use simple keywords — the foreman does string matching, not LLM interpretation.

### Example: `workflows/coding-easy.md`

```markdown
# coding-easy

Simple coding tasks — bug fixes, small features.

## Graph

spec → code → review → [ACCEPTED: ready] [DENIED: fix]
fix → review (loop)
ready → approve

## Steps

### spec
agent: kit
model: 27b
timeout: 10m
artifacts_in: [task]
artifacts_out: [spec.md]

Write a technical spec for this task:
- Root cause or requirements
- Files affected
- Approach and complexity estimate

---

### code
agent: kit
model: 9b
timeout: 30m
artifacts_in: [spec.md]
artifacts_out: [diff.patch]

Implement the spec. Branch: `agent/<task-id>-<slug>`.
Write clean code, commit with clear messages, push.

---

### review
agent: hawk
model: 9b
timeout: 15m
artifacts_in: [spec.md, diff.patch]
artifacts_out: [review.md]
branch: ACCEPTED | DENIED

Review this implementation against the spec.
Check correctness, edge cases, security, code quality.

End your review with exactly one of:
- `ACCEPTED` — code is good to merge
- `DENIED` — changes needed (explain what)

---

### fix
agent: kit
model: 27b
timeout: 20m
artifacts_in: [review.md, spec.md]
artifacts_out: [diff.patch]
next: review

Address the review feedback. Fix issues, push updates.

---

### ready
agent: stefan
model: null
timeout: none

PR ready for human review and merge.
```

### How branching works

The workflow `## Graph` section defines transitions:
```
review → [ACCEPTED: ready] [DENIED: fix]
```

The foreman reads the agent's result and checks for the branch keyword. Simple `includes()` check:
- Result contains "ACCEPTED" → next step is `ready`
- Result contains "DENIED" → next step is `fix`
- Neither → treat as error, retry or escalate

No LLM needed. The agent is instructed to end with a clear keyword. Deterministic routing.

### Loops

`fix → review` creates a loop. The foreman tracks iteration count and has a max (configurable, default 3). If exceeded → escalate to Stefan.

## 5. Jobs

A job = one workflow step being executed.

### Job file: `jobs/pending/20260305-140000-spec-auth-fix.json`

```json
{
  "id": "20260305-140000-spec-auth-fix",
  "task": "auth-fix-mobile",
  "workflow": "coding-easy",
  "step": "spec",
  "agent": "kit",
  "model": "27b",
  "type": "work",
  "priority": "normal",
  "created": "2026-03-05T14:00:00Z",
  "timeout": 600,
  "iteration": 1
}
```

### Job prompt: `jobs/pending/20260305-140000-spec-auth-fix.prompt.md`

```markdown
# Task: Fix auth redirect loop on mobile

Users on iOS Safari get stuck in a redirect loop after OAuth callback.

## Your job

Write a technical spec for this task:
- Root cause or requirements
- Files affected
- Approach and complexity estimate

## Artifacts from previous steps

(none — this is the first step)

## How to communicate

- When done: `dispatch done "summary of what you did"`
- To attach files: `dispatch done --artifact spec.md`
- If you need clarification: `dispatch ask "your question"`
- If this is blocked/impossible: `dispatch fail "reason"`
```

### Job types

| Type | Dispatch method | Description |
|------|----------------|-------------|
| `work` | OpenClaw session | Real agent work with tools |
| `triage` | Direct API call | Pick workflow for new task |
| `parse` | Direct API call | Interpret results if needed |
| `answer` | Direct API call | Auto-answer agent question |
| `human` | Notification | Wait for Stefan |

## 6. The `dispatch` CLI

Three commands. That's it.

```bash
dispatch done "Fixed the redirect by adding state param to OAuth flow"
dispatch done --artifact spec.md --artifact diff.patch
dispatch ask "Should I split this into separate PRs for backend and frontend?"
dispatch fail "Cannot reproduce — works fine on latest main"
```

### How it works

The CLI does two things:
1. **Writes the result/question** to the job's result file (`jobs/active/<id>.result.md`)
2. **Notifies the foreman** immediately (signals the foreman process — Unix signal, named pipe, or HTTP ping to localhost)

The foreman **does not poll** for CLI events. It reacts instantly when notified.

### What the foreman does on notification

```
CLI calls "dispatch done" →
  1. CLI writes result file + sends signal to foreman
  2. Foreman reads result
  3. Foreman moves job from active/ to done/
  4. Foreman releases locks (model + agent)
  5. Foreman checks workflow graph → creates next job in pending/
  6. Foreman checks if next job can be dispatched immediately
  7. If yes → dispatch. Entire cycle in milliseconds.
```

### Installation

The `dispatch` CLI is added to the agent's PATH when the session is spawned. The foreman includes the CLI path in the session setup.

## 7. Agents

### `agents.json`

```json
{
  "kit": {
    "role": "coder",
    "capabilities": ["spec", "code", "fix"]
  },
  "hawk": {
    "role": "reviewer",
    "capabilities": ["review"]
  },
  "stefan": {
    "role": "human",
    "capabilities": ["approve", "decide"],
    "notify": ["discord"]
  }
}
```

No default models. Models are per workflow step.

## 8. Sessions

Each **task** gets one session per agent. Steps for the same agent reuse the session.

### Example: task "auth-fix-mobile"

| Step | Agent | Session | Action |
|------|-------|---------|--------|
| spec | kit | `kit-auth-fix` | **spawn** |
| code | kit | `kit-auth-fix` | reuse |
| review | hawk | `hawk-auth-fix` | **spawn** |
| fix | kit | `kit-auth-fix` | reuse (has full context) |
| review | hawk | `hawk-auth-fix` | reuse |
| approve | stefan | — | notification only |

### Lifecycle (foreman manages everything)

1. **Spawn** — first step for agent+task → create OpenClaw session
2. **Send** — subsequent steps → send to existing session
3. **Health check** — before sending, verify session is alive. If dead → respawn (context lost but pipeline continues)
4. **Memory** — on task completion → send "write a memory summary" to each session
5. **Destroy** — after memory written → destroy session

## 9. State

### `state.json`

```json
{
  "models": {
    "9b": { "busy": false, "job": null, "since": null },
    "27b": { "busy": true, "job": "20260305-140000-spec-auth-fix", "since": "2026-03-05T14:00:00Z" }
  },
  "agents": {
    "kit": {
      "busy": true,
      "job": "20260305-140000-spec-auth-fix",
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
      "kit": "kit-auth-fix-session-key",
      "hawk": null
    }
  },
  "tasks": {
    "auth-fix-mobile": {
      "workflow": "coding-easy",
      "currentStep": "spec",
      "status": "active",
      "iteration": { "review": 0 },
      "created": "2026-03-05T13:55:00Z"
    }
  }
}
```

## 10. Foreman

### What it does

The foreman is a **Node.js process** that:
- **Reacts to events** from the `dispatch` CLI (instant)
- **Polls** for new task intake + health checks (every 30-60s)
- **Manages state** via `state.json`
- **Dispatches jobs** — direct API for LLM jobs, OpenClaw sessions for agent work

### Event handling (instant)

```
on CLI signal:
  1. Read result from active job
  2. Move job to done/ or failed/
  3. Release model + agent locks
  4. Check workflow graph → determine next step
  5. Handle branching (keyword match on result)
  6. Create next job in pending/
  7. If resources free → dispatch immediately
```

### Polling (every 30-60s)

```
on poll:
  1. Check for new tasks to triage (from intake source)
  2. Check active jobs for timeouts
     - Past deadline? → move to failed/, alert Stefan
  3. Scan pending/ for dispatchable jobs
     - Model free? Agent free? → claim locks, dispatch
  4. Check session health for active agent jobs
```

### Dispatching

**LLM jobs** (triage, parse, answer):
```javascript
const response = await fetch(`${model.endpoint}/chat/completions`, {
  method: 'POST',
  body: JSON.stringify({
    messages: [{ role: 'user', content: promptContent }],
    max_tokens: 2048
  })
});
// Write response to job result, move to done/
```

**Agent work jobs:**
```javascript
if (!sessions[taskId]?.[agent]) {
  // First step for this agent on this task — spawn session
  const session = await openclawSpawn({
    task: promptContent,
    label: `${agent}-${taskId}`
  });
  sessions[taskId][agent] = session.key;
} else {
  // Subsequent step — send to existing session
  await openclawSend({
    sessionKey: sessions[taskId][agent],
    message: promptContent
  });
}
// Lock model + agent, wait for CLI notification
```

## 11. Questions & Escalation

### Auto-answer (fast, stays in pipeline)

```
Agent: dispatch ask "Should I use the existing auth middleware or write new?"
  → CLI writes question + notifies foreman
  → Foreman creates answer job (type: answer, model: 9b)
  → When 9b free: sends question + task context to model
  → Model answers → foreman writes answer back to agent's job file
  → Agent reads answer, continues work
```

### Escalation (human needed)

Triggered when:
- Agent uses `dispatch ask --escalate "question"`
- Answer LLM's response expresses uncertainty
- Max retry/loop count exceeded
- Job times out

```
  → Foreman sends notification to Stefan (Discord/Telegram)
  → Job stays active but marked "waiting_human"
  → Stefan responds via CLI or chat
  → Foreman writes answer, agent continues
```

## 12. Artifacts

Stored in `artifacts/<task-id>/`:

```
artifacts/
└── auth-fix-mobile/
    ├── spec.md        ← from spec step
    ├── diff.patch     ← from code step (overwritten by fix step)
    └── review.md      ← from review step
```

- `dispatch done --artifact spec.md` → CLI copies file to `artifacts/<task>/spec.md`
- Next step's prompt includes: "Artifacts from previous steps are in `artifacts/<task>/`"
- Agent can read them directly (file access through OpenClaw session)

## 13. File Structure

```
~/dispatch/
├── foreman.js              ← main process
├── package.json
├── bin/
│   └── dispatch            ← agent CLI
├── state.json              ← locks, sessions, tasks
├── models.json             ← model endpoints
├── agents.json             ← agent registry
├── config.json             ← settings
├── workflows/
│   ├── coding-easy.md
│   ├── coding-complex.md
│   ├── code-review.md
│   └── research.md
├── jobs/
│   ├── pending/            ← queued (json + prompt.md)
│   ├── active/             ← in progress
│   ├── done/               ← completed
│   └── failed/             ← errored / timed out
├── artifacts/
│   └── <task-id>/          ← outputs passed between steps
└── logs/
    └── foreman.log
```

## 14. Implementation Plan

### Phase 1: Core
- [ ] Create file structure on Clawdia
- [ ] `models.json`, `agents.json`, `config.json`
- [ ] `foreman.js` — event loop + polling:
  - State management (read/write `state.json`)
  - Job scanning (pending → active)
  - Lock management (model + agent)
  - Timeout detection
  - Direct LLM dispatch for triage/parse/answer jobs
  - Event listener for CLI notifications
- [ ] `bin/dispatch` CLI:
  - `done`, `ask`, `fail`
  - Writes result file + notifies foreman
- [ ] Systemd service for foreman
- [ ] Manual test: create job file → foreman dispatches → verify

### Phase 2: Workflows
- [ ] Write 2 workflow templates (coding-easy, code-review)
- [ ] Implement workflow graph parsing
- [ ] Implement step advancement + branching (keyword match)
- [ ] Implement artifact passing
- [ ] Implement loop detection + max iteration
- [ ] Test: task → triage → spec → code → review → done

### Phase 3: Sessions + Agents
- [ ] OpenClaw session spawn/send/destroy
- [ ] Session persistence across steps
- [ ] Session health checks
- [ ] Memory write on task completion
- [ ] Question/answer flow
- [ ] Escalation to Stefan
- [ ] End-to-end test with real agent work

### Phase 4: Integration + Polish
- [ ] Task intake from AgenticTodo API
- [ ] Result reporting back to AgenticTodo
- [ ] Notification delivery
- [ ] Log rotation + job archival
- [ ] Open source packaging

## 15. Success Criteria

- Workflow change = edit one markdown file
- Agent communication = 3 CLI commands
- No silent failures — timeouts caught, stuck agents detected
- Full visibility — `cat state.json` + `ls jobs/`
- Models never double-booked
- Context preserved across workflow steps
- Stefan is a first-class participant in workflows
- New task → first agent working in < 60 seconds

## 16. Resolved Decisions

| Decision | Choice | Reason |
|----------|--------|--------|
| Dual vs single llama-server | Dual (9B + 27B simultaneously) | Both fit in 32GB, no swap latency |
| Backend | Vulkan | 2243 t/s pp vs 39 t/s ROCm on gfx1201 |
| Foreman communication | Event-driven (CLI notifies) | No wasted polling, instant reaction |
| CLI commands | 3 only (done/ask/fail) | Minimal surface area |
| Job metadata format | JSON (machine) + markdown (prompt) | Clean separation |
| Branching | Keyword match on agent output | Deterministic, no LLM interpretation |
| Model locking | Yes, exclusive per job | Safer, predictable performance |
| Polling | Only for intake + health checks | Everything else is event-driven |

## 17. Resolved (formerly Open) Questions

1. **CLI → foreman notification:** Named pipe (`/tmp/dispatch.pipe`). Simple, instant, no dependencies.
2. **Job ID scheme:** UUID (`crypto.randomUUID()`). Slug appended for readability: `a1b2c3d4-spec-auth-fix`.
3. **Max review loop:** Default 3, configurable per workflow via `max_iterations` on loop steps.
4. **Task intake:** Manual files in Phase 1. AgenticTodo API integration in Phase 4.
5. **Escalation channel:** Telegram (Stefan's primary).
