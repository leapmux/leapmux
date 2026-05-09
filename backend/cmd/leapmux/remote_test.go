package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestHandleRemoteArgs_WorkerPinsShowsHelpWhenNoSubcommand pins the
// dispatch behaviour for `leapmux remote worker pins` without a verb.
// Before the subgroup split, the single RunWorkerPins handler emitted
// a JSON `invalid_request` envelope, which was unfriendly. The fix
// makes `pins` a real Subgroup so the standard "missing subcommand"
// path prints help to stderr.
func TestHandleRemoteArgs_WorkerPinsShowsHelpWhenNoSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, handled := handleRemoteArgs([]string{"worker", "pins"}, &stdout, &stderr)
	assert.True(t, handled, "args=worker pins must be handled by walkAdminArgs, not dispatched as a leaf")
	assert.Equal(t, 1, code, "missing subcommand is an error -> exit 1")
	// Help block goes to stderr (alongside the missing-command notice);
	// stdout stays empty so a `... | jq` pipeline isn't fed garbage.
	assert.Empty(t, stdout.String(), "stdout must stay clean when help fires")
	help := stderr.String()
	assert.Contains(t, help, "remote worker pins command is required")
	assert.Contains(t, help, "Manage TOFU worker key pins")
	assert.Contains(t, help, "list")
	assert.Contains(t, help, "show")
	assert.Contains(t, help, "remove")
	// The old monolithic handler used to surface this exact JSON error
	// envelope. Guard against a regression that re-introduces it.
	assert.NotContains(t, help, `"error"`)
	assert.NotContains(t, help, "invalid_request")
}

// TestHandleRemoteArgs_WorkerPinsListIsLeaf confirms the verb actually
// dispatches to the leaf handler (returns handled=false so the
// dispatcher runs the registered Run func), not back into the help
// path. Without this, a refactor that turned "list" into a subgroup
// would silently break every existing script.
func TestHandleRemoteArgs_WorkerPinsListIsLeaf(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, handled := handleRemoteArgs([]string{"worker", "pins", "list"}, &stdout, &stderr)
	assert.False(t, handled, "args=worker pins list must reach the leaf dispatch path")
	assert.Equal(t, 0, code)
	assert.Empty(t, stdout.String())
	assert.Empty(t, stderr.String())
}
