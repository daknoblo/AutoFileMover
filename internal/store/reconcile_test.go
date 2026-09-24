package store

import (
	"fmt"
	"testing"
)

func TestClearHistoryPreservesOpenWork(t *testing.T) {
	st, ctx := testStore(t)
	statuses := []string{StatusConfirmed, StatusAutoMoved, StatusRejected, StatusSkipped,
		StatusPendingReview, StatusError, StatusMoving}
	for i, status := range statuses {
		for _, jobStatus := range []string{JobDone, JobFailed, JobPending, JobRunning} {
			item := &Item{SourcePath: fmt.Sprintf("/source/%d-%s", i, jobStatus), Status: status}
			if err := st.UpsertItem(ctx, item); err != nil {
				t.Fatal(err)
			}
			job, err := st.EnqueueJob(ctx, item.ID, JobApplyPlan, JobPayload{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.db.ExecContext(ctx, `UPDATE jobs SET status = ? WHERE id = ?`, jobStatus, job.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	n, err := st.ClearHistory(ctx)
	if err != nil || n != 8 {
		t.Fatalf("removed = %d, err = %v; want 8", n, err)
	}
	items, err := st.ListItems(ctx, "", 0)
	if err != nil || len(items) != 20 {
		t.Fatalf("remaining items = %d, err = %v", len(items), err)
	}
	jobs, err := st.ListJobs(ctx, 0)
	if err != nil || len(jobs) != 20 {
		t.Fatalf("remaining jobs = %d, err = %v", len(jobs), err)
	}
	if n, err := st.ClearHistory(ctx); err != nil || n != 0 {
		t.Fatalf("second clear = %d, %v", n, err)
	}
}

func TestMissingItemWithClaimedJobIsRetained(t *testing.T) {
	st, id := jobStore(t)
	ctx := t.Context()
	if _, err := st.EnqueueJob(ctx, id, JobApplyPlan, JobPayload{}); err != nil {
		t.Fatal(err)
	}

	job, err := st.ClaimNextJob(ctx)
	if err != nil || job == nil {
		t.Fatalf("claim: %+v, %v", job, err)
	}
	if err := st.RemoveMissingItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetItem(ctx, id); err != nil || got == nil {
		t.Fatalf("claimed item was removed: %v", err)
	}
	if err := st.FailJob(ctx, job.ID, "missing"); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveMissingItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueJob(ctx, id, JobApplyPlan, JobPayload{}); err == nil {
		t.Fatal("must not enqueue work for a removed item")
	}
}

func TestDeleteItemRemovesJobsAndProtectsRunningWork(t *testing.T) {
	st, id := jobStore(t)
	ctx := t.Context()
	if _, err := st.EnqueueJob(ctx, id, JobApplyPlan, JobPayload{}); err != nil {
		t.Fatal(err)
	}
	job, err := st.ClaimNextJob(ctx)
	if err != nil || job == nil {
		t.Fatalf("claim: %+v, %v", job, err)
	}
	if err := st.DeleteItem(ctx, id); err == nil {
		t.Fatal("deleting an item with running work must fail")
	}
	if err := st.CompleteJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if jobs, err := st.ListJobs(ctx, 0); err != nil || len(jobs) != 0 {
		t.Fatalf("orphan jobs: %+v, %v", jobs, err)
	}
}

func TestPruneLegacyOrphanJobs(t *testing.T) {
	st, id := jobStore(t)
	ctx := t.Context()
	if _, err := st.EnqueueJob(ctx, id, JobApplyPlan, JobPayload{}); err != nil {
		t.Fatal(err)
	}
	// Simulate the old DeleteItem implementation, which left jobs behind.
	if _, err := st.db.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if n, err := st.PruneOrphanJobs(ctx); err != nil || n != 1 {
		t.Fatalf("pruned = %d, err = %v", n, err)
	}
}

func TestClearQueuePreservesActiveWork(t *testing.T) {
	st, ctx := testStore(t)
	want := make(map[int64]string)
	for _, orphan := range []bool{false, true} {
		item := &Item{SourcePath: fmt.Sprintf("/source/orphan-%t", orphan), Status: StatusPendingReview}
		if err := st.UpsertItem(ctx, item); err != nil {
			t.Fatal(err)
		}
		for _, status := range []string{JobDone, JobFailed, JobPending, JobRunning} {
			job, err := st.EnqueueJob(ctx, item.ID, JobFileAction, JobPayload{RelPath: status})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.db.ExecContext(ctx, `UPDATE jobs SET status = ? WHERE id = ?`, status, job.ID); err != nil {
				t.Fatal(err)
			}
			if status == JobRunning || (!orphan && status == JobPending) {
				want[job.ID] = status
			}
		}
		if orphan {
			if _, err := st.db.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, item.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if n, err := st.ClearQueue(ctx); err != nil || n != 5 {
		t.Fatalf("removed = %d, err = %v; want 5", n, err)
	}
	jobs, err := st.ListJobs(ctx, 0)
	if err != nil || len(jobs) != len(want) {
		t.Fatalf("remaining jobs = %+v, err = %v", jobs, err)
	}
	for _, job := range jobs {
		if want[job.ID] != job.Status {
			t.Errorf("unexpected remaining job: %+v", job)
		}
	}
	if items, err := st.ListItems(ctx, "", 0); err != nil || len(items) != 1 {
		t.Fatalf("cleanup removed item records: %+v, %v", items, err)
	}
	if n, err := st.ClearQueue(ctx); err != nil || n != 0 {
		t.Fatalf("second cleanup = %d, %v", n, err)
	}
}

func TestClearQueueBeyondListLimit(t *testing.T) {
	st, id := jobStore(t)
	for range 205 {
		job, err := st.EnqueueJob(t.Context(), id, JobApplyPlan, JobPayload{})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.CompleteJob(t.Context(), job.ID); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := st.ClearQueue(t.Context()); err != nil || n != 205 {
		t.Fatalf("removed = %d, err = %v; want 205", n, err)
	}
	if jobs, err := st.ListJobs(t.Context(), 0); err != nil || len(jobs) != 0 {
		t.Fatalf("remaining jobs = %+v, err = %v", jobs, err)
	}
}
