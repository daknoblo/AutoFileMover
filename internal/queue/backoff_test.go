package queue

import (
	"errors"
	"io/fs"
	"syscall"
	"testing"
	"time"

	"github.com/daknoblo/AutoFileMover/internal/engine"
)

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	first := Backoff(1)
	if first < baseDelay || first > 2*baseDelay {
		t.Fatalf("first delay %v outside the jittered base range", first)
	}
	// Averaging removes the jitter so the growth itself can be asserted.
	avg := func(attempt int) time.Duration {
		var total time.Duration
		for i := 0; i < 50; i++ {
			total += Backoff(attempt)
		}
		return total / 50
	}
	if avg(4) <= avg(2) {
		t.Fatal("backoff must grow with the attempt count")
	}
	for _, attempt := range []int{20, 100, 1 << 20} {
		if d := Backoff(attempt); d > time.Duration(float64(maxDelay)*(1+jitterFraction)) {
			t.Fatalf("attempt %d: delay %v exceeds the cap", attempt, d)
		}
	}
}

func TestIsPermanent(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unknown kind", errUnknownKind, true},
		{"engine dry run", engine.ErrDryRun, true},
		{"engine no target", engine.ErrNoTarget, true},
		{"engine item busy", engine.ErrItemBusy, false},
		{"missing file", fs.ErrNotExist, true},
		{"permission", fs.ErrPermission, true},
		{"io error", syscall.EIO, false},
		{"timed out", syscall.ETIMEDOUT, false},
		{"disk full", syscall.ENOSPC, false},
		{"stale handle", syscall.ESTALE, false},
		{"wrapped io error", errors.New("move: " + syscall.EIO.Error()), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPermanent(tc.err); got != tc.want {
				t.Fatalf("isPermanent(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestTransientErrorsAreRetriedByDefault guards the safe default: an error the
// classifier does not recognise must be retried rather than silently dropped.
func TestTransientErrorsAreRetriedByDefault(t *testing.T) {
	if isPermanent(errors.New("connection reset by peer")) {
		t.Fatal("unknown errors must be treated as transient")
	}
}

// TestNoOpErrorsCountAsSuccess covers the double-click case: the work is already
// done, so the job must not end up as a red "failed" entry.
func TestNoOpErrorsCountAsSuccess(t *testing.T) {
	for _, err := range []error{engine.ErrNothingToDo, engine.ErrFileDone} {
		if !isNoOp(err) {
			t.Fatalf("%v should be treated as an already-satisfied no-op", err)
		}
	}
	if isNoOp(engine.ErrNoTarget) {
		t.Fatal("a missing target is a real failure, not a no-op")
	}
}
