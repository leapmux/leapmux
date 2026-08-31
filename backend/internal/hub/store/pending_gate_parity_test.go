package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The pending-mail mint gates -- SetPendingEmail/SetPendingRecovery's
// conditional write and the two failed-send clears -- are hand-maintained
// in three dialects' users.sql, and the shared storetest suite that pins
// their behavior runs against mysql and postgres only under the
// integration build tag. A gate that drifts between dialects (`<=` vs
// `<`, a stamp written in one dialect and NULLed in another) compiles
// clean and ships unnoticed in the Docker-free run, so this guard asserts
// the gate lines themselves are textually identical across dialects: one
// rule stated three times, or not at all.
func TestPendingMailGateIsIdenticalAcrossDialects(t *testing.T) {
	t.Parallel()

	gated := []string{"SetPendingEmail", "SetPendingRecovery", "ClearPendingEmailCode", "ClearPendingRecovery"}
	paths := []struct{ dialect, path string }{
		{"sqlite", filepath.Join("sqlite", "db", "queries", "users.sql")},
		{"postgres", filepath.Join("postgres", "db", "queries", "users.sql")},
		{"mysql", filepath.Join("mysql", "db", "queries", "users.sql")},
	}

	var reference map[string]string
	for _, p := range paths {
		body, err := os.ReadFile(p.path)
		require.NoError(t, err)
		blocks := namedQueryBlocks(string(body))
		got := map[string]string{}
		for _, name := range gated {
			block, ok := blocks[name]
			require.True(t, ok, "%s: %s moved out of users.sql; update this guard, do not delete it", p.dialect, name)
			var lines []string
			for _, line := range strings.Split(block, "\n") {
				// The gate lives on the CODE lines that write or read an
				// issue instant; comment blocks are dialect-specific prose,
				// and dialect noise (updated_at's strftime vs NOW()) sits
				// at the end of the SET clause, so both are cut to compare
				// the shared gate columns only.
				if strings.HasPrefix(line, "--") {
					continue
				}
				if !strings.Contains(line, "unblocked_at") {
					continue
				}
				if i := strings.Index(line, ", updated_at"); i >= 0 {
					line = line[:i]
				}
				lines = append(lines, line)
			}
			require.NotEmpty(t, lines, "%s: %s carries no gate lines; the extraction is broken, not the query", p.dialect, name)
			got[name] = strings.Join(lines, "\n")
		}
		if reference == nil {
			reference = got
			continue
		}
		for _, name := range gated {
			assert.Equal(t, reference[name], got[name],
				"%s: the %s gate drifted from sqlite's spelling; change all three dialects together", p.dialect, name)
		}
	}
}

// namedQueryBlocks splits a sqlc input into its `-- name: X :cmd` blocks.
func namedQueryBlocks(body string) map[string]string {
	out := map[string]string{}
	var name string
	var lines []string
	flush := func() {
		if name != "" {
			out[name] = strings.Join(lines, "\n")
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "-- name: ") {
			flush()
			name = strings.TrimSpace(strings.TrimPrefix(line, "-- name:"))
			// Drop the command suffix: "-- name: X :one".
			if i := strings.IndexByte(name, ':'); i >= 0 {
				name = strings.TrimSpace(name[:i])
			}
			lines = nil
			continue
		}
		if name != "" {
			lines = append(lines, line)
		}
	}
	flush()
	return out
}
