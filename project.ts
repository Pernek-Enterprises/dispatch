/**
 * project.ts — Project layer for dispatch.
 *
 * A project binds a workflow run to a real-world context:
 *   - workspace: where agents write files (a repo, a content dir, etc.)
 *   - context: key-value pairs injected into every agent prompt
 *   - hooks: named actions the foreman runs at step transitions
 *
 * Projects are domain-agnostic. Git/GitHub is just one set of hooks.
 * Social, research, support — all use the same structure.
 *
 * Hook timing: "after_<step>" fires after that step completes, BEFORE
 * the next step is dispatched. So "after_spec" = right time to branch.
 */

import * as fs from "fs";
import * as path from "path";
import * as child_process from "child_process";
import { ROOT } from "./config.js";
import { log } from "./log.js";

// ─── Types ────────────────────────────────────────────────────────────────────

export interface Project {
  name: string;
  description?: string;
  /** Working directory for agents. Falls back to artifacts dir if unset. */
  workspace?: string;
  /** Key-value pairs injected as a header block in every agent prompt. */
  context: Record<string, string>;
  /**
   * Hook lists keyed by trigger name.
   * Trigger format: "after_<step>" or "after_<step>_<keyword>" (e.g. "after_review_accepted").
   * Each value is an array of built-in hook names or raw shell commands.
   */
  hooks: Record<string, string[]>;
}

/** Runtime state written by hooks (PR URL, branch, etc.) — stored per task. */
export interface ProjectState {
  branch?: string;
  prUrl?: string;
  prNumber?: number;
}

// ─── Loading ──────────────────────────────────────────────────────────────────

const PROJECTS_DIR = path.join(ROOT, "projects");

export function loadProject(name: string): Project {
  const p = path.join(PROJECTS_DIR, `${name}.json`);
  if (!fs.existsSync(p)) throw new Error(`Project not found: ${name} (${p})`);
  const raw = JSON.parse(fs.readFileSync(p, "utf8")) as Partial<Project>;
  return {
    name,
    description: raw.description,
    workspace: raw.workspace,
    context: raw.context ?? {},
    hooks: raw.hooks ?? {},
  };
}

export function listProjects(): string[] {
  if (!fs.existsSync(PROJECTS_DIR)) return [];
  return fs.readdirSync(PROJECTS_DIR)
    .filter(f => f.endsWith(".json"))
    .map(f => f.replace(".json", ""));
}

// ─── Project state (per task sidecar) ────────────────────────────────────────

function projectStatePath(taskId: string): string {
  return path.join(ROOT, "artifacts", taskId, ".project_state.json");
}

export function loadProjectState(taskId: string): ProjectState {
  const p = projectStatePath(taskId);
  if (!fs.existsSync(p)) return {};
  try { return JSON.parse(fs.readFileSync(p, "utf8")) as ProjectState; } catch { return {}; }
}

export function saveProjectState(taskId: string, state: ProjectState): void {
  const p = projectStatePath(taskId);
  fs.mkdirSync(path.dirname(p), { recursive: true });
  fs.writeFileSync(p, JSON.stringify(state, null, 2) + "\n", "utf8");
}

// ─── Context block ────────────────────────────────────────────────────────────

/**
 * Build the project context header injected at the top of every agent prompt.
 * Includes project metadata + any runtime state (branch, PR URL).
 */
export function buildContextBlock(project: Project, taskId: string): string {
  const state = loadProjectState(taskId);
  const lines: string[] = [`## Project: ${project.name}`];

  if (project.description) lines.push(project.description);
  lines.push("");

  if (project.workspace) lines.push(`**Workspace:** ${project.workspace}`);

  for (const [k, v] of Object.entries(project.context)) {
    lines.push(`**${k}:** ${v}`);
  }

  if (state.branch) lines.push(`**Branch:** ${state.branch}`);
  if (state.prUrl) lines.push(`**PR:** ${state.prUrl}`);

  lines.push("");
  if (project.workspace) {
    lines.push(`Work directly in the workspace directory: \`${project.workspace}\``);
    lines.push(`Write your output files there, not in the current directory.`);
  }

  // Surface PR review comments for fix step
  const prCommentsPath = path.join(ROOT, "artifacts", taskId, ".pr_comments.md");
  if (fs.existsSync(prCommentsPath)) {
    const comments = fs.readFileSync(prCommentsPath, "utf8").trim();
    lines.push("", comments);
  }

  return lines.join("\n");
}

// ─── Hook runner ─────────────────────────────────────────────────────────────

export interface HookContext {
  project: Project;
  taskId: string;
  step: string;
  artifactDir: string;
  workspace: string;
}

/** Run all hooks for a given trigger key. Best-effort — logs failures but doesn't abort. */
export function runHooks(triggerKey: string, ctx: HookContext): void {
  const hooks = ctx.project.hooks[triggerKey];
  if (!hooks?.length) return;

  log.info(`Running hooks for ${triggerKey} on task ${ctx.taskId}: ${hooks.join(", ")}`);

  for (const hook of hooks) {
    try {
      if (hook in BUILTIN_HOOKS) {
        BUILTIN_HOOKS[hook](ctx);
      } else {
        // Treat as raw shell command, run in workspace
        log.info(`[hook] shell: ${hook}`);
        child_process.execSync(hook, {
          cwd: ctx.workspace,
          encoding: "utf8",
          stdio: ["ignore", "pipe", "pipe"],
          env: { ...process.env, DISPATCH_TASK: ctx.taskId, DISPATCH_STEP: ctx.step },
        });
      }
      log.info(`[hook] ${hook} ✓`);
    } catch (e) {
      log.warn(`[hook] ${hook} failed: ${e}`);
    }
  }
}

// ─── Built-in hooks ───────────────────────────────────────────────────────────

type HookFn = (ctx: HookContext) => void;

function exec(cmd: string, cwd: string): string {
  return child_process.execSync(cmd, {
    cwd,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  }).trim();
}

const BUILTIN_HOOKS: Record<string, HookFn> = {

  /** Create a branch dispatch/<taskId> and check it out. */
  git_branch_create(ctx) {
    const branch = `dispatch/${ctx.taskId}`;
    exec(`git checkout -b ${branch}`, ctx.workspace);
    const state = loadProjectState(ctx.taskId);
    saveProjectState(ctx.taskId, { ...state, branch });
    log.info(`[hook] git: created branch ${branch}`);
  },

  /** Stage all changes and commit. */
  git_commit(ctx) {
    const msg = `dispatch: ${ctx.step} on task ${ctx.taskId}`;
    exec(`git add -A`, ctx.workspace);
    // --allow-empty so hook doesn't fail if model made no file changes
    exec(`git commit -m "${msg}" --allow-empty`, ctx.workspace);
    log.info(`[hook] git: committed "${msg}"`);
  },

  /** Push current branch to origin. */
  git_push(ctx) {
    const state = loadProjectState(ctx.taskId);
    const branch = state.branch ?? `dispatch/${ctx.taskId}`;
    exec(`git push --set-upstream origin ${branch}`, ctx.workspace);
    log.info(`[hook] git: pushed ${branch}`);
  },

  /** Create a draft PR on GitHub. Stores PR URL + number in project state. */
  gh_pr_create(ctx) {
    const taskFile = path.join(ctx.artifactDir, "task.md");
    const title = fs.existsSync(taskFile)
      ? fs.readFileSync(taskFile, "utf8").split("\n")[0].slice(0, 72)
      : `dispatch task ${ctx.taskId}`;

    const body = [
      `Automated by dispatch — task \`${ctx.taskId}\``,
      ``,
      `### Task`,
      fs.existsSync(taskFile) ? fs.readFileSync(taskFile, "utf8").trim() : title,
    ].join("\n");

    const bodyFile = path.join(ctx.artifactDir, ".pr_body.md");
    const titleFile = path.join(ctx.artifactDir, ".pr_title.md");
    fs.writeFileSync(bodyFile, body, "utf8");
    fs.writeFileSync(titleFile, title, "utf8");

    // Use files for title + body — avoids shell injection from user-provided task text
    exec(`gh pr create --draft --title-file "${titleFile}" --body-file "${bodyFile}" --base main`, ctx.workspace);

    const prUrl = exec(`gh pr view --json url -q .url`, ctx.workspace);
    const prNumber = parseInt(exec(`gh pr view --json number -q .number`, ctx.workspace), 10);

    const state = loadProjectState(ctx.taskId);
    saveProjectState(ctx.taskId, { ...state, prUrl, prNumber });
    log.info(`[hook] gh: created PR #${prNumber} — ${prUrl}`);
  },

  /** Post review.md content as a GitHub PR approval. */
  gh_pr_review_approve(ctx) {
    const reviewFile = path.join(ctx.artifactDir, "review.md");
    const body = fs.existsSync(reviewFile)
      ? fs.readFileSync(reviewFile, "utf8").trim()
      : "ACCEPTED — automated review by dispatch";
    const bodyFile = path.join(ctx.artifactDir, ".review_body.md");
    fs.writeFileSync(bodyFile, body, "utf8");
    exec(`gh pr review --approve --body-file "${bodyFile}"`, ctx.workspace);
    log.info(`[hook] gh: approved PR`);
  },

  /** Post review.md content as a GitHub PR request-changes review. */
  gh_pr_review_request_changes(ctx) {
    const reviewFile = path.join(ctx.artifactDir, "review.md");
    const body = fs.existsSync(reviewFile)
      ? fs.readFileSync(reviewFile, "utf8").trim()
      : "Changes requested — automated review by dispatch";
    const bodyFile = path.join(ctx.artifactDir, ".review_body.md");
    fs.writeFileSync(bodyFile, body, "utf8");
    exec(`gh pr review --request-changes --body-file "${bodyFile}"`, ctx.workspace);
    log.info(`[hook] gh: requested changes on PR`);
  },

  /**
   * Fetch PR review comments and append them to the fix prompt context.
   * Writes .pr_comments.md to artifacts dir for the fix step to pick up.
   */
  gh_fetch_pr_comments(ctx) {
    const state = loadProjectState(ctx.taskId);
    if (!state.prNumber) { log.warn("[hook] gh_fetch_pr_comments: no PR number in state"); return; }
    const comments = exec(
      `gh pr view ${state.prNumber} --json reviews -q '.reviews[] | "**" + .author.login + ":** " + .body'`,
      ctx.workspace,
    );
    const out = path.join(ctx.artifactDir, ".pr_comments.md");
    fs.writeFileSync(out, `## PR Review Comments\n\n${comments}\n`, "utf8");
    log.info(`[hook] gh: fetched PR comments → .pr_comments.md`);
  },

  /** Generate diff.patch from git diff instead of asking the model to create it. */
  git_diff_patch(ctx) {
    const patch = exec(`git diff HEAD~1 HEAD`, ctx.workspace);
    fs.writeFileSync(path.join(ctx.artifactDir, "diff.patch"), patch, "utf8");
    log.info(`[hook] git: generated diff.patch`);
  },
};

// Export built-in hook names for validation / help output
export const BUILTIN_HOOK_NAMES = Object.keys(BUILTIN_HOOKS);
