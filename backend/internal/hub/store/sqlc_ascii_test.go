package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every file sqlc parses must be pure ASCII.
//
// The repo's CLAUDE.md states the rule, and the rule exists because the
// failure is misleading: the sqlc parser falls over on a non-ASCII byte --
// typically an em-dash or a smart quote inside a comment -- with a
// `mismatched input 'SELECr'`-style error that points at the wrong line, in
// a file the author did not edit. Four em-dashes had already reached the
// migrations, because CLAUDE.md was the only enforcement.
//
// BOTH inputs are covered, not just `queries/`: each dialect's sqlc.yaml
// declares `schema: "db/migrations"`, so the parser reads a migration for
// every generate.
func TestSQLCInputsAreASCII(t *testing.T) {
	t.Parallel()

	roots := []string{
		filepath.Join("sqlite", "db"),
		filepath.Join("postgres", "db"),
		filepath.Join("mysql", "db"),
	}

	scanned := 0
	for _, root := range roots {
		require.DirExistsf(t, root, "%s moved; this guard would pass vacuously", root)
		require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".sql") {
				return err
			}
			scanned++
			body, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			for i, b := range body {
				if b > 0x7f {
					line := 1 + strings.Count(string(body[:i]), "\n")
					assert.Failf(t, "non-ASCII byte in a sqlc input",
						"%s:%d has byte 0x%02x. Use plain ASCII: -- instead of an em-dash, "+
							"and \" instead of a smart quote. sqlc reports this as a parse error "+
							"on an unrelated line.", path, line, b)
					return nil
				}
			}
			return nil
		}))
	}
	assert.Greaterf(t, scanned, 30, "only %d files scanned; the walk is not reaching the query trees", scanned)
}
