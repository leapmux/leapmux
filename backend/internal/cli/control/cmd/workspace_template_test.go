package cmd

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

func TestParseWorkspaceTemplate_RejectsUnknownFields(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "unknown top-level key",
			yaml: "version: 1\ntitle: Demo\nunknown_key: true\n",
		},
		{
			name: "unknown tab key",
			yaml: "version: 1\ntitle: Demo\ntabs:\n  - type: terminal\n    bogus_field: 123\n",
		},
		{
			name: "unknown floating window key",
			yaml: "version: 1\ntitle: Demo\nfloating_windows:\n  - x: 10\n    y: 20\n    width: 400\n    height: 300\n    extra: 1\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseWorkspaceTemplate([]byte(tc.yaml))
			require.Error(t, err)
			assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
		})
	}
}

func TestParseWorkspaceTemplate_RejectsTileRefOnNonLeaf(t *testing.T) {
	t.Run("split node with tile_ref", func(t *testing.T) {
		raw := `
version: 1
title: Split
tree:
  tile_ref: bad-split
  kind: split
  direction: vertical
  children:
    - kind: leaf
      tile_ref: t1
    - kind: leaf
      tile_ref: t2
`
		_, err := parseWorkspaceTemplate([]byte(raw))
		require.Error(t, err)
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
		assert.Contains(t, err.Error(), "only allowed on leaf nodes")
	})

	t.Run("grid node with tile_ref", func(t *testing.T) {
		raw := `
version: 1
title: Grid
tree:
  tile_ref: bad-grid
  kind: grid
  rows: 1
  cols: 2
  children:
    - kind: leaf
      tile_ref: t1
    - kind: leaf
      tile_ref: t2
`
		_, err := parseWorkspaceTemplate([]byte(raw))
		require.Error(t, err)
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
		assert.Contains(t, err.Error(), "only allowed on leaf nodes")
	})

	t.Run("valid leaf tile_ref is retained on parsedTree", func(t *testing.T) {
		raw := `
version: 1
title: Valid
tree:
  kind: split
  direction: vertical
  children:
    - kind: leaf
      tile_ref: left
    - kind: leaf
      tile_ref: right
`
		tmpl, err := parseWorkspaceTemplate([]byte(raw))
		require.NoError(t, err)
		require.NotNil(t, tmpl.parsedTree)
		require.Len(t, tmpl.parsedTree.Children, 2)
		assert.Equal(t, "left", tmpl.parsedTree.Children[0].TileRef)
		assert.Equal(t, "right", tmpl.parsedTree.Children[1].TileRef)
	})
}

func TestParseWorkspaceTemplate_FloatingWindowValidationAndOpacity(t *testing.T) {
	t.Run("omitted opacity defaults to 1.0 and explicit 0.0 is preserved", func(t *testing.T) {
		raw := `
version: 1
title: Floating
floating_windows:
  - x: 10
    y: 20
    width: 640
    height: 480
  - x: 50
    y: 60
    width: 320
    height: 240
    opacity: 0.0
`
		tmpl, err := parseWorkspaceTemplate([]byte(raw))
		require.NoError(t, err)
		require.Len(t, tmpl.FloatingWindows, 2)
		assert.Nil(t, tmpl.FloatingWindows[0].Opacity)
		assert.InDelta(t, 1.0, tmpl.FloatingWindows[0].effectiveOpacity(), 1e-9)
		require.NotNil(t, tmpl.FloatingWindows[1].Opacity)
		assert.InDelta(t, 0.0, *tmpl.FloatingWindows[1].Opacity, 1e-9)
		assert.InDelta(t, 0.0, tmpl.FloatingWindows[1].effectiveOpacity(), 1e-9)
		assert.NotNil(t, tmpl.FloatingWindows[0].parsedTree)
		assert.NotNil(t, tmpl.FloatingWindows[1].parsedTree)
	})

	invalidCases := []struct {
		name string
		yaml string
	}{
		{
			name: "opacity below 0",
			yaml: "version: 1\nfloating_windows:\n  - x: 0\n    y: 0\n    width: 100\n    height: 100\n    opacity: -0.1\n",
		},
		{
			name: "opacity above 1",
			yaml: "version: 1\nfloating_windows:\n  - x: 0\n    y: 0\n    width: 100\n    height: 100\n    opacity: 1.5\n",
		},
		{
			name: "NaN x",
			yaml: "version: 1\nfloating_windows:\n  - x: .nan\n    y: 0\n    width: 100\n    height: 100\n",
		},
		{
			name: "Inf y",
			yaml: "version: 1\nfloating_windows:\n  - x: 0\n    y: .inf\n    width: 100\n    height: 100\n",
		},
		{
			name: "Inf width",
			yaml: "version: 1\nfloating_windows:\n  - x: 0\n    y: 0\n    width: .inf\n    height: 100\n",
		},
		{
			name: "NaN height",
			yaml: "version: 1\nfloating_windows:\n  - x: 0\n    y: 0\n    width: 100\n    height: .nan\n",
		},
		{
			name: "NaN opacity",
			yaml: "version: 1\nfloating_windows:\n  - x: 0\n    y: 0\n    width: 100\n    height: 100\n    opacity: .nan\n",
		},
		{
			name: "zero width",
			yaml: "version: 1\nfloating_windows:\n  - x: 0\n    y: 0\n    width: 0\n    height: 100\n",
		},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseWorkspaceTemplate([]byte(tc.yaml))
			require.Error(t, err)
			assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
		})
	}

	assert.Error(t, validateFiniteFloat(math.NaN(), "x"))
	assert.Error(t, validateFiniteFloat(math.Inf(1), "y"))
	assert.Error(t, validateFiniteFloat(math.Inf(-1), "opacity"))
}

func TestParseWorkspaceTemplate_FileTabPresentation(t *testing.T) {
	t.Run("valid presentation is parsed and stored", func(t *testing.T) {
		raw := `
version: 1
title: Docs
tabs:
  - type: file
    path: README.md
    display_mode: source
    view_mode: unified_diff
    diff_base: main
`
		tmpl, err := parseWorkspaceTemplate([]byte(raw))
		require.NoError(t, err)
		require.Len(t, tmpl.Tabs, 1)
		pres := tmpl.Tabs[0].presentation
		require.NotNil(t, pres)
		assert.Equal(t, leapmuxv1.TabDisplayMode_TAB_DISPLAY_MODE_SOURCE, pres.GetDisplayMode())
		assert.Equal(t, leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_UNIFIED_DIFF, pres.GetFileViewMode())
		assert.Equal(t, "main", pres.GetFileDiffBase())
	})

	t.Run("invalid display_mode fails with usage", func(t *testing.T) {
		raw := `
version: 1
tabs:
  - type: file
    path: README.md
    display_mode: invalid_mode
`
		_, err := parseWorkspaceTemplate([]byte(raw))
		require.Error(t, err)
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
	})

	t.Run("invalid view_mode fails with usage", func(t *testing.T) {
		raw := `
version: 1
tabs:
  - type: file
    path: README.md
    view_mode: invalid_view
`
		_, err := parseWorkspaceTemplate([]byte(raw))
		require.Error(t, err)
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
	})
}

func TestWorkspaceApply_RejectsEmptyTitleBeforeDialing(t *testing.T) {
	hub := &recordingHub{}
	startSpawnIPC(t, hub, nil)

	const validTemplate = "version: 1\ntitle: Original\n"

	o := runControlIn(t, validTemplate, "workspace", "apply", "-", "--title", "   ", "--json")
	f := o.requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "--title")
	assert.Empty(t, hub.called())

	o = runControlIn(t, validTemplate, "workspace", "apply", "-", "--title", "", "--dry-run", "--json")
	f = o.requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "--title")
	assert.Empty(t, hub.called())
}

func TestWorkspaceApplyPartialError_PreservesCauseMetadata(t *testing.T) {
	receipt := &cli.RetryReceipt{
		RequestID:   "req-123",
		Disposition: "uncertain",
	}
	cause := cli.Newf(cli.CodeAmbiguousTarget, "multiple providers installed").
		WithHint("pass --provider").
		WithReceipt(receipt).
		WithExitCode(cli.ExitWaitEnded)
	cause.Candidates = []cli.Candidate{
		{ID: "claude", Kind: "provider", Title: "Claude"},
	}

	wrapped := workspaceApplyPartialError("ws-9", 1, 3, 2, cause)
	e := cli.FromError(wrapped)
	assert.Equal(t, cli.CodeAmbiguousTarget, e.Code)
	assert.Equal(t, cli.ExitWaitEnded, e.ExitCode())
	assert.Equal(t, receipt, e.Receipt)
	assert.Equal(t, cause.Candidates, e.Candidates)
	assert.Equal(t, []string{
		"pass --provider",
		`inspect workspace with "leapmux tab list --workspace ws-9"`,
	}, e.Hints)
	assert.Contains(t, e.Message, "tabs[2] failed")
	assert.Contains(t, e.Message, "multiple providers installed")
	assert.True(t, errors.Is(wrapped, cause))
}

func TestPostOrderLiveSubtree_LeavesFirstAndSkipsTombstones(t *testing.T) {
	b := newStateBuilder().
		splitNode("fw-root", "", "a0").
		leafNode("leaf-b", "fw-root", "a1").
		leafNode("leaf-a", "fw-root", "a0").
		tombstonedNode("leaf-dead", "fw-root")
	children := crdt.LiveChildrenByParent(b.st)

	got := postOrderLiveSubtree(b.st, children, "fw-root")
	assert.Equal(t, []string{"leaf-a", "leaf-b", "fw-root"}, got)
}

type exportPayloadDispatcher struct {
	payloadCalls int
	filePath     string
	workingDir   string
}

func (d *exportPayloadDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	if req.GetMethod() != leapmuxv1connect.WorkerPrivateServiceGetTabPayloadProcedure {
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
		return
	}
	d.payloadCalls++
	writeProtoResponse(rw, &leapmuxv1.GetTabPayloadResponse{
		Payload: &leapmuxv1.TabPayload{
			WorkingDir: d.workingDir,
			Kind: &leapmuxv1.TabPayload_File{
				File: &leapmuxv1.FileTabPayload{FilePath: d.filePath},
			},
		},
	})
}

func TestWorkspaceExport_UsesCRDTSnapshotAndSingleWorkerPass(t *testing.T) {
	rootID := fullTestID("root-exp")
	fwRootID := fullTestID("fw-exp")
	fileTabID := fullTestID("file-exp")
	b := newStateBuilder().
		workspace("ws-1", rootID).
		leafNode(rootID, "", "a0").
		leafNode(fwRootID, "", "a0").
		tab(fileTabID, rootID, leapmuxv1.TabType_TAB_TYPE_FILE)
	b.st.Tabs[fileTabID].WorkerId = &leapmuxv1.LWWString{Value: "worker-A"}
	b.st.FloatingWindows = map[string]*leapmuxv1.FloatingWindowRecord{
		"fw-1": {
			WindowId:    "fw-1",
			WorkspaceId: &leapmuxv1.LWWString{Value: "ws-1"},
			RootNodeId:  fwRootID,
			X:           &leapmuxv1.LWWDouble{Value: 10},
			Y:           &leapmuxv1.LWWDouble{Value: 20},
			Width:       &leapmuxv1.LWWDouble{Value: 500},
			Height:      &leapmuxv1.LWWDouble{Value: 400},
			Opacity:     &leapmuxv1.LWWDouble{Value: 0.0},
		},
	}

	disp := &exportPayloadDispatcher{
		filePath:   "/repo/README.md",
		workingDir: "/repo",
	}
	hub := &recordingHub{
		workers:      workerRows("worker-A"),
		materialized: b.st,
	}
	startSpawnIPC(t, hub, disp)

	data := runControl(t, "workspace", "export", "id:ws-1").Data(t)
	assert.Equal(t, "First", data["title"])
	assert.NotContains(t, hub.called(), leapmuxv1connect.WorkspaceServiceListTabsProcedure, "export must not issue a redundant ListTabs RPC")
	assert.Equal(t, 1, disp.payloadCalls, "export must hydrate file tab payload in one Worker pass via fetchTabDetails")

	tabs, ok := data["tabs"].([]any)
	require.True(t, ok)
	require.Len(t, tabs, 1)
	tabMap := tabs[0].(map[string]any)
	assert.Equal(t, "file", tabMap["type"])
	assert.Equal(t, "/repo/README.md", tabMap["path"])
	assert.Equal(t, "/repo", tabMap["working_dir"])

	fws, ok := data["floating_windows"].([]any)
	require.True(t, ok)
	require.Len(t, fws, 1)
	fwMap := fws[0].(map[string]any)
	assert.Equal(t, float64(0), fwMap["opacity"], "explicit 0.0 floating window opacity must be preserved")
}
