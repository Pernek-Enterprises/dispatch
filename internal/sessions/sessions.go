package sessions

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
	"github.com/Pernek-Enterprises/dispatch/internal/log"
)

// SessionInfo tracks an active session (task + agent + model combo).
type SessionInfo struct {
	SessionID string `json:"sessionId"`
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
	Model     string `json:"model"`
	TaskID    string `json:"taskId"`
	CreatedAt string `json:"createdAt"`
	LastUsed  string `json:"lastUsed"`
}

// MakeSessionID builds the canonical session ID for a task+agent+model combo.
func MakeSessionID(taskID, agentName, model string) string {
	return fmt.Sprintf("dispatch-%s-%s-%s", taskID, agentName, model)
}

// Dispatch sends a prompt to an agent. Spawns a new session on first use
// for this task+agent+model combo, reuses on subsequent calls.
func Dispatch(cfg *config.OpenClawConfig, taskID, agentName, model, prompt string) error {
	sessionID := MakeSessionID(taskID, agentName, model)

	// Resolve dispatch agent name → OpenClaw agent ID
	agentID := agentName
	if agentCfg, ok := cfg.Agents[agentName]; ok && agentCfg.ID != "" {
		agentID = agentCfg.ID
	}

	// Resolve model ID → provider string
	modelProvider := model
	if model != "" {
		models, err := config.LoadModels()
		if err == nil {
			if m, ok := models[model]; ok && m.Provider != "" {
				modelProvider = m.Provider
			}
		}
	}

	// Check if session already exists (for logging)
	existing := Get(taskID, agentName, model)
	if existing != nil {
		log.Info("Reusing session %s", sessionID)
	} else {
		log.Info("New session %s (agent=%s, model=%s)", sessionID, agentID, modelProvider)
	}

	// Call openclaw agent
	binary := cfg.Binary
	if binary == "" {
		binary = "openclaw"
	}

	args := []string{
		"agent",
		"--agent", agentID,
		"--session-id", sessionID,
		"--message", prompt,
		"--json",
	}

	log.Info("Exec: %s %s", binary, strings.Join(args[:4], " ")) // don't log full prompt

	cmd := exec.Command(binary, args...)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("DISPATCH_JOB_ID=%s", sessionID),
		fmt.Sprintf("DISPATCH_TASK_ID=%s", taskID),
		fmt.Sprintf("DISPATCH_ROOT=%s", config.Root),
	)

	// Fire and forget — don't block the foreman waiting for the agent to finish.
	// The agent will call `dispatch done/fail` via the pipe when it's done.
	// We capture output to a log file for debugging.
	logDir := filepath.Join(config.Root, "logs")
	os.MkdirAll(logDir, 0755)
	logFile, err := os.Create(filepath.Join(logDir, sessionID+".log"))
	if err != nil {
		return fmt.Errorf("failed to create session log: %w", err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("openclaw agent failed to start: %w", err)
	}

	// Reap the process in the background
	go func() {
		err := cmd.Wait()
		logFile.Close()
		if err != nil {
			log.Error("Session %s exited with error: %v", sessionID, err)
		} else {
			log.Info("Session %s exited cleanly", sessionID)
		}
	}()

	// Save/update session info
	info := &SessionInfo{
		SessionID: sessionID,
		AgentID:   agentID,
		AgentName: agentName,
		Model:     model,
		TaskID:    taskID,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		LastUsed:  time.Now().UTC().Format(time.RFC3339),
	}
	if existing != nil {
		info.CreatedAt = existing.CreatedAt
	}
	save(info)

	return nil
}

// Get returns session info for a task+agent+model combo, or nil.
func Get(taskID, agentName, model string) *SessionInfo {
	p := sessionPath(taskID, agentName, model)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var info SessionInfo
	if json.Unmarshal(data, &info) != nil {
		return nil
	}
	return &info
}

// Destroy removes a specific session.
func Destroy(taskID, agentName, model string) {
	os.Remove(sessionPath(taskID, agentName, model))
}

// CleanupTask removes all session files for a task.
func CleanupTask(taskID string) {
	dir := filepath.Join(config.Root, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := fmt.Sprintf("dispatch-%s-", taskID)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), ".json") {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// ListActive returns all active session info files.
func ListActive() ([]SessionInfo, error) {
	dir := filepath.Join(config.Root, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var result []SessionInfo
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var info SessionInfo
		if json.Unmarshal(data, &info) == nil {
			result = append(result, info)
		}
	}
	return result, nil
}

// ListTaskSessions returns all sessions for a specific task.
func ListTaskSessions(taskID string) []SessionInfo {
	all, _ := ListActive()
	var result []SessionInfo
	for _, s := range all {
		if s.TaskID == taskID {
			result = append(result, s)
		}
	}
	return result
}

// --- Persistence ---

func sessionPath(taskID, agentName, model string) string {
	id := MakeSessionID(taskID, agentName, model)
	return filepath.Join(config.Root, "sessions", id+".json")
}

func save(info *SessionInfo) {
	dir := filepath.Join(config.Root, "sessions")
	os.MkdirAll(dir, 0755)
	data, _ := json.MarshalIndent(info, "", "  ")
	os.WriteFile(filepath.Join(dir, info.SessionID+".json"), append(data, '\n'), 0644)
}
