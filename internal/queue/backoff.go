package queue

import (
	"errors"
	"io/fs"
	"math"
	"math/rand/v2"
	"syscall"
	"time"

	"github.com/daknoblo/AutoFileMover/internal/engine"
)

const (
	// baseDelay is the wait after the first transient failure.
	baseDelay = 2 * time.Second
	// maxDelay caps the exponential growth.
	maxDelay = 5 * time.Minute
	// jitterFraction spreads retries so a batch of failed jobs does not hit the
	// storage again all at once.
	jitterFraction = 0.2
)

// Backoff returns the delay before attempt number n (1-based), growing
// exponentially up to maxDelay with a small random spread.
func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	// Cap the exponent before shifting so the multiplication cannot overflow.
	exp := math.Min(float64(attempt-1), 20)
	d := time.Duration(float64(baseDelay) * math.Pow(2, exp))
	if d > maxDelay || d <= 0 {
		d = maxDelay
	}
	jitter := (rand.Float64()*2 - 1) * jitterFraction * float64(d)
	d += time.Duration(jitter)
	if d < baseDelay {
		d = baseDelay
	}
	return d
}

// isNoOp reports whether the job's work was already carried out, typically
// because the user clicked twice and an earlier job finished first. Recording
// that as a failure would put a red badge on completed work.
func isNoOp(err error) bool {
	return errors.Is(err, engine.ErrNothingToDo) || errors.Is(err, engine.ErrFileDone)
}

// isPermanent reports whether an error will never succeed on a retry. Anything
// not classified as permanent is treated as transient, which is the safe
// default: a job is retried rather than silently dropped.
func isPermanent(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errUnknownKind) || engine.IsPermanent(err) {
		return true
	}
	// A vanished source or a target that already exists cannot be fixed by
	// waiting; a permission problem needs an operator, not another attempt.
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrExist) || errors.Is(err, fs.ErrPermission) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.ENOENT, syscall.EEXIST, syscall.EACCES, syscall.EPERM,
			syscall.EISDIR, syscall.ENOTDIR, syscall.EINVAL, syscall.ENAMETOOLONG,
			syscall.EXDEV, syscall.EROFS:
			return true
		}
		// Everything else from the kernel (EIO, ETIMEDOUT, EAGAIN, EBUSY, ESTALE,
		// ENOSPC, ECONNRESET, EHOSTDOWN, ENETUNREACH …) is a storage hiccup we
		// want to retry.
		return false
	}
	return false
}
