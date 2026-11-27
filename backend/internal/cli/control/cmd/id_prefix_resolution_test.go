package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

func TestServerSideIDPrefixResolution_WorkspacesTabsWorkersAndSections(t *testing.T) {
	t.Run("workspace prefix and id: send ListWorkspaces with IdPrefix and fall back for bare title", func(t *testing.T) {
		wsA := fullTestID("ws-alpha")
		wsB := fullTestID("ws-bravo")
		hub := &recordingHub{
			workspaces: []*leapmuxv1.Workspace{
				{Id: wsA, Title: "Release"},
				{Id: wsB, Title: "Scratch"},
			},
		}
		startSpawnIPC(t, hub, nil)

		s, err := openSession(&cli.Invocation{Command: NewCommand(), Context: t.Context()})
		require.NoError(t, err)
		defer s.Close()

		got, err := s.resolveOne(resolve.KindWorkspace, wsA[:8])
		require.NoError(t, err)
		assert.Equal(t, wsA, got.ID)

		got, err = s.resolveOne(resolve.KindWorkspace, "id:"+wsB[:8])
		require.NoError(t, err)
		assert.Equal(t, wsB, got.ID)

		calls := hub.recordedListCalls()
		require.Len(t, calls, 2)
		assert.Equal(t, recordedListCall{
			procedure: leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure,
			idPrefix:  wsA[:8],
		}, calls[0])
		assert.Equal(t, recordedListCall{
			procedure: leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure,
			idPrefix:  wsB[:8],
		}, calls[1])

		// Bare title misses id_prefix, then fetches full list (which populates the
		// session cache so a subsequent prefix lookup needs zero RPCs).
		got, err = s.resolveOne(resolve.KindWorkspace, "Release")
		require.NoError(t, err)
		assert.Equal(t, wsA, got.ID)

		callsAfterTitle := hub.recordedListCalls()
		require.Len(t, callsAfterTitle, 4)
		assert.Equal(t, "Release", callsAfterTitle[2].idPrefix)
		assert.Equal(t, "", callsAfterTitle[3].idPrefix)

		// Now that the session cached the full workspace list, another prefix
		// query filters in memory with zero additional RPCs.
		got, err = s.resolveOne(resolve.KindWorkspace, wsB[:8])
		require.NoError(t, err)
		assert.Equal(t, wsB, got.ID)
		assert.Len(t, hub.recordedListCalls(), 4, "cached full workspace list must satisfy subsequent prefix lookups in memory")
	})

	t.Run("tab prefix sends ListTabs with IdPrefix and respects --workspace scope", func(t *testing.T) {
		wsID := fullTestID("ws-scope")
		tabID := fullTestID("tab-pref")
		hub := &recordingHub{
			workspaces: []*leapmuxv1.Workspace{{Id: wsID, Title: "Scoped"}},
			tabs: []*leapmuxv1.WorkspaceTab{
				{
					Tab:         &leapmuxv1.TabIdent{TabId: tabID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
					WorkspaceId: wsID,
					TileId:      "tile-1",
					WorkerId:    "worker-A",
				},
			},
			materialized: newStateBuilder().workspace(wsID, "tile-1").leafNode("tile-1", "", "a0").st,
		}
		startSpawnIPC(t, hub, nil)

		s, err := openSession(&cli.Invocation{Command: NewCommand(), Context: t.Context()})
		require.NoError(t, err)
		defer s.Close()

		got, err := resolve.Canonical(s.ctx, s.listing(), resolve.KindAgent, tabID[:8])
		require.NoError(t, err)
		assert.Equal(t, tabID, got.ID)
		require.NotNil(t, got.Tab)

		calls := hub.recordedListCalls()
		require.Len(t, calls, 1)
		assert.Equal(t, recordedListCall{
			procedure: leapmuxv1connect.WorkspaceServiceListTabsProcedure,
			idPrefix:  tabID[:8],
		}, calls[0])

		// Scoped prefix query passes workspace_ids alongside id_prefix.
		scopedTabs, err := s.listTabsByIDPrefix(s.ctx, []string{wsID}, tabID[:8])
		require.NoError(t, err)
		require.Len(t, scopedTabs, 1)
		calls = hub.recordedListCalls()
		require.Len(t, calls, 2)
		assert.Equal(t, recordedListCall{
			procedure:    leapmuxv1connect.WorkspaceServiceListTabsProcedure,
			idPrefix:     tabID[:8],
			workspaceIDs: []string{wsID},
		}, calls[1])

		// Prefix lookup must NOT have populated allTabsLoaded; a subsequent full
		// listAllTabs still fetches the full tab list, and after that prefix
		// queries stay in memory with zero RPCs.
		allTabs, err := s.listAllTabs(s.ctx)
		require.NoError(t, err)
		require.Len(t, allTabs, 1)
		assert.Len(t, hub.recordedListCalls(), 3)
		assert.Equal(t, "", hub.recordedListCalls()[2].idPrefix)

		inMem, err := s.listTabsByIDPrefix(s.ctx, []string{wsID}, tabID[:8])
		require.NoError(t, err)
		require.Len(t, inMem, 1)
		assert.Len(t, hub.recordedListCalls(), 3, "cached full tab list must satisfy subsequent prefix lookups in memory")
	})

	t.Run("variadic tab resolution sends one id_prefix RPC for a single or duplicated prefix", func(t *testing.T) {
		wsID := fullTestID("ws-var")
		tabID := fullTestID("tab-var1")
		hub := &recordingHub{
			tabs: []*leapmuxv1.WorkspaceTab{
				{
					Tab:         &leapmuxv1.TabIdent{TabId: tabID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
					WorkspaceId: wsID,
					TileId:      "tile-1",
					WorkerId:    "worker-A",
				},
			},
		}
		startSpawnIPC(t, hub, nil)

		s, err := openSession(&cli.Invocation{Command: NewCommand(), Context: t.Context()})
		require.NoError(t, err)
		defer s.Close()

		targets, err := s.resolveVariadicTabs(resolve.KindAgent, []string{tabID[:8], tabID[:8]}, resolve.Need{})
		require.NoError(t, err)
		require.Len(t, targets, 1)
		assert.Equal(t, tabID, targets[0].ID)

		calls := hub.recordedListCalls()
		require.Len(t, calls, 1, "duplicated single prefix must make one id_prefix RPC")
		assert.Equal(t, recordedListCall{
			procedure: leapmuxv1connect.WorkspaceServiceListTabsProcedure,
			idPrefix:  tabID[:8],
		}, calls[0])
	})

	t.Run("worker prefix sends one ListWorkers page with IdPrefix and prefixResolutionLimit", func(t *testing.T) {
		wrkA := fullTestID("wrk-one1")
		wrkB := fullTestID("wrk-two2")
		hub := &recordingHub{
			listWorkerPages: [][]string{
				{wrkA},
				{wrkB},
			},
		}
		startSpawnIPC(t, hub, nil)

		s, err := openSession(&cli.Invocation{Command: NewCommand(), Context: t.Context()})
		require.NoError(t, err)
		defer s.Close()

		got, err := s.resolveOne(resolve.KindWorker, wrkB[:8])
		require.NoError(t, err)
		assert.Equal(t, wrkB, got.ID)

		calls := hub.recordedListCalls()
		require.Len(t, calls, 1, "worker prefix lookup must issue one RPC, not scan all pages")
		assert.Equal(t, recordedListCall{
			procedure: leapmuxv1connect.WorkerManagementServiceListWorkersProcedure,
			idPrefix:  wrkB[:8],
			limit:     prefixResolutionLimit,
		}, calls[0])
	})

	t.Run("section resolution uses server-side id_prefix for id: and bare prefixes and falls back for bare names", func(t *testing.T) {
		secA := fullTestID("sec-one1")
		secB := fullTestID("sec-two2")
		hub := &recordingHub{
			sections: &leapmuxv1.ListSectionsResponse{
				Sections: []*leapmuxv1.Section{
					{Id: secA, Name: "Client Work", Sidebar: leapmuxv1.Sidebar_SIDEBAR_LEFT},
					{Id: secB, Name: "Monitoring", Sidebar: leapmuxv1.Sidebar_SIDEBAR_RIGHT},
				},
			},
		}
		startSpawnIPC(t, hub, nil)

		s, err := openSession(&cli.Invocation{Command: NewCommand(), Context: t.Context()})
		require.NoError(t, err)
		defer s.Close()

		// Full generated ID with id: sends id_prefix=fullID.
		_, sec, err := s.resolveSectionFromHub("id:" + secA)
		require.NoError(t, err)
		assert.Equal(t, secA, sec.GetId())

		// Bare ID prefix sends id_prefix=prefix and skips full list.
		_, sec, err = s.resolveSectionFromHub(secB[:8])
		require.NoError(t, err)
		assert.Equal(t, secB, sec.GetId())

		// Multi-word bare name skips id_prefix and goes straight to full list.
		_, sec, err = s.resolveSectionFromHub("Client Work")
		require.NoError(t, err)
		assert.Equal(t, secA, sec.GetId())

		// Single-word bare name tries id_prefix first, then falls back to full list.
		_, sec, err = s.resolveSectionFromHub("Monitoring")
		require.NoError(t, err)
		assert.Equal(t, secB, sec.GetId())

		calls := hub.recordedListCalls()
		require.Len(t, calls, 5)
		assert.Equal(t, secA, calls[0].idPrefix)
		assert.Equal(t, secB[:8], calls[1].idPrefix)
		assert.Equal(t, "", calls[2].idPrefix)
		assert.Equal(t, "Monitoring", calls[3].idPrefix)
		assert.Equal(t, "", calls[4].idPrefix)
	})
}
