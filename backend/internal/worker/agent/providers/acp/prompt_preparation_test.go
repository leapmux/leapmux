package acp

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPromptPreparationRefusesBeforeWritingAndAllowsRetry(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, _ := newACPTurnBase(t, out)
	failure := errors.New("this model does not accept audio")
	b.hooks.PreparePrompt = func(sessionID string, _ []agent.ClassifiedAttachment, blocks []map[string]any) error {
		assert.Equal(t, "session-1", sessionID)
		require.Len(t, blocks, 1)
		assert.Equal(t, "recording", blocks[0]["text"])
		return failure
	}
	require.ErrorIs(t, b.SendInput("recording", nil), failure)
	assert.False(t, b.PromptActive())
	assert.Empty(t, []byte(out.String()))
	b.hooks.PreparePrompt = nil
	require.NoError(t, b.SendInput("retry", nil))
	assert.NotEmpty(t, []byte(out.String()))
}

func TestPromptPreparationRunsForChildrenWithoutRootSettings(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, _ := newACPTurnBase(t, out)
	b.hooks.PromptParams = func(fields map[string]any) { fields["rootSetting"] = true }
	var sessions []string
	b.hooks.PreparePrompt = func(sessionID string, _ []agent.ClassifiedAttachment, blocks []map[string]any) error {
		sessions = append(sessions, sessionID)
		require.Len(t, blocks, 1)
		blocks[0]["text"] = "prepared"
		return nil
	}
	target := ChildSessionTarget{RootSessionID: b.CurrentSessionID(), SessionID: "child", RowKey: "child-row"}
	require.True(t, b.AttachChildSession(target))
	require.NoError(t, b.SendChildPromptDetached(target, "input", nil, func(json.RawMessage, error) {}))
	var request struct {
		Params map[string]any `json:"params"`
	}
	require.NoError(t, json.Unmarshal([]byte(out.String()), &request))
	assert.Equal(t, "child", request.Params["sessionId"])
	assert.NotContains(t, request.Params, "rootSetting")
	assert.Equal(t, []string{"child"}, sessions)
	assert.Contains(t, string([]byte(out.String())), "prepared")
}

func TestDetachedPromptRefusesAReplacedSession(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, _ := newACPTurnBase(t, out)
	err := b.SendPromptDetached("old-session", "input", []*leapmuxv1.Attachment{}, func(json.RawMessage, error) {})
	require.Error(t, err)
	assert.Empty(t, []byte(out.String()))
}

func TestChildPromptRefusesAnUnservedSessionBeforePreparation(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, _ := newACPTurnBase(t, out)
	prepared := false
	b.hooks.PreparePrompt = func(string, []agent.ClassifiedAttachment, []map[string]any) error {
		prepared = true
		return nil
	}
	err := b.SendChildPromptDetached(ChildSessionTarget{RootSessionID: b.CurrentSessionID(), SessionID: "unserved-child", RowKey: "child-row"}, "input", nil, func(json.RawMessage, error) {})
	assert.ErrorIs(t, err, agent.ErrChildRouteNotReady)
	assert.False(t, prepared)
	assert.Empty(t, out.String())
}

func TestChildOperationsRefuseAnUninitializedProcess(t *testing.T) {
	t.Parallel()
	var b Base
	target := ChildSessionTarget{RootSessionID: "root", SessionID: "child", RowKey: "row"}
	assert.ErrorIs(t, b.SendChildPromptDetached(target, "input", nil, func(json.RawMessage, error) {}), agent.ErrChildRouteNotReady)
	_, err := b.LoadChildSession(target)
	assert.ErrorIs(t, err, agent.ErrChildRouteNotReady)
}

func TestChildPromptRejectsInvalidAndRetiredTargetsBeforePreparation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*Base, *ChildSessionTarget)
	}{
		{name: "empty root", change: func(_ *Base, target *ChildSessionTarget) { target.RootSessionID = "" }},
		{name: "empty child", change: func(_ *Base, target *ChildSessionTarget) { target.SessionID = "" }},
		{name: "empty row", change: func(_ *Base, target *ChildSessionTarget) { target.RowKey = "" }},
		{name: "root as child", change: func(_ *Base, target *ChildSessionTarget) { target.SessionID = target.RootSessionID }},
		{name: "wrong root", change: func(_ *Base, target *ChildSessionTarget) { target.RootSessionID = "another-root" }},
		{name: "replaced root", change: func(b *Base, _ *ChildSessionTarget) { b.SetSessionIDForTest("replacement-root") }},
		{name: "absent root", change: func(b *Base, _ *ChildSessionTarget) { b.SetSessionIDForTest("") }},
		{name: "stopped process", change: func(b *Base, _ *ChildSessionTarget) { b.SetStoppedForTest(true) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := &agenttest.Stdin{}
			b, _ := newACPTurnBase(t, out)
			target := ChildSessionTarget{RootSessionID: b.CurrentSessionID(), SessionID: "child", RowKey: "row"}
			require.True(t, b.AttachChildSession(target))
			tc.change(b, &target)
			prepared := false
			b.hooks.PreparePrompt = func(string, []agent.ClassifiedAttachment, []map[string]any) error {
				prepared = true
				return nil
			}
			err := b.SendChildPromptDetached(target, "input", nil, func(json.RawMessage, error) {})
			assert.ErrorIs(t, err, agent.ErrChildRouteNotReady)
			assert.False(t, prepared)
			assert.Empty(t, out.String())
			assert.False(t, b.AttachChildSession(target))
		})
	}
}

func TestPromptPreparationReceivesTheOriginalClassifiedAudio(t *testing.T) {
	t.Parallel()
	for _, child := range []bool{false, true} {
		t.Run(map[bool]string{false: "root", true: "child"}[child], func(t *testing.T) {
			t.Parallel()
			out := &agenttest.Stdin{}
			b, _ := newACPTurnBase(t, out)
			wantSession := "session-1"
			if child {
				wantSession = "child-session"
			}
			b.hooks.PreparePrompt = func(sessionID string, attachments []agent.ClassifiedAttachment, blocks []map[string]any) error {
				assert.Equal(t, wantSession, sessionID)
				require.Len(t, attachments, 1)
				assert.Equal(t, contracts.AttachmentMediaKindAudio, attachments[0].Kind)
				assert.Equal(t, "audio/wav", attachments[0].MIMEType)
				assert.Equal(t, []byte{0, 255, 1}, attachments[0].Data)
				require.Len(t, blocks, 2)
				assert.Equal(t, map[string]any{"type": "text", "text": "listen"}, blocks[0])
				assert.Equal(t, map[string]any{"type": "resource", "resource": map[string]any{"uri": "voice.wav", "mimeType": "audio/wav", "blob": "AP8B"}}, blocks[1])
				return nil
			}
			attachments := []*leapmuxv1.Attachment{{Filename: "voice.wav", MimeType: "audio/wav", Data: []byte{0, 255, 1}}}
			if child {
				target := ChildSessionTarget{RootSessionID: b.CurrentSessionID(), SessionID: wantSession, RowKey: "child-row"}
				require.True(t, b.AttachChildSession(target))
				require.NoError(t, b.SendChildPromptDetached(target, "listen", attachments, func(json.RawMessage, error) {}))
			} else {
				require.NoError(t, b.SendInput("listen", attachments))
			}
		})
	}
}
