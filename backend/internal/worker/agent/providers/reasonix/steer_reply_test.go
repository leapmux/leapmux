package reasonix

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

func writeReasonixEventsJSONL(t *testing.T, homeDir, sessionID string, messages []map[string]any) {
	t.Helper()
	sessionsDir := filepath.Join(homeDir, ".reasonix", "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))
	eventPath := filepath.Join(sessionsDir, sessionID+".events.jsonl")
	var out []byte
	parent := ""
	for i, msg := range messages {
		rawMsg, err := json.Marshal(msg)
		require.NoError(t, err)
		eventID := msg["id"].(string)
		if eventID == "" {
			eventID = "ev-" + string(rune('a'+i))
		}
		line, err := json.Marshal(map[string]any{
			"schema_version": 2,
			"type":           "message",
			"id":             eventID,
			"parent":         parent,
			"head":           "main",
			"msgs":           []json.RawMessage{rawMsg},
		})
		require.NoError(t, err)
		out = append(out, line...)
		out = append(out, '\n')
		parent = eventID
	}
	require.NoError(t, os.WriteFile(eventPath, out, 0o600))
}

func TestReasonixSteerReceiptAndJournalReconciliation(t *testing.T) {
	t.Parallel()
	homeDir := t.TempDir()
	const sessionID = "reasonix-session"
	writeReasonixEventsJSONL(t, homeDir, sessionID, []map[string]any{
		{"id": "m-1", "role": "user", "origin": "user", "content": "Run the release check."},
	})

	a, _ := acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		func(method string) agenttest.RPCReply {
			if method == reasonixSteerMethod {
				return agenttest.RPCReply{Result: json.RawMessage(`{"itemId":"item-steer-1","disposition":"steer_accepted"}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		},
	)
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest(sessionID)
	a.SetSteerMethodForTest(reasonixSteerMethod)
	a.SetPromptActiveForTest(true)
	a.storeQuery = agent.StoredSessionQuery{HomeDir: homeDir}

	sender, ok := any(a).(agent.SteerReceiptSender)
	require.True(t, ok, "Reasonix must implement SteerReceiptSender")

	receipt, err := sender.SteerInputWithReceipt(agent.SteerRequest{
		InputID: "queue-steer-1",
		Content: "Also include the changelog.",
	})
	require.NoError(t, err)
	assert.Equal(t, sessionID, receipt.SessionID)
	assert.Equal(t, "item-steer-1", receipt.InputID)

	// Simulate stored assistant rows captured via AssistantTextKey / AssistantTextPrepared.
	keyBefore := a.reasonixAssistantTextKey("reasonix:reply")
	require.NotEmpty(t, keyBefore)
	rawBefore, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, "Before steer.", agent.MessageCompletionComplete)
	require.NoError(t, err)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
		Original:       rawBefore,
		AgentSessionID: sessionID,
		IdempotencyKey: keyBefore,
	}, agent.SpanInfo{}))
	a.reasonixAssistantTextPrepared("reasonix:reply", keyBefore, "Before steer.")

	keyAfter := a.reasonixAssistantTextKey("reasonix:reply")
	require.NotEmpty(t, keyAfter)
	rawAfter, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, "After steer.", agent.MessageCompletionComplete)
	require.NoError(t, err)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
		Original:       rawAfter,
		AgentSessionID: sessionID,
		IdempotencyKey: keyAfter,
	}, agent.SpanInfo{}))
	a.reasonixAssistantTextPrepared("reasonix:reply", keyAfter, "After steer.")

	// Reasonix persists the committed pre-steer assistant, mid-turn steer, and post-steer assistant.
	writeReasonixEventsJSONL(t, homeDir, sessionID, []map[string]any{
		{"id": "m-1", "role": "user", "origin": "user", "content": "Run the release check."},
		{"id": "m-2", "role": "assistant", "content": "Before steer."},
		{"id": "m-3", "role": "user", "origin": "user", "content": reasonixMidTurnSteerPrefix + "\nAlso include the changelog.", "raw_content": "Also include the changelog."},
		{"id": "m-4", "role": "assistant", "content": "After steer."},
	})

	reconcile := a.captureReasonixSteerCompletion(agent.NewProviderServices(sink))
	require.NotNil(t, reconcile)
	require.NoError(t, reconcile())

	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	assert.Equal(t, sessionID, turns[0].SessionID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, turns[0].Outcome)
	assert.False(t, turns[0].ExclusiveItems)
	require.Len(t, turns[0].Inputs, 1)
	assert.Equal(t, "item-steer-1", turns[0].Inputs[0].InputID)
	require.Len(t, turns[0].Items, 1)
	assert.Equal(t, keyAfter, turns[0].Items[0].MessageKey)
	assert.Empty(t, sink.NativeReplyRetirements())
}

func TestReasonixUnappliedSteerRetiresAsUnproved(t *testing.T) {
	t.Parallel()
	homeDir := t.TempDir()
	const sessionID = "reasonix-session"
	writeReasonixEventsJSONL(t, homeDir, sessionID, []map[string]any{
		{"id": "m-1", "role": "user", "origin": "user", "content": "Run the release check."},
	})

	a, _ := acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		func(method string) agenttest.RPCReply {
			if method == reasonixSteerMethod {
				return agenttest.RPCReply{Result: json.RawMessage(`{"itemId":"item-unapplied","disposition":"steer_accepted"}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		},
	)
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest(sessionID)
	a.SetSteerMethodForTest(reasonixSteerMethod)
	a.SetPromptActiveForTest(true)
	a.storeQuery = agent.StoredSessionQuery{HomeDir: homeDir}

	sender := any(a).(agent.SteerReceiptSender)
	receipt, err := sender.SteerInputWithReceipt(agent.SteerRequest{
		InputID: "queue-steer-unapplied",
		Content: "Stopped before injection.",
	})
	require.NoError(t, err)

	// Reasonix recorded the unapplied steer as a local-only tool record.
	writeReasonixEventsJSONL(t, homeDir, sessionID, []map[string]any{
		{"id": "m-1", "role": "user", "origin": "user", "content": "Run the release check."},
		{"id": "m-2", "role": "tool", "local_only": true, "content": reasonixMidTurnSteerPrefix + "\nStopped before injection."},
	})

	reconcile := a.captureReasonixSteerCompletion(agent.NewProviderServices(sink))
	require.NotNil(t, reconcile)
	require.NoError(t, reconcile())

	assert.Empty(t, sink.NativeReplyTurns())
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
}

func TestReasonixSteerTracksWhenTranscriptDoesNotExistYet(t *testing.T) {
	t.Parallel()
	homeDir := t.TempDir()
	const sessionID = "brand-new-session"

	a, _ := acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		func(method string) agenttest.RPCReply {
			if method == reasonixSteerMethod {
				return agenttest.RPCReply{Result: json.RawMessage(`{"itemId":"item-first-turn","disposition":"steer_accepted"}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		},
	)
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest(sessionID)
	a.SetSteerMethodForTest(reasonixSteerMethod)
	a.SetPromptActiveForTest(true)
	a.storeQuery = agent.StoredSessionQuery{HomeDir: homeDir}

	receipt, err := a.SteerInputWithReceipt(agent.SteerRequest{
		InputID: "queue-steer-first",
		Content: "First turn steer.",
	})
	require.NoError(t, err)
	assert.Equal(t, agent.NativeInputReceipt{SessionID: sessionID, InputID: "item-first-turn"}, receipt)

	keyAfter := a.reasonixAssistantTextKey("reasonix:reply")
	require.NotEmpty(t, keyAfter)
	rawAfter, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, "First turn post-steer reply.", agent.MessageCompletionComplete)
	require.NoError(t, err)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
		Original:       rawAfter,
		AgentSessionID: sessionID,
		IdempotencyKey: keyAfter,
	}, agent.SpanInfo{}))
	a.reasonixAssistantTextPrepared("reasonix:reply", keyAfter, "First turn post-steer reply.")

	writeReasonixEventsJSONL(t, homeDir, sessionID, []map[string]any{
		{"id": "m-1", "role": "user", "origin": "user", "content": "Initial prompt."},
		{"id": "m-2", "role": "user", "origin": "user", "content": reasonixMidTurnSteerPrefix + "\nFirst turn steer.", "raw_content": "First turn steer."},
		{"id": "m-3", "role": "assistant", "content": "First turn post-steer reply."},
	})

	reconcile := a.captureReasonixSteerCompletion(agent.NewProviderServices(sink))
	require.NotNil(t, reconcile)
	require.NoError(t, reconcile())
	require.Len(t, sink.NativeReplyTurns(), 1)
	assert.Equal(t, keyAfter, sink.NativeReplyTurns()[0].Items[0].MessageKey)
}

func writeReasonixV4Frames(t *testing.T, homeDir, sessionID string, messages []map[string]any, externalLast bool) {
	t.Helper()
	sessionsRoot := filepath.Join(homeDir, ".reasonix", "sessions-v4")
	sessionDir := filepath.Join(sessionsRoot, sessionID)
	require.NoError(t, os.MkdirAll(sessionDir, 0o700))
	manifest, err := json.Marshal(map[string]any{
		"schemaVersion":   4,
		"codec":           "reasonix.session.linear/v4",
		"storageRevision": 3,
		"contentRoot":     "../.content-v1",
		"sessionId":       sessionID,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "manifest.json"), manifest, 0o600))

	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderCRC(true), zstd.WithEncoderConcurrency(1))
	require.NoError(t, err)
	defer func() { _ = encoder.Close() }()

	var frames []byte
	writeFrame := func(raw []byte) {
		compressed := encoder.EncodeAll(raw, nil)
		var hdr [12]byte
		copy(hdr[:4], []byte{'R', 'X', '4', 'F'})
		binary.BigEndian.PutUint32(hdr[4:8], uint32(len(compressed)))
		binary.BigEndian.PutUint32(hdr[8:12], uint32(len(raw)))
		frames = append(frames, hdr[:]...)
		frames = append(frames, compressed...)
	}

	if len(messages) > 0 {
		digest := sha256.New()
		begin, err := json.Marshal(map[string]any{
			"schemaVersion":    4,
			"codec":            "reasonix.session.linear/v4",
			"recordType":       "batch/begin",
			"commitId":         "commit-1",
			"operationId":      "op-1",
			"operationHash":    "hash-1",
			"firstSeq":         1,
			"eventCount":       len(messages),
			"writerGeneration": 1,
		})
		require.NoError(t, err)
		_, _ = digest.Write(begin)
		_, _ = digest.Write([]byte{0})
		writeFrame(begin)

		for i, msg := range messages {
			payload, err := json.Marshal(map[string]any{"message": msg})
			require.NoError(t, err)
			evMap := map[string]any{
				"id":   "ev-" + msg["id"].(string),
				"seq":  i + 1,
				"kind": "message/complete",
			}
			if externalLast && i == len(messages)-1 {
				sum := sha256.Sum256(payload)
				hexDigest := hex.EncodeToString(sum[:])
				objPath := filepath.Join(sessionsRoot, ".content-v1", "objects", hexDigest[:2], hexDigest[2:4], hexDigest)
				require.NoError(t, os.MkdirAll(filepath.Dir(objPath), 0o700))
				require.NoError(t, os.WriteFile(objPath, payload, 0o600))
				evMap["payloadRef"] = map[string]any{
					"digest": hexDigest,
					"bytes":  len(payload),
				}
			} else {
				evMap["payload"] = payload
			}
			evRec, err := json.Marshal(map[string]any{
				"schemaVersion": 4,
				"codec":         "reasonix.session.linear/v4",
				"recordType":    "batch/event",
				"event":         evMap,
			})
			require.NoError(t, err)
			_, _ = digest.Write(evRec)
			_, _ = digest.Write([]byte{0})
			writeFrame(evRec)
		}

		end, err := json.Marshal(map[string]any{
			"schemaVersion": 4,
			"codec":         "reasonix.session.linear/v4",
			"recordType":    "batch/end",
			"commitId":      "commit-1",
			"firstSeq":      1,
			"eventCount":    len(messages),
			"sha256":        hex.EncodeToString(digest.Sum(nil)),
		})
		require.NoError(t, err)
		writeFrame(end)
	}
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "events.frames"), frames, 0o600))
}

func TestReasonixV4FramedStoreSteerAndQueuedFollowupReconciliation(t *testing.T) {
	t.Parallel()
	homeDir := t.TempDir()
	const sessionID = "reasonix-v4-session"
	writeReasonixV4Frames(t, homeDir, sessionID, nil, false)

	callCount := 0
	a, _ := acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		func(method string) agenttest.RPCReply {
			if method == reasonixSteerMethod {
				callCount++
				if callCount == 1 {
					return agenttest.RPCReply{Result: json.RawMessage(`{"itemId":"item-v4-steer","disposition":"steer_accepted"}`)}
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{"itemId":"item-v4-followup","disposition":"queued_followup"}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		},
	)
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest(sessionID)
	a.SetSteerMethodForTest(reasonixSteerMethod)
	a.SetPromptActiveForTest(true)
	a.storeQuery = agent.StoredSessionQuery{HomeDir: homeDir}

	steerReceipt, err := a.SteerInputWithReceipt(agent.SteerRequest{
		InputID: "queue-v4-steer",
		Content: "Steer in v4.",
	})
	require.NoError(t, err)
	assert.Equal(t, agent.NativeInputReceipt{SessionID: sessionID, InputID: "item-v4-steer"}, steerReceipt)

	followupReceipt, err := a.SteerInputWithReceipt(agent.SteerRequest{
		InputID: "queue-v4-followup",
		Content: "Follow up in v4.",
	})
	require.NoError(t, err)
	assert.Equal(t, agent.NativeInputReceipt{SessionID: sessionID, InputID: "item-v4-followup"}, followupReceipt)

	persistAnswer := func(text string) string {
		key := a.reasonixAssistantTextKey("reasonix:reply")
		require.NotEmpty(t, key)
		raw, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, text, agent.MessageCompletionComplete)
		require.NoError(t, err)
		require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
			Original:       raw,
			AgentSessionID: sessionID,
			IdempotencyKey: key,
		}, agent.SpanInfo{}))
		a.reasonixAssistantTextPrepared("reasonix:reply", key, text)
		return key
	}

	keySteer := persistAnswer("Post-steer v4 reply.")
	keyFollowup := persistAnswer("Follow-up v4 reply.")

	writeReasonixV4Frames(t, homeDir, sessionID, []map[string]any{
		{"id": "m-1", "role": "user", "origin": "user", "content": "Initial v4 prompt."},
		{"id": "m-2", "role": "assistant", "content": "Before steer v4."},
		{"id": "m-3", "role": "user", "origin": "user", "content": reasonixMidTurnSteerPrefix + "\nSteer in v4.", "raw_content": "Steer in v4."},
		{"id": "m-4", "role": "assistant", "content": "Post-steer v4 reply."},
		{"id": "m-5", "role": "user", "origin": "user", "content": "Follow up in v4.", "raw_content": "Follow up in v4."},
		{"id": "m-6", "role": "assistant", "content": "Follow-up v4 reply."},
	}, true)

	reconcile := a.captureReasonixSteerCompletion(agent.NewProviderServices(sink))
	require.NotNil(t, reconcile)
	require.NoError(t, reconcile())

	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 2)
	assert.Equal(t, "item-v4-steer", turns[0].Inputs[0].InputID)
	assert.False(t, turns[0].ExclusiveItems)
	require.Len(t, turns[0].Items, 1)
	assert.Equal(t, keySteer, turns[0].Items[0].MessageKey)

	assert.Equal(t, "item-v4-followup", turns[1].Inputs[0].InputID)
	assert.True(t, turns[1].ExclusiveItems)
	require.Len(t, turns[1].Items, 1)
	assert.Equal(t, keyFollowup, turns[1].Items[0].MessageKey)
	assert.Empty(t, sink.NativeReplyRetirements())
}

func TestReasonixSteerCapturesFinalTextFlushedAfterCaptureReplyReconciliation(t *testing.T) {
	t.Parallel()
	homeDir := t.TempDir()
	const sessionID = "reasonix-capture-order"
	writeReasonixV4Frames(t, homeDir, sessionID, nil, false)

	a, _ := acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		func(method string) agenttest.RPCReply {
			if method == reasonixSteerMethod {
				return agenttest.RPCReply{Result: json.RawMessage(`{"itemId":"item-capture-order","disposition":"steer_accepted"}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		},
	)
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest(sessionID)
	a.SetSteerMethodForTest(reasonixSteerMethod)
	a.SetPromptActiveForTest(true)
	a.storeQuery = agent.StoredSessionQuery{HomeDir: homeDir}

	_, err := a.SteerInputWithReceipt(agent.SteerRequest{
		InputID: "queue-capture-order",
		Content: "Add the changelog summary.",
	})
	require.NoError(t, err)

	// ACP captures reply reconciliation in captureRootSnapshotLocked BEFORE
	// finishRootSnapshot persists the turn's final buffered assistant text.
	reconcile := a.captureReasonixSteerCompletion(agent.NewProviderServices(sink))
	require.NotNil(t, reconcile)

	keyAfter := a.reasonixAssistantTextKey("reasonix:reply")
	require.NotEmpty(t, keyAfter, "final buffered text flushed after capture must still get a tracked key")
	rawAfter, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, "Included the changelog summary.", agent.MessageCompletionComplete)
	require.NoError(t, err)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
		Original:       rawAfter,
		AgentSessionID: sessionID,
		IdempotencyKey: keyAfter,
	}, agent.SpanInfo{}))
	a.reasonixAssistantTextPrepared("reasonix:reply", keyAfter, "Included the changelog summary.")

	writeReasonixV4Frames(t, homeDir, sessionID, []map[string]any{
		{"id": "m-1", "role": "user", "origin": "user", "content": "Initial prompt."},
		{"id": "m-2", "role": "user", "origin": "user", "content": reasonixMidTurnSteerPrefix + "\nAdd the changelog summary.", "raw_content": "Add the changelog summary."},
		{"id": "m-3", "role": "assistant", "content": "Included the changelog summary."},
	}, false)

	require.NoError(t, reconcile())
	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	require.Len(t, turns[0].Items, 1)
	assert.Equal(t, keyAfter, turns[0].Items[0].MessageKey)
	assert.Empty(t, sink.NativeReplyRetirements())
}

func TestReasonixLegacyReplaceAppendEventsJSONLSteerReconciliation(t *testing.T) {
	t.Parallel()
	homeDir := t.TempDir()
	const sessionID = "reasonix-legacy-schema1"
	sessionsDir := filepath.Join(homeDir, ".reasonix", "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))
	eventPath := filepath.Join(sessionsDir, sessionID+".events.jsonl")

	writeLines := func(records []map[string]any) {
		var out []byte
		for _, rec := range records {
			line, err := json.Marshal(rec)
			require.NoError(t, err)
			out = append(out, line...)
			out = append(out, '\n')
		}
		require.NoError(t, os.WriteFile(eventPath, out, 0o600))
	}

	writeLines([]map[string]any{
		{
			"schema_version": 1,
			"type":           "replace",
			"messages": []map[string]any{
				{"role": "system", "content": "You are Reasonix."},
				{"role": "user", "content": "Run the release check."},
			},
		},
	})

	a, _ := acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		func(method string) agenttest.RPCReply {
			if method == reasonixSteerMethod {
				return agenttest.RPCReply{Result: json.RawMessage(`{"itemId":"item-schema1","disposition":"steer_accepted"}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		},
	)
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest(sessionID)
	a.SetSteerMethodForTest(reasonixSteerMethod)
	a.SetPromptActiveForTest(true)
	a.storeQuery = agent.StoredSessionQuery{HomeDir: homeDir}

	_, err := a.SteerInputWithReceipt(agent.SteerRequest{
		InputID: "queue-schema1",
		Content: "Also include the changelog.",
	})
	require.NoError(t, err)

	reconcile := a.captureReasonixSteerCompletion(agent.NewProviderServices(sink))
	require.NotNil(t, reconcile)

	keyBefore := a.reasonixAssistantTextKey("reasonix:reply")
	rawBefore, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, "Before steer.", agent.MessageCompletionComplete)
	require.NoError(t, err)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
		Original:       rawBefore,
		AgentSessionID: sessionID,
		IdempotencyKey: keyBefore,
	}, agent.SpanInfo{}))
	a.reasonixAssistantTextPrepared("reasonix:reply", keyBefore, "Before steer.")

	keyAfter := a.reasonixAssistantTextKey("reasonix:reply")
	rawAfter, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, "After steer.", agent.MessageCompletionComplete)
	require.NoError(t, err)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{
		Original:       rawAfter,
		AgentSessionID: sessionID,
		IdempotencyKey: keyAfter,
	}, agent.SpanInfo{}))
	a.reasonixAssistantTextPrepared("reasonix:reply", keyAfter, "After steer.")

	writeLines([]map[string]any{
		{
			"schema_version": 1,
			"type":           "replace",
			"messages": []map[string]any{
				{"role": "system", "content": "You are Reasonix."},
				{"role": "user", "content": "Run the release check."},
			},
		},
		{
			"schema_version": 4,
			"type":           "append",
			"message_index":  2,
			"messages": []map[string]any{
				{"role": "assistant", "content": "Before steer."},
				{"role": "user", "content": reasonixMidTurnSteerPrefix + "\nAlso include the changelog.", "raw_content": "Also include the changelog."},
				{"role": "assistant", "content": "After steer."},
			},
		},
	})

	require.NoError(t, reconcile())
	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	require.Len(t, turns[0].Items, 1)
	assert.Equal(t, keyAfter, turns[0].Items[0].MessageKey)
	assert.Empty(t, sink.NativeReplyRetirements())
}

func TestExtractReasonixSteerTextStripsLeadingWorkspaceAndLanguageBlocks(t *testing.T) {
	t.Parallel()
	content := "<workspace>\nCurrent workspace: \"/tmp/ws\".\n</workspace>\n\n" +
		reasonixMidTurnSteerPrefix + "\nAlso include the changelog."
	got, ok := extractReasonixSteerText(content, "Also include the changelog.")
	require.True(t, ok)
	assert.Equal(t, "Also include the changelog.", got)

	_, ok = extractReasonixSteerText("hey model "+reasonixMidTurnSteerPrefix+"\nnot a steer", "")
	assert.False(t, ok)
}
