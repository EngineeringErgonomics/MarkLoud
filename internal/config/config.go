package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// Config holds user preferences persisted between runs.
type Config struct {
	InputDir     string  `json:"input_dir"`
	OutputDir    string  `json:"output_dir"`
	Voice        string  `json:"voice"`
	Speed        float64 `json:"speed"`
	Instructions string  `json:"instructions"`
	Overwrite    bool    `json:"overwrite"`
}

// Default returns a Config with sensible first-run defaults.
func Default() Config {
	return Config{
		InputDir:     "./notes",
		OutputDir:    "./audio_out",
		Voice:        "alloy",
		Speed:        1.0,
		Instructions: "Speak clearly for podcast listening.",
		Overwrite:    false,
	}
}

// Path returns the platform-appropriate config file path.
func Path() (string, error) {
	switch runtime.GOOS {
	case "windows":
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "markloud", "config.json"), nil
		}
	case "darwin":
		if home := os.Getenv("HOME"); home != "" {
			return filepath.Join(home, ".config", "markloud", "config.json"), nil
		}
	default:
		if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
			return filepath.Join(xdgConfig, "markloud", "config.json"), nil
		}
		if home := os.Getenv("HOME"); home != "" {
			return filepath.Join(home, ".config", "markloud", "config.json"), nil
		}
	}
	return "", nil
}

// Load reads the config file. If the file does not exist, returns Default.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Default(), err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), nil
		}
		return Default(), err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Default(), err
	}
	return cfg, nil
}

// Save writes cfg to the platform-appropriate config file.
func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
