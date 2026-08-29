package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/daknoblo/AutoFileMover/internal/engine"
	"github.com/daknoblo/AutoFileMover/internal/logbuf"
	"github.com/daknoblo/AutoFileMover/internal/mediainfo"
	"github.com/daknoblo/AutoFileMover/internal/store"
	"github.com/daknoblo/AutoFileMover/internal/version"
)

// itemActionTimeout bounds how long a database-only item action waits for the
// item lock. Exceeding it means the worker or a scan currently owns the item,
// which the UI shows as "busy" instead of letting the request hang.
const itemActionTimeout = 2 * time.Second

// fsReadTimeout bounds directory listings so a stalled share fails fast with a
// clear message instead of running into the server's write timeout.
const fsReadTimeout = 5 * time.Second

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("write json response", "err", err)
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeItemErr maps an engine error onto a status code. A busy item is a
// temporary condition the UI retries, not a client mistake.
func writeItemErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, engine.ErrItemBusy), errors.Is(err, context.DeadlineExceeded):
		writeErr(w, http.StatusConflict, engine.ErrItemBusy.Error())
	case errors.Is(err, engine.ErrItemNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	default:
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}

// writeFSErr reports a storage read that timed out as 503 so the UI can tell a
// slow share apart from a genuine error.
func writeFSErr(w http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		writeErr(w, http.StatusServiceUnavailable, "Speicher antwortet nicht rechtzeitig – bitte erneut versuchen")
		return
	}
	writeErr(w, http.StatusInternalServerError, err.Error())
}

// itemActionContext bounds a database-only item action so it never blocks on a
// long-running operation for the same item.
func (s *Server) itemActionContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), itemActionTimeout)
}

// withFSDeadline runs a blocking filesystem read on its own goroutine and gives
// up after fsReadTimeout. The goroutine finishes on its own; it only writes to
// its private channel, so an abandoned call leaks nothing.
func withFSDeadline[T any](parent context.Context, fn func() (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(parent, fsReadTimeout)
	defer cancel()

	type result struct {
		val T
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := fn()
		done <- result{v, err}
	}()
	select {
	case res := <-done:
		return res.val, res.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

// splitLines parses a newline/comma separated list, trimming blanks.
func splitLines(s string) []string {
	repl := strings.ReplaceAll(s, ",", "\n")
	out := []string{}
	for _, line := range strings.Split(repl, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// resolveWithinMediaRoot resolves symlinks for p when possible and ensures the
// resulting path stays inside the configured media root.
func (s *Server) resolveWithinMediaRoot(p string) (string, error) {
	root, err := filepath.EvalSymlinks(filepath.Clean(s.cfg.MediaRoot))
	if err != nil {
		return "", fmt.Errorf("media root does not exist: %s", filepath.Clean(s.cfg.MediaRoot))
	}
	clean := filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		clean = filepath.Clean(resolved)
	}
	// Keep clean inside the media root after resolving symlinks: filepath.Rel
	// yields a ".." prefix when clean escapes root, which we reject.
	if rel, relErr := filepath.Rel(root, clean); relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path must be inside the media root (%s)", root)
	}
	return clean, nil
}

// validatePath ensures p is an absolute, existing directory inside the media root.
func (s *Server) validatePath(p string) error {
	if p == "" || !filepath.IsAbs(p) {
		return fmt.Errorf("path must be absolute")
	}
	clean, err := s.resolveWithinMediaRoot(p)
	if err != nil {
		return err
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

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("ok")); err != nil {
		return
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---- Version ----

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, version.Get())
}

// ---- Scan status ----

// statusDTO combines scan progress, the storage health probe and the queue
// counters the header badge shows.
type statusDTO struct {
	engine.Progress
	FSWritable   bool   `json:"fs_writable"`
	FSMessage    string `json:"fs_message"`
	QueuePending int    `json:"queue_pending"`
	QueueRunning int    `json:"queue_running"`
	QueueFailed  int    `json:"queue_failed"`
	QueuePaused  bool   `json:"queue_paused"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	health, paused := s.queue.Health()
	dto := statusDTO{
		Progress:    s.engine.GetProgress(),
		FSWritable:  health.Writable,
		FSMessage:   health.Message,
		QueuePaused: paused,
	}
	if counts, err := s.store.CountJobs(r.Context()); err != nil {
		s.log.Warn("count jobs", "err", err)
	} else {
		dto.QueuePending, dto.QueueRunning, dto.QueueFailed = counts.Pending, counts.Running, counts.Failed
	}
	writeJSON(w, http.StatusOK, dto)
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
	DryRun       bool    `json:"dry_run"`
	Ignore       string  `json:"ignore_patterns"`
	AIContext    string  `json:"ai_context"`
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
		DryRun:       a.DryRun,
		Ignore:       strings.Join(a.IgnorePatterns, "\n"),
		AIContext:    a.AIContext,
	})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var dto settingsDTO
	if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	// Reject NaN/Inf before the range check: NaN comparisons are always false,
	// so a NaN threshold would otherwise slip through the "0..1" bounds test.
	if math.IsNaN(dto.Threshold) || math.IsInf(dto.Threshold, 0) {
		writeErr(w, http.StatusBadRequest, "threshold must be a real number")
		return
	}
	if dto.Threshold < 0 || dto.Threshold > 1 {
		writeErr(w, http.StatusBadRequest, "threshold must be between 0 and 1")
		return
	}
	if base := strings.TrimSpace(dto.AIBaseURL); base != "" {
		u, perr := url.Parse(base)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			writeErr(w, http.StatusBadRequest, "ai_base_url must be a valid http(s) URL")
			return
		}
	}
	err := s.store.SaveAppSettings(r.Context(), store.AppSettings{
		AIBaseURL:      strings.TrimSpace(dto.AIBaseURL),
		AIModel:        strings.TrimSpace(dto.AIModel),
		AIAPIVersion:   strings.TrimSpace(dto.AIAPIVersion),
		AIAPIKey:       dto.AIAPIKey, // empty -> keep existing
		Threshold:      dto.Threshold,
		AutoMove:       dto.AutoMove,
		IgnorePatterns: splitLines(dto.Ignore),
		AIContext:      strings.TrimSpace(dto.AIContext),
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
		Name          string `json:"name"`
		Kind          string `json:"kind"`
		Path          string `json:"path"`
		UseSubfolders *bool  `json:"use_subfolders"`
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
	// The create form may override the kind-based sub-folder default.
	if body.UseSubfolders != nil && *body.UseSubfolders != lib.UseSubfolders {
		if err := s.store.SetLibraryUseSubfolders(r.Context(), lib.ID, *body.UseSubfolders); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		lib.UseSubfolders = *body.UseSubfolders
	}
	writeJSON(w, http.StatusCreated, lib)
}

// handleUpdateLibrary updates a library's per-title sub-folder routing flag.
func (s *Server) handleUpdateLibrary(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		UseSubfolders *bool `json:"use_subfolders"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.UseSubfolders == nil {
		writeErr(w, http.StatusBadRequest, "use_subfolders is required")
		return
	}
	if err := s.store.SetLibraryUseSubfolders(r.Context(), id, *body.UseSubfolders); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"use_subfolders": *body.UseSubfolders})
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
	folders, err := withFSDeadline(r.Context(), func() ([]string, error) {
		entries, rerr := os.ReadDir(lib.Path)
		if rerr != nil {
			return nil, rerr
		}
		out := []string{}
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				out = append(out, e.Name())
			}
		}
		return out, nil
	})
	if err != nil {
		writeFSErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, folders)
}

// ---- Items ----

// itemDTO is an item plus the state of its outstanding queue job, so a review
// card can show "queued", "running" or "failed" without an extra request.
type itemDTO struct {
	store.Item
	QueueState   string `json:"queue_state,omitempty"`
	QueueKind    string `json:"queue_kind,omitempty"`
	QueueError   string `json:"queue_error,omitempty"`
	QueueAttempt int    `json:"queue_attempt,omitempty"`
	QueueRetryIn int    `json:"queue_retry_in,omitempty"`
}

func (s *Server) handleListItems(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	items, err := s.store.ListItems(r.Context(), status, 500)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jobs, err := s.store.OpenJobsByItem(r.Context())
	if err != nil {
		s.log.Warn("load open jobs", "err", err)
		jobs = map[int64]store.Job{}
	}
	out := make([]itemDTO, 0, len(items))
	for i := range items {
		// Enrich every file with a quality summary derived from its name so the
		// UI can always show resolution/codec/source next to the size.
		for j := range items[i].Files {
			if items[i].Files[j].RelPath != "" {
				items[i].Files[j].Quality = mediainfo.Parse(items[i].Files[j].RelPath).Summary()
			}
		}
		dto := itemDTO{Item: items[i]}
		if job, ok := jobs[items[i].ID]; ok {
			dto.QueueState = job.Status
			dto.QueueKind = job.Kind
			dto.QueueError = job.LastError
			dto.QueueAttempt = job.Attempts
			if job.Status == store.JobPending && job.Attempts > 0 {
				if wait := int(time.Until(job.RunAfter).Seconds()); wait > 0 {
					dto.QueueRetryIn = wait
				}
			}
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, out)
}

// enqueue records a filesystem action for the background worker and answers
// immediately. Nothing in the request path touches the storage, so the UI stays
// responsive even while the share is saturated.
func (s *Server) enqueue(w http.ResponseWriter, r *http.Request, id int64, kind string, payload store.JobPayload) {
	// GetItem reports a missing row as (nil, nil), so the value must be checked
	// as well or a job would be queued for an item that no longer exists.
	item, err := s.store.GetItem(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if item == nil {
		writeErr(w, http.StatusNotFound, engine.ErrItemNotFound.Error())
		return
	}
	job, err := s.store.EnqueueJob(r.Context(), id, kind, payload)
	if errors.Is(err, store.ErrJobExists) {
		writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "duplicate": true})
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.queue.Notify()
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "job_id": job.ID})
}

func (s *Server) handleConfirmItem(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	s.enqueue(w, r, id, store.JobApplyPlan, store.JobPayload{})
}

// handleSetItemTarget assigns a target library (and optional series sub-folder)
// to an item, used when the AI could not resolve a destination.
func (s *Server) handleSetItemTarget(w http.ResponseWriter, r *http.Request) {
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
	ctx, cancel := s.itemActionContext(r)
	defer cancel()
	if err := s.engine.SetItemTarget(ctx, id, body.LibraryID, strings.TrimSpace(body.SubFolder)); err != nil {
		writeItemErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleFileAction queues a single file's move/delete for the background worker.
func (s *Server) handleFileAction(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		RelPath string `json:"rel_path"`
		Action  string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	s.enqueue(w, r, id, store.JobFileAction, store.JobPayload{RelPath: body.RelPath, Action: body.Action})
}

// handlePlanFileAction sets the planned action for a single file without
// executing it; the actual move/delete happens later via confirm (ApplyPlan).
func (s *Server) handlePlanFileAction(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		RelPath string `json:"rel_path"`
		Action  string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	ctx, cancel := s.itemActionContext(r)
	defer cancel()
	if err := s.engine.PlanFileAction(ctx, id, body.RelPath, body.Action); err != nil {
		writeItemErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "planned"})
}

// handleResolveConflict records the user's choice for a file that collides with
// an existing target file: "replace" (overwrite) or "keep" (keep the existing
// file and drop the incoming duplicate).
func (s *Server) handleResolveConflict(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		RelPath    string `json:"rel_path"`
		Resolution string `json:"resolution"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	ctx, cancel := s.itemActionContext(r)
	defer cancel()
	if err := s.engine.ResolveConflict(ctx, id, body.RelPath, body.Resolution); err != nil {
		writeItemErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

// handleReclassifyItem queues a fresh AI classification for one item.
func (s *Server) handleReclassifyItem(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	s.enqueue(w, r, id, store.JobReclassify, store.JobPayload{})
}

// handleCreateItemFolder queues creation of a destination folder for an item.
// With no body it creates the AI-suggested folder; with a {library_id, folder}
// body it creates a folder named by the user under that library (manual review
// when the desired folder does not exist yet).
func (s *Server) handleCreateItemFolder(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		LibraryID int64  `json:"library_id"`
		Folder    string `json:"folder"`
	}
	// The suggested-folder case sends no body at all; anything else must parse.
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	folder := strings.TrimSpace(body.Folder)
	if folder != "" && body.LibraryID == 0 {
		writeErr(w, http.StatusBadRequest, "library_id is required")
		return
	}
	payload := store.JobPayload{Folder: folder}
	if folder != "" {
		payload.LibraryID = body.LibraryID
	}
	s.enqueue(w, r, id, store.JobCreateFolder, payload)
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
	s.log.Info("manual scan requested")
	go s.engine.ProcessAll(context.Background())
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "scan started"})
}

// ---- Logs & level ----

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	lines := []string{}
	if s.logs != nil {
		lines = s.logs.Lines()
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

func (s *Server) handleGetLogLevel(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"level": logbuf.LevelName(s.level.Level())})
}

func (s *Server) handleSetLogLevel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Level string `json:"level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	lvl := logbuf.ParseLevel(body.Level)
	s.level.Set(lvl)
	_ = s.store.SetSetting(r.Context(), "log_level", logbuf.LevelName(lvl))
	s.log.Info("log level changed", "level", logbuf.LevelName(lvl))
	writeJSON(w, http.StatusOK, map[string]string{"level": logbuf.LevelName(lvl)})
}

// ---- What-if (dry-run) ----

func (s *Server) handleSetDryRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := s.store.SetDryRun(r.Context(), body.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"dry_run": body.Enabled})
}

// ---- Folder browser & descriptions ----

type browseEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Description string `json:"description"`
}

type browseResponse struct {
	Path    string        `json:"path"`
	Parent  string        `json:"parent"`
	AtRoot  bool          `json:"at_root"`
	Entries []browseEntry `json:"entries"`
}

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	root, err := s.resolveWithinMediaRoot(s.cfg.MediaRoot)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	p := strings.TrimSpace(r.URL.Query().Get("path"))
	if p == "" {
		p = root
	}
	clean, err := s.resolveWithinMediaRoot(p)
	if err != nil {
		// Constrain browsing to the media root; fall back to the root when the
		// requested path escapes it, including via symlinks.
		clean = root
	}
	info, err := os.Stat(clean)
	if err != nil || !info.IsDir() {
		writeErr(w, http.StatusBadRequest, "path is not a directory")
		return
	}

	notes, err := s.store.FolderNotesByPath(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dirEntries, err := withFSDeadline(r.Context(), func() ([]os.DirEntry, error) {
		return os.ReadDir(clean)
	})
	if err != nil {
		writeFSErr(w, err)
		return
	}
	entries := []browseEntry{}
	for _, e := range dirEntries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(clean, e.Name())
		entries = append(entries, browseEntry{Name: e.Name(), Path: full, Description: notes[full]})
	}

	parent := filepath.Dir(clean)
	atRoot := clean == root
	if atRoot {
		parent = clean
	}
	writeJSON(w, http.StatusOK, browseResponse{
		Path:    clean,
		Parent:  parent,
		AtRoot:  atRoot,
		Entries: entries,
	})
}

// ---- Queue ----

// queueResponse is the payload of the queue tab: the jobs plus the storage
// health that decides whether the worker may run at all.
type queueResponse struct {
	Jobs    []store.Job     `json:"jobs"`
	Counts  store.JobCounts `json:"counts"`
	Paused  bool            `json:"paused"`
	Message string          `json:"message"`
}

func (s *Server) handleListQueue(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.ListJobs(r.Context(), 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	counts, err := s.store.CountJobs(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	health, paused := s.queue.Health()
	writeJSON(w, http.StatusOK, queueResponse{
		Jobs:    jobs,
		Counts:  counts,
		Paused:  paused,
		Message: health.Message,
	})
}

func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.RetryJob(r.Context(), id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.queue.Notify()
	writeJSON(w, http.StatusOK, map[string]string{"status": "queued"})
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteJob(r.Context(), id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListFolderNotes(w http.ResponseWriter, r *http.Request) {
	notes, err := s.store.ListFolderNotes(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if notes == nil {
		notes = []store.FolderNote{}
	}
	writeJSON(w, http.StatusOK, notes)
}

func (s *Server) handleSetFolderNote(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path        string `json:"path"`
		Description string `json:"description"`
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
	if err := s.store.SetFolderNote(r.Context(), path, strings.TrimSpace(body.Description)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, store.FolderNote{Path: path, Description: strings.TrimSpace(body.Description)})
}

func (s *Server) resync() {
	if s.resyncer != nil {
		s.resyncer.Resync(context.Background())
	}
}
