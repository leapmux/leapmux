package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliCall struct {
	command  string
	args     []string
	soloMode bool
}

func testCLIRunners(calls *[]cliCall) cliRunners {
	record := func(command string, args []string, soloMode bool) {
		*calls = append(*calls, cliCall{
			command:  command,
			args:     append([]string(nil), args...),
			soloMode: soloMode,
		})
	}
	return cliRunners{
		runHub: func(args []string) error {
			record("hub", args, false)
			return nil
		},
		runWorker: func(args []string) error {
			record("worker", args, false)
			return nil
		},
		runSolo: func(args []string, soloMode bool) error {
			record("solo", args, soloMode)
			return nil
		},
		runRecover: func(args []string) error {
			record("recover", args, false)
			return nil
		},
		runControl: func(args []string) error {
			record("control", args, false)
			return nil
		},
		version: func() string {
			return "test-version"
		},
	}
}

func TestRunCLIExplicitRouting(t *testing.T) {
	recoverUsageText := formatGroupUsage(recoverTree, "recover")
	recoverPasswordUsageText := formatGroupUsage(findTestGroup(t, recoverTree, "password"), "recover password")
	recoverEncryptionUsageText := formatGroupUsage(findTestGroup(t, recoverTree, "encryption-key"), "recover encryption-key")

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
		wantCalls  []cliCall
	}{
		{
			name:       "bare command is rejected",
			args:       nil,
			wantCode:   1,
			wantStderr: `hint: use "leapmux solo"`,
		},
		{
			name:       "top-level solo flag is rejected",
			args:       []string{"-dev-frontend", "http://localhost:4328"},
			wantCode:   1,
			wantStderr: `hint: use "leapmux solo -dev-frontend"`,
		},
		{
			name:       "top-level help flag prints help",
			args:       []string{"--help"},
			wantCode:   0,
			wantStdout: usageText,
		},
		{
			name:       "top-level help command prints help",
			args:       []string{"help"},
			wantCode:   0,
			wantStdout: usageText,
		},
		{
			name:     "solo dispatches to solo mode",
			args:     []string{"solo", "-dev-frontend", "http://localhost:4328"},
			wantCode: 0,
			wantCalls: []cliCall{{
				command:  "solo",
				args:     []string{"-dev-frontend", "http://localhost:4328"},
				soloMode: true,
			}},
		},
		{
			name:     "solo version flag dispatches through solo",
			args:     []string{"solo", "--version"},
			wantCode: 0,
			wantCalls: []cliCall{{
				command:  "solo",
				args:     []string{"--version"},
				soloMode: true,
			}},
		},
		{
			name:     "dev dispatches to dev mode",
			args:     []string{"dev", "-dev-frontend", "http://localhost:4328"},
			wantCode: 0,
			wantCalls: []cliCall{{
				command:  "solo",
				args:     []string{"-dev-frontend", "http://localhost:4328"},
				soloMode: false,
			}},
		},
		{
			name:     "hub dispatches unchanged",
			args:     []string{"hub", "-listen", ":4327"},
			wantCode: 0,
			wantCalls: []cliCall{{
				command: "hub",
				args:    []string{"-listen", ":4327"},
			}},
		},
		{
			name:     "worker dispatches unchanged",
			args:     []string{"worker", "--hub", "https://hub.example.com"},
			wantCode: 0,
			wantCalls: []cliCall{{
				command: "worker",
				args:    []string{"--hub", "https://hub.example.com"},
			}},
		},
		{
			name:     "recover dispatches unchanged",
			args:     []string{"recover", "db", "path"},
			wantCode: 0,
			wantCalls: []cliCall{{
				command: "recover",
				args:    []string{"db", "path"},
			}},
		},
		{
			name:       "recover without group prints recover usage without dispatching",
			args:       []string{"recover"},
			wantCode:   1,
			wantStderr: "Usage: leapmux recover <group> <command> [flags]",
		},
		{
			name:       "recover help prints recover usage without dispatching",
			args:       []string{"recover", "--help"},
			wantCode:   0,
			wantStdout: recoverUsageText,
		},
		{
			name:       "recover group without command prints group usage without dispatching",
			args:       []string{"recover", "password"},
			wantCode:   1,
			wantStderr: "Usage: leapmux recover password <command> [flags]",
		},
		{
			name:       "recover group help prints group usage without dispatching",
			args:       []string{"recover", "password", "--help"},
			wantCode:   0,
			wantStdout: recoverPasswordUsageText,
		},
		{
			name:       "unknown recover group prints clean error without dispatching",
			args:       []string{"recover", "bogus"},
			wantCode:   1,
			wantStderr: "unknown recover group: bogus",
		},
		{
			name:       "unknown recover group command prints group usage without dispatching",
			args:       []string{"recover", "password", "bogus"},
			wantCode:   1,
			wantStderr: "unknown recover password command: bogus",
		},
		{
			name:       "the old admin tree is gone",
			args:       []string{"admin", "user", "list"},
			wantCode:   1,
			wantStderr: "unknown command: admin",
		},
		{
			name:       "recover encryption-key group help prints nested usage without dispatching",
			args:       []string{"recover", "encryption-key", "help"},
			wantCode:   0,
			wantStdout: recoverEncryptionUsageText,
		},
		{
			name:       "unknown recover encryption-key command prints nested usage without dispatching",
			args:       []string{"recover", "encryption-key", "bogus"},
			wantCode:   1,
			wantStderr: "unknown recover encryption-key command: bogus",
		},
		{
			name:       "unknown command is rejected",
			args:       []string{"bogus"},
			wantCode:   1,
			wantStderr: "unknown command: bogus",
		},
		{
			name:       "version command prints version",
			args:       []string{"version"},
			wantCode:   0,
			wantStdout: "test-version\n",
		},
		{
			name:       "version help prints usage",
			args:       []string{"version", "--help"},
			wantCode:   0,
			wantStdout: "Print version and exit.\n\nUsage: leapmux version\n",
		},
		{
			name:       "top-level long version flag prints version",
			args:       []string{"--version"},
			wantCode:   0,
			wantStdout: "test-version\n",
		},
		{
			name:       "top-level short version flag prints version",
			args:       []string{"-version"},
			wantCode:   0,
			wantStdout: "test-version\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			var calls []cliCall

			code := runCLI(tt.args, &stdout, &stderr, testCLIRunners(&calls))

			assert.Equal(t, tt.wantCode, code)
			assert.Equal(t, tt.wantStdout, stdout.String())
			assert.Contains(t, stderr.String(), tt.wantStderr)
			assert.Equal(t, tt.wantCalls, calls)
		})
	}
}

func TestRunCLISubcommandErrorReportsPlainly(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runners := testCLIRunners(&[]cliCall{})
	runners.runHub = func([]string) error {
		return fmt.Errorf("simulated startup failure")
	}

	code := runCLI([]string{"hub"}, &stdout, &stderr, runners)

	assert.Equal(t, 1, code)
	assert.Equal(t, "", stdout.String())
	assert.Equal(t, "error: simulated startup failure\n", stderr.String())
}

func TestRunCLISubcommandHelpReturnsSuccess(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runners := testCLIRunners(&[]cliCall{})
	runners.runHub = func([]string) error {
		_, _ = stdout.WriteString("hub help\n")
		return flag.ErrHelp
	}

	code := runCLI([]string{"hub", "--help"}, &stdout, &stderr, runners)

	assert.Equal(t, 0, code)
	assert.Equal(t, "", stderr.String())
	assert.Contains(t, stdout.String(), "hub help")
}

func TestRunCLIRecoverLeafHelpIncludesDescription(t *testing.T) {
	var stderr bytes.Buffer
	runners := testCLIRunners(&[]cliCall{})
	runners.runRecover = func(args []string) error {
		return runRecover(args)
	}

	var code int
	stdout := testutil.CaptureStdout(t, func() {
		code = runCLI([]string{"recover", "password", "reset", "--help"}, io.Discard, &stderr, runners)
	})

	assert.Equal(t, 0, code)
	assert.Equal(t, "", stderr.String())
	assert.Contains(t, stdout, "Reset a user's password and revoke all their sessions.")
	assert.Contains(t, stdout, "Usage: leapmux recover password reset [flags]")
	assert.Contains(t, stdout, "-username string")
}

// TestFormatGroupUsage_RecoverRootGolden pins the recover root's help
// as a literal, byte for byte.
//
// The routing table above compares stdout against formatGroupUsage
// itself, so it cannot see a change in the renderer -- both sides move
// together. This text is what an operator reads with the hub down and what
// the runbooks quote, so it is pinned independently. The recover tree holds
// only groups, so the header stays "Groups:" and the usage line asks for two
// more tokens.
func TestFormatGroupUsage_RecoverRootGolden(t *testing.T) {
	want := "Offline break-glass recovery (opens the database directly).\n" +
		"\n" +
		"Usage: leapmux recover <group> <command> [flags]\n" +
		"\n" +
		"Groups:\n" +
		"  bootstrap         First-run recovery on an empty hub (online administration: `leapmux control admin user create`)\n" +
		"  password          Reset passwords offline\n" +
		"  encryption-key    Manage encryption keys\n" +
		"  db                Database utilities\n" +
		"\n" +
		"Any command name can be shortened as far as it stays unambiguous.\n"
	got := formatGroupUsage(recoverTree, "recover")
	assert.Equal(t, want, got)
	assert.NotContains(t, got, "Commands:", "the recover root holds no command of its own")
}

// TestFormatGroupUsage_RecoverLeafGroupGolden pins a group that holds
// only commands: one "Commands:" header, no "Groups:" header, and a usage
// line that asks for exactly one more token.
func TestFormatGroupUsage_RecoverLeafGroupGolden(t *testing.T) {
	want := "Reset passwords offline.\n" +
		"\n" +
		"Usage: leapmux recover password <command> [flags]\n" +
		"\n" +
		"Commands:\n" +
		"  reset             Reset a user's password and revoke all their sessions\n" +
		"\n" +
		"Any command name can be shortened as far as it stays unambiguous.\n"
	got := formatGroupUsage(findTestGroup(t, recoverTree, "password"), "recover password")
	assert.Equal(t, want, got)
	assert.NotContains(t, got, "Groups:", "the password group holds no subgroup")
}

// TestUsageText_Golden pins the top-level help as a literal, byte for byte.
//
// The routing table above compares stdout against usageText itself, so it
// cannot see a change in the renderer -- both sides move together. This is
// the first text a new operator reads, so the column width, the blank lines,
// and the order of the rows are pinned here instead.
func TestUsageText_Golden(t *testing.T) {
	want := "Usage: leapmux <command> [flags]\n" +
		"\n" +
		"Commands:\n" +
		"  solo      Run Hub + Worker locally for single-user use\n" +
		"  hub       Run the Hub service\n" +
		"  worker    Run a Worker connected to a Hub\n" +
		"  dev       Run Hub + Worker for development\n" +
		"  recover   Offline break-glass recovery (opens the database directly)\n" +
		"  control   Remotely control LeapMux from a script or another LeapMux agent\n" +
		"  version   Print version and exit\n" +
		"\n" +
		"Common options:\n" +
		"  -h, --help     Print help and exit\n" +
		"  -version       Print version and exit\n" +
		"  --version      Print version and exit\n" +
		"\n" +
		"Any command name can be shortened as far as it stays unambiguous.\n"
	assert.Equal(t, want, usageText)
}

// cliReferenceUsagePath is the reference page that reproduces the top-level
// usage. The path is relative to this package's directory.
const cliReferenceUsagePath = "../../../site/content/docs/reference/cli-reference.md"

// TestUsageText_MatchesTheReferenceDoc proves the reference page quotes the
// usage the binary actually prints.
//
// The page reproduces the whole block verbatim, and a reader takes it as the
// output. It drifted twice already: a rename of the `control` summary left the
// page quoting a sentence the binary no longer printed, and the page never
// carried the abbreviation line at all. Neither drift could fail a test,
// because TestUsageText_Golden pins the Go side only.
func TestUsageText_MatchesTheReferenceDoc(t *testing.T) {
	page, err := os.ReadFile(cliReferenceUsagePath)
	require.NoError(t, err, "the reference page has to be readable from this package")

	// The block is the one fence that opens with the usage line. Take it by
	// its own first line rather than by a fence count, so an added fence
	// earlier in the page does not move the match.
	const opening = "Usage: leapmux <command> [flags]\n"
	start := strings.Index(string(page), opening)
	require.GreaterOrEqual(t, start, 0, "the reference page no longer quotes the top-level usage")
	rest := string(page)[start:]
	end := strings.Index(rest, "```")
	require.GreaterOrEqual(t, end, 0, "the quoted usage block is not closed")

	assert.Equal(t, usageText, rest[:end],
		"the reference page and the binary have to print the same usage")
}

// TestUsageText_TreeCommandsQuoteTheirTree proves the top-level row of a
// command that owns a command tree carries that tree's own Summary.
//
// The two were hand-written copies, and they disagreed: the row said
// "Offline break-glass recovery (opens the database directly)" where
// recoverTree said "Offline break-glass recovery for a LeapMux hub". An
// operator read one sentence in `leapmux --help` and a different one in
// `leapmux recover --help`.
func TestUsageText_TreeCommandsQuoteTheirTree(t *testing.T) {
	rows := usageCommandRows(t)
	for _, tree := range []cmdGroup{recoverTree, controlTree} {
		t.Run(tree.Name, func(t *testing.T) {
			require.Contains(t, rows, tree.Name, "the tree needs a row in the top-level usage")
			assert.Equal(t, tree.Summary, rows[tree.Name])
		})
	}
}

// TestTopLevelCommands_EveryRowIsRouted proves runCLI's switch covers every
// row that the usage prints. A row that no case handles falls through the
// final `return 0` of runCLI: exit 0, nothing printed, nothing run.
func TestTopLevelCommands_EveryRowIsRouted(t *testing.T) {
	for _, c := range topLevelCommands {
		t.Run(c.Name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			var calls []cliCall

			_ = runCLI([]string{c.Name}, &stdout, &stderr, testCLIRunners(&calls))

			assert.NotContains(t, stderr.String(), "unknown command",
				"the matcher must resolve every row of the usage")
			// A daemon runs its runner; `version` prints; a tree without a
			// token prints its own help. Silence means no case matched.
			assert.True(t, len(calls) > 0 || stdout.Len() > 0 || stderr.Len() > 0,
				"a routed command runs a runner or prints something")
		})
	}
}

// usageCommandRows parses the "Commands:" block of usageText into name ->
// summary, so a test compares a row against the source that produced it
// without repeating the row's format string.
func usageCommandRows(t *testing.T) map[string]string {
	t.Helper()
	_, rest, found := strings.Cut(usageText, "\nCommands:\n")
	require.True(t, found, "usageText must hold a Commands: block")
	block, _, found := strings.Cut(rest, "\n\n")
	require.True(t, found, "the Commands: block must end at a blank line")

	rows := make(map[string]string)
	for _, line := range strings.Split(block, "\n") {
		name, summary, ok := strings.Cut(strings.TrimSpace(line), " ")
		require.True(t, ok, "row %q must hold a name and a summary", line)
		rows[name] = strings.TrimSpace(summary)
	}
	return rows
}

func TestRunSoloRejectsUnknownFlag(t *testing.T) {
	err := runSolo([]string{"--unknown-flag"}, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown-flag")
}

// findTestGroup walks root down path and returns the group it reaches. Both
// command trees use it, so a test names the group it means instead of
// indexing into Subgroups by position.
func findTestGroup(t *testing.T, root cmdGroup, path ...string) cmdGroup {
	t.Helper()
	g := root
	for _, name := range path {
		var next *cmdGroup
		for i := range g.Subgroups {
			if g.Subgroups[i].Name == name {
				next = &g.Subgroups[i]
				break
			}
		}
		require.NotNil(t, next, "%s tree: missing group %v", root.Name, path)
		g = *next
	}
	return g
}
