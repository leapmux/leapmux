package cmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/lexorank"
)

func TestComputeRankedPosition_ModesTiedRanksAndErrors(t *testing.T) {
	t.Run("empty peers return lexorank.First for first, last, and unset", func(t *testing.T) {
		for _, spec := range []positionSpec{
			{},
			{kind: positionFirst},
			{kind: positionLast},
		} {
			pos, err := computeRankedPosition(nil, "", spec, "section")
			require.NoError(t, err)
			assert.Equal(t, lexorank.First(), pos)
		}
	})

	t.Run("first and last exclude moving item without mutating input", func(t *testing.T) {
		peers := []rankedItem{
			{ID: "c", Position: "x"},
			{ID: "a", Position: "b"},
			{ID: "b", Position: "n"},
		}
		orig := append([]rankedItem(nil), peers...)

		firstPos, err := computeRankedPosition(peers, "a", positionSpec{kind: positionFirst}, "workspace")
		require.NoError(t, err)
		assert.Less(t, firstPos, "n")
		assert.Equal(t, orig, peers, "computeRankedPosition must not mutate peers")

		lastPos, err := computeRankedPosition(peers, "c", positionSpec{kind: positionLast}, "workspace")
		require.NoError(t, err)
		assert.Greater(t, lastPos, "n")
		assert.Less(t, lastPos, "x")
		assert.Equal(t, orig, peers)
	})

	t.Run("before skips tied sibling ranks", func(t *testing.T) {
		peers := []rankedItem{
			{ID: "a", Position: "b"},
			{ID: "b", Position: "m"},
			{ID: "c", Position: "m"},
			{ID: "d", Position: "z"},
		}
		pos, err := computeRankedPosition(peers, "d", positionSpec{kind: positionBefore, refID: "c"}, "section")
		require.NoError(t, err)
		assert.Greater(t, pos, "b")
		assert.Less(t, pos, "m")

		allTied := []rankedItem{
			{ID: "a", Position: "m"},
			{ID: "b", Position: "m"},
		}
		pos, err = computeRankedPosition(allTied, "", positionSpec{kind: positionBefore, refID: "b"}, "section")
		require.NoError(t, err)
		assert.Less(t, pos, "m")
	})

	t.Run("after skips tied sibling ranks", func(t *testing.T) {
		peers := []rankedItem{
			{ID: "a", Position: "m"},
			{ID: "b", Position: "m"},
			{ID: "c", Position: "z"},
		}
		pos, err := computeRankedPosition(peers, "", positionSpec{kind: positionAfter, refID: "a"}, "section")
		require.NoError(t, err)
		assert.Greater(t, pos, "m")
		assert.Less(t, pos, "z")

		allTied := []rankedItem{
			{ID: "a", Position: "m"},
			{ID: "b", Position: "m"},
		}
		pos, err = computeRankedPosition(allTied, "", positionSpec{kind: positionAfter, refID: "a"}, "section")
		require.NoError(t, err)
		assert.Greater(t, pos, "m")
	})

	t.Run("rejects self-relative move with CodeUsage", func(t *testing.T) {
		peers := []rankedItem{{ID: "sec-1", Position: "n"}}
		for _, kind := range []positionKind{positionBefore, positionAfter} {
			_, err := computeRankedPosition(peers, "sec-1", positionSpec{kind: kind, refID: "sec-1"}, "section")
			require.Error(t, err)
			cf := cli.FromError(err)
			assert.Equal(t, cli.CodeUsage, cf.Code)
			assert.Contains(t, cf.Message, "cannot move section relative to itself")
		}
	})

	t.Run("rejects missing reference with CodeNotFound", func(t *testing.T) {
		peers := []rankedItem{{ID: "sec-1", Position: "n"}}
		_, err := computeRankedPosition(peers, "", positionSpec{kind: positionBefore, refID: "sec-missing"}, "section")
		require.Error(t, err)
		cf := cli.FromError(err)
		assert.Equal(t, cli.CodeNotFound, cf.Code)
		assert.Contains(t, cf.Message, "section sec-missing not found")
	})
}

func TestTabMove_ValidatesFlagsBeforeSession(t *testing.T) {
	clearRemoteEnv(t)
	cases := []struct {
		name string
		args []string
	}{
		{"missing destination", []string{"tab", "move", fullTestID("tab-1"), "--hub", "https://stub.invalid"}},
		{"mutually exclusive position flags", []string{"tab", "move", fullTestID("tab-1"), "--first", "--last", "--hub", "https://stub.invalid"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runControl(t, tc.args...).requireFailure(t, cli.CodeUsage)
		})
	}
}

func TestTabMove_SkipsRedundantLocateTileOnSameTile(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "n").
		tabAt("agent-1", "root-1", "b", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tabAt("agent-2", "root-1", "n", leapmuxv1.TabType_TAB_TYPE_AGENT).
		st
	hub := &recordingHub{
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabId: "agent-2", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
			WorkspaceId: "ws-1",
			TileId:      "root-1",
			WorkerId:    "worker-A",
		},
		materialized: state,
		onSubmitOps: func(context.Context) ([]byte, error) {
			return proto.Marshal(&leapmuxv1.SubmitOpsResponse{
				Results: []*leapmuxv1.BatchResult{{
					Outcome: &leapmuxv1.BatchResult_Committed{Committed: &leapmuxv1.BatchCommitted{Epoch: 1}},
				}},
			})
		},
	}
	startSpawnIPC(t, hub, nil)

	data := runControl(t, "tab", "move", "agent-2", "--first").Data(t)
	assert.Equal(t, "agent-2", data["id"])
	assert.Equal(t, "root-1", data["tile_id"])
	assert.NotContains(t, hub.called(), leapmuxv1connect.WorkspaceServiceLocateTileProcedure,
		"reordering within the tab's own tile must not call LocateTile")
}
