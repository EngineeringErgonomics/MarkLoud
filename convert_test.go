package main

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

func (m *mockTTSClient) Synthesize(_ context.Context, _ appConfig, chunk string) ([]byte, error) {
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

	got := stripMarkdown(md)
	if got != expected {
		t.Fatalf("stripMarkdown() = %q, want %q", got, expected)
	}
}

func TestChunkTextRespectsLimit(t *testing.T) {
	text := strings.Repeat("Lorem ipsum dolor sit amet. ", 200)
	chunks := chunkText(text, 200)
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

	jobs, err := collectMarkdownFiles(root, filepath.Join(root, "out"), "*.md", "aac")
	if err != nil {
		t.Fatalf("collectMarkdownFiles error: %v", err)
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
	setTTSClient(mock)
	t.Cleanup(func() { setTTSClient(old) })

	res := processFile(context.Background(), fileJob{AbsPath: src, RelPath: "file.md", DestPath: dest}, appConfig{Overwrite: false}, nil)
	if res.Status != jobSkipped {
		t.Fatalf("expected jobSkipped, got %s", res.Status)
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
	setTTSClient(mock)
	t.Cleanup(func() { setTTSClient(old) })

	cfg := appConfig{Overwrite: true, ResponseFormat: "aac"}
	res := processFile(context.Background(), fileJob{AbsPath: src, RelPath: "file.md", DestPath: dest}, cfg, nil)
	if res.Status != jobDone {
		t.Fatalf("expected jobDone, got %s", res.Status)
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
