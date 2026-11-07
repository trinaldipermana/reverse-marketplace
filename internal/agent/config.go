package agent

import (
	"errors"
	"os"
	"strings"
	"time"
)

type Config struct {
	APIKey         string
	Model          string
	RequestTimeout time.Duration
}

// ConfigFromEnv reads exported environment variables, not dotenv files.
func ConfigFromEnv() (Config, error) {
	cfg := Config{APIKey: strings.TrimSpace(os.Getenv("OPENAI_API_KEY")), Model: strings.TrimSpace(os.Getenv("OPENAI_MODEL"))}
	if cfg.Model == "" {
		cfg.Model = "gpt-6.1-sol"
	}
	return normalizeConfig(cfg)
}

func normalizeConfig(cfg Config) (Config, error) {
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.Model = strings.TrimSpace(cfg.Model)
	if cfg.APIKey == "" {
		return Config{}, errors.New("OPENAI_API_KEY is required")
	}
	if cfg.Model == "" {
		return Config{}, errors.New("OPENAI_MODEL is required")
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 20 * time.Second
	}
	if cfg.RequestTimeout < 0 {
		return Config{}, errors.New("request timeout must be positive")
	}
	return cfg, nil
}
