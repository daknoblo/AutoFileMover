package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func jobStore(t *testing.T) (*Store, int64) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	item := &Item{SourcePath: "/data/dl/Movie", Name: "Movie", Status: StatusPendingReview}
	if err := st.UpsertItem(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	return st, item.ID
}

func TestEnqueueDeduplicatesOpenJobs(t *testing.T) {
	st, itemID := jobStore(t)
	ctx := t.Context()

	if _, err := st.EnqueueJob(ctx, itemID, JobApplyPlan, JobPayload{}); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	if _, err := st.EnqueueJob(ctx, itemID, JobApplyPlan, JobPayload{}); !errors.Is(err, ErrJobExists) {
		t.Fatalf("duplicate enqueue = %v, want ErrJobExists", err)
	}
	// A different payload is a different job.
	if _, err := st.EnqueueJob(ctx, itemID, JobFileAction, JobPayload{RelPath: "a.mkv", Action: "move"}); err != nil {
		t.Fatalf("distinct payload: %v", err)
	}
	counts, err := st.CountJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Pending != 2 {
		t.Fatalf("pending = %d, want 2", counts.Pending)
	}
}

func TestClaimNextJobRespectsRunAfter(t *testing.T) {
	st, itemID := jobStore(t)
	ctx := t.Context()

	job, err := st.EnqueueJob(ctx, itemID, JobApplyPlan, JobPayload{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RescheduleJob(ctx, job.ID, time.Now().Add(time.Hour), "busy", true); err != nil {
		t.Fatal(err)
	}
	got, err := st.ClaimNextJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("a job scheduled into the future must not be claimed")
	}

	if err := st.RescheduleJob(ctx, job.ID, time.Now().Add(-time.Second), "busy", false); err != nil {
		t.Fatal(err)
	}
	got, err = st.ClaimNextJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != job.ID {
		t.Fatalf("claim = %v, want job %d", got, job.ID)
	}
	if got.Status != JobRunning {
		t.Fatalf("claimed status = %q, want %q", got.Status, JobRunning)
	}
	if got.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (only the counted reschedule)", got.Attempts)
	}
	// Nothing else is due.
	if next, err := st.ClaimNextJob(ctx); err != nil || next != nil {
		t.Fatalf("second claim = %v, %v; want nil, nil", next, err)
	}
}

func TestResetRunningJobsAfterRestart(t *testing.T) {
	st, itemID := jobStore(t)
	ctx := t.Context()

	if _, err := st.EnqueueJob(ctx, itemID, JobApplyPlan, JobPayload{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimNextJob(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := st.ResetRunningJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reset %d jobs, want 1", n)
	}
	got, err := st.ClaimNextJob(ctx)
	if err != nil || got == nil {
		t.Fatalf("job must be claimable again: %v, %v", got, err)
	}
}

func TestJobPayloadRoundTrip(t *testing.T) {
	st, itemID := jobStore(t)
	ctx := t.Context()

	want := JobPayload{RelPath: "Season 1/ep1.mkv", Action: "move", LibraryID: 7, Folder: "Show"}
	if _, err := st.EnqueueJob(ctx, itemID, JobFileAction, want); err != nil {
		t.Fatal(err)
	}
	got, err := st.ClaimNextJob(ctx)
	if err != nil || got == nil {
		t.Fatalf("claim: %v, %v", got, err)
	}
	if got.Payload != want {
		t.Fatalf("payload = %+v, want %+v", got.Payload, want)
	}
}

func TestFailRetryAndDeleteJob(t *testing.T) {
	st, itemID := jobStore(t)
	ctx := t.Context()

	job, err := st.EnqueueJob(ctx, itemID, JobApplyPlan, JobPayload{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FailJob(ctx, job.ID, "no target"); err != nil {
		t.Fatal(err)
	}
	counts, err := st.CountJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Failed != 1 {
		t.Fatalf("failed = %d, want 1", counts.Failed)
	}
	// A failed job frees the unique index, so the same action can be queued again.
	if _, err := st.EnqueueJob(ctx, itemID, JobApplyPlan, JobPayload{}); err != nil {
		t.Fatalf("re-enqueue after failure: %v", err)
	}

	if err := st.RetryJob(ctx, job.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	// The re-enqueued job above already covers the work, so the failed row is
	// dropped rather than resurrected into a unique-index violation.
	if _, err := st.GetItem(ctx, itemID); err != nil {
		t.Fatal(err)
	}
	counts, err = st.CountJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Failed != 0 || counts.Pending != 1 {
		t.Fatalf("after retry: pending=%d failed=%d, want 1/0", counts.Pending, counts.Failed)
	}
	if err := st.DeleteJob(ctx, job.ID); err == nil {
		t.Fatal("deleting an already-removed job must fail")
	}
}

func TestOpenJobsByItemPrefersRunning(t *testing.T) {
	st, itemID := jobStore(t)
	ctx := t.Context()

	if _, err := st.EnqueueJob(ctx, itemID, JobReclassify, JobPayload{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueJob(ctx, itemID, JobApplyPlan, JobPayload{}); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimNextJob(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v, %v", claimed, err)
	}
	open, err := st.OpenJobsByItem(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := open[itemID]; got.Status != JobRunning {
		t.Fatalf("badge job status = %q, want %q", got.Status, JobRunning)
	}
}

func TestPruneDoneJobs(t *testing.T) {
	st, itemID := jobStore(t)
	ctx := t.Context()

	job, err := st.EnqueueJob(ctx, itemID, JobApplyPlan, JobPayload{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.PruneDoneJobs(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	jobs, err := st.ListJobs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("recent finished job must be kept, got %d", len(jobs))
	}
	if err := st.PruneDoneJobs(ctx, 0); err != nil {
		t.Fatal(err)
	}
	jobs, err = st.ListJobs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expired finished job must be pruned, got %d", len(jobs))
	}
}
