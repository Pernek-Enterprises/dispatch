package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
	"github.com/Pernek-Enterprises/dispatch/internal/pipe"
)

func Done(args []string) {
	artifacts, message := parseAgentArgs(args)
	if message == "" && len(artifacts) == 0 {
		fmt.Fprintln(os.Stderr, "dispatch: usage: dispatch done \"message\"")
		os.Exit(1)
	}

	jobID := os.Getenv("DISPATCH_JOB_ID")
	taskID := os.Getenv("DISPATCH_TASK_ID")

	// Copy artifacts
	artifactNames := copyArtifacts(artifacts, taskID)

	// Write result file
	if jobID != "" {
		resultPath := filepath.Join(config.Root, "jobs", "active", jobID+".result.md")
		content := message
		if len(artifactNames) > 0 {
			content += "\n\nArtifacts: " + join(artifactNames, ", ")
		}
		os.WriteFile(resultPath, []byte(content+"\n"), 0644)
	}

	// Notify foreman
	pipePath := getPipePath()
	err := pipe.Send(pipePath, pipe.Message{
		Type:      "done",
		JobID:     jobID,
		TaskID:    taskID,
		Message:   message,
		Artifacts: artifactNames,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch: failed to notify foreman: %v\n", err)
		os.Exit(1)
	}

	suffix := ""
	if len(artifacts) > 0 {
		suffix = fmt.Sprintf(" (%d artifact%s)", len(artifacts), plural(len(artifacts)))
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
