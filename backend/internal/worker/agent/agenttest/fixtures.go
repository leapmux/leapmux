package agenttest

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/require"
)

// RequireHostAbsDir fails a fixture at its own call site when the working
// directory it states is not absolute for the host.
//
// Without it the failure appears far from the mistake and describes the wrong
// thing: no query matches the seeded row, the reader answers nothing, and the
// assertion that fails names a filter that is correct.
func RequireHostAbsDir(t *testing.T, dir string) {
	t.Helper()
	require.Truef(t, filepath.IsAbs(dir),
		"a fixture working directory must be absolute on %s; build it with testutil.NativeAbsPath: %q", runtime.GOOS, dir)
}

// JSONString renders a value as a JSON string literal, quotation marks
// included, for a fixture that states its document as raw text.
//
// A host path is what needs it. Pasted between two quotation marks, a Windows
// path is not valid JSON: `\U` is an unknown escape, and the decoder rejects
// the WHOLE document rather than that one field, so the session it describes
// disappears and no assertion says why. A JSON string is also a valid YAML
// double-quoted scalar, which is why Copilot's YAML fixture uses this too.
//
// json.Marshal cannot fail for a string, so the error path is unreachable.
func JSONString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// FixtureEnv builds a Getenv seam over a map, so a reader's path resolution can
// be exercised without touching the process environment. t.Setenv is the
// alternative and it cannot be used here: these tests run in parallel, and
// t.Setenv panics under t.Parallel.
func FixtureEnv(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

// NewFixtureDB creates a SQLite database at `path` and applies `ddl`. The
// parent directory is created, so a caller states the whole store layout in one
// path.
func NewFixtureDB(t *testing.T, path, ddl string) *sql.DB {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	db, err := sqlitedb.Open(path, sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(ddl)
	require.NoError(t, err)
	return db
}

// WriteFixtureFile writes a file, creating its parents.
func WriteFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// PlatformBootstrapEnv appends the OS variables a child process needs to boot
// at all on Windows: PowerShell and the system loader answer SYSTEMROOT,
// WINDIR, COMSPEC and PATHEXT, and a replacement environment without them
// starts nothing. They locate OS components and carry no provider login, so a
// test that states a private environment still adds them.
func PlatformBootstrapEnv(entries []string) []string {
	for _, key := range []string{"SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT"} {
		if value := os.Getenv(key); value != "" {
			entries = append(entries, key+"="+value)
		}
	}
	return entries
}

// SamePath reports whether two paths name the same directory on this host.
// Windows hands a test the 8.3 short spelling of its temp directories while
// a resolved path answers the long one, and macOS and Windows ignore case, so
// a plain string comparison rejects the same directory.
func SamePath(t *testing.T, a, b string) bool {
	t.Helper()
	resolve := func(path string) string {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}
		return path
	}
	ra, rb := resolve(a), resolve(b)
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.EqualFold(ra, rb)
	}
	return ra == rb
}

// RenameDirOrOSRefusal renames the directory old to new and reports whether
// the rename happened. Windows refuses to rename a directory that any handle
// still holds open, and a test that stages a swap of an OPENED archive
// directory cannot force it: the refusal itself is the same guarantee the
// test asserts -- the opened archive keeps its original bytes -- so the
// caller takes refused=true as its answer instead of the reader-level check.
func RenameDirOrOSRefusal(t *testing.T, old, new string) (renamed bool, refused bool) {
	t.Helper()
	err := os.Rename(old, new)
	if err == nil {
		return true, false
	}
	// ERROR_SHARING_VIOLATION (32) and ERROR_ACCESS_DENIED (5) are the two
	// shapes Windows answers for a directory an open handle protects.
	var errno syscall.Errno
	if errors.As(err, &errno) && (errno == 32 || errno == 5) {
		return false, true
	}
	t.Fatalf("rename %s: %v", old, err)
	return false, false
}

// JSONEscaped answers value as JSON encodes it inside a string, quotes
// excluded, so an assertion can look for value inside marshaled JSON content:
// a Windows path holds backslashes that the encoding doubles, and the raw
// value then never matches, quoted or not.
func JSONEscaped(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded[1 : len(encoded)-1])
}

// TouchFixture sets a file's modification time, so a test can state the
// recency order the readers sort by rather than depend on write order.
func TouchFixture(t *testing.T, path string, at time.Time) {
	t.Helper()
	require.NoError(t, os.Chtimes(path, at, at))
}

// Handles reduces a result to its handles, which is what most orderings and
// filters are asserted on.
func Handles(sessions []agent.StoredSession) []string {
	out := make([]string, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.Handle)
	}
	return out
}
