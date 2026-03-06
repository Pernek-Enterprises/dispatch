# Dispatch

Local-first, file-based agent orchestration. Route tasks through coding workflows on local LLMs — no database, no cloud, no heavyweight runtime.

> **Status:** The TypeScript rewrite (`dispatch-ts/`) is the active development branch. This Go implementation is the stable reference. See [SPEC-TS-REWRITE.md](./SPEC-TS-REWRITE.md) for migration details.

## Architecture

```
Task → Foreman → Model Queue → Pi process → task_done tool → next step
                                   │
                              read/write/edit/bash
```

- **Dispatch foreman** — orchestration daemon. Workflows, model queues, state management.
- **Pi** — lightweight coding agent. Runs via Pi SDK (TS) or subprocess (Go).
- **llama-server** — local LLM inference via Vulkan/HIP.
- **OpenClaw** — optional communication bridge for human escalation (Discord/Telegram).

## Quick Start (Go binary)

```bash
git clone https://github.com/Pernek-Enterprises/dispatch.git
cd dispatch

# Build (requires Go 1.21+)
make build

# Interactive setup
./dispatch setup

# Start the foreman
./dispatch foreman

# Create a task
./dispatch task create "Fix the auth redirect bug" --workflow coding-easy
```

## How It Works

1. **Create a task** with a workflow (e.g. `coding-easy`)
2. **Foreman creates the first job** (spec step, model: `local-27b`)
3. **Checks model queue** — is `local-27b` free? If yes, spawn Pi
4. **Pi does the work** — reads code, writes files, uses tools
5. **Pi signals done** — calls `task_done` tool (TS) or `dispatch done` via bash (Go)
6. **Foreman advances** — creates next job (code step, model: `local-9b`)
7. **Repeat** until workflow reaches `ready` (human review)

### Branching

Review steps output `ACCEPTED` or `DENIED`. Foreman does a simple string match:
```
result contains "ACCEPTED" → next step: ready
result contains "DENIED"   → next step: fix → loops back to review
```
Max 3 review loops (configurable), then auto-escalates to human.

### Escalation

```
Pi → task_ask(escalate=true, "question")
   → Foreman → OpenClaw → Discord/Telegram → You
   → dispatch answer --job <id> "do X"
   → Pi resumes
```

## CLI Commands

```bash
# Workflow management (Pi uses these tools, not bash)
task_done({ summary: "..." })      # signal completion
task_ask({ question: "..." })      # ask question
task_fail({ reason: "..." })       # report failure

# Human commands
dispatch answer --job <id> "answer"

# Management
dispatch task create "description" --workflow coding-easy
dispatch task list
dispatch task show <id>
dispatch foreman
dispatch setup
```

## Configuration

### `config.json`

```json
{
  "pollIntervalMs": 30000,
  "pipePath": "/tmp/dispatch.pipe",
  "maxLoopIterations": 3,
  "notifications": {
    "escalation": "discord",
    "target": "1475634736834547938"
  },
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

### Workflows — `workflows/<name>.json`

```
workflows/coding-easy.json            ← step graph
workflows/coding-easy/spec.prompt.md  ← what the agent sees
workflows/coding-easy/code.prompt.md
workflows/coding-easy/review.prompt.md
```

### Role Prompts — `agents/<role>.md`

```
agents/system.md      ← shared base
agents/coder.md       ← coder identity
agents/reviewer.md    ← reviewer identity
```

### Pi Skill — `skill/SKILL.md`

Teaches Pi agents to use `task_done`/`task_ask`/`task_fail` tools. Loaded automatically on every session.

## File Structure

```
~/.dispatch/
├── config.json           ← settings + model registry
├── state.json            ← model locks, task progress
├── skill/SKILL.md        ← Pi tool instructions
├── agents/               ← role identity prompts
├── workflows/            ← workflow definitions + step prompts
├── jobs/
│   ├── pending/          ← queued
│   ├── active/           ← in progress
│   ├── done/             ← completed
│   └── failed/           ← errored
├── artifacts/<task-id>/  ← files passed between steps
├── sessions/             ← Pi SDK session JSONL logs
└── logs/                 ← foreman + session logs
```

## Key Principles

- **Files are state** — `ls jobs/` tells you everything
- **Deterministic foreman** — no LLM in the scheduler, just code
- **Event-driven** — CLI notifies foreman via named pipe
- **Model-aware** — one job per GPU, no contention
- **Tool-first completion** — `task_done` tool, not bash, signals job done

## Requirements

- Go 1.21+ (for building the binary)
- [Pi](https://github.com/badlogic/pi-mono) coding agent
- llama-server or any OpenAI-compatible endpoint
- OpenClaw (optional, for escalation notifications)

## Docs

- [SPEC.md](./SPEC.md) — architecture and design decisions
- [SPEC-TS-REWRITE.md](./SPEC-TS-REWRITE.md) — TypeScript migration plan
- [AGENTS.md](./AGENTS.md) — codebase guide for AI agents

## License

MIT
