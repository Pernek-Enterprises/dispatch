/**
 * prompts.ts — System prompt loading (shared between foreman and CLI).
 * Exported as loadSystemPromptPublic to avoid circular imports.
 */
import * as fs from "fs";
import * as path from "path";
import { ROOT } from "./config.js";

export function loadSystemPromptPublic(agentName: string): string {
  const agentsDir = path.join(ROOT, "agents");
  const parts: string[] = [];

  const systemPath = path.join(agentsDir, "system.md");
  if (fs.existsSync(systemPath)) parts.push(fs.readFileSync(systemPath, "utf8").trim());

  if (agentName && agentName !== "stefan") {
    const agentPath = path.join(agentsDir, `${agentName}.md`);
    if (fs.existsSync(agentPath)) parts.push(fs.readFileSync(agentPath, "utf8").trim());
  }

  const skillPath = path.join(ROOT, "skill", "SKILL.md");
  if (fs.existsSync(skillPath)) parts.push(fs.readFileSync(skillPath, "utf8").trim());

  parts.push(`
## Completing Your Work
When ALL your work is done, call the \`task_done\` tool with a brief summary.
If you are blocked and need information, call \`task_ask\` (set escalate=true to reach the human).
If you cannot complete the task, call \`task_fail\` with a reason.

**Do NOT use bash to call dispatch commands. Use the task_* tools instead.**
If any tool returns "identical content" or "no changes made", that means the file already has the correct content — treat it as success and proceed to task_done.
`.trim());

  return parts.join("\n\n");
}
