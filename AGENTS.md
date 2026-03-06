# AGENTS.md — Dispatch TypeScript Codebase Guide

You're working on **Dispatch**, a local agent orchestration system written in TypeScript. This file helps you navigate the source.

## What Dispatch Does

Dispatch runs coding workflows: task → spec → code → review → ready. It manages model queues (one job per GPU at a time), runs Pi SDK sessions to do the work, and advances through workflow steps automatically.

## Binary

Single Node.js entry point. Both the **foreman** (daemon) and **CLI** (user/agent commands) are the same script:

```
dispatch foreman          # run the daemon
dispatch done --job X     # agent signals completion (CLI → pipe → foreman)
dispatch task create ...  # create a task
```

Entry point: `src/index.ts` → routes to `src/cli/*.ts` or `foreman.ts` based on subcommand.

## Source Layout

```
src/
  index.ts              CLI entrypoint — parses subcommand, calls the right handler
  foreman.ts            Main daemon: event loop, job dispatch, workflow advancement
  runner.ts             Pi SDK session manager — THE core innovation of this rewrite
  config.ts             Load config.json, resolve model aliases
  jobs.ts               Job CRUD: create, list, move, read/write result + meta
  workflows.ts          Parse workflow JSON, load step prompts, next-step routing
  state.ts              state.json: model locks, task status
  pipe.ts               Named FIFO pipe: create, listen, send (JSON protocol)
  escalate.ts           Notifications via OpenClaw message send
  prompts.ts            Build system prompts (agents/ + skill/ + task_done instructions)
  log.ts                File + stderr logger

  cli/
    task.ts             dispatch task create/list/show
    done.ts             dispatch done --job <id> "summary"
    answer.ts           dispatch answer --job <id> "text"
    ask.ts              dispatch ask --job <id> "question"
    fail.ts             dispatch fail --job <id> "reason"
    setup.ts            dispatch setup (interactive wizard)
```

## The Critical File: runner.ts

This is where the Go → TypeScript rewrite delivers its value. Read it carefully:

1. **`dispatchJob()`** — fires off a Pi SDK session asynchronously, returns immediately
2. **`runSession()`** — creates a `createAgentSession()` with:
   - **`task_done` tool** — model calls this instead of `bash("dispatch done ...")`
   - **`task_ask` tool** — model calls this when blocked
   - **`task_fail` tool** — model calls this on unrecoverable errors
   - **Wrapped `edit` tool** — intercepts "identical content" errors, returns success
   - **Loop detection** — tracks last 20 tool calls, aborts on 5 identical in a row
3. Session lifecycle: starts on `session.prompt(job.prompt)`, ends when model calls `task_done`/`task_fail` or loop detection fires

## Key Data Flow

```
1. dispatch task create "fix bug" --workflow coding-easy
   → creates task dir + first job in jobs/pending/
   → sends "new_task" to pipe

2. Foreman receives pipe event
   → reads pending job
   → checks model lock (is local-27b free?)
   → locks model, moves job to active/
   → calls runner.dispatchJob(cfg, job, systemPrompt, callbacks)

3. Pi SDK session runs
   → model uses read/edit/write/bash tools
   → model calls task_done({ summary: "wrote the spec" })
   → runner.onDone callback fires

4. Foreman onDone callback
   → writes result, unlocks model, moves to done/
   → loads workflow → determines next step
   → creates next job in pending/
   → dispatchPending() checks queue → start if model free
```

## Workflow Steps

Defined in `~/.dispatch/workflows/<name>.json`. Each step has:
- `role` — which agent prompt to load (`agents/<role>.md`)
- `model` — which model (`local-9b`, `local-27b`) 
- `branch` — keyword routing (`ACCEPTED→ready`, `DENIED→fix`)
- `maxIterations` — loop limit before escalation to human

Per-step prompts in `~/.dispatch/workflows/<name>/<step>.prompt.md`.

## State: Everything is Files

```
~/.dispatch/
  jobs/pending/*.json       — queued, waiting for model
  jobs/active/*.json        — running in a Pi SDK session
  jobs/done/*.json          — completed, result written
  jobs/failed/*.json        — aborted, reason written
  state.json                — model locks + task progress
  artifacts/<task-id>/      — files passed between steps
  sessions/                 — Pi SDK JSONL session files
```

## Pipe Protocol

CLI → foreman via named FIFO (`/tmp/dispatch.pipe`). JSON messages, one per line:

```typescript
{ type: "done",    jobId, taskId, message, artifacts }
{ type: "fail",    jobId, reason }
{ type: "ask",     jobId, question, escalate }
{ type: "answer",  jobId, message }
{ type: "new_task", taskId }
```

## System Prompt Construction

`prompts.ts` builds the full system prompt for each agent session:
1. `~/.dispatch/agents/system.md` (shared base)
2. `~/.dispatch/agents/<role>.md` (role identity — coder, reviewer, etc.)
3. `~/.dispatch/skill/SKILL.md` (task context + tools reference)
4. Hardcoded footer: task_done/ask/fail instructions

**Note:** The skill file should NOT instruct agents to use bash dispatch commands. They use `task_done`, `task_ask`, `task_fail` tools directly.

## Making Changes

- **Fix tool interception?** → `runner.ts` — wrappedEditTool or the loop detection block
- **New workflow step type?** → `workflows.ts` validation + `foreman.ts` dispatch logic
- **New CLI command?** → Add `src/cli/<name>.ts` + wire in `index.ts`
- **New notification channel?** → `escalate.ts`
- **Different model for a step?** → update `config.json` models + workflow JSON

## Build

```bash
npm install
npm run build         # tsc → dist/
node dist/index.js    # run
```

## TypeScript Notes

- Module system: `"module": "Node16"` — use `.js` extensions in imports even for `.ts` files
- Types: `@mariozechner/pi-coding-agent` exports `ToolDefinition`, `createAgentSession`, etc.
- Parameters in `ToolDefinition.execute` are typed via TypeBox schema — use `as` casts if needed
- `session.subscribe()` events are loosely typed — use `// @ts-ignore` for `toolName`/`toolInput` access
