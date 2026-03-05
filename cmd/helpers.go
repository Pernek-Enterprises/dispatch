package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
)

func getPipePath() string {
	// Try config.json
	cfg, err := config.Load()
	if err == nil && cfg.PipePath != "" {
		return cfg.PipePath
	}
	if p := os.Getenv("DISPATCH_PIPE"); p != "" {
		return p
	}
	return "/tmp/dispatch.pipe"
}

func parseAgentArgs(args []string) (artifacts []string, message string) {
	i := 0
	for i < len(args) {
		if args[i] == "--artifact" || args[i] == "-a" {
			i++
			if i < len(args) {
				artifacts = append(artifacts, args[i])
			}
		} else {
			message = joinArgs(args[i:])
			break
		}
		i++
	}
	return
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
