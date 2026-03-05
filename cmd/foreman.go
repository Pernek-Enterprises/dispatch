package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
	"github.com/Pernek-Enterprises/dispatch/internal/jobs"
	"github.com/Pernek-Enterprises/dispatch/internal/llm"
	"github.com/Pernek-Enterprises/dispatch/internal/log"
	"github.com/Pernek-Enterprises/dispatch/internal/pipe"
	"github.com/Pernek-Enterprises/dispatch/internal/state"
	"github.com/Pernek-Enterprises/dispatch/internal/workflows"
)

func Foreman() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n  %v\n\n", err)
		fmt.Fprintln(os.Stderr, "  To get started:")
		fmt.Fprintln(os.Stderr, "    cp config.json.example config.json")
		fmt.Fprintln(os.Stderr, "    cp models.json.example models.json")
		fmt.Fprintln(os.Stderr, "    cp agents.json.example agents.json")
		fmt.Fprintln(os.Stderr, "  Then edit each file for your installation.")
		os.Exit(1)
	}

	config.EnsureDirs()
	st := state.Load()

	// Initialize model/agent locks from config
	models, _ := config.LoadModels()
	for id := range models {
		if !st.IsModelFree(id) && st.Models[id] == nil {
			st.Models[id] = &state.ModelLock{}
		}
	}
	agents, _ := config.LoadAgents()
	for id := range agents {
		if st.Agents[id] == nil {
			st.Agents[id] = &state.AgentLock{}
		}
	}
	st.Save()

	log.Info("Foreman starting (root=%s, poll=%dms)", config.Root, cfg.PollIntervalMs)

	// Named pipe
	if err := pipe.Create(cfg.PipePath); err != nil {
		log.Error("Failed to create pipe: %v", err)
		os.Exit(1)
	}
	log.Info("Listening on pipe: %s", cfg.PipePath)

	// Pipe listener (blocking goroutine)
	go pipe.Listen(cfg.PipePath, func(msg pipe.Message) {
		handleEvent(cfg, st, msg)
	})

	// Initial dispatch
	dispatchPending(cfg, st)
	st.Save()

	// Poll timer
	ticker := time.NewTicker(time.Duration(cfg.PollIntervalMs) * time.Millisecond)
	defer ticker.Stop()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	log.Info("Foreman running")

	for {
		select {
		case <-ticker.C:
			healthCheck(cfg, st)
			dispatchPending(cfg, st)
			st.Save()
		case sig := <-sigCh:
			log.Info("Shutting down (signal: %v)", sig)
			st.Save()
			os.Remove(cfg.PipePath)
			os.Exit(0)
		}
	}
}

func handleEvent(cfg *config.Config, st *state.State, msg pipe.Message) {
	log.Info("CLI event: %s (job=%s)", msg.Type, msg.JobID)

	switch msg.Type {
	case "done":
		handleDone(cfg, st, msg)
	case "fail":
		handleFail(cfg, st, msg)
	case "ask":
		handleAsk(cfg, st, msg)
	default:
		log.Warn("Unknown event type: %s", msg.Type)
	}

	dispatchPending(cfg, st)
	st.Save()
}

func handleDone(cfg *config.Config, st *state.State, msg pipe.Message) {
	meta := jobs.GetMeta(msg.JobID, "active")
	if meta == nil {
		log.Warn("Job %s not found in active/", msg.JobID)
		return
	}

	result := jobs.ReadResult(msg.JobID, "active")
	log.Info("Job done: %s (step=%s)", msg.JobID, meta.Step)

	if meta.Model != "" {
		st.UnlockModel(meta.Model)
	}
	if meta.Agent != "" && meta.Agent != "stefan" {
		st.UnlockAgent(meta.Agent)
	}

	jobs.Move(msg.JobID, "active", "done")
	advanceWorkflow(cfg, st, meta, result)
}

func handleFail(cfg *config.Config, st *state.State, msg pipe.Message) {
	meta := jobs.GetMeta(msg.JobID, "active")
	if meta == nil {
		log.Warn("Job %s not found in active/", msg.JobID)
		return
	}

	log.Warn("Job failed: %s — %s", msg.JobID, msg.Reason)

	if meta.Model != "" {
		st.UnlockModel(meta.Model)
	}
	if meta.Agent != "" && meta.Agent != "stefan" {
		st.UnlockAgent(meta.Agent)
	}

	jobs.Move(msg.JobID, "active", "failed")
	// TODO: escalation notification
}

func handleAsk(cfg *config.Config, st *state.State, msg pipe.Message) {
	log.Info("Question from %s: %s", msg.JobID, msg.Question)

	if msg.Escalate {
		// TODO: send to Stefan via Telegram
		log.Info("Escalated to human: %s", msg.Question)
		return
	}

	meta := jobs.GetMeta(msg.JobID, "active")
	taskID := msg.TaskID
	if meta != nil {
		taskID = meta.Task
	}

	jobs.Create(jobs.CreateOpts{
		Task:     taskID,
		Workflow: "answer",
		Step:     "answer",
		Model:    "9b", // TODO: configurable default answer model
		Type:     "answer",
		Priority: "high",
		Timeout:  60,
		Prompt:   fmt.Sprintf("An agent has a question:\n\n%s\n\nProvide a clear, actionable answer.", msg.Question),
	})
}

func advanceWorkflow(cfg *config.Config, st *state.State, completedJob *jobs.Job, result string) {
	wf, err := workflows.Load(completedJob.Workflow)
	if err != nil {
		log.Warn("Workflow %s not found: %v", completedJob.Workflow, err)
		return
	}

	nextStepName := workflows.GetNextStep(wf, completedJob.Step, result)
	if nextStepName == "" {
		log.Info("Task %s complete (workflow=%s)", completedJob.Task, completedJob.Workflow)
		if ts, ok := st.Tasks[completedJob.Task]; ok {
			ts.Status = "complete"
		}
		return
	}

	nextStep, ok := wf.Steps[nextStepName]
	if !ok {
		log.Error("Step %s not found in workflow %s", nextStepName, completedJob.Workflow)
		return
	}

	// Check loop iteration
	ts := st.Tasks[completedJob.Task]
	if ts == nil {
		ts = &state.TaskState{
			Workflow:    completedJob.Workflow,
			Iteration:  make(map[string]int),
			Created:    time.Now().UTC().Format(time.RFC3339),
		}
		st.Tasks[completedJob.Task] = ts
	}
	if ts.Iteration == nil {
		ts.Iteration = make(map[string]int)
	}

	stepIter := ts.Iteration[nextStepName] + 1
	maxIter := nextStep.MaxIterations
	if maxIter == 0 {
		maxIter = cfg.MaxLoopIterations
	}

	if stepIter > maxIter {
		log.Warn("Max iterations (%d) for %s on task %s — escalating", maxIter, nextStepName, completedJob.Task)
		// TODO: escalate to Stefan
		return
	}

	ts.CurrentStep = nextStepName
	ts.Status = "active"
	ts.Iteration[nextStepName] = stepIter

	// Build prompt
	artifactDir := filepath.Join(config.Root, "artifacts", completedJob.Task)
	artifactNote := ""
	if len(nextStep.ArtifactsIn) > 0 {
		artifactNote = fmt.Sprintf("\n\n## Artifacts\nAvailable in: %s\nFiles: %s", artifactDir, joinStrings(nextStep.ArtifactsIn))
	}

	prompt := fmt.Sprintf("# Task: %s\n\n## Step: %s\n\n%s%s\n\n## Communication\n- Done: `dispatch done \"summary\"`\n- Attach: `dispatch done --artifact file.md \"summary\"`\n- Help: `dispatch ask \"question\"`\n- Blocked: `dispatch fail \"reason\"`",
		completedJob.Task, nextStepName, nextStep.Prompt, artifactNote)

	jobType := "work"
	if nextStep.Agent == "stefan" || nextStep.Type == "human" {
		jobType = "human"
	}

	id, err := jobs.Create(jobs.CreateOpts{
		Task:      completedJob.Task,
		Workflow:  completedJob.Workflow,
		Step:      nextStepName,
		Agent:     nextStep.Agent,
		Model:     nextStep.Model,
		Type:      jobType,
		Priority:  "normal",
		Timeout:   nextStep.Timeout,
		Iteration: stepIter,
		Prompt:    prompt,
	})
	if err != nil {
		log.Error("Failed to create job for %s: %v", nextStepName, err)
		return
	}

	log.Info("Created job %s for step %s (agent=%s, model=%s)", id, nextStepName, nextStep.Agent, nextStep.Model)
}

func dispatchPending(cfg *config.Config, st *state.State) {
	pending, err := jobs.List("pending")
	if err != nil || len(pending) == 0 {
		return
	}

	for _, job := range pending {
		switch job.Type {
		case "triage", "parse", "answer":
			if job.Model != "" && !st.IsModelFree(job.Model) {
				continue
			}

			log.Info("Dispatching LLM job: %s (type=%s, model=%s)", job.ID, job.Type, job.Model)

			if job.Model != "" {
				st.LockModel(job.Model, job.ID)
			}
			jobs.Move(job.ID, "pending", "active")
			st.Save()

			go func(j jobs.Job) {
				result, err := llm.Call(j.Model, j.Prompt, "")
				if err != nil {
					log.Error("LLM job %s failed: %v", j.ID, err)
					if j.Model != "" {
						st.UnlockModel(j.Model)
					}
					jobs.Move(j.ID, "active", "failed")
				} else {
					jobs.WriteResult(j.ID, "active", result)
					if j.Model != "" {
						st.UnlockModel(j.Model)
					}
					jobs.Move(j.ID, "active", "done")

					meta := jobs.GetMeta(j.ID, "done")
					if meta != nil {
						advanceWorkflow(cfg, st, meta, result)
					}
				}
				st.Save()
			}(job)

		case "human":
			log.Info("Human job: %s — waiting for action", job.ID)
			jobs.Move(job.ID, "pending", "active")
			// TODO: notification
			st.Save()

		case "work":
			if job.Model != "" && !st.IsModelFree(job.Model) {
				continue
			}
			if job.Agent != "" && !st.IsAgentFree(job.Agent) {
				continue
			}

			log.Info("Dispatching work: %s (agent=%s, model=%s)", job.ID, job.Agent, job.Model)

			if job.Model != "" {
				st.LockModel(job.Model, job.ID)
			}
			if job.Agent != "" {
				st.LockAgent(job.Agent, job.ID)
			}
			jobs.Move(job.ID, "pending", "active")
			st.Save()

			// TODO: spawn OpenClaw session
			log.Info("Job %s active — agent %s should pick it up", job.ID, job.Agent)
			return // one work job at a time
		}
	}
}

func healthCheck(cfg *config.Config, st *state.State) {
	active, _ := jobs.List("active")
	now := time.Now()

	for _, job := range active {
		if job.Type == "human" || job.Timeout <= 0 {
			continue
		}

		created, err := time.Parse(time.RFC3339, job.Created)
		if err != nil {
			continue
		}

		deadline := created.Add(time.Duration(job.Timeout) * time.Second)
		if now.After(deadline) {
			log.Warn("Job %s timed out (timeout=%ds)", job.ID, job.Timeout)

			if job.Model != "" {
				st.UnlockModel(job.Model)
			}
			if job.Agent != "" && job.Agent != "stefan" {
				st.UnlockAgent(job.Agent)
			}

			jobs.WriteResult(job.ID, "active", fmt.Sprintf("TIMEOUT: exceeded %ds deadline", job.Timeout))
			jobs.Move(job.ID, "active", "failed")
		}
	}
}

func joinStrings(ss []string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += ", "
		}
		result += s
	}
	return result
}
