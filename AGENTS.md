# AGENTS.md — Dispatch Codebase Guide

You're working on **Dispatch**, a local agent orchestration system. This file covers the Go implementation. If you're working on the TypeScript rewrite, see `~/dispatch-ts/AGENTS.md`.

## What Dispatch Does

Dispatch runs coding workflows: task → spec → code → review → ready. It manages model queues (one job per GPU at a time), runs Pi processes or Pi SDK sessions to do the work, and advances through workflow steps automatically.

## Repository Status

- **`~/dispatch/`** — Go implementation (stable, being maintained)
- **`~/dispatch-ts/`** — TypeScript rewrite (active development, uses Pi SDK directly)
- **`~/.dispatch/`** — Live install (shared file formats, same `~/.dispatch/` root for both)

Both implementations read/write the same `~/.dispatch/` directory. File formats are identical.

## Go Binary Layout

Single Go binary. Both the **foreman** (daemon) and **CLI** are the same binary:

```
dispatch foreman          # run the daemon
dispatch done --job X     # signal completion via pipe
dispatch task create ...  # create a task
```

Entry point: `main.go` → routes to `cmd/*.go` based on subcommand.

### Package Layout

```
cmd/
  foreman.go              Main daemon: event loop, job dispatch, workflow advancement
  done.go, ask.go, fail.go  Agent CLI commands (write result to pipe)
  answer.go               Human answer command (unblocks waiting jobs)
  task.go                 Task create/list/show
  setup.go                Interactive setup wizard
  helpers.go              Shared CLI utilities

internal/
  config/config.go        Load config.json from DISPATCH_ROOT
  jobs/jobs.go            Job CRUD: create, move (pending→active→done), read/write
  workflows/workflows.go  Parse workflow JSON, validate, step/branch routing
  state/state.go          state.json: model locks, task progress, iteration counts
  pipe/pipe.go            Named FIFO pipe: create, send, listen (JSON protocol)
  pi/pi.go                Spawn Pi subprocess (--print --no-session --skill)
  escalate/escalate.go    OpenClaw message send notifications
  log/log.go              File + stderr logger
```

## Key Data Flow (Go)

```
1. dispatch task create "fix bug" --workflow coding-easy
   → creates task dir + first job in jobs/pending/
   → sends "new_task" to pipe

2. Foreman receives pipe event
   → reads pending job
   → checks model lock (is local-27b free?)
   → locks model, moves job to active/
   → spawns Pi: pi --model local-27b/... --skill skill/ --print "prompt"

3. Pi session ends, Pi ran dispatch done via bash:
   dispatch done --job <id> --root ~/.dispatch "wrote the spec"
   → writes result file
   → sends "done" to pipe

4. Foreman receives "done"
   → unlocks model, moves job to done/
   → reads workflow → next step
   → creates next job in pending/
```

**Note:** The TS rewrite replaces step 3 — Pi calls `task_done` tool instead of bash.

## Key Data Flow (TypeScript)

Same as above except:
- Step 3: Pi SDK session (in-process) ends when model calls `task_done` tool
- No subprocess — session runs in Node.js event loop
- Edit tool errors normalized before model sees them
- Loop detection built-in (5x identical tool calls → abort)

## Workflow Steps

Defined in `~/.dispatch/workflows/<name>.json`. Each step has:
- `role` — which prompt to load (`agents/<role>.md`)
- `model` — which model queue (`local-9b`, `local-27b`)
- `branch` — keyword routing (`ACCEPTED→ready`, `DENIED→fix`)
- `maxIterations` — loop limit before escalation

Per-step prompts in `~/.dispatch/workflows/<name>/<step>.prompt.md`.

## State: Everything is Files

```
~/.dispatch/
  jobs/pending/*.json       — queued, waiting for model
  jobs/active/*.json        — in progress
  jobs/done/*.json          — completed
  jobs/failed/*.json        — failed, reason written
  state.json                — model locks + task progress
  artifacts/<task-id>/      — files passed between steps
```

## Making Changes

- **New workflow step type?** → `workflows.go` + `foreman.go` dispatch logic
- **New CLI command?** → Add `cmd/<name>.go` + wire in `main.go`
- **New notification channel?** → `escalate.go`
- **Change Pi invocation?** → `internal/pi/pi.go`
- **TypeScript runner changes?** → `~/dispatch-ts/src/runner.ts`

## Testing

```bash
make build
DISPATCH_ROOT=/tmp/test ./dispatch task create "test task" --workflow coding-easy
DISPATCH_ROOT=/tmp/test ./dispatch task list
```
