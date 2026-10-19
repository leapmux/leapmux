package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type permissionPeer struct {
	agenttest.Stdin
	agent    *Agent
	deferred bool
}

func (p *permissionPeer) Write(data []byte) (int, error) {
	n, err := p.Stdin.Write(data)
	var request struct {
		Type    string `json:"type"`
		ID      string `json:"request_id"`
		Request struct {
			Subtype string `json:"subtype"`
			Mode    string `json:"mode"`
		} `json:"request"`
	}
	if json.Unmarshal(data, &request) == nil && request.Type == claudeMsgTypeControlRequest && request.Request.Subtype == "set_permission_mode" && !p.deferred {
		response, _ := json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": request.ID, "response": map[string]string{"mode": request.Request.Mode}}})
		if !p.agent.handlePendingControlResponse(&providerkit.ParsedLine{Raw: response, Type: claudeMsgTypeControlResponse}) {
			p.agent.HandleOutput(response)
		}
	}
	return n, err
}

func newPermissionAgent(t *testing.T, deferred bool) (*Agent, *agenttest.ControlSink, *permissionPeer) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	sink := &agenttest.ControlSink{}
	peer := &permissionPeer{deferred: deferred}
	a := &Agent{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "claude-permissions", ProviderName: "claude", Ctx: ctx, Cancel: cancel, ProcessDone: done, Stdin: peer, APITimeout: 30 * time.Second}), sink: agent.NewProviderServices(sink), confirmedPermissionMode: contracts.ClaudeModeDefault, sessionID: "session"}
	peer.agent = a
	t.Cleanup(func() { close(done); a.Process.Stop() })
	return a, sink, peer
}

func nativeToolPermission(id, tool string) []byte {
	raw, _ := json.Marshal(map[string]any{"type": "control_request", "request_id": id, "request": map[string]any{"subtype": "can_use_tool", "tool_name": tool, "tool_use_id": id + "-tool", "input": map[string]any{"command": "printf hello"}}})
	return raw
}

func TestClaudeBypassApprovesPendingAndLaterToolsWithDeferredAcknowledgement(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		t.Run(fmt.Sprint(deferred), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				a, sink, peer := newPermissionAgent(t, deferred)
				a.HandleOutput(nativeToolPermission("first", "Bash"))
				a.HandleOutput(nativeToolPermission("question", ToolNameAskUserQuestion))
				a.HandleOutput(nativeToolPermission("plan", ToolNameExitPlanMode))
				result := a.UpdateSettings(map[string]string{agent.OptionIDPermissionMode: contracts.ClaudeModeBypassPermissions})
				require.True(t, result.AppliedLive)
				require.Eventually(t, func() bool { return len(sink.CanceledControls()) == 1 }, 30*time.Second, time.Millisecond)
				assert.Equal(t, []string{"first"}, sink.CanceledControls())
				assert.Contains(t, peer.String(), `"behavior":"allow"`)
				a.HandleOutput(nativeToolPermission("later", "Bash"))
				require.Eventually(t, func() bool { return strings.Contains(peer.String(), `"request_id":"later"`) }, 30*time.Second, time.Millisecond)
				assert.Equal(t, 3, sink.PublishedControlCount())
			})
		})
	}
}

func TestClaudeRefusesUnknownAndRepeatedPermissionAnswers(t *testing.T) {
	a, _, peer := newPermissionAgent(t, false)
	answer := []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"permission","response":{"behavior":"allow","updatedInput":{}}}}`)
	assert.Error(t, a.SendRawInput(answer, agent.StopContext{}))
	assert.Empty(t, peer.String())
	a.HandleOutput(nativeToolPermission("permission", "Bash"))
	require.NoError(t, a.SendRawInput(answer, agent.StopContext{}))
	assert.Error(t, a.SendRawInput(answer, agent.StopContext{}))
	assert.Equal(t, 1, strings.Count(peer.String(), `"request_id":"permission"`))
}

func TestClaudeDeferredModeReplyWithoutModeStillSettlesTheRequest(t *testing.T) {
	for _, subtype := range []string{"success", "error"} {
		t.Run(subtype, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				a, _, _ := newPermissionAgent(t, true)
				result := a.UpdateSettings(map[string]string{agent.OptionIDPermissionMode: contracts.ClaudeModeBypassPermissions})
				require.True(t, result.AppliedLive)
				a.Mu.Lock()
				change := a.permissionModeChange
				a.Mu.Unlock()
				require.NotNil(t, change)
				requestID := change.requestID
				require.NotEmpty(t, requestID)
				reply, _ := json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{"subtype": subtype, "request_id": requestID, "error": "The mode is unavailable."}})
				a.HandleOutput(reply)
				a.Mu.Lock()
				pending, mode := a.permissionModeChange, a.permissionModeLocked()
				a.Mu.Unlock()
				assert.Empty(t, pending)
				if subtype == "success" {
					assert.Equal(t, contracts.ClaudeModeBypassPermissions, mode)
				} else {
					assert.Equal(t, contracts.ClaudeModeDefault, mode)
				}
			})
		})
	}
}

func TestClaudeDuplicateControlRepliesDoNotBlockTheReader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, _, _ := newPermissionAgent(t, false)
		response := &providerkit.ParsedLine{Type: claudeMsgTypeControlResponse, Raw: []byte(`{"type":"control_response","response":{"subtype":"success","request_id":"request","response":{"mode":"default"}}}`)}
		channel, release, err := a.controlReplies.Register("request")
		require.NoError(t, err)
		defer release()
		require.True(t, a.handlePendingControlResponse(response))
		done := make(chan bool, 1)
		go func() { done <- a.handlePendingControlResponse(response) }()
		synctest.Wait()
		returned := false
		select {
		case consumed := <-done:
			returned = true
			assert.False(t, consumed)
		default:
			t.Error("a duplicate reply blocked the native reader")
		}
		<-channel
		synctest.Wait()
		if !returned {
			assert.False(t, <-done)
		}
	})
}

func TestClaudeLateModeRejectionStopsFurtherAutomaticApprovals(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, sink, peer := newPermissionAgent(t, true)
		a.HandleOutput(nativeToolPermission("first", "Bash"))
		require.True(t, a.UpdateSettings(map[string]string{agent.OptionIDPermissionMode: contracts.ClaudeModeBypassPermissions}).AppliedLive)
		require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
		a.Mu.Lock()
		change := a.permissionModeChange
		a.Mu.Unlock()
		require.NotNil(t, change)
		reply, err := json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "error", "request_id": change.requestID, "error": "The mode is unavailable."}})
		require.NoError(t, err)
		a.HandleOutput(reply)
		a.HandleOutput(nativeToolPermission("later", "Bash"))
		require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
		assert.Equal(t, 2, sink.PublishedControlCount())
		assert.Equal(t, 1, strings.Count(peer.String(), `"behavior":"allow"`))
		assert.Equal(t, contracts.ClaudeModeDefault, a.SettingsSnapshot().SurfacedOptions[agent.OptionIDPermissionMode])
		assert.Equal(t, agent.OptionSettlementUnresolved, a.SettingsSnapshot().Settlements[agent.OptionIDPermissionMode].State)
	})
}

func TestClaudeBypassPreservesNativeInputAndExcludesInteractions(t *testing.T) {
	for _, testCase := range []struct {
		name, request string
		automatic     bool
	}{
		{"exact input", `{"subtype":"can_use_tool","tool_name":"Bash","input":{"number":9007199254740993,"zero":0,"empty":"","flag":false}}`, true},
		{"interaction", `{"subtype":"can_use_tool","tool_name":"Bash","input":{},"requires_user_interaction":true}`, false},
		{"empty tool", `{"subtype":"can_use_tool","tool_name":"","input":{}}`, false},
		{"null input", `{"subtype":"can_use_tool","tool_name":"Bash","input":null}`, false},
		{"array input", `{"subtype":"can_use_tool","tool_name":"Bash","input":[]}`, false},
		{"hook", `{"subtype":"hook_callback","tool_name":"Bash","input":{}}`, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			a, sink, peer := newPermissionAgent(t, false)
			a.confirmedPermissionMode = contracts.ClaudeModeBypassPermissions
			a.HandleOutput([]byte(`{"type":"control_request","request_id":"permission","request":` + testCase.request + `}`))
			require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
			if testCase.automatic {
				assert.Zero(t, sink.PublishedControlCount())
				for _, part := range []string{`"number":9007199254740993`, `"zero":0`, `"empty":""`, `"flag":false`} {
					assert.Contains(t, peer.String(), part)
				}
			} else {
				assert.Equal(t, 1, sink.PublishedControlCount())
				assert.Empty(t, peer.String())
			}
		})
	}
}

type permissionPublicationSink struct {
	agenttest.ControlSink
	publish func()
}

func (s *permissionPublicationSink) PublishControlRequest(request agent.ControlRequest) error {
	err := s.ControlSink.PublishControlRequest(request)
	if s.publish != nil {
		s.publish()
	}
	return err
}

func TestClaudeModeChangeDuringPublicationApprovesTheSameRequest(t *testing.T) {
	a, _, peer := newPermissionAgent(t, false)
	sink := &permissionPublicationSink{}
	a.sink = agent.NewProviderServices(sink)
	sink.publish = func() {
		assert.True(t, a.UpdateSettings(map[string]string{agent.OptionIDPermissionMode: contracts.ClaudeModeBypassPermissions}).AppliedLive)
	}
	a.HandleOutput(nativeToolPermission("permission", "Bash"))
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
	assert.Equal(t, []string{"permission"}, sink.CanceledControls())
	assert.Contains(t, peer.String(), `"behavior":"allow"`)
}

func TestClaudeRootTurnEndKeepsChildControlsUntilTheirOwnResult(t *testing.T) {
	a, sink, _ := newPermissionAgent(t, false)
	a.HandleOutput(nativeToolPermission("root", "Bash"))
	child := strings.Replace(string(nativeToolPermission("child", "Bash")), `"subtype":"can_use_tool"`, `"subtype":"can_use_tool","agent_id":"child-agent"`, 1)
	a.HandleOutput([]byte(child))
	a.disarmTurn()
	assert.Equal(t, []string{"root"}, sink.CanceledControls())
	a.Mu.Lock()
	assert.Contains(t, a.incomingControls, "child")
	a.Mu.Unlock()
	a.settleToolControls([]contentBlock{{Type: "tool_result", ToolUseID: "child-tool"}})
	assert.Equal(t, []string{"root", "child"}, sink.CanceledControls())
	a.Mu.Lock()
	assert.Empty(t, a.incomingControls)
	a.Mu.Unlock()
}

func TestClaudeCanConfirmTheSameModeAfterAnUnresolvedReply(t *testing.T) {
	a, _, peer := newPermissionAgent(t, false)
	a.unresolvedSettings = map[string]struct{}{agent.OptionIDPermissionMode: {}}
	result := a.UpdateSettings(map[string]string{agent.OptionIDPermissionMode: contracts.ClaudeModeDefault})
	assert.Equal(t, agent.OptionSettlementConfirmed, result.Settlements[agent.OptionIDPermissionMode].State)
	assert.Contains(t, peer.String(), `"subtype":"set_permission_mode"`)
}
