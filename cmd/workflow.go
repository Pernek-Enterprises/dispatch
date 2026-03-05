package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
	"github.com/Pernek-Enterprises/dispatch/internal/workflows"
)

func Workflow(args []string) {
	if len(args) == 0 {
		fmt.Println(`dispatch workflow — manage workflow definitions

Commands:
  dispatch workflow list              List available workflows
  dispatch workflow show <name>       Show workflow details
  dispatch workflow validate <name>   Validate a workflow
  dispatch workflow create            Create a new workflow interactively`)
		return
	}

	switch args[0] {
	case "list":
		workflowList()
	case "show":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: dispatch workflow show <name>")
			os.Exit(1)
		}
		workflowShow(args[1])
	case "validate":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: dispatch workflow validate <name>")
			os.Exit(1)
		}
		workflowValidate(args[1])
	case "create":
		workflowCreate()
	default:
		fmt.Fprintf(os.Stderr, "Unknown workflow command: %s\n", args[0])
		os.Exit(1)
	}
}

func workflowList() {
	names, err := workflows.ListAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(names) == 0 {
		fmt.Println("No workflows found. Create one with: dispatch workflow create")
		return
	}
	fmt.Println("Workflows:")
	for _, name := range names {
		wf, err := workflows.Load(name)
		if err != nil {
			fmt.Printf("  %s (error: %v)\n", name, err)
			continue
		}
		fmt.Printf("  %s (%d steps) — %s\n", name, len(wf.Steps), wf.Description)
	}
}

func workflowShow(name string) {
	wf, err := workflows.Load(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n  %s\n", wf.Name)
	if wf.Description != "" {
		fmt.Printf("  %s\n", wf.Description)
	}
	fmt.Println()

	// Render flow
	fmt.Println("  Flow:")
	visited := make(map[string]bool)
	current := wf.FirstStep
	for current != "" && !visited[current] {
		visited[current] = true
		step := wf.Steps[current]
		agent := step.Agent
		if agent == "" {
			agent = step.Type
		}
		model := ""
		if step.Model != "" {
			model = fmt.Sprintf(" [%s]", step.Model)
		}
		timeout := formatTimeout(step.Timeout)

		if len(step.Branch) > 0 {
			branches := make([]string, 0, len(step.Branch))
			for k, v := range step.Branch {
				branches = append(branches, fmt.Sprintf("%s → %s", k, v))
			}
			fmt.Printf("  %s (%s%s %s) → {%s}\n", current, agent, model, timeout, strings.Join(branches, ", "))
			// Follow first branch for display
			for _, v := range step.Branch {
				current = v
				break
			}
		} else if step.Next != "" {
			fmt.Printf("  %s (%s%s %s) → %s\n", current, agent, model, timeout, step.Next)
			current = step.Next
		} else {
			fmt.Printf("  %s (%s%s %s) ■\n", current, agent, model, timeout)
			current = ""
		}
	}
	fmt.Println()

	// Step details
	for name, step := range wf.Steps {
		prompt := step.Prompt
		if len(prompt) > 60 {
			prompt = prompt[:60]
		}
		if prompt == "" {
			prompt = "(no prompt)"
		}

		agent := step.Agent
		if agent == "" {
			agent = step.Type
		}

		fmt.Printf("  ### %s\n", name)
		fmt.Printf("      agent: %s  model: %s  timeout: %s\n", agent, orDash(step.Model), formatTimeout(step.Timeout))
		if len(step.ArtifactsIn) > 0 {
			fmt.Printf("      in: %s\n", strings.Join(step.ArtifactsIn, ", "))
		}
		if len(step.ArtifactsOut) > 0 {
			fmt.Printf("      out: %s\n", strings.Join(step.ArtifactsOut, ", "))
		}
		fmt.Printf("      %s...\n\n", prompt)
	}

	// Destroy config
	destroyAgents := workflows.GetDestroyAgents(wf)
	fmt.Printf("  ### destroy\n")
	fmt.Printf("      agents: %s  timeout: %s\n", strings.Join(destroyAgents, ", "), formatTimeout(wf.Destroy.Timeout))
	fmt.Printf("      actions: %s\n\n", strings.Join(wf.Destroy.Actions, ", "))
}

func workflowValidate(name string) {
	wf, err := workflows.Load(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	errs := workflows.Validate(wf)
	if len(errs) == 0 {
		fmt.Printf("✓ Workflow %q is valid\n", name)

		// Check missing prompts
		promptDir := filepath.Join(config.Root, "workflows", name)
		var missing []string
		for stepName := range wf.Steps {
			p := filepath.Join(promptDir, stepName+".prompt.md")
			if _, err := os.Stat(p); os.IsNotExist(err) {
				missing = append(missing, stepName)
			}
		}
		if len(missing) > 0 {
			fmt.Printf("  ⚠ Missing prompt files: %s\n", strings.Join(missing, ", "))
		}
	} else {
		fmt.Fprintf(os.Stderr, "✗ Workflow %q has %d error(s):\n", name, len(errs))
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  - %s\n", e)
		}
		os.Exit(1)
	}
}

func workflowCreate() {
	reader := bufio.NewReader(os.Stdin)
	ask := func(prompt string) string {
		fmt.Print(prompt)
		line, _ := reader.ReadString('\n')
		return strings.TrimSpace(line)
	}

	fmt.Println("\n  Create a new workflow\n")

	name := ask("  Workflow name (e.g. coding-easy): ")
	if name == "" {
		fmt.Fprintln(os.Stderr, "Name required")
		os.Exit(1)
	}
	description := ask("  Description: ")

	steps := make(map[string]workflows.Step)
	var stepOrder []string

	fmt.Println("\n  Add steps (enter empty name to finish):\n")

	for {
		stepName := ask("  Step name: ")
		if stepName == "" {
			break
		}

		agent := ask("    Agent: ")
		model := ask("    Model (or empty): ")
		timeoutStr := ask("    Timeout (e.g. 10m, 30m, 1h): ")
		nextOrBranch := ask("    Next step (or \"branch\"): ")

		step := workflows.Step{
			Agent:   agent,
			Model:   model,
			Timeout: parseTimeoutInput(timeoutStr),
		}

		if nextOrBranch == "branch" {
			step.Branch = make(map[string]string)
			fmt.Println("    Add branches (empty keyword to finish):")
			for {
				keyword := ask("      Keyword (e.g. ACCEPTED): ")
				if keyword == "" {
					break
				}
				target := ask("      → Target step: ")
				step.Branch[keyword] = target
			}
			maxIter := ask("    Max iterations (default 3): ")
			if n, err := strconv.Atoi(maxIter); err == nil {
				step.MaxIterations = n
			}
		} else if nextOrBranch != "" {
			step.Next = nextOrBranch
		}

		artIn := ask("    Artifacts in (comma-separated, or empty): ")
		artOut := ask("    Artifacts out (comma-separated, or empty): ")
		if artIn != "" {
			step.ArtifactsIn = splitTrim(artIn)
		}
		if artOut != "" {
			step.ArtifactsOut = splitTrim(artOut)
		}

		steps[stepName] = step
		stepOrder = append(stepOrder, stepName)
		fmt.Printf("    ✓ Added step %q\n\n", stepName)
	}

	if len(stepOrder) == 0 {
		fmt.Fprintln(os.Stderr, "No steps added")
		os.Exit(1)
	}

	wf := &workflows.Workflow{
		Name:        name,
		Description: description,
		FirstStep:   stepOrder[0],
		Steps:       steps,
	}

	errs := workflows.Validate(wf)
	if len(errs) > 0 {
		fmt.Println("\n  ⚠ Validation warnings:")
		for _, e := range errs {
			fmt.Printf("    - %s\n", e)
		}
		proceed := ask("\n  Save anyway? (y/n): ")
		if strings.ToLower(proceed) != "y" {
			os.Exit(1)
		}
	}

	// Write JSON
	wfDir := filepath.Join(config.Root, "workflows")
	os.MkdirAll(wfDir, 0755)
	jsonPath := filepath.Join(wfDir, name+".json")
	data, _ := json.MarshalIndent(wf, "", "  ")
	os.WriteFile(jsonPath, append(data, '\n'), 0644)
	fmt.Printf("\n  ✓ Saved %s\n", jsonPath)

	// Create prompt stubs
	promptDir := filepath.Join(wfDir, name)
	os.MkdirAll(promptDir, 0755)
	for _, sn := range stepOrder {
		pp := filepath.Join(promptDir, sn+".prompt.md")
		if _, err := os.Stat(pp); os.IsNotExist(err) {
			os.WriteFile(pp, []byte("<!-- Prompt for "+sn+" step -->\n\nDescribe what the agent should do.\n"), 0644)
		}
	}
	fmt.Printf("  ✓ Created prompt stubs in %s/\n", promptDir)
	fmt.Printf("\n  Next: edit the prompts, then validate:\n    dispatch workflow validate %s\n\n", name)
}

func formatTimeout(seconds int) string {
	if seconds <= 0 {
		return "-"
	}
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	return fmt.Sprintf("%.1fh", float64(seconds)/3600)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func parseTimeoutInput(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 1800
	}
	// Try "10m", "1h", "30s"
	if len(s) > 1 {
		suffix := s[len(s)-1]
		num, err := strconv.Atoi(s[:len(s)-1])
		if err == nil {
			switch suffix {
			case 's':
				return num
			case 'm':
				return num * 60
			case 'h':
				return num * 3600
			}
		}
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return 1800
}

func splitTrim(s string) []string {
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
