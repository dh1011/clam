package main

// A minimal client for OpenAI's streaming Responses API: just what the
// agent loop needs, with no SDK.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type responseRequest struct {
	Model          string            `json:"model"`
	Instructions   string            `json:"instructions"`
	Input          []json.RawMessage `json:"input"`
	Tools          []toolSpec        `json:"tools"`
	Reasoning      map[string]string `json:"reasoning"`
	Include        []string          `json:"include"`
	PromptCacheKey string            `json:"prompt_cache_key,omitempty"`
	Store          bool              `json:"store"`
	Stream         bool              `json:"stream"`
}

type toolSpec struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// outputItem is the part of a response output item the loop looks at;
// the items themselves go back into the history unchanged.
type outputItem struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	CallID    string `json:"call_id"`
	Arguments string `json:"arguments"`
}

type streamEvent struct {
	Type     string `json:"type"`
	Delta    string `json:"delta"`
	Message  string `json:"message"`
	Response struct {
		Output []json.RawMessage `json:"output"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	} `json:"response"`
}

func baseURL() string {
	if url := os.Getenv("OPENAI_BASE_URL"); url != "" {
		return strings.TrimRight(url, "/")
	}
	return "https://api.openai.com/v1"
}

// streamResponse sends one request, passes text deltas to onText as they
// arrive and returns the response's output items.
func streamResponse(ctx context.Context, req responseRequest, onText func(string)) ([]json.RawMessage, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL()+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+os.Getenv("OPENAI_API_KEY"))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data:")
		if !ok {
			continue
		}
		var event streamEvent
		if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &event); err != nil {
			continue
		}
		switch event.Type {
		case "response.output_text.delta":
			onText(event.Delta)
		case "response.completed":
			return event.Response.Output, nil
		case "response.incomplete":
			reason := "unknown reason"
			if d := event.Response.IncompleteDetails; d != nil && d.Reason != "" {
				reason = d.Reason
			}
			return nil, fmt.Errorf("response incomplete: %s", reason)
		case "response.failed":
			if e := event.Response.Error; e != nil && e.Message != "" {
				return nil, fmt.Errorf("%s", e.Message)
			}
			return nil, fmt.Errorf("model request failed")
		case "error":
			return nil, fmt.Errorf("%s", event.Message)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("response stream ended early")
}

func apiError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &body) == nil && body.Error.Message != "" {
		return fmt.Errorf("%s (HTTP %d)", body.Error.Message, resp.StatusCode)
	}
	return fmt.Errorf("model request failed: HTTP %d", resp.StatusCode)
}
