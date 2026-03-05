package cmd

import (
	"fmt"
	"os"

	"github.com/Pernek-Enterprises/dispatch/internal/sessions"
)

func Sessions(args []string) {
	if len(args) == 0 || args[0] == "list" {
		sessionsList()
		return
	}

	switch args[0] {
	case "list":
		sessionsList()
	case "cleanup":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: dispatch sessions cleanup <task-id>")
			os.Exit(1)
		}
		sessions.CleanupTask(args[1])
		fmt.Printf("✓ Cleaned up sessions for task %s\n", args[1])
	default:
		fmt.Fprintf(os.Stderr, "Unknown sessions command: %s\n", args[0])
		fmt.Fprintln(os.Stderr, "Commands: list, cleanup <task-id>")
		os.Exit(1)
	}
}

func sessionsList() {
	active, err := sessions.ListActive()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(active) == 0 {
		fmt.Println("No active sessions")
		return
	}

	fmt.Println("Active sessions:")
	for _, s := range active {
		fmt.Printf("  %s/%s  key=%s  model=%s  created=%s\n",
			s.TaskID, s.Agent, s.SessionKey, s.Model, s.Created)
	}
}
