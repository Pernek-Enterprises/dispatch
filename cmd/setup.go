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

	// Create directory structure
	dirs := []string{
		"jobs/pending", "jobs/active", "jobs/done", "jobs/failed",
		"artifacts", "logs", "workflows", "sessions", "prompts",
	}
	for _, d := range dirs {
		os.MkdirAll(filepath.Join(root, d), 0755)
	}
	fmt.Println("  ✓ Directory structure created")

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

		// OpenClaw
		fmt.Println()
		fmt.Println("  ── OpenClaw Integration ──")
		fmt.Println()

		openclawBin := ask("OpenClaw binary path", findBinary("openclaw"))

		fmt.Println()
		fmt.Println("  Configure agent mappings (dispatch name → OpenClaw agent ID).")
		fmt.Println("  Enter agents one per line. Empty line to finish.")
		fmt.Println()

		agents := make(map[string]map[string]string)
		for {
			name := ask("Agent name (e.g. kit)", "")
			if name == "" {
				break
			}
			id := ask(fmt.Sprintf("OpenClaw agent ID for '%s'", name), name)
			agents[name] = map[string]string{"id": id}
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
				"escalation": "telegram",
				"channel":    "",
			},
			"openclaw": map[string]interface{}{
				"binary": openclawBin,
				"agents": agents,
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
		fmt.Println("  Configure LLM endpoints (for triage/parse/answer jobs).")
		fmt.Println("  Enter models one per line. Empty line to finish.")
		fmt.Println()

		models := make(map[string]interface{})
		for {
			id := ask("Model ID (e.g. 9b, 27b)", "")
			if id == "" {
				break
			}
			name := ask("Model name", id)
			endpoint := ask("Endpoint URL (e.g. http://localhost:8081/v1)", "")
			provider := ask("Provider string (optional, for OpenClaw)", "")

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
			// Copy example
			copyExample(root, "models.json")
		}
	}

	// ── Agents ──────────────────────────────────────────────────────

	agentsPath := filepath.Join(root, "agents.json")
	if _, err := os.Stat(agentsPath); err == nil {
		fmt.Println("  ✓ agents.json already exists")
	} else {
		fmt.Println()
		fmt.Println("  ── Agents ──")
		fmt.Println("  Configure dispatch agents (role + capabilities).")
		fmt.Println("  Enter agents one per line. Empty line to finish.")
		fmt.Println()

		agents := make(map[string]interface{})
		for {
			name := ask("Agent name (e.g. kit)", "")
			if name == "" {
				break
			}
			role := ask("Role", "coder")
			capsStr := ask("Capabilities (comma-separated)", "code,test")
			caps := strings.Split(capsStr, ",")
			for i := range caps {
				caps[i] = strings.TrimSpace(caps[i])
			}
			agents[name] = map[string]interface{}{
				"role":         role,
				"capabilities": caps,
			}
		}

		if len(agents) > 0 {
			writeJSON(agentsPath, agents)
			fmt.Println("  ✓ agents.json created")
		} else {
			copyExample(root, "agents.json")
		}
	}

	// ── Prompts ─────────────────────────────────────────────────────

	promptsDir := filepath.Join(root, "prompts")
	setupPrompts(promptsDir, root)

	// ── Workflows ───────────────────────────────────────────────────

	setupWorkflows(root)

	// ── State ───────────────────────────────────────────────────────

	statePath := filepath.Join(root, "state.json")
	if _, err := os.Stat(statePath); err != nil {
		writeJSON(statePath, map[string]interface{}{
			"models": map[string]interface{}{},
			"agents": map[string]interface{}{},
			"tasks":  map[string]interface{}{},
		})
		fmt.Println("  ✓ state.json initialized")
	}

	// ── Verify ──────────────────────────────────────────────────────

	fmt.Println()
	fmt.Println("  ── Verification ──")
	fmt.Println()

	// Check openclaw binary
	cfgData, _ := os.ReadFile(configPath)
	var cfgMap map[string]interface{}
	json.Unmarshal(cfgData, &cfgMap)

	ocBin := "openclaw"
	if oc, ok := cfgMap["openclaw"].(map[string]interface{}); ok {
		if b, ok := oc["binary"].(string); ok {
			ocBin = b
		}
	}
	if _, err := exec.LookPath(ocBin); err == nil {
		fmt.Printf("  ✓ %s found in PATH\n", ocBin)
	} else {
		fmt.Printf("  ⚠ %s not found in PATH\n", ocBin)
	}

	// Check dispatch binary
	if _, err := exec.LookPath("dispatch"); err == nil {
		fmt.Println("  ✓ dispatch found in PATH")
	} else {
		fmt.Println("  ⚠ dispatch not in PATH — agents won't be able to call dispatch done/ask/fail")
		fmt.Println("    Fix: sudo cp dispatch /usr/local/bin/dispatch")
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

	fmt.Println()
	fmt.Println("  ── Done! ──")
	fmt.Println()
	fmt.Println("  Start the foreman:")
	fmt.Println("    dispatch foreman")
	fmt.Println()
	fmt.Println("  Create a task:")
	fmt.Printf("    dispatch task create \"Fix the auth bug\" --workflow coding-easy\n")
	fmt.Println()
}

func setupPrompts(promptsDir, root string) {
	os.MkdirAll(promptsDir, 0755)

	systemPath := filepath.Join(promptsDir, "system.md")
	if _, err := os.Stat(systemPath); err != nil {
		// Check for .example
		exPath := filepath.Join(root, "prompts", "system.md.example")
		if data, err := os.ReadFile(exPath); err == nil {
			os.WriteFile(systemPath, data, 0644)
			fmt.Println("  ✓ prompts/system.md created from example")
		} else {
			fmt.Println("  ⚠ No prompts/system.md.example found — create prompts/system.md manually")
		}
	} else {
		fmt.Println("  ✓ prompts/system.md exists")
	}

	// Check for agent-specific prompt examples
	entries, _ := os.ReadDir(promptsDir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md.example") {
			base := strings.TrimSuffix(e.Name(), ".example")
			target := filepath.Join(promptsDir, base)
			if _, err := os.Stat(target); err != nil {
				data, _ := os.ReadFile(filepath.Join(promptsDir, e.Name()))
				os.WriteFile(target, data, 0644)
				fmt.Printf("  ✓ prompts/%s created from example\n", base)
			}
		}
	}
}

func setupWorkflows(root string) {
	wfDir := filepath.Join(root, "workflows")

	// Check if coding-easy exists
	if _, err := os.Stat(filepath.Join(wfDir, "coding-easy.json")); err == nil {
		fmt.Println("  ✓ workflows/coding-easy.json exists")
		return
	}

	// coding-easy.json is in the repo, should already be there
	fmt.Println("  ✓ Workflows directory ready")
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

func checkEndpoint(endpoint string) bool {
	// Quick check — try to hit /v1/models or just connect
	client := &http.Client{Timeout: 3 * time.Second}
	url := strings.TrimSuffix(endpoint, "/") + "/models"
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}
