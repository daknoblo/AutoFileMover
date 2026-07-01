package engine

import "time"

// Phase values for Progress.
const (
	PhaseIdle        = "idle"
	PhaseScanning    = "scanning"
	PhaseClassifying = "classifying"
	PhaseMoving      = "moving"
)

// Progress describes the state of an in-flight scan for the UI status display.
type Progress struct {
	// Active is true while a scan is running.
	Active bool `json:"active"`
	// Phase is the current activity: idle, scanning or classifying.
	Phase string `json:"phase"`
	// Current is the name of the folder/file being processed right now.
	Current string `json:"current"`
	// Done is the number of candidates already processed.
	Done int `json:"done"`
	// Total is the number of candidates in this scan.
	Total int `json:"total"`
	// Percent is Done/Total as a whole number (0..100).
	Percent int `json:"percent"`
	// ETASeconds is the estimated remaining time in seconds.
	ETASeconds int `json:"eta_seconds"`

	startedAt time.Time
}

// GetProgress returns a snapshot of the current scan progress.
func (e *Engine) GetProgress() Progress {
	e.progMu.Lock()
	defer e.progMu.Unlock()
	if e.prog.Phase == "" {
		e.prog.Phase = PhaseIdle
	}
	return e.prog
}

// beginScan marks the start of a scan in the filesystem-reading phase.
func (e *Engine) beginScan() {
	e.startPhase(PhaseScanning, 0)
}

// startPhase resets the progress to an active state in the given phase.
func (e *Engine) startPhase(phase string, total int) {
	e.progMu.Lock()
	e.prog = Progress{Active: true, Phase: phase, Total: total, startedAt: time.Now()}
	e.progMu.Unlock()
}

// setPhase changes the current activity label without resetting progress.
func (e *Engine) setPhase(phase string) {
	e.progMu.Lock()
	e.prog.Phase = phase
	e.progMu.Unlock()
}

func (e *Engine) setTotal(total int) {
	e.progMu.Lock()
	e.prog.Total = total
	e.progMu.Unlock()
}

func (e *Engine) updateProgress(done int, current string) {
	e.progMu.Lock()
	e.prog.Done = done
	e.prog.Current = current
	if e.prog.Total > 0 {
		e.prog.Percent = done * 100 / e.prog.Total
	}
	if elapsed := time.Since(e.prog.startedAt).Seconds(); done > 0 && done < e.prog.Total {
		e.prog.ETASeconds = int((elapsed / float64(done)) * float64(e.prog.Total-done))
	} else {
		e.prog.ETASeconds = 0
	}
	e.progMu.Unlock()
}

func (e *Engine) finishProgress() {
	e.progMu.Lock()
	e.prog.Active = false
	e.prog.Phase = PhaseIdle
	e.prog.Current = ""
	e.prog.ETASeconds = 0
	if e.prog.Total > 0 {
		e.prog.Done = e.prog.Total
		e.prog.Percent = 100
	}
	e.progMu.Unlock()
}
