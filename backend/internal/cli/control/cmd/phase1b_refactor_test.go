package cmd

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

func TestPhase1B_AdminRowTablesRenderCleanHeadersAndQuietLines(t *testing.T) {
	cases := []struct {
		name        string
		result      any
		wantHeaders []string
		wantQuiet   []string
	}{
		{
			name: "settings list",
			result: adminSettingsListResult{Settings: []adminSettingRow{
				{Key: "signup_enabled", Customized: true, Propagation: "hot", EffectiveJSON: []byte("false")},
			}},
			wantHeaders: []string{"KEY", "CUSTOMIZED", "EFFECTIVE", "PROPAGATION"},
			wantQuiet:   []string{"signup_enabled"},
		},
		{
			name: "user list",
			result: adminUserListResult{Users: []adminUserRow{
				{ID: "u-11112222", Username: "alice", DisplayName: "Alice", Email: "alice@example.com", IsAdmin: true},
			}},
			wantHeaders: []string{"ID", "USERNAME", "DISPLAY NAME", "EMAIL", "ADMIN"},
			wantQuiet:   []string{"u-11112222"},
		},
		{
			name: "worker list",
			result: adminWorkerListResult{Workers: []adminWorkerRow{
				{ID: "w-11112222", Username: "alice", Status: "active"},
			}},
			wantHeaders: []string{"ID", "USERNAME", "STATUS", "LAST SEEN"},
			wantQuiet:   []string{"w-11112222"},
		},
		{
			name: "app list",
			result: adminAppListResult{Apps: []AppRegistrationRow{
				{ClientID: "lmx_app_1234", ClientName: "Bot", Visibility: "hub-wide", ClientType: "public", Verified: true, LiveCredentialCount: 2},
			}},
			wantHeaders: []string{"CLIENT ID", "NAME", "VISIBILITY", "TYPE", "VERIFIED", "CREDS"},
			wantQuiet:   []string{"lmx_app_1234"},
		},
		{
			name: "idp list",
			result: adminOAuthProviderListResult{Providers: []adminOAuthProviderRow{
				{ID: "p-11112222", Type: "github", Name: "GitHub", ClientID: "Iv1.abc", Enabled: true},
			}},
			wantHeaders: []string{"ID", "TYPE", "NAME", "CLIENT ID", "ENABLED"},
			wantQuiet:   []string{"p-11112222"},
		},
		{
			name: "api-token list",
			result: adminAPITokenListResult{Tokens: []adminAPITokenRow{
				{CredentialLifetime: CredentialLifetime{ID: "t-11112222", Username: "alice"}, ClientName: "CLI", InstallationName: "ci-1"},
			}},
			wantHeaders: []string{"ID", "USERNAME", "EXPIRES", "APP", "INSTALLATION"},
			wantQuiet:   []string{"t-11112222"},
		},
		{
			name: "delegation-token list",
			result: adminDelegationTokenListResult{Tokens: []adminDelegationTokenRow{
				{CredentialLifetime: CredentialLifetime{ID: "d-11112222", Username: "alice"}, WorkerID: "w-11112222"},
			}},
			wantHeaders: []string{"ID", "USERNAME", "EXPIRES", "WORKER"},
			wantQuiet:   []string{"d-11112222"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var text bytes.Buffer
			p := &cli.Printer{Out: &text, Err: &bytes.Buffer{}}
			require.NoError(t, p.Result(tc.result))
			for _, h := range tc.wantHeaders {
				assert.Contains(t, text.String(), h)
			}

			var quiet bytes.Buffer
			pq := &cli.Printer{Out: &quiet, Err: &bytes.Buffer{}, Format: cli.FormatQuiet}
			require.NoError(t, pq.Result(tc.result))
			assert.Equal(t, tc.wantQuiet[0]+"\n", quiet.String())
		})
	}
}

func TestPhase1B_AdminLeavesDeclareEnumsAndCompletions(t *testing.T) {
	leaves := adminLeaves(t)

	// Every admin target leaf must register a completion function.
	for _, path := range []string{
		"settings get", "settings set", "settings set-secret", "settings reset",
		"user get", "user update", "user delete", "user grant-admin", "user revoke-admin", "user reset-password",
		"session revoke", "session revoke-user",
		"worker get", "worker deregister", "worker reg-key revoke",
		"app update", "app verify", "app unverify", "app allow-elevation", "app deny-elevation", "app revoke", "app delete",
		"idp remove", "idp enable", "idp disable",
		"rate-limit set", "rate-limit enable", "rate-limit disable", "rate-limit reset",
		"api-token issue", "api-token revoke",
		"delegation-token revoke",
	} {
		leaf := leaves[path]
		require.NotNilf(t, leaf, "missing admin leaf %q", path)
		assert.NotNilf(t, leaf.ValidArgsFunction, "admin %s must register Complete", path)
	}

	// Rate-limit operations complete locally with zero network calls.
	vals, dir := leaves["rate-limit enable"].ValidArgsFunction(leaves["rate-limit enable"], nil, "elev")
	assert.Contains(t, vals, "elevation")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)

	// Enum annotations exist on admin enum flags.
	assert.Contains(t, leaves["worker list"].Flags().Lookup("status").Annotations["leapmux_enum"], "deregistering")
	assert.Contains(t, leaves["idp add"].Flags().Lookup("type").Annotations["leapmux_enum"], "github")
	assert.Contains(t, leaves["app register"].Flags().Lookup("type").Annotations["leapmux_enum"], "confidential")
	assert.Contains(t, leaves["app register"].Flags().Lookup("visibility").Annotations["leapmux_enum"], "private")
	assert.Contains(t, leaves["captcha set"].Flags().Lookup("provider").Annotations["leapmux_enum"], "turnstile")
	assert.Contains(t, leaves["captcha set"].Flags().Lookup("algorithm").Annotations["leapmux_enum"], "PBKDF2/SHA-256")
}

// singleChannelWorker counts calls and serves agent, terminal, viewer, system
// info, and control request RPCs over one channel.
type singleChannelWorker struct {
	mu      sync.Mutex
	methods []string
	calls   atomic.Int32
}

func (w *singleChannelWorker) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, writer channel.ResponseWriter) {
	w.mu.Lock()
	w.methods = append(w.methods, req.GetMethod())
	w.mu.Unlock()
	w.calls.Add(1)
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		writeProtoResponse(writer, &leapmuxv1.ListAgentsResponse{
			Agents: []*leapmuxv1.AgentInfo{{Id: "ag-11112222", Title: "Agent 1", ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE}},
		})
	case leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure:
		writeProtoResponse(writer, &leapmuxv1.ListTerminalsResponse{
			Terminals: []*leapmuxv1.TerminalInfo{{TerminalId: "tm-11112222", Title: "Shell", Status: leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY, Cols: 120, Rows: 40}},
		})
	case leapmuxv1connect.WorkerPrivateServiceGetTabPayloadProcedure:
		writeProtoResponse(writer, &leapmuxv1.GetTabPayloadResponse{
			Payload: &leapmuxv1.TabPayload{Kind: &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: "/repo/main.go"}}},
		})
	case leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure:
		writeProtoResponse(writer, &leapmuxv1.GetWorkerSystemInfoResponse{Name: "imac-pro"})
	case leapmuxv1connect.WorkerAgentServiceListControlRequestsProcedure:
		writeProtoResponse(writer, &leapmuxv1.ListControlRequestsResponse{
			Requests: []*leapmuxv1.AgentControlRequest{{
				RequestId:     "req-1",
				ClaimToken:    "tok1234",
				ResponseState: leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY,
				Prompt:        permissionPrompt(),
			}},
		})
	case leapmuxv1connect.WorkerAgentServiceAnswerControlProcedure:
		writeProtoResponse(writer, &leapmuxv1.AnswerControlResponse{
			State: leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED,
		})
	default:
		var ignored proto.Message = &leapmuxv1.GetWorkerSystemInfoResponse{}
		writeProtoResponse(writer, ignored)
	}
}

func TestPhase1B_SingleNoiseChannelForTabListTerminalListAndControlAnswer(t *testing.T) {
	state := newStateBuilder().
		workspace("ws-1", "tile-1").
		leafNode("tile-1", "", "0|a:").
		tab("ag-11112222", "tile-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tab("tm-11112222", "tile-1", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
		tab("fl-11112222", "tile-1", leapmuxv1.TabType_TAB_TYPE_FILE).
		st
	hub := &recordingHub{materialized: state}
	w := &singleChannelWorker{}
	h := startHubChannelHarness(t, hub, w)

	// tab list combines ListAgents + ListTerminals + GetTabPayload + GetWorkerSystemInfo on ONE channel.
	before := h.channelOpens()
	tabData := runControl(t, "tab", "list").Data(t)
	assert.Equal(t, 1, h.channelOpens()-before, "tab list must open exactly 1 E2EE channel per Worker")
	tabs, ok := tabData["tabs"].([]any)
	require.True(t, ok)
	require.Len(t, tabs, 3)

	// terminal list combines ListTerminals + GetWorkerSystemInfo on ONE channel.
	before = h.channelOpens()
	termData := runControl(t, "terminal", "list").Data(t)
	assert.Equal(t, 1, h.channelOpens()-before, "terminal list must open exactly 1 E2EE channel per Worker")
	terms, ok := termData["terminals"].([]any)
	require.True(t, ok)
	require.Len(t, terms, 1)
	assert.Equal(t, "imac-pro", terms[0].(map[string]any)["worker"])

	// agent approve reuses ONE channel across ListControlRequests + AnswerControl.
	before = h.channelOpens()
	ans := runControl(t, "agent", "approve", "ag-11112222", "tok1234").Data(t)
	assert.Equal(t, 1, h.channelOpens()-before, "agent approve must reuse 1 E2EE channel across ListControlRequests and AnswerControl")
	assert.Equal(t, "completed", ans["state"])
}
