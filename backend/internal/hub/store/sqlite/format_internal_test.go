package sqlite

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/timefmt"
)

// canonicalStrftimePattern is the ONE strftime layout every SQL-side timestamp
// write and compare in this dialect must use. It is the strftime spelling of
// the Go-side canonical layout (timefmt.ISO8601, "2006-01-02T15:04:05.000Z")
// that sqltime.SQLiteTime.Value emits: %Y-%m-%d = 2006-01-02, T literal,
// %H:%M = 15:04, %f = seconds with fixed 3-digit millis (05.000), Z literal.
const canonicalStrftimePattern = `'%Y-%m-%dT%H:%M:%fZ'`

// TestStrftimeLiteralsAreCanonical is the Docker-less static source scan (twin
// of the mysql/postgres collation scans) that collapses the ~75 hand-typed
// strftime literals across this dialect's SQL into ONE pinned fact: every
// strftime( call in the migrations and queries uses exactly
// canonicalStrftimePattern. Without this, each literal is an independent
// opportunity for a one-character drift (a missing T, %f -> %S, a stray
// space) that silently breaks the raw-string keyset/liveness compares while
// every result-level test stays green -- the "cannot drift" claim in
// sqltime.SQLiteTime's doc comment held only for the single Go constant until
// this scan made it true SQL-side too.
func TestStrftimeLiteralsAreCanonical(t *testing.T) {
	// Sanity-pin the Go side of the pairing first, so a change to
	// timefmt.ISO8601 cannot silently diverge from the SQL pattern this test
	// enforces.
	require.Equal(t, "2006-01-02T15:04:05.000Z", timefmt.ISO8601,
		"timefmt.ISO8601 changed: update canonicalStrftimePattern (and every SQL literal) in lockstep")

	var files []string
	for _, glob := range []string{"db/migrations/*.sql", "db/queries/*.sql"} {
		matches, err := filepath.Glob(glob)
		require.NoError(t, err)
		files = append(files, matches...)
	}
	require.NotEmpty(t, files, "no SQL files found; the scan is vacuous")

	strftimeCall := regexp.MustCompile(`strftime\(\s*('[^']*')`)
	total := 0
	for _, path := range files {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		for i, line := range strings.Split(string(data), "\n") {
			// Strip -- comments: prose ("...writes strftime('now')") is not an
			// invariant-bearing call site. The canonical pattern itself never
			// contains a double hyphen, so this cannot truncate a real literal.
			if idx := strings.Index(line, "--"); idx >= 0 {
				line = line[:idx]
			}
			for _, m := range strftimeCall.FindAllStringSubmatch(line, -1) {
				total++
				assert.Equal(t, canonicalStrftimePattern, m[1],
					"%s:%d: strftime format literal deviates from the canonical layout; a drifted literal silently breaks the raw-string keyset/liveness compares", path, i+1)
			}
		}
	}
	// The scan must have matched a substantial number of call sites: a near-zero
	// count means the glob or regex broke and the assertions above passed
	// vacuously.
	assert.Greater(t, total, 50, "strftime scan matched too few literals; scan is likely broken")
}
