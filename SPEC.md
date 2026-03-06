# Dispatch — Local Agent Orchestration System

**Version:** 0.5
**Date:** 2026-03-06
**Authors:** Stefan Pernek, Cleo

---

## 1. Problem Statement

The current agent orchestration (AgenticTodo queue → poller → skill → workflow → agent → API) has too many integration points. A format change in any layer silently breaks the pipeline. The system was designed for single-agent + cloud models but now runs on multi-model local hardware.

**Current pain points:**
- 6+ files to update for a workflow change
- Rigid JSON contracts between components — wrong format = silent failure
- No model-awareness — can't route work to specific GPUs
- No concurrency control — agents can't share models safely
- Heavy runtime overhead — OpenClaw carries ~20k system prompt per session

## 2. Design Principles

1. **Event-driven, not poll-driven** — the foreman reacts to events (CLI calls, new tasks). Polling only for intake + health checks.
2. **Deterministic foreman** — pure code, no LLM calls inline. LLM calls are queued jobs like everything else.
3. **Workflows define the graph** — explicit steps, explicit branches, simple keyword triggers. No LLM interpretation of flow control.
4. **Agents use a CLI** — four commands: `done`, `ask`, `fail`, `answer`. No format contracts, no API knowledge.
5. **Files are state** — `ls jobs/` tells you everything. Human-readable, human-debuggable.
6. **Lightweight execution** — Pi processes are ephemeral. Start, work, signal done, exit.
7. **Separation of concerns** — Pi handles execution, OpenClaw handles communication, Dispatch handles orchestration.

## 3. Architecture

```
                          ┌─────────────┐
                          │   Stefan    │
                          │  (Discord)  │
                          └──────┬──────┘
                                 │
                          ┌──────┴──────┐
                          │  Clawdia    │  ← OpenClaw (communication only)
                          │  escalation │
                          └──────┬──────┘
                                 │ dispatch answer
                          ┌──────┴──────┐
    ┌─────────────────────│   Foreman   │─────────────────────┐
    │                     │  (dispatch) │                     │
    │                     └──────┬──────┘                     │
    │                            │                            │
┌───┴────┐                 ┌─────┴─────┐                 ┌───┴────┐
│Model Q │                 │ Model Q   │                 │Model Q │
│  9B    │                 │   27B     │                 │  ...   │
└───┬────┘                 └─────┬─────┘                 └───┬────┘
    │                            │                           │
    ▼                            ▼                           ▼
┌────────┐                ┌──────────┐                  ┌────────┐
│   Pi   │                │    Pi    │                  │   Pi   │
│process │                │ process  │                  │process │
└────────┘                └──────────┘                  └────────┘
```

### Components

| Component | Role | Location |
|-----------|------|----------|
| **Dispatch foreman** | Orchestration — workflows, model queues, state, job lifecycle | Linux server |
| **Pi** | Execution — lightweight coding agent with read/write/edit/bash tools | Linux server |
| **llama-servers** | Models — Vulkan inference on local GPU | Linux server |
| **OpenClaw (Clawdia)** | Communication — relays escalations to Stefan via Discord/Telegram | Linux server |
| **dispatch CLI** | Agent interface — done/ask/fail/answer commands | Linux server |

### Execution Flow

```
Task created
  → Foreman picks workflow, creates first job
  → Foreman checks model queue (is 9B free?)
  → Model free → spawn Pi process with:
      --model local-9b/Qwen3.5-9B
      --skill skill/          (dispatch commands)
      --system-prompt prompts/coder.md
      --print --no-session    (ephemeral)
  → Pi does work using read/write/edit/bash tools
  → Pi runs: dispatch done --job <id> "summary"
  → Foreman receives event via named pipe
  → Foreman advances workflow → next step
  → Repeat until workflow complete
```

### Escalation Flow

```
Pi stuck → dispatch ask --escalate "question"
  → Foreman → openclaw agent --deliver --channel discord "question"
  → Stefan sees notification
  → Stefan resolves with Clawdia
  → dispatch answer --job <id> "do X"
  → Foreman re-dispatches Pi with answer appended
  → Pi continues
```

## 4. Workflows

A workflow defines an explicit step graph in JSON. Each step has a role, model, prompt, and artifacts. Branch points use simple keywords — the foreman does string matching, not LLM interpretation.

### Example: `workflows/coding-easy.json`

```json
{
  "name": "coding-easy",
  "description": "Simple coding tasks — bug fixes, small features.",
  "firstStep": "spec",
  "steps": {
    "spec": {
      "role": "coder",
      "model": "27b",
      "timeout": 600,
      "next": "code",
      "artifactsOut": ["spec.md"]
    },
    "code": {
      "role": "coder",
      "model": "9b",
      "timeout": 1800,
      "next": "review",
      "artifactsIn": ["spec.md"],
      "artifactsOut": ["diff.patch"]
    },
    "review": {
      "role": "reviewer",
      "model": "9b",
      "timeout": 900,
      "branch": { "ACCEPTED": "ready", "DENIED": "fix" },
      "maxIterations": 3,
      "artifactsIn": ["spec.md", "diff.patch"],
      "artifactsOut": ["review.md"]
    },
    "fix": {
      "role": "coder",
      "model": "27b",
      "timeout": 1200,
      "next": "review",
      "artifactsIn": ["review.md", "spec.md"],
      "artifactsOut": ["diff.patch"]
    },
    "ready": {
      "type": "human",
      "timeout": 0
    }
  },
  "destroy": {
    "timeout": 60,
    "actions": ["archive_artifacts", "cleanup_jobs"]
  }
}
```

### Per-step prompts: `workflows/coding-easy/<step>.prompt.md`

Each step has a prompt template file. The foreman loads and injects task context + artifact references.

### How branching works

```json
"branch": { "ACCEPTED": "ready", "DENIED": "fix" }
```

The foreman reads the agent's result and checks for the branch keyword. Simple `includes()` check:
- Result contains "ACCEPTED" → next step is `ready`
- Result contains "DENIED" → next step is `fix`
- Neither → treat as error, retry or escalate

No LLM needed. The agent is instructed to end with a clear keyword. Deterministic routing.

### Loops

`fix → review` creates a loop. The foreman tracks iteration count per step (configurable, default 3). If exceeded → auto-escalate to Stefan.

## 5. Jobs

A job = one workflow step being executed.

### Job lifecycle

```
pending/  →  active/  →  done/
                      →  failed/
```

### Job file: `jobs/pending/<uuid>-<slug>.json`

```json
{
  "id": "a1b2c3d4-spec-auth-fix",
  "task": "auth-fix-mobile",
  "workflow": "coding-easy",
  "step": "spec",
  "role": "coder",
  "model": "27b",
  "type": "work",
  "priority": "normal",
  "created": "2026-03-06T14:00:00Z",
  "timeout": 600
}
```

### Job types

| Type | Dispatch method | Description |
|------|----------------|-------------|
| `work` | Pi process | Real agent work with tools |
| `triage` | Direct LLM call | Pick workflow for new task |
| `parse` | Direct LLM call | Interpret results if needed |
| `answer` | Direct LLM call | Auto-answer agent question |
| `human` | Escalation notify | Wait for Stefan |

## 6. The `dispatch` CLI

Four commands.

```bash
# Signal completion
dispatch done --job <id> --root ~/.dispatch "summary"
dispatch done --job <id> --root ~/.dispatch --artifact spec.md "summary"

# Ask a question (auto-answered by LLM)
dispatch ask --job <id> --root ~/.dispatch "question"

# Escalate to human
dispatch ask --job <id> --root ~/.dispatch --escalate "need human decision"

# Report failure
dispatch fail --job <id> --root ~/.dispatch "reason"

# Answer a waiting job (used by human/Clawdia)
dispatch answer --job <id> --root ~/.dispatch "answer"
```

### How it works

The CLI does two things:
1. **Writes the result/question** to the job's result file
2. **Notifies the foreman** immediately via named pipe (`/tmp/dispatch.pipe`)

The foreman reacts instantly when notified. No polling for CLI events.

### Pi Skill

Agents learn dispatch commands via `skill/SKILL.md` — a Pi skill loaded on every invocation. Job-specific values (JOB_ID, DISPATCH_ROOT) are injected via `--append-system-prompt`.

## 7. Roles & Prompts

Roles replace agents. A role defines the personality and approach — not a persistent entity.

### Role prompts: `prompts/<role>.md`

**prompts/coder.md** — creative problem solver, writes clean code, tests thoroughly.
**prompts/reviewer.md** — adversarial reviewer, checks edge cases, gives clear ACCEPTED/DENIED verdicts.
**prompts/system.md** — shared system prompt loaded for all roles.

Prompt loading: `system.md` + `<role>.md` → Pi's `--system-prompt`.

Roles are referenced in workflow steps:
```json
{ "role": "coder", "model": "27b" }
```

The model determines which queue. The role determines which prompt.

## 8. Model Queues

Each model endpoint has a queue. One task at a time per model (GPU constraint). Multiple models run concurrently on different hardware.

### `state.json` (model section)

```json
{
  "models": {
    "9b": { "busy": true, "job": "a1b2c3d4", "since": "2026-03-06T14:00:00Z" },
    "27b": { "busy": false, "job": null, "since": null }
  }
}
```

### `models.json`

```json
{
  "9b": {
    "name": "Qwen3.5-9B-Q4_K_M.gguf",
    "provider": "local-9b",
    "endpoint": "http://localhost:8081/v1"
  },
  "27b": {
    "name": "Qwen3.5-27B-Q4_K_M.gguf",
    "provider": "local-27b",
    "endpoint": "http://localhost:8080/v1"
  }
}
```

Pi resolves models via `~/.pi/agent/models.json` (configured separately).

## 9. Artifacts

Stored in `artifacts/<task-id>/`:

```
artifacts/auth-fix-mobile/
  spec.md          ← from spec step
  diff.patch       ← from code step (overwritten by fix)
  review.md        ← from review step
```

- `dispatch done --artifact spec.md` → copies file to `artifacts/<task>/`
- Next step's prompt references: "Read artifacts/<task>/spec.md for context"
- Pi can read them directly via its file tools

## 10. Escalation & Notifications

### Escalation triggers
- Agent calls `dispatch ask --escalate`
- Job failure (`dispatch fail`)
- Max review loop iterations exceeded
- Job timeout (not yet implemented)

### Delivery
Foreman calls OpenClaw on the local machine:
```bash
openclaw agent --deliver --channel <channel> --reply-to <target> --message "notification"
```

### Configuration
```json
{
  "notifications": {
    "escalation": "discord",
    "target": "#dispatch"
  }
}
```

### Answer flow
Stefan resolves with Clawdia → `dispatch answer --job <id> "answer"` → foreman re-dispatches Pi with the answer appended to the original prompt.

## 11. Configuration

### `config.json`
```json
{
  "pollIntervalMs": 30000,
  "pipePath": "/tmp/dispatch.pipe",
  "maxLoopIterations": 3,
  "defaultTimeouts": {
    "triage": 60,
    "work": 1800,
    "parse": 60,
    "answer": 60
  },
  "notifications": {
    "escalation": "discord",
    "target": "#dispatch"
  },
  "pi": {
    "binary": "pi",
    "defaultTools": ["read", "bash", "edit", "write"]
  }
}
```

All config files have `.example` templates. Local files are gitignored.

## 12. File Structure

```
~/.dispatch/
├── dispatch                ← Go binary (foreman + CLI)
├── config.json             ← settings
├── models.json             ← model endpoints
├── state.json              ← locks, tasks
├── skill/
│   └── SKILL.md            ← Pi skill (dispatch commands)
├── prompts/
│   ├── system.md           ← shared system prompt
│   ├── coder.md            ← coder role identity
│   └── reviewer.md         ← reviewer role identity
├── workflows/
│   └── coding-easy/
│       ├── coding-easy.json
│       ├── spec.prompt.md
│       ├── code.prompt.md
│       ├── review.prompt.md
│       ├── fix.prompt.md
│       └── ready.prompt.md
├── jobs/
│   ├── pending/
│   ├── active/
│   ├── done/
│   └── failed/
├── artifacts/
│   └── <task-id>/
└── logs/
    ├── foreman.log
    └── pi-<job-id>.log
```

## 13. Implementation Status

### ✅ Complete
- [x] Go binary — single 8MB binary, cross-compiles to Linux
- [x] Foreman event loop — pipe listener + polling + unified channel
- [x] CLI commands — `done`, `ask`, `fail`, `answer`
- [x] Named pipe — `/tmp/dispatch.pipe`
- [x] Job system — pending/active/done/failed lifecycle
- [x] Workflow engine — JSON parsing, step advancement, keyword branching
- [x] Model queues — exclusive locks, auto-release on done/fail
- [x] Loop detection — max iterations per step, auto-escalate
- [x] Artifact system — `artifacts/<task-id>/`
- [x] Pi execution — `--print --no-session --skill` invocation
- [x] Pi skill — `skill/SKILL.md` teaches agents dispatch commands
- [x] Role prompts — `prompts/coder.md`, `prompts/reviewer.md`
- [x] Escalation — OpenClaw delivery to Discord/Telegram
- [x] Answer flow — `dispatch answer` unblocks waiting jobs
- [x] Task CLI — `dispatch task create|list|show`
- [x] Workflow CLI — `dispatch workflow list|show|validate|create`
- [x] Setup wizard — `dispatch setup` interactive config
- [x] Config templates — all `.example` files, local gitignored

### 🔲 Not Yet Implemented
- [ ] Direct LLM calls for triage/parse/answer jobs
- [ ] Timeout detection — active jobs past deadline → auto-fail
- [ ] Systemd service for foreman
- [ ] Log rotation + job archival
- [ ] Task intake from external source (API, webhook)
- [ ] Second workflow template (code-review)

## 14. Resolved Decisions

| Decision | Choice | Reason |
|----------|--------|--------|
| Runtime | Go (single binary) | No deps, fast CLI startup, cross-compile |
| Execution | Pi (coding agent) | Lightweight, ephemeral, 4 tools, millisecond startup |
| Communication | OpenClaw (Clawdia) | Already running, handles Discord/Telegram |
| GPU backend | Vulkan | 2243 t/s pp vs 39 t/s ROCm on gfx1201 |
| Foreman notification | Named pipe | Simple, instant, no dependencies |
| CLI commands | 4 (done/ask/fail/answer) | Minimal surface area |
| Workflow format | JSON + per-step prompt.md | Reliable parsing, rich prompts |
| Branching | Keyword match on result | Deterministic, no LLM interpretation |
| Model locking | Exclusive per job | Safer, predictable performance |
| Agents | Replaced by roles | No persistent entity, just prompt + model |
| Sessions | Ephemeral (Pi --no-session) | Artifacts carry context between steps |
| Config | `.example` templates | Nothing hardcoded, everything customizable |

## 15. Success Criteria

- Workflow change = edit one JSON file + prompt markdown
- Agent communication = 4 CLI commands
- No silent failures — timeouts caught, stuck agents detected, humans notified
- Full visibility — `cat state.json` + `ls jobs/`
- Models never double-booked
- New task → first agent working in < 60 seconds
- Stefan is a first-class participant in workflows
- Server runs lean — just dispatch + Pi + llama-servers
