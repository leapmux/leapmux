package zcode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodestore"
)

// The native preparation command resolves the app-server's shared database.
// Workspace config changes image storage, but cannot move that shared database.
var zcodeCLIConfigRelPath = []string{".zcode", "cli", "config.json"}

func zcodeSessionDBPath(ctx context.Context, q agent.StoredSessionQuery, query zcodeStoragePathQuery) (string, error) {
	if query == nil {
		return "", errors.New("the ZCode provider has no native storage query")
	}
	if q.Environ() == nil {
		primary, alias := q.Env("ZCODE_SESSION_DB_PATH"), q.Env("ZCODE_SESSION_DB")
		if primary != "" && alias != "" && primary != alias {
			return "", errors.New("the ZCode database aliases have no known native environment order")
		}
	}
	return query.DatabasePath(ctx, q)
}

func zcodeToolStorePaths(ctx context.Context, q agent.StoredSessionQuery, query zcodeStoragePathQuery) (zcodeToolStoreLocation, error) {
	database, err := zcodeSessionDBPath(ctx, q, query)
	if err != nil {
		return zcodeToolStoreLocation{}, err
	}
	outputFileRoot, err := zcodeImageOutputFileRoot(q)
	if err != nil {
		return zcodeToolStoreLocation{}, err
	}
	return zcodeToolStoreLocation{databasePath: database, outputFileRoot: outputFileRoot}, nil
}

// zcodeImageOutputFileRoot preserves the configured root of native image URIs.
// Text serialization files use their completed database record's exact path instead.
func zcodeImageOutputFileRoot(q agent.StoredSessionQuery) (string, error) {
	home := q.Home()
	if home == "" {
		return "", errors.New("the ZCode image store has no native home directory")
	}
	directory := filepath.Join(home, ".zcode")
	if configured := zcodeConfigStorageDirectory(filepath.Join(append([]string{home}, zcodeCLIConfigRelPath...)...)); configured != "" {
		directory = configured
	}
	for _, base := range zcodeProjectConfigDirectories(q.WorkingDir) {
		for _, path := range []string{filepath.Join(base, "zcode.json"), filepath.Join(base, ".zcode", "config.json")} {
			if configured := zcodeConfigStorageDirectory(path); configured != "" {
				directory = configured
			}
		}
	}
	if configured := q.Env("ZCODE_STORAGE_DIR"); configured != "" {
		directory = configured
	}
	resolved, err := zcodeNativePath(directory, home, q.WorkingDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, "cli", "artifacts"), nil
}

// The image URI route reads only its existing storage-directory setting.
// The native command owns complete config validation and database path selection.
func zcodeConfigStorageDirectory(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var record map[string]json.RawMessage
	if json.Unmarshal(data, &record) != nil {
		return ""
	}
	var storage map[string]json.RawMessage
	if json.Unmarshal(record["storage"], &storage) != nil {
		return ""
	}
	var directory string
	if json.Unmarshal(storage["dir"], &directory) != nil {
		return ""
	}
	return directory
}

func zcodeProjectConfigDirectories(directory string) []string {
	if directory == "" {
		return nil
	}
	current, err := filepath.Abs(directory)
	if err != nil {
		return nil
	}
	initial := current
	var directories []string
	for {
		directories = append(directories, current)
		if marker, err := os.Stat(filepath.Join(current, ".git")); err == nil && (marker.IsDir() || marker.Mode().IsRegular()) {
			slices.Reverse(directories)
			return directories
		}
		parent := filepath.Dir(current)
		if parent == current {
			return []string{initial}
		}
		current = parent
	}
}

func zcodeNativePath(value, home, directory string) (string, error) {
	if strings.ContainsRune(value, '\x00') {
		return "", errors.New("the ZCode storage path contains a NUL character")
	}
	if strings.HasPrefix(value, "~/") {
		if home == "" {
			return "", errors.New("the ZCode storage path has no native home directory")
		}
		return filepath.Join(home, value[2:]), nil
	}
	if !filepath.IsAbs(value) && directory != "" {
		value = filepath.Join(directory, value)
	}
	return filepath.Abs(value)
}

func zcodeStoredSessions(ctx context.Context, q agent.StoredSessionQuery, query zcodeStoragePathQuery) ([]agent.StoredSession, error) {
	path, err := zcodeSessionDBPath(ctx, q, query)
	if err != nil {
		return nil, err
	}
	return opencodestore.ListSessions(ctx, path, q)
}
