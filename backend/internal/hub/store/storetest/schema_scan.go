package storetest

import (
	"fmt"
	"io/fs"
	"regexp"
	"strings"
)

// MigrationFile is the name and contents of one embedded migration SQL file.
type MigrationFile struct {
	Name string // e.g. "db/migrations/00001_initial.sql"
	SQL  string
}

// MigrationFiles returns the name and contents of every *.sql file under
// db/migrations in fsys -- pass the dialect's embedded migration FS
// (migrate.go's //go:embed db/migrations/*.sql) so the static schema scans
// cover exactly the set of migration files newMigrator feeds goose, current
// and future. Using the embed FS (not os.ReadDir) scans the shipped artifact
// and is cwd-independent, so the scanned file set cannot drift from what ships.
// (On CockroachDB, newMigrator strips COLLATE "C" from the bytes it hands goose
// via transformFS; the raw file text read here is unaffected, and no static
// scan inspects that clause, so the divergence does not matter.) Returns an
// error when zero files match so a layout/pattern drift fails loudly instead of
// making every scan vacuously pass. Names from fs.Glob are sorted, so order is
// deterministic.
func MigrationFiles(fsys fs.FS) ([]MigrationFile, error) {
	names, err := fs.Glob(fsys, "db/migrations/*.sql")
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no db/migrations/*.sql files in fsys")
	}
	out := make([]MigrationFile, 0, len(names))
	for _, name := range names {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		out = append(out, MigrationFile{Name: name, SQL: string(b)})
	}
	return out, nil
}

// StripSQLLineComments removes a SQL line comment (-- to end of line) from
// each line. The migrations use only line comments (no /* */ block comments,
// no string literals containing --), so this is a faithful comment strip for
// the static schema scans: without it, a ';' inside a prose comment (e.g.
// "...issued; force-expires...") would split a CREATE TABLE body.
func StripSQLLineComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// WalkCreateTableStatements strips line comments from migrationSQL, uppercases
// it, and yields each CREATE TABLE statement -- the text from "CREATE TABLE" up
// to (not including) its terminating ";". It returns an error if a CREATE TABLE
// has no terminating ";" (a malformed migration) so the caller can fail loudly
// instead of silently skipping the unterminated table.
//
// This is the statement-level sibling of WalkCreateTableColumns: the MySQL
// collation static scan (mysql/schema_internal_test.go) uses it so the fragile
// "CREATE TABLE ... ;" text split lives -- and is unit-tested -- in one place
// next to the column-level walk, instead of open-coded inline in the test where
// a parser fix could land for one and miss the other. It matches CREATE TABLE
// tolerant of the horizontal whitespace between the two keywords (so a future
// "CREATE TABLE" written with two spaces or a tab is still caught) and splits on
// the first ";" after it, so a ";" inside a string literal or /* block comment */
// (neither of which the migrations use) would split a statement early; the
// integration-tagged live twins are the immune backstop.
func WalkCreateTableStatements(migrationSQL string, fn func(stmt string)) error {
	sql := strings.ToUpper(StripSQLLineComments(migrationSQL))
	idx := 0
	for {
		loc := createTableRe.FindStringIndex(sql[idx:])
		if loc == nil {
			break
		}
		i := idx + loc[0]
		semi := strings.Index(sql[i:], ";")
		if semi < 0 {
			return fmt.Errorf("CREATE TABLE at offset %d missing terminating ;", i)
		}
		fn(sql[i : i+semi])
		idx = i + semi
	}
	return nil
}

// createTableRe matches the CREATE TABLE keyword pair on an uppercased blob,
// tolerant of any horizontal whitespace (spaces or tabs) between the two words,
// so a future migration that does not use the single-space spelling is still
// scanned. It deliberately does NOT span a newline, staying consistent with the
// line-based WalkCreateTableColumns, which cannot match a keyword pair split
// across lines: a cross-line "CREATE\nTABLE" is a line-shape blind spot both
// walks share and the live twins backstop.
var createTableRe = regexp.MustCompile(`CREATE[ \t]+TABLE`)

// WalkCreateTableColumns strips line comments from migration SQL, walks every
// CREATE TABLE body line by line, and yields each line that tokenizes as a
// column definition: fn receives the column name (lowercased) and its type
// token (uppercased, trailing comma removed). Shared by the per-dialect static
// decltype scans (mysql/postgres schema_internal_test.go) so the fragile text
// parse lives once and a parser fix cannot land in one dialect and silently
// miss the other; the dialect-specific type assertions stay at each call site.
//
// Parsing limitation, by design: the walk assumes one column per line with the
// type as the second whitespace token, so a declaration wrapped across lines
// or an inline expression column is silently skipped. The integration-tagged
// live twins (TestMySQLDatetimeColumnsLive, TestPostgresTimestamptzColumnsLive)
// read the resolved types from information_schema and are immune to the
// migration text's line shape; the static scans exist so the everyday
// Docker-free suite still guards the decltype spelling.
func WalkCreateTableColumns(migrationSQL string, fn func(column, typeTok string)) {
	inTable := false
	for _, line := range strings.Split(StripSQLLineComments(migrationSQL), "\n") {
		upper := strings.ToUpper(strings.TrimSpace(line))
		// Match "CREATE TABLE" tolerant of the whitespace between the keywords
		// (Fields collapses any run) and stricter than HasPrefix, which would
		// also match "CREATE TABLEX".
		if flds := strings.Fields(upper); len(flds) >= 2 && flds[0] == "CREATE" && flds[1] == "TABLE" {
			inTable = true
			continue
		}
		if inTable && strings.HasPrefix(upper, ")") {
			inTable = false
			continue
		}
		if !inTable {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		fn(strings.ToLower(fields[0]), strings.ToUpper(strings.TrimSuffix(fields[1], ",")))
	}
}
