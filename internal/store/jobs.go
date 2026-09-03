package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Job status values.
const (
	JobPending = "pending"
	JobRunning = "running"
	JobFailed  = "failed"
	JobDone    = "done"
)

// Job kinds. Each maps to one filesystem-touching engine operation.
const (
	JobApplyPlan       = "apply_plan"
	JobFileAction      = "file_action"
	JobCreateFolder    = "create_folder"
	JobReclassify      = "reclassify"
	JobDetectConflicts = "detect_conflicts"
)

// ErrJobExists is returned by EnqueueJob when an identical job is already
// pending or running for the same item.
var ErrJobExists = errors.New("job already queued")

// Job is one queued unit of work for an item. The queue exists so the UI never
// has to wait for a slow or saturated storage backend: actions are recorded
// here immediately and executed by the background worker.
type Job struct {
	ID        int64      `json:"id"`
	ItemID    int64      `json:"item_id"`
	Kind      string     `json:"kind"`
	Payload   JobPayload `json:"payload"`
	Status    string     `json:"status"`
	Attempts  int        `json:"attempts"`
	LastError string     `json:"last_error"`
	RunAfter  time.Time  `json:"run_after"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`

	// ItemName is filled in by ListJobs for display; it is not stored on the job.
	ItemName string `json:"item_name,omitempty"`
}

// JobPayload carries the kind-specific arguments of a job.
type JobPayload struct {
	RelPath   string `json:"rel_path,omitempty"`
	Action    string `json:"action,omitempty"`
	LibraryID int64  `json:"library_id,omitempty"`
	Folder    string `json:"folder,omitempty"`
}

// JobCounts summarises the queue for the header badge.
type JobCounts struct {
	Pending int `json:"pending"`
	Running int `json:"running"`
	Failed  int `json:"failed"`
}

// EnqueueJob adds a job unless an identical one is already pending or running.
func (s *Store) EnqueueJob(ctx context.Context, itemID int64, kind string, payload JobPayload) (*Job, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode job payload: %w", err)
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO jobs(item_id, kind, payload_json, status, attempts, run_after, created_at, updated_at)
		VALUES(?, ?, ?, ?, 0, ?, ?, ?)`,
		itemID, kind, string(raw), JobPending, now, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrJobExists
		}
		return nil, fmt.Errorf("enqueue job: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("enqueue job id: %w", err)
	}
	return &Job{
		ID: id, ItemID: itemID, Kind: kind, Payload: payload,
		Status: JobPending, RunAfter: now, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// ClaimNextJob atomically marks the oldest due pending job as running and
// returns it, or nil when nothing is due. Safe without an explicit transaction
// because the store limits the pool to a single connection.
func (s *Store) ClaimNextJob(ctx context.Context) (*Job, error) {
	now := time.Now().UTC()
	row := s.db.QueryRowContext(ctx, `
		UPDATE jobs SET status = ?, updated_at = ?
		WHERE id = (
			SELECT id FROM jobs
			WHERE status = ? AND run_after <= ?
			ORDER BY run_after, id LIMIT 1
		)
		RETURNING id, item_id, kind, payload_json, status, attempts, last_error, run_after, created_at, updated_at`,
		JobRunning, now, JobPending, now)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim job: %w", err)
	}
	return job, nil
}

// CompleteJob marks a job as successfully finished.
func (s *Store) CompleteJob(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET status = ?, last_error = '', updated_at = ? WHERE id = ?`,
		JobDone, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("complete job: %w", err)
	}
	return nil
}

// FailJob marks a job as permanently failed with the given reason.
func (s *Store) FailJob(ctx context.Context, id int64, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET status = ?, last_error = ?, attempts = attempts + 1, updated_at = ? WHERE id = ?`,
		JobFailed, reason, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("fail job: %w", err)
	}
	return nil
}

// RescheduleJob puts a job back into the queue after a transient error. When
// countAttempt is false the attempt counter is left alone, which is what the
// health gate uses so an unreachable share does not exhaust the backoff.
func (s *Store) RescheduleJob(ctx context.Context, id int64, runAfter time.Time, reason string, countAttempt bool) error {
	q := `UPDATE jobs SET status = ?, run_after = ?, last_error = ?, updated_at = ? WHERE id = ?`
	if countAttempt {
		q = `UPDATE jobs SET status = ?, run_after = ?, last_error = ?, updated_at = ?, attempts = attempts + 1 WHERE id = ?`
	}
	if _, err := s.db.ExecContext(ctx, q, JobPending, runAfter.UTC(), reason, time.Now().UTC(), id); err != nil {
		return fmt.Errorf("reschedule job: %w", err)
	}
	return nil
}

// RetryJob resets a failed job so the worker picks it up again immediately. If
// an identical job is meanwhile queued, the failed row is dropped instead: the
// work is already represented and the unique index allows only one open job.
func (s *Store) RetryJob(ctx context.Context, id int64) error {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET status = ?, attempts = 0, last_error = '', run_after = ?, updated_at = ? WHERE id = ? AND status = ?`,
		JobPending, now, now, id, JobFailed)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return s.DeleteJob(ctx, id)
		}
		return fmt.Errorf("retry job: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("retry job: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("job not found or not failed")
	}
	return nil
}

// DeleteJob removes a job that has not started yet or has already failed. A
// running job is left alone so the worker is never pulled out from under.
func (s *Store) DeleteJob(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE id = ? AND status != ?`, id, JobRunning)
	if err != nil {
		return fmt.Errorf("delete job: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete job: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("job not found or currently running")
	}
	return nil
}

// ResetRunningJobs returns jobs left in "running" by a crash or restart to the
// pending state. Every job kind is written back to the item after each file, so
// re-running one only picks up the work that is genuinely still outstanding.
func (s *Store) ResetRunningJobs(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET status = ?, updated_at = ? WHERE status = ?`,
		JobPending, time.Now().UTC(), JobRunning)
	if err != nil {
		return 0, fmt.Errorf("reset running jobs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("reset running jobs: %w", err)
	}
	return n, nil
}

// PruneDoneJobs deletes finished jobs older than the given age so the queue tab
// keeps a short history without growing without bound.
func (s *Store) PruneDoneJobs(ctx context.Context, age time.Duration) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE status = ? AND updated_at < ?`,
		JobDone, time.Now().UTC().Add(-age))
	if err != nil {
		return fmt.Errorf("prune jobs: %w", err)
	}
	return nil
}

// ListJobs returns the most recent jobs together with their item name, open
// ones (pending/running/failed) first.
func (s *Store) ListJobs(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT j.id, j.item_id, j.kind, j.payload_json, j.status, j.attempts, j.last_error,
		       j.run_after, j.created_at, j.updated_at, COALESCE(i.name, '')
		FROM jobs j LEFT JOIN items i ON i.id = j.item_id
		ORDER BY (j.status = ?) ASC, j.run_after ASC, j.id ASC
		LIMIT ?`, JobDone, limit)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	jobs := []Job{}
	for rows.Next() {
		var j Job
		var payload string
		if err := rows.Scan(&j.ID, &j.ItemID, &j.Kind, &payload, &j.Status, &j.Attempts,
			&j.LastError, &j.RunAfter, &j.CreatedAt, &j.UpdatedAt, &j.ItemName); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		if err := json.Unmarshal([]byte(payload), &j.Payload); err != nil {
			return nil, fmt.Errorf("decode job payload (job %d): %w", j.ID, err)
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	return jobs, nil
}

// OpenJobsByItem returns the most relevant open job per item id, so the review
// cards can show a queue badge without one query per card.
func (s *Store) OpenJobsByItem(ctx context.Context) (map[int64]Job, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, item_id, kind, payload_json, status, attempts, last_error, run_after, created_at, updated_at
		FROM jobs WHERE status IN (?, ?, ?)
		ORDER BY CASE status WHEN ? THEN 0 WHEN ? THEN 1 ELSE 2 END, id`,
		JobRunning, JobFailed, JobPending, JobRunning, JobFailed)
	if err != nil {
		return nil, fmt.Errorf("open jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[int64]Job{}
	for rows.Next() {
		var j Job
		var payload string
		if err := rows.Scan(&j.ID, &j.ItemID, &j.Kind, &payload, &j.Status, &j.Attempts,
			&j.LastError, &j.RunAfter, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		if err := json.Unmarshal([]byte(payload), &j.Payload); err != nil {
			return nil, fmt.Errorf("decode job payload (job %d): %w", j.ID, err)
		}
		// The ORDER BY puts running before failed before pending, so the first
		// row seen for an item is the one worth showing.
		if _, seen := out[j.ItemID]; !seen {
			out[j.ItemID] = j
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("open jobs: %w", err)
	}
	return out, nil
}

// CountJobs returns the number of pending, running and failed jobs.
func (s *Store) CountJobs(ctx context.Context) (JobCounts, error) {
	var c JobCounts
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(status = ?), 0),
			COALESCE(SUM(status = ?), 0),
			COALESCE(SUM(status = ?), 0)
		FROM jobs`, JobPending, JobRunning, JobFailed).Scan(&c.Pending, &c.Running, &c.Failed)
	if err != nil {
		return c, fmt.Errorf("count jobs: %w", err)
	}
	return c, nil
}

func scanJob(row scanner) (*Job, error) {
	var j Job
	var payload string
	if err := row.Scan(&j.ID, &j.ItemID, &j.Kind, &payload, &j.Status, &j.Attempts,
		&j.LastError, &j.RunAfter, &j.CreatedAt, &j.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(payload), &j.Payload); err != nil {
		return nil, fmt.Errorf("decode job payload (job %d): %w", j.ID, err)
	}
	return &j, nil
}
