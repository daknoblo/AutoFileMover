// Package store provides a pure-Go SQLite-backed persistence layer for
// settings, source folders, target libraries and detected items.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Item status values.
const (
	StatusPendingReview = "pending_review"
	StatusAutoMoved     = "auto_moved"
	StatusConfirmed     = "confirmed"
	StatusMoving        = "moving"
	StatusError         = "error"
	StatusRejected      = "rejected"
	StatusSkipped       = "skipped"
)

// Library kinds.
const (
	KindMovie       = "movie"
	KindSeries      = "series"
	KindDocumentary = "documentary"
)

// Store wraps the database connection.
type Store struct {
	db *sql.DB
}

// Source is a watched download folder.
type Source struct {
	ID        int64     `json:"id"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
}

// Library is a target media library (e.g. Movies, Series, Documentaries).
type Library struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
}

// File-level action values: the engine plans one of these per file.
const (
	FileActionMove   = "move"
	FileActionDelete = "delete"
	FileActionKeep   = "keep"
)

// File describes a single file inside a detected item and the action planned
// for it by the AI classification.
type File struct {
	RelPath string `json:"rel_path"`
	Size    int64  `json:"size"`
	Ext     string `json:"ext"`
	// Action is the planned action: move, delete or keep. Empty means undecided.
	Action string `json:"action"`
	// Probability is the per-file confidence (0..1).
	Probability float64 `json:"probability"`
	// Reason is the AI's short justification for the action.
	Reason string `json:"reason"`
	// TargetPath is the resolved destination for a "move" file.
	TargetPath string `json:"target_path"`
	// Done reports whether the planned action has already been carried out.
	Done bool `json:"done"`
	// Quality is a display-only summary of release attributes parsed from the file
	// name (e.g. "1080p · H265 · WEB"). It is filled in when items are served to the
	// UI and is not persisted.
	Quality string `json:"quality,omitempty"`
	// Conflict describes an existing file in the target that collides with this
	// move (same name or same episode). Nil when there is no collision or the
	// user has already resolved it.
	Conflict *FileConflict `json:"conflict,omitempty"`
	// Overwrite is set when the user chose to replace the colliding target file;
	// the existing file is then deleted before this file is moved in.
	Overwrite bool `json:"overwrite,omitempty"`
	// OverwritePath is the existing target file to delete when Overwrite is set.
	// It may differ from TargetPath when the collision is a same-episode file
	// under a different release name.
	OverwritePath string `json:"overwrite_path,omitempty"`
}

// FileConflict captures a side-by-side comparison between a file about to be
// moved and an existing file already present in the target folder, so the user
// can decide which one to keep during review.
type FileConflict struct {
	// ExistingName/Path/Size describe the file already in the target folder.
	ExistingName string `json:"existing_name"`
	ExistingPath string `json:"existing_path"`
	ExistingSize int64  `json:"existing_size"`
	// ExistingQuality is the release-attribute summary parsed from the existing
	// file's name (e.g. "1080p · H265 · WEB").
	ExistingQuality string `json:"existing_quality"`
	// IncomingQuality is the same summary for the file about to be moved.
	IncomingQuality string `json:"incoming_quality"`
}

// Item is a detected download entry and its classification/move state.
type Item struct {
	ID              int64     `json:"id"`
	SourcePath      string    `json:"source_path"`
	Name            string    `json:"name"`
	DetectedType    string    `json:"detected_type"`
	TargetLibraryID *int64    `json:"target_library_id"`
	TargetPath      string    `json:"target_path"`
	// SuggestedLibraryID and SuggestedFolder hold the AI's proposed destination
	// when the matching folder does not exist yet, so the UI can offer to create
	// it with one click.
	SuggestedLibraryID *int64 `json:"suggested_library_id"`
	SuggestedFolder    string `json:"suggested_folder"`
	Probability     float64   `json:"probability"`
	Status          string    `json:"status"`
	Reasoning       string    `json:"reasoning"`
	Files           []File    `json:"files"`
	AIRaw           string    `json:"ai_raw"`
	ErrorMessage    string    `json:"error_message"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// IsSingleFile reports whether the item is a single loose file rather than a
// folder (its only file equals the source path's base name).
func (it *Item) IsSingleFile() bool {
	return len(it.Files) == 1 && it.Files[0].RelPath == filepath.Base(it.SourcePath)
}

// Open opens (or creates) the database at path and applies migrations.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite write serialization
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sources (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	path       TEXT NOT NULL UNIQUE,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS libraries (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL,
	kind       TEXT NOT NULL,
	path       TEXT NOT NULL UNIQUE,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS items (
	id                INTEGER PRIMARY KEY AUTOINCREMENT,
	source_path       TEXT NOT NULL UNIQUE,
	name              TEXT NOT NULL,
	detected_type     TEXT NOT NULL DEFAULT '',
	target_library_id INTEGER,
	target_path       TEXT NOT NULL DEFAULT '',
	suggested_library_id INTEGER,
	suggested_folder  TEXT NOT NULL DEFAULT '',
	probability       REAL NOT NULL DEFAULT 0,
	status            TEXT NOT NULL,
	reasoning         TEXT NOT NULL DEFAULT '',
	files_json        TEXT NOT NULL DEFAULT '[]',
	ai_raw            TEXT NOT NULL DEFAULT '',
	error_message     TEXT NOT NULL DEFAULT '',
	created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_items_status ON items(status);
CREATE TABLE IF NOT EXISTS folder_notes (
	path        TEXT PRIMARY KEY,
	description TEXT NOT NULL DEFAULT '',
	updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`
	_, err := s.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Columns added after the initial schema; ignore "duplicate column" on DBs
	// that already have them.
	for _, stmt := range []string{
		`ALTER TABLE items ADD COLUMN suggested_library_id INTEGER`,
		`ALTER TABLE items ADD COLUMN suggested_folder TEXT NOT NULL DEFAULT ''`,
	} {
		if _, aerr := s.db.Exec(stmt); aerr != nil && !strings.Contains(aerr.Error(), "duplicate column name") {
			return fmt.Errorf("migrate alter: %w", aerr)
		}
	}
	return nil
}

// ---- Settings ----

// GetSetting returns the value for key, or fallback if not present.
func (s *Store) GetSetting(ctx context.Context, key, fallback string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return fallback, nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// SetSetting stores a value for key.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// AllSettings returns all settings as a map.
func (s *Store) AllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// ---- Sources ----

// ListSources returns all configured source folders.
func (s *Store) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, path, created_at FROM sources ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		var src Source
		if err := rows.Scan(&src.ID, &src.Path, &src.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// AddSource inserts a new source folder.
func (s *Store) AddSource(ctx context.Context, path string) (Source, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO sources(path) VALUES(?)`, path)
	if err != nil {
		return Source{}, err
	}
	id, _ := res.LastInsertId()
	return Source{ID: id, Path: path, CreatedAt: time.Now()}, nil
}

// DeleteSource removes a source folder by id.
func (s *Store) DeleteSource(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sources WHERE id = ?`, id)
	return err
}

// ---- Libraries ----

// ListLibraries returns all configured target libraries.
func (s *Store) ListLibraries(ctx context.Context) ([]Library, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, kind, path, created_at FROM libraries ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Library
	for rows.Next() {
		var l Library
		if err := rows.Scan(&l.ID, &l.Name, &l.Kind, &l.Path, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetLibrary returns a single library by id.
func (s *Store) GetLibrary(ctx context.Context, id int64) (Library, error) {
	var l Library
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, kind, path, created_at FROM libraries WHERE id = ?`, id).
		Scan(&l.ID, &l.Name, &l.Kind, &l.Path, &l.CreatedAt)
	return l, err
}

// AddLibrary inserts a new target library.
func (s *Store) AddLibrary(ctx context.Context, name, kind, path string) (Library, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO libraries(name, kind, path) VALUES(?, ?, ?)`, name, kind, path)
	if err != nil {
		return Library{}, err
	}
	id, _ := res.LastInsertId()
	return Library{ID: id, Name: name, Kind: kind, Path: path, CreatedAt: time.Now()}, nil
}

// DeleteLibrary removes a library by id.
func (s *Store) DeleteLibrary(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM libraries WHERE id = ?`, id)
	return err
}

// ---- Items ----

// FindItemBySource returns the item for a given source path, if any.
func (s *Store) FindItemBySource(ctx context.Context, sourcePath string) (*Item, error) {
	row := s.db.QueryRowContext(ctx, itemSelect+` WHERE source_path = ?`, sourcePath)
	it, err := scanItem(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return it, nil
}

// GetItem returns an item by id.
func (s *Store) GetItem(ctx context.Context, id int64) (*Item, error) {
	row := s.db.QueryRowContext(ctx, itemSelect+` WHERE id = ?`, id)
	it, err := scanItem(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return it, nil
}

// ListItems returns items, optionally filtered by status, newest first.
func (s *Store) ListItems(ctx context.Context, status string, limit int) ([]Item, error) {
	q := itemSelect
	args := []any{}
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY updated_at DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *it)
	}
	return out, rows.Err()
}

// UpsertItem inserts or updates an item keyed by its source path and returns it
// with a populated ID. Existing classification fields are overwritten.
func (s *Store) UpsertItem(ctx context.Context, it *Item) error {
	filesJSON, err := json.Marshal(it.Files)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO items(source_path, name, detected_type, target_library_id, target_path,
	suggested_library_id, suggested_folder,
	probability, status, reasoning, files_json, ai_raw, error_message, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(source_path) DO UPDATE SET
	name = excluded.name,
	detected_type = excluded.detected_type,
	target_library_id = excluded.target_library_id,
	target_path = excluded.target_path,
	suggested_library_id = excluded.suggested_library_id,
	suggested_folder = excluded.suggested_folder,
	probability = excluded.probability,
	status = excluded.status,
	reasoning = excluded.reasoning,
	files_json = excluded.files_json,
	ai_raw = excluded.ai_raw,
	error_message = excluded.error_message,
	updated_at = CURRENT_TIMESTAMP`,
		it.SourcePath, it.Name, it.DetectedType, it.TargetLibraryID, it.TargetPath,
		it.SuggestedLibraryID, it.SuggestedFolder,
		it.Probability, it.Status, it.Reasoning, string(filesJSON), it.AIRaw, it.ErrorMessage)
	if err != nil {
		return err
	}
	if it.ID == 0 {
		if id, e := res.LastInsertId(); e == nil && id != 0 {
			it.ID = id
		} else {
			// On conflict-update LastInsertId may be 0; re-read the id.
			existing, _ := s.FindItemBySource(ctx, it.SourcePath)
			if existing != nil {
				it.ID = existing.ID
			}
		}
	}
	return nil
}

// UpdateItemStatus updates the status (and optional error message) of an item.
func (s *Store) UpdateItemStatus(ctx context.Context, id int64, status, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE items SET status = ?, error_message = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, errMsg, id)
	return err
}

// UpdateItemTarget sets the resolved target library/path for an item.
func (s *Store) UpdateItemTarget(ctx context.Context, id int64, libraryID *int64, targetPath string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE items SET target_library_id = ?, target_path = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		libraryID, targetPath, id)
	return err
}

// DeleteItem removes an item record (does not touch files on disk).
func (s *Store) DeleteItem(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, id)
	return err
}

const itemSelect = `SELECT id, source_path, name, detected_type, target_library_id, target_path,
	suggested_library_id, suggested_folder,
	probability, status, reasoning, files_json, ai_raw, error_message, created_at, updated_at FROM items`

type scanner interface {
	Scan(dest ...any) error
}

func scanItem(row scanner) (*Item, error) {
	var it Item
	var filesJSON string
	if err := row.Scan(&it.ID, &it.SourcePath, &it.Name, &it.DetectedType, &it.TargetLibraryID,
		&it.TargetPath, &it.SuggestedLibraryID, &it.SuggestedFolder, &it.Probability, &it.Status, &it.Reasoning, &filesJSON, &it.AIRaw,
		&it.ErrorMessage, &it.CreatedAt, &it.UpdatedAt); err != nil {
		return nil, err
	}
	if filesJSON != "" {
		_ = json.Unmarshal([]byte(filesJSON), &it.Files)
	}
	return &it, nil
}
