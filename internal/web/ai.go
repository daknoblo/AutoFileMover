package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/daknoblo/AutoFileMover/internal/foundry"
)

// foundryTimeout bounds a discovery request so a settings page load cannot hang
// on an unreachable Azure endpoint, and stays below the server write timeout.
const foundryTimeout = 40 * time.Second

// aiTestTimeout bounds the connection test. A ping needs far less than a real
// classification, and the user is waiting for the answer.
const aiTestTimeout = 30 * time.Second

func (s *Server) handleFoundryStatus(w http.ResponseWriter, r *http.Request) {
	s.writeFoundryStatus(w, r, false)
}

func (s *Server) handleFoundryRefresh(w http.ResponseWriter, r *http.Request) {
	s.writeFoundryStatus(w, r, true)
}

// writeFoundryStatus returns the discovered endpoint and the selectable chat
// deployments. A discovery failure is part of the payload rather than an HTTP
// error, so the settings page can show the reason next to the field instead of
// a generic toast.
func (s *Server) writeFoundryStatus(w http.ResponseWriter, r *http.Request, force bool) {
	provider := s.engine.Foundry()
	if !provider.Enabled() {
		writeJSON(w, http.StatusOK, foundry.Status{})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), foundryTimeout)
	defer cancel()
	status := provider.Status(ctx, force)
	if status.Error != "" {
		s.log.Warn("azure foundry discovery failed", "err", status.Error, "forced", force)
	}
	writeJSON(w, http.StatusOK, status)
}

// handleTestAI verifies the saved configuration against the live endpoint, so a
// wrong key, an unreachable host or a missing role is reported immediately
// instead of only on the next scan.
func (s *Server) handleTestAI(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.LoadAppSettings(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), aiTestTimeout)
	defer cancel()

	client, err := s.engine.AIClient(ctx, settings)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !client.Configured() {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false,
			"error": "the AI endpoint is not configured yet"})
		return
	}
	start := time.Now()
	if err := client.Ping(ctx); err != nil {
		s.log.Warn("ai connection test failed", "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": testError(err)})
		return
	}
	s.log.Info("ai connection test succeeded", "duration_ms", time.Since(start).Milliseconds())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true,
		"duration_ms": time.Since(start).Milliseconds()})
}

// testError keeps a cancelled request from being reported as a broken endpoint.
func testError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "the AI endpoint did not answer in time"
	}
	return err.Error()
}
