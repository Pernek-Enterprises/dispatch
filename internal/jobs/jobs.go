package jobs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
)

type Job struct {
	ID        string `json:"id"`
	Task      string `json:"task"`
	Workflow  string `json:"workflow"`
	Step      string `json:"step"`
	Agent     string `json:"agent"`
	Model     string `json:"model,omitempty"`
	Type      string `json:"type"`
	Priority  string `json:"priority"`
	Created   string `json:"created"`
	Timeout   int    `json:"timeout"`
	Iteration int    `json:"iteration,omitempty"`
	Prompt    string `json:"-"` // loaded separately
}

var slugRe = regexp.MustCompile(`[^a-z0-9-]`)

func newID(step, task string) string {
	b := make([]byte, 4)
	rand.Read(b)
	slug := slugRe.ReplaceAllString(strings.ToLower(step+"-"+task), "-")
	if len(slug) > 40 {
		slug = slug[:40]
	}
	return fmt.Sprintf("%s-%s", hex.EncodeToString(b), slug)
}

func dir(folder string) string {
	return filepath.Join(config.Root, "jobs", folder)
}

type CreateOpts struct {
	Task      string
	Workflow  string
	Step      string
	Agent     string
	Model     string
	Type      string
	Priority  string
	Timeout   int
	Iteration int
	Prompt    string
}

func Create(opts CreateOpts) (string, error) {
	id := newID(opts.Step, opts.Task)
	if opts.Type == "" {
		opts.Type = "work"
	}
	if opts.Priority == "" {
		opts.Priority = "normal"
	}
	if opts.Iteration == 0 {
		opts.Iteration = 1
	}

	job := Job{
		ID:        id,
		Task:      opts.Task,
		Workflow:  opts.Workflow,
		Step:      opts.Step,
		Agent:     opts.Agent,
		Model:     opts.Model,
		Type:      opts.Type,
		Priority:  opts.Priority,
		Created:   time.Now().UTC().Format(time.RFC3339),
		Timeout:   opts.Timeout,
		Iteration: opts.Iteration,
	}

	d := dir("pending")
	os.MkdirAll(d, 0755)

	data, _ := json.MarshalIndent(job, "", "  ")
	if err := os.WriteFile(filepath.Join(d, id+".json"), append(data, '\n'), 0644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(d, id+".prompt.md"), []byte(opts.Prompt+"\n"), 0644); err != nil {
		return "", err
	}
	return id, nil
}

func List(folder string) ([]Job, error) {
	d := dir(folder)
	entries, err := os.ReadDir(d)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var jobs []Job
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(d, e.Name()))
		if err != nil {
			continue
		}
		var j Job
		if json.Unmarshal(data, &j) != nil {
			continue
		}
		// Load prompt
		promptPath := filepath.Join(d, strings.TrimSuffix(e.Name(), ".json")+".prompt.md")
		if pdata, err := os.ReadFile(promptPath); err == nil {
			j.Prompt = string(pdata)
		}
		jobs = append(jobs, j)
	}

	// Sort by priority then created
	prioMap := map[string]int{"urgent": 0, "high": 1, "normal": 2, "low": 3}
	sort.Slice(jobs, func(i, k int) bool {
		pi := prioMap[jobs[i].Priority]
		pk := prioMap[jobs[k].Priority]
		if pi != pk {
			return pi < pk
		}
		return jobs[i].Created < jobs[k].Created
	})

	return jobs, nil
}

func Move(id, from, to string) error {
	fromDir := dir(from)
	toDir := dir(to)
	os.MkdirAll(toDir, 0755)

	for _, ext := range []string{".json", ".prompt.md", ".result.md"} {
		src := filepath.Join(fromDir, id+ext)
		dst := filepath.Join(toDir, id+ext)
		if _, err := os.Stat(src); err == nil {
			if err := os.Rename(src, dst); err != nil {
				return err
			}
		}
	}
	return nil
}

func ReadResult(id, folder string) string {
	data, err := os.ReadFile(filepath.Join(dir(folder), id+".result.md"))
	if err != nil {
		return ""
	}
	return string(data)
}

func WriteResult(id, folder, content string) error {
	return os.WriteFile(filepath.Join(dir(folder), id+".result.md"), []byte(content+"\n"), 0644)
}

func GetMeta(id, folder string) *Job {
	data, err := os.ReadFile(filepath.Join(dir(folder), id+".json"))
	if err != nil {
		return nil
	}
	var j Job
	if json.Unmarshal(data, &j) != nil {
		return nil
	}
	return &j
}
