# Dispatch TypeScript Rewrite — Spec

**Status:** Draft  
**Date:** 2026-03-06  
**Replaces:** Go implementation in `cmd/`, `internal/`  
**Goal:** Rewrite dispatch in TypeScript using the Pi SDK directly, fixing structural brittleness at the right layer.

---

## Why

The Go implementation shells out to `pi --print` as a subprocess. This means:
- Tool results are opaque — we can't intercept or fix them
- The `edit` tool returns `isError: true` for identical content, causing agent loops
- Agents call `dispatch done` via bash; if they lose track, nothing advances the pipeline
- No way to detect/break tool call loops without prompt hacks

With the Pi SDK (`@mariozechner/pi-coding-agent`) we own the tool execution loop. We can:
- Intercept and normalize tool results before the model sees them
- Replace bash-based `dispatch done` with a first-class `task_done` tool the model calls directly
- Detect tool call loops and terminate gracefully
- Inject context (job ID, task ID, artifact paths) without env vars

---

## What Stays the Same

- **File layout**: `~/.dispatch/` directory structure unchanged
- **Workflow JSON format**: `workflows/<name>.json` + `workflows/<name>/<step>.prompt.md`
- **Job file format**: `jobs/{pending,active,done,failed}/<id>.json` + `.prompt.md` + `.result.md`
- **State file**: `state.json` (model locks, task state)
- **CLI interface**: `dispatch task create`, `dispatch done`, `dispatch answer`, `dispatch foreman` — same commands, same flags
- **Escalation**: `openclaw message send --target <id>` — unchanged
- **Config format**: `config.json` — unchanged

---

## Architecture

```
dispatch (TypeScript, Node.js)
├── foreman.ts          — event loop, state machine, job scheduler
├── runner.ts           — Pi SDK session manager (replaces pi/pi.go)
├── tools.ts            — custom tool definitions (task_done, task_ask, task_fail)
├── jobs.ts             — job file I/O (port of internal/jobs)
├── workflows.ts        — workflow loading + routing (port of internal/workflows)
├── state.ts            — state.json r/w (port of internal/state)
├── config.ts           — config.json loading (port of internal/config)
├── escalate.ts         — openclaw message send notifications (port of internal/escalate)
├── pipe.ts             — named pipe IPC (port of internal/pipe)
└── cli/
    ├── task.ts         — dispatch task create/list/show
    ├── done.ts         — dispatch done
    ├── answer.ts       — dispatch answer
    ├── ask.ts          — dispatch ask
    ├── fail.ts         — dispatch fail
    └── setup.ts        — dispatch setup
```

---

## The Core Fix: Pi SDK Runner

### Current (Go subprocess)

```go
cmd := exec.Command("pi", "--print", "--model", model, prompt)
cmd.Start() // fire and forget
```

The agent calls `bash("dispatch done --job $DISPATCH_JOB_ID \"result\"")` to signal completion. If it gets confused, it never calls done.

### New (Pi SDK)

```typescript
import { createAgentSession, ModelRegistry, SessionManager, AuthStorage } from "@mariozechner/pi-coding-agent";

async function runJob(job: Job): Promise<void> {
  const { session } = await createAgentSession({
    sessionManager: SessionManager.inMemory(),
    authStorage: AuthStorage.create(),
    modelRegistry: new ModelRegistry(AuthStorage.create()),
    model: resolveModel(job.model),
    systemPrompt: loadSystemPrompt(job.agent),
    tools: buildTools(job),  // custom tools injected here
  });

  await session.prompt(job.prompt);
  // session ends when model calls task_done tool
}
```

### Custom Tools

Three tools replace the bash-based dispatch CLI:

#### `task_done`
```typescript
{
  name: "task_done",
  description: "Call this when your work is complete. Provide a summary of what you did.",
  parameters: {
    summary: { type: "string", description: "Brief summary of completed work" }
  },
  execute: async ({ summary }) => {
    await advanceJob(job.id, summary);
    session.abort(); // end the session
    return { ok: true };
  }
}
```

#### `task_ask`
```typescript
{
  name: "task_ask",
  description: "Ask a question when you need information to proceed. Set escalate=true to send to the human.",
  parameters: {
    question: { type: "string" },
    escalate: { type: "boolean", default: false }
  },
  execute: async ({ question, escalate }) => {
    await handleAsk(job, question, escalate);
    // session pauses — foreman will resume with answer via task_resume
    return { ok: true, waiting: true };
  }
}
```

#### `task_fail`
```typescript
{
  name: "task_fail",
  description: "Call this if you cannot complete the task. Explain what went wrong.",
  parameters: {
    reason: { type: "string" }
  },
  execute: async ({ reason }) => {
    await failJob(job.id, reason);
    session.abort();
    return { ok: true };
  }
}
```

### Tool Wrappers (fixing bad behavior)

Wrap Pi's built-in tools before the model sees results:

```typescript
session.on("tool_result", (toolName, result) => {
  // Fix: edit returns isError for identical content — normalize to success
  if (toolName === "edit" && result.isError && 
      result.content?.includes("identical content")) {
    return { isError: false, content: "File already has the correct content." };
  }
  return result; // pass through
});
```

### Loop Detection

```typescript
const recentCalls: string[] = [];

session.on("tool_call", (call) => {
  const sig = `${call.name}:${JSON.stringify(call.arguments)}`;
  recentCalls.push(sig);
  if (recentCalls.length > 20) recentCalls.shift();
  
  // Same tool call repeated 5+ times = loop
  const last5 = recentCalls.slice(-5);
  if (last5.every(c => c === sig)) {
    log.warn(`Tool call loop detected in job ${job.id}: ${call.name}`);
    session.abort();
    failJob(job.id, `Tool call loop: ${call.name} called identically 5 times`);
  }
});
```

---

## Foreman Event Loop

Port of `foreman.go` — same logic, TypeScript:

```typescript
async function startForeman() {
  const cfg = await loadConfig();
  const st = await loadState();

  // Named pipe for CLI → foreman IPC (same protocol)
  await startPipeListener(cfg.pipePath, (msg) => handleEvent(cfg, st, msg));

  // Poll loop
  setInterval(async () => {
    await healthCheck(cfg, st);
    await dispatchPending(cfg, st);
    await st.save();
  }, cfg.pollIntervalMs);

  await dispatchPending(cfg, st);
  log.info("Foreman running");
}
```

Active Pi sessions are held in a `Map<jobId, AbortController>` — no subprocess reaping needed.

---

## Model Resolution

Pi's SDK takes a provider+model. Dispatch config references models like `local-9b/Qwen3.5-9B-Q4_K_M.gguf`. We map these via `~/.dispatch/config.json`:

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

The runner resolves `job.model` → SDK model config before creating the session.

---

## Prompt Changes

Since agents no longer call `dispatch done` via bash, the skill/system prompt changes:

**Remove:** Instructions about `dispatch done --job $DISPATCH_JOB_ID`  
**Add:** Instructions about calling `task_done`, `task_ask`, `task_fail` tools

```markdown
## Completing Your Work
When you have finished all your work, call the `task_done` tool with a summary.
If you are blocked or need information, call `task_ask`.
If you cannot complete the task, call `task_fail` with the reason.

Do NOT call bash to run dispatch commands. Use the task_* tools directly.
```

---

## CLI Commands

Same interface as Go CLI. Built with a minimal arg parser (no heavy framework needed).

```
dispatch foreman              — start foreman daemon
dispatch task create "..."    — create a task
dispatch task list            — list tasks
dispatch task show <id>       — show task details
dispatch done --job <id> "…"  — mark job done (CLI → pipe → foreman)
dispatch answer --job <id> "…"— answer a human job
dispatch ask --job <id> "…"   — ask a question
dispatch fail --job <id> "…"  — mark job failed
dispatch setup                — interactive setup wizard
dispatch sessions             — list Pi sessions
```

CLI commands communicate with the foreman via the named pipe (same JSON protocol as today). No change to external interface.

---

## File Layout

```
dispatch/              (source repo, ~/dispatch)
├── package.json
├── tsconfig.json
├── src/
│   ├── index.ts       — CLI entrypoint
│   ├── foreman.ts
│   ├── runner.ts
│   ├── tools.ts
│   ├── jobs.ts
│   ├── workflows.ts
│   ├── state.ts
│   ├── config.ts
│   ├── escalate.ts
│   ├── pipe.ts
│   └── cli/
│       ├── task.ts
│       ├── done.ts
│       ├── answer.ts
│       ├── ask.ts
│       ├── fail.ts
│       └── setup.ts
└── dist/              — compiled output

~/.dispatch/           (live install — unchanged structure)
```

### Build & Install

```bash
cd ~/dispatch
npm install
npm run build
cp dist/index.js /usr/local/bin/dispatch
# or: node dist/index.js aliased as dispatch
```

---

## Dependencies

```json
{
  "dependencies": {
    "@mariozechner/pi-coding-agent": "latest"
  },
  "devDependencies": {
    "typescript": "^5",
    "@types/node": "^22"
  }
}
```

No other runtime deps. Node.js stdlib for file I/O, named pipes, process management.

---

## Migration

1. Build TS version alongside Go version
2. Run both against the same `~/.dispatch/` — file formats identical
3. Swap `/usr/local/bin/dispatch` when TS version passes the same end-to-end test
4. Delete Go source

Existing jobs in `pending/active/done/failed` work without changes. Existing workflows work without changes (only skill prompt updates needed to switch from `dispatch done` bash to `task_done` tool).

---

## What This Fixes

| Problem | Go fix | TS fix |
|---|---|---|
| `edit` loop on identical content | Prompt hint (fragile) | Tool wrapper returns success |
| Agent forgets to call `dispatch done` | Session timeout + manual rescue | Session ends only via `task_done` tool — impossible to forget |
| Loop detection | None | Built-in, terminates cleanly |
| Model lies to agent | Cannot intercept | `tool_result` event hook |
| Subprocess reaping / zombie Pi | `cmd.Wait()` in goroutine | In-process session, no subprocess |
| Debug visibility | Log file per job | Full session events, structured |

---

## Out of Scope

- LLM direct calls (`llm.go` for triage/parse/answer steps) — keep as simple HTTP fetch to llama-server, no Pi needed
- Workflow format changes — out of scope, keep backward compat
- UI / dashboard — not now
