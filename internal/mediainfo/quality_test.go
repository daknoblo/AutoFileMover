package mediainfo

import "testing"

func TestParseQuality(t *testing.T) {
	cases := []struct {
		name string
		want Quality
	}{
		{
			"The.Bear.2022.S05E01.GERMAN.DL.1080p.DV.HDR.WEB.H265-TSCC.mkv",
			Quality{Resolution: "1080p", Codec: "H265", DynRange: "DV", Source: "WEB"},
		},
		{
			"Some.Movie.2019.2160p.UHD.BluRay.x265-GRP.mkv",
			Quality{Resolution: "2160p", Codec: "H265", DynRange: "", Source: "BluRay"},
		},
		{
			"Show.S01E02.720p.HDTV.x264-XYZ.mkv",
			Quality{Resolution: "720p", Codec: "H264", DynRange: "", Source: "HDTV"},
		},
		{
			"plain_filename.mkv",
			Quality{},
		},
		{
			"Movie.HDR10.WEB-DL.AV1.mkv",
			Quality{Resolution: "", Codec: "AV1", DynRange: "HDR", Source: "WEB"},
		},
	}
	for _, c := range cases {
		got := Parse(c.name)
		if got != c.want {
			t.Errorf("Parse(%q) = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestParseDVNotMatchedInDVD(t *testing.T) {
	// "DVD" must not be read as Dolby Vision.
	q := Parse("Old.Movie.1999.DVDRip.x264.mkv")
	if q.DynRange != "" {
		t.Errorf("DynRange = %q, want empty (DVD is not DV)", q.DynRange)
	}
	if q.Source != "DVD" {
		t.Errorf("Source = %q, want DVD", q.Source)
	}
}

func TestQualitySummary(t *testing.T) {
	q := Quality{Resolution: "1080p", Codec: "H265", DynRange: "HDR", Source: "WEB"}
	if got, want := q.Summary(), "1080p · HDR · H265 · WEB"; got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
	if got := (Quality{}).Summary(); got != "" {
		t.Errorf("empty Summary() = %q, want empty", got)
	}
}

func TestEpisode(t *testing.T) {
	cases := map[string]string{
		"The.Show.S05E01.1080p.mkv":   "S05E01",
		"The.Show.s5e1.1080p.mkv":     "S05E01",
		"The.Show.S05.E02.mkv":        "S05E02",
		"The Show 1x05 720p.mkv":      "S01E05",
		"Movie.2019.1080p.mkv":        "",
		"Show.S10E120.mkv":            "S10E120",
	}
	for name, want := range cases {
		if got := Episode(name); got != want {
			t.Errorf("Episode(%q) = %q, want %q", name, got, want)
		}
	}
}
