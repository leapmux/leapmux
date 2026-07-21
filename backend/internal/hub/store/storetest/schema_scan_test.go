package storetest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStripSQLLineComments(t *testing.T) {
	in := "CREATE TABLE t ( -- issued; force-expires\n  id TEXT, -- trailing\n  n INT\n);\n"
	// The ';' inside the comment must go; code before '--' must survive. The
	// helper emits one '\n' per split segment, so input ending in a newline
	// gains a trailing blank line -- harmless for the scans, pinned here.
	assert.Equal(t, "CREATE TABLE t ( \n  id TEXT, \n  n INT\n);\n\n", StripSQLLineComments(in))
	assert.Equal(t, "\n", StripSQLLineComments(""), "empty input yields a single newline (line-join shape)")
}

func TestWalkCreateTableColumns(t *testing.T) {
	schema := `
-- prose with a ; semicolon
CREATE TABLE alpha (
  id TEXT PRIMARY KEY,
  created_at DATETIME(3) NOT NULL, -- comment after column
  loose_at datetime(2),
  recorded TIMESTAMP,
  CONSTRAINT fk FOREIGN KEY (id) REFERENCES other (id)
);
CREATE INDEX idx ON alpha (id);
CREATE TABLE bravo (
  seen_at TIMESTAMPTZ
);
`
	type col struct{ name, typeTok string }
	var got []col
	WalkCreateTableColumns(schema, func(name, typeTok string) {
		got = append(got, col{name, typeTok})
	})
	// Every body line with >= 2 tokens is yielded (constraint lines included --
	// callers filter by name shape); names lowercased, types uppercased,
	// trailing comma trimmed; the index statement outside a body is skipped.
	assert.Equal(t, []col{
		{"id", "TEXT"},
		{"created_at", "DATETIME(3)"},
		{"loose_at", "DATETIME(2)"},
		{"recorded", "TIMESTAMP"},
		{"constraint", "FK"},
		{"seen_at", "TIMESTAMPTZ"},
	}, got)
}

func TestWalkCreateTableColumnsNoTables(t *testing.T) {
	// Empty input and DDL without CREATE TABLE bodies must yield nothing --
	// the dialect scans' atColumns sanity guard is what turns "walked nothing"
	// into a failure, so the walker itself stays silent.
	for _, schema := range []string{"", "CREATE INDEX idx ON alpha (id);\n-- comment only\n"} {
		WalkCreateTableColumns(schema, func(name, typeTok string) {
			t.Errorf("unexpected column yielded from %q: %s %s", schema, name, typeTok)
		})
	}
}
