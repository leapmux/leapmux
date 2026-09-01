package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
)

// ZCode's CLI stores its sessions in a SQLite database whose `session` table is
// OpenCode's, column for column (see opencode_sessions.go), so this file
// supplies only the path and delegates the query.
//
// Note that this is a DIFFERENT file from the one zcode_config.go reads. That
// one is the desktop application's model catalog at `~/.zcode/v2/config.json`;
// this one is the CLI's own configuration at `~/.zcode/cli/config.json`, and
// the two carry unrelated settings.

// zcodeCLIConfigRelPath is the CLI configuration file's path under the home
// directory.
var zcodeCLIConfigRelPath = []string{".zcode", "cli", "config.json"}

// zcodeSessionDBRelPath is the session database's path under ZCode's storage
// directory.
var zcodeSessionDBRelPath = []string{"cli", "db", "db.sqlite"}

// zcodeCLIConfig is the subset of ZCode's CLI configuration this file reads.
type zcodeCLIConfig struct {
	Storage struct {
		// Dir is the storage root, default `~/.zcode`. It may begin with `~`.
		Dir string `json:"dir"`
		// SessionDbPath points straight at the database and wins over Dir.
		// It may begin with `~`.
		SessionDbPath string `json:"sessionDbPath"`
	} `json:"storage"`
}

// zcodeStorageDir resolves ZCode's storage root.
//
// ZCODE_STORAGE_DIR wins, then the CLI configuration's `storage.dir`, then
// `~/.zcode`. That is the CLI's own order, so a user who moved the store is
// followed rather than told there are no sessions.
func zcodeStorageDir(q StoredSessionQuery, cfg zcodeCLIConfig) string {
	home := q.home()
	if dir := strings.TrimSpace(q.env("ZCODE_STORAGE_DIR")); dir != "" {
		return expandHome(dir, home)
	}
	if dir := strings.TrimSpace(cfg.Storage.Dir); dir != "" {
		return expandHome(dir, home)
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".zcode")
}

// zcodeSessionDBPath resolves ZCode's session database.
func zcodeSessionDBPath(q StoredSessionQuery) string {
	home := q.home()
	var cfg zcodeCLIConfig
	if home != "" {
		path := filepath.Join(append([]string{home}, zcodeCLIConfigRelPath...)...)
		// A missing or malformed configuration file is not a failure: the
		// defaults below describe a stock installation, which is the common
		// case, and every field read here is optional in that file.
		_ = readSidecarFile(path, maxSidecarBytes, func(data []byte) error {
			return json.Unmarshal(data, &cfg)
		})
	}
	if explicit := strings.TrimSpace(cfg.Storage.SessionDbPath); explicit != "" {
		return expandHome(explicit, home)
	}
	dir := zcodeStorageDir(q, cfg)
	if dir == "" {
		return ""
	}
	return filepath.Join(append([]string{dir}, zcodeSessionDBRelPath...)...)
}

// zcodeStoredSessions is ZCode's Provider.ListStoredSessions.
func zcodeStoredSessions(ctx context.Context, q StoredSessionQuery) ([]StoredSession, error) {
	return openCodeFamilySessions(ctx, zcodeSessionDBPath(q), q)
}

// expandHome resolves a leading `~` against `home`. Several of these
// configuration files store paths in that form, and none of them expand it
// before writing.
func expandHome(path, home string) string {
	if home == "" || path == "" {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(home, path[2:])
	}
	return path
}

// maxSidecarBytes caps a read of a small metadata file next to a session --
// a configuration file, a `.meta`, a `workspace.yaml`. Generous for every such
// file, and small enough that a file which is not what its name says cannot be
// read into memory whole.
const maxSidecarBytes = 256 * 1024
