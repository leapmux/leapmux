package storetest

import "strings"

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
		if strings.HasPrefix(upper, "CREATE TABLE") {
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
