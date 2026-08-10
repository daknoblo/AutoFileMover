// Package demo seeds a throw-away AutoFileMover instance with realistic sample
// data: a media tree on disk plus sources, libraries, a review queue and a
// history in the database. It backs the afm-demo command, which serves the real
// web UI so the documentation screenshots always match the shipped product.
//
// All titles are fictional and every file is an empty placeholder — the sizes
// shown in the UI come from the seeded records, not from disk.
package demo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/daknoblo/AutoFileMover/internal/mediainfo"
	"github.com/daknoblo/AutoFileMover/internal/store"
)

const (
	kb int64 = 1 << 10
	mb int64 = 1 << 20
	gb int64 = 1 << 30
)

// Folder names below the media root.
const (
	downloadsDir    = "Downloads"
	moviesDir       = "Movies"
	seriesDir       = "Series"
	documentaryDir  = "Documentaries"
	placeholderText = "AutoFileMover demo placeholder\n"
)

// Release folder names of the downloads used by the review queue.
const (
	relNebula = "Nebula.Drift.2024.2160p.WEB-DL.DV.H265-DEMO"
	relHarbor = "Harbor.Lights.S02E04.1080p.WEB.H264-DEMO"
	relSilent = "Silent.Ridge.2021.1080p.BluRay.x264-DEMO"
	relOcean  = "Deep.Ocean.Secrets.2023.1080p.WEB.H264-DEMO"
	relEmpty  = "Old.Release.Leftovers"
)

// Setup creates the demo media tree below root and fills the store with the
// matching configuration, review queue and history.
func Setup(ctx context.Context, st *store.Store, root string) error {
	if err := createTree(root); err != nil {
		return err
	}
	libs, err := seedConfig(ctx, st, root)
	if err != nil {
		return err
	}
	return seedItems(ctx, st, root, libs)
}

// createTree lays out the download folders and the target libraries on disk so
// the folder browser and the library folder pickers show real content.
func createTree(root string) error {
	dirs := []string{
		filepath.Join(root, downloadsDir, relEmpty),
		filepath.Join(root, moviesDir),
		filepath.Join(root, seriesDir, "Copper Canyon"),
		filepath.Join(root, seriesDir, "Harbor Lights"),
		filepath.Join(root, seriesDir, "Northern Trail"),
		filepath.Join(root, documentaryDir, "Deep Ocean"),
		filepath.Join(root, documentaryDir, "Wild Rivers"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create demo dir: %w", err)
		}
	}

	files := []string{
		filepath.Join(root, downloadsDir, relNebula, relNebula+".mkv"),
		filepath.Join(root, downloadsDir, relNebula, "Nebula.Drift.2024.German.srt"),
		filepath.Join(root, downloadsDir, relNebula, "nebula-drift-sample.mkv"),
		filepath.Join(root, downloadsDir, relNebula, "nebula.drift.nfo"),
		filepath.Join(root, downloadsDir, relNebula, "proof.jpg"),

		filepath.Join(root, downloadsDir, relHarbor, relHarbor+".mkv"),
		filepath.Join(root, downloadsDir, relHarbor, "Harbor.Lights.S02E04.srt"),
		filepath.Join(root, downloadsDir, relHarbor, "harbor.lights.s02e04.nfo"),

		filepath.Join(root, downloadsDir, relSilent, relSilent+".mkv"),
		filepath.Join(root, downloadsDir, relSilent, "silent.ridge.nfo"),

		filepath.Join(root, downloadsDir, relOcean, relOcean+".mkv"),
		filepath.Join(root, downloadsDir, relOcean, "readme.txt"),

		filepath.Join(root, moviesDir, "Iron.Harvest.2022.1080p.WEB.H264-DEMO.mkv"),
		filepath.Join(root, moviesDir, existingSilentRidge),
		filepath.Join(root, seriesDir, "Copper Canyon", "Copper.Canyon.S01E07.1080p.WEB.H264-DEMO.mkv"),
		filepath.Join(root, seriesDir, "Harbor Lights", "Harbor.Lights.S02E03.1080p.WEB.H264-DEMO.mkv"),
		filepath.Join(root, documentaryDir, "Wild Rivers", "Wild.Rivers.2020.2160p.WEB.H265-DEMO.mkv"),
	}
	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			return fmt.Errorf("create demo dir: %w", err)
		}
		if err := os.WriteFile(f, []byte(placeholderText), 0o644); err != nil {
			return fmt.Errorf("create demo file: %w", err)
		}
	}
	return nil
}

// libs holds the seeded target libraries by kind.
type libs struct {
	movies      store.Library
	series      store.Library
	documentary store.Library
}

// seedConfig stores the source folder, the target libraries, their AI context
// notes and the application settings.
func seedConfig(ctx context.Context, st *store.Store, root string) (libs, error) {
	var l libs
	if _, err := st.AddSource(ctx, filepath.Join(root, downloadsDir)); err != nil {
		return l, err
	}

	var err error
	if l.movies, err = st.AddLibrary(ctx, "Movies", store.KindMovie, filepath.Join(root, moviesDir)); err != nil {
		return l, err
	}
	if l.series, err = st.AddLibrary(ctx, "Series", store.KindSeries, filepath.Join(root, seriesDir)); err != nil {
		return l, err
	}
	if l.documentary, err = st.AddLibrary(ctx, "Documentaries", store.KindDocumentary, filepath.Join(root, documentaryDir)); err != nil {
		return l, err
	}

	notes := map[string]string{
		l.movies.Path:      "Feature films, one file per movie directly in the library root.",
		l.series.Path:      "TV series, one folder per show, episodes named SxxExx.",
		l.documentary.Path: "Documentaries and nature/science shows, one folder per title.",
	}
	for path, desc := range notes {
		if err := st.SetFolderNote(ctx, path, desc); err != nil {
			return l, err
		}
	}

	settings := store.AppSettings{
		AIBaseURL:      "https://demo-endpoint.openai.azure.com",
		AIAPIKey:       "demo-key-not-a-real-secret",
		AIModel:        "gpt-4o-mini",
		AIAPIVersion:   "2024-06-01",
		Threshold:      0.9,
		AutoMove:       true,
		IgnorePatterns: store.DefaultIgnorePatterns,
		AIContext:      store.DefaultAIContext,
	}
	return l, st.SaveAppSettings(ctx, settings)
}

// existingSilentRidge is the file already present in the movie library that
// collides with the incoming Silent Ridge release.
const existingSilentRidge = "Silent.Ridge.2021.720p.WEB.x264-OLD.mkv"

// seedItems fills the review queue and the history.
func seedItems(ctx context.Context, st *store.Store, root string, l libs) error {
	downloads := filepath.Join(root, downloadsDir)
	harborFolder := filepath.Join(l.series.Path, "Harbor Lights")
	silentIncoming := relSilent + ".mkv"

	items := []store.Item{
		{
			SourcePath:      filepath.Join(downloads, relNebula),
			Name:            relNebula,
			DetectedType:    "movie",
			TargetLibraryID: &l.movies.ID,
			TargetPath:      l.movies.Path,
			Probability:     0.72,
			Status:          store.StatusPendingReview,
			Reasoning:       "Feature film in Dolby Vision. Sample clip, NFO and proof image are release junk. Confidence below the threshold because the release year is ambiguous.",
			Files: []store.File{
				movingFile(relNebula+".mkv", 18*gb+400*mb, 0.97, "Main feature, largest video file.", filepath.Join(l.movies.Path, relNebula+".mkv")),
				movingFile("Nebula.Drift.2024.German.srt", 74*kb, 0.93, "Matching subtitle track.", filepath.Join(l.movies.Path, "Nebula.Drift.2024.German.srt")),
				junkFile("nebula-drift-sample.mkv", 42*mb, 0.99, "Sample clip."),
				junkFile("nebula.drift.nfo", 3*kb, 0.98, "Release info file."),
				junkFile("proof.jpg", 512*kb, 0.95, "Proof screenshot."),
			},
		},
		{
			SourcePath:      filepath.Join(downloads, relHarbor),
			Name:            relHarbor,
			DetectedType:    "series",
			TargetLibraryID: &l.series.ID,
			TargetPath:      harborFolder,
			Probability:     0.88,
			Status:          store.StatusPendingReview,
			Reasoning:       "Episode S02E04 of an existing show; the matching series folder already exists.",
			Files: []store.File{
				movingFile(relHarbor+".mkv", 2*gb+700*mb, 0.96, "Episode video.", filepath.Join(harborFolder, relHarbor+".mkv")),
				movingFile("Harbor.Lights.S02E04.srt", 61*kb, 0.9, "Matching subtitle track.", filepath.Join(harborFolder, "Harbor.Lights.S02E04.srt")),
				junkFile("harbor.lights.s02e04.nfo", 2*kb, 0.97, "Release info file."),
			},
		},
		{
			SourcePath:      filepath.Join(downloads, relSilent),
			Name:            relSilent,
			DetectedType:    "movie",
			TargetLibraryID: &l.movies.ID,
			TargetPath:      l.movies.Path,
			Probability:     0.94,
			Status:          store.StatusPendingReview,
			Reasoning:       "Feature film. Held back for review: a copy of this movie already exists in the library.",
			Files: []store.File{
				conflictFile(
					movingFile(silentIncoming, 9*gb+100*mb, 0.98, "Main feature.", filepath.Join(l.movies.Path, silentIncoming)),
					silentIncoming, existingSilentRidge, filepath.Join(l.movies.Path, existingSilentRidge), 2*gb+100*mb),
				junkFile("silent.ridge.nfo", 3*kb, 0.96, "Release info file."),
			},
		},
		{
			SourcePath:   filepath.Join(downloads, relOcean),
			Name:         relOcean,
			Probability:  0,
			Status:       store.StatusError,
			ErrorMessage: `classify: post "https://demo-endpoint.openai.azure.com/openai/deployments/gpt-4o-mini/chat/completions": dial tcp: connect: connection refused`,
			Files: []store.File{
				{RelPath: relOcean + ".mkv", Size: 4*gb + 200*mb, Ext: ".mkv"},
				{RelPath: "readme.txt", Size: 1 * kb, Ext: ".txt"},
			},
		},
		{
			SourcePath:   filepath.Join(downloads, relEmpty),
			Name:         relEmpty,
			DetectedType: "empty",
			Status:       store.StatusPendingReview,
			Reasoning:    "empty folder",
			Files:        []store.File{{RelPath: "", Action: store.FileActionDelete, Reason: "empty folder"}},
		},

		// History: already processed items, newest last so the list reads
		// top-down from the most recent move.
		{
			SourcePath:   filepath.Join(downloads, "Random.Junk.Archive"),
			Name:         "Random.Junk.Archive",
			DetectedType: "unknown",
			Probability:  0.31,
			Status:       store.StatusRejected,
			Reasoning:    "No media content recognised; rejected during review.",
			Files:        []store.File{{RelPath: "archive.rar", Size: 120 * mb, Ext: ".rar", Action: store.FileActionKeep, Probability: 0.31, Reason: "Unknown archive."}},
		},
		{
			SourcePath:      filepath.Join(downloads, "Wild.Rivers.2020.2160p.WEB.H265-DEMO"),
			Name:            "Wild.Rivers.2020.2160p.WEB.H265-DEMO",
			DetectedType:    "documentary",
			TargetLibraryID: &l.documentary.ID,
			TargetPath:      filepath.Join(l.documentary.Path, "Wild Rivers"),
			Probability:     0.82,
			Status:          store.StatusConfirmed,
			Reasoning:       "Nature documentary; target folder confirmed manually during review.",
			Files: []store.File{
				doneFile(movingFile("Wild.Rivers.2020.2160p.WEB.H265-DEMO.mkv", 21*gb, 0.89, "Main feature.",
					filepath.Join(l.documentary.Path, "Wild Rivers", "Wild.Rivers.2020.2160p.WEB.H265-DEMO.mkv"))),
				doneFile(junkFile("wild.rivers.nfo", 2*kb, 0.94, "Release info file.")),
			},
		},
		{
			SourcePath:      filepath.Join(downloads, "Iron.Harvest.2022.1080p.WEB.H264-DEMO"),
			Name:            "Iron.Harvest.2022.1080p.WEB.H264-DEMO",
			DetectedType:    "movie",
			TargetLibraryID: &l.movies.ID,
			TargetPath:      l.movies.Path,
			Probability:     0.98,
			Status:          store.StatusAutoMoved,
			Reasoning:       "Unambiguous feature film above the confidence threshold; moved automatically.",
			Files: []store.File{
				doneFile(movingFile("Iron.Harvest.2022.1080p.WEB.H264-DEMO.mkv", 8*gb+300*mb, 0.99, "Main feature.",
					filepath.Join(l.movies.Path, "Iron.Harvest.2022.1080p.WEB.H264-DEMO.mkv"))),
				doneFile(junkFile("sample.mkv", 38*mb, 0.99, "Sample clip.")),
			},
		},
		{
			SourcePath:      filepath.Join(downloads, "Copper.Canyon.S01E07.1080p.WEB.H264-DEMO"),
			Name:            "Copper.Canyon.S01E07.1080p.WEB.H264-DEMO",
			DetectedType:    "series",
			TargetLibraryID: &l.series.ID,
			TargetPath:      filepath.Join(l.series.Path, "Copper Canyon"),
			Probability:     0.96,
			Status:          store.StatusAutoMoved,
			Reasoning:       "Episode S01E07 matched the existing show folder; moved automatically.",
			Files: []store.File{
				doneFile(movingFile("Copper.Canyon.S01E07.1080p.WEB.H264-DEMO.mkv", 3*gb+100*mb, 0.97, "Episode video.",
					filepath.Join(l.series.Path, "Copper Canyon", "Copper.Canyon.S01E07.1080p.WEB.H264-DEMO.mkv"))),
				doneFile(junkFile("copper.canyon.s01e07.nfo", 2*kb, 0.95, "Release info file.")),
			},
		},
	}

	for i := range items {
		if err := st.UpsertItem(ctx, &items[i]); err != nil {
			return fmt.Errorf("seed item %q: %w", items[i].Name, err)
		}
	}
	return nil
}

// movingFile builds a file the AI wants to move to dest.
func movingFile(rel string, size int64, prob float64, reason, dest string) store.File {
	return store.File{
		RelPath:     rel,
		Size:        size,
		Ext:         filepath.Ext(rel),
		Action:      store.FileActionMove,
		Probability: prob,
		Reason:      reason,
		TargetPath:  dest,
	}
}

// junkFile builds a file the AI wants to delete.
func junkFile(rel string, size int64, prob float64, reason string) store.File {
	return store.File{
		RelPath:     rel,
		Size:        size,
		Ext:         filepath.Ext(rel),
		Action:      store.FileActionDelete,
		Probability: prob,
		Reason:      reason,
	}
}

// doneFile marks a planned action as already carried out (history entries).
func doneFile(f store.File) store.File {
	f.Done = true
	return f
}

// conflictFile attaches a collision with an existing file in the target folder.
func conflictFile(f store.File, incomingName, existingName, existingPath string, existingSize int64) store.File {
	f.Conflict = &store.FileConflict{
		ExistingName:    existingName,
		ExistingPath:    existingPath,
		ExistingSize:    existingSize,
		ExistingQuality: mediainfo.Parse(existingName).Summary(),
		IncomingQuality: mediainfo.Parse(incomingName).Summary(),
	}
	return f
}

// LogLines returns pre-rendered log records for the Logs tab. They carry fixed
// timestamps so the documentation screenshots stay byte-identical between runs.
func LogLines() []string {
	return []string{
		`{"time":"2026-05-04T09:12:41.104Z","level":"INFO","msg":"starting autofilemover","version":"demo (demo, built 2026-05-04T09:00:00Z)"}`,
		`{"time":"2026-05-04T09:12:41.118Z","level":"INFO","msg":"starting http server","addr":":8080","media_root":"/dataroot"}`,
		`{"time":"2026-05-04T09:12:41.121Z","level":"INFO","msg":"watching source","path":"/dataroot/Downloads"}`,
		`{"time":"2026-05-04T09:13:02.550Z","level":"INFO","msg":"scan started","sources":1}`,
		`{"time":"2026-05-04T09:13:02.981Z","level":"INFO","msg":"classified item","name":"Iron.Harvest.2022.1080p.WEB.H264-DEMO","type":"movie","confidence":0.98,"move":1,"delete":1,"keep":0}`,
		`{"time":"2026-05-04T09:13:03.014Z","level":"INFO","msg":"auto move","name":"Iron.Harvest.2022.1080p.WEB.H264-DEMO","target":"/dataroot/Movies"}`,
		`{"time":"2026-05-04T09:13:07.442Z","level":"INFO","msg":"classified item","name":"Copper.Canyon.S01E07.1080p.WEB.H264-DEMO","type":"series","confidence":0.96,"move":1,"delete":1,"keep":0}`,
		`{"time":"2026-05-04T09:13:07.470Z","level":"INFO","msg":"auto move","name":"Copper.Canyon.S01E07.1080p.WEB.H264-DEMO","target":"/dataroot/Series/Copper Canyon"}`,
		`{"time":"2026-05-04T09:13:11.903Z","level":"INFO","msg":"classified item","name":"Nebula.Drift.2024.2160p.WEB-DL.DV.H265-DEMO","type":"movie","confidence":0.72,"move":2,"delete":3,"keep":0}`,
		`{"time":"2026-05-04T09:13:11.905Z","level":"WARN","msg":"confidence below threshold, queued for review","name":"Nebula.Drift.2024.2160p.WEB-DL.DV.H265-DEMO","confidence":0.72,"threshold":0.9}`,
		`{"time":"2026-05-04T09:13:15.336Z","level":"INFO","msg":"classified item","name":"Silent.Ridge.2021.1080p.BluRay.x264-DEMO","type":"movie","confidence":0.94,"move":1,"delete":1,"keep":0}`,
		`{"time":"2026-05-04T09:13:15.338Z","level":"WARN","msg":"target collision, queued for review","name":"Silent.Ridge.2021.1080p.BluRay.x264-DEMO","existing":"Silent.Ridge.2021.720p.WEB.x264-OLD.mkv"}`,
		`{"time":"2026-05-04T09:13:19.771Z","level":"ERROR","msg":"classify failed","name":"Deep.Ocean.Secrets.2023.1080p.WEB.H264-DEMO","err":"dial tcp: connect: connection refused"}`,
		`{"time":"2026-05-04T09:13:19.802Z","level":"INFO","msg":"empty folder detected","name":"Old.Release.Leftovers"}`,
		`{"time":"2026-05-04T09:13:19.815Z","level":"INFO","msg":"scan finished","items":5,"moved":2,"review":3,"duration":"17.2s"}`,
	}
}
