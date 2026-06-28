// Package config loads infrastructure configuration from environment variables.
// Application settings (AI endpoint, threshold, libraries, sources) are stored
// in the database and managed through the web UI.
package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds the infrastructure configuration for the service.
type Config struct {
	// HTTPAddr is the address the web server listens on.
	HTTPAddr string
	// DBPath is the path to the SQLite database file.
	DBPath string
	// MediaRoot is the root path of the mounted media volume. It is used to
	// validate that configured source/target folders stay inside the mount.
	MediaRoot string
	// StabilityWindow is how long a download folder must be unchanged before it
	// is considered complete and ready for processing.
	StabilityWindow time.Duration
	// ScanInterval is the fallback periodic scan interval. The watcher handles
	// real-time events; the periodic scan catches anything missed.
	ScanInterval time.Duration
}

// Load reads configuration from the environment, applying sensible defaults.
func Load() Config {
	return Config{
		HTTPAddr:        getEnv("AFM_HTTP_ADDR", ":8080"),
		DBPath:          getEnv("AFM_DB_PATH", "/data/autofilemover.db"),
		MediaRoot:       getEnv("AFM_MEDIA_ROOT", "/media"),
		StabilityWindow: getEnvDuration("AFM_STABILITY_WINDOW", 30*time.Second),
		ScanInterval:    getEnvDuration("AFM_SCAN_INTERVAL", 5*time.Minute),
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		// Allow a plain number to be interpreted as seconds.
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return fallback
}
