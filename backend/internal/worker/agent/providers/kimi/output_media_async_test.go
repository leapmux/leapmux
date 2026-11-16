package kimi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/terminal"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiNativeMediaDoesNotBlockControlsOrTurnProcessing(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.agent.SetAPITimeoutForTest(time.Minute)
	data := []byte("native image bytes")
	digest := sha256.Sum256(append([]byte("image/png\x00"), data...))
	fileID := "f_" + hex.EncodeToString(digest[:])
	entered, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, request *http.Request) {
		close(entered)
		select {
		case <-released:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(data)
		case <-request.Context().Done():
		}
	})
	frame := fmt.Sprintf(`{"type":"transcript.ops","session_id":"session_1","payload":{"type":"transcript.ops","agent_id":"main","seq":1,"ops":[{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"image","toolCallId":"image","name":"ReadMediaFile","state":"done","input":{},"output":[{"type":"image","source":{"kind":"session_media","file_id":%q}}]}}]}}`, fileID)
	handled := make(chan struct{})
	go func() { rig.agent.HandleOutput([]byte(frame)); close(handled) }()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the native media request did not reach its handler")
	}
	select {
	case <-handled:
	case <-ctx.Done():
		t.Fatal("the media download blocked native event dispatch")
	}
	rig.agent.HandleOutput([]byte(`{"type":"interaction","session_id":"session_1","agent_id":"main","interaction_id":"question_1","kind":"question","status":"pending","tool_call_id":"call_1","event_created_at":"2026-11-15T00:00:00Z","request":{"questions":[{"id":"q_0","question":"Continue?","options":[{"id":"yes","label":"Yes"}]}]}}`))
	assert.Equal(t, 1, rig.sink.PublishedControlCount())
	rig.transcriptOps(t, 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"completed","origin":{"kind":"user"}}}`)
	assert.Empty(t, rig.sink.Messages(), "later transcript rows wait for the reserved complete result")
	release()
	waitFor(t, func() bool { return len(rig.sink.Messages()) == 2 }, "the completed media result and turn divider persist")
	rows := rig.sink.Messages()
	require.Len(t, rows, 2)
	assert.Contains(t, string(rows[0].Content), `"frame.upsert"`)
	assert.NotEmpty(t, rows[0].SupplementalContent)
	assert.Contains(t, string(rows[1].Content), `"turn.upsert"`)
}

func TestKimiRunningChildResetCapturesMediaInItsActiveScope(t *testing.T) {
	t.Parallel()
	for _, liveStart := range []bool{false, true} {
		t.Run(fmt.Sprintf("preceding_live_start=%t", liveStart), func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			fileID, data := kimiMediaTestFile("child reset image")
			entered, released := make(chan string, 1), make(chan struct{})
			var requests atomic.Int64
			release := sync.OnceFunc(func() { close(released) })
			defer release()
			rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, request *http.Request) {
				if requests.Add(1) == 1 {
					entered <- request.URL.Path
				}
				select {
				case <-released:
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(data)
				case <-request.Context().Done():
				}
			})
			rig.transcriptOps(t, 1,
				`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`,
				`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"description":"Image reader","prompt":"Inspect the image."},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
			if liveStart {
				rig.transcriptAgentOps(t, "agent-0", 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"other"},"prompt":"Inspect the image."}}`)
			}
			turn := map[string]any{"kind": "turn", "turnId": "t0", "ordinal": 0, "state": "running",
				"origin": map[string]any{"kind": "other"}, "prompt": "Inspect the image.",
				"steps": []any{map[string]any{"kind": "step", "stepId": "t0.1", "turnId": "t0", "ordinal": 1, "state": "running",
					"frames": []any{map[string]any{"kind": "tool", "frameId": "image", "toolCallId": "image", "name": "ReadMediaFile", "state": "done", "input": map[string]any{"path": "image.png"},
						"output": []any{map[string]any{"type": "image", "source": map[string]any{"kind": "session_media", "file_id": fileID}}}}}}}}
			frame := nativeKimiRecoveryReset(t, rig.sessionID(), "agent-0", 2, turn)
			handled := make(chan struct{})
			go func() { rig.agent.HandleOutput(frame); close(handled) }()
			assert.Contains(t, awaitKimiMediaRequest(t, entered), fileID)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			select {
			case <-handled:
			case <-ctx.Done():
				t.Fatal("the held image read blocked the child reset")
			}
			assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
			_, child := childOf(t, rig.sink, "session_1/agent-0")
			require.Equal(t, 1, child.MessageCount(), "the child prompt is visible while the image read waits")
			release()
			waitFor(t, func() bool { return child.MessageCount() == 2 }, "the child image result is retained")
			result := child.Messages()[1]
			assert.Contains(t, string(result.Content), "ReadMediaFile")
			assert.NotEmpty(t, result.SupplementalContent)
			assert.False(t, result.TranscriptOnly, "the restored running turn owns its live image result")
			assert.EqualValues(t, 1, requests.Load())
		})
	}
}

func kimiMediaTestFile(text string) (string, []byte) {
	data := []byte(text)
	digest := sha256.Sum256(append([]byte("image/png\x00"), data...))
	return "f_" + hex.EncodeToString(digest[:]), data
}

func kimiMediaTestOperation(t *testing.T, callID string, ids ...string) string {
	t.Helper()
	parts := make([]map[string]any, len(ids))
	for index, id := range ids {
		parts[index] = map[string]any{"type": "image", "source": map[string]string{"kind": "session_media", "file_id": id}}
	}
	op, err := json.Marshal(map[string]any{"op": "frame.upsert", "turnId": "t0", "stepId": "t0.1", "frame": map[string]any{
		"kind": "tool", "frameId": callID, "toolCallId": callID, "name": "ReadMediaFile", "state": "done", "input": map[string]any{}, "output": parts,
	}})
	require.NoError(t, err)
	return string(op)
}

func awaitKimiMediaRequest(t *testing.T, entered <-chan string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	select {
	case id := <-entered:
		return id
	case <-ctx.Done():
		t.Fatal("the media request did not reach its handler")
		return ""
	}
}

func awaitKimiMediaPreparation(t *testing.T, a *Agent, callID string) {
	t.Helper()
	service := a.mediaService()
	service.mu.Lock()
	var prepared context.Context
	for job := range service.jobs {
		if job.media.identity.toolCallID == callID {
			prepared = job.ctx
			break
		}
	}
	service.mu.Unlock()
	require.NotNil(t, prepared)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	select {
	case <-prepared.Done():
	case <-ctx.Done():
		t.Fatal("the media worker did not finish preparation")
	}
}

func TestKimiNativeMediaPublishesResultsInAdmissionOrder(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	firstID, first := kimiMediaTestFile("first image")
	lastID, last := kimiMediaTestFile("last image")
	entered := make(chan string, 2)
	firstRelease, lastRelease := make(chan struct{}), make(chan struct{})
	releaseFirst := sync.OnceFunc(func() { close(firstRelease) })
	releaseLast := sync.OnceFunc(func() { close(lastRelease) })
	defer releaseFirst()
	defer releaseLast()
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[len("/api/v1/sessions/session_1/media/"):]
		entered <- id
		data, released := first, firstRelease
		if id == lastID {
			data, released = last, lastRelease
		}
		select {
		case <-released:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(data)
		case <-r.Context().Done():
		}
	})
	rig.transcriptOps(t, 1, kimiMediaTestOperation(t, "first", firstID))
	assert.Equal(t, firstID, awaitKimiMediaRequest(t, entered))
	rig.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"middle","toolCallId":"middle","name":"Bash","state":"done","input":{},"output":"middle result"}}`)
	rig.transcriptOps(t, 3, kimiMediaTestOperation(t, "last", lastID))
	assert.Equal(t, lastID, awaitKimiMediaRequest(t, entered))
	releaseLast()
	awaitKimiMediaPreparation(t, rig.agent, "last")
	assert.Empty(t, rig.sink.Messages())
	releaseFirst()
	waitFor(t, func() bool { return len(rig.sink.Messages()) == 3 }, "all reserved results persist")
	for index, id := range []string{"first", "middle", "last"} {
		row, ok := readKimiTranscriptRecord(rig.sink.Messages()[index].Content)
		require.True(t, ok)
		var frame kimiTranscriptToolFrame
		require.NoError(t, json.Unmarshal(row.operation.Frame, &frame))
		assert.Equal(t, id, frame.FrameID)
	}
}

func TestKimiNativeMediaReplayReservesOneResult(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	id, data := kimiMediaTestFile("one image")
	entered, released := make(chan string, 4), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	var requests atomic.Int32
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		entered <- id
		select {
		case <-released:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(data)
		case <-r.Context().Done():
		}
	})
	op := kimiMediaTestOperation(t, "image", id, id)
	rig.transcriptOps(t, 1, op)
	awaitKimiMediaRequest(t, entered)
	rig.transcriptOps(t, 2, op)
	var parsed kimiTranscriptOperation
	require.NoError(t, json.Unmarshal([]byte(op), &parsed))
	rig.transcriptReset(t, 3, fmt.Sprintf(`{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"},"steps":[{"kind":"step","stepId":"t0.1","state":"running","frames":[%s]}]}`, parsed.Frame))
	release()
	waitFor(t, func() bool { return len(rig.sink.Messages()) == 1 }, "the retained result persists once")
	assert.EqualValues(t, 1, requests.Load())
	assert.NotEmpty(t, rig.sink.Messages()[0].SupplementalContent)
}

func TestKimiNativeMediaSessionReplacementKeepsTheOriginalDestination(t *testing.T) {
	t.Parallel()
	for _, finalSession := range []string{"session_2", "session_1"} {
		t.Run(finalSession, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.sink.UpdateSessionID("session_1")
			id, data := kimiMediaTestFile("session image")
			entered, released := make(chan string, 4), make(chan struct{})
			release := sync.OnceFunc(func() { close(released) })
			defer release()
			rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				entered <- r.URL.Path
				select {
				case <-released:
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(data)
				case <-r.Context().Done():
				}
			})
			op := kimiMediaTestOperation(t, "same-call", id)
			rig.transcriptOps(t, 1, op)
			awaitKimiMediaRequest(t, entered)
			rig.agent.dispatchMu.Lock()
			rig.agent.Mu.Lock()
			rig.agent.runs = nil
			rig.agent.Mu.Unlock()
			rig.agent.adoptSessionLocked("session_2", nil)
			if finalSession == "session_1" {
				rig.agent.adoptSessionLocked("session_1", nil)
			}
			rig.agent.dispatchMu.Unlock()
			waitFor(t, func() bool { return len(rig.sink.Messages()) == 1 }, "the accepted old closing result persists")
			old := rig.sink.Messages()[0]
			assert.Equal(t, "session_1", old.AgentSessionID)
			assert.Empty(t, old.SupplementalContent)
			release()
			rig.transcriptOps(t, 1, op)
			waitFor(t, func() bool { return len(rig.sink.Messages()) == 2 }, "the replacement run retains its own media")
			current := rig.sink.Messages()[1]
			assert.Equal(t, finalSession, current.AgentSessionID)
			assert.NotEmpty(t, current.SupplementalContent)
		})
	}
}

func TestKimiNativeMediaShutdownCompletesEveryReservation(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	id, _ := kimiMediaTestFile("shutdown image")
	entered := make(chan string, 8)
	rig.agent.api = newKimiTestClient(t, func(_ http.ResponseWriter, r *http.Request) { entered <- id; <-r.Context().Done() })
	for seq := int64(1); seq <= 6; seq++ {
		rig.transcriptOps(t, seq, kimiMediaTestOperation(t, fmt.Sprint(seq), id))
	}
	awaitKimiMediaRequest(t, entered)
	rig.agent.closeMedia()
	require.Len(t, rig.sink.Messages(), 6)
	for _, row := range rig.sink.Messages() {
		assert.Empty(t, row.SupplementalContent)
	}
}

func TestKimiNativeMediaBudgetChargesEncodedBytesAcrossFiles(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		previous, next, maximum int
		accepted                bool
	}{
		{0, 1, 4, true}, {0, 2, 4, true}, {0, 3, 4, true}, {0, 4, 4, false},
		{1, 2, 0, true}, {2, 3, 0, true}, {3, 4, 3, false}, {3, 4, 4, true},
	} {
		budget := kimiMediaBudget{remaining: test.maximum}
		assert.Equal(t, test.accepted, budget.charge(test.previous, test.next))
	}
	budget := kimiMediaBudget{remaining: 64}
	var accepted atomic.Int32
	var workers sync.WaitGroup
	for range 64 {
		workers.Go(func() {
			if budget.charge(0, 1) {
				accepted.Add(1)
			}
		})
	}
	workers.Wait()
	assert.EqualValues(t, 16, accepted.Load())
	assert.Zero(t, budget.remaining)
}

func TestKimiNativeMediaQueueRefusesExcessJobsWithoutLosingResults(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.agent.SetAPITimeoutForTest(time.Minute)
	id, data := kimiMediaTestFile("queued image")
	released := make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	var requests atomic.Int32
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case <-released:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(data)
		case <-r.Context().Done():
		}
	})
	for seq := int64(1); seq <= 33; seq++ {
		rig.transcriptOps(t, seq, kimiMediaTestOperation(t, fmt.Sprint(seq), id))
	}
	release()
	waitFor(t, func() bool { return len(rig.sink.Messages()) == 33 }, "all accepted original results persist")
	assert.EqualValues(t, 32, requests.Load())
	assert.Empty(t, rig.sink.Messages()[32].SupplementalContent)
	rig.transcriptOps(t, 34, kimiMediaTestOperation(t, "after-drain", id))
	waitFor(t, func() bool { return len(rig.sink.Messages()) == 34 }, "media admission resumes after draining")
	assert.NotEmpty(t, rig.sink.Messages()[33].SupplementalContent)
}

func TestKimiNativeMediaPreparedResultsKeepTheirAdmissionSlots(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.agent.SetAPITimeoutForTest(time.Minute)
	heldID, held := kimiMediaTestFile("held first image")
	fastID, fast := kimiMediaTestFile("fast later image")
	entered, released := make(chan string, 64), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	var requests atomic.Int32
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		data := fast
		if strings.HasSuffix(r.URL.Path, heldID) {
			entered <- heldID
			select {
			case <-released:
				data = held
			case <-r.Context().Done():
				return
			}
		} else {
			entered <- fastID
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	})
	rig.transcriptOps(t, 1, kimiMediaTestOperation(t, "first", heldID))
	for seq := int64(2); seq <= 32; seq++ {
		rig.transcriptOps(t, seq, kimiMediaTestOperation(t, fmt.Sprint(seq), fastID))
	}
	// Four job workers cannot start all these requests without preparing earlier results.
	for range 32 {
		awaitKimiMediaRequest(t, entered)
	}
	for seq := int64(33); seq <= 64; seq++ {
		rig.transcriptOps(t, seq, kimiMediaTestOperation(t, fmt.Sprint(seq), fastID))
	}
	release()
	waitFor(t, func() bool { return len(rig.sink.Messages()) == 64 }, "every admitted original result persists")
	assert.EqualValues(t, 32, requests.Load(), "prepared rows behind the first reservation still consume admission slots")
}

func TestKimiNativeMediaPreparedSupplementsConsumeRetentionBytes(t *testing.T) {
	// Keep this large-memory case serial with the other package tests.
	rig := newKimiOutputRig(t)
	rig.agent.SetAPITimeoutForTest(time.Minute)
	heldID, held := kimiMediaTestFile("held image")
	largeID, large := kimiMediaTestFile(strings.Repeat("x", 8<<20))
	barrierID, barrier := kimiMediaTestFile("barrier image")
	firstEntered, barrierEntered := make(chan string, 1), make(chan string, 3)
	released := make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		data := large
		if strings.HasSuffix(r.URL.Path, heldID) || strings.HasSuffix(r.URL.Path, barrierID) {
			if strings.HasSuffix(r.URL.Path, heldID) {
				firstEntered <- heldID
				data = held
			} else {
				barrierEntered <- barrierID
				data = barrier
			}
			select {
			case <-released:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	})
	rig.transcriptOps(t, 1, kimiMediaTestOperation(t, "first", heldID))
	awaitKimiMediaRequest(t, firstEntered)
	for seq := int64(2); seq <= 8; seq++ {
		rig.transcriptOps(t, seq, kimiMediaTestOperation(t, fmt.Sprint(seq), largeID))
	}
	for seq := int64(9); seq <= 11; seq++ {
		rig.transcriptOps(t, seq, kimiMediaTestOperation(t, fmt.Sprint(seq), barrierID))
	}
	// The first worker stays held. All three other workers reach these barriers
	// only after every earlier large result completed preparation.
	for range 3 {
		awaitKimiMediaRequest(t, barrierEntered)
	}
	release()
	waitFor(t, func() bool { return len(rig.sink.Messages()) == 11 }, "all queued original results persist")
	total, withImages := 0, 0
	for _, row := range rig.sink.Messages() {
		total += len(row.Content) + len(row.SupplementalContent)
		if strings.Contains(string(row.Content), largeID) && len(row.SupplementalContent) > 0 {
			withImages++
		}
	}
	assert.Greater(t, withImages, 0)
	assert.Less(t, withImages, 7, "optional supplements cannot exceed the aggregate retention allowance")
	assert.LessOrEqual(t, total, kimiMediaMaxQueuedBytes)
}

func TestKimiNativeMediaQueueCapsRetainedOriginalBytes(t *testing.T) {
	// Large native originals exercise the aggregate queue limit before its job-count limit.
	rig := newKimiOutputRig(t)
	rig.agent.SetAPITimeoutForTest(time.Minute)
	id, data := kimiMediaTestFile("large-record image")
	released := make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	var requests atomic.Int32
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case <-released:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(data)
		case <-r.Context().Done():
		}
	})
	text := strings.Repeat("x", 2<<20)
	for seq := int64(1); seq <= 32; seq++ {
		var op map[string]any
		require.NoError(t, json.Unmarshal([]byte(kimiMediaTestOperation(t, fmt.Sprint(seq), id)), &op))
		frame := op["frame"].(map[string]any)
		frame["output"] = append(frame["output"].([]any), map[string]any{"type": "text", "text": text})
		raw, err := json.Marshal(op)
		require.NoError(t, err)
		rig.transcriptOps(t, seq, string(raw))
	}
	release()
	waitFor(t, func() bool { return len(rig.sink.Messages()) == 32 }, "large original results all persist")
	assert.Less(t, requests.Load(), int32(32), "the byte cap refuses media before the job cap")
	assert.Empty(t, rig.sink.Messages()[31].SupplementalContent)
}

func TestKimiNativeMediaRejectsAnAggregateEncodedOverflow(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	firstID, first := kimiMediaTestFile("abc")
	secondID, second := kimiMediaTestFile("defg")
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		data := first
		if strings.HasSuffix(r.URL.Path, secondID) {
			data = second
		}
		_, _ = w.Write(data)
	})
	media := &kimiTranscriptMedia{ids: []string{firstID, secondID}, maximum: 10,
		identity: kimiTranscriptToolIdentity{sessionID: "session_1", agentID: "main", turnID: "t0", stepID: "t0.1", frameID: "image", toolCallID: "image"}}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	supplement, err := rig.agent.readTranscriptMedia(ctx, media, make(chan struct{}, kimiMediaConcurrency))
	require.ErrorContains(t, err, "remaining message size limit")
	assert.Empty(t, supplement)
}

func TestKimiNativeMediaCancelsAHeldSiblingAfterFailure(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	badID, _ := kimiMediaTestFile("bad MIME")
	heldID, _ := kimiMediaTestFile("held sibling")
	held, canceled := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, badID) {
			select {
			case <-held:
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write([]byte("refused media"))
			case <-ctx.Done():
			}
			return
		}
		close(held)
		<-r.Context().Done()
		close(canceled)
	})
	media := &kimiTranscriptMedia{ids: []string{badID, heldID}, maximum: 4096,
		identity: kimiTranscriptToolIdentity{sessionID: "session_1", agentID: "main", turnID: "t0", stepID: "t0.1", frameID: "image", toolCallID: "image"}}
	_, err := rig.agent.readTranscriptMedia(ctx, media, make(chan struct{}, kimiMediaConcurrency))
	require.ErrorContains(t, err, "MIME")
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("the failed media read did not cancel its sibling")
	}
}

type kimiMediaReservationSink struct {
	*agenttest.ControlSink
	refuse atomic.Bool
}

func (sink *kimiMediaReservationSink) ReserveTranscriptOutput(queue *agent.OutputQueue, writes ...agent.OutputWrite) ([]*agent.OutputTicket, error) {
	if sink.refuse.Load() {
		return nil, errors.New("the test refuses this media reservation")
	}
	return sink.ControlSink.ReserveTranscriptOutput(queue, writes...)
}

func TestKimiNativeMediaReservationFailureLeavesTheResultRetryable(t *testing.T) {
	t.Parallel()
	base := &agenttest.ControlSink{}
	sink := &kimiMediaReservationSink{ControlSink: base}
	sink.refuse.Store(true)
	rig := &kimiOutputRig{agent: newOfflineKimiAgent(t, sink), sink: base}
	id, data := kimiMediaTestFile("reservation image")
	var requests atomic.Int32
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	})
	op := kimiMediaTestOperation(t, "image", id)
	rig.transcriptOps(t, 1, op)
	assert.Empty(t, base.Messages())
	assert.Zero(t, requests.Load())
	assert.False(t, rig.agent.run("main").transcript.turns["t0"].steps["t0.1"].tools["image"].finished)
	sink.refuse.Store(false)
	rig.transcriptOps(t, 2, op)
	waitFor(t, func() bool { return len(base.Messages()) == 1 }, "the replay retains the previously refused result")
	assert.EqualValues(t, 1, requests.Load())
}

func TestKimiNativeMediaPersistenceRetryKeepsThePreparedSupplement(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.sink.PersistErr = errors.New("the first media storage attempt fails")
	id, data := kimiMediaTestFile("retry image")
	var requests atomic.Int32
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	})
	rig.transcriptOps(t, 1, kimiMediaTestOperation(t, "image", id))
	awaitKimiMediaPreparation(t, rig.agent, "image")
	failed := rig.sink.Messages()
	require.Len(t, failed, 1)
	assert.False(t, failed[0].Stored())
	service := rig.agent.mediaService()
	service.mu.Lock()
	assert.Len(t, service.jobs, 1)
	assert.Greater(t, service.retainedBytes, len(failed[0].Content))
	service.mu.Unlock()
	// Joining download workers must not require a successful storage retry.
	rig.agent.closeMedia()
	rig.sink.PersistErr = nil
	require.NoError(t, agent.FlushPendingOutput(rig.agent.sink))
	rows := rig.sink.Messages()
	require.Len(t, rows, 2)
	assert.True(t, rows[1].Stored())
	assert.Equal(t, failed[0].Content, rows[1].Content)
	assert.Equal(t, failed[0].SupplementalContent, rows[1].SupplementalContent)
	assert.EqualValues(t, 1, requests.Load())
	service.mu.Lock()
	assert.Empty(t, service.jobs)
	assert.Zero(t, service.retainedBytes)
	service.mu.Unlock()
}

func TestKimiNativeMediaSharesTheDownloadLimitAcrossMainAndChild(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.agent.SetAPITimeoutForTest(time.Minute)
	rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"parent","stepId":"parent.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"prompt":"Read images"},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	files := make(map[string][]byte)
	ids := make([]string, 12)
	for index := range ids {
		id, data := kimiMediaTestFile(fmt.Sprintf("concurrent image %d", index))
		ids[index] = id
		files[id] = data
	}
	entered, released := make(chan string, 16), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	var active, peak, requests atomic.Int32
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		id := parts[len(parts)-1]
		count := active.Add(1)
		defer active.Add(-1)
		for {
			previous := peak.Load()
			if count <= previous || peak.CompareAndSwap(previous, count) {
				break
			}
		}
		requests.Add(1)
		entered <- id
		select {
		case <-released:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(files[id])
		case <-r.Context().Done():
		}
	})
	rig.transcriptOps(t, 2, kimiMediaTestOperation(t, "main-images", ids[:6]...))
	rig.transcriptAgentOps(t, "agent-0", 1, kimiMediaTestOperation(t, "child-images", ids[6:]...))
	for range 4 {
		awaitKimiMediaRequest(t, entered)
	}
	assert.EqualValues(t, 4, active.Load())
	select {
	case id := <-entered:
		t.Fatalf("a fifth media request entered while four replies remained held: %s", id)
	default:
	}
	release()
	waitFor(t, func() bool {
		service := rig.agent.mediaService()
		service.mu.Lock()
		defer service.mu.Unlock()
		return len(service.jobs) == 0
	}, "main and child media jobs finish")
	assert.EqualValues(t, 12, requests.Load())
	assert.EqualValues(t, 4, peak.Load())
}

func TestKimiNativeMediaKeepsItsCapturedChildAfterCleanup(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"parent","stepId":"parent.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"prompt":"Read images"},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	_, child := childOf(t, rig.sink, "session_1/agent-0")
	initial := len(child.Messages())
	id, data := kimiMediaTestFile("child image")
	entered, released := make(chan string, 1), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		entered <- id
		select {
		case <-released:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(data)
		case <-r.Context().Done():
		}
	})
	rig.transcriptAgentOps(t, "agent-0", 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`, kimiMediaTestOperation(t, "image", id))
	awaitKimiMediaRequest(t, entered)
	rig.transcriptAgentOps(t, "agent-0", 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"completed","origin":{"kind":"user"}}}`)
	linked, present := rig.agent.children.get("agent-0")
	require.True(t, present)
	assert.True(t, linked.ended)
	release()
	waitFor(t, func() bool { return len(child.Messages()) == initial+1 }, "the captured child result survives cleanup")
	row := child.Messages()[initial]
	assert.Contains(t, string(row.Content), id)
	assert.NotEmpty(t, row.SupplementalContent)
	for _, parentRow := range rig.sink.Messages() {
		assert.NotContains(t, string(parentRow.Content), id)
	}
}

func TestKimiNativeMediaBodyFailureRetainsTheOriginalResult(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	id, data := kimiMediaTestFile("incomplete response body")
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", fmt.Sprint(len(data)+100))
		_, _ = w.Write(data)
	})
	rig.transcriptOps(t, 1, kimiMediaTestOperation(t, "image", id))
	waitFor(t, func() bool { return len(rig.sink.Messages()) == 1 }, "the incomplete media response retains its native closing row")
	row := rig.sink.Messages()[0]
	assert.Contains(t, string(row.Content), id)
	assert.Empty(t, row.SupplementalContent)
}

func TestKimiNativeMediaRefusesNewDownloadsAfterStopIntent(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	id, data := kimiMediaTestFile("stop-window image")
	var requests atomic.Int32
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	})
	rig.agent.NoteIntentionalStop()
	require.False(t, rig.agent.IsStopped(), "the process remains live during graceful shutdown")
	rig.transcriptOps(t, 1, kimiMediaTestOperation(t, "during-stop", id))
	waitFor(t, func() bool { return len(rig.sink.Messages()) == 1 }, "the accepted original closing result persists during stop")
	assert.Zero(t, requests.Load(), "stop intent prevents a new optional media request")
	assert.Empty(t, rig.sink.Messages()[0].SupplementalContent)
}

func TestKimiNativeMediaCancelsBeforeSendingNativeShutdown(t *testing.T) {
	t.Parallel()
	fake, _ := newFakeKap(t)
	id, _ := kimiMediaTestFile("shutdown-order image")
	entered := make(chan string, 1)
	jobContext := make(chan context.Context, 1)
	shutdownObservation := make(chan error, 1)
	released := make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/media/") {
			entered <- id
			<-r.Context().Done()
			return
		}
		if strings.HasSuffix(r.URL.Path, "/shutdown") {
			var ctx context.Context
			select {
			case ctx = <-jobContext:
			case <-r.Context().Done():
				return
			}
			shutdownObservation <- ctx.Err()
			select {
			case <-released:
				writeFakeKapEnvelope(w, http.StatusOK, 0, "success", nil)
			case <-r.Context().Done():
			}
			return
		}
		fake.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	program, err := os.Executable()
	require.NoError(t, err)
	base := &agenttest.ControlSink{}
	opts := agent.Options{
		AgentID: "media-shutdown", Shell: terminal.ResolveDefaultShell(), WorkingDir: t.TempDir(),
		StartupTimeout: 30 * time.Second, APITimeout: time.Minute,
		ExtraEnv: []string{"LEAPMUX_KIMI_CONFIG_PEER=" + server.URL, "LEAPMUX_KIMI_CONFIG_VALUE=kept"},
	}
	spec := launch.Spec{Program: program, PrefixArgs: []string{"-test.run=^TestKimiConfigDiagnosticPeer$", "--"}}
	running, err := startServer(t.Context(), opts, agent.NewProviderServices(base), spec, time.Second)
	require.NoError(t, err)
	a := running.(*Agent)
	stopped := make(chan struct{})
	stop := sync.OnceFunc(func() { go func() { a.Stop(); _ = a.Wait(); close(stopped) }() })
	t.Cleanup(func() { stop(); <-stopped })
	rig := &kimiOutputRig{agent: a, sink: base}
	rig.transcriptOps(t, 1, kimiMediaTestOperation(t, "image", id))
	awaitKimiMediaRequest(t, entered)
	service := a.mediaService()
	service.mu.Lock()
	for job := range service.jobs {
		jobContext <- job.ctx
		break
	}
	service.mu.Unlock()
	stop()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	select {
	case observed := <-shutdownObservation:
		require.ErrorIs(t, observed, context.Canceled, "media cancellation precedes native shutdown")
	case <-ctx.Done():
		t.Fatal("native shutdown did not reach its handler")
	}
	release()
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("the real helper process did not stop")
	}
	require.Len(t, base.Messages(), 1)
	assert.Empty(t, base.Messages()[0].SupplementalContent)
}
