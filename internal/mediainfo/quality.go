// Package mediainfo derives release attributes (resolution, codec, dynamic
// range, source) and episode identifiers from a media file name. It relies only
// on the file name, so it needs no external tools (ffprobe/ffmpeg) and keeps the
// container image small. Scene/P2P release names already encode this metadata,
// e.g. "The.Show.S05E01.1080p.DV.HDR.WEB.H265-GRP".
package mediainfo

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Quality holds the release attributes parsed from a file name. Empty fields
// mean the attribute could not be determined from the name.
type Quality struct {
	Resolution string // e.g. 2160p, 1080p, 720p, 480p
	Codec      string // e.g. H265, H264, AV1
	DynRange   string // e.g. DV, HDR
	Source     string // e.g. WEB, BluRay, HDTV, DVD
}

// Summary renders the present attributes as a compact human string such as
// "1080p · H265 · HDR · WEB". It returns "" when nothing was recognised.
func (q Quality) Summary() string {
	parts := make([]string, 0, 4)
	for _, p := range []string{q.Resolution, q.DynRange, q.Codec, q.Source} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}

var (
	reResP = regexp.MustCompile(`(?i)\b(2160|1440|1080|720|576|480)p\b`)
	reDV   = regexp.MustCompile(`(?i)(?:\bdv\b|dovi|dolby[\s._-]?vision)`)
	reHDR  = regexp.MustCompile(`(?i)\bhdr(?:10)?\+?\b`)
)

// Parse extracts the release attributes from a file name.
func Parse(name string) Quality {
	l := strings.ToLower(name)
	q := Quality{}

	switch {
	case reResP.MatchString(l):
		q.Resolution = strings.ToLower(reResP.FindStringSubmatch(l)[1]) + "p"
	case strings.Contains(l, "2160") || strings.Contains(l, "4k") || strings.Contains(l, "uhd"):
		q.Resolution = "2160p"
	}

	switch {
	case containsAny(l, "x265", "h265", "h.265", "hevc"):
		q.Codec = "H265"
	case containsAny(l, "x264", "h264", "h.264", "avc"):
		q.Codec = "H264"
	case strings.Contains(l, "av1"):
		q.Codec = "AV1"
	}

	switch {
	case reDV.MatchString(l):
		q.DynRange = "DV"
	case reHDR.MatchString(l):
		q.DynRange = "HDR"
	}

	switch {
	case containsAny(l, "bluray", "blu-ray", "bdrip", "brrip", "bdremux", "remux"):
		q.Source = "BluRay"
	case containsAny(l, "web-dl", "webdl", "webrip", "web."):
		q.Source = "WEB"
	case strings.Contains(l, "hdtv"):
		q.Source = "HDTV"
	case containsAny(l, "dvdrip", "dvd"):
		q.Source = "DVD"
	}

	return q
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

var (
	reSxxExx = regexp.MustCompile(`(?i)s(\d{1,2})[\s._-]?e(\d{1,3})`)
	reXform  = regexp.MustCompile(`(?i)(?:^|[^0-9])(\d{1,2})x(\d{2})(?:[^0-9]|$)`)
)

// Episode returns a canonical season/episode identifier such as "S05E01" found
// in name, or "" when name carries no episode marker. It recognises both the
// "S05E01" and "1x05" notations.
func Episode(name string) string {
	if m := reSxxExx.FindStringSubmatch(name); m != nil {
		return fmtEpisode(m[1], m[2])
	}
	if m := reXform.FindStringSubmatch(name); m != nil {
		return fmtEpisode(m[1], m[2])
	}
	return ""
}

func fmtEpisode(seasonStr, episodeStr string) string {
	season, _ := strconv.Atoi(seasonStr)
	episode, _ := strconv.Atoi(episodeStr)
	return fmt.Sprintf("S%02dE%02d", season, episode)
}
