package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/joho/godotenv"
)

type appState int

const (
	stateConfig appState = iota
	stateRunning
	stateDone
	stateError
)

type summaryCounts struct {
	Done    int
	Skipped int
	Empty   int
	Failed  int
}

type preparedMsg struct {
	cfg  appConfig
	jobs []fileJob
}

type prepareFailedMsg struct{ err error }

type fileDoneMsg struct {
	idx int
	res jobResult
	job fileJob
}

type chunkMsg struct {
	job   fileJob
	idx   int
	total int
	done  bool
	err   error
}

type allDoneMsg struct{}

type cliOptions struct {
	inputDir  string
	outputDir string
	voice     string
	overwrite bool
}

type model struct {
	state      appState
	inputs     []textinput.Model
	focusIndex int
	overwrite  bool
	message    string
	err        error

	cfg          appConfig
	jobs         []fileJob
	currentIdx   int
	summary      summaryCounts
	currentChunk string
	lastError    string

	workerSem chan struct{}
	chunkCh   chan chunkMsg
	tasks     map[string]taskStatus

	logFile *os.File
	logPath string
	logMu   sync.Mutex

	spin spinner.Model

	// CLI mode - skip config screen and auto-quit on completion
	cliMode bool
	cliOpts *cliOptions
}

type taskStatus struct {
	name   string
	idx    int
	total  int
	status string
	err    error
}

func initialModel(opts *cliOptions) model {
	tiRoot := textinput.New()
	tiRoot.Placeholder = "./notes"
	tiRoot.SetValue(".")
	tiRoot.Focus()

	tiOut := textinput.New()
	tiOut.Placeholder = "./audio_out"
	tiOut.SetValue("./audio_out")

	tiVoice := textinput.New()
	tiVoice.Placeholder = "alloy"
	tiVoice.SetValue(envOr("OPENAI_TTS_VOICE", "alloy"))

	inputs := []textinput.Model{tiRoot, tiOut, tiVoice}
	for i := range inputs {
		if i == 0 {
			inputs[i].Focus()
			inputs[i].PromptStyle = focusedStyle
			inputs[i].TextStyle = focusedStyle
		}
	}

	spin := spinner.New()
	spin.Spinner = spinner.Points

	m := model{
		state:      stateConfig,
		inputs:     inputs,
		focusIndex: 0,
		overwrite:  false,
		message:    "",
		err:        nil,
		spin:       spin,
		tasks:      make(map[string]taskStatus),
	}

	// CLI mode: pre-fill inputs and mark for auto-start
	if opts != nil {
		m.cliMode = true
		m.cliOpts = opts
		m.inputs[0].SetValue(opts.inputDir)
		m.inputs[1].SetValue(opts.outputDir)
		m.inputs[2].SetValue(opts.voice)
		m.overwrite = opts.overwrite
	}

	return m
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func (m model) Init() tea.Cmd {
	if m.cliMode {
		// Auto-start conversion in CLI mode
		return m.startConversionCmd()
	}
	return textinput.Blink
}

func (m model) startConversionCmd() tea.Cmd {
	root := strings.TrimSpace(m.inputs[0].Value())
	out := strings.TrimSpace(m.inputs[1].Value())
	voice := strings.TrimSpace(m.inputs[2].Value())
	if voice == "" {
		voice = "alloy"
	}

	cfg := appConfig{
		Root:           root,
		Out:            out,
		Voice:          voice,
		Model:          "tts-1-hd-1106",
		ResponseFormat: "aac",
		Speed:          1.0,
		Overwrite:      m.overwrite,
		Instructions:   envOr("OPENAI_TTS_INSTRUCTIONS", "Speak clearly for podcast listening."),
		APIKey:         strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		Pattern:        "*.md",
	}

	return prepareConversionCmd(cfg)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)
	case preparedMsg:
		m.state = stateRunning
		m.cfg = msg.cfg
		m.jobs = msg.jobs
		m.currentIdx = 0
		m.summary = summaryCounts{}
		m.currentChunk = "waiting…"
		m.lastError = ""
		m.tasks = make(map[string]taskStatus)

		// Set up error log file if not already set
		if m.logFile == nil {
			cwd, _ := os.Getwd()
			logPath := filepath.Join(cwd, "logs", "markloud_errors.log")
			_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
			if logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
				m.logFile = logFile
				m.logPath = logPath
				fmt.Fprintf(logFile, "\n=== MarkLoud run %s ===\n", time.Now().Format(time.RFC3339))
			}
		}

		workers := runtime.NumCPU() - 2
		if workers < 1 {
			workers = 1
		}
		m.workerSem = make(chan struct{}, workers)
		m.chunkCh = make(chan chunkMsg, 100)

		cmds := []tea.Cmd{m.spin.Tick, listenChunks(m.chunkCh)}
		for idx, job := range msg.jobs {
			cmds = append(cmds, runJobCmd(msg.cfg, job, idx, m.workerSem, m.chunkCh))
		}
		return m, tea.Batch(cmds...)
	case prepareFailedMsg:
		m.err = msg.err
		m.message = ""
		// In CLI mode, show error and quit
		if m.cliMode {
			m.state = stateError
			return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return tea.Quit() })
		}
		m.state = stateConfig
		return m, nil
	case fileDoneMsg:
		m.applyResult(msg)
		if m.currentIdx >= len(m.jobs) {
			return m, func() tea.Msg { return allDoneMsg{} }
		}
		return m, nil
	case allDoneMsg:
		m.state = stateDone
		m.message = "Conversion finished."
		if m.logFile != nil {
			fmt.Fprintf(m.logFile, "=== run finished %s ===\n", time.Now().Format(time.RFC3339))
			m.logFile.Close()
			m.logFile = nil
		}
		// Auto-quit in CLI mode after brief display
		if m.cliMode {
			return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return tea.Quit() })
		}
		return m, nil
	case chunkMsg:
		key := msg.job.RelPath
		ts := m.tasks[key]
		ts.name = key
		ts.idx = msg.idx
		ts.total = msg.total
		if msg.done {
			if msg.err != nil {
				ts.status = "error"
				ts.err = msg.err
				m.lastError = msg.err.Error()
			} else {
				ts.status = "done"
			}
		} else {
			ts.status = "running"
		}
		m.tasks[key] = ts

		m.currentChunk = fmt.Sprintf("%s (%d/%d)", msg.job.RelPath, msg.idx, msg.total)
		// Only re-listen if still running (prevents goroutine leak after completion)
		if m.state == stateRunning {
			return m, listenChunks(m.chunkCh)
		}
		return m, nil
	case spinner.TickMsg:
		if m.state == stateRunning {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			return m, cmd
		}
	}
	return m.updateInputs(msg)
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.state {
	case stateConfig:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "tab", "shift+tab", "up", "down":
			m.focusIndex = nextFocus(msg.String(), m.focusIndex, len(m.inputs))
			for i := range m.inputs {
				if i == m.focusIndex {
					m.inputs[i].Focus()
					m.inputs[i].PromptStyle = focusedStyle
					m.inputs[i].TextStyle = focusedStyle
				} else {
					m.inputs[i].Blur()
					m.inputs[i].PromptStyle = noStyle
					m.inputs[i].TextStyle = noStyle
				}
			}
			return m, nil
		case "o":
			// 'o' toggles overwrite
			m.overwrite = !m.overwrite
			return m, nil
		case "enter":
			return m.startConversion()
		default:
			// let text inputs handle normal typing/paste
			return m.updateInputs(msg)
		}
	case stateRunning:
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			return m, tea.Quit
		}
	case stateDone, stateError:
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			return m, tea.Quit
		}
		if msg.String() == "enter" {
			m.state = stateConfig
			m.err = nil
			m.message = ""
			return m, textinput.Blink
		}
	}
	return m, nil
}

func (m model) updateInputs(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.state != stateConfig {
		return m, nil
	}

	var cmds []tea.Cmd
	for i := range m.inputs {
		var cmd tea.Cmd
		m.inputs[i], cmd = m.inputs[i].Update(msg)
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

func nextFocus(key string, current, total int) int {
	switch key {
	case "tab", "down", "enter":
		current++
	case "shift+tab", "up":
		current--
	}
	if current < 0 {
		current = total - 1
	}
	if current >= total {
		current = 0
	}
	return current
}

func (m model) startConversion() (tea.Model, tea.Cmd) {
	root := strings.TrimSpace(m.inputs[0].Value())
	out := strings.TrimSpace(m.inputs[1].Value())
	voice := strings.TrimSpace(m.inputs[2].Value())
	if voice == "" {
		voice = "alloy"
	}

	cwd, _ := os.Getwd()
	logPath := filepath.Join(cwd, "markloud_errors.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		m.err = fmt.Errorf("failed to open log file: %w", err)
		return m, nil
	}
	fmt.Fprintf(logFile, "\n=== MarkLoud run %s ===\n", time.Now().Format(time.RFC3339))

	cfg := appConfig{
		Root:           root,
		Out:            out,
		Voice:          voice,
		Model:          "tts-1-hd-1106",
		ResponseFormat: "aac",
		Speed:          1.0,
		Overwrite:      m.overwrite,
		Instructions:   envOr("OPENAI_TTS_INSTRUCTIONS", "Speak clearly for podcast listening."),
		APIKey:         strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		Pattern:        "*.md",
	}

	m.err = nil
	m.message = "Preparing files…"
	m.logFile = logFile
	m.logPath = logPath
	return m, prepareConversionCmd(cfg)
}

func prepareConversionCmd(cfg appConfig) tea.Cmd {
	return func() tea.Msg {
		if cfg.APIKey == "" {
			return prepareFailedMsg{errors.New("OPENAI_API_KEY is not set")}
		}
		info, err := os.Stat(cfg.Root)
		if err != nil || !info.IsDir() {
			return prepareFailedMsg{fmt.Errorf("input directory not found: %s", cfg.Root)}
		}
		jobs, err := collectMarkdownFiles(cfg.Root, cfg.Out, cfg.Pattern, cfg.ResponseFormat)
		if err != nil {
			return prepareFailedMsg{err}
		}
		if len(jobs) == 0 {
			return prepareFailedMsg{fmt.Errorf("no markdown files matching %s", cfg.Pattern)}
		}
		return preparedMsg{cfg: cfg, jobs: jobs}
	}
}

func runJobCmd(cfg appConfig, job fileJob, idx int, sem chan struct{}, chunkCh chan<- chunkMsg) tea.Cmd {
	return func() tea.Msg {
		sem <- struct{}{}
		defer func() { <-sem }()

		ctx := context.Background()
		res := processFile(ctx, job, cfg, func(cur, total int) {
			chunkCh <- chunkMsg{job: job, idx: cur, total: total}
		})
		chunkCh <- chunkMsg{job: job, idx: res.Chunks, total: res.Chunks, done: true, err: res.Err}
		return fileDoneMsg{idx: idx, res: res, job: job}
	}
}

func listenChunks(ch <-chan chunkMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func (m *model) applyResult(msg fileDoneMsg) {
	ts := m.tasks[msg.job.RelPath]
	ts.name = msg.job.RelPath
	switch msg.res.Status {
	case jobDone:
		m.summary.Done++
		ts.status = "done"
	case jobSkipped:
		m.summary.Skipped++
		m.currentChunk = fmt.Sprintf("%s (skipped)", msg.job.RelPath)
		ts.status = "skipped"
	case jobEmpty:
		m.summary.Empty++
		m.currentChunk = fmt.Sprintf("%s (empty)", msg.job.RelPath)
		ts.status = "empty"
	case jobFailed:
		m.summary.Failed++
		ts.status = "error"
		ts.err = msg.res.Err
	}
	m.tasks[msg.job.RelPath] = ts
	m.currentIdx++
	if msg.res.Err != nil {
		m.currentChunk = fmt.Sprintf("%s (error)", msg.job.RelPath)
		m.lastError = msg.res.Err.Error()
		m.logf("ERROR %s: %v\n", msg.job.RelPath, msg.res.Err)
	}
}

func (m model) View() string {
	switch m.state {
	case stateConfig:
		return m.viewConfig()
	case stateRunning:
		return m.viewRunning()
	case stateDone:
		return m.viewDone()
	case stateError:
		return m.viewError()
	default:
		return ""
	}
}

var (
	titleStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#9A7FF0")).Bold(true).MarginBottom(1)
	labelStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#888"))
	valueStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#E6E6E6"))
	focusedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#D7C8FF"))
	noStyle      = lipgloss.NewStyle()
	boxStyle     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2).BorderForeground(lipgloss.Color("#6B5B95"))
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#9BE564")).Bold(true)
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF8BA7")).Bold(true)
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#666"))
	emphStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#F6D186")).Bold(true)
	keyStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#A0E7E5"))
	counterStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFE156")).Bold(true)
)

func (m model) viewConfig() string {
	rows := []string{
		titleStyle.Render("MarkLoud ▸ Markdown → AAC (OpenAI)"),
		fmt.Sprintf("%s %s", labelStyle.Render("API key:"), presentMissing(os.Getenv("OPENAI_API_KEY"))),
		"",
		fmt.Sprintf("%s\n%s", labelStyle.Render("Input directory"), m.inputs[0].View()),
		fmt.Sprintf("%s\n%s", labelStyle.Render("Output directory"), m.inputs[1].View()),
		fmt.Sprintf("%s\n%s", labelStyle.Render("Voice"), m.inputs[2].View()),
		fmt.Sprintf("%s %s", labelStyle.Render("Overwrite existing [o]:"), boolBadge(m.overwrite)),
	}

	if m.err != nil {
		rows = append(rows, errorStyle.Render(m.err.Error()))
	}
	if m.message != "" {
		rows = append(rows, dimStyle.Render(m.message))
	}

	rows = append(rows, dimStyle.Render("tab/shift+tab to move · enter to start · o to toggle overwrite · q to quit"))

	return boxStyle.Width(76).Render(strings.Join(rows, "\n"))
}

func presentMissing(v string) string {
	if strings.TrimSpace(v) == "" {
		return errorStyle.Render("missing")
	}
	return successStyle.Render("found")
}

func boolBadge(v bool) string {
	if v {
		return successStyle.Render("ON")
	}
	return dimStyle.Render("off")
}

func (m model) renderActive() []string {
	lines := []string{}
	names := make([]string, 0, len(m.tasks))
	for k := range m.tasks {
		names = append(names, k)
	}
	sort.Strings(names)
	for i, name := range names {
		if i >= 6 {
			break
		}
		ts := m.tasks[name]
		progress := progressBar(ts.idx, ts.total, 24)
		state := ""
		switch ts.status {
		case "running":
			state = dimStyle.Render("running")
		case "done":
			state = successStyle.Render("done")
		case "error":
			state = errorStyle.Render("error (see log)")
		case "skipped":
			state = dimStyle.Render("skipped")
		case "empty":
			state = dimStyle.Render("empty")
		}
		line := fmt.Sprintf("%s %s %s %s", valueStyle.Render("•"), progress, valueStyle.Render(ts.name), labelStyle.Render(state))
		lines = append(lines, line)
	}
	return lines
}

func progressBar(cur, total, width int) string {
	if total <= 0 {
		total = 1
	}
	if width < 4 {
		width = 4
	}
	ratio := float64(cur) / float64(total)
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	filled := int(ratio * float64(width))
	if filled > width {
		filled = width
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return valueStyle.Render("[" + bar + fmt.Sprintf(" %d/%d]", cur, total))
}

func (m *model) logf(format string, args ...any) {
	if m.logFile == nil {
		return
	}
	m.logMu.Lock()
	defer m.logMu.Unlock()
	fmt.Fprintf(m.logFile, format, args...)
}

func (m model) viewRunning() string {
	total := len(m.jobs)
	current := m.currentIdx
	bar := fmt.Sprintf("%s %d/%d files", m.spin.View(), current, total)

	summary := fmt.Sprintf("%s %d  %s %d  %s %d  %s %d",
		counterStyle.Render("done"), m.summary.Done,
		labelStyle.Render("skipped"), m.summary.Skipped,
		labelStyle.Render("empty"), m.summary.Empty,
		errorStyle.Render("failed"), m.summary.Failed,
	)

	lines := []string{
		titleStyle.Render("Synthesizing…"),
		bar,
		summary,
		"",
		labelStyle.Render("Active files:"),
	}

	active := m.renderActive()
	if len(active) == 0 {
		lines = append(lines, dimStyle.Render("waiting for work…"))
	} else {
		lines = append(lines, active...)
	}

	if m.lastError != "" {
		lines = append(lines, "", errorStyle.Render("Last error (see log)"))
	}
	if m.logPath != "" {
		lines = append(lines, dimStyle.Render("Log: "+m.logPath))
	}

	lines = append(lines, "", dimStyle.Render("ctrl+c or q to abort"))
	return boxStyle.Width(76).Render(strings.Join(lines, "\n"))
}

func (m model) viewDone() string {
	lines := []string{
		titleStyle.Render("All done!"),
		fmt.Sprintf("%s %d · %s %d · %s %d · %s %d",
			counterStyle.Render("written"), m.summary.Done,
			labelStyle.Render("skipped"), m.summary.Skipped,
			labelStyle.Render("empty"), m.summary.Empty,
			errorStyle.Render("failed"), m.summary.Failed,
		),
		fmt.Sprintf("%s %s", labelStyle.Render("Output"), valueStyle.Render(filepath.Clean(m.cfg.Out))),
		"",
		emphStyle.Render("Press enter to run again, q to quit."),
	}
	if m.summary.Failed > 0 && m.logPath != "" {
		lines = append(lines, errorStyle.Render("Errors logged to: "+m.logPath))
	}
	return boxStyle.Width(76).Render(strings.Join(lines, "\n"))
}

func (m model) viewError() string {
	lines := []string{
		errorStyle.Render("Error"),
		m.err.Error(),
		"",
		emphStyle.Render("Press enter to go back or q to quit."),
	}
	return boxStyle.Width(76).Render(strings.Join(lines, "\n"))
}

func main() {
	_ = godotenv.Load()

	// CLI flags
	inputDir := flag.String("i", "", "Input directory containing markdown files")
	outputDir := flag.String("o", "", "Output directory for audio files")
	voice := flag.String("voice", envOr("OPENAI_TTS_VOICE", "alloy"), "TTS voice (alloy, echo, fable, onyx, nova, shimmer)")
	overwrite := flag.Bool("overwrite", false, "Overwrite existing audio files")
	flag.Parse()

	var opts *cliOptions
	if *inputDir != "" {
		if *outputDir == "" {
			*outputDir = "./audio_out"
		}
		opts = &cliOptions{
			inputDir:  *inputDir,
			outputDir: *outputDir,
			voice:     *voice,
			overwrite: *overwrite,
		}
	}

	p := tea.NewProgram(initialModel(opts), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
}
