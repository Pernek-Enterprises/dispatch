# AGENTS.md — Dispatch Codebase Guide

You're working on **Dispatch**, a local agent orchestration system written in Go. This file helps you navigate the codebase.

## What Dispatch Does

Dispatch runs coding workflows: task → spec → code → review → ready. It manages model queues (one job per GPU at a time), spawns Pi processes to do the work, and advances through workflow steps automatically.

## Binary

Single Go binary. Both the **foreman** (daemon) and **CLI** (agent commands) are the same binary:

```
dispatch foreman          # run the daemon
dispatch done --job X     # agent signals completion
dispatch task create ...  # create a task
```

Entry point: `main.go` → routes to `cmd/*.go` based on subcommand.

## Package Layout

```
cmd/                        ← CLI commands + foreman
  foreman.go                  Main daemon: event loop, job dispatch, workflow advancement
  done.go, ask.go, fail.go   Agent CLI commands (write to pipe)
  answer.go                   Human answer command (unblocks waiting jobs)
  task.go                     Task create/list/show
  workflow.go                 Workflow list/show/validate/create
  setup.go                    Interactive setup wizard
  sessions.go                 Deprecated (stub)
  helpers.go                  Shared CLI utilities

internal/
  config/config.go            Load config.json, models.json from DISPATCH_ROOT
  jobs/jobs.go                Job CRUD: create, move (pending→active→done), read meta/result
  workflows/workflows.go      Parse workflow JSON, validate, step/branch logic
  state/state.go              state.json: model locks, task progress, iteration counts
  pipe/pipe.go                Named pipe: create, send, listen (JSON messages)
  pi/pi.go                    Spawn Pi processes (--print --no-session --skill)
  escalate/escalate.go        Send notifications via OpenClaw (openclaw agent --deliver)
  llm/llm.go                  Direct LLM calls to llama-server (for triage/parse/answer)
  log/log.go                  Simple file + stdout logger
```

## Key Data Flow

```
1. dispatch task create "fix bug" --workflow coding-easy
   → creates task dir + first job in jobs/pending/
   → sends "new_task" to pipe

2. Foreman receives pipe event
   → reads pending job
   → checks model lock (is 27b free?)
   → locks model, moves job to active/
   → spawns Pi: pi --model local-27b/... --skill skill/ --print "prompt"

3. Pi does work, then runs:
   dispatch done --job <id> --root ~/dispatch "wrote the spec"
   → writes result file
   → sends "done" to pipe

4. Foreman receives "done"
   → unlocks model
   → moves job to done/
   → reads workflow → determines next step
   → creates next job in pending/
   → if model free → dispatch immediately
```

## Workflow Steps

Defined in `workflows/<name>.json`. Each step has:
- `role` — which prompt to load (coder, reviewer)
- `model` — which model queue (9b, 27b)
- `branch` — keyword routing (ACCEPTED→ready, DENIED→fix)
- `maxIterations` — loop limit before escalation

Per-step prompts in `workflows/<name>/<step>.prompt.md`.

## State

Everything is files:
- `jobs/pending/*.json` — queued work
- `jobs/active/*.json` — in progress
- `jobs/done/*.json` — completed
- `state.json` — model locks + task progress
- `artifacts/<task-id>/` — outputs passed between steps

## Making Changes

- **New workflow step type?** → Add to `workflows.go` validation + `foreman.go` dispatch logic
- **New CLI command?** → Add `cmd/<name>.go` + wire in `main.go`
- **New notification channel?** → Update `escalate.go`
- **Change Pi invocation?** → `internal/pi/pi.go`

## Testing

```bash
make build                              # build binary
DISPATCH_ROOT=/tmp/test dispatch workflow validate coding-easy
DISPATCH_ROOT=/tmp/test dispatch task create "test" --workflow coding-easy
```
