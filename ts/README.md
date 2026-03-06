# Dispatch

Local-first, file-based agent orchestration. Route tasks through coding workflows on local LLMs — no database, no cloud, no heavyweight runtime.

**This is the TypeScript rewrite** of the original Go implementation. It uses the [Pi SDK](https://github.com/badlogic/pi-mono/tree/main/packages/coding-agent) directly instead of shelling out to a `pi` subprocess, giving full control over tool execution, loop detection, and job signalling.

## Why TypeScript

The Go version shelled out to `pi --print` and relied on the agent calling `bash("dispatch done --job $ID")` to signal completion. When the 9B model got confused, it would loop indefinitely with no way to intervene.

The TypeScript version uses the Pi SDK in-process:
- **Tool result interception** — `edit` returning "identical content" is normalized to success
- **Loop detection** — same tool call repeated 5x → session aborts cleanly
- **`task_done` / `task_ask` / `task_fail` tools** — model calls a tool to signal completion, not bash. Impossible to lose.
- **No subprocess reaping** — sessions are in-memory, no zombie Pi processes

## Architecture

```
Task → Foreman → Model Queue → Pi SDK session → task_done tool → next step
                                    │
                      read/edit/write/bash + task_done/ask/fail
```

- **Foreman** (`foreman.ts`) — orchestration event loop, workflow state machine
- **Runner** (`runner.ts`) — Pi SDK session manager, tool interception, loop detection
- **Pi SDK** — `@mariozechner/pi-coding-agent`, runs models via OpenAI-compatible endpoints
- **llama-server** — local LLM inference via Vulkan/HIP
- **OpenClaw** — optional human escalation (Discord/Telegram)

## Quick Start

```bash
git clone https://github.com/Pernek-Enterprises/dispatch.git ~/dispatch-ts
cd ~/dispatch-ts

# Install dependencies
npm install

# Build
npm run build

# Interactive setup
node dist/index.js setup

# Start the foreman
DISPATCH_ROOT=~/.dispatch node dist/index.js foreman

# Create a task
DISPATCH_ROOT=~/.dispatch node dist/index.js task create "Fix the auth redirect bug" --workflow coding-easy
```

### Install as system binary

```bash
npm run build
sudo cp dist/index.js /usr/local/bin/dispatch
# Add shebang if needed: echo '#!/usr/bin/env node' | cat - dist/index.js > /tmp/d && mv /tmp/d /usr/local/bin/dispatch
```

## Prerequisites

- Node.js v22+
- [Pi](https://github.com/badlogic/pi-mono) — coding agent (`npm install -g @mariozechner/pi-coding-agent`)
- llama-server (or any OpenAI-compatible endpoint) — for local LLM inference
- `~/.pi/agent/models.json` — must have your local model providers configured
- OpenClaw (optional) — for human escalation via Discord/Telegram

## Model Configuration

Models are configured in two places:

**`~/.dispatch/config.json`** — maps model aliases to providers:
```json
{
  "models": {
    "local-9b": {
      "provider": "openai-completions",
      "endpoint": "http://localhost:8081/v1",
      "model": "Qwen3.5-9B-Q4_K_M.gguf"
    },
    "local-27b": {
      "provider": "openai-completions",
      "endpoint": "http://localhost:8080/v1",
      "model": "Qwen3.5-27B-Q4_K_M.gguf"
    }
  }
}
```

**`~/.pi/agent/models.json`** — Pi SDK's model registry (same providers, Pi resolves credentials here).

Workflows reference models as `local-9b/Qwen3.5-9B-Q4_K_M.gguf` — the foreman splits on `/` to get provider + model ID.

## How It Works

1. **Create a task** — `dispatch task create "fix the bug" --workflow coding-easy`
2. **Foreman creates the first job** (spec step, model: `local-27b`)
3. **Checks model lock** — is `local-27b` free? If yes, start Pi SDK session
4. **Pi does the work** — reads code, writes files, uses tools
5. **Pi calls `task_done`** — a custom tool injected into every session
6. **Runner signals foreman** — job moves to `done/`, workflow advances
7. **Repeat** until workflow reaches `ready` (human review)

### Loop Detection

Every tool call signature is tracked. If the same tool is called with identical parameters 5 times in a row:
1. Session is aborted
2. Job moves to `failed/`
3. Human notified via Discord/Telegram

### Edit Tool Fix

The Pi SDK's built-in `edit` tool returns `isError: true` when the file already has the correct content. The runner wraps this tool and returns a success response instead, stopping the most common loop pattern.

## CLI

```bash
dispatch foreman                          # Start daemon
dispatch task create "desc" [--workflow]  # Create task
dispatch task list                        # List all tasks
dispatch task show <id>                   # Show task + jobs
dispatch done --job <id> "summary"        # Mark job complete (from within sessions)
dispatch answer --job <id> "text"         # Answer a human job
dispatch ask --job <id> "question"        # Ask a question
dispatch fail --job <id> "reason"         # Mark job failed
dispatch setup                            # Interactive setup
```

All commands communicate with the running foreman via named pipe (`/tmp/dispatch.pipe` by default).

## File Structure

Source:
```
dispatch-ts/
├── src/
│   ├── index.ts          CLI entrypoint + command routing
│   ├── foreman.ts        Orchestration event loop + workflow state machine
│   ├── runner.ts         Pi SDK session manager (the core)
│   ├── config.ts         Config loading + model resolution
│   ├── jobs.ts           Job file I/O
│   ├── workflows.ts      Workflow JSON loading + routing
│   ├── state.ts          state.json management
│   ├── pipe.ts           Named FIFO pipe IPC
│   ├── escalate.ts       OpenClaw notifications
│   ├── prompts.ts        System prompt builder (shared)
│   ├── log.ts            Logger
│   └── cli/
│       ├── task.ts       dispatch task [create|list|show]
│       ├── done.ts       dispatch done
│       ├── answer.ts     dispatch answer
│       ├── ask.ts        dispatch ask
│       ├── fail.ts       dispatch fail
│       └── setup.ts      dispatch setup
├── package.json
└── tsconfig.json
```

Live install (unchanged from Go version):
```
~/.dispatch/
├── config.json           Settings (pollIntervalMs, pipePath, models, notifications)
├── state.json            Model locks + task progress
├── skill/SKILL.md        Agent instructions (task_done/ask/fail tools)
├── agents/               Role identity prompts
├── workflows/            Workflow definitions + step prompts
├── jobs/{pending,active,done,failed}/
├── artifacts/<task-id>/  Files passed between steps
├── sessions/             Pi SDK session JSONL files
└── logs/                 Foreman + session logs
```

## Environment

| Variable | Default | Description |
|---|---|---|
| `DISPATCH_ROOT` | `~/.dispatch` | Live install directory |
| `DISPATCH_JOB_ID` | — | Current job ID (set by foreman, used by `done`/`ask`/`fail`) |

## Key Differences from Go Version

| | Go | TypeScript |
|---|---|---|
| Pi invocation | `pi --print` subprocess | Pi SDK in-process |
| Completion signal | `bash("dispatch done ...")` | `task_done` tool call |
| Edit loop fix | Prompt hint (fragile) | Tool wrapper (correct) |
| Loop detection | None | Built-in, 5x identical calls → abort |
| Debug visibility | Log file per session | Full session events via `session.subscribe()` |
| Config | `config.json` + `models.json` | Single `config.json` with `models` section |

## License

MIT
