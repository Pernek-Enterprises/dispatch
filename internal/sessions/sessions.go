package sessions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
	"github.com/Pernek-Enterprises/dispatch/internal/log"
)

// SessionInfo tracks an active OpenClaw session.
type SessionInfo struct {
	SessionKey string `json:"sessionKey"`
	AgentID    string `json:"agentId"`
	Model      string `json:"model"`
	TaskID     string `json:"taskId"`
	AgentName  string `json:"agentName"`
	CreatedAt  string `json:"createdAt"`
}

// Get returns an existing session for a task+agent combo, or nil.
func Get(taskID, agentName string) *SessionInfo {
	p := sessionPath(taskID, agentName)
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

// Spawn creates a new OpenClaw session.
func Spawn(cfg *config.OpenClawConfig, taskID, agentName, modelID, prompt string) (*SessionInfo, error) {
	// Resolve dispatch agent name → OpenClaw agent ID + workspace
	agentCfg := cfg.Agents[agentName]
	agentID := agentCfg.ID
	if agentID == "" {
		agentID = agentName // fallback to dispatch name
	}
	workspaceDir := agentCfg.WorkspaceDir

	// Resolve model ID → provider string
	modelProvider := ""
	if modelID != "" {
		models, err := config.LoadModels()
		if err == nil {
			if m, ok := models[modelID]; ok && m.Provider != "" {
				modelProvider = m.Provider
			}
		}
	}

	label := fmt.Sprintf("dispatch-%s-%s", taskID[:min(8, len(taskID))], agentName)

	switch cfg.SpawnMethod {
	case "api":
		return spawnViaAPI(cfg, agentID, modelProvider, taskID, agentName, label, prompt)
	default:
		return spawnViaCLI(cfg, agentID, modelProvider, taskID, agentName, workspaceDir, label, prompt)
	}
}

// Send sends a message to an existing session.
func Send(cfg *config.OpenClawConfig, info *SessionInfo, message string) error {
	switch cfg.SpawnMethod {
	case "api":
		return sendViaAPI(cfg, info.SessionKey, message)
	default:
		return sendViaCLI(cfg, info.SessionKey, message)
	}
}

// Destroy removes session tracking for a task+agent.
func Destroy(taskID, agentName string) {
	os.Remove(sessionPath(taskID, agentName))
}

// --- CLI-based ---

func spawnViaCLI(cfg *config.OpenClawConfig, agentID, modelProvider, taskID, agentName, workspaceDir, label, prompt string) (*SessionInfo, error) {
	binary := cfg.Binary

	args := []string{
		"session", "spawn",
		"--agent", agentID,
		"--task", prompt,
		"--mode", "run",
		"--label", label,
	}
	if modelProvider != "" {
		args = append(args, "--model", modelProvider)
	}

	log.Info("Spawning: %s %s", binary, strings.Join(args, " "))

	cmd := exec.Command(binary, args...)
	if workspaceDir != "" {
		cmd.Dir = workspaceDir
	}
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("DISPATCH_JOB_ID=%s", label),
		fmt.Sprintf("DISPATCH_TASK_ID=%s", taskID),
		fmt.Sprintf("DISPATCH_ROOT=%s", config.Root),
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("spawn failed: %w — %s", err, string(output))
	}

	sessionKey := strings.TrimSpace(string(output))

	info := &SessionInfo{
		SessionKey: sessionKey,
		AgentID:    agentID,
		Model:      modelProvider,
		TaskID:     taskID,
		AgentName:  agentName,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	}

	save(info)
	return info, nil
}

func sendViaCLI(cfg *config.OpenClawConfig, sessionKey, message string) error {
	binary := cfg.Binary

	cmd := exec.Command(binary, "session", "send", "--key", sessionKey, "--message", message)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("send failed: %w — %s", err, string(output))
	}
	return nil
}

// --- API-based ---

type spawnRequest struct {
	Task    string `json:"task"`
	AgentID string `json:"agentId,omitempty"`
	Model   string `json:"model,omitempty"`
	Mode    string `json:"mode"`
	Label   string `json:"label,omitempty"`
}

type spawnResponse struct {
	SessionKey string `json:"sessionKey"`
}

func spawnViaAPI(cfg *config.OpenClawConfig, agentID, modelProvider, taskID, agentName, label, prompt string) (*SessionInfo, error) {
	if cfg.GatewayURL == "" {
		return nil, fmt.Errorf("openclaw.gatewayUrl not configured")
	}

	reqBody := spawnRequest{
		Task:    prompt,
		AgentID: agentID,
		Model:   modelProvider,
		Mode:    "run",
		Label:   label,
	}

	data, _ := json.Marshal(reqBody)
	url := strings.TrimRight(cfg.GatewayURL, "/") + "/api/sessions/spawn"

	req, _ := http.NewRequest("POST", url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if cfg.GatewayToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.GatewayToken)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API spawn: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return nil, fmt.Errorf("API spawn HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result spawnResponse
	json.Unmarshal(body, &result)

	info := &SessionInfo{
		SessionKey: result.SessionKey,
		AgentID:    agentID,
		Model:      modelProvider,
		TaskID:     taskID,
		AgentName:  agentName,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	}

	save(info)
	return info, nil
}

type sendAPIRequest struct {
	SessionKey string `json:"sessionKey"`
	Message    string `json:"message"`
}

func sendViaAPI(cfg *config.OpenClawConfig, sessionKey, message string) error {
	if cfg.GatewayURL == "" {
		return fmt.Errorf("openclaw.gatewayUrl not configured")
	}

	reqBody := sendAPIRequest{SessionKey: sessionKey, Message: message}
	data, _ := json.Marshal(reqBody)
	url := strings.TrimRight(cfg.GatewayURL, "/") + "/api/sessions/send"

	req, _ := http.NewRequest("POST", url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if cfg.GatewayToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.GatewayToken)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("API send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API send HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// --- Persistence ---

func sessionPath(taskID, agentName string) string {
	return filepath.Join(config.Root, "sessions", fmt.Sprintf("%s-%s.json", taskID, agentName))
}

func save(info *SessionInfo) {
	dir := filepath.Join(config.Root, "sessions")
	os.MkdirAll(dir, 0755)
	data, _ := json.MarshalIndent(info, "", "  ")
	os.WriteFile(sessionPath(info.TaskID, info.AgentName), append(data, '\n'), 0644)
}

// CleanupTask removes all session files for a given task.
func CleanupTask(taskID string) {
	dir := filepath.Join(config.Root, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), taskID) && strings.HasSuffix(e.Name(), ".json") {
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
