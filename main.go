package main

import (
	"fmt"
	"os"

	"github.com/Pernek-Enterprises/dispatch/cmd"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	switch os.Args[1] {
	case "setup":
		cmd.Setup()
	case "foreman":
		cmd.Foreman()
	case "done":
		cmd.Done(os.Args[2:])
	case "ask":
		cmd.Ask(os.Args[2:])
	case "fail":
		cmd.Fail(os.Args[2:])
	case "answer":
		cmd.Answer(os.Args[2:])
	case "task":
		cmd.Task(os.Args[2:])
	case "workflow":
		cmd.Workflow(os.Args[2:])
	case "sessions":
		cmd.Sessions(os.Args[2:])
	case "status":
		cmd.Status()
	case "--help", "-h", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "dispatch: unknown command %q\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`dispatch — local agent orchestration

Commands:
  dispatch setup                       Interactive setup wizard
  dispatch foreman                     Start the foreman daemon
  dispatch done "message"              Mark step as complete
  dispatch done --artifact file.md     Complete with artifact(s)
  dispatch ask "question"              Ask a question
  dispatch ask --escalate "question"   Ask and escalate to human
  dispatch fail "reason"               Report failure
  dispatch answer --job ID "answer"    Answer a waiting job
  dispatch task create|list|show         Create and manage tasks
  dispatch workflow list|show|validate|create  Manage workflows
  dispatch sessions list|cleanup       Manage agent sessions
  dispatch status                      Show current state

Environment:
  DISPATCH_ROOT      Root directory (default: ~/.dispatch)
  DISPATCH_JOB_ID    Current job ID (set by foreman)
  DISPATCH_TASK_ID   Current task ID (set by foreman)`)
}
