package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
	"github.com/Pernek-Enterprises/dispatch/internal/pipe"
)

func Fail(args []string) {
	message := joinArgs(args)
	if message == "" {
		fmt.Fprintln(os.Stderr, "dispatch: usage: dispatch fail \"reason\"")
		os.Exit(1)
	}

	jobID := os.Getenv("DISPATCH_JOB_ID")
	taskID := os.Getenv("DISPATCH_TASK_ID")

	// Write failure result
	if jobID != "" {
		resultPath := filepath.Join(config.Root, "jobs", "active", jobID+".result.md")
		os.WriteFile(resultPath, []byte("FAILED: "+message+"\n"), 0644)
	}

	pipePath := getPipePath()
	err := pipe.Send(pipePath, pipe.Message{
		Type:   "fail",
		JobID:  jobID,
		TaskID: taskID,
		Reason: message,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch: failed to notify foreman: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✗ Step failed: %s\n", message)
}
