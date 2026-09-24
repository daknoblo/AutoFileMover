package web

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daknoblo/AutoFileMover/internal/config"
	"github.com/daknoblo/AutoFileMover/internal/engine"
	"github.com/daknoblo/AutoFileMover/internal/foundry"
	"github.com/daknoblo/AutoFileMover/internal/logbuf"
	"github.com/daknoblo/AutoFileMover/internal/queue"
	"github.com/daknoblo/AutoFileMover/internal/store"
)

// testFoundryServer starts a server whose engine uses the given Azure identity.
func testFoundryServer(t *testing.T, identity foundry.Identity) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{MediaRoot: dir, Foundry: identity}
	eng := engine.New(st, cfg, log)
	var level slog.LevelVar
	srv := NewServer(st, eng, queue.New(st, eng, cfg, log), cfg, log,
		noopResyncer{}, logbuf.New(50, io.Discard), &level)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postResult(t *testing.T, rawURL string) map[string]any {
	t.Helper()
	resp, err := http.Post(rawURL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFoundryStatusIsDisabledWithoutAnIdentity(t *testing.T) {
	ts, _, _ := testHTTP(t)
	resp, err := http.Get(ts.URL + "/api/foundry")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var status foundry.Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || status.Enabled || status.Error != "" {
		t.Fatalf("status = %d, %+v", resp.StatusCode, status)
	}
}

func TestFoundryStatusReportsDiscoveryFailureInThePayload(t *testing.T) {
	// A misconfigured identity must not break the settings page; the reason
	// belongs next to the field.
	ts := testFoundryServer(t, foundry.Identity{ResourceID: "nonsense", TenantID: "x",
		ClientID: "y", ClientSecret: "z"})
	for _, endpoint := range []string{"/api/foundry", "/api/foundry/refresh"} {
		var status foundry.Status
		if endpoint == "/api/foundry" {
			resp, err := http.Get(ts.URL + endpoint)
			if err != nil {
				t.Fatal(err)
			}
			err = json.NewDecoder(resp.Body).Decode(&status)
			_ = resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
		} else {
			raw := postResult(t, ts.URL+endpoint)
			if enabled, _ := raw["enabled"].(bool); !enabled {
				t.Fatalf("%s: enabled = %v", endpoint, raw["enabled"])
			}
			if message, _ := raw["error"].(string); message == "" {
				t.Fatalf("%s: missing discovery error", endpoint)
			}
			continue
		}
		if !status.Enabled || status.Error == "" || len(status.Deployments) != 0 {
			t.Fatalf("%s: status = %+v", endpoint, status)
		}
	}
}

func TestSettingsRejectAnUnavailableDeploymentInFoundryMode(t *testing.T) {
	ts := testFoundryServer(t, foundry.Identity{ResourceID: "nonsense", TenantID: "x",
		ClientID: "y", ClientSecret: "z"})
	resp := putJSON(t, ts.URL+"/api/settings", `{"ai_model":"gpt-4o","threshold":0.9}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 while discovery is unavailable", resp.StatusCode)
	}
	// Without a deployment name there is nothing to verify against Azure.
	empty := putJSON(t, ts.URL+"/api/settings", `{"ai_model":"","threshold":0.9}`)
	defer empty.Body.Close()
	if empty.StatusCode != http.StatusOK {
		t.Fatalf("clearing the deployment failed: %d", empty.StatusCode)
	}
}

func TestAITestReportsAWorkingEndpoint(t *testing.T) {
	var path string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer upstream.Close()

	ts, _, _ := testHTTP(t)
	saved := putJSON(t, ts.URL+"/api/settings",
		`{"ai_base_url":"`+upstream.URL+`","ai_model":"m","ai_api_key":"k","threshold":0.9}`)
	defer saved.Body.Close()
	if saved.StatusCode != http.StatusOK {
		t.Fatalf("saving settings failed: %d", saved.StatusCode)
	}

	result := postResult(t, ts.URL+"/api/ai/test")
	if ok, _ := result["ok"].(bool); !ok {
		t.Fatalf("connection test failed: %+v", result)
	}
	if path != "/chat/completions" {
		t.Fatalf("unexpected upstream path %q", path)
	}
}

func TestAITestReportsAFailingEndpointWithoutAnHTTPError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Access denied"}}`))
	}))
	defer upstream.Close()

	ts, _, _ := testHTTP(t)
	saved := putJSON(t, ts.URL+"/api/settings",
		`{"ai_base_url":"`+upstream.URL+`","ai_model":"m","ai_api_key":"k","threshold":0.9}`)
	defer saved.Body.Close()

	result := postResult(t, ts.URL+"/api/ai/test")
	if ok, _ := result["ok"].(bool); ok {
		t.Fatalf("a 401 endpoint must not pass the test: %+v", result)
	}
	if message, _ := result["error"].(string); !strings.Contains(message, "401") {
		t.Fatalf("error = %v", result["error"])
	}
}

func TestAITestReportsAnUnconfiguredEndpoint(t *testing.T) {
	ts, _, _ := testHTTP(t)
	result := postResult(t, ts.URL+"/api/ai/test")
	if ok, _ := result["ok"].(bool); ok {
		t.Fatalf("an unconfigured endpoint must not pass the test: %+v", result)
	}
	if message, _ := result["error"].(string); message == "" {
		t.Fatal("missing reason for the failed test")
	}
}
