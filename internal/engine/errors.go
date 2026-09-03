package engine

import "errors"

// Domain errors. They are sentinels so the queue worker can tell a permanent
// failure (retrying will never help) from a transient filesystem problem.
var (
	// ErrItemNotFound is returned when an item id no longer exists.
	ErrItemNotFound = errors.New("item not found")
	// ErrItemBusy is returned when an item is currently being processed and the
	// caller's context expired while waiting for it.
	ErrItemBusy = errors.New("Element wird gerade verarbeitet")
	// ErrLibraryNotFound is returned for an unknown library id.
	ErrLibraryNotFound = errors.New("library not found")
	// ErrFileNotFound is returned when a relative path is not part of the item.
	ErrFileNotFound = errors.New("file not found in item")
	// ErrFileDone is returned when a file has already been moved or deleted.
	ErrFileDone = errors.New("file already processed")
	// ErrInvalidAction is returned for an unknown per-file action.
	ErrInvalidAction = errors.New("invalid action")
	// ErrDryRun is returned while What-If mode blocks filesystem changes.
	ErrDryRun = errors.New("What-If-Modus aktiv: es werden keine Dateien verschoben oder gelöscht")
	// ErrNothingToDo is returned when an item has no pending work.
	ErrNothingToDo = errors.New("nichts auszuführen")
	// ErrNoTarget is returned when a move has no resolved destination.
	ErrNoTarget = errors.New("kein Ziel aufgelöst – bitte zuerst eine Bibliothek/Datei wählen")
	// ErrUnresolvedConflict is returned while a collision is still undecided.
	ErrUnresolvedConflict = errors.New("Konflikt mit vorhandener Datei – bitte zuerst auflösen (Ersetzen oder Vorhandene behalten)")
	// ErrNothingToClassify is returned when an item holds no real files.
	ErrNothingToClassify = errors.New("nichts zu analysieren")
	// ErrAINotConfigured is returned when no AI endpoint is set up.
	ErrAINotConfigured = errors.New("KI-Endpoint nicht konfiguriert")
	// ErrNoSuggestedFolder is returned when no folder was proposed for creation.
	ErrNoSuggestedFolder = errors.New("kein Ordner zum Anlegen vorgeschlagen")
	// ErrInvalidFolderName is returned for a folder name that is empty or would
	// escape the library directory.
	ErrInvalidFolderName = errors.New("ungültiger Ordnername")
	// ErrNoConflict is returned when resolving a collision that does not exist.
	ErrNoConflict = errors.New("kein Konflikt für diese Datei")
	// ErrTargetDirMissing is returned when a planned destination folder is gone
	// by the time the plan runs.
	ErrTargetDirMissing = errors.New("Zielordner existiert nicht")
	// ErrInvalidResolution is returned for an unknown conflict resolution.
	ErrInvalidResolution = errors.New("invalid resolution")
)

// IsPermanent reports whether err can never succeed on a retry, so the queue
// should fail the job instead of scheduling another attempt.
func IsPermanent(err error) bool {
	switch {
	case errors.Is(err, ErrItemNotFound),
		errors.Is(err, ErrLibraryNotFound),
		errors.Is(err, ErrFileNotFound),
		errors.Is(err, ErrFileDone),
		errors.Is(err, ErrInvalidAction),
		errors.Is(err, ErrDryRun),
		errors.Is(err, ErrNothingToDo),
		errors.Is(err, ErrNoTarget),
		errors.Is(err, ErrUnresolvedConflict),
		errors.Is(err, ErrNothingToClassify),
		errors.Is(err, ErrAINotConfigured),
		errors.Is(err, ErrNoSuggestedFolder),
		errors.Is(err, ErrInvalidFolderName),
		errors.Is(err, ErrNoConflict),
		errors.Is(err, ErrTargetDirMissing),
		errors.Is(err, ErrInvalidResolution):
		return true
	}
	return false
}
