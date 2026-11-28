package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
)

func TestRunCLIRecoverLeafHelpIncludesDescription(t *testing.T) {
	o := runTestCLI(t, "recover", "reset-password", "--help")

	assert.Equal(t, cli.ExitOK, o.code)
	assert.Empty(t, o.stderr)
	assert.Contains(t, o.stdout, "Reset a user's password and revoke all their sessions\n")
	assert.Contains(t, o.stdout, "Usage:\n  leapmux recover reset-password <user> [flags]\n")
	assert.Contains(t, o.stdout, `<user>  A username, "id:<id>" for a user id, or "username:<name>".`)
	assert.Contains(t, o.stdout, "--password string")
}

// TestRecoverRootHelpGolden pins the recover root's help as a literal, byte
// for byte. This text is what an operator reads with the hub down and what
// the runbooks quote, so it is pinned independently of the renderer.
func TestRecoverRootHelpGolden(t *testing.T) {
	want := "Offline break-glass recovery (opens the database directly).\n" +
		"\n" +
		"Each command works on the database and the encryption key file of a Hub, solo or\n" +
		"dev instance, with no running Hub, no network call and no login. --data-dir\n" +
		"selects the instance; --config reads the storage settings of a Hub on PostgreSQL\n" +
		"or MySQL. While the Hub serves, use \"leapmux admin\" instead.\n" +
		"\n" +
		"Usage:\n" +
		"  leapmux recover <command>\n" +
		"\n" +
		"Commands:\n" +
		"  create-admin    Create the initial administrator (refuses once any admin exists)\n" +
		"  reset-password  Reset a user's password and revoke all their sessions\n" +
		"  encryption-key  Manage encryption keys\n" +
		"  db              Database utilities\n" +
		"\n" +
		"Global Flags:\n" +
		"      --color string   when to emit ANSI colors on TTY output (one of: auto, always, never; default: auto)\n" +
		"      --fields string  comma-separated columns or fields to print\n" +
		"      --format string  output format (one of: text, json, ndjson, tsv; default: text)\n" +
		"      --json           print the result as one JSON envelope per line\n" +
		"      --no-header      omit the header row from table and TSV output\n" +
		"  -q, --quiet          print only the ids of the result, one per line\n" +
		"  -w, --wide           do not truncate table columns\n" +
		"\n" +
		"Run \"leapmux recover <command> --help\" for the help of a command.\n" +
		"Any command name can be shortened as far as it stays unambiguous. A later\n" +
		"release can make an ad-hoc prefix ambiguous, so scripts use full names or\n" +
		"documented aliases (such as ag, term, ws, ls, rm, mv).\n"
	o := runTestCLI(t, "recover", "--help")
	require.Equal(t, cli.ExitOK, o.code, o.stderr)
	assert.Equal(t, want, o.stdout)

	bare := runTestCLI(t, "recover")
	assert.Equal(t, cli.ExitOK, bare.code, "a group alone prints its help")
	assert.Equal(t, want, bare.stdout)
}

// TestRecoverGroupHelpGolden pins a recover subgroup's help output.
func TestRecoverGroupHelpGolden(t *testing.T) {
	want := "Database utilities\n" +
		"\n" +
		"Usage:\n" +
		"  leapmux recover db <command>\n" +
		"\n" +
		"Commands:\n" +
		"  path     Print the database path\n" +
		"  migrate  Run schema migrations\n" +
		"  version  Show current schema version\n" +
		"\n" +
		"Global Flags:\n" +
		"      --color string   when to emit ANSI colors on TTY output (one of: auto, always, never; default: auto)\n" +
		"      --fields string  comma-separated columns or fields to print\n" +
		"      --format string  output format (one of: text, json, ndjson, tsv; default: text)\n" +
		"      --json           print the result as one JSON envelope per line\n" +
		"      --no-header      omit the header row from table and TSV output\n" +
		"  -q, --quiet          print only the ids of the result, one per line\n" +
		"  -w, --wide           do not truncate table columns\n" +
		"\n" +
		"Run \"leapmux recover db <command> --help\" for the help of a command.\n" +
		"Any command name can be shortened as far as it stays unambiguous. A later\n" +
		"release can make an ad-hoc prefix ambiguous, so scripts use full names or\n" +
		"documented aliases (such as ag, term, ws, ls, rm, mv).\n"
	o := runTestCLI(t, "recover", "db", "--help")
	require.Equal(t, cli.ExitOK, o.code, o.stderr)
	assert.Equal(t, want, o.stdout)
}

func TestRunSoloRejectsUnknownFlag(t *testing.T) {
	err := runModeCommand("solo", []string{"run", "--unknown-flag"})
	require.Error(t, err)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
	assert.Contains(t, err.Error(), "unknown flag: --unknown-flag")
}

// An entry point that runs before the command tree reports its failure in
// the form that the root prints every failure, with the exit status of its
// code.
func TestReportEntryFailure(t *testing.T) {
	var stderr bytes.Buffer
	code := reportEntryFailure(&stderr, errors.New("invalid Windows service entry"))
	assert.Equal(t, cli.ExitFailure, code)
	assert.Equal(t, "error: internal: invalid Windows service entry\n", stderr.String())

	stderr.Reset()
	code = reportEntryFailure(&stderr, cli.Usagef("unknown helper"))
	assert.Equal(t, cli.ExitUsage, code)
	assert.Equal(t, "error: usage: unknown helper\n", stderr.String())
}
