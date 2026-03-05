import { readFileSync, readdirSync, existsSync } from 'fs';
import { join } from 'path';
import { ROOT } from './state.js';

const WORKFLOWS_DIR = join(ROOT, 'workflows');

/**
 * Load a workflow definition from JSON.
 * Prompts are loaded from separate .prompt.md files.
 */
export function parseWorkflow(name) {
  const filePath = join(WORKFLOWS_DIR, `${name}.json`);
  if (!existsSync(filePath)) return null;

  const workflow = JSON.parse(readFileSync(filePath, 'utf-8'));

  // Load prompts from per-step markdown files
  const promptDir = join(WORKFLOWS_DIR, name);
  for (const [stepName, step] of Object.entries(workflow.steps || {})) {
    const promptPath = join(promptDir, `${stepName}.prompt.md`);
    step.prompt = existsSync(promptPath)
      ? readFileSync(promptPath, 'utf-8').trim()
      : '';
  }

  return workflow;
}

/**
 * Determine the next step given a completed step and its result text.
 * Uses keyword matching on branch definitions.
 */
export function getNextStep(workflow, currentStepName, result) {
  const step = workflow.steps[currentStepName];
  if (!step) return null;

  // Branching step — match keywords in result
  if (step.branch) {
    for (const [keyword, target] of Object.entries(step.branch)) {
      if (result && result.toUpperCase().includes(keyword.toUpperCase())) {
        return target;
      }
    }
    // No keyword matched
    return null;
  }

  // Explicit next
  if (step.next) return step.next;

  // No transition — this is the last step
  return null;
}

/**
 * List available workflow names.
 */
export function listWorkflows() {
  if (!existsSync(WORKFLOWS_DIR)) return [];
  return readdirSync(WORKFLOWS_DIR)
    .filter(f => f.endsWith('.json'))
    .map(f => f.replace('.json', ''));
}

/**
 * Validate a workflow definition. Returns array of error strings (empty = valid).
 */
export function validateWorkflow(workflow) {
  const errors = [];

  if (!workflow.name) errors.push('Missing "name"');
  if (!workflow.firstStep) errors.push('Missing "firstStep"');
  if (!workflow.steps || Object.keys(workflow.steps).length === 0) {
    errors.push('No steps defined');
    return errors;
  }

  const stepNames = new Set(Object.keys(workflow.steps));

  // firstStep must exist
  if (workflow.firstStep && !stepNames.has(workflow.firstStep)) {
    errors.push(`firstStep "${workflow.firstStep}" not found in steps`);
  }

  for (const [name, step] of Object.entries(workflow.steps)) {
    // Validate next references
    if (step.next && !stepNames.has(step.next)) {
      errors.push(`Step "${name}": next "${step.next}" not found`);
    }

    // Validate branch references
    if (step.branch) {
      for (const [keyword, target] of Object.entries(step.branch)) {
        if (!stepNames.has(target)) {
          errors.push(`Step "${name}": branch ${keyword} → "${target}" not found`);
        }
      }
    }

    // Must have agent (unless type is explicitly set)
    if (!step.agent && !step.type) {
      errors.push(`Step "${name}": missing "agent"`);
    }
  }

  // Check for unreachable steps
  const reachable = new Set([workflow.firstStep]);
  let changed = true;
  while (changed) {
    changed = false;
    for (const [name, step] of Object.entries(workflow.steps)) {
      if (!reachable.has(name)) continue;
      if (step.next && !reachable.has(step.next)) {
        reachable.add(step.next);
        changed = true;
      }
      if (step.branch) {
        for (const target of Object.values(step.branch)) {
          if (!reachable.has(target)) {
            reachable.add(target);
            changed = true;
          }
        }
      }
    }
  }
  for (const name of stepNames) {
    if (!reachable.has(name)) {
      errors.push(`Step "${name}" is unreachable from firstStep`);
    }
  }

  return errors;
}
