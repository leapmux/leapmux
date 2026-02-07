package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds the hub's runtime configuration.
type Config struct {
	Addr        string // Listen address (e.g. ":4327")
	DataDir     string // Data directory for DB, socket, etc.
	DevFrontend string // Vite dev server URL (dev mode only; empty for production)
}

// DefineFlags registers command-line flags for hub configuration.
// Call flag.Parse() separately after defining all flags.
func DefineFlags() *Config {
	c := &Config{}
	flag.StringVar(&c.Addr, "addr", ":4327", "listen address")
	flag.StringVar(&c.DataDir, "data-dir", defaultDataDir(), "data directory")
	flag.StringVar(&c.DevFrontend, "dev-frontend", "", "Vite dev server URL for reverse proxy (dev mode only)")
	return c
}

// Validate checks the configuration values and ensures required directories exist.
func (c *Config) Validate() error {
	if c.Addr == "" {
		return fmt.Errorf("addr is required")
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
		return filepath.Join(".config", "leapmux", "hub")
	}
	return filepath.Join(home, ".config", "leapmux", "hub")
}

// DBPath returns the path to the SQLite database file.
func (c *Config) DBPath() string {
	return filepath.Join(c.DataDir, "hub.db")
}

// SocketPath returns the path to the Unix domain socket.
func (c *Config) SocketPath() string {
	return filepath.Join(c.DataDir, "hub.sock")
}
