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

// Request is the input to a classification.
type Request struct {
	Name string   `json:"name"`
	Files []string `json:"files"`
	// SourceContext is an optional description of the source folder the item was
	// found in, supplied by the user as additional context.
	SourceContext string        `json:"source_context,omitempty"`
	Libraries     []LibraryInfo `json:"libraries"`
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
}

const systemPrompt = `You are a media library organizer. You classify a downloaded folder or file
and decide which target library it belongs to so it can be moved automatically.

Rules:
- Determine the media type: "movie", "series", "documentary", or "unknown".
- Choose the single best matching target library from the provided list by its exact name.
- For a "series", you MUST pick "series_folder" from the chosen library's existing_folders list
  that matches the show. If none of the existing folders match the show, set "series_folder" to "".
- "confidence" is your overall certainty (0.0 to 1.0) that BOTH the type and the target are correct.
- Be conservative: if the name is ambiguous or no good target exists, lower the confidence.
- Use any provided folder descriptions as additional context to pick the correct target.
- Respond ONLY with a JSON object, no markdown, matching this exact schema:
{"type": string, "library": string, "series_folder": string, "title": string, "confidence": number, "reasoning": string}`

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
		b.WriteString("- ")
		b.WriteString(f)
		b.WriteString("\n")
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
	return &res, nil
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
