package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
	"github.com/Pernek-Enterprises/dispatch/internal/jobs"
	"github.com/Pernek-Enterprises/dispatch/internal/llm"
	"github.com/Pernek-Enterprises/dispatch/internal/log"
	"github.com/Pernek-Enterprises/dispatch/internal/pipe"
	"github.com/Pernek-Enterprises/dispatch/internal/sessions"
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

	// Check if this was a destroy step completing
	if strings.HasPrefix(completedJob.Step, "_destroy:") {
		handleDestroyComplete(cfg, st, completedJob)
		return
	}

	nextStepName := workflows.GetNextStep(wf, completedJob.Step, result)
	if nextStepName == "" {
		// Terminal step reached — enter destroy phase
		log.Info("Task %s reached terminal step — starting destroy phase", completedJob.Task)
		startDestroy(cfg, st, completedJob.Task, wf)
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

	// Build prompt: system prompt + step prompt + artifacts + communication
	systemPrompt := loadSystemPrompt(nextStep.Agent)

	artifactDir := filepath.Join(config.Root, "artifacts", completedJob.Task)
	artifactNote := ""
	if len(nextStep.ArtifactsIn) > 0 {
		artifactNote = fmt.Sprintf("\n\n## Artifacts\nAvailable in: %s\nFiles: %s", artifactDir, joinStrings(nextStep.ArtifactsIn))
	}

	stepPrompt := fmt.Sprintf("# Task: %s\n\n## Step: %s\n\n%s%s",
		completedJob.Task, nextStepName, nextStep.Prompt, artifactNote)

	prompt := systemPrompt + "\n\n---\n\n" + stepPrompt

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

			// Spawn or reuse OpenClaw session
			dispatchToSession(cfg, job)
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

// =============================================================================
// Destroy Phase
// =============================================================================

// startDestroy kicks off the destroy phase — sends destroy prompt to each involved agent.
func startDestroy(cfg *config.Config, st *state.State, taskID string, wf *workflows.Workflow) {
	agents := workflows.GetDestroyAgents(wf)
	if len(agents) == 0 {
		// No agents to destroy — run foreman cleanup directly
		log.Info("No agents for destroy phase — running cleanup")
		runDestroyActions(cfg, st, taskID, wf)
		return
	}

	// Load destroy prompt
	destroyPrompt := loadDestroyPrompt(wf.Name)

	// Track how many destroy jobs we're waiting for
	ts := st.Tasks[taskID]
	if ts == nil {
		ts = &state.TaskState{
			Workflow:   wf.Name,
			Iteration:  make(map[string]int),
		}
		st.Tasks[taskID] = ts
	}
	ts.Status = "destroying"
	ts.CurrentStep = "_destroy"

	for _, agentName := range agents {
		// Find the model this agent last used (use smallest available)
		model := getAgentModel(wf, agentName)

		prompt := fmt.Sprintf("# Task: %s — Destroy Phase\n\n%s\n\n## Context\nWorkflow: %s\nYour role: %s\nArtifacts dir: %s",
			taskID, destroyPrompt, wf.Name, agentName,
			filepath.Join(config.Root, "artifacts", taskID))

		stepName := fmt.Sprintf("_destroy:%s", agentName)

		_, err := jobs.Create(jobs.CreateOpts{
			Task:     taskID,
			Workflow: wf.Name,
			Step:     stepName,
			Agent:    agentName,
			Model:    model,
			Type:     "work",
			Priority: "high",
			Timeout:  wf.Destroy.Timeout,
			Prompt:   prompt,
		})
		if err != nil {
			log.Error("Failed to create destroy job for %s: %v", agentName, err)
			continue
		}
		log.Info("Created destroy job for agent %s on task %s", agentName, taskID)
	}
}

// handleDestroyComplete processes a completed destroy step.
// When all agents have completed destroy, runs foreman cleanup actions.
func handleDestroyComplete(cfg *config.Config, st *state.State, completedJob *jobs.Job) {
	taskID := completedJob.Task

	// Check if any other destroy jobs are still pending/active for this task
	pending, _ := jobs.List("pending")
	active, _ := jobs.List("active")

	for _, j := range append(pending, active...) {
		if j.Task == taskID && strings.HasPrefix(j.Step, "_destroy:") && j.ID != completedJob.ID {
			log.Info("Destroy: still waiting for %s on task %s", j.Step, taskID)
			return
		}
	}

	// All destroy jobs done — run foreman actions
	log.Info("All agents completed destroy for task %s — running cleanup", taskID)

	wf, err := workflows.Load(completedJob.Workflow)
	if err != nil {
		log.Warn("Workflow %s not found for cleanup: %v", completedJob.Workflow, err)
		// Still do basic cleanup
		sessions.CleanupTask(taskID)
		return
	}

	runDestroyActions(cfg, st, taskID, wf)
}

// runDestroyActions executes the foreman-side cleanup actions.
func runDestroyActions(cfg *config.Config, st *state.State, taskID string, wf *workflows.Workflow) {
	for _, action := range wf.Destroy.Actions {
		switch action {
		case "close_sessions":
			sessions.CleanupTask(taskID)
			log.Info("Destroy: closed sessions for task %s", taskID)

		case "archive_artifacts":
			// Artifacts already in artifacts/<task-id>/ — just log
			log.Info("Destroy: artifacts preserved in artifacts/%s/", taskID)

		case "cleanup_jobs":
			// Move any remaining job files to done
			cleanupJobFiles(taskID)
			log.Info("Destroy: cleaned up job files for task %s", taskID)
		}
	}

	// Mark task complete
	if ts, ok := st.Tasks[taskID]; ok {
		ts.Status = "complete"
	}
	log.Info("Task %s fully complete (destroy phase done)", taskID)
}

func cleanupJobFiles(taskID string) {
	for _, folder := range []string{"pending", "active"} {
		jobList, _ := jobs.List(folder)
		for _, j := range jobList {
			if j.Task == taskID {
				jobs.Move(j.ID, folder, "done")
			}
		}
	}
}

// getAgentModel finds the last/smallest model used by an agent in a workflow.
func getAgentModel(wf *workflows.Workflow, agentName string) string {
	model := ""
	for _, step := range wf.Steps {
		if step.Agent == agentName && step.Model != "" {
			model = step.Model
		}
	}
	return model
}

// loadDestroyPrompt loads the destroy prompt from workflows/<name>/destroy.prompt.md
func loadDestroyPrompt(workflowName string) string {
	promptPath := filepath.Join(config.Root, "workflows", workflowName, "destroy.prompt.md")
	data, err := os.ReadFile(promptPath)
	if err != nil {
		// Default destroy prompt
		return `This task is complete. Before your session closes:

1. Write a brief summary of what you did to your memory/session notes
2. Note any lessons learned or gotchas for future similar tasks
3. Clean up any temporary files you created

When done: ` + "`dispatch done \"cleanup complete\"`"
	}
	return strings.TrimSpace(string(data))
}

// loadSystemPrompt loads prompts/system.md + prompts/<agent>.md (if exists).
func loadSystemPrompt(agentName string) string {
	promptsDir := filepath.Join(config.Root, "prompts")
	var parts []string

	// Shared system prompt
	if data, err := os.ReadFile(filepath.Join(promptsDir, "system.md")); err == nil {
		parts = append(parts, strings.TrimSpace(string(data)))
	}

	// Agent-specific prompt
	if agentName != "" && agentName != "stefan" {
		if data, err := os.ReadFile(filepath.Join(promptsDir, agentName+".md")); err == nil {
			parts = append(parts, strings.TrimSpace(string(data)))
		}
	}

	return strings.Join(parts, "\n\n")
}

func dispatchToSession(cfg *config.Config, job jobs.Job) {
	existing := sessions.Get(job.Task, job.Agent)

	if existing != nil {
		log.Info("Reusing session %s for %s/%s", existing.SessionKey, job.Task, job.Agent)
		err := sessions.Send(&cfg.OpenClaw, existing, job.Prompt)
		if err != nil {
			log.Error("Failed to send to session %s: %v — spawning new", existing.SessionKey, err)
			sessions.Destroy(job.Task, job.Agent)
			existing = nil
		}
	}

	if existing == nil {
		session, err := sessions.Spawn(&cfg.OpenClaw, job.Task, job.Agent, job.Model, job.Prompt)
		if err != nil {
			log.Error("Failed to spawn session for %s/%s: %v", job.Task, job.Agent, err)
			return
		}
		log.Info("Spawned session %s for %s/%s (model=%s)", session.SessionKey, job.Task, job.Agent, job.Model)
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
