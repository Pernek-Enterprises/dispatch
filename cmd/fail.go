package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
	"github.com/Pernek-Enterprises/dispatch/internal/pipe"
)

func Fail(args []string) {
	f := ParseAgentFlags(args)
	if f.Message == "" {
		fmt.Fprintln(os.Stderr, `dispatch fail — report step failure

Usage:
  dispatch fail --job <id> "reason for failure"

Flags:
  --job, -j       Job ID (or DISPATCH_JOB_ID env)
  --task, -t      Task ID (or DISPATCH_TASK_ID env)
  --root          Dispatch root dir (or DISPATCH_ROOT env)
  --pipe          Named pipe path (or DISPATCH_PIPE env)`)
		os.Exit(1)
	}

	if f.JobID == "" {
		fmt.Fprintln(os.Stderr, "dispatch: --job is required (or set DISPATCH_JOB_ID)")
		os.Exit(1)
	}

	// Write failure result
	resultPath := filepath.Join(config.Root, "jobs", "active", f.JobID+".result.md")
	os.WriteFile(resultPath, []byte("FAILED: "+f.Message+"\n"), 0644)

	pipePath := getPipePathWithOverride(f.Pipe)
	err := pipe.Send(pipePath, pipe.Message{
		Type:   "fail",
		JobID:  f.JobID,
		TaskID: f.TaskID,
		Reason: f.Message,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch: failed to notify foreman: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✗ Step failed: %s\n", f.Message)
}
