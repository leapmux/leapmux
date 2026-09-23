package zcode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodestore"
	"github.com/leapmux/leapmux/util/pathutil"
)

// ZCode's CLI stores its sessions in a SQLite database whose `session` table is
// OpenCode's, column for column (see opencode/sessions.go), so this file
// supplies only the path and delegates the query.
//
// Note that this is a DIFFERENT file from the one config.go reads. That
// one is the desktop application's model catalog at `~/.zcode/v2/config.json`;
// this one is the CLI's own configuration at `~/.zcode/cli/config.json`, and
// the two carry unrelated settings.

// zcodeCLIConfigRelPath is the CLI configuration file's path under the home
// directory.
var zcodeCLIConfigRelPath = []string{".zcode", "cli", "config.json"}

// zcodeSessionDBRelPath is the session database's path under ZCode's storage
// directory.
var zcodeSessionDBRelPath = []string{"cli", "db", "db.sqlite"}

// zcodeArtifactRelPath is the artifact directory's path under ZCode's storage
// directory. A live installation holds `cli/db/db.sqlite` beside `cli/artifacts`, so
// the artifact root is a SIBLING of the database's own directory rather than that
// directory itself.
var zcodeArtifactRelPath = []string{"cli", "artifacts"}

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
func zcodeStorageDir(q agent.StoredSessionQuery, cfg zcodeCLIConfig) string {
	home := q.Home()
	if dir := strings.TrimSpace(q.Env("ZCODE_STORAGE_DIR")); dir != "" {
		return pathutil.ExpandHome(dir, home)
	}
	if dir := strings.TrimSpace(cfg.Storage.Dir); dir != "" {
		return pathutil.ExpandHome(dir, home)
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".zcode")
}

// zcodeSessionDBPath resolves ZCode's session database.
func zcodeSessionDBPath(q agent.StoredSessionQuery) string {
	return zcodeToolStorePaths(q).databasePath
}

// zcodeToolStorePaths resolves ZCode's session database and its artifact directory.
//
// The storage root gives both, and the storage-root artifact directory stays the first
// choice. `storage.sessionDbPath` then moves the DATABASE alone, because that setting
// states one FILE and cannot relocate a directory.
//
// A storage-root artifact directory that does not exist falls back to the grandparent
// of the resolved database path. The two branches match these layouts:
//
//   - `<root>/cli/artifacts` beside `<root>/cli/db/db.sqlite`, which is what a stock
//     installation holds.
//   - `<x>/artifacts` beside a database at `<x>/db/db.sqlite` that `sessionDbPath`
//     moved, which keeps ZCode's own layout around the file that the setting points
//     at.
//
// The fallback reports the first choice again for a stock installation, because the
// grandparent of `<root>/cli/db/db.sqlite` is `<root>/cli`.
func zcodeToolStorePaths(q agent.StoredSessionQuery) zcodeToolStoreLocation {
	home := q.Home()
	var cfg zcodeCLIConfig
	if home != "" {
		path := filepath.Join(append([]string{home}, zcodeCLIConfigRelPath...)...)
		// A missing or malformed configuration file is not a failure: the
		// defaults below describe a stock installation, which is the common
		// case, and every field read here is optional in that file.
		_ = sessionstore.ReadSidecarFile(path, sessionstore.MaxSidecarBytes, func(data []byte) error {
			return json.Unmarshal(data, &cfg)
		})
	}
	dir := zcodeStorageDir(q, cfg)
	location := zcodeToolStoreLocation{}
	if dir != "" {
		location.databasePath = filepath.Join(append([]string{dir}, zcodeSessionDBRelPath...)...)
		location.artifactRoot = filepath.Join(append([]string{dir}, zcodeArtifactRelPath...)...)
	}
	if explicit := strings.TrimSpace(cfg.Storage.SessionDbPath); explicit != "" {
		location.databasePath = pathutil.ExpandHome(explicit, home)
	}
	if info, err := os.Stat(location.artifactRoot); err != nil || !info.IsDir() {
		if beside := zcodeArtifactRootBesideDatabase(location.databasePath); beside != "" {
			location.artifactRoot = beside
		}
	}
	return location
}

// zcodeArtifactRootBesideDatabase reproduces ZCode's layout around the database file
// that `storage.sessionDbPath` points at: `<x>/db/db.sqlite` puts the artifacts at
// `<x>/artifacts`.
//
// It reports "" when the path has no grandparent directory, which is what a bare file
// name and a path at a filesystem root both give. The caller then keeps the artifact
// root that the storage root supplied.
func zcodeArtifactRootBesideDatabase(databasePath string) string {
	if databasePath == "" {
		return ""
	}
	parent := filepath.Dir(databasePath)
	grandparent := filepath.Dir(parent)
	if grandparent == parent || grandparent == "." {
		return ""
	}
	return filepath.Join(grandparent, "artifacts")
}

// zcodeStoredSessions is ZCode's Provider.ListStoredSessions.
func zcodeStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return opencodestore.ListSessions(ctx, zcodeSessionDBPath(q), q)
}
