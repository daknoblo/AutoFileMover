package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/daknoblo/AutoFileMover/internal/store"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

// validatePath ensures p is an absolute, existing directory inside the media root.
func (s *Server) validatePath(p string) error {
	if p == "" || !filepath.IsAbs(p) {
		return fmt.Errorf("path must be absolute")
	}
	clean := filepath.Clean(p)
	root := filepath.Clean(s.cfg.MediaRoot)
	if clean != root && !strings.HasPrefix(clean, root+string(os.PathSeparator)) {
		return fmt.Errorf("path must be inside the media root (%s)", root)
	}
	info, err := os.Stat(clean)
	if err != nil {
		return fmt.Errorf("path does not exist: %s", clean)
	}
	if !info.IsDir() {
		return fmt.Errorf("path is not a directory")
	}
	return nil
}

// ---- Health ----

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---- Settings ----

type settingsDTO struct {
	AIBaseURL    string  `json:"ai_base_url"`
	AIModel      string  `json:"ai_model"`
	AIAPIVersion string  `json:"ai_api_version"`
	AIAPIKey     string  `json:"ai_api_key,omitempty"`
	HasAPIKey    bool    `json:"has_api_key"`
	Threshold    float64 `json:"threshold"`
	AutoMove     bool    `json:"auto_move"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.LoadAppSettings(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settingsDTO{
		AIBaseURL:    a.AIBaseURL,
		AIModel:      a.AIModel,
		AIAPIVersion: a.AIAPIVersion,
		HasAPIKey:    a.AIAPIKey != "",
		Threshold:    a.Threshold,
		AutoMove:     a.AutoMove,
	})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var dto settingsDTO
	if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if dto.Threshold < 0 || dto.Threshold > 1 {
		writeErr(w, http.StatusBadRequest, "threshold must be between 0 and 1")
		return
	}
	err := s.store.SaveAppSettings(r.Context(), store.AppSettings{
		AIBaseURL:    strings.TrimSpace(dto.AIBaseURL),
		AIModel:      strings.TrimSpace(dto.AIModel),
		AIAPIVersion: strings.TrimSpace(dto.AIAPIVersion),
		AIAPIKey:     dto.AIAPIKey, // empty -> keep existing
		Threshold:    dto.Threshold,
		AutoMove:     dto.AutoMove,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.handleGetSettings(w, r)
}

// ---- Sources ----

func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	src, err := s.store.ListSources(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, src)
}

func (s *Server) handleCreateSource(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	path := filepath.Clean(strings.TrimSpace(body.Path))
	if err := s.validatePath(path); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	src, err := s.store.AddSource(r.Context(), path)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	s.resync()
	writeJSON(w, http.StatusCreated, src)
}

func (s *Server) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteSource(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.resync()
	w.WriteHeader(http.StatusNoContent)
}

// ---- Libraries ----

func (s *Server) handleListLibraries(w http.ResponseWriter, r *http.Request) {
	libs, err := s.store.ListLibraries(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, libs)
}

func (s *Server) handleCreateLibrary(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	name := strings.TrimSpace(body.Name)
	kind := strings.TrimSpace(body.Kind)
	path := filepath.Clean(strings.TrimSpace(body.Path))
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	switch kind {
	case store.KindMovie, store.KindSeries, store.KindDocumentary:
	default:
		writeErr(w, http.StatusBadRequest, "kind must be movie, series or documentary")
		return
	}
	if err := s.validatePath(path); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	lib, err := s.store.AddLibrary(r.Context(), name, kind, path)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, lib)
}

func (s *Server) handleDeleteLibrary(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteLibrary(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLibraryFolders(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	lib, err := s.store.GetLibrary(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "library not found")
		return
	}
	entries, err := os.ReadDir(lib.Path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	folders := []string{}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			folders = append(folders, e.Name())
		}
	}
	writeJSON(w, http.StatusOK, folders)
}

// ---- Items ----

func (s *Server) handleListItems(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	items, err := s.store.ListItems(r.Context(), status, 500)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []store.Item{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleConfirmItem(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		LibraryID int64  `json:"library_id"`
		SubFolder string `json:"sub_folder"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.LibraryID == 0 {
		writeErr(w, http.StatusBadRequest, "library_id is required")
		return
	}
	if err := s.engine.ConfirmItem(r.Context(), id, body.LibraryID, strings.TrimSpace(body.SubFolder)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "moved"})
}

func (s *Server) handleRejectItem(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.engine.RejectItem(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
}

func (s *Server) handleDeleteItem(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteItem(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Scan ----

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	go s.engine.ProcessAll(context.Background())
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "scan started"})
}

func (s *Server) resync() {
	if s.resyncer != nil {
		s.resyncer.Resync(context.Background())
	}
}
