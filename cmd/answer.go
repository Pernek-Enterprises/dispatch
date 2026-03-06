package cmd

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
	"github.com/Pernek-Enterprises/dispatch/internal/pipe"
)

// Answer sends an answer to a waiting job, unblocking it.
// Usage: dispatch answer --job <id> [--root <path>] "answer text"
func Answer(args []string) {
	fs := flag.NewFlagSet("answer", flag.ExitOnError)
	jobID := fs.String("job", "", "Job ID to answer")
	root := fs.String("root", "", "Dispatch root directory")
	fs.Parse(args)

	if *root != "" {
		config.Root = *root
	}

	if *jobID == "" {
		fmt.Fprintln(os.Stderr, "Error: --job is required")
		os.Exit(1)
	}

	answer := strings.Join(fs.Args(), " ")
	if answer == "" {
		fmt.Fprintln(os.Stderr, "Error: answer text is required")
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	msg := pipe.Message{
		Type:    "answer",
		JobID:   *jobID,
		Message: answer,
	}
	if err := pipe.Send(cfg.PipePath, msg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Answer sent for job %s\n", *jobID)
}
