package acp

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

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
