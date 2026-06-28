package store

import (
	"context"
	"strconv"
)

// Setting keys.
const (
	KeyAIBaseURL    = "ai_base_url"
	KeyAIAPIKey     = "ai_api_key"
	KeyAIModel      = "ai_model"
	KeyAIAPIVersion = "ai_api_version"
	KeyThreshold    = "threshold"
	KeyAutoMove     = "auto_move"
)

// AppSettings is the typed view of the user-configurable settings.
type AppSettings struct {
	AIBaseURL    string  `json:"ai_base_url"`
	AIAPIKey     string  `json:"ai_api_key"`
	AIModel      string  `json:"ai_model"`
	AIAPIVersion string  `json:"ai_api_version"`
	Threshold    float64 `json:"threshold"`
	AutoMove     bool    `json:"auto_move"`
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
	return AppSettings{
		AIBaseURL:    all[KeyAIBaseURL],
		AIAPIKey:     all[KeyAIAPIKey],
		AIModel:      all[KeyAIModel],
		AIAPIVersion: all[KeyAIAPIVersion],
		Threshold:    threshold,
		AutoMove:     autoMove,
	}, nil
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
