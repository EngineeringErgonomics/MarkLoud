package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	Speed float64 `json:"speed"`
}

const (
	MinSpeed     = 0.25
	MaxSpeed     = 4.0
	DefaultSpeed = 1.0
)

func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "markloud", "config.json"), nil
}

func Load() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{Speed: DefaultSpeed}, nil
		}
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	if cfg.Speed < MinSpeed {
		cfg.Speed = MinSpeed
	}
	if cfg.Speed > MaxSpeed {
		cfg.Speed = MaxSpeed
	}

	return &cfg, nil
}

func Save(cfg *Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}

	if cfg.Speed < MinSpeed {
		cfg.Speed = MinSpeed
	}
	if cfg.Speed > MaxSpeed {
		cfg.Speed = MaxSpeed
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	return os.WriteFile(path, data, 0o644)
}
