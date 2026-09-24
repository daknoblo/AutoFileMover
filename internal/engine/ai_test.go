package engine

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daknoblo/AutoFileMover/internal/config"
	"github.com/daknoblo/AutoFileMover/internal/foundry"
	"github.com/daknoblo/AutoFileMover/internal/store"
)

func aiTestEngine(t *testing.T, identity foundry.Identity) (*Engine, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(st, config.Config{MediaRoot: dir, Foundry: identity}, log), st
}

func TestAIClientUsesTheStoredEndpointWithoutFoundry(t *testing.T) {
	eng, _ := aiTestEngine(t, foundry.Identity{})
	if eng.Foundry().Enabled() {
		t.Fatal("Foundry mode must stay off without an identity")
	}
	settings := store.AppSettings{AIBaseURL: "https://api.openai.com/v1", AIModel: "gpt-4o-mini", AIAPIKey: "k"}
	client, err := eng.AIClient(t.Context(), settings)
	if err != nil || !client.Configured() {
		t.Fatalf("AIClient = %v, configured = %v", err, client.Configured())
	}
}

func TestAIClientReportsAnUnavailableFoundryDiscovery(t *testing.T) {
	eng, st := aiTestEngine(t, foundry.Identity{ResourceID: "nonsense", TenantID: "x",
		ClientID: "y", ClientSecret: "z"})
	if !eng.Foundry().Enabled() {
		t.Fatal("a supplied identity must enable Foundry mode")
	}
	if _, err := eng.AIClient(t.Context(), store.AppSettings{AIModel: "gpt"}); err == nil {
		t.Fatal("an unusable identity must be reported instead of silently falling back")
	}

	// A failing discovery must not abort a scan: detection keeps working and
	// every candidate simply goes to manual review.
	sc, err := eng.newScanContext(t.Context())
	if err != nil {
		t.Fatalf("newScanContext: %v", err)
	}
	if sc.client == nil || sc.client.Configured() {
		t.Fatal("the scan must continue with an unconfigured AI client")
	}
	if _, err := st.ListLibraries(t.Context()); err != nil {
		t.Fatalf("store still usable: %v", err)
	}
}

func TestAIClientRequiresASelectedDeployment(t *testing.T) {
	eng, _ := aiTestEngine(t, foundry.Identity{ResourceID: "nonsense"})
	_, err := eng.AIClient(t.Context(), store.AppSettings{})
	if err == nil || !strings.Contains(err.Error(), "Cognitive Services") {
		t.Fatalf("err = %v; want the identity validation error", err)
	}
}
