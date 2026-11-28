package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestWhoamiOverIPC_ExposesSpawnedByAgentID(t *testing.T) {
	const tabID = "agent-1"
	spawnerID := fullTestID("ag-boss")
	hub := &recordingHub{
		workers: workerRows("worker-A"),
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabId: tabID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
			WorkspaceId: "ws-1",
			TileId:      "tile-1",
			WorkerId:    "worker-A",
		},
	}
	disp := &tabMetadataDispatcher{
		agents: []*leapmuxv1.AgentInfo{
			{
				Id:               tabID,
				Title:            "Spawned Worker",
				WorkerId:         "worker-A",
				WorkingDir:       "/repo",
				AgentProvider:    leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
				SpawnedByAgentId: spawnerID,
			},
		},
	}
	startSpawnIPC(t, hub, disp)

	data := runControl(t, "whoami").Data(t)
	assert.Equal(t, tabID, data["tab_id"])
	assert.Equal(t, spawnerID, data["spawned_by_agent_id"])
}
