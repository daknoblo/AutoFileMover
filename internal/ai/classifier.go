package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ExistingFolder is a sub-folder already present in a library, with an optional
// user-provided description used as additional context.
type ExistingFolder struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// LibraryInfo describes a candidate target library for classification.
type LibraryInfo struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Description is an optional user-provided context for this library.
	Description string `json:"description,omitempty"`
	// ExistingFolders are the sub-folders already present in the library.
	// For series libraries the model must pick one of these (or report none).
	ExistingFolders []ExistingFolder `json:"existing_folders,omitempty"`
}

// FileInput is a single file inside the item, given to the model so it can
// decide what to do with each file individually.
type FileInput struct {
	// Path is the file path relative to the item root.
	Path string `json:"path"`
	// SizeBytes is the file size in bytes (helps tell the main media file apart
	// from samples and metadata).
	SizeBytes int64 `json:"size_bytes"`
}

// Request is the input to a classification.
type Request struct {
	Name  string      `json:"name"`
	Files []FileInput `json:"files"`
	// GlobalContext is an always-sent description of what the files are and how
	// to treat them (from settings). It guides every classification.
	GlobalContext string `json:"global_context,omitempty"`
	// SourceContext is an optional description of the source folder the item was
	// found in, supplied by the user as additional context.
	SourceContext string        `json:"source_context,omitempty"`
	Libraries     []LibraryInfo `json:"libraries"`
}

// Action values for a single file decision.
const (
	ActionMove   = "move"
	ActionDelete = "delete"
	ActionKeep   = "keep"
)

// FileDecision is the per-file action the model recommends.
type FileDecision struct {
	// Path matches the input file path.
	Path string `json:"path"`
	// Action is one of: move, delete, keep.
	Action string `json:"action"`
	// Confidence is the certainty for this single file (0..1).
	Confidence float64 `json:"confidence"`
	// Reason is a short justification (e.g. "sample file", "metadata").
	Reason string `json:"reason"`
}

// Result is the structured classification produced by the model.
type Result struct {
	// Type is one of: movie, series, documentary, unknown.
	Type string `json:"type"`
	// Library is the name of the chosen target library.
	Library string `json:"library"`
	// SeriesFolder is the chosen existing series sub-folder (series only).
	// Empty means no matching series folder exists.
	SeriesFolder string `json:"series_folder"`
	// Title is the cleaned-up media title.
	Title string `json:"title"`
	// Confidence is the model's certainty in the range 0..1.
	Confidence float64 `json:"confidence"`
	// Reasoning is a short human-readable justification.
	Reasoning string `json:"reasoning"`
	// Files lists the recommended action for each input file.
	Files []FileDecision `json:"files"`
}

const systemPrompt = `You are a media library organizer. For a downloaded folder (or single file) you
decide a per-file action for EVERY file, the media type, the target library and the
destination sub-folder.

PER-FILE ACTION — the most important task. Return one entry in "files" for EVERY input
file, with the EXACT same path and an action:
  "move"   = the wanted media: the main (largest) video file, plus matching subtitles.
  "delete" = junk to discard: sample clips (name/path contains "sample"), .nfo, .txt, .url,
             screenshots/proof images (.jpg/.png), repair/checksum files (.par2/.sfv/.md5)
             and similar metadata.
  "keep"   = ONLY if you genuinely cannot decide; such items go to manual review.
Prefer "move"/"delete" over "keep" — most files are clearly one or the other. Use the byte
sizes: a sample is far smaller than the real feature even with the same extension. Each file
decision has its own "confidence" (0.0 to 1.0) and a short "reason".

Then classify the item:
- type: "movie", "series", "documentary", or "unknown".
- library: the single best matching target library, by its exact name from the list.
- series_folder: the matching sub-folder inside the chosen library, taken from its
  existing_folders (ignore release tags, separators and case, e.g.
  "The.Terminal.List.Dark.Wolf.S01E01" matches "The Terminal List"). For a "series" you MUST
  set it if a folder matches, else "". For movies/documentaries set it only if a clearly
  matching folder exists, else "". Copy the folder name EXACTLY as in existing_folders.
- confidence: overall certainty (0.0 to 1.0) that the type and target are correct.
- Use any provided folder descriptions as additional context.

Respond ONLY with a JSON object, no markdown, matching this exact schema:
{"type": string, "library": string, "series_folder": string, "title": string, "confidence": number, "reasoning": string,
 "files": [{"path": string, "action": "move"|"delete"|"keep", "confidence": number, "reason": string}]}`

// Classify runs the classification request against the model.
func (c *Client) Classify(ctx context.Context, req Request) (*Result, error) {
	user := buildUserPrompt(req)
	raw, err := c.ChatJSON(ctx, systemPrompt, user)
	if err != nil {
		return nil, err
	}
	res, err := parseResult(raw)
	if err != nil {
		return nil, fmt.Errorf("%w (raw: %s)", err, raw)
	}
	return res, nil
}

func buildUserPrompt(req Request) string {
	var b strings.Builder
	if req.GlobalContext != "" {
		b.WriteString("Context:\n")
		b.WriteString(req.GlobalContext)
		b.WriteString("\n\n")
	}
	b.WriteString("Downloaded item name:\n")
	b.WriteString(req.Name)
	if req.SourceContext != "" {
		b.WriteString("\n\nSource folder context:\n")
		b.WriteString(req.SourceContext)
	}
	b.WriteString("\n\nFiles contained:\n")
	if len(req.Files) == 0 {
		b.WriteString("(none)\n")
	}
	for _, f := range req.Files {
		b.WriteString(fmt.Sprintf("- %s (%s)\n", f.Path, humanSize(f.SizeBytes)))
	}
	b.WriteString("\nAvailable target libraries:\n")
	for _, l := range req.Libraries {
		b.WriteString(fmt.Sprintf("- name=%q kind=%q", l.Name, l.Kind))
		if l.Description != "" {
			b.WriteString(fmt.Sprintf(" description=%q", l.Description))
		}
		if len(l.ExistingFolders) > 0 {
			b.WriteString(" existing_folders=[")
			for i, f := range l.ExistingFolders {
				if i > 0 {
					b.WriteString(", ")
				}
				if f.Description != "" {
					b.WriteString(fmt.Sprintf("{name=%q, description=%q}", f.Name, f.Description))
				} else {
					b.WriteString(fmt.Sprintf("%q", f.Name))
				}
			}
			b.WriteString("]")
		}
		b.WriteString("\n")
	}
	b.WriteString("\nClassify and respond with the JSON object only.")
	return b.String()
}

func parseResult(raw string) (*Result, error) {
	cleaned := stripCodeFence(strings.TrimSpace(raw))
	var res Result
	if err := json.Unmarshal([]byte(cleaned), &res); err != nil {
		return nil, fmt.Errorf("parse classification json: %w", err)
	}
	res.Type = strings.ToLower(strings.TrimSpace(res.Type))
	if res.Confidence < 0 {
		res.Confidence = 0
	}
	if res.Confidence > 1 {
		res.Confidence = 1
	}
	for i := range res.Files {
		res.Files[i].Action = strings.ToLower(strings.TrimSpace(res.Files[i].Action))
		switch res.Files[i].Action {
		case ActionMove, ActionDelete, ActionKeep:
		default:
			res.Files[i].Action = ActionKeep
		}
		if res.Files[i].Confidence < 0 {
			res.Files[i].Confidence = 0
		}
		if res.Files[i].Confidence > 1 {
			res.Files[i].Confidence = 1
		}
	}
	return &res, nil
}

// humanSize formats a byte count compactly (e.g. 6.7 GB) for the prompt.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 4 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// stripCodeFence removes a surrounding ```json ... ``` fence if present.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:] // drop the language tag line
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}
