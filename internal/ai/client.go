// Package ai provides an OpenAI-compatible chat completion client that also
// supports Azure OpenAI / Azure AI Foundry deployments.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Config configures the chat client.
type Config struct {
	// BaseURL is the endpoint root.
	//   Azure:   https://<resource>.openai.azure.com
	//   Foundry: https://<resource>.services.ai.azure.com/openai/v1
	//   OpenAI:  https://api.openai.com/v1
	BaseURL string
	// APIKey authenticates the request.
	APIKey string
	// Model is the model name (OpenAI) or deployment name (Azure).
	Model string
	// APIVersion, when set, switches the client into Azure mode and is sent as
	// the api-version query parameter (e.g. 2024-06-01).
	APIVersion string
	// Logger, when set, receives an INFO summary of every call and the full
	// request/response at DEBUG level (the API key is never logged).
	Logger *slog.Logger
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
		http: &http.Client{Timeout: requestTimeout},
	}
}

// requestTimeout bounds a single call. Reasoning models spend extra time before
// the first token, so the window is generous.
const requestTimeout = 120 * time.Second

// Configured reports whether the minimum configuration is present.
func (c *Client) Configured() bool {
	return c.cfg.BaseURL != "" && c.cfg.APIKey != "" && c.cfg.Model != ""
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model,omitempty"`
	Messages []chatMessage `json:"messages"`
	// Temperature is omitted for models that only accept their default value.
	Temperature    *float64       `json:"temperature,omitempty"`
	ResponseFormat map[string]any `json:"response_format,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
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

	ep := c.endpoint()
	messages := []chatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}

	call, err := c.post(ctx, ep, messages, !rejectsTemperature(c.cfg.Model))
	if err != nil {
		return "", err
	}
	if rejectedTemperature(call.status, call.body) {
		markRejectsTemperature(c.cfg.Model)
		if c.cfg.Logger != nil {
			c.cfg.Logger.Info("model rejects a custom temperature, retrying with its default", "model", c.cfg.Model)
		}
		if call, err = c.post(ctx, ep, messages, false); err != nil {
			return "", err
		}
	}

	if call.status < 200 || call.status >= 300 {
		trimmed := strings.TrimSpace(string(call.body))
		if c.cfg.Logger != nil {
			c.cfg.Logger.Error("ai endpoint error", "status", call.status, "url", ep.url, "body", trimmed)
		}
		return "", fmt.Errorf("ai endpoint returned %d: %s", call.status, trimmed)
	}

	var parsed chatResponse
	if err := json.Unmarshal(call.body, &parsed); err != nil {
		return "", fmt.Errorf("decode ai response: %w", err)
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("ai error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("ai returned no choices")
	}
	content := parsed.Choices[0].Message.Content
	if c.cfg.Logger != nil {
		c.cfg.Logger.Info("ai call",
			"model", c.cfg.Model, "azure", ep.azureKey, "status", call.status,
			"finish_reason", parsed.Choices[0].FinishReason,
			"prompt_chars", len(system)+len(user), "response_chars", len(content),
			"prompt_tokens", parsed.Usage.PromptTokens, "completion_tokens", parsed.Usage.CompletionTokens,
			"duration_ms", call.duration.Milliseconds())
		c.cfg.Logger.Debug("ai response content", "content", content)
	}
	return content, nil
}

// callResult is the raw outcome of a single HTTP round trip.
type callResult struct {
	status   int
	body     []byte
	duration time.Duration
}

// promptOf returns the content of the first message with the given role, for
// debug logging.
func promptOf(messages []chatMessage, role string) string {
	for _, m := range messages {
		if m.Role == role {
			return m.Content
		}
	}
	return ""
}

// post sends one chat completion request. A non-2xx answer is returned as a
// result, not an error, so the caller can inspect it before giving up.
func (c *Client) post(ctx context.Context, ep endpoint, messages []chatMessage, withTemperature bool) (callResult, error) {
	reqBody := chatRequest{
		Messages:       messages,
		ResponseFormat: map[string]any{"type": "json_object"},
	}
	if ep.sendModel {
		reqBody.Model = c.cfg.Model
	}
	if withTemperature {
		zero := 0.0
		reqBody.Temperature = &zero
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return callResult{}, err
	}

	if c.cfg.Logger != nil {
		c.cfg.Logger.Debug("ai request",
			"url", ep.url, "model", c.cfg.Model, "azure", ep.azureKey,
			"temperature", withTemperature,
			"system_prompt", promptOf(messages, "system"), "user_prompt", promptOf(messages, "user"))
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.url, bytes.NewReader(payload))
	if err != nil {
		return callResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if ep.azureKey {
		httpReq.Header.Set("api-key", c.cfg.APIKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	start := time.Now()
	resp, err := c.http.Do(httpReq)
	if err != nil {
		if c.cfg.Logger != nil {
			c.cfg.Logger.Error("ai request failed", "url", ep.url, "err", err, "duration_ms", time.Since(start).Milliseconds())
		}
		return callResult{}, fmt.Errorf("ai request: %w", err)
	}
	defer resp.Body.Close()

	body, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if rerr != nil {
		if c.cfg.Logger != nil {
			c.cfg.Logger.Error("ai response read failed", "url", ep.url, "err", rerr)
		}
		return callResult{}, fmt.Errorf("read ai response: %w", rerr)
	}
	duration := time.Since(start)
	if c.cfg.Logger != nil {
		c.cfg.Logger.Debug("ai raw response", "status", resp.StatusCode, "duration_ms", duration.Milliseconds(), "body", string(body))
	}
	return callResult{status: resp.StatusCode, body: body, duration: duration}, nil
}

// Reasoning models (o-series, GPT-5 and newer) only accept their default
// temperature and answer a custom one with HTTP 400. The first rejection is
// remembered per model so later calls skip the parameter right away; the cache
// is package level because a fresh client is built for every scan.
var (
	noTemperatureMu sync.RWMutex
	noTemperature   = map[string]bool{}
)

func rejectsTemperature(model string) bool {
	noTemperatureMu.RLock()
	defer noTemperatureMu.RUnlock()
	return noTemperature[strings.ToLower(model)]
}

func markRejectsTemperature(model string) {
	noTemperatureMu.Lock()
	defer noTemperatureMu.Unlock()
	noTemperature[strings.ToLower(model)] = true
}

// rejectedTemperature reports whether a response rejects the temperature
// parameter, e.g. {"error":{"message":"Unsupported value: 'temperature' does not
// support 0 with this model. Only the default (1) value is supported.",
// "param":"temperature","code":"unsupported_value"}}.
func rejectedTemperature(status int, body []byte) bool {
	if status != http.StatusBadRequest {
		return false
	}
	lower := strings.ToLower(string(body))
	if !strings.Contains(lower, "temperature") {
		return false
	}
	return strings.Contains(lower, "unsupported") || strings.Contains(lower, "not supported")
}

// endpoint is a resolved request target.
type endpoint struct {
	url string
	// azureKey selects the Azure "api-key" header over "Authorization: Bearer".
	azureKey bool
	// sendModel adds the model to the body; deployment-scoped Azure URLs carry
	// it in the path instead.
	sendModel bool
}

// endpoint resolves the chat completions URL for the configured base URL and
// accepts every common shape:
//
//	https://<res>.openai.azure.com            + API version -> deployment path
//	https://<res>.services.ai.azure.com/openai/v1 (Foundry)  -> <base>/chat/completions
//	https://api.openai.com/v1                                -> <base>/chat/completions
//	any URL already ending in /chat/completions              -> used as is
//
// An API version is ignored for OpenAI-compatible v1 roots, which are versioned
// by their path; appending the deployment path there would hit a URL that does
// not exist and answers 401.
func (c *Client) endpoint() endpoint {
	base := strings.TrimSpace(c.cfg.BaseURL)
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return endpoint{
			url:       strings.TrimRight(base, "/") + "/chat/completions",
			azureKey:  c.cfg.APIVersion != "",
			sendModel: true,
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	azure := c.cfg.APIVersion != "" || isAzureHost(u.Hostname())
	path := strings.ToLower(u.Path)

	switch {
	case strings.HasSuffix(path, "/chat/completions"):
		return endpoint{url: u.String(), azureKey: azure, sendModel: !strings.Contains(path, "/deployments/")}
	case strings.HasSuffix(path, "/openai/v1"):
		u.Path += "/chat/completions"
		return endpoint{url: u.String(), azureKey: azure, sendModel: true}
	case c.cfg.APIVersion != "":
		u.Path += "/openai/deployments/" + c.cfg.Model + "/chat/completions"
		q := u.Query()
		q.Set("api-version", c.cfg.APIVersion)
		u.RawQuery = q.Encode()
		return endpoint{url: u.String(), azureKey: true}
	default:
		u.Path += "/chat/completions"
		return endpoint{url: u.String(), azureKey: azure, sendModel: true}
	}
}

// isAzureHost reports whether the host belongs to an Azure OpenAI or Foundry
// resource, which authenticates with the "api-key" header.
func isAzureHost(host string) bool {
	host = strings.ToLower(host)
	for _, suffix := range []string{".azure.com", ".azure.us", ".azure.cn"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}
