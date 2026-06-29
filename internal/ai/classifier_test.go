package ai

import "testing"

func TestParseResultPerFile(t *testing.T) {
	raw := "```json\n{\"type\":\"series\",\"library\":\"Serien\",\"series_folder\":\"Dutton Ranch\"," +
		"\"title\":\"Dutton Ranch\",\"confidence\":0.95,\"reasoning\":\"ok\"," +
		"\"files\":[{\"path\":\"a.mkv\",\"action\":\"MOVE\",\"confidence\":0.97,\"reason\":\"main\"}," +
		"{\"path\":\"a.sample.mkv\",\"action\":\"delete\",\"confidence\":0.9,\"reason\":\"sample\"}," +
		"{\"path\":\"a.nfo\",\"action\":\"weird\",\"confidence\":2,\"reason\":\"meta\"}]}\n```"
	res, err := parseResult(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Type != "series" || len(res.Files) != 3 {
		t.Fatalf("unexpected: %+v", res)
	}
	if res.Files[0].Action != ActionMove {
		t.Errorf("want move, got %q", res.Files[0].Action)
	}
	if res.Files[2].Action != ActionKeep {
		t.Errorf("unknown action should fall back to keep, got %q", res.Files[2].Action)
	}
	if res.Files[2].Confidence != 1 {
		t.Errorf("confidence should clamp to 1, got %v", res.Files[2].Confidence)
	}
}

func TestHumanSize(t *testing.T) {
	if got := humanSize(6_700_000_000); got != "6.2 GB" {
		t.Errorf("got %q", got)
	}
}
