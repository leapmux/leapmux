package agenttest

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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
