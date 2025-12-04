package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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
}

type allDoneMsg struct{}

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

	workerSem chan struct{}
	chunkCh   chan chunkMsg

	spin spinner.Model
}

func initialModel() model {
	_ = godotenv.Load()

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

	return model{
		state:      stateConfig,
		inputs:     inputs,
		focusIndex: 0,
		overwrite:  false,
		message:    "",
		err:        nil,
		spin:       spin,
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
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

		workers := runtime.NumCPU() - 2
		if workers < 1 {
			workers = 1
		}
		m.workerSem = make(chan struct{}, workers)
		m.chunkCh = make(chan chunkMsg)

		cmds := []tea.Cmd{m.spin.Tick, listenChunks(m.chunkCh)}
		for idx, job := range msg.jobs {
			cmds = append(cmds, runJobCmd(msg.cfg, job, idx, m.workerSem, m.chunkCh))
		}
		return m, tea.Batch(cmds...)
	case prepareFailedMsg:
		m.state = stateConfig
		m.err = msg.err
		m.message = ""
		return m, nil
	case fileDoneMsg:
		m.applyResult(msg)
		if m.currentIdx >= len(m.jobs) {
			return m, func() tea.Msg { return allDoneMsg{} }
		}
		return m, nil
	case allDoneMsg:
		if m.chunkCh != nil {
			close(m.chunkCh)
			m.chunkCh = nil
		}
		m.state = stateDone
		m.message = "Conversion finished."
		return m, nil
	case chunkMsg:
		m.currentChunk = fmt.Sprintf("%s (%d/%d)", msg.job.RelPath, msg.idx, msg.total)
		return m, listenChunks(m.chunkCh)
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
		case " ":
			// space toggles overwrite
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
	switch msg.res.Status {
	case jobDone:
		m.summary.Done++
	case jobSkipped:
		m.summary.Skipped++
		m.currentChunk = fmt.Sprintf("%s (skipped)", msg.job.RelPath)
	case jobEmpty:
		m.summary.Empty++
		m.currentChunk = fmt.Sprintf("%s (empty)", msg.job.RelPath)
	case jobFailed:
		m.summary.Failed++
	}
	m.currentIdx++
	if msg.res.Err != nil {
		m.currentChunk = fmt.Sprintf("%s (error)", msg.job.RelPath)
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
		fmt.Sprintf("%s %s", labelStyle.Render("Overwrite existing [space]:"), boolBadge(m.overwrite)),
	}

	if m.err != nil {
		rows = append(rows, errorStyle.Render(m.err.Error()))
	}
	if m.message != "" {
		rows = append(rows, dimStyle.Render(m.message))
	}

	rows = append(rows, dimStyle.Render("tab/shift+tab to move · enter to start · space to toggle overwrite · q to quit"))

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
		labelStyle.Render("Current chunk:"),
	}
	lines = append(lines, valueStyle.Render(m.currentChunk))

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
	if m.summary.Failed > 0 {
		lines = append(lines, errorStyle.Render("Check logs above for failed files."))
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
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
}
