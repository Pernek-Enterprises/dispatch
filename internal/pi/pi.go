package pi

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
	"github.com/Pernek-Enterprises/dispatch/internal/log"
)

// RunOpts configures a Pi invocation.
type RunOpts struct {
	Model        string // e.g. "local-llm/Qwen3.5-27B-Q4_K_M.gguf"
	Prompt       string // the full prompt text
	SystemPrompt string // system prompt (file path or text)
	WorkDir      string // working directory for Pi
	JobID        string // for logging and dispatch instructions
	TaskID       string // for artifact paths
	Tools        []string // tools to enable (default: read,bash,edit,write)
}

// Run starts a Pi process in non-interactive (--print) mode.
// Non-blocking: returns immediately, Pi runs in background.
func Run(opts RunOpts) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config load: %w", err)
	}

	binary := cfg.Pi.Binary
	if binary == "" {
		binary = findPi()
	}

	args := []string{
		"--print",
		"--no-session",
		"--model", opts.Model,
	}

	// System prompt
	if opts.SystemPrompt != "" {
		args = append(args, "--system-prompt", opts.SystemPrompt)
	}

	// Load dispatch skill (teaches Pi how to use done/ask/fail)
	skillDir := filepath.Join(config.Root, "skill")
	args = append(args, "--skill", skillDir)

	// Append job-specific values so the skill's placeholders become concrete
	jobContext := fmt.Sprintf("\n## Your Job\n\nJOB_ID: %s\nDISPATCH_ROOT: %s\nTASK_ID: %s", opts.JobID, config.Root, opts.TaskID)
	args = append(args, "--append-system-prompt", jobContext)

	// Tools
	tools := opts.Tools
	if len(tools) == 0 {
		tools = cfg.Pi.DefaultTools
	}
	if len(tools) == 0 {
		tools = []string{"read", "bash", "edit", "write"}
	}
	args = append(args, "--tools", strings.Join(tools, ","))

	// The prompt itself
	args = append(args, opts.Prompt)

	log.Info("Pi: model=%s job=%s", opts.Model, opts.JobID)

	cmd := exec.Command(binary, args...)

	// Working directory
	if opts.WorkDir != "" {
		cmd.Dir = opts.WorkDir
	} else {
		// Default to artifacts directory for this task
		artifactDir := filepath.Join(config.Root, "artifacts", opts.TaskID)
		os.MkdirAll(artifactDir, 0755)
		cmd.Dir = artifactDir
	}

	// Environment
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("DISPATCH_JOB_ID=%s", opts.JobID),
		fmt.Sprintf("DISPATCH_TASK_ID=%s", opts.TaskID),
		fmt.Sprintf("DISPATCH_ROOT=%s", config.Root),
	)

	// Log output to file
	logDir := filepath.Join(config.Root, "logs")
	os.MkdirAll(logDir, 0755)
	logFile, err := os.Create(filepath.Join(logDir, fmt.Sprintf("pi-%s.log", opts.JobID)))
	if err != nil {
		return fmt.Errorf("create log: %w", err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("pi start: %w", err)
	}

	// Reap in background
	go func() {
		err := cmd.Wait()
		logFile.Close()
		if err != nil {
			log.Error("Pi process %s exited with error: %v", opts.JobID, err)
		} else {
			log.Info("Pi process %s exited cleanly", opts.JobID)
		}
	}()

	return nil
}

// findPi locates the Pi binary.
func findPi() string {
	// Check PATH
	if p, err := exec.LookPath("pi"); err == nil {
		return p
	}
	// Check OpenClaw's bundled Pi
	candidates := []string{
		"/opt/openclaw/node_modules/.bin/pi",
		"/usr/local/bin/pi",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "pi" // hope for the best
}
