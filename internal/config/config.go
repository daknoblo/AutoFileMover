// Package config loads infrastructure configuration from environment variables.
// Application settings (AI endpoint, threshold, libraries, sources) are stored
// in the database and managed through the web UI.
package config

import (
	"os"
	"strconv"
	"time"

	"github.com/daknoblo/AutoFileMover/internal/foundry"
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
	// Foundry is the optional Azure AI Foundry identity. When any field is set,
	// the AI endpoint and the selectable deployments are discovered from Azure
	// instead of being typed in by hand. The client secret is read from the
	// environment on every start and is never persisted.
	Foundry foundry.Identity
}

// Load reads configuration from the environment, applying sensible defaults.
func Load() Config {
	return Config{
		HTTPAddr:        getEnv("AFM_HTTP_ADDR", ":8080"),
		DBPath:          getEnv("AFM_DB_PATH", "/appdata/autofilemover.db"),
		MediaRoot:       getEnv("AFM_MEDIA_ROOT", "/dataroot"),
		StabilityWindow: getEnvDuration("AFM_STABILITY_WINDOW", 30*time.Second),
		ScanInterval:    getEnvDuration("AFM_SCAN_INTERVAL", 5*time.Minute),
		Foundry: foundry.Identity{
			ResourceID:   azureEnv("RESOURCE_ID"),
			TenantID:     azureEnv("TENANT_ID"),
			ClientID:     azureEnv("CLIENT_ID"),
			ClientSecret: azureEnv("CLIENT_SECRET"),
		},
	}
}

// azureEnv reads an Azure identity variable. The project prefix wins so the
// setting matches the other AFM_ options, and the bare name is accepted as the
// conventional Azure spelling used by the Azure SDKs and CLI.
func azureEnv(name string) string {
	return getEnv("AFM_AZURE_"+name, os.Getenv("AZURE_"+name))
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
