import * as fs from "fs";
import * as path from "path";
import * as os from "os";

export let ROOT: string = process.env.DISPATCH_ROOT ?? path.join(os.homedir(), ".dispatch");

export interface ModelConfig {
  provider: string;   // e.g. "openai-completions"
  endpoint: string;   // e.g. "http://localhost:8080/v1"
  model: string;      // e.g. "Qwen3.5-27B-Q4_K_M.gguf"
  apiKey?: string;    // optional, defaults to "none" for local
}

export interface Config {
  pollIntervalMs: number;
  pipePath: string;
  maxLoopIterations: number;
  defaultTimeouts: Record<string, number>;
  notifications: {
    escalation: string;
    target: string;
    channel?: string; // deprecated alias
  };
  pi: {
    binary?: string;
    defaultTools?: string[];
  };
  // Model registry: maps "local-9b" → provider config
  models: Record<string, ModelConfig>;
}

const DEFAULTS: Config = {
  pollIntervalMs: 30000,
  pipePath: "/tmp/dispatch.pipe",
  maxLoopIterations: 3,
  defaultTimeouts: {},
  notifications: { escalation: "", target: "" },
  pi: {},
  models: {
    "local-9b": {
      provider: "openai-completions",
      endpoint: "http://localhost:8081/v1",
      model: "Qwen3.5-9B-Q4_K_M.gguf",
    },
    "local-27b": {
      provider: "openai-completions",
      endpoint: "http://localhost:8080/v1",
      model: "Qwen3.5-27B-Q4_K_M.gguf",
    },
  },
};

let _cfg: Config | undefined;

export function loadConfig(): Config {
  if (_cfg) return _cfg;
  const cfgPath = path.join(ROOT, "config.json");
  const raw: Record<string, unknown> = fs.existsSync(cfgPath) ? JSON.parse(fs.readFileSync(cfgPath, "utf8")) : {};
  const cfg: Config = {
    ...DEFAULTS,
    ...raw,
    notifications: { ...DEFAULTS.notifications, ...((raw.notifications as object | undefined) ?? {}) },
    pi: { ...DEFAULTS.pi, ...((raw.pi as object | undefined) ?? {}) },
    models: { ...DEFAULTS.models, ...((raw.models as object | undefined) ?? {}) },
  };
  // backward compat
  const openclaw = raw.openclaw as { binary?: string } | undefined;
  if (openclaw?.binary && !cfg.pi.binary) cfg.pi.binary = openclaw.binary;
  if (!cfg.pipePath) cfg.pipePath = "/tmp/dispatch.pipe";
  if (!cfg.pollIntervalMs) cfg.pollIntervalMs = 30000;
  _cfg = cfg;
  return _cfg;
}

/** Resolve "local-9b/Qwen3.5-9B-Q4_K_M.gguf" → ModelConfig */
export function resolveModel(cfg: Config, modelStr: string): ModelConfig {
  const slash = modelStr.indexOf("/");
  const providerKey = slash >= 0 ? modelStr.slice(0, slash) : modelStr;
  const modelId = slash >= 0 ? modelStr.slice(slash + 1) : undefined;

  const mc = cfg.models[providerKey];
  if (!mc) throw new Error(`Unknown model provider: ${providerKey} (from "${modelStr}")`);

  // Use the registry key (e.g. "local-27b") as provider — Pi SDK looks up by that key
  return { ...mc, provider: providerKey, model: modelId ?? mc.model };
}

export function ensureDirs(): void {
  const dirs = [
    "jobs/pending", "jobs/active", "jobs/done", "jobs/failed",
    "artifacts", "logs", "workflows", "sessions", "agents", "skill",
  ];
  for (const d of dirs) {
    fs.mkdirSync(path.join(ROOT, d), { recursive: true });
  }
}
