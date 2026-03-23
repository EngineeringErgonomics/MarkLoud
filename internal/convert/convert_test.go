package convert

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type mockTTSClient struct {
	calls  int
	chunks []string
	resp   []byte
	err    error
}

func (m *mockTTSClient) Synthesize(_ context.Context, _ Config, chunk string) ([]byte, error) {
	m.calls++
	m.chunks = append(m.chunks, chunk)
	if m.err != nil {
		return nil, m.err
	}
	return m.resp, nil
}

func TestStripMarkdown(t *testing.T) {
	md := "" +
		"# Title\n\n" +
		"Some `inline` code and a [link](https://example.com).\n\n" +
		"- bullet one\n- bullet two\n\n" +
		"```\ncode fence\n```\n"

	expected := "Title\n\nSome inline code and a link.\n\nbullet one\nbullet two"

	got := StripMarkdown(md)
	if got != expected {
		t.Fatalf("StripMarkdown() = %q, want %q", got, expected)
	}
}

func TestChunkTextRespectsLimit(t *testing.T) {
	text := strings.Repeat("Lorem ipsum dolor sit amet. ", 200)
	chunks := ChunkText(text, 200)
	if len(chunks) == 0 {
		t.Fatalf("expected chunks, got none")
	}
	for i, c := range chunks {
		if len(c) > 200 {
			t.Fatalf("chunk %d exceeds limit: %d", i, len(c))
		}
	}
}

func TestCollectMarkdownFiles(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "docs")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(sub, "note.md")
	if err := os.WriteFile(src, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignore.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	jobs, err := CollectMarkdownFiles(root, filepath.Join(root, "out"), "*.md", "aac", false)
	if err != nil {
		t.Fatalf("CollectMarkdownFiles error: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if !strings.HasSuffix(jobs[0].DestPath, filepath.Join("out", "docs", "note.aac")) {
		t.Fatalf("unexpected dest path: %s", jobs[0].DestPath)
	}
}

func TestProcessFileSkipsExisting(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "file.md")
	dest := filepath.Join(root, "out", "file.aac")

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockTTSClient{}
	old := ttsClient
	SetTTSClient(mock)
	t.Cleanup(func() { SetTTSClient(old) })

	res := ProcessFile(context.Background(), FileJob{AbsPath: src, RelPath: "file.md", DestPath: dest}, Config{Overwrite: false}, nil)
	if res.Status != JobSkipped {
		t.Fatalf("expected JobSkipped, got %s", res.Status)
	}
	if mock.calls != 0 {
		t.Fatalf("expected TTS not to be called, got %d", mock.calls)
	}
}

func TestProcessFileUsesTTSClient(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "file.md")
	dest := filepath.Join(root, "out", "file.aac")

	if err := os.WriteFile(src, []byte("Hello world."), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockTTSClient{resp: []byte("AUDIO")}
	old := ttsClient
	SetTTSClient(mock)
	t.Cleanup(func() { SetTTSClient(old) })

	cfg := Config{Overwrite: true, ResponseFormat: "aac"}
	res := ProcessFile(context.Background(), FileJob{AbsPath: src, RelPath: "file.md", DestPath: dest}, cfg, nil)
	if res.Status != JobDone {
		t.Fatalf("expected JobDone, got %s", res.Status)
	}
	if mock.calls == 0 {
		t.Fatalf("expected TTS client to be called")
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "AUDIO" {
		t.Fatalf("unexpected audio data %q", string(data))
	}
}

func TestExtractHeadings(t *testing.T) {
	md := `# Introduction

Some intro content.

# Getting Started

## Installation

Install steps.

## Configuration

Config steps.

# FAQ`

	headings := extractHeadings(md)
	if len(headings) != 5 {
		t.Fatalf("expected 5 headings, got %d", len(headings))
	}

	// Check first heading
	if headings[0].text != "Introduction" || headings[0].level != 1 {
		t.Fatalf("first heading: got %q (level %d), want %q (level %d)", headings[0].text, headings[0].level, "Introduction", 1)
	}

	// Check nested heading
	if headings[1].text != "Getting Started" || headings[1].level != 1 {
		t.Fatalf("second heading: got %q (level %d), want %q (level %d)", headings[1].text, headings[1].level, "Getting Started", 1)
	}

	if headings[2].text != "Installation" || headings[2].level != 2 {
		t.Fatalf("third heading: got %q (level %d), want %q (level %d)", headings[2].text, headings[2].level, "Installation", 2)
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Hello World", "hello-world"},
		{"  Spaces  ", "spaces"},
		{"Special!@#$%^&*()", "special"},
		{"multiple   spaces", "multiple-spaces"},
		{"already-clean", "already-clean"},
		{"UPPERCASE", "uppercase"},
		{"mixCase123", "mixcase123"},
		{"with_underscores", "with_underscores"},
		{"with-dashes", "with-dashes"},
	}

	for _, tc := range tests {
		got := sanitizeFilename(tc.input)
		if got != tc.expected {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestSplitByHeadings(t *testing.T) {
	md := `# Intro

Intro content.

# Getting Started

## Install

Install content.

## Config

Config content.

# Outro

Outro content.`

	// Test top-level only
	sections := splitByHeadings(md, true)
	if len(sections) != 3 {
		t.Fatalf("expected 3 sections (top-level only), got %d", len(sections))
	}

	// Test all levels
	sections = splitByHeadings(md, false)
	if len(sections) != 5 {
		t.Fatalf("expected 5 sections (all levels), got %d", len(sections))
	}
}

func TestCollectMarkdownFilesWithSplitOnHeading(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "docs")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(sub, "note.md")
	md := `# Introduction

Intro content.

# Getting Started

## Installation

Install content.`
	if err := os.WriteFile(src, []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}

	// Without split on heading - should get 1 job
	jobs, err := CollectMarkdownFiles(root, filepath.Join(root, "out"), "*.md", "aac", false)
	if err != nil {
		t.Fatalf("CollectMarkdownFiles error: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job without split, got %d", len(jobs))
	}

	// With split on heading - should get 3 jobs (Introduction, Getting Started, Installation)
	jobs, err = CollectMarkdownFiles(root, filepath.Join(root, "out"), "*.md", "aac", true)
	if err != nil {
		t.Fatalf("CollectMarkdownFiles error: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("expected 3 jobs with split, got %d", len(jobs))
	}

	// Check that jobs have Heading info set
	if jobs[0].Heading == nil || jobs[1].Heading == nil || jobs[2].Heading == nil {
		t.Fatal("expected Heading info to be set for split jobs")
	}
	if jobs[0].Heading.Text != "Introduction" {
		t.Errorf("first job heading: got %q, want %q", jobs[0].Heading.Text, "Introduction")
	}
	if jobs[1].Heading.Text != "Getting Started" {
		t.Errorf("second job heading: got %q, want %q", jobs[1].Heading.Text, "Getting Started")
	}
	if jobs[2].Heading.Text != "Installation" {
		t.Errorf("third job heading: got %q, want %q", jobs[2].Heading.Text, "Installation")
	}

	// Check nested heading has correct parent
	if jobs[2].Heading.ParentSlug != "getting-started" {
		t.Errorf("nested heading parent slug: got %q, want %q", jobs[2].Heading.ParentSlug, "getting-started")
	}
}

func TestCollectMarkdownFilesNoHeadings(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "note.md")
	if err := os.WriteFile(src, []byte("Just plain content without any headings."), 0o644); err != nil {
		t.Fatal(err)
	}

	// With split on heading but no headings - should still get 1 job
	jobs, err := CollectMarkdownFiles(root, filepath.Join(root, "out"), "*.md", "aac", true)
	if err != nil {
		t.Fatalf("CollectMarkdownFiles error: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job for file without headings, got %d", len(jobs))
	}
	if jobs[0].Heading != nil {
		t.Error("expected Heading to be nil for file without headings")
	}
}
