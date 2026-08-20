package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// The fake keys its rows exactly as production does, INCLUDING the derived key
// an unusable provider key becomes.
//
// This file's upsert comments state the rule: no link of the chain may be
// hand-written here, or a provider test asserts a contract the registry does
// not keep. NormalizeRowKey is the first link (agentOutputSink.applyAndBroadcast
// applies it before the closure runs), and it was the one most recently added
// -- so this pins the fake against it rather than trusting the next reader to
// remember.
func TestTestSinkKeysRowsTheWayProductionDoes(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	raw := strings.Repeat("a", bgtask.RowKeyByteLimit+1)
	derived := bgtask.NormalizeRowKey(raw)
	require.NotEqual(t, raw, derived, "the case must be one the rule refuses")

	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: raw, Kind: bgtask.KindShell, Title: "derived", Status: bgtask.StatusRunning,
	}))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, derived, rows[0].RowKey, "the fake must store the key production stores")

	// The lifecycle addresses the row by the RAW key on every call, so each one
	// must derive the same string -- the failure a fake that normalized only on
	// upsert would hide.
	require.NoError(t, sink.UpdateBackgroundTaskStatus(raw, bgtask.StatusRunning, "working"))
	require.NoError(t, sink.CloseBackgroundTask(raw, bgtask.StatusCompleted))

	rows = sink.BackgroundTasks()
	require.Len(t, rows, 1, "the close must find the row the upsert opened")
	assert.Equal(t, bgtask.StatusCompleted, rows[0].Status)
	assert.Equal(t, "working", rows[0].ActiveForm)
}
