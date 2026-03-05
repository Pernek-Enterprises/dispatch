package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
	"github.com/Pernek-Enterprises/dispatch/internal/pipe"
)

func Done(args []string) {
	f := ParseAgentFlags(args)
	if f.Message == "" && len(f.Artifacts) == 0 {
		fmt.Fprintln(os.Stderr, `dispatch done — report step completion

Usage:
  dispatch done --job <id> "result message"
  dispatch done --job <id> --artifact spec.md "wrote the spec"
  dispatch done --job <id> --task <id> --root /path/to/dispatch "message"

Flags:
  --job, -j       Job ID (or DISPATCH_JOB_ID env)
  --task, -t      Task ID (or DISPATCH_TASK_ID env)
  --root          Dispatch root dir (or DISPATCH_ROOT env)
  --pipe          Named pipe path (or DISPATCH_PIPE env)
  --artifact, -a  Artifact file to copy (repeatable)`)
		os.Exit(1)
	}

	if f.JobID == "" {
		fmt.Fprintln(os.Stderr, "dispatch: --job is required (or set DISPATCH_JOB_ID)")
		os.Exit(1)
	}

	// Copy artifacts
	artifactNames := copyArtifacts(f.Artifacts, f.TaskID)

	// Write result file
	resultPath := filepath.Join(config.Root, "jobs", "active", f.JobID+".result.md")
	content := f.Message
	if len(artifactNames) > 0 {
		content += "\n\nArtifacts: " + join(artifactNames, ", ")
	}
	os.WriteFile(resultPath, []byte(content+"\n"), 0644)

	// Notify foreman
	pipePath := getPipePathWithOverride(f.Pipe)
	err := pipe.Send(pipePath, pipe.Message{
		Type:      "done",
		JobID:     f.JobID,
		TaskID:    f.TaskID,
		Message:   f.Message,
		Artifacts: artifactNames,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch: failed to notify foreman: %v\n", err)
		os.Exit(1)
	}

	suffix := ""
	if len(f.Artifacts) > 0 {
		suffix = fmt.Sprintf(" (%d artifact%s)", len(f.Artifacts), plural(len(f.Artifacts)))
	}
	fmt.Printf("✓ Step complete%s\n", suffix)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func join(ss []string, sep string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += sep
		}
		result += s
	}
	return result
}
