#!/usr/bin/env node
/**
 * dispatch — Agent orchestration harness (TypeScript rewrite)
 * Uses Pi SDK directly for full tool interception and loop detection.
 */

import { taskCmd }   from "./cli/task.js";
import { doneCmd }   from "./cli/done.js";
import { answerCmd } from "./cli/answer.js";
import { askCmd }    from "./cli/ask.js";
import { failCmd }   from "./cli/fail.js";
import { setupCmd }  from "./cli/setup.js";
import { startForeman } from "./foreman.js";

const [,, cmd, ...args] = process.argv;

switch (cmd) {
  case "foreman":  startForeman().catch((e) => { console.error(e); process.exit(1); }); break;
  case "task":     taskCmd(args); break;
  case "done":     doneCmd(args); break;
  case "answer":   answerCmd(args); break;
  case "ask":      askCmd(args); break;
  case "fail":     failCmd(args); break;
  case "setup":    setupCmd(args); break;
  case "sessions":
    console.log("Sessions are managed in-process — check ~/.dispatch/sessions/ for JSONL files.");
    break;
  case undefined:
  case "help":
  case "--help":
    console.log(`dispatch — agent orchestration harness

Commands:
  dispatch foreman                          Start the foreman daemon
  dispatch task create "desc" [--workflow]  Create a new task
  dispatch task list                        List all tasks
  dispatch task show <id>                   Show task details
  dispatch done --job <id> "summary"        Mark job complete
  dispatch answer --job <id> "text"         Answer a human job
  dispatch ask --job <id> "question"        Ask a question
  dispatch fail --job <id> "reason"         Mark job failed
  dispatch setup                            Interactive setup wizard
  dispatch sessions                         List active sessions

Environment:
  DISPATCH_ROOT   Dispatch data directory (default: ~/.dispatch)
  DISPATCH_JOB_ID Current job ID (set by foreman for agent processes)`);
    break;
  default:
    console.error(`Unknown command: ${cmd}`);
    process.exit(1);
}
