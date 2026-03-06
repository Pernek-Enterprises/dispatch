package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

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

// Call sends a prompt directly to an endpoint URL.
// Used for stateless LLM jobs (triage, parse, answer) — not for agent work.
func Call(endpoint, prompt string, systemPrompt string) (string, error) {
	if endpoint == "" {
		return "", fmt.Errorf("no endpoint provided")
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

	resp, err := client.Post(endpoint+"/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("llm call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("llm %s HTTP %d: %s", endpoint, resp.StatusCode, string(b))
	}

	var cr chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return "", err
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("llm returned no choices")
	}
	return cr.Choices[0].Message.Content, nil
}
