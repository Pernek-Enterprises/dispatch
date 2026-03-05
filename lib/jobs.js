import { readFileSync, writeFileSync, readdirSync, renameSync, existsSync, mkdirSync } from 'fs';
import { join, basename } from 'path';
import { randomUUID } from 'crypto';
import { ROOT } from './state.js';

const DIRS = {
  pending: join(ROOT, 'jobs', 'pending'),
  active: join(ROOT, 'jobs', 'active'),
  done: join(ROOT, 'jobs', 'done'),
  failed: join(ROOT, 'jobs', 'failed')
};

// Ensure dirs exist
for (const dir of Object.values(DIRS)) {
  mkdirSync(dir, { recursive: true });
}

export function createJob({ task, workflow, step, agent, model, type, priority, timeout, iteration, prompt }) {
  const slug = `${step}-${task}`.replace(/[^a-z0-9-]/gi, '-').substring(0, 40);
  const id = `${randomUUID().split('-')[0]}-${slug}`;

  const meta = {
    id,
    task,
    workflow,
    step,
    agent,
    model,
    type: type || 'work',
    priority: priority || 'normal',
    created: new Date().toISOString(),
    timeout: timeout || 1800,
    iteration: iteration || 1
  };

  writeFileSync(join(DIRS.pending, `${id}.json`), JSON.stringify(meta, null, 2) + '\n');
  writeFileSync(join(DIRS.pending, `${id}.prompt.md`), prompt + '\n');

  return id;
}

export function listJobs(folder) {
  const dir = DIRS[folder];
  if (!existsSync(dir)) return [];
  return readdirSync(dir)
    .filter(f => f.endsWith('.json'))
    .map(f => {
      try {
        const meta = JSON.parse(readFileSync(join(dir, f), 'utf-8'));
        const promptPath = join(dir, f.replace('.json', '.prompt.md'));
        const prompt = existsSync(promptPath) ? readFileSync(promptPath, 'utf-8') : '';
        return { ...meta, prompt, _file: f };
      } catch {
        return null;
      }
    })
    .filter(Boolean)
    .sort((a, b) => {
      // Priority sort: urgent > high > normal > low
      const prio = { urgent: 0, high: 1, normal: 2, low: 3 };
      const pa = prio[a.priority] ?? 2;
      const pb = prio[b.priority] ?? 2;
      if (pa !== pb) return pa - pb;
      return new Date(a.created) - new Date(b.created);
    });
}

export function moveJob(id, from, to) {
  const fromDir = DIRS[from];
  const toDir = DIRS[to];

  for (const ext of ['.json', '.prompt.md', '.result.md']) {
    const src = join(fromDir, `${id}${ext}`);
    const dst = join(toDir, `${id}${ext}`);
    if (existsSync(src)) {
      renameSync(src, dst);
    }
  }
}

export function readResult(id) {
  const resultPath = join(DIRS.active, `${id}.result.md`);
  if (!existsSync(resultPath)) return null;
  return readFileSync(resultPath, 'utf-8');
}

export function writeResult(id, folder, content) {
  const dir = DIRS[folder];
  writeFileSync(join(dir, `${id}.result.md`), content + '\n');
}

export function getJobMeta(id, folder) {
  const p = join(DIRS[folder], `${id}.json`);
  if (!existsSync(p)) return null;
  return JSON.parse(readFileSync(p, 'utf-8'));
}

export function getActiveJobForAgent(agentId) {
  const jobs = listJobs('active');
  return jobs.find(j => j.agent === agentId) || null;
}

export { DIRS };
