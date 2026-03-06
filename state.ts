import * as fs from "fs";
import * as path from "path";
import { ROOT } from "./config.js";

interface ModelLock {
  busy: boolean;
  job?: string;
  since?: string;
}

export interface TaskState {
  workflow: string;
  currentStep: string;
  status: string;   // "active" | "destroying" | "complete"
  iteration: Record<string, number>;
  created?: string;
}

export interface AppState {
  models: Record<string, ModelLock>;
  tasks: Record<string, TaskState>;
}

export class State {
  private data: AppState = { models: {}, tasks: {} };

  static load(): State {
    const s = new State();
    const p = path.join(ROOT, "state.json");
    if (fs.existsSync(p)) {
      try {
        const raw = JSON.parse(fs.readFileSync(p, "utf8")) as Partial<AppState>;
        s.data.models = raw.models ?? {};
        s.data.tasks = raw.tasks ?? {};
      } catch { /* start fresh */ }
    }
    return s;
  }

  save(): void {
    const p = path.join(ROOT, "state.json");
    fs.writeFileSync(p, JSON.stringify(this.data, null, 2) + "\n", "utf8");
  }

  isModelFree(modelId: string): boolean {
    return !this.data.models[modelId]?.busy;
  }

  lockModel(modelId: string, jobId: string): void {
    this.data.models[modelId] = { busy: true, job: jobId, since: new Date().toISOString() };
  }

  unlockModel(modelId: string): void {
    this.data.models[modelId] = { busy: false };
  }

  getTask(taskId: string): TaskState | undefined {
    return this.data.tasks[taskId];
  }

  setTask(taskId: string, ts: TaskState): void {
    this.data.tasks[taskId] = ts;
  }

  get tasks(): Record<string, TaskState> {
    return this.data.tasks;
  }
}
