package ai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// decodeBody returns the JSON request body the endpoint received.
func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("request body: %v", err)
	}
	return body
}

func TestRedirectIsRefusedSoTheKeyStaysOnTheConfiguredHost(t *testing.T) {
	var leaked string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization") + r.Header.Get("api-key")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/chat/completions", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	c := New(Config{BaseURL: origin.URL, APIKey: "secret-key", Model: "redirect-test-model"})
	if _, err := c.ChatJSON(t.Context(), "sys", "user"); err == nil {
		t.Fatal("a redirected endpoint must fail instead of forwarding the credential")
	}
	if leaked != "" {
		t.Fatalf("the credential reached the redirect target: %q", leaked)
	}
}

func TestAuthorizeHookReplacesTheStoredKey(t *testing.T) {
	var auth, apiKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, apiKey = r.Header.Get("Authorization"), r.Header.Get("api-key")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer srv.Close()

	signed := 0
	c := New(Config{BaseURL: srv.URL, Model: "identity-test-model", Reasoning: true,
		Authorize: func(req *http.Request) error {
			signed++
			req.Header.Set("Authorization", "Bearer discovered-token")
			return nil
		}})
	if !c.Configured() {
		t.Fatal("a signing hook must satisfy the configuration check without an API key")
	}
	if _, err := c.ChatJSON(t.Context(), "sys", "user"); err != nil {
		t.Fatalf("ChatJSON: %v", err)
	}
	if signed != 1 {
		t.Fatalf("signing hook called %d times, want 1", signed)
	}
	if auth != "Bearer discovered-token" || apiKey != "" {
		t.Fatalf("request carried the wrong credential: %q / %q", auth, apiKey)
	}
}

func TestAuthorizeFailureAbortsTheCall(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Model: "unverified-test-model",
		Authorize: func(*http.Request) error { return errTest }})
	_, err := c.ChatJSON(t.Context(), "sys", "user")
	if err == nil || !strings.Contains(err.Error(), "unverified target") {
		t.Fatalf("error = %v; want the signing failure", err)
	}
	if reached {
		t.Fatal("an unsigned request must not be sent")
	}
}

var errTest = errTestType("unverified target")

type errTestType string

func (e errTestType) Error() string { return string(e) }

func TestReasoningDeploymentSkipsTheTemperatureRoundTrip(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodies = append(bodies, decodeBody(t, r))
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "known-reasoning-test-model", Reasoning: true})
	if _, err := c.ChatJSON(t.Context(), "sys", "user"); err != nil {
		t.Fatalf("ChatJSON: %v", err)
	}
	if len(bodies) != 1 {
		t.Fatalf("want a single request, got %d", len(bodies))
	}
	if _, ok := bodies[0]["temperature"]; ok {
		t.Error("a known reasoning deployment must not send a temperature at all")
	}
}

func TestPingUsesAMinimalCompatibleRequest(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = decodeBody(t, r)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "ping-test-model"})
	if err := c.Ping(t.Context()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if body["model"] != "ping-test-model" {
		t.Errorf("model = %v", body["model"])
	}
	// A response format or a token cap is rejected by some deployments and
	// would turn a working endpoint into a failed test.
	if _, ok := body["response_format"]; ok {
		t.Error("ping must not request a JSON response format")
	}
	for _, key := range []string{"max_tokens", "max_completion_tokens", "temperature"} {
		if _, ok := body[key]; ok {
			t.Errorf("ping must not send %s", key)
		}
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages = %v", body["messages"])
	}
}

func TestPingReportsAnUnusableEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Access denied"}}`))
	}))
	defer srv.Close()

	err := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "ping-fail-test-model"}).Ping(t.Context())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %v; want the status", err)
	}
	if err := New(Config{}).Ping(t.Context()); err == nil {
		t.Fatal("an unconfigured client must report that it cannot be tested")
	}
}

func TestUpstreamErrorIsBoundedBeforeItReachesTheUI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(strings.Repeat("A\x00\n", 4000)))
	}))
	defer srv.Close()

	_, err := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "noisy-test-model"}).
		ChatJSON(t.Context(), "sys", "user")
	if err == nil {
		t.Fatal("want an error for a 502 response")
	}
	if len(err.Error()) > 400 {
		t.Fatalf("the upstream body is not bounded: %d characters", len(err.Error()))
	}
	if strings.ContainsRune(err.Error(), '\x00') {
		t.Fatal("control characters must be stripped from the message")
	}
}

func TestSanitize(t *testing.T) {
	if got := sanitize("  hello\tworld\n "); got != "hello world" {
		t.Errorf("sanitize = %q", got)
	}
	if got := sanitize(strings.Repeat("x", maxErrorChars+50)); len([]rune(got)) != maxErrorChars+1 {
		t.Errorf("sanitize length = %d", len([]rune(got)))
	}
}

func TestConfiguredRequiresAnEndpointModelAndCredential(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"complete", Config{BaseURL: "https://x", Model: "m", APIKey: "k"}, true},
		{"signed", Config{BaseURL: "https://x", Model: "m", Authorize: func(*http.Request) error { return nil }}, true},
		{"no credential", Config{BaseURL: "https://x", Model: "m"}, false},
		{"no model", Config{BaseURL: "https://x", APIKey: "k"}, false},
		{"no endpoint", Config{Model: "m", APIKey: "k"}, false},
	}
	for _, tc := range cases {
		if got := New(tc.cfg).Configured(); got != tc.want {
			t.Errorf("%s: Configured() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
