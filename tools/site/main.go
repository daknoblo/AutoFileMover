// Command site renders the project documentation into a static website for
// GitHub Pages. It is a separate Go module so the service binary keeps its
// dependency set untouched.
//
//	go run ./tools/site -repo . -out site
//
// Every page comes from a Markdown file in the repository, so the site is
// always in sync with the docs — including docs/demo.md, which is generated
// from the screenshots.
package main

import (
	"bytes"
	"embed"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

//go:embed assets/*
var assets embed.FS

// page is one Markdown source rendered to one HTML file.
type page struct {
	Src  string // path relative to the repository root
	Out  string // file name in the output directory
	Nav  string // label in the navigation
	Desc string // meta description
}

// pages defines the site structure and the navigation order.
var pages = []page{
	{Src: "README.md", Out: "index.html", Nav: "Overview", Desc: "Self-hosted service that sorts downloaded media into the matching library using an AI endpoint."},
	{Src: "docs/demo.md", Out: "demo.html", Nav: "Demo", Desc: "Screenshots of every section of the AutoFileMover web interface."},
	{Src: "docs/installation.md", Out: "installation.html", Nav: "Installation", Desc: "Install AutoFileMover with Docker Compose."},
	{Src: "docs/configuration.md", Out: "configuration.html", Nav: "Configuration", Desc: "Environment variables and application settings."},
	{Src: "docs/architecture.md", Out: "architecture.html", Nav: "Architecture", Desc: "How scanning, classification and moving fit together."},
	{Src: "docs/development.md", Out: "development.html", Nav: "Development", Desc: "Build, test and contribute to AutoFileMover."},
}

// navLink is one entry of the rendered navigation.
type navLink struct {
	Label  string
	Href   string
	Active bool
}

// pageData is the template input for a single page.
type pageData struct {
	Title   string
	Desc    string
	Nav     []navLink
	Content template.HTML
	Repo    string
}

func main() {
	repo := flag.String("repo", ".", "path of the repository root")
	out := flag.String("out", "site", "output directory")
	repoURL := flag.String("repo-url", "https://github.com/daknoblo/AutoFileMover", "repository URL shown in the header")
	flag.Parse()

	if err := build(*repo, *out, *repoURL); err != nil {
		log.Fatal(err)
	}
}

func build(repo, out, repoURL string) error {
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	tmpl, err := template.ParseFS(assets, "assets/page.html")
	if err != nil {
		return err
	}
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(html.WithUnsafe()),
	)

	for _, p := range pages {
		src, err := os.ReadFile(filepath.Join(repo, p.Src))
		if err != nil {
			return fmt.Errorf("read %s: %w", p.Src, err)
		}
		doc := md.Parser().Parse(text.NewReader(src))
		rewriteLinks(doc, repoURL)

		var body bytes.Buffer
		if err := md.Renderer().Render(&body, src, doc); err != nil {
			return fmt.Errorf("render %s: %w", p.Src, err)
		}

		data := pageData{
			Title:   title(doc, src, p.Nav),
			Desc:    p.Desc,
			Nav:     navigation(p),
			Content: template.HTML(body.String()), //nolint:gosec // rendered from the repository's own Markdown
			Repo:    repoURL,
		}
		var out2 bytes.Buffer
		if err := tmpl.Execute(&out2, data); err != nil {
			return fmt.Errorf("template %s: %w", p.Src, err)
		}
		if err := os.WriteFile(filepath.Join(out, p.Out), out2.Bytes(), 0o644); err != nil {
			return err
		}
	}

	if err := copyStatic(assets, "assets/style.css", filepath.Join(out, "style.css")); err != nil {
		return err
	}
	if err := copyFile(filepath.Join(repo, "internal/web/static/icon.svg"), filepath.Join(out, "icon.svg")); err != nil {
		return err
	}
	// GitHub Pages must serve the files as-is instead of running Jekyll.
	if err := os.WriteFile(filepath.Join(out, ".nojekyll"), nil, 0o644); err != nil {
		return err
	}
	return copyDir(filepath.Join(repo, "docs/images"), filepath.Join(out, "images"))
}

// navigation builds the navigation with the current page marked active.
func navigation(current page) []navLink {
	links := make([]navLink, 0, len(pages))
	for _, p := range pages {
		links = append(links, navLink{Label: p.Nav, Href: p.Out, Active: p.Out == current.Out})
	}
	return links
}

// title returns the first level-1 heading of the document, falling back to the
// navigation label.
func title(doc ast.Node, src []byte, fallback string) string {
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		if h, ok := n.(*ast.Heading); ok && h.Level == 1 {
			return string(h.Text(src)) //nolint:staticcheck // Text is the simplest way to flatten a heading
		}
	}
	return fallback
}

// rewriteLinks maps repository-relative Markdown links onto the generated site:
// "docs/foo.md" and "foo.md" become "foo.html", image paths are flattened to the
// copied images directory and any other repository path becomes a GitHub URL.
// External links are left untouched.
func rewriteLinks(doc ast.Node, repoURL string) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.Link:
			v.Destination = []byte(rewriteTarget(string(v.Destination), repoURL))
		case *ast.Image:
			v.Destination = []byte(rewriteTarget(string(v.Destination), repoURL))
		}
		return ast.WalkContinue, nil
	})
}

func rewriteTarget(dest, repoURL string) string {
	if dest == "" || strings.Contains(dest, "://") || strings.HasPrefix(dest, "#") || strings.HasPrefix(dest, "mailto:") {
		return dest
	}
	path, anchor, hasAnchor := strings.Cut(dest, "#")
	path = strings.TrimPrefix(path, "./")
	switch {
	case strings.HasSuffix(path, ".md"):
		path = pageFor(filepath.Base(path), repoURL, path)
	case strings.HasPrefix(path, "docs/images/"):
		path = strings.TrimPrefix(path, "docs/")
	case strings.HasPrefix(path, "images/"):
		// already relative to the docs directory
	default:
		// Files that are not part of the site (LICENSE, go.mod, …) stay in the
		// repository.
		path = repoURL + "/blob/main/" + path
	}
	if hasAnchor {
		return path + "#" + anchor
	}
	return path
}

// pageFor maps a Markdown file name to its generated page, falling back to the
// file in the repository when the document is not part of the site.
func pageFor(name, repoURL, orig string) string {
	for _, p := range pages {
		if filepath.Base(p.Src) == name {
			return p.Out
		}
	}
	return repoURL + "/blob/main/" + orig
}

func copyStatic(fsys fs.FS, src, dst string) error {
	b, err := fs.ReadFile(fsys, src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // paths come from the build configuration
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst) //nolint:gosec // paths come from the build configuration
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no screenshots generated yet
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
