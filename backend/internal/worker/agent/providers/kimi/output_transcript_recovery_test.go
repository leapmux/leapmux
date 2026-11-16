package kimi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiConnectedRigCancelsBeforeJoiningRecoveryWorkers(t *testing.T) {
	t.Parallel()
	finished := make(chan struct{})
	t.Run("cleanup", func(t *testing.T) {
		rig := newKimiTestRig(t, agent.Options{})
		ctx := testutil.DeadlineContext(t)
		entered := make(chan struct{})
		rig.agent.transcriptRecoveryWork.Run("cleanup-probe", func() {
			close(entered)
			<-rig.agent.Context().Done()
			close(finished)
		})
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("the recovery worker did not start")
		}
		t.Cleanup(func() {
			assert.ErrorIs(t, rig.agent.Context().Err(), context.Canceled, "the rig must cancel its context before cleanup joins recovery workers")
			// Explicit cancellation also releases the worker when the assertion fails.
			rig.agent.CancelForTest()
		})
	})
	select {
	case <-finished:
	default:
		t.Fatal("the rig returned before its recovery worker finished")
	}
}

func TestKimiIncompleteMainResetRecoversBeforeApplyingLaterOperations(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		previousSeq int64
	}{
		{"increasing watermark", 1}, {"lowered watermark", 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			ctx := testutil.DeadlineContext(t)
			rig.transcriptOps(t, tc.previousSeq,
				`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`,
				`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"f1","role":"assistant","text":"old"}}`)
			var data any
			require.NoError(t, json.Unmarshal([]byte(`{"agent_id":"main","items":[{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"},"steps":[{"kind":"step","stepId":"t0.1","state":"running","frames":[{"kind":"text","frameId":"f1","role":"assistant","text":"restored"}]}]}],"has_more":false,"interactions":[],"seq":7}`), &data))
			entered, release := make(chan struct{}), make(chan struct{})
			releaseResponse := sync.OnceFunc(func() { close(release) })
			defer releaseResponse()
			rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer "+fakeKapToken, r.Header.Get("Authorization"))
				assert.Equal(t, "/api/v1/sessions/session_1/transcript", r.URL.Path)
				assert.Equal(t, "main", r.URL.Query().Get("agent_id"))
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return
				}
				writeFakeKapEnvelope(w, http.StatusOK, 0, "success", data)
			})
			rig.agent.HandleOutput([]byte(`{"type":"transcript.reset","session_id":"session_1","payload":{"type":"transcript.reset","agent_id":"main","seq":7,"has_more_older":true,"snapshot":{"items":[],"interactions":[],"hasMoreOlder":true}}}`))
			require.True(t, rig.agent.PublishTurnActive().Active, "an incomplete reset must not make a running turn idle")
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("the transcript read did not start")
			}
			rig.transcriptOps(t, 7, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"f1","role":"assistant","text":"duplicate"}}`)
			rig.transcriptOps(t, 8, `{"op":"append","target":{"type":"frame","turnId":"t0","stepId":"t0.1","frameId":"f1"},"offset":8,"text":"!"}`)
			rig.transcriptOps(t, 9, `{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","state":"completed"}}`)
			releaseResponse()
			waitFor(t, rig.agent.transcriptRecoveryWork.Idle, "the authoritative snapshot and queued operations finish")
			messages := rig.sink.Messages()
			require.Len(t, messages, 1)
			_, text := assembledRow(t, messages[0])
			assert.Equal(t, "restored!", text)
		})
	}
}

func TestKimiNativePendingOverflowReleasesSupersededFrames(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	for seq := int64(1); seq <= kimiMaxPendingEvents+1; seq++ {
		rig.transcriptAgentOps(t, "agent-0", seq)
	}
	run := rig.agent.run("agent-0")
	assert.True(t, run.pendingNativeRecovery)
	assert.Zero(t, len(run.pendingNative), "snapshot recovery supersedes the buffered frames")
}

func TestKimiNativePendingQueueAccountsForDecodedWireSize(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	// The decoder records the original byte count independently of the projected payload.
	frame := kimiFrame{Type: "transcript.ops", SessionID: "session_1", wireSize: 64 << 20}
	rig.agent.dispatchMu.Lock()
	held := rig.agent.holdNativeTranscriptFrame("agent-0", frame)
	rig.agent.dispatchMu.Unlock()
	require.True(t, held)
	run := rig.agent.run("agent-0")
	assert.True(t, run.pendingNativeRecovery, "one maximum-size frame must require snapshot recovery")
	assert.Empty(t, run.pendingNative)
}

func TestKimiNativePendingQueueCapsTheWholeSession(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	frame := kimiFrame{Type: "transcript.ops", SessionID: "session_1", wireSize: 4 << 20}
	rig.agent.dispatchMu.Lock()
	for child := range 6 {
		for range 3 {
			require.True(t, rig.agent.holdNativeTranscriptFrame(fmt.Sprintf("agent-%d", child), frame))
		}
	}
	rig.agent.dispatchMu.Unlock()
	assert.EqualValues(t, 60<<20+15*kimiPendingFrameOverhead, rig.agent.pendingNativeBytes)
	for child := range 5 {
		assert.Len(t, rig.agent.run(fmt.Sprintf("agent-%d", child)).pendingNative, 3)
	}
	last := rig.agent.run("agent-5")
	assert.True(t, last.pendingNativeRecovery)
	assert.Zero(t, len(last.pendingNative))
	rig.agent.dispatchMu.Lock()
	for child := range 5 {
		rig.agent.takePendingNativeFrames(rig.agent.run(fmt.Sprintf("agent-%d", child)))
	}
	rig.agent.dispatchMu.Unlock()
	assert.Zero(t, rig.agent.pendingNativeBytes)
}

func TestKimiNativeRecoveryDiscardsThePreviousSessionsResponse(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	ctx := testutil.DeadlineContext(t)
	entered, release := make(chan struct{}), make(chan struct{})
	releaseResponse := sync.OnceFunc(func() { close(release) })
	defer releaseResponse()
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return
		}
		writeFakeKapEnvelope(w, http.StatusOK, 0, "success", map[string]any{
			"agent_id": "main", "items": []any{}, "seq": 2,
		})
	})
	rig.agent.HandleOutput([]byte(`{"type":"transcript.reset","session_id":"session_1","payload":{"agent_id":"main","seq":2,"has_more_older":true,"snapshot":{"items":[],"hasMoreOlder":true}}}`))
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the transcript request did not start")
	}
	rig.agent.dispatchMu.Lock()
	rig.agent.Mu.Lock()
	rig.agent.sessionID = "session_2"
	rig.agent.runs = nil
	rig.agent.pendingNativeBytes = 0
	rig.agent.Mu.Unlock()
	replacement := rig.agent.run("main")
	rig.agent.dispatchMu.Unlock()
	releaseResponse()
	waitFor(t, rig.agent.transcriptRecoveryWork.Idle, "the previous session's recovery returns")
	assert.Same(t, replacement, rig.agent.run("main"))
	assert.Empty(t, replacement.transcript.turns)
	assert.False(t, replacement.transcript.hasSeq)
	assert.Empty(t, rig.sink.Messages())
}

func TestKimiNativeTranscriptRecoveryDoesNotHoldTheDispatcher(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"prompt":"Review"},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	releaseRequest := func() { once.Do(func() { close(release) }) }
	defer releaseRequest()
	ctx := testutil.DeadlineContext(t)
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		writeFakeKapEnvelope(w, http.StatusOK, 0, "success", map[string]any{"agent_id": "agent-0", "items": []any{}, "seq": 2})
	})
	rig.agent.run("agent-0").pendingNativeRecovery = true
	done := make(chan struct{})
	go func() {
		defer close(done)
		rig.agent.HandleOutput([]byte(`{"type":"transcript.ops","session_id":"session_1","payload":{"type":"transcript.ops","agent_id":"agent-0","seq":3,"ops":[]}}`))
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the transcript request did not start")
	}
	available := rig.agent.dispatchMu.TryLock()
	if available {
		rig.agent.dispatchMu.Unlock()
	}
	assert.True(t, available, "a held transcript response must not block other agents and controls")
	require.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active, "the child cannot admit input before its transcript is restored")
	assert.ErrorIs(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "another prompt"}), agent.ErrAgentBusy)
	releaseRequest()
	<-done
	waitFor(t, rig.agent.transcriptRecoveryWork.Idle, "the child recovery finishes")
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active, "an empty authoritative snapshot releases the recovery admission")
}
