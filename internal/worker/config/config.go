package config

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds the worker's runtime configuration.
type Config struct {
	HubURL  string `json:"hub_url"`  // Hub server URL (e.g. "http://localhost:4327") or "unix:<socket-path>"
	DataDir string `json:"data_dir"` // Directory for persistent state
}

// State holds the worker's persistent state (saved to disk after registration).
type State struct {
	WorkerID  string `json:"worker_id"`
	AuthToken string `json:"auth_token"`
	OrgID     string `json:"org_id"`
}

// DefineFlags registers command-line flags for worker configuration.
// Call flag.Parse() separately after defining all flags.
func DefineFlags() *Config {
	c := &Config{}
	flag.StringVar(&c.HubURL, "hub", "http://localhost:4327", "Hub server URL or unix:<socket-path>")
	flag.StringVar(&c.DataDir, "data-dir", defaultDataDir(), "data directory")
	return c
}

// Validate checks the configuration and ensures required directories exist.
func (c *Config) Validate() error {
	if c.HubURL == "" {
		return fmt.Errorf("hub URL is required")
	}

	// Ensure data dir exists.
	if err := os.MkdirAll(c.DataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	return nil
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "leapmux", "worker")
	}
	return filepath.Join(home, ".config", "leapmux", "worker")
}

// StatePath returns the path to the state file.
func (c *Config) StatePath() string {
	return filepath.Join(c.DataDir, "state.json")
}

// LoadState loads persisted state from disk. Returns nil if no state file exists.
func (c *Config) LoadState() (*State, error) {
	data, err := os.ReadFile(c.StatePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ClearState removes the persisted state file.
func (c *Config) ClearState() error {
	return os.Remove(c.StatePath())
}

// SaveState persists state to disk.
func (c *Config) SaveState(s *State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.StatePath(), data, 0o600)
}
