package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daknoblo/AutoFileMover/internal/config"
	"github.com/daknoblo/AutoFileMover/internal/engine"
	"github.com/daknoblo/AutoFileMover/internal/logbuf"
	"github.com/daknoblo/AutoFileMover/internal/store"
)

type noopResyncer struct{}

func (noopResyncer) Resync(context.Context) {}

func testHTTP(t *testing.T) (*httptest.Server, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{MediaRoot: dir}
	eng := engine.New(st, cfg, log)
	var level slog.LevelVar
	srv := NewServer(st, eng, cfg, log, noopResyncer{}, logbuf.New(50, io.Discard), &level)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = st.Close()
	})
	return ts, st, dir
}

func putJSON(t *testing.T, rawURL, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, rawURL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestGetSettingsMasksAPIKey(t *testing.T) {
	ts, st, _ := testHTTP(t)
	if err := st.SaveAppSettings(context.Background(), store.AppSettings{AIAPIKey: "supersecret", AIModel: "gpt"}); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(ts.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "supersecret") {
		t.Errorf("API key leaked in settings response: %s", body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["has_api_key"] != true {
		t.Errorf("has_api_key should be true, got %v", got["has_api_key"])
	}
	if _, ok := got["ai_api_key"]; ok {
		t.Error("ai_api_key must not be present in the response")
	}
}

func TestPutSettingsRejectsInvalidURL(t *testing.T) {
	ts, _, _ := testHTTP(t)
	resp := putJSON(t, ts.URL+"/api/settings", `{"ai_base_url":"ftp://nope","threshold":0.5}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid ai_base_url should be 400, got %d", resp.StatusCode)
	}
}

func TestPutSettingsRejectsThresholdRange(t *testing.T) {
	ts, _, _ := testHTTP(t)
	resp := putJSON(t, ts.URL+"/api/settings", `{"threshold":2.5}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("out-of-range threshold should be 400, got %d", resp.StatusCode)
	}
}

func TestPutSettingsValid(t *testing.T) {
	ts, _, _ := testHTTP(t)
	resp := putJSON(t, ts.URL+"/api/settings", `{"ai_base_url":"https://api.example.com","threshold":0.8}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("valid settings should be 200, got %d", resp.StatusCode)
	}
}

func TestBodySizeLimitRejectsHugePayload(t *testing.T) {
	ts, _, _ := testHTTP(t)
	huge := strings.Repeat("x", 2<<20) // 2 MiB, well over the 1 MiB limit
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/api/settings", strings.NewReader(`{"ai_context":"`+huge+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// A connection reset from MaxBytesReader also means the body was rejected.
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Errorf("oversized body should not be accepted, got 200")
	}
}

func TestBrowseClampsTraversal(t *testing.T) {
	ts, _, dir := testHTTP(t)
	resp, err := http.Get(ts.URL + "/api/browse?path=" + url.QueryEscape("/etc"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var br struct {
		Path   string `json:"path"`
		AtRoot bool   `json:"at_root"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&br); err != nil {
		t.Fatal(err)
	}
	if br.Path != filepath.Clean(dir) {
		t.Errorf("traversal not clamped to media root: path=%q want %q", br.Path, filepath.Clean(dir))
	}
	if !br.AtRoot {
		t.Error("clamped browse should report at_root=true")
	}
}
