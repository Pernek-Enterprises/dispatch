package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
)

// AgentFlags holds the common flags for done/ask/fail commands.
type AgentFlags struct {
	JobID     string
	TaskID    string
	Root      string
	Pipe      string
	Artifacts []string
	Message   string
}

// ParseAgentFlags parses --job, --task, --root, --pipe, --artifact flags
// from args, falling back to env vars for job/task/root/pipe.
func ParseAgentFlags(args []string) AgentFlags {
	var f AgentFlags
	var messageParts []string

	i := 0
	for i < len(args) {
		switch args[i] {
		case "--job", "-j":
			i++
			if i < len(args) {
				f.JobID = args[i]
			}
		case "--task", "-t":
			i++
			if i < len(args) {
				f.TaskID = args[i]
			}
		case "--root":
			i++
			if i < len(args) {
				f.Root = args[i]
			}
		case "--pipe":
			i++
			if i < len(args) {
				f.Pipe = args[i]
			}
		case "--artifact", "-a":
			i++
			if i < len(args) {
				f.Artifacts = append(f.Artifacts, args[i])
			}
		default:
			// Everything else is the message
			messageParts = append(messageParts, args[i])
		}
		i++
	}
	f.Message = joinArgs(messageParts)

	// Fallback to env vars
	if f.JobID == "" {
		f.JobID = os.Getenv("DISPATCH_JOB_ID")
	}
	if f.TaskID == "" {
		f.TaskID = os.Getenv("DISPATCH_TASK_ID")
	}
	if f.Root != "" {
		// Override config.Root for this invocation
		config.Root = f.Root
	}

	return f
}

func getPipePath() string {
	if p := os.Getenv("DISPATCH_PIPE"); p != "" {
		return p
	}
	cfg, err := config.Load()
	if err == nil && cfg.PipePath != "" {
		return cfg.PipePath
	}
	return "/tmp/dispatch.pipe"
}

func getPipePathWithOverride(override string) string {
	if override != "" {
		return override
	}
	return getPipePath()
}

func copyArtifacts(paths []string, taskID string) []string {
	if taskID == "" || len(paths) == 0 {
		names := make([]string, len(paths))
		for i, p := range paths {
			names[i] = filepath.Base(p)
		}
		return names
	}

	artifactDir := filepath.Join(config.Root, "artifacts", taskID)
	os.MkdirAll(artifactDir, 0755)

	var names []string
	for _, p := range paths {
		name := filepath.Base(p)
		src, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warning: artifact not found: %s\n", p)
			names = append(names, name)
			continue
		}
		dst := filepath.Join(artifactDir, name)
		os.WriteFile(dst, src, 0644)
		fmt.Printf("  artifact: %s\n", name)
		names = append(names, name)
	}
	return names
}

// Status prints current state.
func Status() {
	data, err := os.ReadFile(filepath.Join(config.Root, "state.json"))
	if err != nil {
		fmt.Println("No state file found. Is the foreman running?")
		return
	}

	var s map[string]interface{}
	json.Unmarshal(data, &s)

	out, _ := json.MarshalIndent(s, "", "  ")
	fmt.Println(string(out))
}
