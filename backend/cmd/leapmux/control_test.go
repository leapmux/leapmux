package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHandleControlArgs_WorkerPinsShowsHelpWhenNoSubcommand pins the
// dispatch behaviour for `leapmux control worker pins` without a verb.
// Before the subgroup split, the single RunWorkerPins handler emitted
// a JSON `invalid_request` envelope, which was unfriendly. The fix
// makes `pins` a real Subgroup so the standard "missing subcommand"
// path prints help to stderr.
func TestHandleControlArgs_WorkerPinsShowsHelpWhenNoSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, handled := handleControlArgs([]string{"worker", "pins"}, &stdout, &stderr)
	assert.True(t, handled, "args=worker pins must be handled by walkGroupArgs, not dispatched as a leaf")
	assert.Equal(t, 1, code, "missing subcommand is an error -> exit 1")
	// Help block goes to stderr (alongside the missing-command notice);
	// stdout stays empty so a `... | jq` pipeline isn't fed garbage.
	assert.Empty(t, stdout.String(), "stdout must stay clean when help fires")
	help := stderr.String()
	assert.Contains(t, help, "control worker pins command is required")
	assert.Contains(t, help, "Manage TOFU worker key pins")
	assert.Contains(t, help, "list")
	assert.Contains(t, help, "show")
	assert.Contains(t, help, "remove")
	// The old monolithic handler used to surface this exact JSON error
	// envelope. Guard against a regression that re-introduces it.
	assert.NotContains(t, help, `"error"`)
	assert.NotContains(t, help, "invalid_request")
}

// TestHandleControlArgs_WorkerPinsListIsLeaf confirms the verb actually
// dispatches to the leaf handler (returns handled=false so the
// dispatcher runs the registered Run func), not back into the help
// path. Without this, a refactor that turned "list" into a subgroup
// would silently break every existing script.
func TestHandleControlArgs_WorkerPinsListIsLeaf(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, handled := handleControlArgs([]string{"worker", "pins", "list"}, &stdout, &stderr)
	assert.False(t, handled, "args=worker pins list must reach the leaf dispatch path")
	assert.Equal(t, 0, code)
	assert.Empty(t, stdout.String())
	assert.Empty(t, stderr.String())
}

// TestFormatGroupUsage_ControlRootSplitsCommandsFromGroups pins the two
// headers of `leapmux control`.
//
// whoami and version run on their own. The other twelve rows are groups that
// need one more token at least. One flat "Commands:" list over all fourteen
// told the operator that `leapmux control admin` runs, and it does not.
func TestFormatGroupUsage_ControlRootSplitsCommandsFromGroups(t *testing.T) {
	usage := formatGroupUsage(controlTree, "control")

	assert.Contains(t, usage, "Remotely control LeapMux from a script or another LeapMux agent.\n")
	assert.Contains(t, usage, "Usage: leapmux control <command> [flags]\n")

	commands := strings.Index(usage, "\nCommands:\n")
	groups := strings.Index(usage, "\nGroups:\n")
	require.NotEqual(t, -1, commands, "the root's own commands need a Commands: header")
	require.Greater(t, groups, commands, "the subgroups need a Groups: header of their own, below the commands")

	// Each row is "  %-18s%s", so "\n  <name> " matches the name column only.
	// A bare substring would also hit the same word inside a summary.
	commandBlock := usage[commands:groups]
	assert.Contains(t, commandBlock, "\n  whoami ")
	assert.Contains(t, commandBlock, "\n  version ")
	assert.NotContains(t, commandBlock, "\n  admin ", "a group must never be listed as a command")
	assert.NotContains(t, commandBlock, "\n  workspace ")

	groupBlock := usage[groups:]
	assert.Contains(t, groupBlock, "\n  admin ")
	assert.Contains(t, groupBlock, "\n  workspace ")
	assert.NotContains(t, groupBlock, "\n  whoami ", "a command must never be listed as a group")
}

// TestFormatGroupUsage_ControlAdminIsAGroupOfGroups pins the help of a
// group that holds subgroups only. It asks for two more tokens and prints a
// Groups: header. It used to print "<command>" over a list in which every
// row was another group.
func TestFormatGroupUsage_ControlAdminIsAGroupOfGroups(t *testing.T) {
	usage := formatGroupUsage(findTestGroup(t, controlTree, "admin"), "control admin")

	assert.Contains(t, usage, "Usage: leapmux control admin <group> <command> [flags]\n")
	assert.Contains(t, usage, "\nGroups:\n")
	assert.NotContains(t, usage, "\nCommands:\n", "the admin group holds no command of its own")
	assert.Contains(t, usage, "\n  user ")
	assert.Contains(t, usage, "\n  session ")
}

// TestHandleControlArgs_RootWithoutArgsPrintsBothSections runs the walk that
// `leapmux control` reaches with no argument. It reports the missing token
// and prints the same two-section help.
func TestHandleControlArgs_RootWithoutArgsPrintsBothSections(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, handled := handleControlArgs(nil, &stdout, &stderr)
	assert.True(t, handled)
	assert.Equal(t, 1, code)
	assert.Empty(t, stdout.String(), "stdout must stay clean when help fires")

	out := stderr.String()
	assert.Contains(t, out, "error: control command is required")
	assert.Contains(t, out, "\nCommands:\n")
	assert.Contains(t, out, "\nGroups:\n")
}

// TestHandleControlArgs_AdminWithoutArgsAsksForAGroup pins the noun for a
// group of groups. `leapmux control admin` needs a GROUP next, and the
// refusal and the usage line must agree about that.
func TestHandleControlArgs_AdminWithoutArgsAsksForAGroup(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, handled := handleControlArgs([]string{"admin"}, &stdout, &stderr)
	assert.True(t, handled)
	assert.Equal(t, 1, code)
	assert.Empty(t, stdout.String())

	out := stderr.String()
	assert.Contains(t, out, "error: control admin group is required")
	assert.Contains(t, out, "Usage: leapmux control admin <group> <command> [flags]\n")
	assert.Contains(t, out, "\nGroups:\n")
}

// TestControlUnresolvedTokenNoun pins the noun in the refusal for a token
// that resolves to nothing, at both layers.
//
// The control root runs whoami and version directly, so an unresolved token
// there is a command. The old wording said "unknown control group: xyz" and
// sent the operator after a group that the token never had to be. Under
// `control admin`, where every child IS a group, the noun stays "group".
func TestControlUnresolvedTokenNoun(t *testing.T) {
	t.Run("the root refuses a command, not a group", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code, handled := handleControlArgs([]string{"xyz"}, &stdout, &stderr)
		assert.True(t, handled)
		assert.Equal(t, 1, code)
		assert.Empty(t, stdout.String())
		assert.Contains(t, stderr.String(), "unknown control command: xyz")
		assert.NotContains(t, stderr.String(), "unknown control group")
	})

	t.Run("a group of groups refuses a group", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code, handled := handleControlArgs([]string{"admin", "xyz"}, &stdout, &stderr)
		assert.True(t, handled)
		assert.Equal(t, 1, code)
		assert.Contains(t, stderr.String(), "unknown control admin group: xyz")
	})

	// The dispatcher builds the same message from the same path. It is
	// unreachable while the walk runs first, and the two used to disagree:
	// the dispatcher prefixed a hardcoded "recover" onto every path,
	// the control tree's included.
	t.Run("the dispatcher says exactly the same thing", func(t *testing.T) {
		err := runControl([]string{"xyz"})
		require.Error(t, err)
		assert.Equal(t, "unknown control command: xyz", err.Error())

		err = runControl([]string{"admin", "xyz"})
		require.Error(t, err)
		assert.Equal(t, "unknown control admin group: xyz", err.Error())

		err = runControl(nil)
		require.Error(t, err)
		assert.Equal(t, "control command is required", err.Error())

		err = runControl([]string{"admin"})
		require.Error(t, err)
		assert.Equal(t, "control admin group is required", err.Error())
	})
}
