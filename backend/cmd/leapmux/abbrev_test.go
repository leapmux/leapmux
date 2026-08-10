package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMatchCommandToken pins the btrfs-style abbreviation algorithm: exact
// match wins outright, a unique prefix resolves to it, an ambiguous prefix
// lists every candidate, and a total miss reports "unknown".
func TestMatchCommandToken(t *testing.T) {
	t.Run("exact match wins over longer prefixed names", func(t *testing.T) {
		// "list" is also a prefix of "list-sessions", but exact wins.
		matched, candidates, err := matchCommandToken("list", []string{"list", "list-sessions"})
		require.NoError(t, err)
		assert.Equal(t, "list", matched)
		assert.Nil(t, candidates)
	})

	t.Run("unique prefix resolves", func(t *testing.T) {
		matched, _, err := matchCommandToken("lis", []string{"list", "create", "delete"})
		require.NoError(t, err)
		assert.Equal(t, "list", matched)
	})

	t.Run("single-character prefix resolves when unique", func(t *testing.T) {
		matched, _, err := matchCommandToken("s", []string{"solo", "hub", "admin"})
		require.NoError(t, err)
		assert.Equal(t, "solo", matched)
	})

	t.Run("ambiguous prefix lists all candidates", func(t *testing.T) {
		// "lo" is a prefix of both "login" and "logout".
		matched, candidates, err := matchCommandToken("lo", []string{"login", "logout", "list", "status"})
		require.Error(t, err)
		assert.Equal(t, "", matched)
		assert.ElementsMatch(t, []string{"login", "logout"}, candidates)
		assert.Contains(t, err.Error(), "ambiguous")
		assert.Contains(t, err.Error(), "login")
		assert.Contains(t, err.Error(), "logout")
	})

	t.Run("ambiguous prefix across subcommands", func(t *testing.T) {
		// "re" is a prefix of "rename" and "remove".
		matched, _, err := matchCommandToken("re", []string{"rename", "remove", "list", "get"})
		require.Error(t, err)
		assert.Equal(t, "", matched)
	})

	t.Run("total miss reports unknown", func(t *testing.T) {
		matched, candidates, err := matchCommandToken("xyz", []string{"list", "get", "create"})
		require.Error(t, err)
		assert.Equal(t, "", matched)
		assert.Nil(t, candidates)
		assert.Contains(t, err.Error(), "unknown command")
	})

	t.Run("full name exact match among siblings", func(t *testing.T) {
		matched, _, err := matchCommandToken("get", []string{"get", "grant-admin", "list"})
		require.NoError(t, err)
		assert.Equal(t, "get", matched)
	})

	t.Run("grant-admin disambiguates from get with gr", func(t *testing.T) {
		// "gr" is a prefix of "grant-admin" but NOT of "get".
		matched, _, err := matchCommandToken("gr", []string{"get", "grant-admin", "list"})
		require.NoError(t, err)
		assert.Equal(t, "grant-admin", matched)
	})

	t.Run("empty names list is unknown", func(t *testing.T) {
		_, _, err := matchCommandToken("anything", nil)
		require.Error(t, err)
	})

	t.Run("exact match against empty arg is not a prefix false-positive", func(t *testing.T) {
		// An empty arg is a prefix of everything; it must be ambiguous,
		// not silently resolve to the first name.
		matched, candidates, err := matchCommandToken("", []string{"solo", "hub", "admin"})
		require.Error(t, err)
		assert.Equal(t, "", matched)
		assert.Len(t, candidates, 3)
	})
}

// TestDispatchAdminGroup_Abbreviation verifies the integrated dispatcher
// resolves abbreviated subcommands and leaf commands through the real
// adminTree, and that ambiguity surfaces as an error.
func TestDispatchAdminGroup_Abbreviation(t *testing.T) {
	t.Run("abbreviated subgroup resolves", func(t *testing.T) {
		// "use" is a unique prefix of "user".
		err := dispatchAdminGroup(adminTree, []string{"use", "list", "--data-dir", "/nonexistent"},
			[]string{"admin"})
		// The dispatch reaches runUserList, which fails on the bad data dir —
		// that's expected; the important thing is no "unknown group" error.
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "unknown admin group: use")
	})

	t.Run("abbreviated leaf command resolves", func(t *testing.T) {
		// "lis" is a unique prefix of "list" within the user group.
		err := dispatchAdminGroup(adminTree, []string{"user", "lis", "--data-dir", "/nonexistent"},
			[]string{"admin"})
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "unknown admin user command: lis")
	})

	t.Run("ambiguous subgroup prefix errors", func(t *testing.T) {
		// "s" is a prefix of "session" only among admin subgroups — but we
		// need a genuinely ambiguous case. "de" is not ambiguous because only
		// "delegation-token" starts with "de". Use a contrived tree instead.
		tree := adminGroup{
			Name: "test",
			Subgroups: []adminGroup{
				{Name: "subvolume"},
				{Name: "subtree"},
			},
		}
		err := dispatchAdminGroup(tree, []string{"sub"}, []string{"test"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ambiguous")
	})
}

// TestRunCLI_TopLevelAbbreviation verifies the top-level command dispatch
// resolves unambiguous prefixes.
func TestRunCLI_TopLevelAbbreviation(t *testing.T) {
	t.Run("abbreviated solo dispatches", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		var calls []cliCall
		code := runCLI([]string{"sol"}, &stdout, &stderr, testCLIRunners(&calls))
		assert.Equal(t, 0, code)
		require.Len(t, calls, 1)
		assert.Equal(t, "solo", calls[0].command)
		assert.True(t, calls[0].soloMode)
	})

	t.Run("abbreviated version dispatches", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runCLI([]string{"ver"}, &stdout, &stderr, testCLIRunners(&[]cliCall{}))
		assert.Equal(t, 0, code)
		assert.Equal(t, "test-version\n", stdout.String())
	})

	t.Run("abbreviated admin dispatches", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		var calls []cliCall
		code := runCLI([]string{"adm", "user", "list"}, &stdout, &stderr, testCLIRunners(&calls))
		assert.Equal(t, 0, code)
		require.Len(t, calls, 1)
		assert.Equal(t, "admin", calls[0].command)
	})

	t.Run("abbreviated control dispatches", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		var calls []cliCall
		// "c" is a unique prefix of "control" among top-level commands.
		code := runCLI([]string{"c", "whoami"}, &stdout, &stderr, testCLIRunners(&calls))
		assert.Equal(t, 0, code)
		require.Len(t, calls, 1)
		assert.Equal(t, "control", calls[0].command)
	})

	t.Run("abbreviated dev dispatches", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		var calls []cliCall
		code := runCLI([]string{"d"}, &stdout, &stderr, testCLIRunners(&calls))
		assert.Equal(t, 0, code)
		require.Len(t, calls, 1)
		assert.Equal(t, "solo", calls[0].command)
		assert.False(t, calls[0].soloMode)
	})

	t.Run("full command names still work", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		var calls []cliCall
		code := runCLI([]string{"hub"}, &stdout, &stderr, testCLIRunners(&calls))
		assert.Equal(t, 0, code)
		require.Len(t, calls, 1)
		assert.Equal(t, "hub", calls[0].command)
	})
}

// TestRunCLI_TopLevelAmbiguity verifies that an ambiguous top-level prefix
// is rejected with an error listing the candidates, and that an unknown
// command (no prefix match at all) reports "unknown command".
func TestRunCLI_TopLevelAmbiguity(t *testing.T) {
	// Among {solo, hub, worker, dev, admin, control, version}, "s" is unique
	// (only "solo"), so there are no genuine ambiguous top-level prefixes.
	// Test the ambiguity path via the matchCommandToken unit test instead,
	// and exercise the unknown-command path end-to-end here.
	t.Run("unknown command reports error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runCLI([]string{"xyz"}, &stdout, &stderr, testCLIRunners(&[]cliCall{}))
		assert.Equal(t, 1, code)
		assert.Contains(t, stderr.String(), "unknown command: xyz")
	})

	t.Run("help flag still works before prefix matching", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runCLI([]string{"--help"}, &stdout, &stderr, testCLIRunners(&[]cliCall{}))
		assert.Equal(t, 0, code)
		assert.Equal(t, usageText, stdout.String())
	})

	t.Run("flag-like arg is rejected before prefix matching", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runCLI([]string{"-x"}, &stdout, &stderr, testCLIRunners(&[]cliCall{}))
		assert.Equal(t, 1, code)
		assert.Contains(t, stderr.String(), "not a top-level flag")
	})
}

// TestWalkAdminArgs_Abbreviation verifies the help/validation walk also
// resolves abbreviated names, so `leapmux admin use --help` shows the
// "user" group's help (not "unknown group").
func TestWalkAdminArgs_Abbreviation(t *testing.T) {
	t.Run("abbreviated subgroup shows help", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		// "use" uniquely abbreviates "user"; the walk should descend into
		// the user subgroup and print its usage.
		code, handled := walkAdminArgs(adminTree, []string{"admin"}, []string{"use", "--help"}, &stdout, &stderr)
		assert.True(t, handled)
		assert.Equal(t, 0, code)
		// The user group's help includes its command list.
		assert.Contains(t, stdout.String(), "List users")
	})

	t.Run("abbreviated subgroup without command prints error+usage", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code, handled := walkAdminArgs(adminTree, []string{"admin"}, []string{"use"}, &stdout, &stderr)
		assert.True(t, handled)
		assert.Equal(t, 1, code)
		assert.Contains(t, stderr.String(), "command is required")
	})

	t.Run("unknown subgroup prints error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code, handled := walkAdminArgs(adminTree, []string{"admin"}, []string{"xyz"}, &stdout, &stderr)
		assert.True(t, handled)
		assert.Equal(t, 1, code)
		assert.Contains(t, stderr.String(), "unknown admin group: xyz")
	})
}

// TestFormatAdminGroupUsage_AbbreviationNote verifies the help text tells
// users they can abbreviate.
func TestFormatAdminGroupUsage_AbbreviationNote(t *testing.T) {
	usage := formatAdminGroupUsage(adminTree, "admin")
	assert.Contains(t, usage, "Any command name can be shortened as far as it stays unambiguous.")
}

// TestUsageText_AbbreviationNote verifies the top-level usage tells users
// they can abbreviate.
func TestUsageText_AbbreviationNote(t *testing.T) {
	assert.True(t, strings.Contains(usageText,
		"Any command name can be shortened as far as it stays unambiguous."))
}
