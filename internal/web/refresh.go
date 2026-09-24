package web

import (
	"context"
	"net/http"
)

type sourceRefresh struct {
	done chan struct{}
	err  error
}

// refreshSources shares in-flight reads between pollers. A stuck filesystem
// syscall can outlive the deadline, but cannot spawn more refresh goroutines.
func (s *Server) refreshSources(parent context.Context) error {
	s.refreshMu.Lock()
	call := s.refresh
	if call == nil {
		call = &sourceRefresh{done: make(chan struct{})}
		s.refresh = call
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), fsReadTimeout)
			defer cancel()
			call.err = s.engine.RefreshSources(ctx)
			if call.err != nil {
				s.log.Warn("refresh source listings", "err", call.err)
			}
			s.refreshMu.Lock()
			close(call.done)
			s.refresh = nil
			s.refreshMu.Unlock()
		}()
	}
	s.refreshMu.Unlock()

	ctx, cancel := context.WithTimeout(parent, fsReadTimeout)
	defer cancel()
	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return ctx.Err()
	}
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
