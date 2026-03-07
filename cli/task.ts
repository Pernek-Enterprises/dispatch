import * as fs from "fs";
import * as path from "path";
import * as readline from "readline";
import { ROOT, loadConfig } from "../config.js";
import { createJob, newTaskId, listJobs } from "../jobs.js";
import { loadWorkflow, listWorkflows, getRole, validateWorkflowRoles } from "../workflows.js";
import { sendPipe } from "../pipe.js";
import { loadSystemPromptPublic } from "../prompts.js";
import { loadProject, listProjects, validateProjectHooks } from "../project.js";

export function taskCmd(args: string[]): void {
  if (!args.length || args[0] === "help") {
    console.log(`dispatch task — manage tasks

Commands:
  dispatch task create "description" --workflow coding-easy [--project agentictodo]
  dispatch task create --interactive
  dispatch task list
  dispatch task show <task-id>
  dispatch task projects`);
    return;
  }

  switch (args[0]) {
    case "create": taskCreate(args.slice(1)); break;
    case "list":   taskList(); break;
    case "projects": taskProjects(); break;
    case "show":
      if (args.length < 2) { console.error("Usage: dispatch task show <task-id>"); process.exit(1); }
      taskShow(args[1]);
      break;
    default:
      console.error(`Unknown task command: ${args[0]}`);
      process.exit(1);
  }
}

function taskCreate(args: string[]): void {
  const descParts: string[] = [];
  let workflowName = "";
  let priority = "normal";
  let interactive = false;
  let projectName = "";

  for (let i = 0; i < args.length; i++) {
    if ((args[i] === "--workflow" || args[i] === "-w") && args[i + 1]) { workflowName = args[++i]; }
    else if ((args[i] === "--priority" || args[i] === "-p") && args[i + 1]) { priority = args[++i]; }
    else if ((args[i] === "--project" || args[i] === "-P") && args[i + 1]) { projectName = args[++i]; }
    else if (args[i] === "--interactive" || args[i] === "-i") { interactive = true; }
    else { descParts.push(args[i]); }
  }

  if (interactive) { taskCreateInteractive(); return; }

  const description = descParts.join(" ");
  if (!description) {
    console.error(`Usage: dispatch task create "description" --workflow coding-easy [--project myproject]`);
    process.exit(1);
  }

  if (!workflowName) {
    const available = listWorkflows();
    if (available.length === 1) { workflowName = available[0]; }
    else {
      console.error("Multiple workflows available. Specify one with --workflow:\n" + available.map(w => `  ${w}`).join("\n"));
      process.exit(1);
    }
  }

  // Validate project if specified
  if (projectName) {
    try { loadProject(projectName); } catch (e) {
      console.error(`Project not found: ${projectName}\nAvailable: ${listProjects().join(", ") || "(none)"}`);
      process.exit(1);
    }
  }

  const taskId = doCreateTask(description, workflowName, priority, projectName);
  const projectLine = projectName ? `\n  project:  ${projectName}` : "";
  console.log(`✓ Task created: ${taskId}\n  workflow: ${workflowName}\n  priority: ${priority}${projectLine}`);
}

function taskCreateInteractive(): void {
  const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
  const ask = (prompt: string): Promise<string> => new Promise(r => rl.question(`  ${prompt}`, (a) => r(a.trim())));

  (async () => {
    console.log("\n  Create a new task\n");
    const description = await ask("Description: ");
    if (!description) { console.error("Description required"); process.exit(1); }

    const available = listWorkflows();
    console.log("\n  Available workflows:");
    for (const w of available) {
      try { const wf = loadWorkflow(w); console.log(`    ${w} — ${wf.description ?? ""}`); }
      catch { console.log(`    ${w}`); }
    }

    let workflowName = await ask("\n  Workflow: ");
    if (!workflowName && available.length === 1) workflowName = available[0];
    if (!workflowName) { console.error("Workflow required"); process.exit(1); }

    let priority = await ask("Priority (normal/high/urgent) [normal]: ");
    if (!priority) priority = "normal";

    console.log("  Additional context (empty line to finish):");
    const contextLines: string[] = [];
    while (true) {
      const line = await ask("    ");
      if (!line) break;
      contextLines.push(line);
    }

    const fullDescription = contextLines.length
      ? `${description}\n\n## Context\n${contextLines.join("\n")}`
      : description;

    const taskId = doCreateTask(fullDescription, workflowName, priority);
    console.log(`\n  ✓ Task created: ${taskId}\n    workflow: ${workflowName}\n    priority: ${priority}\n`);
    rl.close();
  })().catch((e) => { console.error(e); process.exit(1); });
}

function taskProjects(): void {
  const projects = listProjects();
  if (!projects.length) {
    console.log(`No projects. Create one at: ~/.dispatch/projects/<name>.json`);
    return;
  }
  console.log("Projects:");
  for (const name of projects) {
    try {
      const p = loadProject(name);
      const ws = p.workspace ? `  workspace: ${p.workspace}` : "";
      console.log(`  ${name}${p.description ? ` — ${p.description}` : ""}${ws ? `\n${ws}` : ""}`);
    } catch {
      console.log(`  ${name} (failed to load)`);
    }
  }
}

function doCreateTask(description: string, workflowName: string, priority: string, projectName = ""): string {
  const wf = loadWorkflow(workflowName);
  const firstStep = wf.steps[wf.firstStep];
  if (!firstStep) { console.error(`First step "${wf.firstStep}" not found in workflow`); process.exit(1); }

  // Validate all roles in the workflow have corresponding agent files
  const roleWarnings = validateWorkflowRoles(wf);
  if (roleWarnings.length > 0) {
    console.warn(`\n⚠️  Role validation failed for workflow "${workflowName}":`);
    for (const w of roleWarnings) console.warn(`   • ${w}`);
    console.warn(`   Create the missing agent file(s) in ~/.dispatch/agents/ to fix this.\n`);
  }

  // Validate project hooks against workflow steps — warn but don't block
  if (projectName) {
    try {
      const proj = loadProject(projectName);
      const hookWarnings = validateProjectHooks(proj, wf.steps);
      if (hookWarnings.length > 0) {
        console.warn(`\n⚠️  Hook/workflow mismatch for project "${projectName}" + workflow "${workflowName}":`);
        for (const w of hookWarnings) console.warn(`   • ${w}`);
        console.warn(`   Hooks with no matching step will silently not fire.\n`);
      }
    } catch {
      // loadProject failure is handled downstream — skip validation
    }
  }

  const taskId = newTaskId();
  const role = getRole(firstStep);
  const systemPrompt = loadSystemPromptPublic(role);
  const stepPrompt = `# Task: ${taskId}\n\n${description}\n\n## Step: ${wf.firstStep}\n\n${firstStep.prompt ?? ""}`;
  const prompt = systemPrompt ? `${systemPrompt}\n\n---\n\n${stepPrompt}` : stepPrompt;

  const jobType = (firstStep.agent === "stefan" || firstStep.type === "human") ? "human" : "work";

  createJob({
    task: taskId,
    workflow: workflowName,
    step: wf.firstStep,
    agent: role,
    model: firstStep.model,
    type: jobType,
    priority,
    timeout: firstStep.timeout ?? 120,
    project: projectName || undefined,
    loop: firstStep.loop,
    maxLoopIterations: firstStep.maxLoopIterations,
    prompt,
  });

  // Create artifact dir + save task description
  const artifactDir = path.join(ROOT, "artifacts", taskId);
  fs.mkdirSync(artifactDir, { recursive: true });
  fs.writeFileSync(path.join(artifactDir, "task.md"), description + "\n", "utf8");

  // Notify foreman if running
  const cfg = loadConfig();
  if (fs.existsSync(cfg.pipePath)) {
    try { sendPipe(cfg.pipePath, { type: "new_task", taskId }); } catch {}
  }

  return taskId;
}

function taskList(): void {
  const statePath = path.join(ROOT, "state.json");
  let tasks: Record<string, { workflow: string; currentStep: string; status: string }> = {};
  if (fs.existsSync(statePath)) {
    try { tasks = JSON.parse(fs.readFileSync(statePath, "utf8")).tasks ?? {}; } catch {}
  }

  // Also scan pending/active for tasks not yet in state
  for (const j of [...listJobs("pending"), ...listJobs("active")]) {
    if (j.task && !tasks[j.task]) {
      tasks[j.task] = { workflow: j.workflow, currentStep: j.step, status: "pending" };
    }
  }

  if (!Object.keys(tasks).length) {
    console.log('No tasks. Create one with: dispatch task create "description" --workflow coding-easy');
    return;
  }

  console.log("Tasks:");
  for (const [id, t] of Object.entries(tasks)) {
    console.log(`  ${id} — ${t.status} (${t.workflow}/${t.currentStep})`);
  }
}

function taskShow(taskId: string): void {
  const statePath = path.join(ROOT, "state.json");
  if (fs.existsSync(statePath)) {
    try {
      const s = JSON.parse(fs.readFileSync(statePath, "utf8"));
      if (s.tasks?.[taskId]) {
        console.log(`Task: ${taskId}`);
        console.log(JSON.stringify(s.tasks[taskId], null, 2));
      } else {
        console.log(`Task: ${taskId} (not in state)`);
      }
    } catch {}
  }

  const descPath = path.join(ROOT, "artifacts", taskId, "task.md");
  if (fs.existsSync(descPath)) {
    console.log(`\nDescription:\n  ${fs.readFileSync(descPath, "utf8").trim()}`);
  }

  console.log("\nJobs:");
  for (const folder of ["pending", "active", "done", "failed"]) {
    for (const j of listJobs(folder)) {
      if (j.task === taskId) {
        console.log(`  [${folder}] ${j.id} — step:${j.step} agent:${j.agent}`);
      }
    }
  }
}
