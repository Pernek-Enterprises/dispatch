# Dispatch

Local-first, file-based agent orchestration. Route tasks through coding workflows on local LLMs — no database, no cloud, no heavyweight runtime.

## Architecture

```
Task → Foreman → Model Queue → Pi process → dispatch done → next step
                                   │
                              read/write/edit/bash
```

- **Dispatch foreman** — orchestration daemon. Workflows, model queues, state management.
- **Pi** — lightweight coding agent. Ephemeral processes with 4 tools. Starts in milliseconds.
- **llama-server** — local LLM inference via Vulkan.
- **OpenClaw** — optional communication bridge for human escalation (Discord/Telegram).

## Quick Start

```bash
git clone https://github.com/Pernek-Enterprises/dispatch.git
cd dispatch

# Build (requires Go 1.21+)
make build

# Interactive setup — walks through all config
./dispatch setup

# Or configure manually
cp config.json.example config.json
cp models.json.example models.json
cp agents/coder.md.example agents/coder.md
cp agents/reviewer.md.example agents/reviewer.md

# Start the foreman
./dispatch foreman

# Create a task
./dispatch task create "Fix the auth redirect bug" --workflow coding-easy
```

## How It Works

1. **Create a task** with a workflow (e.g. `coding-easy`)
2. **Foreman creates the first job** (spec step, model: 27b)
3. **Checks model queue** — is 27b free? If yes, spawn Pi
4. **Pi does the work** — reads code, writes spec, commits
5. **Pi signals done** — `dispatch done --job <id> "wrote the spec"`
6. **Foreman advances** — creates next job (code step, model: 9b)
7. **Repeat** until workflow reaches `ready` (human review)

### Branching

Review steps output `ACCEPTED` or `DENIED`. Foreman does a simple string check — no LLM interpretation:

```
review result contains "ACCEPTED" → next step: ready
review result contains "DENIED"  → next step: fix → loops back to review
```

Max 3 review loops (configurable), then auto-escalates to human.

### Escalation

When an agent is stuck or a job fails, foreman notifies you:

```
Pi → dispatch ask --escalate "question"
   → Foreman → OpenClaw → Discord/Telegram → You
   → dispatch answer --job <id> "do X"
   → Pi resumes
```

## CLI Commands

```bash
# Agent commands (used by Pi during work)
dispatch done --job <id> --root ~/.dispatch "summary"
dispatch done --job <id> --root ~/.dispatch --artifact file.md "summary"
dispatch ask --job <id> --root ~/.dispatch "question"
dispatch ask --job <id> --root ~/.dispatch --escalate "need human"
dispatch fail --job <id> --root ~/.dispatch "reason"

# Human commands
dispatch answer --job <id> --root ~/.dispatch "answer"

# Management
dispatch task create "description" --workflow coding-easy
dispatch task list
dispatch task show <id>
dispatch workflow list
dispatch workflow show <name>
dispatch workflow validate <name>
dispatch workflow create <name>
dispatch setup
dispatch status
dispatch foreman
```

## Configuration

All config uses `.example` templates. Copy and customize for your installation.

### `models.json` — Model endpoints

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

### `config.json` — System settings

```json
{
  "pollIntervalMs": 30000,
  "pipePath": "/tmp/dispatch.pipe",
  "maxLoopIterations": 3,
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

### Workflows — `workflows/<name>.json`

Each workflow is a JSON step graph with per-step prompt templates:

```
workflows/coding-easy.json          ← step graph
workflows/coding-easy/spec.prompt.md  ← what the agent sees
workflows/coding-easy/code.prompt.md
workflows/coding-easy/review.prompt.md
```

### Role Prompts — `agents/<role>.md`

Roles give agents personality. A `coder` is creative, a `reviewer` is adversarial:

```
agents/system.md      ← shared across all roles
agents/coder.md       ← coder identity
agents/reviewer.md    ← reviewer identity
```

### Pi Skill — `skill/SKILL.md`

Teaches Pi agents how to use `dispatch done/ask/fail`. Loaded automatically on every invocation.

## File Structure

```
~/.dispatch/
├── dispatch              ← Go binary (foreman + CLI)
├── config.json           ← settings
├── models.json           ← model endpoints
├── state.json            ← model locks, task progress
├── skill/SKILL.md        ← Pi skill (dispatch commands)
├── agents/              ← role identities
├── workflows/            ← workflow definitions + prompts
├── jobs/
│   ├── pending/          ← queued
│   ├── active/           ← in progress
│   ├── done/             ← completed
│   └── failed/           ← errored
├── artifacts/<task-id>/  ← outputs passed between steps
└── logs/                 ← foreman + Pi process logs
```

## Key Principles

- **Files are state** — `ls jobs/` tells you everything
- **Deterministic foreman** — no LLM in the scheduler, just code
- **Event-driven** — CLI notifies foreman via named pipe, instant reaction
- **Model-aware** — one job per GPU, no contention
- **Lightweight** — Pi processes start in milliseconds, exit when done
- **Nothing hardcoded** — all config from `.example` templates

## Requirements

- Go 1.21+ (build only)
- [Pi](https://github.com/badlogic/pi-mono) coding agent
- llama-server or any OpenAI-compatible endpoint
- OpenClaw (optional, for escalation notifications)

## Docs

- [SPEC.md](./SPEC.md) — full architecture and design decisions
- [AGENTS.md](./AGENTS.md) — codebase guide for AI agents

## License

MIT
