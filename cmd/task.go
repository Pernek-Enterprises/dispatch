package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
	"github.com/Pernek-Enterprises/dispatch/internal/jobs"
	"github.com/Pernek-Enterprises/dispatch/internal/log"
	"github.com/Pernek-Enterprises/dispatch/internal/pipe"
	"github.com/Pernek-Enterprises/dispatch/internal/workflows"
)

func Task(args []string) {
	if len(args) == 0 {
		fmt.Println(`dispatch task — manage tasks

Commands:
  dispatch task create "description" --workflow coding-easy
  dispatch task create --interactive
  dispatch task list
  dispatch task show <task-id>`)
		return
	}

	switch args[0] {
	case "create":
		taskCreate(args[1:])
	case "list":
		taskList()
	case "show":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: dispatch task show <task-id>")
			os.Exit(1)
		}
		taskShow(args[1])
	default:
		fmt.Fprintf(os.Stderr, "Unknown task command: %s\n", args[0])
		os.Exit(1)
	}
}

func taskCreate(args []string) {
	var description string
	var workflowName string
	var priority string
	interactive := false

	// Parse flags (flags can appear anywhere)
	var descParts []string
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--workflow", "-w":
			i++
			if i < len(args) {
				workflowName = args[i]
			}
		case "--priority", "-p":
			i++
			if i < len(args) {
				priority = args[i]
			}
		case "--interactive", "-i":
			interactive = true
		default:
			descParts = append(descParts, args[i])
		}
		i++
	}
	description = strings.Join(descParts, " ")

	if interactive {
		taskCreateInteractive()
		return
	}

	if description == "" {
		fmt.Fprintln(os.Stderr, "Usage: dispatch task create \"description\" --workflow coding-easy")
		os.Exit(1)
	}

	// Default workflow
	if workflowName == "" {
		// List available and pick first, or error
		available, _ := workflows.ListAll()
		if len(available) == 1 {
			workflowName = available[0]
		} else {
			fmt.Fprintln(os.Stderr, "Multiple workflows available. Specify one with --workflow:")
			for _, w := range available {
				fmt.Fprintf(os.Stderr, "  %s\n", w)
			}
			os.Exit(1)
		}
	}

	if priority == "" {
		priority = "normal"
	}

	taskID := createTask(description, workflowName, priority)
	fmt.Printf("✓ Task created: %s\n  workflow: %s\n  priority: %s\n", taskID, workflowName, priority)
}

func taskCreateInteractive() {
	reader := bufio.NewReader(os.Stdin)
	ask := func(prompt string) string {
		fmt.Print(prompt)
		line, _ := reader.ReadString('\n')
		return strings.TrimSpace(line)
	}

	fmt.Println("\n  Create a new task\n")

	description := ask("  Description: ")
	if description == "" {
		fmt.Fprintln(os.Stderr, "Description required")
		os.Exit(1)
	}

	// Show available workflows
	available, _ := workflows.ListAll()
	fmt.Println("\n  Available workflows:")
	for _, w := range available {
		wf, _ := workflows.Load(w)
		desc := ""
		if wf != nil {
			desc = " — " + wf.Description
		}
		fmt.Printf("    %s%s\n", w, desc)
	}
	workflowName := ask("\n  Workflow: ")
	if workflowName == "" && len(available) == 1 {
		workflowName = available[0]
	}
	if workflowName == "" {
		fmt.Fprintln(os.Stderr, "Workflow required")
		os.Exit(1)
	}

	priority := ask("  Priority (normal/high/urgent) [normal]: ")
	if priority == "" {
		priority = "normal"
	}

	// Optional: multi-line context
	fmt.Println("  Additional context (empty line to finish):")
	var contextLines []string
	for {
		line := ask("    ")
		if line == "" {
			break
		}
		contextLines = append(contextLines, line)
	}

	fullDescription := description
	if len(contextLines) > 0 {
		fullDescription += "\n\n## Context\n" + strings.Join(contextLines, "\n")
	}

	taskID := createTask(fullDescription, workflowName, priority)
	fmt.Printf("\n  ✓ Task created: %s\n    workflow: %s\n    priority: %s\n\n", taskID, workflowName, priority)
}

func createTask(description, workflowName, priority string) string {
	// Validate workflow exists
	wf, err := workflows.Load(workflowName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workflow %q: %v\n", workflowName, err)
		os.Exit(1)
	}

	// Get first step
	firstStep, ok := wf.Steps[wf.FirstStep]
	if !ok {
		fmt.Fprintf(os.Stderr, "First step %q not found in workflow\n", wf.FirstStep)
		os.Exit(1)
	}

	// Generate task ID
	taskID := jobs.NewTaskID()

	// Build the prompt for the first step
	prompt := fmt.Sprintf("# Task: %s\n\n%s\n\n## Step: %s\n\n%s",
		taskID, description, wf.FirstStep, firstStep.Prompt)

	artifactDir := filepath.Join(config.Root, "artifacts")

	jobType := "work"
	if firstStep.Agent == "stefan" || firstStep.Type == "human" {
		jobType = "human"
	}

	// Create the first job
	_, err = jobs.Create(jobs.CreateOpts{
		Task:     taskID,
		Workflow: workflowName,
		Step:     wf.FirstStep,
		Agent:    firstStep.Agent,
		Model:    firstStep.Model,
		Type:     jobType,
		Priority: priority,
		Timeout:  firstStep.Timeout,
		Prompt:   prompt,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating job: %v\n", err)
		os.Exit(1)
	}

	// Create artifacts dir for this task
	os.MkdirAll(filepath.Join(artifactDir, taskID), 0755)

	// Save the task description as an artifact
	os.WriteFile(
		filepath.Join(artifactDir, taskID, "task.md"),
		[]byte(description+"\n"),
		0644,
	)

	// Notify foreman if pipe exists
	cfg, _ := config.Load()
	pipePath := "/tmp/dispatch.pipe"
	if cfg != nil && cfg.PipePath != "" {
		pipePath = cfg.PipePath
	}
	if _, err := os.Stat(pipePath); err == nil {
		pipe.Send(pipePath, pipe.Message{
			Type:   "new_task",
			TaskID: taskID,
		})
	}

	log.Info("Task created: %s (workflow=%s)", taskID, workflowName)
	return taskID
}

func taskList() {
	// Show tasks from state.json + scan job folders
	statePath := filepath.Join(config.Root, "state.json")
	data, _ := os.ReadFile(statePath)

	var s struct {
		Tasks map[string]struct {
			Workflow    string `json:"workflow"`
			CurrentStep string `json:"currentStep"`
			Status     string `json:"status"`
		} `json:"tasks"`
	}
	json.Unmarshal(data, &s)

	// Also scan pending/active for tasks not yet in state
	pendingJobs, _ := jobs.List("pending")
	activeJobs, _ := jobs.List("active")
	allJobs := append(pendingJobs, activeJobs...)

	// Collect unique tasks
	tasks := make(map[string]string) // taskID → status
	for id, t := range s.Tasks {
		tasks[id] = fmt.Sprintf("%s (%s/%s)", t.Status, t.Workflow, t.CurrentStep)
	}
	for _, j := range allJobs {
		if j.Task != "" {
			if _, exists := tasks[j.Task]; !exists {
				tasks[j.Task] = fmt.Sprintf("pending (%s/%s)", j.Workflow, j.Step)
			}
		}
	}

	if len(tasks) == 0 {
		fmt.Println("No tasks. Create one with: dispatch task create \"description\" --workflow coding-easy")
		return
	}

	fmt.Println("Tasks:")
	for id, status := range tasks {
		fmt.Printf("  %s — %s\n", id, status)
	}
}

func taskShow(taskID string) {
	// Check state
	statePath := filepath.Join(config.Root, "state.json")
	data, _ := os.ReadFile(statePath)

	var s struct {
		Tasks map[string]json.RawMessage `json:"tasks"`
	}
	json.Unmarshal(data, &s)

	if taskData, ok := s.Tasks[taskID]; ok {
		var pretty map[string]interface{}
		json.Unmarshal(taskData, &pretty)
		out, _ := json.MarshalIndent(pretty, "  ", "  ")
		fmt.Printf("Task: %s\n  %s\n", taskID, string(out))
	} else {
		fmt.Printf("Task: %s (not in state)\n", taskID)
	}

	// Show task description
	descPath := filepath.Join(config.Root, "artifacts", taskID, "task.md")
	if desc, err := os.ReadFile(descPath); err == nil {
		fmt.Printf("\nDescription:\n  %s\n", strings.TrimSpace(string(desc)))
	}

	// Show related jobs
	fmt.Println("\nJobs:")
	for _, folder := range []string{"pending", "active", "done", "failed"} {
		jobList, _ := jobs.List(folder)
		for _, j := range jobList {
			if j.Task == taskID || strings.HasPrefix(j.ID, taskID[:8]) {
				fmt.Printf("  [%s] %s — step:%s agent:%s\n", folder, j.ID, j.Step, j.Agent)
			}
		}
	}
}
