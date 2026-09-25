package ohmypi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// omp writes one JSONL file per session:
// `<agent dir>/sessions/<encoded cwd>/<UTC timestamp>_<session id>.jsonl`.
//
// The reader follows omp's own lister (`session/session-listing.ts`):
//
//   - Line 1 is a fixed-size TITLE slot that omp rewrites in place:
//     `{"type":"title","v":1,"title":"...","pad":"   "}`.
//   - Line 2 is the session header: `{"type":"session","id","timestamp","cwd"}`,
//     with an optional `title`.
//   - omp's RPC mode writes no title, so most sessions LeapMux started carry
//     none. readSession states the fallback order.
//   - The session's cwd is the header's, never the directory name: the name
//     encodes the cwd lossily, because `-` stands for both `/` and `-`.
//   - A directory beside a session file holds that session's subagent transcripts,
//     which are not sessions to resume.
//
// The handle is the session FILE, the same form the running agent reports (see
// sessionHandleLocked).

// omp's environment variables that move its store. omp inherited their names from
// Pi, which reads PI_CODING_AGENT_DIR and PI_CODING_AGENT_SESSION_DIR too.
const (
	// envSessionDir stores every session FLAT in one directory, with no bucket
	// per working directory.
	envSessionDir = "PI_CODING_AGENT_SESSION_DIR"
	// envAgentDir moves the agent directory of the default profile.
	envAgentDir = "PI_CODING_AGENT_DIR"
	// envConfigDir renames the config root under the home directory (".omp").
	envConfigDir = "PI_CONFIG_DIR"
	// envProfile selects a named profile, which has an agent directory of its own
	// and ignores envAgentDir. envLegacyProfile is read only when envProfile is
	// unset.
	envProfile       = "OMP_PROFILE"
	envLegacyProfile = "PI_PROFILE"
	// envXDGDataHome moves a profile's data -- the sessions among it -- under
	// `$XDG_DATA_HOME/omp`, when omp's directory there exists.
	envXDGDataHome = "XDG_DATA_HOME"
)

// defaultConfigDirName is omp's config root under the home directory.
const defaultConfigDirName = ".omp"

// ompAppName is omp's directory under `$XDG_DATA_HOME`.
const ompAppName = "omp"

// defaultProfileName is the profile name that omp reads as "no named profile".
const defaultProfileName = "default"

// sessionHeadBytes is how much of a session file the reader reads: the title
// slot, the header, and the first messages. omp's own lister reads 4 KB; the
// first user message can sit past that, so the reader takes more.
const sessionHeadBytes = 16 * 1024

// storedSessions is Oh My Pi's Provider.ListStoredSessions.
func storedSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	workingDir := strings.TrimSpace(q.WorkingDir)
	if workingDir == "" {
		return nil, nil
	}
	dir := sessionDir(q, workingDir)
	if dir == "" {
		return nil, nil
	}
	limit := q.EffectiveLimit()
	// The walk is NOT capped at `limit`: a flat session directory holds the
	// sessions of every working directory, and only each file's header says which
	// one it belongs to. Collect stops at `limit` accepted sessions.
	entries, err := sessionstore.NewestEntries(dir, 0, sessionstore.EntryItself(isSessionFile))
	if err != nil {
		if errors.Is(err, sessionstore.ErrAbsent) {
			return nil, nil
		}
		return nil, err
	}
	// omp records the cwd with its symbolic links resolved -- process.cwd() does
	// that -- so the header of a session started in `/var/...` on macOS states
	// `/private/var/...`. The reader accepts either spelling.
	canonicalDir := canonicalPath(workingDir)
	sessions := sessionstore.Collect(ctx, entries, limit, func(entry sessionstore.Entry) (agent.StoredSession, bool) {
		return readSession(entry, workingDir, canonicalDir)
	})
	return agent.SortAndCapSessions(sessions, limit), nil
}

// isSessionFile accepts the session FILES of a directory. A directory is refused:
// it holds a session's subagent transcripts.
func isSessionFile(entry os.DirEntry) bool {
	return !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl")
}

// sessionDir resolves the directory that holds the sessions of one working
// directory, in the order omp resolves it.
func sessionDir(q agent.StoredSessionQuery, workingDir string) string {
	// omp reads the variable untrimmed, and an empty value as unset. A relative
	// path is relative to omp's working directory, and omp expands no `~`.
	if flat := q.Env(envSessionDir); flat != "" {
		return resolveAgainst(workingDir, flat)
	}
	root := sessionsRoot(q, workingDir)
	if root == "" {
		return ""
	}
	return filepath.Join(root, encodeSessionDirName(workingDir, q.Home(), tempDir(q)))
}

// sessionsRoot resolves the directory that holds every bucket of sessions, as
// omp's DirResolver does (pi-utils `dirs.ts`):
//
//   - A named profile uses `~/.omp/profiles/<name>/agent` and ignores envAgentDir.
//   - The default profile uses envAgentDir, else `~/.omp/agent`.
//   - When the agent directory is the profile's own, on Linux and macOS, the data
//     moves to `$XDG_DATA_HOME/omp` (or `.../omp/profiles/<name>`) once that
//     directory exists. omp drops the `agent` level there.
//
// It returns "" when omp would refuse to start: a profile name that omp rejects
// makes omp exit before it writes a session.
func sessionsRoot(q agent.StoredSessionQuery, workingDir string) string {
	profile, ok := activeProfile(q)
	if !ok {
		return ""
	}
	var defaultAgentDir string
	if home := q.Home(); home != "" {
		configDir := q.Env(envConfigDir)
		if configDir == "" {
			configDir = defaultConfigDirName
		}
		configRoot := filepath.Join(home, configDir)
		if profile != "" {
			configRoot = filepath.Join(configRoot, "profiles", profile)
		}
		defaultAgentDir = filepath.Join(configRoot, "agent")
	}
	agentDir := defaultAgentDir
	if profile == "" {
		if override := q.Env(envAgentDir); override != "" {
			agentDir = resolveAgainst(workingDir, override)
		}
	}
	if agentDir == "" {
		return ""
	}
	if agentDir == defaultAgentDir {
		if dataRoot := xdgDataRoot(q, profile); dataRoot != "" {
			return filepath.Join(dataRoot, "sessions")
		}
	}
	return filepath.Join(agentDir, "sessions")
}

// activeProfile returns the named profile that omp selects, or "" for the
// default profile. It returns false for a name that omp rejects.
//
// omp reads envLegacyProfile only when envProfile is UNSET, so an empty
// envProfile selects the default profile. The query's Getenv cannot tell an
// unset variable from an empty one, so here an empty envProfile falls back to
// envLegacyProfile. The two readings differ only when envProfile is set to ""
// and envLegacyProfile holds a name.
func activeProfile(q agent.StoredSessionQuery) (string, bool) {
	value := q.Env(envProfile)
	if value == "" {
		value = q.Env(envLegacyProfile)
	}
	name := strings.TrimSpace(value)
	if name == "" || name == defaultProfileName {
		return "", true
	}
	if !validProfileName(name) {
		return "", false
	}
	return name, true
}

// profileNamePattern and reservedProfileNamePattern are omp's own rules
// (`normalizeProfileName`): a lowercase name of at most 64 characters, which is
// not a Windows device name.
var (
	profileNamePattern         = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	reservedProfileNamePattern = regexp.MustCompile(`(?i)^(?:CON|PRN|AUX|NUL|COM[0-9]|LPT[0-9])(?:\..*)?$`)
)

// validProfileName reports whether omp accepts a profile name.
func validProfileName(name string) bool {
	return name != "." && name != ".." && !strings.HasSuffix(name, ".") &&
		profileNamePattern.MatchString(name) && !reservedProfileNamePattern.MatchString(name)
}

// xdgDataRoot returns omp's data directory under `$XDG_DATA_HOME`, or "" when omp
// keeps its data in the agent directory. omp moves the data only on Linux and
// macOS, and only when the directory exists. A named profile moves only when its
// OWN directory there exists, so a profile does not move when the default
// profile migrates.
func xdgDataRoot(q agent.StoredSessionQuery, profile string) string {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return ""
	}
	xdg := q.Env(envXDGDataHome)
	if xdg == "" {
		return ""
	}
	root := filepath.Join(xdg, ompAppName)
	if profile != "" {
		root = filepath.Join(root, "profiles", profile)
	}
	if _, err := os.Stat(root); err != nil {
		return ""
	}
	return root
}

// resolveAgainst resolves a path from omp's environment the way omp's
// `path.resolve` does: an absolute path stays, and a relative one is relative to
// omp's working directory.
func resolveAgainst(workingDir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(workingDir, path)
}

// tempDir is the directory that omp's os.tmpdir() answers, by Node's rule:
//
//   - POSIX: TMPDIR, else TMP, else TEMP, else "/tmp", with no trailing slash.
//   - Windows: TEMP, else TMP, else `%SystemRoot%\temp` (or `%windir%\temp`),
//     with no trailing backslash unless the path is a drive root.
func tempDir(q agent.StoredSessionQuery) string {
	if runtime.GOOS == "windows" {
		dir := sessionstore.FirstNonBlank(q.Env("TEMP"), q.Env("TMP"))
		if dir == "" {
			dir = sessionstore.FirstNonBlank(q.Env("SystemRoot"), q.Env("windir")) + `\temp`
		}
		if len(dir) > 1 && strings.HasSuffix(dir, `\`) && !strings.HasSuffix(dir, `:\`) {
			dir = dir[:len(dir)-1]
		}
		return dir
	}
	dir := sessionstore.FirstNonBlank(q.Env("TMPDIR"), q.Env("TMP"), q.Env("TEMP"))
	if dir == "" {
		return "/tmp"
	}
	if len(dir) > 1 && strings.HasSuffix(dir, "/") {
		dir = dir[:len(dir)-1]
	}
	return dir
}

// encodeSessionDirName reproduces omp's `getDefaultSessionDirName`: the name of
// the directory that holds one working directory's sessions.
//
//   - Under the home directory: "-" and the path relative to home, with `/`, `\`
//     and `:` replaced by `-`. The home directory itself is "-".
//   - Under the temp directory: "-tmp-" and the relative path; the temp
//     directory itself is "-tmp".
//   - Anywhere else: "--", the absolute path without its first separator and
//     with every separator replaced, and "--".
//
// omp compares the CANONICAL paths, with symbolic links resolved, so this does
// too: on macOS `/tmp` is `/private/tmp`.
func encodeSessionDirName(workingDir, home, temp string) string {
	cwd := canonicalPath(workingDir)
	if home != "" {
		if rel, ok := relativeInside(canonicalPath(home), cwd); ok {
			return encodeRelativeDirName("-", rel)
		}
	}
	if temp != "" {
		if rel, ok := relativeInside(canonicalPath(temp), cwd); ok {
			return encodeRelativeDirName("-tmp", rel)
		}
	}
	trimmed := cwd
	if trimmed != "" && (trimmed[0] == '/' || trimmed[0] == '\\') {
		trimmed = trimmed[1:]
	}
	return "--" + replaceSeparators(trimmed) + "--"
}

// encodeRelativeDirName is omp's `encodeRelativeSessionDirName`.
func encodeRelativeDirName(prefix, relative string) string {
	encoded := replaceSeparators(relative)
	switch {
	case encoded == "":
		return prefix
	case strings.HasSuffix(prefix, "-"):
		return prefix + encoded
	default:
		return prefix + "-" + encoded
	}
}

// replaceSeparators replaces every `/`, `\` and `:` with `-`.
func replaceSeparators(path string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' {
			return '-'
		}
		return r
	}, path)
}

// relativeInside returns target's path relative to base, and whether omp counts
// target as base or a path inside it. The relative path of base itself is "".
//
// omp's test is `startsWith("..")`, which also refuses a first component such
// as `..cache`. The reader copies that test, because it must find the directory
// omp chose, not the directory a stricter test would choose.
func relativeInside(base, target string) (string, bool) {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return "", false
	}
	if rel == "." {
		return "", true
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

// canonicalPath resolves a path's symbolic links, and cleans it when it cannot.
func canonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// sessionRecord is one record of a session file's head: the title slot, the
// header, a message, or a compaction.
type sessionRecord struct {
	Type         string  `json:"type"`
	ID           string  `json:"id"`
	Cwd          string  `json:"cwd"`
	Title        *string `json:"title"`
	ShortSummary string  `json:"shortSummary"`
	Message      *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// sessionHead is what the reader takes from a session file's head.
type sessionHead struct {
	header       *sessionRecord
	title        string
	shortSummary string
	firstUser    string
	answered     bool
}

// readSession derives one session from its file, or reports that the file is not
// a session of this working directory, or holds nothing to resume.
//
// The title follows omp's own lister (`scanSessionFile`), then omp's picker:
//
//  1. The title slot, when it holds a title. A slot that holds a BLANK title
//     clears the header's title: omp writes that when a user removes the title.
//  2. The header's title, when there is no slot.
//  3. The short summary of the first compaction in the head.
//  4. The first user message.
//
// omp's picker hides an EMPTY session: no title, no user message, and no reply.
// The reader skips it too, since it holds nothing to resume.
func readSession(entry sessionstore.Entry, workingDir, canonicalDir string) (agent.StoredSession, bool) {
	lines, _, err := sessionstore.JSONLHead(entry.Path, sessionHeadBytes)
	if err != nil || len(lines) == 0 {
		return agent.StoredSession{}, false
	}
	head, ok := parseSessionHead(lines)
	if !ok {
		return agent.StoredSession{}, false
	}
	if !sessionstore.SameDir(head.header.Cwd, workingDir) && !sessionstore.SameDir(head.header.Cwd, canonicalDir) {
		return agent.StoredSession{}, false
	}
	title := sessionstore.FirstNonBlank(head.title, head.shortSummary, head.firstUser)
	if strings.TrimSpace(title) == "" && !head.answered {
		return agent.StoredSession{}, false
	}
	return agent.StoredSession{
		Handle:    entry.Path,
		Title:     sessionstore.TrimTitle(title),
		UpdatedAt: entry.ModTime,
	}, true
}

// parseSessionHead reads the records of a session file's head. It reports false
// for a file that is not an omp session: omp writes the header as the first
// record, or as the second after the title slot.
func parseSessionHead(lines [][]byte) (sessionHead, bool) {
	var head sessionHead
	var slot *sessionRecord
	for i, line := range lines {
		var record sessionRecord
		if json.Unmarshal(line, &record) != nil {
			if head.header == nil {
				return sessionHead{}, false
			}
			continue
		}
		if head.header == nil {
			switch {
			case i == 0 && record.Type == "title":
				slot = &record
				continue
			case record.Type == "session" && strings.TrimSpace(record.ID) != "":
				head.header = &record
				continue
			default:
				return sessionHead{}, false
			}
		}
		switch record.Type {
		case "compaction":
			if head.shortSummary == "" {
				head.shortSummary = strings.TrimSpace(record.ShortSummary)
			}
		case "message":
			if record.Message == nil {
				continue
			}
			switch record.Message.Role {
			case "user":
				if head.firstUser == "" {
					head.firstUser = sessionstore.ContentBlockText(record.Message.Content)
				}
			case "assistant":
				head.answered = true
			}
		}
	}
	if head.header == nil {
		return sessionHead{}, false
	}
	switch {
	case slot != nil && slot.Title != nil:
		head.title = strings.TrimSpace(*slot.Title)
	case head.header.Title != nil:
		head.title = strings.TrimSpace(*head.header.Title)
	}
	return head, true
}
