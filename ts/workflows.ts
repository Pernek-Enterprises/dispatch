import * as fs from "fs";
import * as path from "path";
import { ROOT } from "./config.js";

export interface Step {
  role?: string;
  agent?: string;   // deprecated, use role
  model?: string;
  timeout?: number;
  next?: string;
  branch?: Record<string, string>;
  maxIterations?: number;
  type?: string;
  artifactsIn?: string[];
  artifactsOut?: string[];
  prompt?: string;  // loaded from file
}

export interface DestroyConfig {
  agents?: string[];
  timeout?: number;
  actions: string[];
}

export interface Workflow {
  name: string;
  description?: string;
  firstStep: string;
  steps: Record<string, Step>;
  destroy: DestroyConfig;
}

export function loadWorkflow(name: string): Workflow {
  const p = path.join(ROOT, "workflows", `${name}.json`);
  if (!fs.existsSync(p)) throw new Error(`Workflow not found: ${name}`);
  const wf = JSON.parse(fs.readFileSync(p, "utf8")) as Workflow;

  // Load step prompts
  const promptDir = path.join(ROOT, "workflows", name);
  for (const [stepName, step] of Object.entries(wf.steps)) {
    const promptPath = path.join(promptDir, `${stepName}.prompt.md`);
    if (fs.existsSync(promptPath)) {
      step.prompt = fs.readFileSync(promptPath, "utf8").trim();
    }
    wf.steps[stepName] = step;
  }

  // Defaults
  if (!wf.destroy) wf.destroy = { actions: ["close_sessions", "archive_artifacts"] };
  if (!wf.destroy.timeout) wf.destroy.timeout = 120;
  if (!wf.destroy.actions?.length) wf.destroy.actions = ["close_sessions", "archive_artifacts"];

  return wf;
}

export function listWorkflows(): string[] {
  const dir = path.join(ROOT, "workflows");
  if (!fs.existsSync(dir)) return [];
  return fs.readdirSync(dir)
    .filter(f => f.endsWith(".json"))
    .map(f => f.replace(".json", ""));
}

export function getRole(step: Step): string {
  return step.role ?? step.agent ?? "";
}

export function getNextStep(wf: Workflow, currentStep: string, result: string): string | null {
  const step = wf.steps[currentStep];
  if (!step) return null;

  if (step.branch && Object.keys(step.branch).length > 0) {
    const upper = result.toUpperCase();
    for (const [keyword, target] of Object.entries(step.branch)) {
      if (upper.includes(keyword.toUpperCase())) return target;
    }
    return null; // no keyword matched → terminal
  }

  return step.next ?? null;
}

export function getDestroyAgents(wf: Workflow): string[] {
  if (wf.destroy.agents?.length) return wf.destroy.agents;
  const seen = new Set<string>();
  const roles: string[] = [];
  for (const step of Object.values(wf.steps)) {
    const role = getRole(step);
    if (role && role !== "stefan" && step.type !== "human" && !seen.has(role)) {
      seen.add(role);
      roles.push(role);
    }
  }
  return roles;
}
