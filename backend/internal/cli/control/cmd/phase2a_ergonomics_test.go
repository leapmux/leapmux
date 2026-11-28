package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/util/agentenv"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

func TestContextCommands_ShowUseClear(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	require.NoError(t, control.SaveCredentials("https://hub.example.com", control.CredentialFile{AccessToken: "tok"}))

	data := runControl(t, "context", "show").Data(t)
	assert.Empty(t, data["hub"])
	assert.Empty(t, data["worker"])

	used := runControl(t, "ctx", "use",
		"--hub", "https://hub.example.com",
		"--worker", "build-box",
		"--workspace", "Release 1.4",
		"--provider", "claude",
	).Data(t)
	assert.Equal(t, "https://hub.example.com", used["hub"])
	assert.Equal(t, "build-box", used["worker"])
	assert.Equal(t, "Release 1.4", used["workspace"])
	assert.Equal(t, "claude", used["provider"])

	// Partial clear removes only the requested field.
	clearedWorker := runControl(t, "context", "clear", "--field", "worker").Data(t)
	assert.Equal(t, "https://hub.example.com", clearedWorker["hub"])
	assert.Empty(t, clearedWorker["worker"])
	assert.Equal(t, "Release 1.4", clearedWorker["workspace"])

	// Full clear removes everything.
	clearedAll := runControl(t, "context", "clear").Data(t)
	assert.Empty(t, clearedAll["hub"])
	assert.Empty(t, clearedAll["workspace"])
	assert.Empty(t, clearedAll["provider"])

	// Missing flags on `context use` fails with usage.
	runControl(t, "context", "use").requireFailure(t, cli.CodeUsage)
}

func TestValidateAgentSlug(t *testing.T) {
	assert.NoError(t, validateAgentSlug("reviewer"))
	assert.NoError(t, validateAgentSlug("test-runner_1"))
	assert.NoError(t, validateAgentSlug("a"))

	for _, bad := range []string{"", "Reviewer", "1agent", "has space", "self", "parent", "children", "a12345678901234567890123456789012"} {
		err := validateAgentSlug(bad)
		require.Error(t, err, bad)
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code, bad)
	}
}

func TestShouldFocusNewTab_AgentAwareDefaults(t *testing.T) {
	cmd := NewCommand()
	openCmd, _, err := cmd.Find([]string{"agent", "open"})
	require.NoError(t, err)

	makeInv := func(args ...string) *cli.Invocation {
		require.NoError(t, openCmd.Flags().Parse(args))
		return &cli.Invocation{Command: openCmd, Stdin: bytes.NewBuffer(nil)}
	}

	// Non-TTY buffers default to no-focus.
	t.Setenv("LEAPMUX_FOCUS_NEW", "")
	t.Setenv(controlenv.TabTypeVariable, "")
	assert.False(t, shouldFocusNewTab(makeInv()))

	// LEAPMUX_FOCUS_NEW=1 overrides non-TTY default.
	t.Setenv("LEAPMUX_FOCUS_NEW", "1")
	assert.True(t, shouldFocusNewTab(makeInv()))

	// LEAPMUX_FOCUS_NEW=0 disables focus.
	t.Setenv("LEAPMUX_FOCUS_NEW", "0")
	assert.False(t, shouldFocusNewTab(makeInv()))

	// Explicit --focus overrides LEAPMUX_FOCUS_NEW=0.
	assert.True(t, shouldFocusNewTab(makeInv("--focus")))
	// Reset flag state and test --no-focus overriding LEAPMUX_FOCUS_NEW=1.
	cmd2 := NewCommand()
	openCmd2, _, err := cmd2.Find([]string{"agent", "open"})
	require.NoError(t, err)
	require.NoError(t, openCmd2.Flags().Parse([]string{"--no-focus"}))
	t.Setenv("LEAPMUX_FOCUS_NEW", "1")
	assert.False(t, shouldFocusNewTab(&cli.Invocation{Command: openCmd2, Stdin: bytes.NewBuffer(nil)}))

	// Both --focus and --no-focus are rejected.
	cmd3 := NewCommand()
	openCmd3, _, err := cmd3.Find([]string{"agent", "open"})
	require.NoError(t, err)
	require.NoError(t, openCmd3.Flags().Parse([]string{"--focus", "--no-focus"}))
	err = validateFocusFlags(&cli.Invocation{Command: openCmd3, Stdin: bytes.NewBuffer(nil)})
	require.Error(t, err)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
}

func TestIsAgentEnvironment_DetectsOpenCodeMarkers(t *testing.T) {
	t.Setenv(controlenv.TabTypeVariable, "")
	t.Setenv("CI", "")
	for _, key := range agentenv.HarnessDetectionMarkers {
		t.Setenv(key, "")
	}
	assert.False(t, isAgentEnvironment())

	for _, tc := range []struct {
		key   string
		value string
	}{
		{key: controlenv.TabTypeVariable, value: "agent"},
		{key: "CI", value: "true"},
		{key: "OPENCODE_CLIENT", value: "cli"},
		{key: "OPENCODE_RUN_ID", value: "run-123"},
		{key: "KILO_CLIENT", value: "acp"},
		{key: "MIMOCODE_CLIENT", value: "leapmux"},
		{key: "QWEN_CODE", value: "1"},
		{key: "GOOSE_TERMINAL", value: "1"},
	} {
		t.Setenv(tc.key, tc.value)
		assert.Truef(t, isAgentEnvironment(), "%s=%q must be detected as an agent environment", tc.key, tc.value)
		t.Setenv(tc.key, "")
	}

	// Generic trace and ambient keys must not suppress focus in ordinary shells.
	for _, key := range []string{"TRACEPARENT", "TRACESTATE", "CONVERSATION_ID", "MEMORY_DIR", "TRANSCRIPT_PATH", "AGENT_ID"} {
		t.Setenv(key, "ambient-value")
		assert.Falsef(t, isAgentEnvironment(), "%s must not trigger agent environment detection", key)
		t.Setenv(key, "")
	}
}

func TestAgentApprove_SolePendingDefaultAndAmbiguity(t *testing.T) {
	req1 := &leapmuxv1.AgentControlRequest{
		RequestId:     "req-1",
		ClaimToken:    "tok-aaaa1111",
		ResponseState: leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY,
		Prompt: &leapmuxv1.ControlPrompt{
			Kind: &leapmuxv1.ControlPrompt_Permission{
				Permission: &leapmuxv1.PermissionPrompt{
					Title: "Run command",
					Options: []*leapmuxv1.PermissionOption{
						{OptionId: "allow", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE},
					},
				},
			},
		},
	}
	req2 := &leapmuxv1.AgentControlRequest{
		RequestId:     "req-2",
		ClaimToken:    "tok-bbbb2222",
		ResponseState: leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY,
		Prompt:        req1.Prompt,
	}

	got, err := findSoleControlRequest("ag-1", []*leapmuxv1.AgentControlRequest{req1})
	require.NoError(t, err)
	assert.Equal(t, "tok-aaaa1111", got.GetClaimToken())

	_, err = findSoleControlRequest("ag-1", nil)
	assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)

	_, err = findSoleControlRequest("ag-1", []*leapmuxv1.AgentControlRequest{req1, req2})
	ce := cli.FromError(err)
	assert.Equal(t, cli.CodeAmbiguousTarget, ce.Code)
	require.Len(t, ce.Candidates, 2)
	assert.Equal(t, "tok-aaaa1111", ce.Candidates[0].ID)
	assert.Equal(t, "tok-bbbb2222", ce.Candidates[1].ID)
}

func TestEventsStream_SnapshotAndTimeout(t *testing.T) {
	t.Run("snapshot exits cleanly after stream.ready", func(t *testing.T) {
		clk := quartz.NewMock(t)
		var out bytes.Buffer
		p := &cli.Printer{Out: &out, Err: &bytes.Buffer{}, Format: cli.FormatJSON}
		s := newEventStream(eventStreamConfig{
			printer:  &eventPrinter{p: p, filter: kindFilter{}},
			clock:    clk,
			snapshot: true,
			hubSession: func(ctx context.Context, onEvent func(*leapmuxv1.WatchUserEvent) error) error {
				if err := onEvent(&leapmuxv1.WatchUserEvent{
					Event: &leapmuxv1.WatchUserEvent_Initial{
						Initial: &leapmuxv1.UserMaterialized{UserId: "u-1"},
					},
				}); err != nil {
					return err
				}
				<-ctx.Done()
				return nil
			},
		})
		require.NoError(t, s.run(context.Background()))
		assert.Contains(t, out.String(), `"kind":"stream.ready"`)
	})

	t.Run("timeout exits cleanly via monotonic clock", func(t *testing.T) {
		clk := quartz.NewMock(t)
		timerWait := clk.Trap().NewTimer(eventTimeoutTimerTag)
		defer timerWait.Close()

		var out bytes.Buffer
		ready := make(chan struct{}, 1)
		w := &notifyWriter{
			w: &out,
			onWrite: func(p []byte) {
				if strings.Contains(string(p), `"kind":"stream.ready"`) {
					select {
					case ready <- struct{}{}:
					default:
					}
				}
			},
		}
		p := &cli.Printer{Out: w, Err: &bytes.Buffer{}, Format: cli.FormatJSON}
		s := newEventStream(eventStreamConfig{
			printer: &eventPrinter{p: p, filter: kindFilter{}},
			clock:   clk,
			timeout: 5 * time.Second,
			hubSession: func(ctx context.Context, onEvent func(*leapmuxv1.WatchUserEvent) error) error {
				if err := onEvent(&leapmuxv1.WatchUserEvent{
					Event: &leapmuxv1.WatchUserEvent_Initial{
						Initial: &leapmuxv1.UserMaterialized{UserId: "u-1"},
					},
				}); err != nil {
					return err
				}
				<-ctx.Done()
				return nil
			},
		})
		runCtx, cancelRun := context.WithCancel(t.Context())
		done := make(chan struct{})
		var runErr error
		go func() {
			defer close(done)
			runErr = s.run(runCtx)
		}()
		t.Cleanup(func() {
			cancelRun()
			<-done
		})

		call, err := timerWait.Wait(t.Context())
		require.NoError(t, err)
		require.NoError(t, call.Release(t.Context()))
		<-ready
		clk.Advance(5 * time.Second).MustWait(t.Context())

		<-done
		require.NoError(t, runErr)
		assert.Contains(t, out.String(), `"kind":"stream.ready"`)
	})

	t.Run("noReconnect stops immediately on Hub disconnect", func(t *testing.T) {
		clk := quartz.NewMock(t)
		var out bytes.Buffer
		p := &cli.Printer{Out: &out, Err: &bytes.Buffer{}, Format: cli.FormatJSON}
		sessions := 0
		s := newEventStream(eventStreamConfig{
			printer:     &eventPrinter{p: p, filter: kindFilter{}},
			clock:       clk,
			noReconnect: true,
			hubSession: func(ctx context.Context, onEvent func(*leapmuxv1.WatchUserEvent) error) error {
				sessions++
				_ = onEvent(&leapmuxv1.WatchUserEvent{
					Event: &leapmuxv1.WatchUserEvent_Initial{
						Initial: &leapmuxv1.UserMaterialized{UserId: "u-1"},
					},
				})
				return nil
			},
		})
		err := s.run(context.Background())
		require.Error(t, err)
		assert.Equal(t, cli.CodeUnavailable, cli.FromError(err).Code)
		assert.Equal(t, 1, sessions)
	})
}

func TestWorktreeGet_AcceptsUnifiedSelectors(t *testing.T) {
	s1, err := parseWorktreeSelector("/repo/wt-a")
	require.NoError(t, err)
	assert.Equal(t, "/repo/wt-a", s1.GetPath())

	s2, err := parseWorktreeSelector("path:/repo/wt-b")
	require.NoError(t, err)
	assert.Equal(t, "/repo/wt-b", s2.GetPath())

	s3, err := parseWorktreeSelector("branch:feat/cli")
	require.NoError(t, err)
	assert.Equal(t, "feat/cli", s3.GetBranch())
}

func TestReadAgentOpenMessage_FileStdinAndAttach(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.md")
	attachFile := filepath.Join(dir, "note.txt")
	require.NoError(t, os.WriteFile(promptFile, []byte("Fix the bug"), 0o600))
	require.NoError(t, os.WriteFile(attachFile, []byte("context"), 0o600))

	parseOpen := func(stdin string, args ...string) (*userMessage, error) {
		cmd := NewCommand()
		openCmd, _, err := cmd.Find([]string{"agent", "open"})
		require.NoError(t, err)
		require.NoError(t, openCmd.Flags().Parse(args))
		return readAgentOpenMessage(&cli.Invocation{Command: openCmd, Stdin: bytes.NewBufferString(stdin)})
	}

	msg, err := parseOpen("", "--file", promptFile, "--attach", attachFile)
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, "Fix the bug", msg.text)
	require.Len(t, msg.attachments, 1)
	assert.Equal(t, "note.txt", msg.attachments[0].GetFilename())

	msg2, err := parseOpen("from stdin", "--message", "-")
	require.NoError(t, err)
	require.NotNil(t, msg2)
	assert.Equal(t, "from stdin", msg2.text)

	_, err = parseOpen("", "--message", "hi", "--file", promptFile)
	require.Error(t, err)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
}

func TestWorkspaceList_FiltersArchivedByDefault(t *testing.T) {
	hub := &recordingHub{
		workspaces: []*leapmuxv1.Workspace{
			{Id: "ws-active", Title: "Active"},
			{Id: "ws-old", Title: "Old"},
		},
		sections: &leapmuxv1.ListSectionsResponse{
			Sections: []*leapmuxv1.Section{
				{Id: "sec-arch", Name: "Archived", SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED},
			},
			Items: []*leapmuxv1.SectionItem{
				{WorkspaceId: "ws-old", SectionId: "sec-arch"},
			},
		},
	}
	startSpawnIPC(t, hub, nil)

	// Default hides archived workspaces.
	data := runControl(t, "workspace", "list").Data(t)
	rows := data["workspaces"].([]any)
	require.Len(t, rows, 1)
	assert.Equal(t, "ws-active", rows[0].(map[string]any)["id"])

	// --archived includes archived workspaces and marks archived=true.
	allData := runControl(t, "workspace", "list", "--archived").Data(t)
	allRows := allData["workspaces"].([]any)
	require.Len(t, allRows, 2)
	assert.Equal(t, true, allRows[1].(map[string]any)["archived"])
}

type phase2aAgentDispatcher struct {
	agents []*leapmuxv1.AgentInfo
}

func (d phase2aAgentDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	if req.GetMethod() != leapmuxv1connect.WorkerAgentServiceListAgentsProcedure {
		_ = w.SendError(12, req.GetMethod())
		return
	}
	payload, err := proto.Marshal(&leapmuxv1.ListAgentsResponse{Agents: d.agents})
	if err != nil {
		_ = w.SendError(13, err.Error())
		return
	}
	_ = w.SendResponse(channelwire.NewPayloadResponse(payload))
}

func TestAgentList_FiltersByStateProviderAndParent(t *testing.T) {
	hub := &recordingHub{
		tabs: []*leapmuxv1.WorkspaceTab{
			{Tab: &leapmuxv1.TabIdent{TabId: "11111111-1111-1111-1111-111111111111", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
			{Tab: &leapmuxv1.TabIdent{TabId: "22222222-2222-2222-2222-222222222222", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, WorkerId: "worker-A", WorkspaceId: "ws-1"},
		},
	}
	agents := []*leapmuxv1.AgentInfo{
		{
			Id:            "11111111-1111-1111-1111-111111111111",
			Title:         "Parent Agent",
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
			WorkerId:      "worker-A",
		},
		{
			Id:            "22222222-2222-2222-2222-222222222222",
			Title:         "Child Agent",
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
			ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
			ParentAgentId: "11111111-1111-1111-1111-111111111111",
			WorkerId:      "worker-A",
		},
	}
	startSpawnIPC(t, hub, phase2aAgentDispatcher{agents: agents})
	t.Setenv(controlenv.TabIDVariable, "22222222-2222-2222-2222-222222222222")
	t.Setenv(controlenv.TabTypeVariable, "agent")
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	// --state idle
	idle := runControl(t, "agent", "list", "--state", "idle").Data(t)["agents"].([]any)
	require.Len(t, idle, 1)
	assert.Equal(t, "22222222-2222-2222-2222-222222222222", idle[0].(map[string]any)["id"])

	// --provider claude
	claude := runControl(t, "agent", "list", "--provider", "claude").Data(t)["agents"].([]any)
	require.Len(t, claude, 1)
	assert.Equal(t, "11111111-1111-1111-1111-111111111111", claude[0].(map[string]any)["id"])

	// --parent 11111111
	kids := runControl(t, "agent", "list", "--parent", "11111111").Data(t)["agents"].([]any)
	require.Len(t, kids, 1)
	assert.Equal(t, "22222222-2222-2222-2222-222222222222", kids[0].(map[string]any)["id"])

	// Relational "parent" target on `agent get parent`
	parent := runControl(t, "agent", "get", "parent").Data(t)
	assert.Equal(t, "11111111-1111-1111-1111-111111111111", parent["id"])
}

func TestResolve_BareTitleAndNameFallback(t *testing.T) {
	l := resolve.Listing{
		Tabs: func(context.Context) ([]*leapmuxv1.WorkspaceTab, error) {
			return []*leapmuxv1.WorkspaceTab{
				{Tab: &leapmuxv1.TabIdent{TabId: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}},
				{Tab: &leapmuxv1.TabIdent{TabId: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}},
			}, nil
		},
		TabTitles: func(context.Context, []*leapmuxv1.WorkspaceTab) map[string]string {
			return map[string]string{
				"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa": "reviewer",
				"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb": "Release Notes",
			}
		},
		Workspaces: func(context.Context) ([]*leapmuxv1.Workspace, error) {
			return []*leapmuxv1.Workspace{
				{Id: "ws-1111", Title: "Release 1.4"},
			}, nil
		},
		Workers: func(context.Context) ([]*leapmuxv1.Worker, error) {
			return []*leapmuxv1.Worker{
				{Id: "wk-1111", Online: true},
			}, nil
		},
		WorkerNames: func(context.Context, []*leapmuxv1.Worker) map[string]string {
			return map[string]string{"wk-1111": "build-box"}
		},
	}

	gotAgent, err := resolve.Canonical(context.Background(), l, resolve.KindAgent, "REVIEWER")
	require.NoError(t, err)
	assert.Equal(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", gotAgent.ID)

	gotWS, err := resolve.Canonical(context.Background(), l, resolve.KindWorkspace, "release 1.4")
	require.NoError(t, err)
	assert.Equal(t, "ws-1111", gotWS.ID)

	gotWorker, err := resolve.Canonical(context.Background(), l, resolve.KindWorker, "BUILD-BOX")
	require.NoError(t, err)
	assert.Equal(t, "wk-1111", gotWorker.ID)

	_, err = resolve.Canonical(context.Background(), l, resolve.KindWorker, "missing-worker")
	ce := cli.FromError(err)
	assert.Equal(t, cli.CodeNotFound, ce.Code)
	assert.Contains(t, ce.Hints, `list available workers with "leapmux worker list"`)
}

type notifyWriter struct {
	w       io.Writer
	onWrite func([]byte)
}

func (n *notifyWriter) Write(p []byte) (int, error) {
	written, err := n.w.Write(p)
	if n.onWrite != nil && written > 0 {
		n.onWrite(p[:written])
	}
	return written, err
}
