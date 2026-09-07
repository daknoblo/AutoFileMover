package ai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEndpointResolution(t *testing.T) {
	cases := []struct {
		name      string
		cfg       Config
		wantURL   string
		wantAzure bool
		wantModel bool
	}{
		{
			name:      "azure deployment",
			cfg:       Config{BaseURL: "https://res.openai.azure.com/", Model: "gpt-4o-mini", APIVersion: "2024-06-01"},
			wantURL:   "https://res.openai.azure.com/openai/deployments/gpt-4o-mini/chat/completions?api-version=2024-06-01",
			wantAzure: true,
			wantModel: false,
		},
		{
			name:      "foundry v1 root",
			cfg:       Config{BaseURL: "https://res.services.ai.azure.com/openai/v1", Model: "gpt-5.6-sol"},
			wantURL:   "https://res.services.ai.azure.com/openai/v1/chat/completions",
			wantAzure: true,
			wantModel: true,
		},
		{
			name:      "foundry v1 root ignores a stale api version",
			cfg:       Config{BaseURL: "https://res.services.ai.azure.com/openai/v1/", Model: "gpt-5.6-sol", APIVersion: "2024-06-01"},
			wantURL:   "https://res.services.ai.azure.com/openai/v1/chat/completions",
			wantAzure: true,
			wantModel: true,
		},
		{
			name:      "plain openai",
			cfg:       Config{BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini"},
			wantURL:   "https://api.openai.com/v1/chat/completions",
			wantAzure: false,
			wantModel: true,
		},
		{
			name:      "full url is used as is",
			cfg:       Config{BaseURL: "https://proxy.example.com/v1/chat/completions", Model: "llama"},
			wantURL:   "https://proxy.example.com/v1/chat/completions",
			wantAzure: false,
			wantModel: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ep := (&Client{cfg: tc.cfg}).endpoint()
			if ep.url != tc.wantURL {
				t.Errorf("url = %q, want %q", ep.url, tc.wantURL)
			}
			if ep.azureKey != tc.wantAzure {
				t.Errorf("azureKey = %v, want %v", ep.azureKey, tc.wantAzure)
			}
			if ep.sendModel != tc.wantModel {
				t.Errorf("sendModel = %v, want %v", ep.sendModel, tc.wantModel)
			}
		})
	}
}

// TestChatJSONRetriesWithoutTemperature covers reasoning models that reject any
// temperature but the default.
func TestChatJSONRetriesWithoutTemperature(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body: %v", err)
		}
		bodies = append(bodies, body)
		if _, ok := body["temperature"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unsupported value: 'temperature' does not support 0 with this model. Only the default (1) value is supported.","param":"temperature","code":"unsupported_value"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{}"}}]}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "reasoning-test-model"})
	got, err := c.ChatJSON(t.Context(), "sys", "user")
	if err != nil {
		t.Fatalf("ChatJSON: %v", err)
	}
	if got != "{}" {
		t.Errorf("content = %q, want %q", got, "{}")
	}
	if len(bodies) != 2 {
		t.Fatalf("want 2 requests (with and without temperature), got %d", len(bodies))
	}
	if _, ok := bodies[0]["temperature"]; !ok {
		t.Error("first request should carry a temperature")
	}
	if _, ok := bodies[1]["temperature"]; ok {
		t.Error("retry must omit the temperature")
	}

	// The rejection is remembered per model, so a fresh client (the engine
	// builds one per scan) skips the failing attempt.
	bodies = nil
	if _, err := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "reasoning-test-model"}).
		ChatJSON(t.Context(), "sys", "user"); err != nil {
		t.Fatalf("second ChatJSON: %v", err)
	}
	if len(bodies) != 1 {
		t.Fatalf("want 1 request after the model was remembered, got %d", len(bodies))
	}
	if _, ok := bodies[0]["temperature"]; ok {
		t.Error("remembered model must not receive a temperature")
	}
}

func TestChatJSONSendsTemperatureAndModel(t *testing.T) {
	var body map[string]any
	var auth, apiKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, apiKey = r.Header.Get("Authorization"), r.Header.Get("api-key")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body: %v", err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "classic-test-model"})
	if _, err := c.ChatJSON(t.Context(), "sys", "user"); err != nil {
		t.Fatalf("ChatJSON: %v", err)
	}
	if got, ok := body["temperature"].(float64); !ok || got != 0 {
		t.Errorf("temperature = %v, want 0", body["temperature"])
	}
	if body["model"] != "classic-test-model" {
		t.Errorf("model = %v, want classic-test-model", body["model"])
	}
	if auth != "Bearer k" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer k")
	}
	if apiKey != "" {
		t.Errorf("api-key should stay empty for non-Azure hosts, got %q", apiKey)
	}
}

func TestChatJSONSurfacesEndpointError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"401","message":"Access denied"}}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "any-test-model"})
	_, err := c.ChatJSON(t.Context(), "sys", "user")
	if err == nil {
		t.Fatal("want an error for a 401 response")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention the status, got %q", err)
	}
}
