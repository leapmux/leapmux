package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
)

// startTabWorker serves w as the Worker of tab, which the hub's LocateTab
// places on worker-A.
func startTabWorker(t *testing.T, w *privateCommandWorker, tab *leapmuxv1.WorkspaceTab) {
	t.Helper()
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A"), locateTab: tab}, w)
}

func TestTabRevealOverIPC_SendsTheTabToItsWorker(t *testing.T) {
	for _, tab := range []*leapmuxv1.WorkspaceTab{agentTab(), terminalTab()} {
		t.Run(tabTypeName(tab.GetTab().GetTabType()), func(t *testing.T) {
			w := &privateCommandWorker{}
			startTabWorker(t, w, tab)
			id := tab.GetTab().GetTabId()

			data := runControl(t, "tab", "reveal", id).Data(t)
			assert.Equal(t, map[string]any{"id": id, "type": tabTypeName(tab.GetTab().GetTabType()), "workspace_id": "ws-1"}, data)

			calls := w.recorded()
			require.Len(t, calls, 1)
			assert.Equal(t, leapmuxv1connect.WorkerPrivateServiceRevealTabProcedure, calls[0].method)
			assert.True(t, proto.Equal(&leapmuxv1.RevealTabRequest{TabId: id}, calls[0].request), "request: %v", calls[0].request)
		})
	}
}

// "self" is an explicit target: the tab that runs the command.
func TestTabRevealOverIPC_RevealsTheCallersOwnTab(t *testing.T) {
	w := &privateCommandWorker{}
	self := agentTab()
	startTabWorker(t, w, self)
	t.Setenv("LEAPMUX_CONTROL_TAB_ID", self.GetTab().GetTabId())

	data := runControl(t, "tab", "reveal", "self").Data(t)
	assert.Equal(t, self.GetTab().GetTabId(), data["id"])
	calls := w.recorded()
	require.Len(t, calls, 1)
	assert.Equal(t, self.GetTab().GetTabId(), calls[0].request.(*leapmuxv1.RevealTabRequest).GetTabId())
}

// The command changes what every window shows, so a bare invocation is a
// usage error rather than a reveal of the caller's own tab.
func TestTabReveal_RequiresATab(t *testing.T) {
	w := &privateCommandWorker{}
	startTabWorker(t, w, agentTab())

	runControl(t, "tab", "reveal").requireFailure(t, cli.CodeUsage)
	assert.Empty(t, w.recorded())
}

// A tab that the Worker does not host reads as not found, with exit 4.
func TestTabRevealOverIPC_ReportsATabThatTheWorkerDoesNotHost(t *testing.T) {
	w := &privateCommandWorker{refusals: map[string]privateCommandRefusal{
		leapmuxv1connect.WorkerPrivateServiceRevealTabProcedure: {codes.NotFound, "tab not found on this worker"},
	}}
	startTabWorker(t, w, agentTab())

	f := runControl(t, "tab", "reveal", agentTab().GetTab().GetTabId()).requireFailure(t, cli.CodeNotFound)
	assert.Contains(t, f.Message, "tab not found on this worker")
}
