package control

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/util/atomicfile"
)

const contextFileName = "context.json"

// ContextConfig stores persistent CLI defaults used outside a LeapMux tab
// when explicit flags and environment variables are absent.
type ContextConfig struct {
	Hub       string `json:"hub,omitempty"`
	Worker    string `json:"worker,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Provider  string `json:"provider,omitempty"`
}

// IsEmpty reports whether no default field is set.
func (c ContextConfig) IsEmpty() bool {
	return c.Hub == "" && c.Worker == "" && c.Workspace == "" && c.Provider == ""
}

// ContextPath returns the path to context.json under ConfigDir.
func ContextPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, contextFileName), nil
}

// LoadContext reads context.json. An absent file returns a zero ContextConfig
// and no error.
func LoadContext() (ContextConfig, error) {
	path, err := ContextPath()
	if err != nil {
		return ContextConfig{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ContextConfig{}, nil
		}
		return ContextConfig{}, err
	}
	var cfg ContextConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ContextConfig{}, fmt.Errorf("parse context %s: %w", path, err)
	}
	cfg.Hub = strings.TrimSpace(cfg.Hub)
	cfg.Worker = strings.TrimSpace(cfg.Worker)
	cfg.Workspace = strings.TrimSpace(cfg.Workspace)
	cfg.Provider = strings.TrimSpace(cfg.Provider)
	return cfg, nil
}

// SaveContext writes context.json atomically (mode 0600), or removes it when
// every field is empty.
func SaveContext(cfg ContextConfig) error {
	cfg.Hub = strings.TrimSpace(cfg.Hub)
	cfg.Worker = strings.TrimSpace(cfg.Worker)
	cfg.Workspace = strings.TrimSpace(cfg.Workspace)
	cfg.Provider = strings.TrimSpace(cfg.Provider)
	if cfg.IsEmpty() {
		return ClearContext()
	}
	path, err := ContextPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, data, 0o600)
}

// ClearContext deletes context.json if present.
func ClearContext() error {
	path, err := ContextPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return atomicfile.RemoveTempFiles(path)
}
