package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type chatMessage struct {
	Content   string `json:"content"`
	ToolCalls []struct {
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// chatCompletion posts one non-streaming completion to the provider and
// returns the first choice's message — the bare-model baseline path.
func chatCompletion(opts options, body map[string]any) (chatMessage, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return chatMessage{}, err
	}
	client := &http.Client{Timeout: opts.timeout}
	resp, err := client.Post(opts.baseURL+"/v1/chat/completions", "application/json", bytes.NewReader(payload))
	if err != nil {
		return chatMessage{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return chatMessage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return chatMessage{}, fmt.Errorf("provider %s: %s", resp.Status, tail(string(data), 200))
	}
	var parsed struct {
		Choices []struct {
			Message chatMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return chatMessage{}, err
	}
	if len(parsed.Choices) == 0 {
		return chatMessage{}, fmt.Errorf("provider returned no choices")
	}
	return parsed.Choices[0].Message, nil
}
