package web

import (
	"context"
	"net/http"
	"time"
)

const sourceRefreshInterval = 10 * time.Second

type sourceRefreshStatus struct {
	Running       bool      `json:"running"`
	StartedAt     time.Time `json:"started_at,omitzero"`
	FinishedAt    time.Time `json:"finished_at,omitzero"`
	LastSuccessAt time.Time `json:"last_success_at,omitzero"`
	Error         string    `json:"error,omitempty"`
}

type sourceRefresh struct {
	done   chan struct{}
	status sourceRefreshStatus
}

// scheduleSourceRefresh never waits for storage. A slow refresh runs to
// completion instead of being aborted and restarted at every HTTP poll.
func (s *Server) scheduleSourceRefresh(refresh func(context.Context) error) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	var previous sourceRefreshStatus
	if s.refresh != nil {
		previous = s.refresh.status
		if previous.Running || time.Since(previous.FinishedAt) < sourceRefreshInterval {
			return
		}
	}
	call := &sourceRefresh{
		done: make(chan struct{}),
		status: sourceRefreshStatus{
			Running: true, StartedAt: time.Now().UTC(),
			FinishedAt: previous.FinishedAt, LastSuccessAt: previous.LastSuccessAt,
			Error: previous.Error,
		},
	}
	s.refresh = call
	go func() {
		err := refresh(context.Background())
		if err != nil {
			s.log.Warn("refresh source listings", "err", err)
		}
		s.refreshMu.Lock()
		defer s.refreshMu.Unlock()
		call.status.Running = false
		call.status.FinishedAt = time.Now().UTC()
		if err != nil {
			call.status.Error = err.Error()
		} else {
			call.status.Error = ""
			call.status.LastSuccessAt = call.status.FinishedAt
		}
		close(call.done)
	}()
}

func (s *Server) sourceRefreshStatus() sourceRefreshStatus {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if s.refresh == nil {
		return sourceRefreshStatus{}
	}
	return s.refresh.status
}

func (s *Server) handleClearHistory(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.ClearHistory(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("history cleared", "items", n)
	writeJSON(w, http.StatusOK, map[string]int64{"removed": n})
}
