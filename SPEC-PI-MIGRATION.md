# Spec: Pi Execution Layer

## Summary

Replace OpenClaw agent sessions with direct Pi (coding agent) invocations.
Dispatch becomes a standalone orchestrator that needs only Pi + llama-servers.
OpenClaw stays on the Mac for Cleo (human interface) — not on the execution path.

## Architecture

```
                          ┌─────────────┐
                          │   Stefan    │
                          │  (Discord)  │
                          └──────┬──────┘
                                 │
                          ┌──────┴──────┐
                          │    Cleo     │  ← OpenClaw (Mac only)
                          │  (coord)   │
                          └──────┬──────┘
                                 │ dispatch ask --escalate
                          ┌──────┴──────┐
    ┌─────────────────────│   Foreman   │─────────────────────┐
    │                     │  (dispatch) │                     │
    │                     └──────┬──────┘                     │
    │                            │                            │
┌───┴────┐  ┌───────────┐  ┌────┴─────┐  ┌───────────┐  ┌───┴────┐
│Model Q │  │ Model Q   │  │ Model Q  │  │ Model Q   │  │ Model Q│
│  9B    │  │   27B     │  │  Codex   │  │  Kimi     │  │  ...   │
└───┬────┘  └─────┬─────┘  └────┬─────┘  └─────┬─────┘  └───┬────┘
    │             │              │               │            │
    ▼             ▼              ▼               ▼            ▼
┌────────┐  ┌──────────┐  ┌──────────┐   ┌──────────┐  ┌────────┐
│   Pi   │  │    Pi    │  │    Pi    │   │    Pi    │  │   Pi   │
│process │  │ process  │  │ process  │   │ process  │  │process │
└────────┘  └──────────┘  └──────────┘   └──────────┘  └────────┘
```

## Key Changes

### 1. Model Queues Replace Agent+Model Locks

**Before:** Lock agent (kit) + lock model (9b) → dispatch
**After:** Lock model (9b) → dispatch. No agent concept.

Each model endpoint has a queue. One task at a time per model (GPU constraint).
Multiple models can run concurrently if on different hardware.

State simplifies from:
```json
{
  "models": { "9b": { "busy": true, "job": "abc" } },
  "agents": { "kit": { "busy": true, "job": "abc" } }
}
```
To:
```json
{
  "models": { "9b": { "busy": true, "job": "abc" } }
}
```

### 2. Pi Replaces OpenClaw Sessions

**Before:**
```go
openclaw agent --agent kit --session-id dispatch-abc-kit-9b --message "prompt"
```

**After:**
```go
pi --model local-llm/Qwen3.5-9B --print --no-session --system-prompt "system.md" "step prompt"
```

Key Pi flags:
- `--model <provider/id>` — which model to use
- `--print` (-p) — non-interactive, process and exit
- `--no-session` — ephemeral, no session file saved
- `--system-prompt <text|file>` — custom system prompt
- `--append-system-prompt <text>` — add dispatch instructions
- `--tools read,bash,edit,write` — standard tools (default)
- `--skill <path>` — load dispatch skill (optional)
- `--session-dir <dir>` — for persistent sessions (if needed)

### 3. Workflow Steps Reference Models, Not Agents

**Before:**
```json
{
  "spec": { "agent": "kit", "model": "27b", ... },
  "code": { "agent": "kit", "model": "9b", ... },
  "review": { "agent": "hawk", "model": "9b", ... }
}
```

**After:**
```json
{
  "spec": { "model": "27b", "role": "coder", ... },
  "code": { "model": "9b", "role": "coder", ... },
  "review": { "model": "9b", "role": "reviewer", ... }
}
```

`role` is optional — just used for prompt selection (prompts/coder.md, prompts/reviewer.md).
The model determines which queue the job enters.

### 4. Config Simplification

**Before (config.json):**
```json
{
  "openclaw": {
    "binary": "openclaw",
    "agents": {
      "kit": { "id": "kit" },
      "hawk": { "id": "hawk" }
    }
  }
}
```

**After (config.json):**
```json
{
  "pi": {
    "binary": "pi",
    "modelsConfig": "~/.pi/agent/models.json",
    "defaultTools": ["read", "bash", "edit", "write"],
    "sessionDir": null
  }
}
```

**agents.json → deleted.**
Only models.json remains (endpoint mapping for direct LLM calls + Pi model references).

### 5. Destroy Phase Simplifies

No sessions to close. Pi processes exit when done.
Destroy phase becomes:
1. Run destroy prompt on each model used in the workflow (optional cleanup)
2. Archive artifacts
3. Cleanup job files
4. Done

Or just skip destroy entirely — Pi is ephemeral by default.

### 6. Memory System

Pi can read/write files. Dispatch controls the working directory.
Memory is just files in the task's artifact directory:

```
artifacts/<task-id>/
  task.md          — original task description
  spec.md          — spec artifact
  diff.patch       — code artifact
  review.md        — review artifact
  memory.md        — agent notes (written by Pi, read by next step)
```

Each step's prompt can reference previous artifacts:
"Read artifacts/abc123/spec.md for the spec, then implement it."

### 7. Escalation Path

When Pi encounters something it can't resolve:
1. Pi runs `dispatch ask --job <id> "question"` via bash tool
2. Foreman receives the question
3. Foreman notifies Stefan via Cleo/Discord/Telegram
4. Stefan responds
5. Foreman creates an answer job or sends response back

This is the ONLY OpenClaw touchpoint — for human communication.

## What Gets Deleted

- `internal/sessions/sessions.go` — entire file
- `cmd/sessions.go` — entire file
- `agents.json` + `agents.json.example` — no more agents
- `config.json` `openclaw` block — replaced with `pi` block
- Agent locks in state.go — only model locks remain

## What Gets Added

- `internal/pi/pi.go` — Pi process management (start, log, wait)
- `config.json` `pi` block
- Updated workflow schema (model + role instead of agent + model)
- Updated prompts (role-based instead of agent-based)

## What Stays

- Foreman event loop, pipe, health checks
- Workflow engine, step advancement, branching
- Job system (pending/active/done/failed)
- Model locks
- Task CLI
- Direct LLM calls for triage/parse/answer
- Artifact system

## Migration Steps

1. Create `internal/pi/pi.go` — Pi process launcher
2. Update `cmd/foreman.go` — replace `dispatchToSession` with `dispatchToPi`
3. Update workflow schema — `model` + `role` instead of `agent` + `model`
4. Update config — `pi` block instead of `openclaw` block
5. Remove agent locks from state
6. Remove sessions package
7. Update setup wizard
8. Update coding-easy workflow
9. Create role-based prompts (prompts/coder.md, prompts/reviewer.md)
10. Test end-to-end
