package muse

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInputPartsRetainsTextAndImageBytes(t *testing.T) {
	t.Parallel()
	data := []byte{0, 1, 2, 255}
	parts, err := inputParts("", []*leapmuxv1.Attachment{{Filename: "proof.png", MimeType: "image/png", Data: data}})
	require.NoError(t, err)
	require.Len(t, parts, 2)
	require.NotNil(t, parts[0].Text)
	assert.Equal(t, "", *parts[0].Text)
	assert.Equal(t, base64.StdEncoding.EncodeToString(data), parts[1].Base64Data)
	assert.Equal(t, "image/png", parts[1].MediaType)
	raw, err := json.Marshal(parts)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"type":"text","text":""},{"type":"image","mediaType":"image/png","base64Data":"AAEC/w=="}]`, string(raw))
}

func TestInputPartsRejectsUnsupportedBinaryPayloads(t *testing.T) {
	t.Parallel()
	for _, media := range []string{"application/pdf", "application/octet-stream"} {
		_, err := inputParts("proof", []*leapmuxv1.Attachment{{Filename: "proof.bin", MimeType: media, Data: []byte{255, 1}}})
		require.Error(t, err)
	}
}

func TestMuseQueuedDispositionStartsNoTurn(t *testing.T) {
	for _, scenario := range []struct {
		disposition string
		started     bool
		want        bool
	}{
		{"queued", false, false}, {"started", false, true}, {"queued", true, true}, {"future", false, false},
	} {
		t.Run(fmt.Sprintf("%s/started=%t", scenario.disposition, scenario.started), func(t *testing.T) {
			a, sink := testAgent(t)
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
				assert.Equal(t, methodTurnStart, method)
				var params map[string]any
				require.NoError(t, json.Unmarshal(raw, &params))
				data, err := json.Marshal(map[string]any{"commandId": params["commandId"], "status": "accepted", "turnId": "turn", "disposition": scenario.disposition, "startedNewTurn": scenario.started})
				require.NoError(t, err)
				return agenttest.RPCReply{Result: data}
			}})
			require.NoError(t, a.SendInput("message", nil))
			if scenario.want {
				assert.Equal(t, "turn", a.sessions["session"].turnID)
				assert.Equal(t, []bool{true}, sink.TurnActiveCalls)
			} else {
				assert.Empty(t, a.sessions["session"].turnID)
				assert.Empty(t, sink.TurnActiveCalls)
				feed(t, a, methodTurnStarted, map[string]any{"sessionId": "session", "turnId": "turn"})
				assert.Equal(t, []bool{true}, sink.TurnActiveCalls)
			}
		})
	}
}

func TestMuseRetriedInputReusesTheCommandID(t *testing.T) {
	for _, repeatedFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("repeated-failure=%t", repeatedFailure), func(t *testing.T) {
			a, _ := testAgent(t)
			var ids []string
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(_ string, raw json.RawMessage) agenttest.RPCReply {
				var params struct {
					CommandID string `json:"commandId"`
				}
				require.NoError(t, json.Unmarshal(raw, &params))
				ids = append(ids, params.CommandID)
				if len(ids) == 1 || repeatedFailure && len(ids) == 2 {
					return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{"commandId":"` + params.CommandID + `","status":"accepted","disposition":"queued"}`)}
			}})
			require.ErrorIs(t, a.SendInput("message", nil), agent.ErrDeliveryUncertain)
			if repeatedFailure {
				require.ErrorIs(t, a.SendInput("message", nil), agent.ErrDeliveryUncertain)
			}
			require.NoError(t, a.SendInput("message", nil))
			for _, id := range ids {
				assert.Equal(t, ids[0], id)
			}
			require.NoError(t, a.SendInput("message", nil))
			assert.NotEqual(t, ids[0], ids[len(ids)-1], "a successful input clears the uncertain command")
		})
	}
}

func TestMuseRetriedInputDoesNotReuseAnotherInputIdentity(t *testing.T) {
	for _, change := range []string{"text", "attachment", "session", "steer"} {
		t.Run(change, func(t *testing.T) {
			a, _ := testAgent(t)
			var ids []string
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(_ string, raw json.RawMessage) agenttest.RPCReply {
				var params struct {
					CommandID string `json:"commandId"`
				}
				require.NoError(t, json.Unmarshal(raw, &params))
				ids = append(ids, params.CommandID)
				if len(ids) == 1 {
					return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
				}
				data, err := json.Marshal(map[string]any{"commandId": params.CommandID, "status": "accepted", "disposition": museDispositionQueued})
				require.NoError(t, err)
				return agenttest.RPCReply{Result: data}
			}})
			require.ErrorIs(t, a.SendInput("message", nil), agent.ErrDeliveryUncertain)
			switch change {
			case "text":
				require.NoError(t, a.SendInput("different", nil))
			case "attachment":
				require.NoError(t, a.SendInput("message", []*leapmuxv1.Attachment{{Filename: "note.txt", MimeType: "text/plain", Data: []byte("different")}}))
			case "session":
				a.sessionID = "replacement"
				require.NoError(t, a.SendInput("message", nil))
				a.sessionID = "session"
			case "steer":
				a.startTurn("session", "turn")
				require.NoError(t, a.SteerInput("message", nil))
				a.sessions["session"].turnID = ""
			}
			require.NoError(t, a.SendInput("message", nil))
			require.Len(t, ids, 3)
			assert.NotEqual(t, ids[0], ids[1])
			assert.NotEqual(t, ids[0], ids[2])
		})
	}
}

func TestMuseRetriedInputForgetsAConfirmedRefusal(t *testing.T) {
	a, _ := testAgent(t)
	var ids []string
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(_ string, raw json.RawMessage) agenttest.RPCReply {
		var params struct {
			CommandID string `json:"commandId"`
		}
		require.NoError(t, json.Unmarshal(raw, &params))
		ids = append(ids, params.CommandID)
		if len(ids) == 1 {
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"The input is invalid."}`)}
	}})
	require.ErrorIs(t, a.SendInput("message", nil), agent.ErrDeliveryUncertain)
	require.Error(t, a.SendInput("message", nil))
	require.Error(t, a.SendInput("message", nil))
	require.Len(t, ids, 3)
	assert.Equal(t, ids[0], ids[1])
	assert.NotEqual(t, ids[0], ids[2])
}
