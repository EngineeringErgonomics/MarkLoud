package convert

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// RunState represents the persisted state of a conversion run.
type RunState struct {
	// RunID is a unique identifier for this run (directory hash + timestamp)
	RunID string `json:"run_id"`
	// Root is the input directory for this run
	Root string `json:"root"`
	// Out is the output directory for this run
	Out string `json:"out"`
	// Voice is the TTS voice used
	Voice string `json:"voice"`
	// Pattern is the file pattern used
	Pattern string `json:"pattern"`
	// ResponseFormat is the audio format
	ResponseFormat string `json:"response_format"`
	// Files maps absolute file paths to their conversion state
	Files map[string]FileState `json:"files"`
}

// FileState represents the conversion state of a single file.
type FileState struct {
	AbsPath         string     `json:"abs_path"`
	RelPath         string     `json:"rel_path"`
	DestPath        string     `json:"dest_path"`
	TotalChunks     int        `json:"total_chunks"`
	CompletedChunks []int      `json:"completed_chunks"`
	Status          JobOutcome `json:"status"`
}

// IsChunkComplete checks if a specific chunk index has been completed.
func (fs *FileState) IsChunkComplete(idx int) bool {
	for _, c := range fs.CompletedChunks {
		if c == idx {
			return true
		}
	}
	return false
}

// MarkChunkDone marks a chunk as completed.
func (fs *FileState) MarkChunkDone(idx int) {
	if !fs.IsChunkComplete(idx) {
		fs.CompletedChunks = append(fs.CompletedChunks, idx)
	}
}

// IsComplete returns true if all chunks have been completed or if status is final.
func (fs *FileState) IsComplete() bool {
	if fs.Status == JobDone || fs.Status == JobSkipped || fs.Status == JobEmpty {
		return true
	}
	if fs.TotalChunks > 0 && len(fs.CompletedChunks) >= fs.TotalChunks {
		return true
	}
	return false
}

// StateManager handles loading, saving, and managing the conversion state.
type StateManager struct {
	statePath string
	state     *RunState
	mu        sync.RWMutex
}

// NewStateManager creates a new state manager with the given path.
func NewStateManager(statePath string) *StateManager {
	return &StateManager{
		statePath: statePath,
	}
}

// LoadState loads the state from disk. Returns nil if no state exists or if the file is corrupted.
func (sm *StateManager) LoadState() (*RunState, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	data, err := os.ReadFile(sm.statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var state RunState
	if err := json.Unmarshal(data, &state); err != nil {
		// Corrupted state file - treat as no state
		return nil, nil
	}

	sm.state = &state
	return &state, nil
}

// SaveState saves the current state to disk.
func (sm *StateManager) SaveState(state *RunState) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	data, err := json.Marshal(state)
	if err != nil {
		return err
	}

	// Ensure directory exists
	dir := filepath.Dir(sm.statePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	if err := os.WriteFile(sm.statePath, data, 0o644); err != nil {
		return err
	}

	sm.state = state
	return nil
}

// CleanupState removes the state file on clean completion.
func (sm *StateManager) CleanupState() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.state != nil {
		sm.state = nil
	}

	if _, err := os.Stat(sm.statePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	return os.Remove(sm.statePath)
}

// GetStatePath returns the default state file path (~/.markloud/state.json).
func GetStatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".markloud", "state.json"), nil
}

// LoadOrCreateState loads existing state or returns nil for a new run.
func LoadOrCreateState(root, out, voice, pattern, responseFormat string) (*StateManager, *RunState, error) {
	statePath, err := GetStatePath()
	if err != nil {
		return nil, nil, err
	}

	sm := NewStateManager(statePath)
	state, err := sm.LoadState()
	if err != nil {
		return nil, nil, err
	}

	// Check if existing state matches the current run configuration
	if state != nil {
		if state.Root != root || state.Out != out || state.Voice != voice || state.Pattern != pattern {
			// Different run configuration - treat as no state
			return sm, nil, nil
		}
	}

	return sm, state, nil
}
