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
	Agent         string            `json:"agent"`
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

type Workflow struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	FirstStep   string          `json:"firstStep"`
	Steps       map[string]Step `json:"steps"`
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

	// Load prompts
	promptDir := filepath.Join(config.Root, "workflows", name)
	for stepName, step := range wf.Steps {
		promptPath := filepath.Join(promptDir, stepName+".prompt.md")
		if pdata, err := os.ReadFile(promptPath); err == nil {
			step.Prompt = strings.TrimSpace(string(pdata))
		}
		wf.Steps[stepName] = step
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
		if step.Agent == "" && step.Type == "" {
			errs = append(errs, fmt.Sprintf("step %q: missing agent", name))
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

	return errs
}
