package store

import (
	"context"
	"database/sql"
	"errors"
)

// FolderNote is a free-text description attached to a filesystem folder. The
// description is sent to the AI endpoint as additional context for files that
// relate to this folder.
type FolderNote struct {
	Path        string `json:"path"`
	Description string `json:"description"`
}

// GetFolderNote returns the description for a path, or empty string if none.
func (s *Store) GetFolderNote(ctx context.Context, path string) (string, error) {
	var desc string
	err := s.db.QueryRowContext(ctx, `SELECT description FROM folder_notes WHERE path = ?`, path).Scan(&desc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return desc, nil
}

// SetFolderNote stores (or clears) the description for a path. An empty
// description removes the note.
func (s *Store) SetFolderNote(ctx context.Context, path, description string) error {
	if description == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM folder_notes WHERE path = ?`, path)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO folder_notes(path, description, updated_at) VALUES(?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(path) DO UPDATE SET description = excluded.description, updated_at = CURRENT_TIMESTAMP`,
		path, description)
	return err
}

// ListFolderNotes returns all stored folder descriptions.
func (s *Store) ListFolderNotes(ctx context.Context) ([]FolderNote, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, description FROM folder_notes ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FolderNote
	for rows.Next() {
		var n FolderNote
		if err := rows.Scan(&n.Path, &n.Description); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// FolderNotesByPath returns all folder notes as a path->description map.
func (s *Store) FolderNotesByPath(ctx context.Context) (map[string]string, error) {
	notes, err := s.ListFolderNotes(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(notes))
	for _, n := range notes {
		m[n.Path] = n.Description
	}
	return m, nil
}
