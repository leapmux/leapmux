package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliCall struct {
	command string
	args    []string
}

func testCLIRunners(calls *[]cliCall) cliRunners {
	record := func(command string, args []string) {
		*calls = append(*calls, cliCall{
			command: command,
			args:    append([]string(nil), args...),
		})
	}
	return cliRunners{
		runMode: func(mode string, args []string) error {
			record(mode, args)
			return nil
		},
		runRecover: func(args []string) error {
			record("recover", args)
			return nil
		},
		version: func() string {
			return "test-version"
		},
	}
}

func TestRunCLIRecoverLeafHelpIncludesDescription(t *testing.T) {
	var stderr bytes.Buffer
	runners := testCLIRunners(&[]cliCall{})
	runners.runRecover = func(args []string) error {
		return runRecover(args)
	}

	var code int
	stdout := testutil.CaptureStdout(t, func() {
		code = runCLI(context.Background(), []string{"recover", "password", "reset", "--help"}, strings.NewReader(""), io.Discard, &stderr, runners)
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

func TestRunSoloRejectsUnknownFlag(t *testing.T) {
	err := runModeCommand("solo", []string{"run", "--unknown-flag"})
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
