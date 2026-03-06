import * as fs from "fs";
import * as path from "path";
import { ROOT, loadConfig } from "../config.js";
import { sendPipe } from "../pipe.js";

export function doneCmd(args: string[]): void {
  const opts = parseFlags(args);

  if (!opts.jobId) {
    console.error("dispatch: --job is required (or set DISPATCH_JOB_ID)");
    process.exit(1);
  }
  if (!opts.message && !opts.artifacts.length) {
    console.error(`dispatch done — report step completion

Usage:
  dispatch done --job <id> "result message"
  dispatch done --job <id> --artifact spec.md "message"

Flags:
  --job, -j       Job ID (or DISPATCH_JOB_ID env)
  --artifact, -a  Artifact file to copy (repeatable)`);
    process.exit(1);
  }

  const artifactNames = copyArtifacts(opts.artifacts, opts.taskId);

  // Write result file
  const resultPath = path.join(ROOT, "jobs", "active", `${opts.jobId}.result.md`);
  let content = opts.message ?? "";
  if (artifactNames.length) content += `\n\nArtifacts: ${artifactNames.join(", ")}`;
  fs.writeFileSync(resultPath, content + "\n", "utf8");

  const cfg = loadConfig();
  sendPipe(cfg.pipePath, {
    type: "done",
    jobId: opts.jobId,
    taskId: opts.taskId,
    message: opts.message ?? "",
    artifacts: artifactNames,
  });

  const suffix = artifactNames.length ? ` (${artifactNames.length} artifact${artifactNames.length !== 1 ? "s" : ""})` : "";
  console.log(`✓ Step complete${suffix}`);
}

function copyArtifacts(artifacts: string[], taskId?: string): string[] {
  if (!artifacts.length || !taskId) return [];
  const dest = path.join(ROOT, "artifacts", taskId);
  fs.mkdirSync(dest, { recursive: true });
  const names: string[] = [];
  for (const src of artifacts) {
    const name = path.basename(src);
    fs.copyFileSync(src, path.join(dest, name));
    names.push(name);
  }
  return names;
}

function parseFlags(args: string[]): { jobId: string; taskId: string; message: string; artifacts: string[] } {
  let jobId = process.env.DISPATCH_JOB_ID ?? "";
  let taskId = process.env.DISPATCH_TASK_ID ?? "";
  let message = "";
  const artifacts: string[] = [];
  const rest: string[] = [];

  for (let i = 0; i < args.length; i++) {
    if ((args[i] === "--job" || args[i] === "-j") && args[i + 1]) { jobId = args[++i]; }
    else if ((args[i] === "--task" || args[i] === "-t") && args[i + 1]) { taskId = args[++i]; }
    else if ((args[i] === "--artifact" || args[i] === "-a") && args[i + 1]) { artifacts.push(args[++i]); }
    else if (args[i] === "--root" && args[i + 1]) { i++; /* already set via env */ }
    else { rest.push(args[i]); }
  }

  message = rest.join(" ");
  return { jobId, taskId, message, artifacts };
}
