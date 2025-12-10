package convert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Config holds inputs for a conversion run.
type Config struct {
	Root           string
	Out            string
	Voice          string
	Model          string
	ResponseFormat string
	Speed          float64
	Overwrite      bool
	Instructions   string
	APIKey         string
	Pattern        string
}

// FileJob describes one markdown file to convert.
type FileJob struct {
	AbsPath  string
	RelPath  string
	DestPath string
}

type JobOutcome string

const (
	JobDone    JobOutcome = "done"
	JobSkipped JobOutcome = "skipped"
	JobEmpty   JobOutcome = "empty"
	JobFailed  JobOutcome = "failed"
)

// JobResult represents the result of processing one file.
type JobResult struct {
	Status JobOutcome
	Chunks int
	Err    error
}

// TTSClient abstracts the text-to-speech provider so tests can swap in a mock.
type TTSClient interface {
	Synthesize(ctx context.Context, cfg Config, chunk string) ([]byte, error)
}

type openAIClient struct {
	httpClient *http.Client
}

var (
	defaultHTTPClient           = &http.Client{Timeout: 90 * time.Second}
	ttsClient         TTSClient = &openAIClient{httpClient: defaultHTTPClient}
)

// SetTTSClient overrides the global TTS client (used in tests).
func SetTTSClient(c TTSClient) {
	if c != nil {
		ttsClient = c
	}
}

var (
	codeFenceRe    = regexp.MustCompile("(?s)```.*?```")
	inlineCodeRe   = regexp.MustCompile("`([^`]*)`")
	headingRe      = regexp.MustCompile("(?m)^#+\\s*")
	bulletRe       = regexp.MustCompile("(?m)^[>-]\\s*")
	linkRe         = regexp.MustCompile(`\[((?:[^\]]|\\])+)]\([^)]+\)`)
	multiNewlineRe = regexp.MustCompile("\n{3,}")
)

// StripMarkdown removes light Markdown syntax for cleaner TTS output.
func StripMarkdown(md string) string {
	md = codeFenceRe.ReplaceAllString(md, "")
	md = inlineCodeRe.ReplaceAllString(md, "$1")
	md = headingRe.ReplaceAllString(md, "")
	md = bulletRe.ReplaceAllString(md, "")
	md = linkRe.ReplaceAllString(md, "$1")
	md = multiNewlineRe.ReplaceAllString(md, "\n\n")
	return strings.TrimSpace(md)
}

// ChunkText splits text into roughly maxChars-sized chunks at paragraph/sentence boundaries.
func ChunkText(text string, maxChars int) []string {
	if maxChars <= 0 {
		maxChars = 4000
	}
	paras := strings.Split(text, "\n\n")
	chunks := make([]string, 0)

	var current []string
	currentLen := 0

	flush := func() {
		if len(current) == 0 {
			return
		}
		chunks = append(chunks, strings.TrimSpace(strings.Join(current, "\n\n")))
		current = current[:0]
		currentLen = 0
	}

	for _, para := range paras {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		paraLen := len(para)
		if paraLen > maxChars {
			sentences := regexp.MustCompile(`[.!?]\s+`).Split(para, -1)
			buf := make([]string, 0)
			bufLen := 0
			for _, s := range sentences {
				s = strings.TrimSpace(s)
				if s == "" {
					continue
				}
				sLen := len(s)
				if bufLen+sLen+1 > maxChars {
					if len(buf) > 0 {
						chunks = append(chunks, strings.TrimSpace(strings.Join(buf, " ")))
					}
					buf = []string{s}
					bufLen = sLen
				} else {
					buf = append(buf, s)
					bufLen += sLen + 1
				}
			}
			if len(buf) > 0 {
				chunks = append(chunks, strings.TrimSpace(strings.Join(buf, " ")))
			}
			continue
		}

		if currentLen+paraLen+2 <= maxChars {
			current = append(current, para)
			currentLen += paraLen + 2
		} else {
			flush()
			current = append(current, para)
			currentLen = paraLen
		}
	}

	flush()
	out := make([]string, 0, len(chunks))
	for _, c := range chunks {
		if strings.TrimSpace(c) != "" {
			out = append(out, c)
		}
	}
	return out
}

// CollectMarkdownFiles returns a list of jobs for matching markdown files.
func CollectMarkdownFiles(root, outDir, pattern, responseFormat string) ([]FileJob, error) {
	if pattern == "" {
		pattern = "*.md"
	}
	root = filepath.Clean(root)
	outDir = filepath.Clean(outDir)

	var jobs []FileJob
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		match, err := filepath.Match(pattern, d.Name())
		if err != nil {
			return err
		}
		if !match {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		ext := filepath.Ext(rel)
		destRel := strings.TrimSuffix(rel, ext) + "." + responseFormat
		destPath := filepath.Join(outDir, destRel)
		jobs = append(jobs, FileJob{
			AbsPath:  path,
			RelPath:  rel,
			DestPath: destPath,
		})
		return nil
	})
	return jobs, err
}

// ProcessFile converts a single file using the configured TTS client.
func ProcessFile(ctx context.Context, job FileJob, cfg Config, progress func(current, total int)) JobResult {
	if err := ctx.Err(); err != nil {
		return JobResult{Status: JobFailed, Err: err}
	}

	if !cfg.Overwrite {
		if _, err := os.Stat(job.DestPath); err == nil {
			return JobResult{Status: JobSkipped, Chunks: 0, Err: nil}
		}
	}

	data, err := os.ReadFile(job.AbsPath)
	if err != nil {
		return JobResult{Status: JobFailed, Err: err}
	}
	plain := StripMarkdown(string(data))
	if strings.TrimSpace(plain) == "" {
		return JobResult{Status: JobEmpty}
	}

	chunks := ChunkText(plain, 4000)
	if len(chunks) == 0 {
		return JobResult{Status: JobEmpty}
	}

	if err := os.MkdirAll(filepath.Dir(job.DestPath), 0o755); err != nil {
		return JobResult{Status: JobFailed, Err: err}
	}

	totalChunks := len(chunks)
	if progress != nil {
		progress(0, totalChunks)
	}
	var buf bytes.Buffer
	for idx, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return JobResult{Status: JobFailed, Chunks: totalChunks, Err: err}
		}
		if progress != nil {
			progress(idx+1, totalChunks)
		}
		if ttsClient == nil {
			return JobResult{Status: JobFailed, Chunks: totalChunks, Err: errors.New("tts client not configured")}
		}
		chunkAudio, err := ttsClient.Synthesize(ctx, cfg, chunk)
		if err != nil {
			return JobResult{Status: JobFailed, Chunks: totalChunks, Err: err}
		}
		if _, err := buf.Write(chunkAudio); err != nil {
			return JobResult{Status: JobFailed, Chunks: totalChunks, Err: err}
		}
	}

	if err := os.WriteFile(job.DestPath, buf.Bytes(), 0o644); err != nil {
		return JobResult{Status: JobFailed, Chunks: totalChunks, Err: err}
	}

	return JobResult{Status: JobDone, Chunks: len(chunks)}
}

func (c *openAIClient) Synthesize(ctx context.Context, cfg Config, chunk string) ([]byte, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("OPENAI_API_KEY is missing")
	}
	if c.httpClient == nil {
		c.httpClient = defaultHTTPClient
	}

	payload := map[string]any{
		"model":           cfg.Model,
		"input":           chunk,
		"voice":           cfg.Voice,
		"response_format": cfg.ResponseFormat,
	}
	if cfg.Speed > 0 && cfg.Speed != 1.0 {
		payload["speed"] = cfg.Speed
	}
	if strings.TrimSpace(cfg.Instructions) != "" {
		payload["instructions"] = cfg.Instructions
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			time.Sleep(time.Duration(attempt*attempt) * 300 * time.Millisecond)
		}

		var buf bytes.Buffer
		if err := doTTSRequest(ctx, c.httpClient, cfg.APIKey, body, &buf); err != nil {
			lastErr = err
			var apiErr *apiError
			if errors.As(err, &apiErr) && apiErr.retryable {
				continue
			}
			return nil, err
		}
		return buf.Bytes(), nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("unknown TTS error")
}

type apiError struct {
	status    string
	message   string
	retryable bool
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s: %s", e.status, e.message)
}

func doTTSRequest(ctx context.Context, client *http.Client, apiKey string, body []byte, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/audio/speech", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		retryable := resp.StatusCode == 429 || resp.StatusCode >= 500
		return &apiError{status: resp.Status, message: strings.TrimSpace(string(snippet)), retryable: retryable}
	}

	_, err = io.Copy(w, resp.Body)
	return err
}
