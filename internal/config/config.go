// Package config provides shared configuration loading utilities for hub and worker.
package config

import (
	"flag"
	"os"
	"path/filepath"
	"strings"

	"github.com/knadh/koanf/v2"
)

// ExtractConfigFlag pre-scans args for -config/--config before full flag parsing.
// Returns the config file path found, or defaultPath if not specified.
func ExtractConfigFlag(args []string, defaultPath string) string {
	for i, arg := range args {
		// Handle -config=value or --config=value
		if strings.HasPrefix(arg, "-config=") || strings.HasPrefix(arg, "--config=") {
			_, val, _ := strings.Cut(arg, "=")
			return val
		}
		// Handle -config value or --config value
		if (arg == "-config" || arg == "--config") && i+1 < len(args) {
			return args[i+1]
		}
	}
	return defaultPath
}

// FlagProvider is a koanf.Provider that reads only explicitly-set flags from a FlagSet.
// Unlike basicflag, it uses fs.Visit (not fs.VisitAll) so default values are not loaded.
type FlagProvider struct {
	fs       *flag.FlagSet
	fieldMap map[string]string // flag name -> koanf key (e.g. "data-dir" -> "data_dir")
}

// NewFlagProvider creates a provider that maps explicitly-set CLI flags to koanf keys.
// fieldMap maps flag names (with hyphens) to koanf keys (with underscores).
func NewFlagProvider(fs *flag.FlagSet, fieldMap map[string]string) *FlagProvider {
	return &FlagProvider{fs: fs, fieldMap: fieldMap}
}

// ReadBytes is not supported.
func (f *FlagProvider) ReadBytes() ([]byte, error) {
	return nil, nil
}

// Read returns a map of only explicitly-set flags mapped to their koanf keys.
func (f *FlagProvider) Read() (map[string]interface{}, error) {
	out := make(map[string]interface{})
	f.fs.Visit(func(fl *flag.Flag) {
		key, ok := f.fieldMap[fl.Name]
		if !ok {
			return
		}
		out[key] = fl.Value.String()
	})
	return out, nil
}

// ResolveDataDir resolves a relative data_dir against the config file's directory.
// If the config file was not loaded (doesn't exist), resolves against defaultConfigDir.
func ResolveDataDir(dataDir, configFilePath, defaultConfigDir string) string {
	dataDir = ExpandHome(dataDir)

	if filepath.IsAbs(dataDir) {
		return dataDir
	}

	// Determine the base directory for resolution.
	var baseDir string
	if configFilePath != "" {
		if info, err := os.Stat(configFilePath); err == nil && !info.IsDir() {
			baseDir = filepath.Dir(configFilePath)
		} else {
			baseDir = ExpandHome(defaultConfigDir)
		}
	} else {
		baseDir = ExpandHome(defaultConfigDir)
	}

	return filepath.Join(baseDir, dataDir)
}

// ExpandHome expands a leading ~ in a path to the user's home directory.
func ExpandHome(path string) string {
	if path == "" {
		return path
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[1:])
	}
	return path
}

// Load is a helper that performs the standard koanf loading sequence:
// defaults -> config file -> env vars -> CLI flags.
// It returns the loaded koanf instance. The caller should unmarshal into their config struct.
func Load(k *koanf.Koanf, defaults map[string]interface{}, configFilePath, envPrefix string, fp *FlagProvider) error {
	// 1. Defaults.
	if err := k.Load(confmapProvider(defaults), nil); err != nil {
		return err
	}

	// 2. Config file (optional, silent if missing).
	configFilePath = ExpandHome(configFilePath)
	if _, err := os.Stat(configFilePath); err == nil {
		if err := k.Load(fileProvider(configFilePath), yamlParser()); err != nil {
			return err
		}
	}

	// 3. Env vars.
	if err := k.Load(envProvider(envPrefix), nil); err != nil {
		return err
	}

	// 4. CLI flags (only explicitly-set).
	if err := k.Load(fp, nil); err != nil {
		return err
	}

	return nil
}
