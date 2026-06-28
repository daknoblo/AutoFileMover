// Package ai provides an OpenAI-compatible chat completion client that also
// supports Azure OpenAI / Azure AI Foundry deployments.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config configures the chat client.
type Config struct {
	// BaseURL is the endpoint root.
	//   Azure:  https://<resource>.openai.azure.com  (or a Foundry endpoint)
	//   OpenAI: https://api.openai.com/v1
	BaseURL string
	// APIKey authenticates the request.
	APIKey string
	// Model is the model name (OpenAI) or deployment name (Azure).
	Model string
	// APIVersion, when set, switches the client into Azure mode and is sent as
	// the api-version query parameter (e.g. 2024-06-01).
	APIVersion string
}

// Client talks to a chat completions endpoint.
type Client struct {
	cfg  Config
	http *http.Client
}

// New creates a new client.
func New(cfg Config) *Client {
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

// Configured reports whether the minimum configuration is present.
func (c *Client) Configured() bool {
	return c.cfg.BaseURL != "" && c.cfg.APIKey != "" && c.cfg.Model != ""
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string         `json:"model,omitempty"`
	Messages       []chatMessage  `json:"messages"`
	Temperature    float64        `json:"temperature"`
	ResponseFormat map[string]any `json:"response_format,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ChatJSON sends a system and user prompt and returns the raw assistant content,
// requesting a JSON object response.
func (c *Client) ChatJSON(ctx context.Context, system, user string) (string, error) {
	if !c.Configured() {
		return "", fmt.Errorf("ai client not configured")
	}

	reqBody := chatRequest{
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature:    0,
		ResponseFormat: map[string]any{"type": "json_object"},
	}

	url, isAzure := c.endpointURL()
	if !isAzure {
		reqBody.Model = c.cfg.Model
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if isAzure {
		httpReq.Header.Set("api-key", c.cfg.APIKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("ai request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("ai endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decode ai response: %w", err)
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("ai error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("ai returned no choices")
	}
	return parsed.Choices[0].Message.Content, nil
}

// endpointURL builds the request URL and reports whether Azure mode is active.
func (c *Client) endpointURL() (string, bool) {
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	if c.cfg.APIVersion != "" {
		// Azure OpenAI deployment-scoped endpoint.
		return fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s",
			base, c.cfg.Model, c.cfg.APIVersion), true
	}
	return base + "/chat/completions", false
}
