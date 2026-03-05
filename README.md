# Dispatch

Local-first, file-based agent orchestration system.

Route tasks to local LLMs, manage model contention, coordinate agents — all through plain files. No database, no API server, no format contracts.

## How it works

```
Task arrives → triage job (9B) → work job (27B) → parse result (9B) → done
```

Everything goes through the job queue — including LLM calls for triage and parsing. The foreman is a deterministic loop that shuffles files and manages locks. Intelligence comes from LLM jobs, not from the scheduler.

## File structure

```
~/dispatch/
├── foreman.js          ← deterministic loop (no LLM)
├── state.json          ← lock table (models + agents)
├── models.json         ← endpoint config
├── config.json         ← foreman settings
├── workflows/          ← markdown workflow templates
├── jobs/
│   ├── pending/        ← ready to pick up
│   ├── active/         ← in progress
│   ├── done/           ← completed
│   └── failed/         ← timed out or errored
└── logs/
```

## Key design principles

- **One file to change** for workflow updates
- **Zero format contracts** between dispatch and agents
- **No silent failures** — stuck jobs get detected and handled
- **Full visibility** — `ls jobs/` tells you everything
- **Model-aware** — no two jobs fight for the same GPU
- **LLM calls are jobs** — triage, parsing, answering questions all go through the same queue

## Status

Early development. See [SPEC.md](./SPEC.md) for the full PRD.

## License

MIT
