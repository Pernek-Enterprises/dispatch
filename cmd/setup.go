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
		"artifacts", "logs", "workflows", "agents", "skill",
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

	// ── Models ──────────────────────────────────────────────────────

	modelsPath := filepath.Join(root, "models.json")
	if _, err := os.Stat(modelsPath); err == nil {
		fmt.Println("  ✓ models.json already exists")
	} else {
		fmt.Println()
		fmt.Println("  ── Models ──")
		fmt.Println("  Configure your LLM endpoints. Each model gets its own queue.")
		fmt.Println("  Enter models one per line. Empty name to finish.")
		fmt.Println()

		models := make(map[string]interface{})
		for {
			id := ask("Model ID (e.g. 9b, 27b)", "")
			if id == "" {
				break
			}
			name := ask("Model filename/name", id)
			endpoint := ask("Endpoint URL (e.g. http://localhost:8081/v1)", "")
			provider := ask("Pi provider name (matches ~/.pi/agent/models.json)", "")

			m := map[string]string{
				"name":     name,
				"endpoint": endpoint,
			}
			if provider != "" {
				m["provider"] = provider
			}
			models[id] = m
		}

		if len(models) > 0 {
			writeJSON(modelsPath, models)
			fmt.Println("  ✓ models.json created")
		} else {
			copyExample(root, "models.json")
		}
	}

	// ── Agents ─────────────────────────────────────────────────────

	agentsDir := filepath.Join(root, "agents")
	setupAgents(agentsDir, root)

	// ── Skill ───────────────────────────────────────────────────────

	setupSkill(root)

	// ── Workflows ───────────────────────────────────────────────────

	setupWorkflows(root)

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

	// Check model endpoints
	modelsData, _ := os.ReadFile(modelsPath)
	var modelsMap map[string]interface{}
	json.Unmarshal(modelsData, &modelsMap)
	for id, m := range modelsMap {
		if mm, ok := m.(map[string]interface{}); ok {
			if ep, ok := mm["endpoint"].(string); ok && ep != "" {
				fmt.Printf("  … Checking model %s at %s — ", id, ep)
				if checkEndpoint(ep) {
					fmt.Println("✓ reachable")
				} else {
					fmt.Println("✗ unreachable")
				}
			}
		}
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

func setupAgents(agentsDir, root string) {
	os.MkdirAll(agentsDir, 0755)

	// Copy all .example files to their real counterparts if missing
	examples := []string{"system.md", "coder.md", "reviewer.md"}
	for _, name := range examples {
		target := filepath.Join(agentsDir, name)
		if _, err := os.Stat(target); err != nil {
			exPath := filepath.Join(agentsDir, name+".example")
			if data, err := os.ReadFile(exPath); err == nil {
				os.WriteFile(target, data, 0644)
				fmt.Printf("  ✓ agents/%s created from example\n", name)
			} else {
				fmt.Printf("  ⚠ agents/%s.example not found — create agents/%s manually\n", name, name)
			}
		} else {
			fmt.Printf("  ✓ agents/%s exists\n", name)
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

func setupWorkflows(root string) {
	wfDir := filepath.Join(root, "workflows")

	if _, err := os.Stat(filepath.Join(wfDir, "coding-easy.json")); err == nil {
		fmt.Println("  ✓ workflows/coding-easy.json exists")
	} else {
		fmt.Println("  ⚠ workflows/coding-easy.json missing — should be in the repo")
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

	// Copy prompt .example files and create .md if missing
	srcAgents := filepath.Join(repoDir, "agents")
	dstAgents := filepath.Join(root, "agents")
	entries, err := os.ReadDir(srcAgents)
	if err == nil {
		for _, e := range entries {
			src := filepath.Join(srcAgents, e.Name())
			dst := filepath.Join(dstAgents, e.Name())

			// Always copy .example files
			if strings.HasSuffix(e.Name(), ".example") {
				data, _ := os.ReadFile(src)
				os.WriteFile(dst, data, 0644)

				// Create the real file from example if missing
				realName := strings.TrimSuffix(e.Name(), ".example")
				realPath := filepath.Join(dstAgents, realName)
				if _, err := os.Stat(realPath); err != nil {
					os.WriteFile(realPath, data, 0644)
					fmt.Printf("  ✓ agents/%s created from example\n", realName)
				}
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
