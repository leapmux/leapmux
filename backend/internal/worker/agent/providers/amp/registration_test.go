package amp

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir/agentdirtest"
)

// agentdir tests the directory itself. This test pins what Amp states: its
// registration carries the spec that its start uses, and the spec leaves room
// for the bridge's socket, which the bridge then binds.
func TestAgentDirLeavesRoomForTheBridgeSocket(t *testing.T) {
	t.Parallel()
	spec := agentDirSpec()
	registered := Registration().AgentDir
	require.NotNil(t, registered, "the worker sweeps Amp's directories at its start")
	assert.Equal(t, spec.Prefix, registered.Prefix)
	assert.Equal(t, bridgeSocketName, registered.SocketName)
	assert.Nil(t, registered.OnStale, "an Amp agent leaves no process that outlives its worker")

	short := agentdirtest.ShortBase(t)
	long := filepath.Join(agentdirtest.ShortBase(t), strings.Repeat("d", agentdir.MaxSocketPathBytes()))
	dir, err := agentdirtest.NewDirs(t, []agentdir.Spec{spec}, long, short).New(context.Background(), spec)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dir.Close() })
	assert.True(t, strings.HasPrefix(dir.Path(), short), "a base too long for the socket gives way to the next one")
	assert.LessOrEqual(t, len(filepath.Join(dir.Path(), bridgeSocketName)), agentdir.MaxSocketPathBytes())
	bridge, err := newPermissionBridge(dir.Path())
	require.NoError(t, err, "the bridge binds its socket in the directory")
	bridge.close(errAgentStopped)

	_, err = agentdirtest.NewDirs(t, []agentdir.Spec{spec}, long).New(context.Background(), spec)
	assert.ErrorContains(t, err, "the limit is "+strconv.Itoa(agentdir.MaxSocketPathBytes()))
}
