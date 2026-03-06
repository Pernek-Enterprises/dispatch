import * as fs from "fs";
import * as path from "path";
import * as crypto from "crypto";
import { ROOT } from "./config.js";

export interface Job {
  id: string;
  task: string;
  workflow: string;
  step: string;
  agent: string;
  model?: string;
  type: string;       // "work" | "human" | "triage" | "answer"
  priority: string;   // "urgent" | "high" | "normal" | "low"
  created: string;
  timeout: number;
  iteration?: number;
  prompt?: string;    // loaded separately, not in JSON
}

export interface CreateOpts {
  task: string;
  workflow: string;
  step: string;
  agent?: string;
  model?: string;
  type?: string;
  priority?: string;
  timeout?: number;
  iteration?: number;
  prompt?: string;
}

const PRIO: Record<string, number> = { urgent: 0, high: 1, normal: 2, low: 3 };

function jobDir(folder: string): string {
  return path.join(ROOT, "jobs", folder);
}

function randomHex(n = 4): string {
  return crypto.randomBytes(n).toString("hex");
}

function slugify(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9-]/g, "-").slice(0, 40);
}

function newId(step: string, task: string): string {
  return `${randomHex(4)}-${slugify(step + "-" + task)}`;
}

export function newTaskId(): string {
  return randomHex(4);
}

export function createJob(opts: CreateOpts): string {
  const id = newId(opts.step, opts.task);
  const job: Job = {
    id,
    task: opts.task,
    workflow: opts.workflow,
    step: opts.step,
    agent: opts.agent ?? "",
    model: opts.model,
    type: opts.type ?? "work",
    priority: opts.priority ?? "normal",
    created: new Date().toISOString(),
    timeout: opts.timeout ?? 120,
    iteration: opts.iteration ?? 1,
  };

  const dir = jobDir("pending");
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, `${id}.json`), JSON.stringify(job, null, 2) + "\n", "utf8");
  fs.writeFileSync(path.join(dir, `${id}.prompt.md`), (opts.prompt ?? "") + "\n", "utf8");
  return id;
}

export function listJobs(folder: string): Job[] {
  const dir = jobDir(folder);
  if (!fs.existsSync(dir)) return [];

  const jobs: Job[] = [];
  for (const entry of fs.readdirSync(dir)) {
    if (!entry.endsWith(".json")) continue;
    try {
      const j = JSON.parse(fs.readFileSync(path.join(dir, entry), "utf8")) as Job;
      const promptPath = path.join(dir, entry.replace(".json", ".prompt.md"));
      if (fs.existsSync(promptPath)) {
        j.prompt = fs.readFileSync(promptPath, "utf8");
      }
      jobs.push(j);
    } catch { /* skip corrupt */ }
  }

  return jobs.sort((a, b) => {
    const pa = PRIO[a.priority] ?? 2;
    const pb = PRIO[b.priority] ?? 2;
    if (pa !== pb) return pa - pb;
    return a.created.localeCompare(b.created);
  });
}

export function moveJob(id: string, from: string, to: string): void {
  const fromDir = jobDir(from);
  const toDir = jobDir(to);
  fs.mkdirSync(toDir, { recursive: true });
  for (const ext of [".json", ".prompt.md", ".result.md"]) {
    const src = path.join(fromDir, id + ext);
    const dst = path.join(toDir, id + ext);
    if (fs.existsSync(src)) fs.renameSync(src, dst);
  }
}

export function getJobMeta(id: string, folder: string): Job | null {
  const p = path.join(jobDir(folder), `${id}.json`);
  if (!fs.existsSync(p)) return null;
  try { return JSON.parse(fs.readFileSync(p, "utf8")) as Job; } catch { return null; }
}

export function writeJobMeta(id: string, folder: string, job: Job): void {
  fs.writeFileSync(path.join(jobDir(folder), `${id}.json`), JSON.stringify(job, null, 2) + "\n", "utf8");
}

export function readResult(id: string, folder: string): string {
  const p = path.join(jobDir(folder), `${id}.result.md`);
  return fs.existsSync(p) ? fs.readFileSync(p, "utf8") : "";
}

export function writeResult(id: string, folder: string, content: string): void {
  fs.writeFileSync(path.join(jobDir(folder), `${id}.result.md`), content + "\n", "utf8");
}
