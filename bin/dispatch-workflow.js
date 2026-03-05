#!/usr/bin/env node

/**
 * dispatch workflow — create, list, show, and validate workflows
 *
 * Usage:
 *   dispatch workflow list
 *   dispatch workflow show <name>
 *   dispatch workflow validate <name>
 *   dispatch workflow create
 */

import { readFileSync, writeFileSync, mkdirSync, existsSync } from 'fs';
import { join } from 'path';
import { createInterface } from 'readline';

const DISPATCH_ROOT = process.env.DISPATCH_ROOT || join(process.env.HOME, 'dispatch');
const WORKFLOWS_DIR = join(DISPATCH_ROOT, 'workflows');

// Import workflow lib (resolve relative to bin/)
import { parseWorkflow, listWorkflows, validateWorkflow } from '../lib/workflows.js';

const rl = createInterface({ input: process.stdin, output: process.stdout });
const ask = (q) => new Promise(r => rl.question(q, r));

async function main() {
  const sub = process.argv[3];

  switch (sub) {
    case 'list': return cmdList();
    case 'show': return cmdShow(process.argv[4]);
    case 'validate': return cmdValidate(process.argv[4]);
    case 'create': return await cmdCreate();
    default:
      console.log(`dispatch workflow — manage workflow definitions

Commands:
  dispatch workflow list              List available workflows
  dispatch workflow show <name>       Show workflow details
  dispatch workflow validate <name>   Validate a workflow
  dispatch workflow create            Create a new workflow interactively`);
      process.exit(0);
  }
}

function cmdList() {
  const workflows = listWorkflows();
  if (workflows.length === 0) {
    console.log('No workflows found. Create one with: dispatch workflow create');
    return;
  }
  console.log('Workflows:');
  for (const name of workflows) {
    const wf = parseWorkflow(name);
    const stepCount = Object.keys(wf?.steps || {}).length;
    console.log(`  ${name} (${stepCount} steps) — ${wf?.description || ''}`);
  }
}

function cmdShow(name) {
  if (!name) { console.error('Usage: dispatch workflow show <name>'); process.exit(1); }
  const wf = parseWorkflow(name);
  if (!wf) { console.error(`Workflow "${name}" not found`); process.exit(1); }

  console.log(`\n  ${wf.name}`);
  if (wf.description) console.log(`  ${wf.description}`);
  console.log();

  // Render flow
  const steps = Object.keys(wf.steps);
  const lines = [];
  let current = wf.firstStep;
  const visited = new Set();

  while (current && !visited.has(current)) {
    visited.add(current);
    const step = wf.steps[current];
    const agent = step.agent || step.type || '?';
    const model = step.model ? ` [${step.model}]` : '';
    const timeout = step.timeout ? ` ${formatTimeout(step.timeout)}` : '';

    if (step.branch) {
      const branches = Object.entries(step.branch)
        .map(([k, v]) => `${k} → ${v}`)
        .join(', ');
      lines.push(`  ${current} (${agent}${model}${timeout}) → {${branches}}`);
      // Follow first branch for display
      current = Object.values(step.branch)[0];
    } else if (step.next) {
      lines.push(`  ${current} (${agent}${model}${timeout}) → ${step.next}`);
      current = step.next;
    } else {
      lines.push(`  ${current} (${agent}${model}${timeout}) ■`);
      current = null;
    }
  }

  console.log('  Flow:');
  for (const line of lines) console.log(line);
  console.log();

  // Step details
  for (const [name, step] of Object.entries(wf.steps)) {
    const prompt = step.prompt ? step.prompt.split('\n')[0].substring(0, 60) : '(no prompt)';
    console.log(`  ### ${name}`);
    console.log(`      agent: ${step.agent || step.type || '-'}  model: ${step.model || '-'}  timeout: ${formatTimeout(step.timeout || 0)}`);
    if (step.artifactsIn?.length) console.log(`      in: ${step.artifactsIn.join(', ')}`);
    if (step.artifactsOut?.length) console.log(`      out: ${step.artifactsOut.join(', ')}`);
    console.log(`      ${prompt}...`);
    console.log();
  }
}

function cmdValidate(name) {
  if (!name) { console.error('Usage: dispatch workflow validate <name>'); process.exit(1); }

  const filePath = join(WORKFLOWS_DIR, `${name}.json`);
  if (!existsSync(filePath)) { console.error(`Workflow "${name}" not found`); process.exit(1); }

  const wf = JSON.parse(readFileSync(filePath, 'utf-8'));
  const errors = validateWorkflow(wf);

  if (errors.length === 0) {
    console.log(`✓ Workflow "${name}" is valid`);

    // Check for missing prompts
    const promptDir = join(WORKFLOWS_DIR, name);
    const missing = [];
    for (const stepName of Object.keys(wf.steps)) {
      const promptPath = join(promptDir, `${stepName}.prompt.md`);
      if (!existsSync(promptPath)) missing.push(stepName);
    }
    if (missing.length > 0) {
      console.log(`  ⚠ Missing prompt files: ${missing.join(', ')}`);
      console.log(`    Create them at: ${promptDir}/<step>.prompt.md`);
    }
  } else {
    console.error(`✗ Workflow "${name}" has ${errors.length} error(s):`);
    for (const err of errors) console.error(`  - ${err}`);
    process.exit(1);
  }
}

async function cmdCreate() {
  console.log('\n  Create a new workflow\n');

  const name = await ask('  Workflow name (e.g. coding-easy): ');
  if (!name.trim()) { console.error('Name required'); process.exit(1); }

  const description = await ask('  Description: ');

  const steps = {};
  const stepOrder = [];
  let addingSteps = true;

  console.log('\n  Add steps (enter empty name to finish):\n');

  while (addingSteps) {
    const stepName = await ask('  Step name: ');
    if (!stepName.trim()) { addingSteps = false; break; }

    const agent = await ask('    Agent: ');
    const model = await ask('    Model (or empty for none): ');
    const timeoutStr = await ask('    Timeout (e.g. 10m, 30m, 1h): ');
    const nextOrBranch = await ask('    Next step (or "branch" for branching): ');

    const step = {
      agent: agent.trim(),
      ...(model.trim() && { model: model.trim() }),
      timeout: parseTimeoutInput(timeoutStr.trim()),
    };

    if (nextOrBranch.trim() === 'branch') {
      step.branch = {};
      let addingBranches = true;
      console.log('    Add branches (empty keyword to finish):');
      while (addingBranches) {
        const keyword = await ask('      Keyword (e.g. ACCEPTED): ');
        if (!keyword.trim()) { addingBranches = false; break; }
        const target = await ask('      → Target step: ');
        step.branch[keyword.trim()] = target.trim();
      }
      const maxIter = await ask('    Max iterations (default 3): ');
      if (maxIter.trim()) step.maxIterations = parseInt(maxIter) || 3;
    } else if (nextOrBranch.trim()) {
      step.next = nextOrBranch.trim();
    }

    const artifactsIn = await ask('    Artifacts in (comma-separated, or empty): ');
    const artifactsOut = await ask('    Artifacts out (comma-separated, or empty): ');
    if (artifactsIn.trim()) step.artifactsIn = artifactsIn.split(',').map(s => s.trim());
    if (artifactsOut.trim()) step.artifactsOut = artifactsOut.split(',').map(s => s.trim());

    steps[stepName.trim()] = step;
    stepOrder.push(stepName.trim());
    console.log(`    ✓ Added step "${stepName.trim()}"\n`);
  }

  if (stepOrder.length === 0) {
    console.error('No steps added');
    process.exit(1);
  }

  const workflow = {
    name: name.trim(),
    description: description.trim(),
    firstStep: stepOrder[0],
    steps
  };

  // Validate
  const errors = validateWorkflow(workflow);
  if (errors.length > 0) {
    console.error('\n  ⚠ Validation warnings:');
    for (const err of errors) console.error(`    - ${err}`);
    const proceed = await ask('\n  Save anyway? (y/n): ');
    if (proceed.toLowerCase() !== 'y') { process.exit(1); }
  }

  // Write workflow JSON
  mkdirSync(WORKFLOWS_DIR, { recursive: true });
  const jsonPath = join(WORKFLOWS_DIR, `${name.trim()}.json`);
  writeFileSync(jsonPath, JSON.stringify(workflow, null, 2) + '\n');
  console.log(`\n  ✓ Saved ${jsonPath}`);

  // Create prompt directory and stub files
  const promptDir = join(WORKFLOWS_DIR, name.trim());
  mkdirSync(promptDir, { recursive: true });
  for (const stepName of stepOrder) {
    const promptPath = join(promptDir, `${stepName}.prompt.md`);
    if (!existsSync(promptPath)) {
      writeFileSync(promptPath, `<!-- Prompt for ${stepName} step -->\n\nDescribe what the agent should do in this step.\n`);
    }
  }
  console.log(`  ✓ Created prompt stubs in ${promptDir}/`);
  console.log(`\n  Next: edit the prompt files, then validate with:`);
  console.log(`    dispatch workflow validate ${name.trim()}\n`);

  rl.close();
}

function parseTimeoutInput(value) {
  if (!value) return 1800;
  const match = value.match(/(\d+)\s*(s|m|h|min)/);
  if (!match) return parseInt(value) || 1800;
  const num = parseInt(match[1]);
  switch (match[2]) {
    case 's': return num;
    case 'm': case 'min': return num * 60;
    case 'h': return num * 3600;
    default: return 1800;
  }
}

function formatTimeout(seconds) {
  if (!seconds) return '-';
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
  return `${(seconds / 3600).toFixed(1)}h`;
}

main().catch(err => {
  console.error(err.message);
  process.exit(1);
});
