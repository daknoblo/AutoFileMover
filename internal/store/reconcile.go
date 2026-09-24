package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// PruneOrphanJobs repairs jobs left by older versions when deleting an item.
func (s *Store) PruneOrphanJobs(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE status != ?
		AND NOT EXISTS (SELECT 1 FROM items WHERE items.id = jobs.item_id)`, JobRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClearQueue removes finished, failed and orphaned jobs, but never running
// work or pending jobs attached to an existing item. It does not touch files.
func (s *Store) ClearQueue(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE status != ?
		AND (status IN (?, ?) OR NOT EXISTS (SELECT 1 FROM items WHERE items.id = jobs.item_id))`,
		JobRunning, JobDone, JobFailed)
	if err != nil {
		return 0, fmt.Errorf("clear queue: %w", err)
	}
	return res.RowsAffected()
}

// RemoveObsoleteFileJobs retains unclaimed per-file work only for unchanged
// files. Whole-item plans and the completed-job history stay intact.
func (s *Store) RemoveObsoleteFileJobs(ctx context.Context, id int64, files []File) error {
	raw, err := json.Marshal(files)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM jobs
		WHERE item_id = ? AND kind = ? AND status IN (?, ?)
		AND NOT EXISTS (SELECT 1 FROM json_each(?)
			WHERE json_extract(value, '$.rel_path') = COALESCE(json_extract(jobs.payload_json, '$.rel_path'), ''))`,
		id, JobFileAction, JobPending, JobFailed, string(raw))
	return err
}

// IsHistory reports whether the item is no longer awaiting processing.
func (it *Item) IsHistory() bool {
	switch it.Status {
	case StatusAutoMoved, StatusConfirmed, StatusRejected, StatusSkipped:
		return true
	default:
		return false
	}
}

// RemoveMissingItem removes obsolete work after the caller verified that the
// source is absent. History is retained; a claimed job protects its item.
func (s *Store) RemoveMissingItem(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	var running bool
	if err := tx.QueryRowContext(ctx, `SELECT status,
		EXISTS(SELECT 1 FROM jobs WHERE item_id = items.id AND status = ?)
		FROM items WHERE id = ?`, JobRunning, id).Scan(&status, &running); err != nil {
		return err
	}
	if running {
		return nil
	}
	item := Item{Status: status}
	if item.IsHistory() {
		_, err = tx.ExecContext(ctx, `DELETE FROM jobs WHERE item_id = ? AND status != ?`, id, JobDone)
	} else {
		if _, err = tx.ExecContext(ctx, `DELETE FROM jobs WHERE item_id = ?`, id); err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, id)
		}
	}
	if err != nil {
		return fmt.Errorf("remove missing item: %w", err)
	}
	return tx.Commit()
}

// ClearHistory removes terminal records and their jobs, never files. Items
// with pending or running jobs are excluded in the same transaction.
func (s *Store) ClearHistory(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	const eligible = `SELECT id FROM items WHERE status IN (?, ?, ?, ?)
		AND NOT EXISTS (SELECT 1 FROM jobs WHERE item_id = items.id AND status IN (?, ?))`
	args := []any{StatusAutoMoved, StatusConfirmed, StatusRejected, StatusSkipped, JobPending, JobRunning}
	if _, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE item_id IN (`+eligible+`)`, args...); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM items WHERE id IN (`+eligible+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}
