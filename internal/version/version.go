// Package version exposes build-time metadata injected via -ldflags.
package version

import "runtime/debug"

// These values are overridden at build time using -ldflags, e.g.:
//
//	-X github.com/daknoblo/AutoFileMover/internal/version.Version=1.2.3
var (
	// Version is the build version (git tag/branch) or "dev" locally.
	Version = "dev"
	// Channel is the release channel: "stable", "dev" or "local".
	Channel = "local"
	// Commit is the git commit hash.
	Commit = "unknown"
	// Date is the build date in RFC3339 format.
	Date = "unknown"
)

// Info bundles the build metadata for display in the UI and logs.
type Info struct {
	Version string `json:"version"`
	Channel string `json:"channel"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	GoVer   string `json:"go_version"`
}

// Get returns the current build information.
func Get() Info {
	goVer := "unknown"
	if bi, ok := debug.ReadBuildInfo(); ok {
		goVer = bi.GoVersion
	}
	return Info{
		Version: Version,
		Channel: Channel,
		Commit:  Commit,
		Date:    Date,
		GoVer:   goVer,
	}
}

// String returns a compact human-readable build string.
func (i Info) String() string {
	return i.Version + " (" + i.Commit + ", built " + i.Date + ")"
}
