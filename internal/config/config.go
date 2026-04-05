package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Provider       string
	APIKey         string
	Model          string
	BaseURL        string
	Context7APIKey string
}

func Path() string {
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".forge", "config")
}

func Load() (Config, error) {
	path := Path()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}

	var cfg Config
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		switch key {
		case "FORGE_PROVIDER":
			cfg.Provider = value
		case "FORGE_API_KEY":
			cfg.APIKey = value
		case "FORGE_MODEL":
			cfg.Model = value
		case "FORGE_BASE_URL", "FORGE_API_URL":
			cfg.BaseURL = value
		case "CONTEXT7_API_KEY":
			cfg.Context7APIKey = value
		}
	}
	return cfg, nil
}

func Save(cfg Config) error {
	path := Path()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	var lines []string
	if cfg.Provider != "" {
		lines = append(lines, fmt.Sprintf("FORGE_PROVIDER=%s", cfg.Provider))
	}
	if cfg.APIKey != "" {
		lines = append(lines, fmt.Sprintf("FORGE_API_KEY=%s", cfg.APIKey))
	}
	if cfg.Model != "" {
		lines = append(lines, fmt.Sprintf("FORGE_MODEL=%s", cfg.Model))
	}
	if cfg.BaseURL != "" {
		lines = append(lines, fmt.Sprintf("FORGE_BASE_URL=%s", cfg.BaseURL))
	}
	if cfg.Context7APIKey != "" {
		lines = append(lines, fmt.Sprintf("CONTEXT7_API_KEY=%s", cfg.Context7APIKey))
	}

	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

func SetEnvFromFile() error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	if cfg.Provider != "" {
		os.Setenv("FORGE_PROVIDER", cfg.Provider)
	}
	if cfg.APIKey != "" {
		os.Setenv("FORGE_API_KEY", cfg.APIKey)
	}
	if cfg.Model != "" {
		os.Setenv("FORGE_MODEL", cfg.Model)
	}
	if cfg.BaseURL != "" {
		os.Setenv("FORGE_BASE_URL", cfg.BaseURL)
		os.Setenv("FORGE_API_URL", cfg.BaseURL) // legacy compatibility for older paths
	}
	if cfg.Context7APIKey != "" {
		os.Setenv("CONTEXT7_API_KEY", cfg.Context7APIKey)
	}
	return nil
}
