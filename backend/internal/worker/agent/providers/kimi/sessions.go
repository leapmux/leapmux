package kimi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// Kimi Code keeps every session under its data root, in a directory keyed by
// the working directory:
//
//	$KIMI_CODE_HOME/sessions/wd_<slug>_<sha256[:12]>/session_<uuid>/state.json
//
// `state.json` is the session's own record -- its id, working directory, title,
// last prompt and timestamps. The reader opens it read-only and never writes to
// the store.

// kimiHomeEnv is the variable that moves Kimi Code's data root. It defaults to
// `~/.kimi-code`.
const kimiHomeEnv = "KIMI_CODE_HOME"

// kimiStateFile is a session's own record.
const kimiStateFile = "state.json"

// kimiSessionDirPrefix starts the name of every session directory.
const kimiSessionDirPrefix = "session_"

// kimiWorkDirSlugMaxLen is the longest slug Kimi Code puts in a directory key.
const kimiWorkDirSlugMaxLen = 40

// kimiSlugRun matches a run of characters a slug replaces with a hyphen.
var kimiSlugRun = regexp.MustCompile(`[^a-z0-9._-]+`)

// kimiWorkDirKey reproduces Kimi Code's `encodeWorkDirKey`: `wd_`, a slug of the
// directory's base name, `_`, and the first 12 hex digits of the SHA-256 of the
// normalized path. The path is normalized by turning every backslash into a
// slash and trimming trailing slashes, as the CLI does.
func kimiWorkDirKey(workDir string) string {
	normalized := strings.TrimRight(strings.ReplaceAll(workDir, `\`, "/"), "/")
	base := normalized
	if i := strings.LastIndex(normalized, "/"); i >= 0 {
		base = normalized[i+1:]
	}
	sum := sha256.Sum256([]byte(normalized))
	return "wd_" + kimiSlug(base) + "_" + hex.EncodeToString(sum[:])[:12]
}

// kimiSlug reproduces Kimi Code's `slugifyWorkDirName`.
func kimiSlug(name string) string {
	slug := kimiSlugRun.ReplaceAllString(strings.ToLower(name), "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > kimiWorkDirSlugMaxLen {
		slug = slug[:kimiWorkDirSlugMaxLen]
	}
	slug = strings.Trim(slug, "-")
	if slug == "" || slug == "." || slug == ".." {
		return "workspace"
	}
	return slug
}

// kimiDataRoot resolves Kimi Code's data root.
func kimiDataRoot(q agent.StoredSessionQuery) string {
	return sessionstore.HomeDirFromEnv(q, kimiHomeEnv, ".kimi-code")
}

// kimiSessionState is the part of `state.json` the reader takes.
type kimiSessionState struct {
	ID         string `json:"id"`
	Cwd        string `json:"cwd"`
	Archived   bool   `json:"archived"`
	Title      string `json:"title"`
	LastPrompt string `json:"lastPrompt"`
	UpdatedAt  int64  `json:"updatedAt"`
}

// kimiStoredSessions is Kimi Code's Provider.ListStoredSessions.
func kimiStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	workingDir := strings.TrimSpace(q.WorkingDir)
	root := kimiDataRoot(q)
	if workingDir == "" || root == "" {
		return nil, nil
	}
	dir := filepath.Join(root, "sessions", kimiWorkDirKey(workingDir))
	entries, err := sessionstore.NewestEntries(dir, 0, kimiSessionEntry)
	if err != nil {
		if errors.Is(err, sessionstore.ErrAbsent) {
			return nil, nil
		}
		return nil, err
	}
	limit := q.EffectiveLimit()
	sessions := sessionstore.Collect(ctx, entries, limit, func(entry sessionstore.Entry) (agent.StoredSession, bool) {
		return readKimiSession(entry, workingDir)
	})
	return agent.SortAndCapSessions(sessions, limit), nil
}

// kimiSessionEntry keeps a session directory that holds a state file, timed by
// that file.
func kimiSessionEntry(dir string, entry os.DirEntry) (sessionstore.Entry, bool) {
	if !strings.HasPrefix(entry.Name(), kimiSessionDirPrefix) {
		return sessionstore.Entry{}, false
	}
	return sessionstore.NamedFileInside(kimiStateFile)(dir, entry)
}

// readKimiSession reads one session's state file.
//
// A session the user archived is not offered, and neither is one with no
// prompt: an empty session has nothing to resume into, which the CLI's own
// `exclude_empty` states too. The working directory must match exactly -- two
// directories share a key only by a hash collision, and a guess would resume
// the wrong session.
func readKimiSession(entry sessionstore.Entry, workingDir string) (agent.StoredSession, bool) {
	var state kimiSessionState
	if err := sessionstore.ReadSidecarFile(filepath.Join(entry.Path, kimiStateFile), sessionstore.MaxSidecarBytes, func(data []byte) error {
		return json.Unmarshal(data, &state)
	}); err != nil {
		return agent.StoredSession{}, false
	}
	id := strings.TrimSpace(state.ID)
	if id == "" || !strings.HasPrefix(id, kimiSessionDirPrefix) || state.Archived || strings.TrimSpace(state.LastPrompt) == "" {
		return agent.StoredSession{}, false
	}
	if !sessionstore.SameDir(state.Cwd, workingDir) {
		return agent.StoredSession{}, false
	}
	updated := sessionstore.EpochMillis(state.UpdatedAt)
	if updated.IsZero() {
		updated = entry.ModTime
	}
	return agent.StoredSession{
		Handle:    id,
		Title:     sessionstore.TrimTitle(sessionstore.FirstNonBlank(state.Title, state.LastPrompt)),
		UpdatedAt: updated,
	}, true
}
