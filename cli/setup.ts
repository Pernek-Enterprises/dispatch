import * as fs from "fs";
import * as path from "path";
import * as readline from "readline";
import { ROOT } from "../config.js";

export function setupCmd(_args: string[]): void {
  const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
  const ask = (prompt: string, def?: string): Promise<string> =>
    new Promise(r => rl.question(def ? `  ${prompt} [${def}]: ` : `  ${prompt}: `, (a) => r(a.trim() || def || "")));
  const confirm = (prompt: string): Promise<boolean> =>
    new Promise(r => rl.question(`  ${prompt} (y/n): `, (a) => r(a.trim().toLowerCase().startsWith("y"))));

  (async () => {
    console.log("\n  ⚡ Dispatch Setup\n  ─────────────────\n");
    console.log(`  Dispatch root: ${ROOT}`);
    if (!await confirm("Use this directory?")) {
      console.log(`\n  Set DISPATCH_ROOT=/your/path in your shell profile and re-run setup.\n`);
      rl.close(); return;
    }

    const dirs = [
      "jobs/pending", "jobs/active", "jobs/done", "jobs/failed",
      "artifacts", "logs", "sessions", "workflows", "agents", "skill",
    ];
    for (const d of dirs) fs.mkdirSync(path.join(ROOT, d), { recursive: true });
    console.log("  ✓ Directory structure created");

    // Config
    const cfgPath = path.join(ROOT, "config.json");
    if (fs.existsSync(cfgPath)) {
      console.log("  ✓ config.json already exists");
    } else {
      const pollMs = parseInt(await ask("Poll interval ms", "30000")) || 30000;
      const pipePath = await ask("Named pipe path", "/tmp/dispatch.pipe");
      const escalationChannel = await ask("Escalation channel (discord/telegram/etc)", "discord");
      const escalationTarget = await ask("Escalation target (channel ID)", "");

      const cfg = {
        pollIntervalMs: pollMs,
        pipePath,
        maxLoopIterations: 3,
        notifications: { escalation: escalationChannel, target: escalationTarget },
        models: {
          "local-9b": { provider: "openai-completions", endpoint: "http://localhost:8081/v1", model: "Qwen3.5-9B-Q4_K_M.gguf" },
          "local-27b": { provider: "openai-completions", endpoint: "http://localhost:8080/v1", model: "Qwen3.5-27B-Q4_K_M.gguf" },
        },
      };
      fs.writeFileSync(cfgPath, JSON.stringify(cfg, null, 2) + "\n", "utf8");
      console.log("  ✓ config.json created");
    }

    console.log("\n  ✓ Setup complete!\n");
    console.log("  Start the foreman with: dispatch foreman");
    console.log("  Create a task with:     dispatch task create \"description\" --workflow <name>\n");
    rl.close();
  })().catch((e) => { console.error(e); rl.close(); process.exit(1); });
}
