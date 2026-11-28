package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
)

func TestAgentOpenDefaultsToTheCallingTabsPlacement(t *testing.T) {
	worker := &creationPublicationDispatcher{}
	tabID := fullTestID("callingtab")
	workspaceID := fullTestID("workspace")
	state := newStateBuilder().
		workspace(workspaceID, "root").
		splitNode("root", "", "").
		leafNode("first-leaf", "root", "a").
		leafNode("calling-leaf", "root", "b").
		tabOnWorker(tabID, "calling-leaf", "worker-A", leapmuxv1.TabType_TAB_TYPE_AGENT).st
	hub := &recordingHub{workers: workerRows("worker-A"), materialized: state, locateTab: &leapmuxv1.WorkspaceTab{
		Tab:         &leapmuxv1.TabIdent{TabId: tabID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
		WorkspaceId: workspaceID, TileId: "calling-leaf", WorkerId: "worker-A",
	}}
	startSpawnIPC(t, hub, worker)
	t.Setenv("LEAPMUX_CONTROL_TAB_ID", tabID)
	t.Setenv("LEAPMUX_CONTROL_WORKER_ID", "worker-A")
	t.Setenv("LEAPMUX_CONTROL_WORKING_DIR", "/source/checkout")

	result := runControl(t, "agent", "open", "--provider", "claude").Data(t)
	assert.Equal(t, workspaceID, result["workspace_id"])
	assert.Equal(t, "worker-A", result["worker_id"])

	calls := worker.recorded()
	require.Len(t, calls, 1)
	assert.Equal(t, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, calls[0].method)
	request, ok := calls[0].in.(*leapmuxv1.OpenAgentRequest)
	require.True(t, ok)
	assert.Equal(t, "/source/checkout", request.GetWorkingDir())
	placement := request.GetCreation().GetPlacement()
	assert.Equal(t, workspaceID, placement.GetWorkspaceId())
	assert.Equal(t, "calling-leaf", placement.GetTileId())
}
