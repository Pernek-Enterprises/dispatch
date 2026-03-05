# coding-easy

Simple coding tasks — bug fixes, small features, straightforward changes.

## Graph

spec → code → review → [ACCEPTED: ready] [DENIED: fix]
fix → review

## Steps

### spec
agent: kit
model: 27b
timeout: 10m
artifacts_in: [task]
artifacts_out: [spec.md]

Analyze this task and write a technical spec:
- Root cause (for bugs) or requirements (for features)
- Files likely affected
- Approach and estimated complexity

---

### code
agent: kit
model: 9b
timeout: 30m
artifacts_in: [spec.md]
artifacts_out: [diff.patch]

Implement the spec. Create a branch (`agent/<task-id>-<slug>`).
Write clean code, commit with clear messages, push.

---

### review
agent: hawk
model: 9b
timeout: 15m
artifacts_in: [spec.md, diff.patch]
artifacts_out: [review.md]
branch: ACCEPTED | DENIED
max_iterations: 3

Review this implementation against the spec.
Check correctness, edge cases, security, code quality.

End your review with exactly one of:
- `ACCEPTED` — code is good to merge
- `DENIED` — changes needed (explain what)

---

### fix
agent: kit
model: 27b
timeout: 20m
artifacts_in: [review.md, spec.md]
artifacts_out: [diff.patch]
next: review

Address the review feedback. Fix the issues identified, push updates.

---

### ready
agent: stefan
model: null
timeout: none

PR ready for human review and merge.
