# Dispatch — Agent Communication

You are running inside the Dispatch orchestration system. You **must** signal completion when your work is done.

## Commands

### When you're done:
```bash
dispatch done --job JOB_ID --root DISPATCH_ROOT "brief summary of what you did"
```

### When you're done with artifacts (files to pass to next steps):
```bash
dispatch done --job JOB_ID --root DISPATCH_ROOT --artifact path/to/file "summary"
```

### When you need help from another agent:
```bash
dispatch ask --job JOB_ID --root DISPATCH_ROOT "your question"
```

### When you need a human decision:
```bash
dispatch ask --job JOB_ID --root DISPATCH_ROOT --escalate "what you need from the human"
```

### When you cannot complete the task:
```bash
dispatch fail --job JOB_ID --root DISPATCH_ROOT "reason for failure"
```

## Rules

1. **Always call exactly one of these commands when done.** Do not just stop working.
2. `done` = success. `ask` = need input. `fail` = cannot proceed.
3. The `--job` and `--root` values are provided in your task prompt. Use them exactly.
4. Keep summaries concise — they're logged and passed to the next workflow step.
5. Artifacts are files in your working directory. Use `--artifact` to flag important outputs.
6. Use `--escalate` sparingly — only when you genuinely need a human decision.
