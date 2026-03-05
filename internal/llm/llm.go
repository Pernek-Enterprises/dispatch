package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
)

type chatRequest struct {
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

var client = &http.Client{Timeout: 5 * time.Minute}

// Call sends a prompt to a model endpoint (resolved from models.json).
func Call(modelID, prompt string, systemPrompt string) (string, error) {
	models, err := config.LoadModels()
	if err != nil {
		return "", err
	}
	model, ok := models[modelID]
	if !ok {
		available := make([]string, 0, len(models))
		for k := range models {
			available = append(available, k)
		}
		return "", fmt.Errorf("unknown model %q (available: %v)", modelID, available)
	}
	if model.Endpoint == "" {
		return "", fmt.Errorf("model %q has no endpoint configured", modelID)
	}

	messages := []chatMessage{}
	if systemPrompt != "" {
		messages = append(messages, chatMessage{Role: "system", Content: systemPrompt})
	}
	messages = append(messages, chatMessage{Role: "user", Content: prompt})

	body, _ := json.Marshal(chatRequest{
		Messages:    messages,
		MaxTokens:   2048,
		Temperature: 0.7,
	})

	resp, err := client.Post(model.Endpoint+"/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("model %s: %w", modelID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("model %s (%s) HTTP %d: %s", modelID, model.Endpoint, resp.StatusCode, string(b))
	}

	var cr chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return "", err
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("model %s returned no choices", modelID)
	}
	return cr.Choices[0].Message.Content, nil
}

// GetProvider returns the provider string for a model (used for session spawning).
func GetProvider(modelID string) string {
	models, err := config.LoadModels()
	if err != nil {
		return ""
	}
	m, ok := models[modelID]
	if !ok {
		return ""
	}
	return m.Provider
}
