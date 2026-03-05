import { readFileSync, readdirSync, existsSync } from 'fs';
import { join } from 'path';
import { ROOT } from './state.js';

const WORKFLOWS_DIR = join(ROOT, 'workflows');

/**
 * Parse a workflow markdown file into a structured object.
 * 
 * Expected format:
 * ```
 * # workflow-name
 * Description text
 * 
 * ## Graph
 * step1 → step2 → step3 → [ACCEPTED: step4] [DENIED: step5]
 * step5 → step3 (loop)
 * 
 * ## Steps
 * ### step-name
 * agent: kit
 * model: 27b
 * timeout: 10m
 * artifacts_in: [task]
 * artifacts_out: [spec.md]
 * max_iterations: 3
 * 
 * Prompt text here...
 * ```
 */
export function parseWorkflow(name) {
  const filePath = join(WORKFLOWS_DIR, `${name}.md`);
  if (!existsSync(filePath)) return null;

  const content = readFileSync(filePath, 'utf-8');
  const lines = content.split('\n');

  const workflow = {
    name,
    description: '',
    graph: [],
    steps: {}
  };

  let section = 'header';
  let currentStep = null;
  let promptLines = [];

  for (const line of lines) {
    // Top-level sections
    if (line.startsWith('## Graph')) {
      section = 'graph';
      continue;
    }
    if (line.startsWith('## Steps')) {
      section = 'steps';
      continue;
    }

    // Workflow name
    if (line.startsWith('# ') && section === 'header') {
      workflow.name = line.slice(2).trim();
      continue;
    }

    // Graph lines
    if (section === 'graph' && line.trim()) {
      workflow.graph.push(parseGraphLine(line.trim()));
      continue;
    }

    // Step header
    if (section === 'steps' && line.startsWith('### ')) {
      // Save previous step's prompt
      if (currentStep) {
        workflow.steps[currentStep].prompt = promptLines.join('\n').trim();
      }
      currentStep = line.slice(4).trim();
      workflow.steps[currentStep] = {
        name: currentStep,
        agent: null,
        model: null,
        timeout: 1800,
        artifacts_in: [],
        artifacts_out: [],
        branches: null,
        max_iterations: 3,
        prompt: ''
      };
      promptLines = [];
      continue;
    }

    // Step metadata
    if (section === 'steps' && currentStep && line.match(/^(agent|model|timeout|artifacts_in|artifacts_out|branch|next|max_iterations):/)) {
      const [key, ...valueParts] = line.split(':');
      const value = valueParts.join(':').trim();
      const step = workflow.steps[currentStep];

      switch (key.trim()) {
        case 'agent': step.agent = value; break;
        case 'model': step.model = value; break;
        case 'timeout': step.timeout = parseTimeout(value); break;
        case 'artifacts_in': step.artifacts_in = parseArray(value); break;
        case 'artifacts_out': step.artifacts_out = parseArray(value); break;
        case 'next': step.next = value; break;
        case 'max_iterations': step.max_iterations = parseInt(value) || 3; break;
        case 'branch':
          step.branches = value.split('|').map(b => b.trim());
          break;
      }
      continue;
    }

    // Separator between steps
    if (section === 'steps' && line.trim() === '---') continue;

    // Step prompt content
    if (section === 'steps' && currentStep) {
      promptLines.push(line);
    }

    // Description
    if (section === 'header' && line.trim() && !line.startsWith('#')) {
      workflow.description += line.trim() + ' ';
    }
  }

  // Save last step's prompt
  if (currentStep) {
    workflow.steps[currentStep].prompt = promptLines.join('\n').trim();
  }

  workflow.description = workflow.description.trim();
  return workflow;
}

function parseGraphLine(line) {
  // Parse: step1 → step2 → [ACCEPTED: step3] [DENIED: step4]
  const parts = line.split('→').map(p => p.trim());
  const transitions = [];

  for (let i = 0; i < parts.length - 1; i++) {
    const from = parts[i].replace(/\(.*\)/, '').trim();
    const toRaw = parts[i + 1];

    // Check for branch syntax: [KEYWORD: target]
    const branches = [...toRaw.matchAll(/\[(\w+):\s*(\w+)\]/g)];
    if (branches.length > 0) {
      const branchMap = {};
      for (const [, keyword, target] of branches) {
        branchMap[keyword] = target;
      }
      transitions.push({ from, branches: branchMap });
    } else {
      const to = toRaw.replace(/\(.*\)/, '').trim();
      transitions.push({ from, to });
    }
  }

  return transitions;
}

function parseTimeout(value) {
  const match = value.match(/(\d+)\s*(s|m|h|min)/);
  if (!match) return 1800;
  const num = parseInt(match[1]);
  switch (match[2]) {
    case 's': return num;
    case 'm': case 'min': return num * 60;
    case 'h': return num * 3600;
    default: return 1800;
  }
}

function parseArray(value) {
  // Parse [item1, item2] or [item1]
  const match = value.match(/\[(.*)\]/);
  if (!match) return [];
  return match[1].split(',').map(s => s.trim()).filter(Boolean);
}

/**
 * Given a workflow and the current step + result, determine the next step.
 */
export function getNextStep(workflow, currentStepName, result) {
  const step = workflow.steps[currentStepName];
  if (!step) return null;

  // If step has an explicit `next`, use it
  if (step.next) return step.next;

  // Check graph transitions for branching
  for (const transitionGroup of workflow.graph) {
    for (const t of transitionGroup) {
      if (t.from === currentStepName) {
        if (t.branches) {
          // Check result for branch keywords
          for (const [keyword, target] of Object.entries(t.branches)) {
            if (result && result.toUpperCase().includes(keyword.toUpperCase())) {
              return target;
            }
          }
          // No keyword matched — error
          return null;
        }
        if (t.to) return t.to;
      }
    }
  }

  // No transition found — check if there's a step listed after this one
  const stepNames = Object.keys(workflow.steps);
  const idx = stepNames.indexOf(currentStepName);
  if (idx >= 0 && idx < stepNames.length - 1) {
    // Only auto-advance if no graph defined for this step
    const hasGraphEntry = workflow.graph.flat().some(t => t.from === currentStepName);
    if (!hasGraphEntry) return stepNames[idx + 1];
  }

  return null; // Last step or no transition
}

export function listWorkflows() {
  if (!existsSync(WORKFLOWS_DIR)) return [];
  return readdirSync(WORKFLOWS_DIR)
    .filter(f => f.endsWith('.md'))
    .map(f => f.replace('.md', ''));
}
