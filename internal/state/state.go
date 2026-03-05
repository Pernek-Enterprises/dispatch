package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
)

type ModelLock struct {
	Busy  bool   `json:"busy"`
	Job   string `json:"job,omitempty"`
	Since string `json:"since,omitempty"`
}

type AgentLock struct {
	Busy  bool   `json:"busy"`
	Job   string `json:"job,omitempty"`
	Since string `json:"since,omitempty"`
}

type TaskState struct {
	Workflow    string         `json:"workflow"`
	CurrentStep string        `json:"currentStep"`
	Status     string          `json:"status"`
	Iteration  map[string]int  `json:"iteration"`
	Created    string          `json:"created,omitempty"`
}

type State struct {
	Models  map[string]*ModelLock `json:"models"`
	Agents  map[string]*AgentLock `json:"agents"`
	Tasks   map[string]*TaskState `json:"tasks"`
	mu      sync.Mutex
}

var current *State

func Load() *State {
	s := &State{
		Models: make(map[string]*ModelLock),
		Agents: make(map[string]*AgentLock),
		Tasks:  make(map[string]*TaskState),
	}

	p := filepath.Join(config.Root, "state.json")
	data, err := os.ReadFile(p)
	if err == nil {
		json.Unmarshal(data, s)
	}
	if s.Models == nil {
		s.Models = make(map[string]*ModelLock)
	}
	if s.Agents == nil {
		s.Agents = make(map[string]*AgentLock)
	}
	if s.Tasks == nil {
		s.Tasks = make(map[string]*TaskState)
	}

	current = s
	return s
}

func (s *State) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(config.Root, "state.json"), append(data, '\n'), 0644)
}

func (s *State) LockModel(modelID, jobID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Models[modelID] = &ModelLock{Busy: true, Job: jobID, Since: time.Now().UTC().Format(time.RFC3339)}
}

func (s *State) UnlockModel(modelID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Models[modelID] = &ModelLock{Busy: false}
}

func (s *State) LockAgent(agentID, jobID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Agents[agentID] = &AgentLock{Busy: true, Job: jobID, Since: time.Now().UTC().Format(time.RFC3339)}
}

func (s *State) UnlockAgent(agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Agents[agentID] = &AgentLock{Busy: false}
}

func (s *State) IsModelFree(modelID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.Models[modelID]
	return !ok || !m.Busy
}

func (s *State) IsAgentFree(agentID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.Agents[agentID]
	return !ok || !a.Busy
}
