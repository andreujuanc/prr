package config

import (
	"encoding/json"
	"os"

	"github.com/andreujuanc/prr/internal/pipe"
)

// LoadPipeTargets reads pipe targets from the config file.
// Returns nil (not an error) if the config doesn't exist or has no pipes configured.
func LoadPipeTargets() []pipe.Target {
	path, err := DefaultConfigPath()
	if err != nil {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var cfg struct {
		Pipes []pipe.Target `json:"pipes"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}

	return cfg.Pipes
}
