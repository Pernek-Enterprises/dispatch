# Dispatch

Local-first, file-based agent orchestration system.

Route tasks to local LLMs, manage model contention, coordinate agents — all through plain files. No database, no API server, no format contracts.

## Quick Start

```bash
git clone https://github.com/Pernek-Enterprises/dispatch.git
cd dispatch

# Configure for your installation
cp config.json.example config.json
cp models.json.example models.json
cp agents.json.example agents.json

# Edit each file for your setup:
# - models.json: your LLM endpoints and provider names
# - agents.json: your agents and their capabilities
# - config.json: poll interval, pipe path, timeouts

# Run the foreman
node foreman.js
```

## How it works

```
Task arrives → triage job (LLM) → work job (agent) → parse result (LLM) → done
```

Everything goes through the job queue — including LLM calls for triage and parsing. The foreman is a deterministic loop that shuffles files and manages locks. Intelligence comes from LLM jobs, not from the scheduler.

## Configuration

All configuration lives in JSON files. Nothing is hardcoded.

### `models.json` — Define your models

```json
{
  "9b": {
    "name": "Qwen3.5-9B",
    "provider": "local/qwen-9b",
    "endpoint": "http://localhost:8081/v1"
  },
  "27b": {
    "name": "Qwen3.5-27B",
    "provider": "local/qwen-27b",
    "endpoint": "http://localhost:8080/v1"
  }
}
```

- `provider`: passed to session backend when spawning agent sessions
- `endpoint`: OpenAI-compatible API URL for direct LLM calls

### `agents.json` — Define your agents

```json
{
  "kit": {
    "role": "coder",
    "capabilities": ["spec", "code", "fix"]
  }
}
```

### `config.json` — System settings

```json
{
  "pollIntervalMs": 30000,
  "pipePath": "/tmp/dispatch.pipe",
  "maxLoopIterations": 3,
  "sessionBackend": "openclaw"
}
```

## Agent CLI

Agents communicate with the foreman using 3 commands:

```bash
dispatch done "summary of work"           # mark step complete
dispatch done --artifact spec.md "done"   # complete with artifact
dispatch ask "question for help"           # ask a question
dispatch fail "reason it failed"           # report failure
```

## File Structure

```
~/dispatch/
├── foreman.js          ← deterministic loop (no LLM)
├── bin/dispatch.js     ← agent CLI
├── config.json         ← your settings
├── models.json         ← your model endpoints
├── agents.json         ← your agents
├── workflows/          ← markdown workflow templates
├── jobs/
│   ├── pending/        ← queued
│   ├── active/         ← in progress
│   ├── done/           ← completed
│   └── failed/         ← errored
├── artifacts/          ← outputs passed between steps
└── logs/
```

## Key Principles

- **One file to change** for workflow updates
- **Zero format contracts** between dispatch and agents
- **No silent failures** — stuck jobs get detected
- **Full visibility** — `ls jobs/` tells you everything
- **Model-aware** — no two jobs fight for the same GPU
- **Nothing hardcoded** — all config from JSON files

## Status

Early development. See [SPEC.md](./SPEC.md) for the full PRD.

## License

MIT
