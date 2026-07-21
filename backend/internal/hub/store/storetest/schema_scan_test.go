package storetest

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrationFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"db/migrations/00001_a.sql": {Data: []byte("CREATE TABLE a ();")},
		"db/migrations/00002_b.sql": {Data: []byte("CREATE TABLE b ();")},
		"db/migrations/readme.md":   {Data: []byte("not sql")},
		"other/00003_c.sql":         {Data: []byte("CREATE TABLE c ();")},
		// The glob must not recurse: neither goose nor the //go:embed
		// db/migrations/*.sql pattern picks up nested files, so the scan
		// must not either.
		"db/migrations/sub/00004_d.sql": {Data: []byte("CREATE TABLE d ();")},
	}
	got, err := MigrationFiles(fsys)
	require.NoError(t, err)
	assert.Equal(t, []MigrationFile{
		{Name: "db/migrations/00001_a.sql", SQL: "CREATE TABLE a ();"},
		{Name: "db/migrations/00002_b.sql", SQL: "CREATE TABLE b ();"},
	}, got)
}

func TestMigrationFilesEmpty(t *testing.T) {
	_, err := MigrationFiles(fstest.MapFS{
		"db/migrations/readme.md": {Data: []byte("not sql")},
		"other/00001.sql":         {Data: []byte("CREATE TABLE t ();")},
	})
	require.ErrorContains(t, err, "no db/migrations")
}

func TestMigrationFilesReadError(t *testing.T) {
	// A directory whose name matches the glob (fs.Glob matches dirs too) makes
	// fs.ReadFile fail; the error must propagate, not be swallowed. Pin the
	// path in the error so this exercises the read branch, not the
	// no-files-matched branch.
	_, err := MigrationFiles(fstest.MapFS{
		"db/migrations/baddir.sql": {Mode: fs.ModeDir},
	})
	require.ErrorContains(t, err, "baddir.sql")
}

func TestWalkCreateTableStatements(t *testing.T) {
	sql := `
CREATE TABLE alpha (
  id TEXT -- a ; semicolon inside a comment must not terminate the statement
) COLLATE=utf8mb4_bin;
CREATE INDEX idx ON alpha (id);
CREATE TABLE bravo (n INT) COLLATE=utf8mb4_bin;
`
	var got []string
	err := WalkCreateTableStatements(sql, func(stmt string) { got = append(got, stmt) })
	require.NoError(t, err)
	// Statements are uppercased and span "CREATE TABLE" to the first real ';'.
	// The ';' inside the stripped comment must not end alpha; the CREATE INDEX
	// between the tables is not a CREATE TABLE, so it is skipped, not yielded.
	require.Len(t, got, 2)
	assert.Contains(t, got[0], "CREATE TABLE ALPHA")
	assert.Contains(t, got[0], "COLLATE=UTF8MB4_BIN")
	assert.NotContains(t, got[0], "CREATE INDEX", "alpha must end at its own ';', not swallow the following DDL")
	assert.Contains(t, got[1], "CREATE TABLE BRAVO")
}

func TestWalkCreateTableStatementsNoTables(t *testing.T) {
	// Empty input and DDL without a CREATE TABLE must yield nothing and no
	// error -- the collation guard's aggregate count sanity is what turns
	// "walked nothing" into a failure, so the walker itself stays silent.
	for _, sql := range []string{"", "CREATE INDEX idx ON alpha (id);\n-- comment only\n"} {
		err := WalkCreateTableStatements(sql, func(stmt string) {
			t.Errorf("unexpected statement yielded from %q: %s", sql, stmt)
		})
		require.NoError(t, err)
	}
}

func TestWalkCreateTableStatementsMissingSemicolon(t *testing.T) {
	// A CREATE TABLE with no terminating ';' is a malformed migration; the walk
	// must surface an error so the caller fails loudly instead of silently
	// skipping (or panicking on) the unterminated table.
	err := WalkCreateTableStatements("CREATE TABLE alpha (id TEXT)", func(string) {
		t.Error("no complete statement should be yielded for unterminated SQL")
	})
	require.ErrorContains(t, err, "missing terminating ;")
}

func TestWalkCreateTableStatementsToleratesWhitespace(t *testing.T) {
	// A future migration writing "CREATE  TABLE" with two spaces or a tab must
	// not hide the statement from the collation scan -- the CREATE TABLE anchor
	// tolerates any horizontal whitespace so the table's COLLATE clause is still
	// checked. A newline between the keywords is out of contract (a line-shape
	// blind spot shared with WalkCreateTableColumns, backstopped by the live
	// twin), so it is not asserted here.
	for _, gap := range []string{"  ", "\t"} {
		var got []string
		err := WalkCreateTableStatements("CREATE"+gap+"TABLE gamma (n INT) COLLATE=utf8mb4_bin;", func(stmt string) {
			got = append(got, stmt)
		})
		require.NoError(t, err, "gap=%q", gap)
		require.Len(t, got, 1, "gap=%q", gap)
		assert.Contains(t, got[0], "TABLE GAMMA", "gap=%q", gap)
		assert.Contains(t, got[0], "COLLATE=UTF8MB4_BIN", "gap=%q", gap)
	}
}

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

func TestWalkCreateTableColumnsToleratesWhitespace(t *testing.T) {
	// A future migration writing "CREATE  TABLE" with two spaces or a tab must
	// still be walked, so the table's columns are not silently dropped from the
	// decltype scan. A newline between the keywords is out of contract (a
	// line-shape blind spot shared with WalkCreateTableStatements, backstopped
	// by the live twin), so it is not asserted here.
	for _, gap := range []string{"  ", "\t"} {
		schema := "CREATE" + gap + "TABLE charlie (\n  touched_at DATETIME(3)\n);\n"
		var got [][2]string
		WalkCreateTableColumns(schema, func(name, typeTok string) {
			got = append(got, [2]string{name, typeTok})
		})
		assert.Equal(t, [][2]string{{"touched_at", "DATETIME(3)"}}, got, "gap=%q", gap)
	}
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
