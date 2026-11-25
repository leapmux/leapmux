// Package config provides shared configuration loading utilities for hub and worker.
package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/knadh/koanf/maps"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/util/pathutil"
)

// ParseFlags creates a quiet pflag.FlagSet named name, runs register on it,
// parses args, and rejects unexpected positional arguments.
func ParseFlags(name string, args []string, register func(*pflag.FlagSet) error) (*pflag.FlagSet, error) {
	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.SortFlags = false
	if err := register(fs); err != nil {
		return nil, err
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return fs, nil
}

// HiddenFunc defines a string flag that help output omits; fn receives each
// value. A hidden flag serves a caller other than a person: the service
// definition that LeapMux writes passes one, and a command can accept a flag
// that belongs to another command only to state that command in its error.
func HiddenFunc(fs *pflag.FlagSet, name, usage string, fn func(string) error) {
	fs.Var(&funcValue{fn: fn}, name, usage)
	if err := fs.MarkHidden(name); err != nil {
		panic(fmt.Sprintf("config: hide flag %q: %v", name, err))
	}
}

// funcValue adapts a function to pflag.Value, as flag.Func does internally,
// and retains the last accepted argument so String() and fs.GetString reflect
// what the command line passed.
type funcValue struct {
	fn  func(string) error
	val string
}

func (f *funcValue) Set(s string) error {
	if err := f.fn(s); err != nil {
		return err
	}
	f.val = s
	return nil
}
func (f *funcValue) String() string { return f.val }
func (f *funcValue) Type() string   { return "string" }

// Delim is the koanf path separator. Every koanf instance in this repo takes
// it, and FlagProvider splits a nested key on it, so the two cannot disagree.
const Delim = "."

// FlagProvider is a koanf.Provider that reads only explicitly-set flags from a FlagSet.
// Unlike basicflag, it uses fs.Visit (not fs.VisitAll) so default values are not loaded.
type FlagProvider struct {
	fs       *pflag.FlagSet
	fieldMap map[string]string // flag name -> koanf key (e.g. "data-dir" -> "data_dir")
}

// NewFlagProvider creates a provider that maps explicitly-set CLI flags to koanf keys.
// fieldMap maps flag names (with hyphens) to koanf keys (with underscores).
func NewFlagProvider(fs *pflag.FlagSet, fieldMap map[string]string) *FlagProvider {
	return &FlagProvider{fs: fs, fieldMap: fieldMap}
}

// ReadBytes is not supported.
func (f *FlagProvider) ReadBytes() ([]byte, error) {
	return nil, nil
}

// listValuer is a flag.Value that reports its values as a slice. A repeatable
// flag holds more than one value, and a []string field cannot be unmarshalled
// from the one comma-joined string Value.String() reports -- so the provider
// stores the slice instead.
type listValuer interface {
	Get() []string
}

// Read returns a map of only explicitly-set flags mapped to their koanf keys.
//
// The result is nested, not a flat map of dotted keys. koanf.Load does not
// split a key on the delimiter -- only a provider that takes one, such as
// confmap, does -- so a flat "storage.postgres.dsn" would land as a top-level
// key whose name merely contains dots. Get would still find it, because the
// lookup index is flat either way, and Unmarshal would not, because it walks
// the nested map, where "storage" holds only what the defaults put there. Every
// storage flag then parsed, reported no error, and was ignored.
func (f *FlagProvider) Read() (map[string]interface{}, error) {
	out := make(map[string]interface{})
	f.fs.Visit(func(fl *pflag.Flag) {
		key, ok := f.fieldMap[fl.Name]
		if !ok {
			return
		}
		if sv, isSlice := fl.Value.(pflag.SliceValue); isSlice {
			out[key] = sv.GetSlice()
			return
		}
		if lv, isList := fl.Value.(listValuer); isList {
			out[key] = lv.Get()
			return
		}
		out[key] = fl.Value.String()
	})
	return maps.Unflatten(out, Delim), nil
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

// ExpandHome expands a leading ~ in a path to the PROCESS's home directory.
//
// The rule itself lives in pathutil, which every other caller of it shares.
// This wrapper adds only where the home comes from: a configuration path is
// resolved against the user running the process, and a home lookup that fails
// leaves the path as written.
func ExpandHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return pathutil.ExpandHome(path, home)
}

// Load is a helper that performs the standard koanf loading sequence:
// defaults -> config file -> env vars -> CLI flags.
// It returns the loaded koanf instance. The caller should unmarshal into their config struct.
//
// envListKeys names the keys whose ENVIRONMENT VARIABLE holds a delimited
// list, which the env provider splits into a []string. It does not affect the
// config file (whose lists are already lists) or the flags (whose repeatable
// form appends one value at a time).
func Load(k *koanf.Koanf, defaults map[string]interface{}, configFilePath, envPrefix string, envListKeys map[string]bool, fp *FlagProvider) error {
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
	if err := k.Load(envProvider(envPrefix, envListKeys), nil); err != nil {
		return err
	}

	// 4. CLI flags (only explicitly-set).
	if err := k.Load(fp, nil); err != nil {
		return err
	}

	return nil
}
