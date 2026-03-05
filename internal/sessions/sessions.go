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

// Session tracks an OpenClaw session for a task+agent pair.
type Session struct {
	SessionKey string `json:"sessionKey"`
	AgentID    string `json:"agentId"`
	TaskID     string `json:"taskId"`
	Agent      string `json:"agent"`
	Model      string `json:"model,omitempty"`
	Created    string `json:"created"`
	LastUsed   string `json:"lastUsed"`
}

// sessionsDir returns the path to the sessions directory.
func sessionsDir() string {
	return filepath.Join(config.Root, "sessions")
}

// sessionFile returns the path to a session state file for a task+agent pair.
func sessionFile(taskID, agent string) string {
	return filepath.Join(sessionsDir(), fmt.Sprintf("%s_%s.json", taskID, agent))
}

// Get returns an existing session for a task+agent pair, or nil.
func Get(taskID, agent string) *Session {
	data, err := os.ReadFile(sessionFile(taskID, agent))
	if err != nil {
		return nil
	}
	var s Session
	if json.Unmarshal(data, &s) != nil {
		return nil
	}
	return &s
}

// Save persists a session to disk.
func Save(s *Session) error {
	os.MkdirAll(sessionsDir(), 0755)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(sessionFile(s.TaskID, s.Agent), append(data, '\n'), 0644)
}

// Destroy removes a session file.
func Destroy(taskID, agent string) {
	os.Remove(sessionFile(taskID, agent))
}

// Spawn creates a new OpenClaw session for a task+agent pair.
// Returns the session key.
func Spawn(cfg *config.OpenClawConfig, taskID, agent, model, prompt string) (*Session, error) {
	// Resolve the OpenClaw agent ID from config mapping
	agentID := agent
	if cfg.AgentIDs != nil {
		if mapped, ok := cfg.AgentIDs[agent]; ok {
			agentID = mapped
		}
	}

	// Resolve model provider
	modelProvider := ""
	if model != "" {
		models, err := config.LoadModels()
		if err == nil {
			if m, ok := models[model]; ok {
				modelProvider = m.Provider
			}
		}
	}

	switch cfg.SpawnMethod {
	case "cli":
		return spawnCLI(cfg, taskID, agent, agentID, modelProvider, prompt)
	case "api":
		return spawnAPI(cfg, taskID, agent, agentID, modelProvider, prompt)
	default:
		return nil, fmt.Errorf("unknown spawn method: %s", cfg.SpawnMethod)
	}
}

// Send sends a message to an existing session.
func Send(cfg *config.OpenClawConfig, session *Session, message string) error {
	switch cfg.SpawnMethod {
	case "cli":
		return sendCLI(cfg, session, message)
	case "api":
		return sendAPI(cfg, session, message)
	default:
		return fmt.Errorf("unknown spawn method: %s", cfg.SpawnMethod)
	}
}

// --- CLI-based session management ---

func spawnCLI(cfg *config.OpenClawConfig, taskID, agent, agentID, model, prompt string) (*Session, error) {
	label := fmt.Sprintf("dispatch-%s-%s", taskID[:8], agent)

	args := []string{"session", "spawn",
		"--label", label,
		"--mode", "session",
		"--task", prompt,
	}
	if agentID != "" {
		args = append(args, "--agent-id", agentID)
	}
	if model != "" {
		args = append(args, "--model", model)
	}

	log.Info("Spawning session: %s %s", cfg.Binary, strings.Join(args, " "))

	cmd := exec.Command(cfg.Binary, args...)
	if cfg.WorkspaceDir != "" {
		cmd.Dir = cfg.WorkspaceDir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("spawn failed: %v — %s", err, string(output))
	}

	// Parse session key from output
	sessionKey := strings.TrimSpace(string(output))

	session := &Session{
		SessionKey: sessionKey,
		AgentID:    agentID,
		TaskID:     taskID,
		Agent:      agent,
		Model:      model,
		Created:    time.Now().UTC().Format(time.RFC3339),
		LastUsed:   time.Now().UTC().Format(time.RFC3339),
	}

	if err := Save(session); err != nil {
		return nil, err
	}

	log.Info("Session spawned: %s (key=%s)", label, sessionKey)
	return session, nil
}

func sendCLI(cfg *config.OpenClawConfig, session *Session, message string) error {
	args := []string{"session", "send",
		"--session-key", session.SessionKey,
		"--message", message,
	}

	cmd := exec.Command(cfg.Binary, args...)
	if cfg.WorkspaceDir != "" {
		cmd.Dir = cfg.WorkspaceDir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("send failed: %v — %s", err, string(output))
	}

	session.LastUsed = time.Now().UTC().Format(time.RFC3339)
	Save(session)
	return nil
}

// --- API-based session management ---

func spawnAPI(cfg *config.OpenClawConfig, taskID, agent, agentID, model, prompt string) (*Session, error) {
	// TODO: implement HTTP API spawning when gatewayUrl is configured
	return nil, fmt.Errorf("API spawn not yet implemented — use spawnMethod: cli")
}

func sendAPI(cfg *config.OpenClawConfig, session *Session, message string) error {
	// TODO: implement HTTP API send
	return fmt.Errorf("API send not yet implemented — use spawnMethod: cli")
}

// ListActive returns all active session files.
func ListActive() ([]*Session, error) {
	dir := sessionsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []*Session
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var s Session
		if json.Unmarshal(data, &s) == nil {
			sessions = append(sessions, &s)
		}
	}
	return sessions, nil
}

// CleanupTask removes all sessions for a completed task.
func CleanupTask(taskID string) {
	dir := sessionsDir()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), taskID+"_") {
			os.Remove(filepath.Join(dir, e.Name()))
			log.Info("Cleaned up session: %s", e.Name())
		}
	}
}
