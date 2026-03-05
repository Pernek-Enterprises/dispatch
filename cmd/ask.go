package cmd

import (
	"fmt"
	"os"

	"github.com/Pernek-Enterprises/dispatch/internal/pipe"
)

func Ask(args []string) {
	escalate := false
	var filtered []string
	for _, a := range args {
		if a == "--escalate" || a == "-e" {
			escalate = true
		} else {
			filtered = append(filtered, a)
		}
	}

	message := joinArgs(filtered)
	if message == "" {
		fmt.Fprintln(os.Stderr, "dispatch: usage: dispatch ask \"question\"")
		os.Exit(1)
	}

	pipePath := getPipePath()
	err := pipe.Send(pipePath, pipe.Message{
		Type:     "ask",
		JobID:    os.Getenv("DISPATCH_JOB_ID"),
		TaskID:   os.Getenv("DISPATCH_TASK_ID"),
		Question: message,
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
