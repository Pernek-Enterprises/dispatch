import * as fs from "fs";
import * as path from "path";
import { ROOT } from "./config.js";

export interface Step {
  preset?: string;  // name of a step preset to inherit defaults from
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

/** A step preset — reusable defaults a workflow step can inherit via "preset": "<name>" */
export type StepPreset = Omit<Step, "preset" | "next" | "branch" | "prompt">;

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

/** Load step presets from ~/.dispatch/step-presets.json, returns {} if absent */
function loadPresets(): Record<string, StepPreset> {
  const p = path.join(ROOT, "step-presets.json");
  if (!fs.existsSync(p)) return {};
  try { return JSON.parse(fs.readFileSync(p, "utf8")); }
  catch (e) { console.warn(`Warning: could not parse step-presets.json: ${e}`); return {}; }
}

export function loadWorkflow(name: string): Workflow {
  const p = path.join(ROOT, "workflows", `${name}.json`);
  if (!fs.existsSync(p)) throw new Error(`Workflow not found: ${name}`);
  const wf = JSON.parse(fs.readFileSync(p, "utf8")) as Workflow;

  // Load presets once — applied before step-level overrides
  const presets = loadPresets();

  // Resolve steps: merge preset defaults → step fields (step always wins)
  const promptDir = path.join(ROOT, "workflows", name);
  for (const [stepName, rawStep] of Object.entries(wf.steps)) {
    let step = rawStep;

    // 1. Apply preset defaults if specified
    if (step.preset) {
      const preset = presets[step.preset];
      if (!preset) {
        console.warn(`Warning: step "${stepName}" references unknown preset "${step.preset}"`);
      } else {
        // Preset provides defaults; step-level values override
        step = { ...preset, ...step };
      }
    }

    // 2. Load per-step prompt file (overrides anything in preset or JSON)
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
    return null; // no keyword matched
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
