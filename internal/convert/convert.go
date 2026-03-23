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
	SplitOnHeading bool
}

// FileJob describes one markdown file to convert.
type FileJob struct {
	AbsPath  string
	RelPath  string
	DestPath string
	// Heading is set when SplitOnHeading is enabled and this job represents a heading section.
	Heading *HeadingInfo
}

// HeadingInfo contains information about a heading section.
type HeadingInfo struct {
	Text       string // Original heading text
	Slug       string // Sanitized filename component
	Level      int    // Heading level (1 for #, 2 for ##, etc.)
	ParentSlug string // Slug of parent heading (for nested headings)
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
	codeFenceRe       = regexp.MustCompile("(?s)```.*?```")
	inlineCodeRe      = regexp.MustCompile("`([^`]*)`")
	headingRe         = regexp.MustCompile(`(?m)^#+\s*`)
	bulletRe          = regexp.MustCompile(`(?m)^[>-]\s*`)
	linkRe            = regexp.MustCompile(`\[((?:[^\]]|\\])+)]\([^)]+\)`)
	multiNewlineRe    = regexp.MustCompile(`\n{3,}`)
	headingFullRe     = regexp.MustCompile(`(?m)^(#+)(\s+)(.+)$`)
	invalidFilenameRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
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

// heading represents a parsed markdown heading.
type heading struct {
	level     int
	text      string
	lineStart int
	lineEnd   int
}

// extractHeadings finds all headings in markdown and returns their text and positions.
func extractHeadings(md string) []heading {
	var headings []heading
	matches := headingFullRe.FindAllStringSubmatchIndex(md, -1)
	for _, match := range matches {
		if len(match) < 8 {
			continue
		}
		level := len(md[match[2]:match[3]])
		text := md[match[6]:match[7]]
		lineStart := strings.Count(md[:match[0]], "\n")
		headings = append(headings, heading{
			level:     level,
			text:      strings.TrimSpace(text),
			lineStart: lineStart,
		})
	}
	// Set lineEnd for each heading (start of next heading or end of file)
	for i := range headings {
		if i+1 < len(headings) {
			headings[i].lineEnd = headings[i+1].lineStart
		} else {
			headings[i].lineEnd = strings.Count(md, "\n")
		}
	}
	return headings
}

// sanitizeFilename converts heading text to a valid filename component.
func sanitizeFilename(text string) string {
	// Convert to lowercase
	text = strings.ToLower(text)
	// Replace spaces with hyphens
	text = invalidFilenameRe.ReplaceAllString(text, "-")
	// Collapse multiple hyphens
	for strings.Contains(text, "--") {
		text = strings.ReplaceAll(text, "--", "-")
	}
	// Trim hyphens from ends
	text = strings.Trim(text, "-")
	return text
}

// splitByHeadings splits markdown content into sections based on headings.
func splitByHeadings(md string, topLevelOnly bool) []string {
	headings := extractHeadings(md)
	if len(headings) == 0 {
		return nil
	}

	lines := strings.Split(md, "\n")
	var sections []string
	var currentSection []string
	currentLevel := 0

	for i, line := range lines {
		// Check if this line is a heading
		isHeading := false
		var h heading
		for _, heading := range headings {
			if heading.lineStart == i {
				isHeading = true
				h = heading
				break
			}
		}

		if isHeading {
			// Save current section if it has content
			if len(currentSection) > 0 {
				sections = append(sections, strings.Join(currentSection, "\n"))
				currentSection = nil
			}
			currentLevel = h.level
			// Skip the heading line itself, but include content under it
			continue
		}

		if topLevelOnly && currentLevel > 1 {
			continue
		}
		currentSection = append(currentSection, line)
	}

	// Don't forget the last section
	if len(currentSection) > 0 {
		sections = append(sections, strings.Join(currentSection, "\n"))
	}

	return sections
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
func CollectMarkdownFiles(root, outDir, pattern, responseFormat string, splitOnHeading bool) ([]FileJob, error) {
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

		if splitOnHeading {
			headingJobs, err := collectHeadingJobs(path, rel, outDir, responseFormat)
			if err != nil {
				return err
			}
			jobs = append(jobs, headingJobs...)
			return nil
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

// collectHeadingJobs creates FileJobs for each heading section in a markdown file.
func collectHeadingJobs(absPath, relPath, outDir, responseFormat string) ([]FileJob, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}

	headings := extractHeadings(string(data))
	if len(headings) == 0 {
		// No headings, create a single job with the original filename
		ext := filepath.Ext(relPath)
		destRel := strings.TrimSuffix(relPath, ext) + "." + responseFormat
		destPath := filepath.Join(outDir, destRel)
		return []FileJob{{
			AbsPath:  absPath,
			RelPath:  relPath,
			DestPath: destPath,
		}}, nil
	}

	// Use a map to track slugs and handle duplicates
	slugCounts := make(map[string]int)
	usedSlugs := make(map[string]bool)

	// Build heading jobs with slug tracking
	var headingInfos []HeadingInfo
	for _, h := range headings {
		slug := sanitizeFilename(h.text)

		// Handle duplicate slugs by appending numeric suffix
		if usedSlugs[slug] {
			slugCounts[slug]++
			slug = fmt.Sprintf("%s-%d", slug, slugCounts[slug])
		}
		usedSlugs[slug] = true

		headingInfos = append(headingInfos, HeadingInfo{
			Text:  h.text,
			Slug:  slug,
			Level: h.level,
		})
	}

	// Build the heading tree to find parent slugs
	buildHeadingTree(headingInfos)

	// Create jobs for each heading
	var jobs []FileJob
	baseDir := filepath.Dir(relPath)
	baseName := strings.TrimSuffix(filepath.Base(relPath), filepath.Ext(relPath))

	for i := range headings {
		var destRel string
		parentSlug := headingInfos[i].ParentSlug

		if parentSlug != "" {
			// Nested heading: parentSlug__slug.aac
			destRel = filepath.Join(baseDir, fmt.Sprintf("%s__%s.%s", parentSlug, headingInfos[i].Slug, responseFormat))
		} else {
			// Top-level heading: slug.aac
			destRel = filepath.Join(baseDir, fmt.Sprintf("%s.%s", headingInfos[i].Slug, responseFormat))
		}

		// If baseDir is not empty and we have a nested structure, we need to include the base filename
		if baseDir != "." && parentSlug == "" {
			// For top-level headings in subdirectory files, prepend directory context
			destRel = filepath.Join(baseDir, fmt.Sprintf("%s__%s.%s", baseName, headingInfos[i].Slug, responseFormat))
		}

		destPath := filepath.Join(outDir, destRel)

		jobs = append(jobs, FileJob{
			AbsPath:  absPath,
			RelPath:  relPath,
			DestPath: destPath,
			Heading:  &headingInfos[i],
		})
	}

	return jobs, nil
}

// buildHeadingTree updates ParentSlug for nested headings.
func buildHeadingTree(headings []HeadingInfo) {
	if len(headings) == 0 {
		return
	}

	for i, h := range headings {
		if h.Level == 1 {
			continue // No parent for top-level headings
		}

		// Find the nearest parent heading (previous heading with lower level)
		for j := i - 1; j >= 0; j-- {
			if headings[j].Level < h.Level {
				headings[i].ParentSlug = headings[j].Slug
				break
			}
		}
	}
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

	mdContent := string(data)

	// If this job represents a heading section, extract just that section
	if job.Heading != nil {
		mdContent = extractSectionForHeading(mdContent, job.Heading.Text, job.Heading.Level)
	}

	plain := StripMarkdown(mdContent)
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

// extractSectionForHeading extracts the markdown content under a specific heading.
func extractSectionForHeading(md, headingText string, headingLevel int) string {
	lines := strings.Split(md, "\n")
	var sectionLines []string
	inSection := false
	currentLevel := 0

	for _, line := range lines {
		// Check if this line is the target heading
		matched := headingFullRe.FindStringSubmatch(line)
		if matched != nil {
			level := len(matched[1])
			text := strings.TrimSpace(matched[3])

			if text == headingText && level == headingLevel {
				inSection = true
				currentLevel = level
				continue
			}

			// If we find another heading at same or lower level, we've left our section
			if inSection && level <= currentLevel {
				break
			}
		}

		if inSection {
			sectionLines = append(sectionLines, line)
		}
	}

	return strings.Join(sectionLines, "\n")
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
