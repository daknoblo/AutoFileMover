// Package queue runs filesystem work in the background so the web UI stays
// responsive when the underlying storage (e.g. a saturated SMB share) is slow.
// User actions are recorded as jobs and executed here one at a time, with a
// health gate that pauses the queue while the media root is unreachable and an
// exponential backoff for transient I/O errors.
package queue

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/daknoblo/AutoFileMover/internal/config"
	"github.com/daknoblo/AutoFileMover/internal/engine"
	"github.com/daknoblo/AutoFileMover/internal/mover"
	"github.com/daknoblo/AutoFileMover/internal/store"
)

const (
	// idlePoll is how long the worker sleeps when no job is due.
	idlePoll = time.Second
	// healthInterval is the gap between two media-root writability probes while
	// the storage is healthy.
	healthInterval = 15 * time.Second
	// unhealthyRetry is how long the queue waits before probing again after the
	// storage turned out to be unavailable.
	unhealthyRetry = 30 * time.Second
	// doneRetention is how long finished jobs stay visible in the queue tab.
	doneRetention = 24 * time.Hour
	// prunePeriod is how often finished jobs are pruned.
	prunePeriod = time.Hour
)

// Health is the last known state of the media root.
type Health struct {
	// Writable reports whether the media root accepted a create/move/delete probe.
	Writable bool `json:"writable"`
	// Message carries the probe error, empty while healthy.
	Message string `json:"message"`
	// CheckedAt is when the probe last ran.
	CheckedAt time.Time `json:"checked_at"`
}

// Worker executes queued jobs serially. Serial execution is deliberate: the
// bottleneck is the storage backend, so running jobs in parallel would only
// increase contention.
type Worker struct {
	store *store.Store
	eng   *engine.Engine
	cfg   config.Config
	log   *slog.Logger

	mu     sync.RWMutex
	health Health
	paused bool
	// wake lets an enqueue skip the idle poll delay.
	wake chan struct{}
}

// New creates a queue worker.
func New(st *store.Store, eng *engine.Engine, cfg config.Config, log *slog.Logger) *Worker {
	return &Worker{
		store: st,
		eng:   eng,
		cfg:   cfg,
		log:   log,
		wake:  make(chan struct{}, 1),
	}
}

// Notify wakes the worker so a freshly enqueued job starts without waiting for
// the next poll tick. It never blocks.
func (w *Worker) Notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Health returns the last known media-root state and whether the queue is
// currently paused because of it.
func (w *Worker) Health() (Health, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.health, w.paused
}

// Run processes the queue until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	if n, err := w.store.ResetRunningJobs(ctx); err != nil {
		w.log.Error("reset running jobs", "err", err)
	} else if n > 0 {
		w.log.Info("requeued jobs interrupted by restart", "jobs", n)
	}
	w.probeHealth(ctx)

	prune := time.NewTicker(prunePeriod)
	defer prune.Stop()
	w.prune(ctx)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-prune.C:
			w.prune(ctx)
		default:
		}

		worked, wait := w.step(ctx)
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.wake:
		case <-time.After(wait):
		}
	}
}

// step claims and runs at most one job. It reports whether work was done and
// how long to wait before looking again.
func (w *Worker) step(ctx context.Context) (bool, time.Duration) {
	if due := w.refreshHealthIfDue(ctx); !due {
		return false, unhealthyRetry
	}
	job, err := w.store.ClaimNextJob(ctx)
	if err != nil {
		w.log.Error("claim job", "err", err)
		return false, idlePoll
	}
	if job == nil {
		return false, idlePoll
	}
	w.execute(ctx, job)
	return true, 0
}

// refreshHealthIfDue re-probes the media root when the last result is stale and
// reports whether the queue may run jobs right now.
func (w *Worker) refreshHealthIfDue(ctx context.Context) bool {
	w.mu.RLock()
	last := w.health.CheckedAt
	ok := w.health.Writable
	w.mu.RUnlock()

	interval := healthInterval
	if !ok {
		interval = unhealthyRetry
	}
	if time.Since(last) >= interval {
		ok = w.probeHealth(ctx)
	}
	return ok
}

// probeHealth runs the writability probe and publishes the result. It blocks on
// the storage, which is exactly why it lives here and not in an HTTP handler.
func (w *Worker) probeHealth(ctx context.Context) bool {
	err := mover.CheckWritable(ctx, w.cfg.MediaRoot)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	w.mu.Lock()
	was := w.health.Writable
	first := w.health.CheckedAt.IsZero()
	w.health = Health{Writable: err == nil, Message: msg, CheckedAt: time.Now()}
	w.paused = err != nil
	w.mu.Unlock()

	if err != nil && (was || first) {
		w.log.Warn("queue paused: media root not writable", "err", err)
	}
	if err == nil && !was && !first {
		w.log.Info("queue resumed: media root writable again")
	}
	return err == nil
}

func (w *Worker) prune(ctx context.Context) {
	if err := w.store.PruneDoneJobs(ctx, doneRetention); err != nil {
		w.log.Warn("prune finished jobs", "err", err)
	}
}

// execute runs a single job and records the outcome.
func (w *Worker) execute(ctx context.Context, job *store.Job) {
	w.log.Info("job started", "id", job.ID, "kind", job.Kind, "item", job.ItemID, "attempt", job.Attempts+1)
	err := w.dispatch(ctx, job)
	switch {
	case err == nil:
		if cerr := w.store.CompleteJob(ctx, job.ID); cerr != nil {
			w.log.Error("complete job", "id", job.ID, "err", cerr)
		}
		w.log.Info("job finished", "id", job.ID, "kind", job.Kind, "item", job.ItemID)

	case isNoOp(err):
		// The desired state is already reached (e.g. the user double-clicked and
		// the first job did the work). That is a success, not a failure.
		if cerr := w.store.CompleteJob(ctx, job.ID); cerr != nil {
			w.log.Error("complete job", "id", job.ID, "err", cerr)
		}
		w.log.Debug("job had nothing to do", "id", job.ID, "kind", job.Kind, "item", job.ItemID)

	case errors.Is(err, context.Canceled):
		// Shutdown: hand the job back untouched so it resumes on the next start.
		if rerr := w.store.RescheduleJob(ctx, job.ID, time.Now(), "", false); rerr != nil {
			w.log.Error("requeue job on shutdown", "id", job.ID, "err", rerr)
		}

	case isPermanent(err):
		// A "permanent" error while the storage is unreachable is almost always
		// environmental (the share vanished mid-job), so re-probe before giving
		// up on work the user explicitly asked for.
		if !w.probeHealth(ctx) {
			if rerr := w.store.RescheduleJob(ctx, job.ID, time.Now().Add(unhealthyRetry), err.Error(), false); rerr != nil {
				w.log.Error("requeue job while storage is down", "id", job.ID, "err", rerr)
			}
			w.log.Warn("job deferred: storage unavailable", "id", job.ID, "kind", job.Kind, "item", job.ItemID, "err", err)
			return
		}
		if ferr := w.store.FailJob(ctx, job.ID, err.Error()); ferr != nil {
			w.log.Error("fail job", "id", job.ID, "err", ferr)
		}
		w.log.Warn("job failed permanently", "id", job.ID, "kind", job.Kind, "item", job.ItemID, "err", err)

	default:
		delay := Backoff(job.Attempts + 1)
		if rerr := w.store.RescheduleJob(ctx, job.ID, time.Now().Add(delay), err.Error(), true); rerr != nil {
			w.log.Error("reschedule job", "id", job.ID, "err", rerr)
		}
		w.log.Warn("job deferred after transient error", "id", job.ID, "kind", job.Kind,
			"item", job.ItemID, "attempt", job.Attempts+1, "retry_in", delay.String(), "err", err)
	}
}

// dispatch maps a job onto the matching engine call. A background context is
// used for the item lookup so a job is never dropped just because the HTTP
// request that created it is long gone.
func (w *Worker) dispatch(ctx context.Context, job *store.Job) error {
	switch job.Kind {
	case store.JobApplyPlan:
		return w.eng.ApplyPlan(ctx, job.ItemID)
	case store.JobFileAction:
		return w.eng.ApplyFileAction(ctx, job.ItemID, job.Payload.RelPath, job.Payload.Action)
	case store.JobCreateFolder:
		if job.Payload.LibraryID > 0 {
			return w.eng.CreateNamedTargetFolder(ctx, job.ItemID, job.Payload.LibraryID, job.Payload.Folder)
		}
		return w.eng.CreateTargetFolder(ctx, job.ItemID)
	case store.JobReclassify:
		return w.eng.ReclassifyItem(ctx, job.ItemID)
	default:
		return errUnknownKind
	}
}

var errUnknownKind = errors.New("unknown job kind")
