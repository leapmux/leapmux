// Package config provides shared configuration loading utilities for hub and worker.
package config

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/knadh/koanf/v2"
)

// IsHelpArg reports whether arg is one of the recognized help tokens.
func IsHelpArg(arg string) bool {
	return arg == "-h" || arg == "-help" || arg == "--help" || arg == "help"
}

// HasHelpArg reports whether any arg is a recognized help token.
func HasHelpArg(args []string) bool {
	for _, arg := range args {
		if IsHelpArg(arg) {
			return true
		}
	}
	return false
}

// RejectPositionalArgs returns an error if fs has any non-flag args remaining
// after Parse. Use it to fail fast on `command unexpected` invocations rather
// than silently ignoring trailing tokens.
func RejectPositionalArgs(fs *flag.FlagSet) error {
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument: %q (use --help for usage)", fs.Arg(0))
	}
	return nil
}

// ConfigureAndParse routes help output to stdout when --help is requested,
// installs a categorized fs.Usage callback, parses args, and rejects extra
// positional arguments. Pass nil categories/categoryOrder for a flat
// "Options:" section. Call after all flag definitions are registered.
func ConfigureAndParse(fs *flag.FlagSet, args []string, description string, categories map[string]string, categoryOrder []string) error {
	if HasHelpArg(args) {
		fs.SetOutput(os.Stdout)
	}
	fs.Usage = func() {
		PrintFlagUsage(fs, description, categories, categoryOrder)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	return RejectPositionalArgs(fs)
}

// PrintFlagUsage writes a help message for fs to fs.Output(). When categories
// is nil, flags are printed in a single "Options" section using the standard
// flag package format. Otherwise, flags are grouped by their category from the
// map (flag name -> category name); empty/missing categories fall under
// "Options". Categories listed in categoryOrder appear in that order;
// categories not listed are appended after.
func PrintFlagUsage(fs *flag.FlagSet, description string, categories map[string]string, categoryOrder []string) {
	if description != "" {
		_, _ = fmt.Fprintf(fs.Output(), "%s\n\n", description)
	}
	_, _ = fmt.Fprintf(fs.Output(), "Usage: %s [flags]\n", fs.Name())
	if categories == nil {
		_, _ = fmt.Fprint(fs.Output(), "\nOptions:\n")
		fs.PrintDefaults()
		return
	}
	seen := make(map[string]bool, len(categoryOrder))
	for _, category := range categoryOrder {
		seen[category] = true
		printFlagCategory(fs, categories, category)
	}
	printFlagCategory(fs, categories, "")
	fs.VisitAll(func(f *flag.Flag) {
		category := categories[f.Name]
		if category == "" || seen[category] {
			return
		}
		seen[category] = true
		printFlagCategory(fs, categories, category)
	})
}

func printFlagCategory(fs *flag.FlagSet, categories map[string]string, category string) {
	var flags []*flag.Flag
	fs.VisitAll(func(f *flag.Flag) {
		if categories[f.Name] == category {
			flags = append(flags, f)
		}
	})
	if len(flags) == 0 {
		return
	}
	label := category
	if label == "" {
		label = "Options"
	}
	_, _ = fmt.Fprintf(fs.Output(), "\n%s:\n\n", label)
	for _, f := range flags {
		printFlagDefault(fs.Output(), f)
	}
}

func printFlagDefault(w io.Writer, f *flag.Flag) {
	_, _ = fmt.Fprintf(w, "  -%s", f.Name)
	name, usage := flag.UnquoteUsage(f)
	if name != "" {
		_, _ = fmt.Fprintf(w, " %s", name)
	}
	_, _ = fmt.Fprintf(w, "\n    \t%s", usage)
	if !isZeroValueFlag(f) {
		_, _ = fmt.Fprintf(w, " (default %s)", defaultValueString(f))
	}
	_, _ = fmt.Fprint(w, "\n")
}

func isZeroValueFlag(f *flag.Flag) bool {
	return f.DefValue == "" || f.DefValue == "0" || f.DefValue == "false"
}

func defaultValueString(f *flag.Flag) string {
	if getter, ok := f.Value.(flag.Getter); ok {
		if _, ok := getter.Get().(string); ok {
			return strconv.Quote(f.DefValue)
		}
	}
	return f.DefValue
}

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
