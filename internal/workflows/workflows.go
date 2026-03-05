package workflows

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
)

type Step struct {
	Agent         string            `json:"agent,omitempty"` // deprecated, use role
	Role          string            `json:"role,omitempty"`  // e.g. "coder", "reviewer"
	Model         string            `json:"model,omitempty"`
	Timeout       int               `json:"timeout,omitempty"`
	Next          string            `json:"next,omitempty"`
	Branch        map[string]string `json:"branch,omitempty"`
	MaxIterations int               `json:"maxIterations,omitempty"`
	Type          string            `json:"type,omitempty"`
	ArtifactsIn   []string          `json:"artifactsIn,omitempty"`
	ArtifactsOut  []string          `json:"artifactsOut,omitempty"`
	Prompt        string            `json:"-"` // loaded from file
}

type DestroyConfig struct {
	// Which agents to run the destroy prompt on (empty = all agents involved in the workflow)
	Agents  []string `json:"agents,omitempty"`
	// Timeout for each agent's destroy step
	Timeout int      `json:"timeout,omitempty"`
	// Foreman actions to run after agents complete destroy
	Actions []string `json:"actions"`
}

type Workflow struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	FirstStep   string          `json:"firstStep"`
	Steps       map[string]Step `json:"steps"`
	Destroy     DestroyConfig   `json:"destroy"`
}

func Load(name string) (*Workflow, error) {
	p := filepath.Join(config.Root, "workflows", name+".json")
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}

	var wf Workflow
	if err := json.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("invalid workflow %s: %w", name, err)
	}

	// Load step prompts
	promptDir := filepath.Join(config.Root, "workflows", name)
	for stepName, step := range wf.Steps {
		promptPath := filepath.Join(promptDir, stepName+".prompt.md")
		if pdata, err := os.ReadFile(promptPath); err == nil {
			step.Prompt = strings.TrimSpace(string(pdata))
		}
		wf.Steps[stepName] = step
	}

	// Set destroy defaults
	if wf.Destroy.Timeout == 0 {
		wf.Destroy.Timeout = 120
	}
	if len(wf.Destroy.Actions) == 0 {
		wf.Destroy.Actions = []string{"close_sessions", "archive_artifacts"}
	}

	return &wf, nil
}

// GetNextStep determines the next step based on the result text.
func GetNextStep(wf *Workflow, currentStep, result string) string {
	step, ok := wf.Steps[currentStep]
	if !ok {
		return ""
	}

	// Branching — keyword match
	if len(step.Branch) > 0 {
		upper := strings.ToUpper(result)
		for keyword, target := range step.Branch {
			if strings.Contains(upper, strings.ToUpper(keyword)) {
				return target
			}
		}
		return "" // no keyword matched
	}

	// Explicit next
	if step.Next != "" {
		return step.Next
	}

	return "" // terminal step
}

// GetRole returns the effective role for a step (role field, or agent field for compat).
func GetRole(step Step) string {
	if step.Role != "" {
		return step.Role
	}
	return step.Agent // backward compat
}

// GetDestroyAgents returns the agents to run destroy on.
// If destroy.agents is empty, returns all non-human agents used in the workflow.
// GetDestroyAgents returns agents/roles to run destroy on (backward compat).
// With Pi, destroy is optional since processes are ephemeral.
func GetDestroyAgents(wf *Workflow) []string {
	if len(wf.Destroy.Agents) > 0 {
		return wf.Destroy.Agents
	}
	seen := make(map[string]bool)
	var roles []string
	for _, step := range wf.Steps {
		role := GetRole(step)
		if role != "" && role != "stefan" && step.Type != "human" && !seen[role] {
			seen[role] = true
			roles = append(roles, role)
		}
	}
	return roles
}

func ListAll() ([]string, error) {
	d := filepath.Join(config.Root, "workflows")
	entries, err := os.ReadDir(d)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			names = append(names, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	return names, nil
}

// Validate checks a workflow for errors. Returns a slice of error strings.
func Validate(wf *Workflow) []string {
	var errs []string

	if wf.Name == "" {
		errs = append(errs, `missing "name"`)
	}
	if wf.FirstStep == "" {
		errs = append(errs, `missing "firstStep"`)
	}
	if len(wf.Steps) == 0 {
		errs = append(errs, "no steps defined")
		return errs
	}

	stepNames := make(map[string]bool)
	for n := range wf.Steps {
		stepNames[n] = true
	}

	if wf.FirstStep != "" && !stepNames[wf.FirstStep] {
		errs = append(errs, fmt.Sprintf("firstStep %q not found in steps", wf.FirstStep))
	}

	for name, step := range wf.Steps {
		if step.Next != "" && !stepNames[step.Next] {
			errs = append(errs, fmt.Sprintf("step %q: next %q not found", name, step.Next))
		}
		for keyword, target := range step.Branch {
			if !stepNames[target] {
				errs = append(errs, fmt.Sprintf("step %q: branch %s → %q not found", name, keyword, target))
			}
		}
		if step.Agent == "" && step.Role == "" && step.Type == "" {
			errs = append(errs, fmt.Sprintf("step %q: missing role (or agent)", name))
		}
	}

	// Reachability check
	reachable := make(map[string]bool)
	reachable[wf.FirstStep] = true
	changed := true
	for changed {
		changed = false
		for name := range reachable {
			step := wf.Steps[name]
			if step.Next != "" && !reachable[step.Next] {
				reachable[step.Next] = true
				changed = true
			}
			for _, target := range step.Branch {
				if !reachable[target] {
					reachable[target] = true
					changed = true
				}
			}
		}
	}
	for name := range stepNames {
		if !reachable[name] {
			errs = append(errs, fmt.Sprintf("step %q is unreachable from firstStep", name))
		}
	}

	// Validate destroy agent references
	for _, agent := range wf.Destroy.Agents {
		if agent == "stefan" {
			continue
		}
		// Check agent is used in at least one step
		found := false
		for _, step := range wf.Steps {
			if step.Agent == agent {
				found = true
				break
			}
		}
		if !found {
			errs = append(errs, fmt.Sprintf("destroy: agent %q not used in any step", agent))
		}
	}

	// Validate destroy actions
	validActions := map[string]bool{
		"close_sessions":    true,
		"archive_artifacts": true,
		"cleanup_jobs":      true,
	}
	for _, action := range wf.Destroy.Actions {
		if !validActions[action] {
			errs = append(errs, fmt.Sprintf("destroy: unknown action %q", action))
		}
	}

	return errs
}
