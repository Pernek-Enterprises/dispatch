#!/usr/bin/env node

/**
 * dispatch CLI — agent communication tool
 * 
 * Usage:
 *   dispatch done "summary message"
 *   dispatch done --artifact spec.md --artifact diff.patch "summary"
 *   dispatch ask "question for the foreman"
 *   dispatch fail "reason for failure"
 */

import { writeFileSync, readFileSync, existsSync, copyFileSync, mkdirSync } from 'fs';
import { join, basename, resolve } from 'path';

const DISPATCH_ROOT = process.env.DISPATCH_ROOT || join(process.env.HOME, 'dispatch');

// Read pipe path from config, fall back to env/default
let PIPE_PATH;
try {
  const configPath = join(DISPATCH_ROOT, 'config.json');
  if (existsSync(configPath)) {
    const config = JSON.parse(readFileSync(configPath, 'utf-8'));
    PIPE_PATH = config.pipePath;
  }
} catch { /* ignore */ }
PIPE_PATH = PIPE_PATH || process.env.DISPATCH_PIPE || '/tmp/dispatch.pipe';
const JOB_ID = process.env.DISPATCH_JOB_ID;
const TASK_ID = process.env.DISPATCH_TASK_ID;

function die(msg) {
  console.error(`dispatch: ${msg}`);
  process.exit(1);
}

function sendToForeman(message) {
  if (!existsSync(PIPE_PATH)) {
    die(`Foreman pipe not found at ${PIPE_PATH}. Is the foreman running?`);
  }
  writeFileSync(PIPE_PATH, JSON.stringify(message) + '\n');
}

function parseArgs(args) {
  const result = { artifacts: [], message: '' };
  let i = 0;
  while (i < args.length) {
    if (args[i] === '--artifact' || args[i] === '-a') {
      i++;
      if (i >= args.length) die('--artifact requires a file path');
      result.artifacts.push(args[i]);
    } else if (args[i] === '--escalate' || args[i] === '-e') {
      result.escalate = true;
    } else {
      // Collect remaining as message
      result.message = args.slice(i).join(' ');
      break;
    }
    i++;
  }
  return result;
}

function copyArtifacts(artifacts) {
  if (!TASK_ID) return artifacts.map(a => basename(a));
  
  const artifactDir = join(DISPATCH_ROOT, 'artifacts', TASK_ID);
  mkdirSync(artifactDir, { recursive: true });

  return artifacts.map(a => {
    const src = resolve(a);
    const name = basename(a);
    const dst = join(artifactDir, name);
    if (existsSync(src)) {
      copyFileSync(src, dst);
      console.log(`  artifact: ${name}`);
    } else {
      console.error(`  warning: artifact not found: ${a}`);
    }
    return name;
  });
}

// --- Commands ---

const command = process.argv[2];
const cmdArgs = process.argv.slice(3);

// Subcommand routing
if (command === 'workflow') {
  // Delegate to workflow CLI
  const { execFileSync } = await import('child_process');
  const workflowCli = join(new URL('.', import.meta.url).pathname, 'dispatch-workflow.js');
  try {
    execFileSync('node', [workflowCli, ...process.argv.slice(2)], { stdio: 'inherit', env: { ...process.env, DISPATCH_ROOT } });
  } catch (e) {
    process.exit(e.status || 1);
  }
  process.exit(0);
}

if (!command || command === '--help' || command === '-h') {
  console.log(`dispatch — agent communication CLI

Commands:
  dispatch done "message"              Mark step as complete
  dispatch done --artifact file.md     Complete with artifact(s)
  dispatch ask "question"              Ask a question
  dispatch ask --escalate "question"   Ask and escalate to human
  dispatch fail "reason"               Report failure
  dispatch workflow list|show|validate|create   Manage workflows

Environment:
  DISPATCH_JOB_ID    Current job ID (set by foreman)
  DISPATCH_TASK_ID   Current task ID (set by foreman)
  DISPATCH_PIPE      Foreman pipe path (default: /tmp/dispatch.pipe)
  DISPATCH_ROOT      Dispatch root dir (default: ~/dispatch)`);
  process.exit(0);
}

switch (command) {
  case 'done': {
    const { artifacts, message } = parseArgs(cmdArgs);
    if (!message && artifacts.length === 0) die('Usage: dispatch done "message"');

    // Copy artifacts to shared location
    const artifactNames = copyArtifacts(artifacts);

    // Write result to active job
    if (JOB_ID) {
      const resultPath = join(DISPATCH_ROOT, 'jobs', 'active', `${JOB_ID}.result.md`);
      const content = [
        message,
        artifactNames.length > 0 ? `\nArtifacts: ${artifactNames.join(', ')}` : ''
      ].join('\n').trim();
      writeFileSync(resultPath, content + '\n');
    }

    // Notify foreman
    sendToForeman({
      type: 'done',
      jobId: JOB_ID,
      taskId: TASK_ID,
      message,
      artifacts: artifacts.map(a => basename(a))
    });

    console.log(`✓ Step complete${artifacts.length ? ` (${artifacts.length} artifact${artifacts.length > 1 ? 's' : ''})` : ''}`);
    break;
  }

  case 'ask': {
    const { message, escalate } = parseArgs(cmdArgs);
    if (!message) die('Usage: dispatch ask "question"');

    sendToForeman({
      type: 'ask',
      jobId: JOB_ID,
      taskId: TASK_ID,
      question: message,
      escalate: escalate || false
    });

    console.log(`? Question sent${escalate ? ' (escalated to human)' : ''}`);
    break;
  }

  case 'fail': {
    const { message } = parseArgs(cmdArgs);
    if (!message) die('Usage: dispatch fail "reason"');

    if (JOB_ID) {
      const resultPath = join(DISPATCH_ROOT, 'jobs', 'active', `${JOB_ID}.result.md`);
      writeFileSync(resultPath, `FAILED: ${message}\n`);
    }

    sendToForeman({
      type: 'fail',
      jobId: JOB_ID,
      taskId: TASK_ID,
      reason: message
    });

    console.log(`✗ Step failed: ${message}`);
    break;
  }

  default:
    die(`Unknown command: ${command}. Use: done, ask, fail`);
}
