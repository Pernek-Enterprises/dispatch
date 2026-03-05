import { readFileSync, writeFileSync, existsSync } from 'fs';
import { join } from 'path';

const ROOT = process.env.DISPATCH_ROOT || join(process.env.HOME, 'dispatch');
const STATE_PATH = join(ROOT, 'state.json');

const DEFAULT_STATE = {
  models: {},
  agents: {},
  sessions: {},
  tasks: {}
};

export function load() {
  if (!existsSync(STATE_PATH)) return structuredClone(DEFAULT_STATE);
  try {
    return JSON.parse(readFileSync(STATE_PATH, 'utf-8'));
  } catch {
    return structuredClone(DEFAULT_STATE);
  }
}

export function save(state) {
  writeFileSync(STATE_PATH, JSON.stringify(state, null, 2) + '\n');
}

export function loadConfig(name) {
  const p = join(ROOT, `${name}.json`);
  if (!existsSync(p)) return {};
  return JSON.parse(readFileSync(p, 'utf-8'));
}

export function lockModel(state, modelId, jobId) {
  state.models[modelId] = { busy: true, job: jobId, since: new Date().toISOString() };
}

export function unlockModel(state, modelId) {
  state.models[modelId] = { busy: false, job: null, since: null };
}

export function lockAgent(state, agentId, jobId) {
  state.agents[agentId] = {
    ...state.agents[agentId],
    busy: true,
    job: jobId,
    since: new Date().toISOString()
  };
}

export function unlockAgent(state, agentId) {
  state.agents[agentId] = {
    ...state.agents[agentId],
    busy: false,
    job: null,
    since: null
  };
}

export function isModelFree(state, modelId) {
  return !state.models[modelId]?.busy;
}

export function isAgentFree(state, agentId) {
  return !state.agents[agentId]?.busy;
}

export { ROOT };
