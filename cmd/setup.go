package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
)

func Setup() {
	reader := bufio.NewReader(os.Stdin)
	ask := func(prompt, defaultVal string) string {
		if defaultVal != "" {
			fmt.Printf("  %s [%s]: ", prompt, defaultVal)
		} else {
			fmt.Printf("  %s: ", prompt)
		}
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return defaultVal
		}
		return line
	}
	confirm := func(prompt string) bool {
		fmt.Printf("  %s (y/n): ", prompt)
		line, _ := reader.ReadString('\n')
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y")
	}

	fmt.Println()
	fmt.Println("  ⚡ Dispatch Setup")
	fmt.Println("  ─────────────────")
	fmt.Println()

	root := config.Root
	fmt.Printf("  Dispatch root: %s\n", root)
	if !confirm("Use this directory?") {
		root = ask("Dispatch root directory", root)
		fmt.Println()
		fmt.Printf("  Set DISPATCH_ROOT=%s in your shell profile.\n", root)
	}
	fmt.Println()

	// Detect repo directory (where dispatch binary / git repo lives)
	repoDir := detectRepoDir()
	if repoDir != "" {
		fmt.Printf("  Repo detected: %s\n", repoDir)
	}

	// Create directory structure
	dirs := []string{
		"jobs/pending", "jobs/active", "jobs/done", "jobs/failed",
		"artifacts", "logs", "sessions", "workflows", "agents", "skill",
	}
	for _, d := range dirs {
		os.MkdirAll(filepath.Join(root, d), 0755)
	}
	fmt.Println("  ✓ Directory structure created")

	// Copy repo assets (workflows, prompts, skill) into root
	if repoDir != "" && repoDir != root {
		copyRepoAssets(repoDir, root)
	}

	// ── Config ──────────────────────────────────────────────────────

	configPath := filepath.Join(root, "config.json")
	if _, err := os.Stat(configPath); err == nil {
		fmt.Println("  ✓ config.json already exists")
	} else {
		fmt.Println()
		fmt.Println("  ── Configuration ──")
		fmt.Println()

		pipePath := ask("Named pipe path", "/tmp/dispatch.pipe")
		pollStr := ask("Poll interval (ms)", "30000")
		pollMs, _ := strconv.Atoi(pollStr)
		if pollMs == 0 {
			pollMs = 30000
		}
		maxLoopStr := ask("Max review loop iterations", "3")
		maxLoop, _ := strconv.Atoi(maxLoopStr)
		if maxLoop == 0 {
			maxLoop = 3
		}

		// Pi configuration
		fmt.Println()
		fmt.Println("  ── Pi (Execution Agent) ──")
		fmt.Println()

		piBin := ask("Pi binary path", findBinary("pi"))

		// Escalation
		fmt.Println()
		fmt.Println("  ── Escalation (Human Notifications) ──")
		fmt.Println("  When agents are stuck or jobs fail, dispatch notifies you")
		fmt.Println("  via OpenClaw. Requires OpenClaw running on this machine.")
		fmt.Println()

		escalationChannel := ask("Escalation channel (discord/telegram/none)", "discord")
		escalationTarget := ""
		if escalationChannel != "none" && escalationChannel != "" {
			escalationTarget = ask("Escalation target (e.g. #dispatch, +phone)", "")
		}

		cfg := map[string]interface{}{
			"pollIntervalMs":    pollMs,
			"pipePath":          pipePath,
			"maxLoopIterations": maxLoop,
			"defaultTimeouts": map[string]int{
				"triage": 60,
				"work":   1800,
				"parse":  60,
				"answer": 60,
			},
			"notifications": map[string]string{
				"escalation": escalationChannel,
				"target":     escalationTarget,
			},
			"pi": map[string]interface{}{
				"binary":       piBin,
				"defaultTools": []string{"read", "bash", "edit", "write"},
			},
		}

		writeJSON(configPath, cfg)
		fmt.Println("  ✓ config.json created")
	}

	// ── Agents ─────────────────────────────────────────────────────

	agentsDir := filepath.Join(root, "agents")
	setupAgents(agentsDir, root, confirm)

	// ── Skill ───────────────────────────────────────────────────────

	setupSkill(root)

	// ── Workflows ───────────────────────────────────────────────────

	setupWorkflows(root, confirm, ask)

	// ── State ───────────────────────────────────────────────────────

	statePath := filepath.Join(root, "state.json")
	if _, err := os.Stat(statePath); err != nil {
		writeJSON(statePath, map[string]interface{}{
			"models": map[string]interface{}{},
			"tasks":  map[string]interface{}{},
		})
		fmt.Println("  ✓ state.json initialized")
	}

	// ── Verify ──────────────────────────────────────────────────────

	fmt.Println()
	fmt.Println("  ── Verification ──")
	fmt.Println()

	// Check Pi binary
	cfgData, _ := os.ReadFile(configPath)
	var cfgMap map[string]interface{}
	json.Unmarshal(cfgData, &cfgMap)

	piBin := "pi"
	if pi, ok := cfgMap["pi"].(map[string]interface{}); ok {
		if b, ok := pi["binary"].(string); ok {
			piBin = b
		}
	}
	if _, err := exec.LookPath(piBin); err == nil {
		fmt.Printf("  ✓ Pi (%s) found in PATH\n", piBin)
	} else {
		fmt.Printf("  ⚠ Pi (%s) not found in PATH — install: npm install -g @mariozechner/pi-coding-agent\n", piBin)
	}

	// Check OpenClaw (for escalation)
	if _, err := exec.LookPath("openclaw"); err == nil {
		fmt.Println("  ✓ openclaw found in PATH (escalation available)")
	} else {
		fmt.Println("  ⚠ openclaw not in PATH — escalation notifications won't work")
	}

	// Check dispatch binary in PATH
	if _, err := exec.LookPath("dispatch"); err == nil {
		fmt.Println("  ✓ dispatch found in PATH")
	} else {
		fmt.Println("  ⚠ dispatch not in PATH — Pi agents won't be able to call dispatch done/ask/fail")
		fmt.Printf("    Fix: sudo cp %s/dispatch /usr/local/bin/dispatch\n", root)
	}

	// Check named pipe path
	if pipeP, ok := cfgMap["pipePath"].(string); ok {
		dir := filepath.Dir(pipeP)
		if _, err := os.Stat(dir); err == nil {
			fmt.Printf("  ✓ Pipe directory exists: %s\n", dir)
		} else {
			fmt.Printf("  ⚠ Pipe directory missing: %s\n", dir)
		}
	}

	// Check Pi models config
	home, _ := os.UserHomeDir()
	piModelsPath := filepath.Join(home, ".pi", "agent", "models.json")
	if _, err := os.Stat(piModelsPath); err == nil {
		fmt.Printf("  ✓ Pi models config exists: %s\n", piModelsPath)
	} else {
		fmt.Printf("  ⚠ Pi models config missing: %s\n", piModelsPath)
		fmt.Println("    Create it with your local model endpoints so Pi can reach llama-server")
	}

	// Check skill
	skillPath := filepath.Join(root, "skill", "SKILL.md")
	if _, err := os.Stat(skillPath); err == nil {
		fmt.Println("  ✓ skill/SKILL.md exists")
	} else {
		fmt.Println("  ⚠ skill/SKILL.md missing — Pi agents won't know dispatch commands")
	}

	fmt.Println()
	fmt.Println("  ── Done! ──")
	fmt.Println()
	fmt.Println("  Start the foreman:")
	fmt.Println("    ./dispatch foreman")
	fmt.Println()
	fmt.Println("  Create a task:")
	fmt.Printf("    ./dispatch task create \"Fix the auth bug\" --workflow coding-easy\n")
	fmt.Println()
}

func setupAgents(agentsDir, root string, confirm func(string) bool) {
	os.MkdirAll(agentsDir, 0755)

	// Always copy system.md from example (shared base prompt)
	systemTarget := filepath.Join(agentsDir, "system.md")
	if _, err := os.Stat(systemTarget); err != nil {
		exPath := filepath.Join(agentsDir, "system.md.example")
		if data, err := os.ReadFile(exPath); err == nil {
			os.WriteFile(systemTarget, data, 0644)
			fmt.Println("  ✓ agents/system.md created from example")
		}
	} else {
		fmt.Println("  ✓ agents/system.md exists")
	}

	// Check for existing custom agents
	hasAgents := false
	entries, _ := os.ReadDir(agentsDir)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") && e.Name() != "system.md" && !strings.HasSuffix(e.Name(), ".example") {
			hasAgents = true
			fmt.Printf("  ✓ agents/%s exists\n", e.Name())
		}
	}

	if !hasAgents {
		fmt.Println()
		fmt.Println("  No agent identities found.")
		fmt.Println("  The default agents are a generic coder and reviewer.")
		fmt.Println("  You can customize or delete them later.")
		if confirm("Create default coder + reviewer agents for quick start?") {
			for _, name := range []string{"coder.md", "reviewer.md"} {
				exPath := filepath.Join(agentsDir, name+".example")
				if data, err := os.ReadFile(exPath); err == nil {
					os.WriteFile(filepath.Join(agentsDir, name), data, 0644)
					fmt.Printf("  ✓ agents/%s created\n", name)
				}
			}
		} else {
			fmt.Println("  Skipped — create your own agents in agents/*.md")
		}
	}
}

func setupSkill(root string) {
	skillDir := filepath.Join(root, "skill")
	os.MkdirAll(skillDir, 0755)

	skillPath := filepath.Join(skillDir, "SKILL.md")
	if _, err := os.Stat(skillPath); err == nil {
		fmt.Println("  ✓ skill/SKILL.md exists")
	} else {
		fmt.Println("  ⚠ skill/SKILL.md missing — should be in the repo. Re-clone or copy from GitHub.")
	}
}

func setupWorkflows(root string, confirm func(string) bool, ask func(string, string) string) {
	wfDir := filepath.Join(root, "workflows")

	if _, err := os.Stat(filepath.Join(wfDir, "coding-easy.json")); err == nil {
		fmt.Println("  ✓ workflows/coding-easy.json exists")
	} else {
		fmt.Println()
		fmt.Println("  No workflows found.")
		fmt.Println("  The starter workflow (coding-easy) runs: spec → code → review → ready.")
		if confirm("Install the coding-easy starter workflow?") {
			fmt.Println()
			fmt.Println("  Model names must match your Pi config (~/.pi/agent/models.json).")
			fmt.Println("  Format: provider/model-id (e.g. local-27b/Qwen3.5-27B-Q4_K_M.gguf)")
			fmt.Println("  Run 'pi --list-models' to see available models.")
			fmt.Println()
			largeModel := ask("Large model (for spec, fix steps)", "")
			smallModel := ask("Small model (for code, review steps)", "")
			if largeModel != "" && smallModel != "" {
				createCodingEasyWorkflow(wfDir, largeModel, smallModel)
				fmt.Println("  ✓ workflows/coding-easy.json created")
			} else {
				fmt.Println("  ⚠ Skipped — need both model names")
			}
		} else {
			fmt.Println("  Skipped — create your own workflows in workflows/")
			return
		}
	}

	// Check prompt files
	promptDir := filepath.Join(wfDir, "coding-easy")
	prompts := []string{"spec.prompt.md", "code.prompt.md", "review.prompt.md", "fix.prompt.md", "ready.prompt.md"}
	missing := 0
	for _, p := range prompts {
		if _, err := os.Stat(filepath.Join(promptDir, p)); err != nil {
			missing++
		}
	}
	if missing == 0 {
		fmt.Println("  ✓ workflows/coding-easy/ prompts all present")
	} else {
		fmt.Printf("  ⚠ workflows/coding-easy/ missing %d prompt file(s)\n", missing)
	}
}

// ── Helpers ─────────────────────────────────────────────────────────

func findBinary(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}

func writeJSON(path string, v interface{}) {
	data, _ := json.MarshalIndent(v, "", "  ")
	os.WriteFile(path, append(data, '\n'), 0644)
}

func copyExample(root, name string) {
	src := filepath.Join(root, name+".example")
	dst := filepath.Join(root, name)
	if data, err := os.ReadFile(src); err == nil {
		os.WriteFile(dst, data, 0644)
		fmt.Printf("  ✓ %s created from example\n", name)
	} else {
		fmt.Printf("  ⚠ %s.example not found — create %s manually\n", name, name)
	}
}

func detectRepoDir() string {
	// Try: directory where the binary lives
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		if isRepoDir(dir) {
			return dir
		}
	}
	// Try: current working directory
	cwd, err := os.Getwd()
	if err == nil {
		if isRepoDir(cwd) {
			return cwd
		}
	}
	return ""
}

func isRepoDir(dir string) bool {
	// Check for dispatch repo markers
	markers := []string{"SPEC.md", "skill/SKILL.md", "workflows/coding-easy.json"}
	for _, m := range markers {
		if _, err := os.Stat(filepath.Join(dir, m)); err != nil {
			return false
		}
	}
	return true
}

func copyRepoAssets(repoDir, root string) {
	// Copy skill/
	copyDirContents(filepath.Join(repoDir, "skill"), filepath.Join(root, "skill"))

	// Copy workflows/
	copyDirRecursive(filepath.Join(repoDir, "workflows"), filepath.Join(root, "workflows"))

	// Copy .example files only — user creates actual agents during setup
	srcAgents := filepath.Join(repoDir, "agents")
	dstAgents := filepath.Join(root, "agents")
	os.MkdirAll(dstAgents, 0755)
	entries, err := os.ReadDir(srcAgents)
	if err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".example") {
				data, _ := os.ReadFile(filepath.Join(srcAgents, e.Name()))
				os.WriteFile(filepath.Join(dstAgents, e.Name()), data, 0644)
			}
		}
	}

	fmt.Println("  ✓ Repo assets copied to dispatch root")
}

func copyDirContents(src, dst string) {
	os.MkdirAll(dst, 0755)
	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err == nil {
			os.WriteFile(filepath.Join(dst, e.Name()), data, 0644)
		}
	}
}

func copyDirRecursive(src, dst string) {
	os.MkdirAll(dst, 0755)
	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			copyDirRecursive(srcPath, dstPath)
		} else {
			data, err := os.ReadFile(srcPath)
			if err == nil {
				os.WriteFile(dstPath, data, 0644)
			}
		}
	}
}

func createCodingEasyWorkflow(wfDir, largeModel, smallModel string) {
	os.MkdirAll(wfDir, 0755)
	wf := map[string]interface{}{
		"name":        "coding-easy",
		"description": "Simple coding tasks — bug fixes, small features, straightforward changes.",
		"firstStep":   "spec",
		"steps": map[string]interface{}{
			"spec": map[string]interface{}{
				"role": "coder", "model": largeModel, "timeout": 600,
				"next": "code", "artifactsOut": []string{"spec.md"},
			},
			"code": map[string]interface{}{
				"role": "coder", "model": smallModel, "timeout": 1800,
				"next": "review", "artifactsIn": []string{"spec.md"}, "artifactsOut": []string{"diff.patch"},
			},
			"review": map[string]interface{}{
				"role": "reviewer", "model": smallModel, "timeout": 900,
				"branch": map[string]string{"ACCEPTED": "ready", "DENIED": "fix"},
				"maxIterations": 3,
				"artifactsIn":   []string{"spec.md", "diff.patch"}, "artifactsOut": []string{"review.md"},
			},
			"fix": map[string]interface{}{
				"role": "coder", "model": largeModel, "timeout": 1200,
				"next": "review", "artifactsIn": []string{"review.md", "spec.md"}, "artifactsOut": []string{"diff.patch"},
			},
			"ready": map[string]interface{}{
				"type": "human", "timeout": 0,
			},
		},
		"destroy": map[string]interface{}{
			"timeout": 60,
			"actions": []string{"archive_artifacts", "cleanup_jobs"},
		},
	}
	writeJSON(filepath.Join(wfDir, "coding-easy.json"), wf)
}

func checkEndpoint(endpoint string) bool {
	client := &http.Client{Timeout: 3 * time.Second}
	url := strings.TrimSuffix(endpoint, "/") + "/models"
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}
