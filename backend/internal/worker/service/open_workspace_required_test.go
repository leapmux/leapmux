package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestOpenAgent_RejectsMissingWorkspaceID(t *testing.T) {
	_, d, w := setupTestService(t, "ws-1")

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir: t.TempDir(),
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(3), w.errors[0].code, "expected INVALID_ARGUMENT")
	assert.Contains(t, w.errors[0].message, "workspace_id")
	assert.Empty(t, w.responses, "no agent should be created without workspace_id")
}

func TestOpenTerminal_RejectsMissingWorkspaceID(t *testing.T) {
	_, d, w := setupTestService(t, "ws-1")

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkingDir: t.TempDir(),
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(3), w.errors[0].code, "expected INVALID_ARGUMENT")
	assert.Contains(t, w.errors[0].message, "workspace_id")
	assert.Empty(t, w.responses, "no terminal should be created without workspace_id")
}

func TestOpenAgent_RejectsInaccessibleWorkspaceID(t *testing.T) {
	_, d, w := setupTestService(t, "ws-1")

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId: "ws-other",
		WorkingDir:  t.TempDir(),
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(7), w.errors[0].code, "expected PERMISSION_DENIED")
	assert.Contains(t, w.errors[0].message, "not accessible")
	assert.Empty(t, w.responses, "no agent should be created for an inaccessible workspace")
}

func TestOpenTerminal_RejectsInaccessibleWorkspaceID(t *testing.T) {
	_, d, w := setupTestService(t, "ws-1")

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId: "ws-other",
		WorkingDir:  t.TempDir(),
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(7), w.errors[0].code, "expected PERMISSION_DENIED")
	assert.Contains(t, w.errors[0].message, "not accessible")
	assert.Empty(t, w.responses, "no terminal should be created for an inaccessible workspace")
}
