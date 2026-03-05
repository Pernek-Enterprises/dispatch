package cmd

import (
	"fmt"
	"os"

	"github.com/Pernek-Enterprises/dispatch/internal/pipe"
)

func Ask(args []string) {
	// Extract --escalate before parsing standard flags
	escalate := false
	var filtered []string
	for _, a := range args {
		if a == "--escalate" || a == "-e" {
			escalate = true
		} else {
			filtered = append(filtered, a)
		}
	}

	f := ParseAgentFlags(filtered)
	if f.Message == "" {
		fmt.Fprintln(os.Stderr, `dispatch ask — ask a question (blocks until answered)

Usage:
  dispatch ask --job <id> "question"
  dispatch ask --job <id> --escalate "need human help"

Flags:
  --job, -j       Job ID (or DISPATCH_JOB_ID env)
  --task, -t      Task ID (or DISPATCH_TASK_ID env)
  --escalate, -e  Escalate to human`)
		os.Exit(1)
	}

	if f.JobID == "" {
		fmt.Fprintln(os.Stderr, "dispatch: --job is required (or set DISPATCH_JOB_ID)")
		os.Exit(1)
	}

	pipePath := getPipePathWithOverride(f.Pipe)
	err := pipe.Send(pipePath, pipe.Message{
		Type:     "ask",
		JobID:    f.JobID,
		TaskID:   f.TaskID,
		Question: f.Message,
		Escalate: escalate,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch: failed to notify foreman: %v\n", err)
		os.Exit(1)
	}

	suffix := ""
	if escalate {
		suffix = " (escalated to human)"
	}
	fmt.Printf("? Question sent%s\n", suffix)
}

func joinArgs(args []string) string {
	result := ""
	for i, a := range args {
		if i > 0 {
			result += " "
		}
		result += a
	}
	return result
}
