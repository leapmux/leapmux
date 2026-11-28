package acp

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadSessionCapabilities(t *testing.T) {
	t.Parallel()

	cases := []struct {
		response  string
		wantClose bool
		wantFork  bool
	}{
		{response: `{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"fork":{}}}}`, wantClose: false, wantFork: true},
		{response: `{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"fork":true}}}`, wantClose: false, wantFork: true},
		{response: `{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"list":{},"delete":{},"fork":{}}}}`, wantClose: false, wantFork: true},
		{response: `{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"close":{},"fork":{}}}}`, wantClose: true, wantFork: true},
		{response: `{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"fork":null}}}`, wantClose: false, wantFork: false},
		{response: `{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"fork":false}}}`, wantClose: false, wantFork: false},
		{response: `{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"close":{}}}}`, wantClose: true, wantFork: false},
		{response: `{"protocolVersion":1,"agentCapabilities":{}}`, wantClose: false, wantFork: false},
		{response: `{"protocolVersion":1}`, wantClose: false, wantFork: false},
		{response: `not json`, wantClose: false, wantFork: false},
	}

	for _, tc := range cases {
		gotClose, gotFork := readSessionCapabilities([]byte(tc.response))
		assert.Equal(t, tc.wantClose, gotClose, tc.response)
		assert.Equal(t, tc.wantFork, gotFork, tc.response)
	}
}

func TestStartACPHandshake_SupportsNativeForkAndForksSession(t *testing.T) {
	t.Parallel()

	var adjusted []string
	hooks := Hooks{
		SessionParams: func(method string, params map[string]any) {
			adjusted = append(adjusted, method)
			params["_meta"] = map[string]any{"provider": "extra"}
		},
	}
	peer := &handshakePeer{
		linesBefore: map[string][]string{
			MethodSessionFork: {
				string(acptest.Chunk("forked-2", contracts.ACPUpdateUserMessageChunk, "REPLAYED_PROMPT")),
				string(acptest.Chunk("forked-2", contracts.ACPUpdateAgentMessageChunk, "REPLAYED_HISTORY")),
			},
			MethodSessionPrompt: {
				string(acptest.Chunk("forked-2", contracts.ACPUpdateAgentMessageChunk, "LIVE_AFTER_FORK")),
			},
		},
	}

	b, sink, session := runHandshakeForTest(
		t,
		peer,
		hooks,
		agent.Options{WorkingDir: "/work/tree", Session: agent.ForkSession("source-1", "")},
		acpDefaultSessionConfig,
		func(method string) json.RawMessage {
			switch method {
			case MethodInitialize:
				return json.RawMessage(`{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"fork":{}}}}`)
			case MethodSessionPrompt:
				return json.RawMessage(`{"stopReason":"end_turn"}`)
			default:
				return json.RawMessage(`{"sessionId":"forked-2"}`)
			}
		},
	)
	b.finishSessionUpdates()

	assert.True(t, b.SupportsNativeFork())
	assert.Equal(t, []string{MethodSessionFork}, adjusted)
	assert.Equal(t, "forked-2", session.SessionID)
	assert.Equal(t, "forked-2", b.CurrentSessionID())
	assert.Equal(t, "forked-2", sink.LastSessionID())

	requests := peer.recorded()
	require.Len(t, requests, 2)
	assert.Equal(t, MethodInitialize, requests[0].Method)
	assert.Equal(t, MethodSessionFork, requests[1].Method)
	assert.Equal(t, "source-1", requests[1].Params["sessionId"])
	assert.Equal(t, "/work/tree", requests[1].Params["cwd"])
	assert.Equal(t, []any{}, requests[1].Params["mcpServers"])
	assert.Equal(t, map[string]any{"provider": "extra"}, requests[1].Params["_meta"])

	// Replay cutoff drops chunks written before the session/fork reply.
	assert.Empty(t, sink.Messages(), "history replayed before session/fork reply must be dropped")

	// Live output after the fork handshake is preserved.
	require.NoError(t, agenttest.SendInput(b, "NEXT", nil))
	testutil.RequireEventually(t, func() bool { return !b.PromptActive() })
	assert.Equal(t, []string{"text:LIVE_AFTER_FORK"}, assembledTexts(t, sink.Messages()))
}

func TestStartACPHandshake_ForkRequiresLoadLoadsSourceBeforeForking(t *testing.T) {
	t.Parallel()

	peer := &handshakePeer{}
	cfg := SessionConfig{
		NewMethod:        MethodSessionNew,
		ResumeMethod:     MethodSessionResume,
		ForkRequiresLoad: true,
	}
	b, sink, session := runHandshakeForTest(
		t,
		peer,
		Hooks{},
		agent.Options{WorkingDir: "/work/tree", Session: agent.ForkSession("source-1", "")},
		cfg,
		func(method string) json.RawMessage {
			switch method {
			case MethodInitialize:
				return json.RawMessage(`{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"fork":{}}}}`)
			case MethodSessionResume:
				return json.RawMessage(`{"sessionId":"source-1"}`)
			default:
				return json.RawMessage(`{"sessionId":"forked-3"}`)
			}
		},
	)
	b.finishSessionUpdates()

	assert.Equal(t, "forked-3", session.SessionID)
	assert.Equal(t, "forked-3", b.CurrentSessionID())
	assert.Equal(t, []string{"forked-3"}, sink.SessionIDs())

	requests := peer.recorded()
	require.Len(t, requests, 3)
	assert.Equal(t, MethodInitialize, requests[0].Method)
	assert.Equal(t, MethodSessionResume, requests[1].Method)
	assert.Equal(t, "source-1", requests[1].Params["sessionId"])
	assert.Equal(t, MethodSessionFork, requests[2].Method)
	assert.Equal(t, "source-1", requests[2].Params["sessionId"])
	assert.Equal(t, []any{}, requests[2].Params["mcpServers"])
}

func TestStartACPHandshake_ForkRequiresLoadAbortsOnLoadError(t *testing.T) {
	t.Parallel()

	peer := &handshakePeer{
		errors: map[string]any{
			MethodSessionLoad: map[string]any{"code": -32000, "message": "source session load failed"},
		},
	}
	cfg := SessionConfig{
		NewMethod:        MethodSessionNew,
		ResumeMethod:     MethodSessionLoad,
		ForkRequiresLoad: true,
	}
	rig := newHandshakeRig(t, peer, Hooks{}, nil, func(method string) json.RawMessage {
		switch method {
		case MethodInitialize:
			return json.RawMessage(`{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"fork":{}}}}`)
		default:
			return json.RawMessage(`{"sessionId":"unexpected-fork"}`)
		}
	})

	session, err := rig.handshake(agent.Options{WorkingDir: "/work/tree", Session: agent.ForkSession("source-1", "")}, cfg)
	require.Nil(t, session)
	require.Error(t, err)
	assert.ErrorContains(t, err, "source session load failed")

	requests := peer.recorded()
	require.Len(t, requests, 2, "must abort before sending session/fork when pre-fork load fails")
	assert.Equal(t, MethodInitialize, requests[0].Method)
	assert.Equal(t, MethodSessionLoad, requests[1].Method)
	assert.Equal(t, "source-1", requests[1].Params["sessionId"])
}

func TestStartACPHandshake_ForkRequiresLoadDropsHistoryReplayedBeforeLoadReply(t *testing.T) {
	t.Parallel()

	peer := &handshakePeer{
		linesBefore: map[string][]string{
			MethodSessionLoad: {
				string(acptest.Chunk("source-1", contracts.ACPUpdateUserMessageChunk, "REPLAYED_LOAD_PROMPT")),
				string(acptest.Chunk("source-1", contracts.ACPUpdateAgentMessageChunk, "REPLAYED_LOAD_HISTORY")),
			},
			MethodSessionPrompt: {
				string(acptest.Chunk("forked-4", contracts.ACPUpdateAgentMessageChunk, "LIVE_AFTER_LOAD_FORK")),
			},
		},
	}
	cfg := SessionConfig{
		NewMethod:        MethodSessionNew,
		ResumeMethod:     MethodSessionLoad,
		ForkRequiresLoad: true,
	}
	var (
		rig              *handshakeRig
		cutoffBeforeFork = -1
	)
	hooks := Hooks{
		SessionParams: func(method string, _ map[string]any) {
			if method == MethodSessionFork {
				rig.base.sessionUpdates.mu.Lock()
				cutoffBeforeFork = rig.base.sessionUpdates.replayCutoff
				rig.base.sessionUpdates.mu.Unlock()
			}
		},
	}
	rig = newHandshakeRig(t, peer, hooks, nil, func(method string) json.RawMessage {
		switch method {
		case MethodInitialize:
			return json.RawMessage(`{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"fork":{}}}}`)
		case MethodSessionLoad:
			return json.RawMessage(`{"sessionId":"source-1"}`)
		case MethodSessionPrompt:
			return json.RawMessage(`{"stopReason":"end_turn"}`)
		default:
			return json.RawMessage(`{"sessionId":"forked-4"}`)
		}
	})

	session, err := rig.handshake(agent.Options{WorkingDir: "/work/tree", Session: agent.ForkSession("source-1", "")}, cfg)
	require.NoError(t, err)
	assert.Equal(t, -1, cutoffBeforeFork, "pre-fork session/load must not lock replayCutoff before session/fork completes")

	b, sink := rig.base, rig.sink
	b.finishSessionUpdates()

	assert.Equal(t, "forked-4", session.SessionID)
	assert.Empty(t, sink.Messages(), "history chunks replayed before session/load reply must be dropped")

	require.NoError(t, agenttest.SendInput(b, "NEXT", nil))
	testutil.RequireEventually(t, func() bool { return !b.PromptActive() })
	assert.Equal(t, []string{"text:LIVE_AFTER_LOAD_FORK"}, assembledTexts(t, sink.Messages()))
}

func TestStartACPHandshake_ForkRequiresLoadDropsHistoryReplayedDuringFork(t *testing.T) {
	t.Parallel()

	peer := &handshakePeer{
		linesBefore: map[string][]string{
			MethodSessionLoad: {
				string(acptest.Chunk("source-1", contracts.ACPUpdateUserMessageChunk, "REPLAYED_LOAD_PROMPT")),
				string(acptest.Chunk("source-1", contracts.ACPUpdateAgentMessageChunk, "REPLAYED_LOAD_HISTORY")),
			},
			MethodSessionFork: {
				string(acptest.Chunk("forked-5", contracts.ACPUpdateUserMessageChunk, "REPLAYED_FORK_PROMPT")),
				string(acptest.Chunk("forked-5", contracts.ACPUpdateAgentMessageChunk, "REPLAYED_FORK_HISTORY")),
			},
			MethodSessionPrompt: {
				string(acptest.Chunk("forked-5", contracts.ACPUpdateAgentMessageChunk, "LIVE_AFTER_LOAD_AND_FORK")),
			},
		},
	}
	cfg := SessionConfig{
		NewMethod:        MethodSessionNew,
		ResumeMethod:     MethodSessionLoad,
		ForkRequiresLoad: true,
	}
	b, sink, session := runHandshakeForTest(
		t,
		peer,
		Hooks{},
		agent.Options{WorkingDir: "/work/tree", Session: agent.ForkSession("source-1", "")},
		cfg,
		func(method string) json.RawMessage {
			switch method {
			case MethodInitialize:
				return json.RawMessage(`{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"fork":{}}}}`)
			case MethodSessionLoad:
				return json.RawMessage(`{"sessionId":"source-1"}`)
			case MethodSessionPrompt:
				return json.RawMessage(`{"stopReason":"end_turn"}`)
			default:
				return json.RawMessage(`{"sessionId":"forked-5"}`)
			}
		},
	)
	b.finishSessionUpdates()

	assert.Equal(t, "forked-5", session.SessionID)
	assert.Empty(t, sink.Messages(), "history replayed during both session/load and session/fork must be dropped")

	require.NoError(t, agenttest.SendInput(b, "NEXT", nil))
	testutil.RequireEventually(t, func() bool { return !b.PromptActive() })
	assert.Equal(t, []string{"text:LIVE_AFTER_LOAD_AND_FORK"}, assembledTexts(t, sink.Messages()))
}

func TestStartACPHandshake_ForkRequiresLoadUsesForkSourceWorkingDirForLoad(t *testing.T) {
	t.Parallel()

	for _, resumeMethod := range []string{MethodSessionResume, MethodSessionLoad} {
		t.Run(resumeMethod, func(t *testing.T) {
			t.Parallel()
			peer := &handshakePeer{}
			cfg := SessionConfig{
				NewMethod:        MethodSessionNew,
				ResumeMethod:     resumeMethod,
				ForkRequiresLoad: true,
			}
			b, _, session := runHandshakeForTest(
				t,
				peer,
				Hooks{},
				agent.Options{
					WorkingDir: "/work/tree",
					Session:    agent.ForkSession("source-1", "/source/repo"),
				},
				cfg,
				func(method string) json.RawMessage {
					switch method {
					case MethodInitialize:
						return json.RawMessage(`{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"fork":{}}}}`)
					case resumeMethod:
						return json.RawMessage(`{"sessionId":"source-1"}`)
					default:
						return json.RawMessage(`{"sessionId":"forked-6"}`)
					}
				},
			)
			b.finishSessionUpdates()

			assert.Equal(t, "forked-6", session.SessionID)
			requests := peer.recorded()
			require.Len(t, requests, 3)
			assert.Equal(t, resumeMethod, requests[1].Method)
			assert.Equal(t, "/source/repo", requests[1].Params["cwd"], "pre-fork load must use ForkSourceWorkingDir")
			assert.Equal(t, MethodSessionFork, requests[2].Method)
			assert.Equal(t, "/work/tree", requests[2].Params["cwd"], "session/fork must use target WorkingDir")
		})
	}
}

func TestStartACPHandshake_SupportsNativeForkIsFalseWhenNotAdvertised(t *testing.T) {
	t.Parallel()

	peer := &handshakePeer{}
	b, _, _ := runHandshakeForTest(
		t,
		peer,
		Hooks{},
		agent.Options{WorkingDir: "/work"},
		acpDefaultSessionConfig,
		func(method string) json.RawMessage {
			if method == MethodInitialize {
				return json.RawMessage(`{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"close":{}}}}`)
			}
			return json.RawMessage(`{"sessionId":"session-1"}`)
		},
	)
	assert.False(t, b.SupportsNativeFork())
}

func TestStartACPHandshake_ForkFailsWhenCapabilityNotAdvertised(t *testing.T) {
	t.Parallel()

	peer := &handshakePeer{}
	rig := newHandshakeRig(t, peer, Hooks{}, nil, func(method string) json.RawMessage {
		if method == MethodInitialize {
			return json.RawMessage(`{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{}}}`)
		}
		return json.RawMessage(`{"sessionId":"unexpected"}`)
	})

	session, err := rig.handshake(agent.Options{WorkingDir: "/work", Session: agent.ForkSession("source-1", "")}, acpDefaultSessionConfig)
	require.Nil(t, session)
	require.Error(t, err)
	assert.ErrorContains(t, err, "does not advertise ACP session/fork capability")
	assert.False(t, rig.base.SupportsNativeFork())

	requests := peer.recorded()
	require.Len(t, requests, 1, "must not send session/fork when server did not advertise fork capability")
	assert.Equal(t, MethodInitialize, requests[0].Method)
}

func TestStartACPHandshake_ForkRejectsSameOrEmptySessionID(t *testing.T) {
	t.Parallel()

	for name, reply := range map[string]string{
		"empty session id":  `{}`,
		"source session id": `{"sessionId":"source-1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			peer := &handshakePeer{}
			rig := newHandshakeRig(t, peer, Hooks{}, nil, func(method string) json.RawMessage {
				if method == MethodInitialize {
					return json.RawMessage(`{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"fork":{}}}}`)
				}
				return json.RawMessage(reply)
			})
			session, err := rig.handshake(agent.Options{WorkingDir: "/work", Session: agent.ForkSession("source-1", "")}, acpDefaultSessionConfig)
			require.Nil(t, session)
			require.Error(t, err)
		})
	}
}

func TestStartACPHandshake_RejectsInvalidSessionInit(t *testing.T) {
	t.Parallel()

	peer := &handshakePeer{}
	rig := newHandshakeRig(t, peer, Hooks{}, nil, func(string) json.RawMessage {
		return json.RawMessage(`{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"fork":{}}}}`)
	})
	session, err := rig.handshake(agent.Options{
		WorkingDir: "/work",
		Session: agent.SessionInit{
			Kind:             agent.SessionInitResume,
			SessionID:        "s1",
			SourceWorkingDir: "/source",
		},
	}, acpDefaultSessionConfig)
	require.Nil(t, session)
	require.Error(t, err)
	require.Len(t, peer.recorded(), 1)
}
