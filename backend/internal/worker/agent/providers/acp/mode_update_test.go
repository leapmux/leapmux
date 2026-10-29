package acp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACPCurrentModeUpdatesTheSharedSettings(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		mode ModeChannel
	}{
		{"permission mode", ModeChannelPermissionMode},
		{"primary agent", ModeChannelPrimaryAgent},
		{"unmapped mode", ModeChannelUnmapped},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			a, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
			a.hooks.ModeChannel = test.mode
			a.handleACPUpdate(json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Retained text"}}`))
			original := json.RawMessage(` {"sessionUpdate":"current_mode_update", "currentModeId":"plan", "future":9007199254740993} `)
			a.handleACPUpdate(original)
			assert.Equal(t, "plan", *a.secondaryChannel().field)
			assert.Equal(t, 1, sink.SettingsRefreshCount())
			require.Len(t, sink.Messages(), 1)
			assert.Equal(t, []byte(original), sink.Messages()[0].Content)
			a.turnMu.Lock()
			assert.Equal(t, "Retained text", a.turnAssistantText.String())
			a.turnMu.Unlock()
		})
	}
}

func TestModeSetterRequiresOneValidAcknowledgmentForItsNativeSession(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		setter func(*Base, func(string)) error
	}{
		{"missing acknowledgment", func(_ *Base, _ func(string)) error { return nil }},
		{"empty value", func(_ *Base, acknowledge func(string)) error { acknowledge(""); return nil }},
		{"replaced session", func(b *Base, acknowledge func(string)) error {
			b.SetSessionIDForTest("new-session")
			acknowledge("yolo")
			return nil
		}},
		{"failed reply", func(_ *Base, _ func(string)) error { return errors.New("native mode failure") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := &Base{}
			b.SetSessionIDForTest("session-1")
			b.SetPermissionMode("default")
			b.HooksForTest().ModeSetter = func(_ string, acknowledge func(string)) error { return test.setter(b, acknowledge) }
			require.Error(t, b.setSecondary("yolo"))
			assert.Equal(t, "default", b.PermissionModeForTest())
		})
	}
}

func TestModeSetterIgnoresDuplicateAndPostReturnAcknowledgments(t *testing.T) {
	t.Parallel()
	b := &Base{}
	b.SetSessionIDForTest("session-1")
	var late func(string)
	b.HooksForTest().ModeSetter = func(_ string, acknowledge func(string)) error {
		late = acknowledge
		acknowledge("yolo")
		acknowledge("default")
		return nil
	}
	require.NoError(t, b.setSecondary("yolo"))
	assert.Equal(t, "yolo", b.PermissionModeForTest())
	late("plan")
	assert.Equal(t, "yolo", b.PermissionModeForTest())
}

func TestModeSetterKeepsAConcurrentNativeEventBeforeTheWaiterReturns(t *testing.T) {
	t.Parallel()
	b := &Base{}
	b.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	b.SetSessionIDForTest("session-1")
	acknowledged := make(chan struct{})
	finish := make(chan struct{})
	b.HooksForTest().ModeSetter = func(value string, acknowledge func(string)) error {
		acknowledge(value)
		close(acknowledged)
		<-finish
		return nil
	}
	result := make(chan error, 1)
	go func() { result <- b.setSecondary("yolo") }()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-acknowledged:
	case err := <-result:
		t.Fatalf("the setter returned before its acknowledgment: %v", err)
	case <-ctx.Done():
		close(finish)
		t.Fatal("the setter did not acknowledge its native value")
	}
	b.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"current_mode_update","currentModeId":"plan"}}}`))
	close(finish)
	require.NoError(t, <-result)
	assert.Equal(t, "plan", b.PermissionModeForTest())
}

func TestModeWritersRejectAnAbsentAcknowledgmentCallback(t *testing.T) {
	t.Parallel()
	b := &Base{}
	assert.ErrorContains(t, b.acpSetMode("default", nil, nil), "requires an acknowledgment")
	assert.ErrorContains(t, b.SetModeViaConfigOption("default", nil), "requires an acknowledgment")
}

func TestACPModeWritersRequireAnObjectAcknowledgment(t *testing.T) {
	t.Parallel()
	for _, method := range []string{MethodSessionSetMode, MethodSessionSetConfigOption} {
		for _, response := range []string{`null`, `[]`, `true`, `0`, `"accepted"`, `{}`, `{"_meta":{"native":true}}`, `{"_meta":null}`} {
			t.Run(method+"/"+response, func(t *testing.T) {
				t.Parallel()
				a, _ := newTestAgentForRPCWithResponder(t, func(string) agenttest.RPCReply {
					return agenttest.RPCReply{Result: json.RawMessage(response)}
				})
				a.SetPermissionMode("default")
				if method == MethodSessionSetConfigOption {
					a.hooks.ModeSetter = a.SetModeViaConfigOption
				}
				err := a.setSecondary("yolo")
				if response[0] == '{' {
					require.NoError(t, err)
					assert.Equal(t, "yolo", a.PermissionModeForTest())
				} else {
					require.Error(t, err, "an ACP acknowledgment must be an object before it can confirm a mode")
					assert.Equal(t, "default", a.PermissionModeForTest())
				}
			})
		}
	}
}

func TestACPCurrentModeRejectsInvalidValuesAndAvoidsDuplicateRefreshes(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	a, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
	a.permissionMode = "plan"
	for _, payload := range []string{
		`{"sessionUpdate":"current_mode_update"}`,
		`{"sessionUpdate":"current_mode_update","currentModeId":""}`,
		`{"sessionUpdate":"current_mode_update","currentModeId":null}`,
		`{"sessionUpdate":"current_mode_update","currentModeId":0}`,
		`{"sessionUpdate":"current_mode_update","currentModeId":"plan"}`,
	} {
		a.handleACPUpdate(json.RawMessage(payload))
		assert.Equal(t, "plan", a.permissionMode)
	}
	assert.Zero(t, sink.SettingsRefreshCount())
	assert.Len(t, sink.Messages(), 5)
}

func TestObserveCurrentMode_UpdatesTheAxisOnce(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}
	b.hooks.ModeChannel = ModeChannelPermissionMode

	b.ObserveCurrentMode("plan")
	b.ObserveCurrentMode("plan")
	b.ObserveCurrentMode("")

	assert.Equal(t, "plan", b.PermissionModeForTest())
	assert.Equal(t, 1, sink.SettingsRefreshCount(), "a repeated report changes nothing, and an empty one states nothing")
}

func TestCurrentNativeModeTracksReportsInsteadOfLocalSelections(t *testing.T) {
	t.Parallel()
	b := &Base{sink: agent.NewProviderServices(&agenttest.Sink{})}
	b.hooks.ModeChannel = ModeChannelPermissionMode
	b.applyHandshakeMode(&SessionResult{CurrentModeID: "normal"}, "normal")
	assert.Equal(t, "normal", b.CurrentNativeMode())
	b.SetPermissionMode("goal")
	assert.Equal(t, "normal", b.CurrentNativeMode())
	b.ObserveCurrentMode("plan")
	assert.Equal(t, "plan", b.CurrentNativeMode())
}

func TestModeSetterKeepsANativeChangeAfterItsAcknowledgment(t *testing.T) {
	t.Parallel()
	b := &Base{}
	b.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	b.SetSessionIDForTest("session-1")
	b.HooksForTest().ModeChannel = ModeChannelPermissionMode
	b.SetPermissionMode("default")
	b.HooksForTest().ModeSetter = func(value string, acknowledge func(string)) error {
		acknowledge(value)
		b.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"current_mode_update","currentModeId":"plan"}}}`))
		return nil
	}
	require.NoError(t, b.setSecondary("yolo"))
	assert.Equal(t, "plan", b.PermissionModeForTest(), "the later native event must survive the return of the setter")
}

func TestWriteSecondarySettingRejectsAnAbsentWriter(t *testing.T) {
	t.Parallel()
	b := &Base{}
	require.ErrorContains(t, b.WriteSecondarySetting("plan", nil), "mode writer is required")
}

func TestSetModeAckKeepsTheReportedMode(t *testing.T) {
	for _, channel := range []ModeChannel{ModeChannelPermissionMode, ModeChannelPrimaryAgent} {
		for _, initial := range []string{"default", "vibe"} {
			for _, update := range []string{
				`{"sessionUpdate":"current_mode_update","currentModeId":"vibe"}`,
				`{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","category":"mode","name":"Mode","type":"select","currentValue":"vibe","options":[{"value":"vibe","name":"Vibe"},{"value":"plan","name":"Plan"}]}]}`,
			} {
				t.Run(fmt.Sprintf("channel=%d/initial=%s/update=%s", channel, initial, update), func(t *testing.T) {
					b := &Base{sink: agent.NewProviderServices(&agenttest.Sink{})}
					b.sessionID = "session-1"
					b.hooks.ModeChannel = channel
					axis := b.secondaryChannel()
					*axis.field = initial
					b.hooks.ModeSetter = func(value string, acknowledge func(string)) error {
						b.HandleOutput([]byte(`{"method":"session/update","params":{"sessionId":"session-1","update":` + update + `}}`))
						acknowledge(value)
						return nil
					}
					require.NoError(t, b.setSecondary("plan"))
					assert.Equal(t, "vibe", *axis.field)
				})
			}
		}
	}
}

func TestSetModeAckIgnoresUnrelatedReports(t *testing.T) {
	for _, frame := range []string{
		`{"method":"session/update","params":{"sessionId":"other","update":{"sessionUpdate":"current_mode_update","currentModeId":"vibe"}}}`,
		`{"method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"effort","category":"thought_level","name":"Effort","type":"select","currentValue":"high","options":[{"value":"high","name":"High"}]}]}}}`,
	} {
		b := &Base{sink: agent.NewProviderServices(&agenttest.Sink{})}
		b.sessionID = "session-1"
		b.hooks.ModeChannel = ModeChannelPermissionMode
		b.hooks.ModeSetter = func(value string, acknowledge func(string)) error {
			b.HandleOutput([]byte(frame))
			acknowledge(value)
			return nil
		}
		require.NoError(t, b.setSecondary("plan"))
		assert.Equal(t, "plan", b.PermissionModeForTest())
	}
}

func TestStartupKeepsTheReportedModeWhenTheStoredModeIsGone(t *testing.T) {
	a, requests := newTestAgentForRPC(t)
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.permissionMode = "vibe"
	a.availableModes = []*leapmuxv1.AvailableOption{nil, {Id: "vibe", Name: "Vibe"}, {Id: "plan", Name: "Plan"}}
	require.NoError(t, a.applyStartupPermissionMode("probe-agent", false))
	assert.Equal(t, "vibe", a.permissionMode)
	assert.Empty(t, requests())
	require.Len(t, sink.LeapMuxNotifications(), 1)
	assert.Equal(t, contracts.NotificationTypeAgentStatus, sink.LeapMuxNotifications()[0][contracts.NotificationFieldType])
	assert.Equal(t, "The session does not offer the mode probe-agent. It runs the mode Vibe.", sink.LeapMuxNotifications()[0][contracts.NotificationFieldText])
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "vibe", sink.LastSettingsRefresh().PermissionMode)
}

func TestStartupFallbackHookSuppressesTheRow(t *testing.T) {
	a, requests := newTestAgentForRPC(t)
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.permissionMode = "vibe"
	a.availableModes = []*leapmuxv1.AvailableOption{{Id: "vibe", Name: "Vibe"}}
	a.hooks.AgentStatesModeFallback = true
	require.NoError(t, a.applyStartupPermissionMode("probe-agent", false))
	assert.Empty(t, requests())
	assert.Empty(t, sink.LeapMuxNotifications())
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "vibe", sink.LastSettingsRefresh().PermissionMode)
}

func TestStartupDefaultedModeKeepsTheSilentFallback(t *testing.T) {
	a, requests := newTestAgentForRPC(t)
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.permissionMode = "vibe"
	a.availableModes = []*leapmuxv1.AvailableOption{{Id: "vibe", Name: "Vibe"}}
	require.NoError(t, a.applyStartupPermissionMode("safe-default", true))
	assert.Empty(t, requests())
	assert.Empty(t, sink.LeapMuxNotifications())
	assert.Zero(t, sink.SettingsRefreshCount())
}

func TestStartupWithoutAReportedModeKeepsTheNativeCheck(t *testing.T) {
	a, requests := newTestAgentForRPC(t)
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.availableModes = []*leapmuxv1.AvailableOption{{Id: "vibe", Name: "Vibe"}}
	require.Error(t, a.applyStartupPermissionMode("probe-agent", false))
	assert.Empty(t, requests())
	assert.Empty(t, sink.LeapMuxNotifications())
	assert.Zero(t, sink.SettingsRefreshCount())
}
