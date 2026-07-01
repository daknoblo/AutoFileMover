package store

import (
	"context"
	"strconv"
	"strings"
)

// Setting keys.
const (
	KeyAIBaseURL    = "ai_base_url"
	KeyAIAPIKey     = "ai_api_key"
	KeyAIModel      = "ai_model"
	KeyAIAPIVersion = "ai_api_version"
	KeyThreshold    = "threshold"
	KeyAutoMove     = "auto_move"
	KeyDryRun       = "dry_run"
	KeyIgnore       = "ignore_patterns"
	KeyAIContext    = "ai_context"
)

// DefaultIgnorePatterns are applied when the user has not configured any.
var DefaultIgnorePatterns = []string{"_UNPACK", "sample"}

// DefaultAIContext is the always-sent context prompt describing what the files
// are and how to treat them. Users can override it in the settings.
const DefaultAIContext = "These are downloaded media releases (movies, TV series " +
	"episodes and documentaries) from scene/p2p sources. Each item is a folder or a " +
	"single file. Keep the actual feature: the main (largest) video file plus matching " +
	"subtitles. Discard junk: sample clips (name/path contains 'sample'), .nfo files, " +
	"screenshots/proof images, .txt/.url files and checksums (.sfv/.md5). Sort movies and " +
	"documentaries into their library and series episodes into the matching existing show " +
	"folder."

// AppSettings is the typed view of the user-configurable settings.
type AppSettings struct {
	AIBaseURL    string  `json:"ai_base_url"`
	AIAPIKey     string  `json:"ai_api_key"`
	AIModel      string  `json:"ai_model"`
	AIAPIVersion string  `json:"ai_api_version"`
	Threshold    float64 `json:"threshold"`
	AutoMove     bool    `json:"auto_move"`
	// DryRun is the "what-if" mode: when true no files are moved.
	DryRun bool `json:"dry_run"`
	// IgnorePatterns are case-insensitive substrings or globs; matching folders
	// and files are skipped during scanning.
	IgnorePatterns []string `json:"ignore_patterns"`
	// AIContext is an always-sent prompt describing the files and how to treat
	// them. Empty means the built-in default is used.
	AIContext string `json:"ai_context"`
}

// LoadAppSettings reads the typed application settings, applying defaults.
func (s *Store) LoadAppSettings(ctx context.Context) (AppSettings, error) {
	all, err := s.AllSettings(ctx)
	if err != nil {
		return AppSettings{}, err
	}
	threshold := 0.9
	if v, ok := all[KeyThreshold]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			threshold = f
		}
	}
	autoMove := true
	if v, ok := all[KeyAutoMove]; ok {
		autoMove = v == "true" || v == "1"
	}
	dryRun := false
	if v, ok := all[KeyDryRun]; ok {
		dryRun = v == "true" || v == "1"
	}
	ignore := DefaultIgnorePatterns
	if v, ok := all[KeyIgnore]; ok {
		ignore = splitPatterns(v)
	}
	aiContext := DefaultAIContext
	if v, ok := all[KeyAIContext]; ok {
		aiContext = v
	}
	return AppSettings{
		AIBaseURL:      all[KeyAIBaseURL],
		AIAPIKey:       all[KeyAIAPIKey],
		AIModel:        all[KeyAIModel],
		AIAPIVersion:   all[KeyAIAPIVersion],
		Threshold:      threshold,
		AutoMove:       autoMove,
		DryRun:         dryRun,
		IgnorePatterns: ignore,
		AIContext:      aiContext,
	}, nil
}

// splitPatterns parses a newline/comma separated list, trimming blanks.
func splitPatterns(s string) []string {
	repl := strings.ReplaceAll(s, ",", "\n")
	var out []string
	for _, line := range strings.Split(repl, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// SetDryRun toggles the what-if mode independently of the other settings.
func (s *Store) SetDryRun(ctx context.Context, enabled bool) error {
	return s.SetSetting(ctx, KeyDryRun, strconv.FormatBool(enabled))
}

// SaveAppSettings persists the typed application settings. An empty API key is
// ignored so that the secret is not overwritten when the UI does not resend it.
func (s *Store) SaveAppSettings(ctx context.Context, a AppSettings) error {
	pairs := map[string]string{
		KeyAIBaseURL:    a.AIBaseURL,
		KeyAIModel:      a.AIModel,
		KeyAIAPIVersion: a.AIAPIVersion,
		KeyThreshold:    strconv.FormatFloat(a.Threshold, 'f', -1, 64),
		KeyAutoMove:     strconv.FormatBool(a.AutoMove),
		KeyIgnore:       strings.Join(a.IgnorePatterns, "\n"),
		KeyAIContext:    a.AIContext,
	}
	for k, v := range pairs {
		if err := s.SetSetting(ctx, k, v); err != nil {
			return err
		}
	}
	if a.AIAPIKey != "" {
		if err := s.SetSetting(ctx, KeyAIAPIKey, a.AIAPIKey); err != nil {
			return err
		}
	}
	return nil
}
