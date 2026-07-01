package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// Force the keys to empty so Load() falls back to its defaults regardless of
	// the ambient environment. t.Setenv restores the previous values afterwards.
	for _, k := range []string{
		"AFM_HTTP_ADDR", "AFM_DB_PATH", "AFM_MEDIA_ROOT",
		"AFM_STABILITY_WINDOW", "AFM_SCAN_INTERVAL",
	} {
		t.Setenv(k, "")
	}
	cfg := Load()
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr default = %q", cfg.HTTPAddr)
	}
	if cfg.DBPath != "/data/autofilemover.db" {
		t.Errorf("DBPath default = %q", cfg.DBPath)
	}
	if cfg.MediaRoot != "/dataroot" {
		t.Errorf("MediaRoot default = %q", cfg.MediaRoot)
	}
	if cfg.StabilityWindow != 30*time.Second {
		t.Errorf("StabilityWindow default = %v", cfg.StabilityWindow)
	}
	if cfg.ScanInterval != 5*time.Minute {
		t.Errorf("ScanInterval default = %v", cfg.ScanInterval)
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("AFM_HTTP_ADDR", "127.0.0.1:9000")
	t.Setenv("AFM_DB_PATH", "/tmp/x.db")
	t.Setenv("AFM_MEDIA_ROOT", "/media")
	t.Setenv("AFM_STABILITY_WINDOW", "45s")
	t.Setenv("AFM_SCAN_INTERVAL", "10m")

	cfg := Load()
	if cfg.HTTPAddr != "127.0.0.1:9000" || cfg.DBPath != "/tmp/x.db" || cfg.MediaRoot != "/media" {
		t.Errorf("env override failed: %+v", cfg)
	}
	if cfg.StabilityWindow != 45*time.Second {
		t.Errorf("StabilityWindow = %v, want 45s", cfg.StabilityWindow)
	}
	if cfg.ScanInterval != 10*time.Minute {
		t.Errorf("ScanInterval = %v, want 10m", cfg.ScanInterval)
	}
}

func TestGetEnvDurationPlainSeconds(t *testing.T) {
	// A bare number is interpreted as seconds.
	t.Setenv("AFM_STABILITY_WINDOW", "90")
	if got := Load().StabilityWindow; got != 90*time.Second {
		t.Errorf("plain-number parse = %v, want 90s", got)
	}
}

func TestGetEnvDurationInvalidFallsBack(t *testing.T) {
	t.Setenv("AFM_SCAN_INTERVAL", "not-a-duration")
	if got := Load().ScanInterval; got != 5*time.Minute {
		t.Errorf("invalid duration should fall back to default, got %v", got)
	}
}
