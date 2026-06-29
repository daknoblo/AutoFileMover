// Package web exposes the REST API and the embedded single-page UI.
package web

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/daknoblo/AutoFileMover/internal/config"
	"github.com/daknoblo/AutoFileMover/internal/engine"
	"github.com/daknoblo/AutoFileMover/internal/logbuf"
	"github.com/daknoblo/AutoFileMover/internal/store"
)

//go:embed static
var staticFS embed.FS

// Resyncer is implemented by the watcher so the API can refresh watched paths
// when sources change.
type Resyncer interface {
	Resync(ctx context.Context)
}

// Server is the HTTP server.
type Server struct {
	store    *store.Store
	engine   *engine.Engine
	cfg      config.Config
	log      *slog.Logger
	resyncer Resyncer
	logs     *logbuf.Buffer
	level    *slog.LevelVar
}

// NewServer creates the HTTP server.
func NewServer(st *store.Store, eng *engine.Engine, cfg config.Config, log *slog.Logger, resyncer Resyncer, logs *logbuf.Buffer, level *slog.LevelVar) *Server {
	return &Server{store: st, engine: eng, cfg: cfg, log: log, resyncer: resyncer, logs: logs, level: level}
}

// Handler builds the http.Handler with all routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// API.
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/version", s.handleVersion)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePutSettings)

	mux.HandleFunc("GET /api/sources", s.handleListSources)
	mux.HandleFunc("POST /api/sources", s.handleCreateSource)
	mux.HandleFunc("DELETE /api/sources/{id}", s.handleDeleteSource)

	mux.HandleFunc("GET /api/libraries", s.handleListLibraries)
	mux.HandleFunc("POST /api/libraries", s.handleCreateLibrary)
	mux.HandleFunc("DELETE /api/libraries/{id}", s.handleDeleteLibrary)
	mux.HandleFunc("GET /api/libraries/{id}/folders", s.handleLibraryFolders)

	mux.HandleFunc("GET /api/items", s.handleListItems)
	mux.HandleFunc("POST /api/items/{id}/confirm", s.handleConfirmItem)
	mux.HandleFunc("POST /api/items/{id}/target", s.handleSetItemTarget)
	mux.HandleFunc("POST /api/items/{id}/file-action", s.handleFileAction)
	mux.HandleFunc("POST /api/items/{id}/file-plan", s.handlePlanFileAction)
	mux.HandleFunc("POST /api/items/{id}/reject", s.handleRejectItem)
	mux.HandleFunc("DELETE /api/items/{id}", s.handleDeleteItem)

	mux.HandleFunc("POST /api/scan", s.handleScan)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("PUT /api/dry-run", s.handleSetDryRun)

	mux.HandleFunc("GET /api/logs", s.handleLogs)
	mux.HandleFunc("GET /api/log-level", s.handleGetLogLevel)
	mux.HandleFunc("PUT /api/log-level", s.handleSetLogLevel)

	// Folder browser & per-folder descriptions (AI context).
	mux.HandleFunc("GET /api/browse", s.handleBrowse)
	mux.HandleFunc("GET /api/folder-notes", s.handleListFolderNotes)
	mux.HandleFunc("PUT /api/folder-notes", s.handleSetFolderNote)

	// Static UI.
	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	return logRequests(s.log, mux)
}

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Debug("http", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
