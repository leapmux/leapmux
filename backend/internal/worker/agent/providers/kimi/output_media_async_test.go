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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/terminal"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiRunningChildSnapshotCapturesMediaInItsActiveScope(t *testing.T) {
	t.Parallel()
	for _, liveStart := range []bool{false, true} {
		t.Run(fmt.Sprintf("preceding_live_start=%t", liveStart), func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
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
			rig.startMediaProbeChild(t)
			const prompt = `{"type":"user_message","item_id":"child-opener","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","prompt_ids":[],"placement":"opener","origin":{"kind":"system"},"text":"Inspect the image.","attachments":[],"skills":[]}`
			if liveStart {
				rig.startConversationMediaTurn(t, "agent-0")
				rig.conversationAgentFrame(t, "agent-0", `{"type":"item.ended","turn_id":"t0","item":`+prompt+`}`)
			}
			image := kimiMediaFixtureItem(t, "image", fileID)
			page := fmt.Sprintf(`{"type":"turns","turns":[{%s,"items":[%s,%s]}],"has_more":false}`, kimiMediaFixtureTurn, prompt, image)
			frame := kimiConversationTestFrame(t, rig.sessionID(), "agent-0", page)
			handled := make(chan struct{})
			go func() { rig.agent.HandleOutput(frame); close(handled) }()
			assert.Contains(t, awaitKimiMediaRequest(t, entered), fileID)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			select {
			case <-handled:
			case <-ctx.Done():
				t.Fatal("the held image read blocked the child snapshot")
			}
			assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
			_, child := childOf(t, rig.sink, "session_1/agent-0")
			rows := child.Messages()
			require.NotEmpty(t, rows, "the child prompt is visible while the image read waits")
			view, err := decodeKimiConversationRecord(rows[0].Content)
			require.NoError(t, err)
			require.NotNil(t, view.item)
			assert.Equal(t, "child-opener", view.item.ItemID)
			require.Less(t, len(rows), 3, "the image result remains reserved")
			release()
			waitFor(t, func() bool { return child.MessageCount() == 3 }, "the child prompt, request and image result are retained")
			result := child.Messages()[2]
			view, err = decodeKimiConversationRecord(result.Content)
			require.NoError(t, err)
			assert.Equal(t, "agent-0", view.record.AgentID)
			assert.Equal(t, contracts.KimiConversationRecordRoleResult, view.record.Role)
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
		if slices.Contains(job.itemIDs, callID) {
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

const kimiMediaFixtureTurn = `"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]`

func (rig *kimiOutputRig) startConversationMediaTurn(t *testing.T, agentID string) {
	t.Helper()
	rig.conversationAgentFrame(t, agentID, `{"type":"turn.started","turn":{`+kimiMediaFixtureTurn+`}}`)
}

func (rig *kimiOutputRig) publishConversationMediaResult(t *testing.T, agentID, itemID string, fileIDs ...string) {
	t.Helper()
	item := kimiMediaFixtureItem(t, itemID, fileIDs...)
	rig.conversationAgentFrame(t, agentID, `{"type":"item.ended","turn_id":"t0","item":`+string(item)+`}`)
}

func (rig *kimiOutputRig) startMediaProbeChild(t *testing.T) {
	t.Helper()
	rig.startConversationMediaTurn(t, kimiMainAgentID)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"spawn","name":"Agent","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"description":"Image reader","prompt":"Inspect the image."},"agent_refs":[{"agent_id":"agent-0","role":"child"}]}}`)
}

func TestKimiNativeMediaPublishesResultsInAdmissionOrder(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startConversationMediaTurn(t, kimiMainAgentID)
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
	rig.publishConversationMediaResult(t, kimiMainAgentID, "first", firstID)
	assert.Equal(t, firstID, awaitKimiMediaRequest(t, entered))
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":{"type":"tool_call","item_id":"middle","name":"Bash","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"command":"printf middle"},"output":"middle result"}}`)
	rig.publishConversationMediaResult(t, kimiMainAgentID, "last", lastID)
	assert.Equal(t, lastID, awaitKimiMediaRequest(t, entered))
	releaseLast()
	awaitKimiMediaPreparation(t, rig.agent, "last")
	rows := rig.sink.Messages()
	require.Len(t, rows, 1, "only the first request can precede the held image result")
	firstRequest, err := decodeKimiConversationRecord(rows[0].Content)
	require.NoError(t, err)
	require.NotNil(t, firstRequest.item)
	assert.Equal(t, "first", firstRequest.item.ItemID)
	assert.Equal(t, contracts.KimiConversationRecordRoleRequest, firstRequest.record.Role)
	releaseFirst()
	awaitKimiTestOutput(t, rig.agent.sink)
	rows = rig.sink.Messages()
	require.Len(t, rows, 6)
	for index, id := range []string{"first", "middle", "last"} {
		for roleIndex, role := range []string{contracts.KimiConversationRecordRoleRequest, contracts.KimiConversationRecordRoleResult} {
			view, err := decodeKimiConversationRecord(rows[2*index+roleIndex].Content)
			require.NoError(t, err)
			require.NotNil(t, view.item)
			assert.Equal(t, id, view.item.ItemID)
			assert.Equal(t, role, view.record.Role)
		}
	}
}

func TestKimiNativeMediaReplayReservesOneResult(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startConversationMediaTurn(t, kimiMainAgentID)
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
	rig.publishConversationMediaResult(t, kimiMainAgentID, "image", id, id)
	awaitKimiMediaRequest(t, entered)
	rig.publishConversationMediaResult(t, kimiMainAgentID, "image", id, id)
	item := kimiMediaFixtureItem(t, "image", id, id)
	rig.conversationFrame(t, fmt.Sprintf(`{"type":"turns","turns":[{%s,"items":[%s]}],"has_more":false}`, kimiMediaFixtureTurn, item))
	release()
	awaitKimiTestOutput(t, rig.agent.sink)
	rows := rig.sink.Messages()
	require.Len(t, rows, 2)
	assert.EqualValues(t, 1, requests.Load())
	assert.Empty(t, rows[0].SupplementalContent)
	assert.NotEmpty(t, rows[1].SupplementalContent)
}

func TestKimiNativeMediaSessionReplacementKeepsTheOriginalDestination(t *testing.T) {
	t.Parallel()
	for _, finalSession := range []string{"session_2", "session_1"} {
		t.Run(finalSession, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			rig.startConversationMediaTurn(t, kimiMainAgentID)
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
			rig.publishConversationMediaResult(t, kimiMainAgentID, "same-call", id)
			assert.Equal(t, "/api/v1/sessions/session_1/media/"+id, awaitKimiMediaRequest(t, entered))
			rig.agent.dispatchMu.Lock()
			rig.agent.Mu.Lock()
			rig.agent.runs = nil
			rig.agent.Mu.Unlock()
			rig.agent.adoptSessionLocked("session_2", nil)
			if finalSession == "session_1" {
				rig.agent.adoptSessionLocked("session_1", nil)
			}
			rig.agent.finishDispatch()
			awaitKimiTestOutput(t, rig.agent.sink)
			oldRows := rig.sink.Messages()
			require.Len(t, oldRows, 2, "the accepted old result survives session retirement")
			old := oldRows[1]
			assert.Equal(t, "session_1", old.AgentSessionID)
			assert.Empty(t, old.SupplementalContent)
			release()
			rig.startConversationMediaTurn(t, kimiMainAgentID)
			rig.publishConversationMediaResult(t, kimiMainAgentID, "same-call", id)
			assert.Equal(t, "/api/v1/sessions/"+finalSession+"/media/"+id, awaitKimiMediaRequest(t, entered))
			awaitKimiTestOutput(t, rig.agent.sink)
			rows := rig.sink.Messages()
			if finalSession == "session_1" {
				require.Len(t, rows, 2, "reattaching the exact native item enriches its existing row")
				assert.Equal(t, old.Content, rows[1].Content)
				assert.EqualValues(t, 1, rows[1].SupplementalRevision)
			} else {
				require.Len(t, rows, 4)
				assert.Equal(t, old.Content, rows[1].Content)
				assert.Empty(t, rows[1].SupplementalContent)
				assert.NotEqual(t, old.IdempotencyKey, rows[3].IdempotencyKey)
			}
			current := rows[len(rows)-1]
			assert.Equal(t, finalSession, current.AgentSessionID)
			assert.NotEmpty(t, current.SupplementalContent)
		})
	}
}

func TestKimiNativeMediaShutdownCompletesEveryReservation(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startConversationMediaTurn(t, kimiMainAgentID)
	id, _ := kimiMediaTestFile("shutdown image")
	entered := make(chan string, 8)
	rig.agent.api = newKimiTestClient(t, func(_ http.ResponseWriter, r *http.Request) { entered <- id; <-r.Context().Done() })
	for itemNumber := 1; itemNumber <= 6; itemNumber++ {
		rig.publishConversationMediaResult(t, kimiMainAgentID, fmt.Sprint(itemNumber), id)
	}
	awaitKimiMediaRequest(t, entered)
	rig.agent.closeMedia()
	require.Len(t, rig.sink.Messages(), 12)
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
	rig := newKimiConversationRig(t)
	rig.startConversationMediaTurn(t, kimiMainAgentID)
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
	for itemNumber := 1; itemNumber <= 33; itemNumber++ {
		rig.publishConversationMediaResult(t, kimiMainAgentID, fmt.Sprint(itemNumber), id)
	}
	release()
	awaitKimiTestOutput(t, rig.agent.sink)
	rows := rig.sink.Messages()
	require.Len(t, rows, 66, "all requests and original results persist")
	assert.EqualValues(t, 32, requests.Load())
	refused := rows[65]
	view, err := decodeKimiConversationRecord(refused.Content)
	require.NoError(t, err)
	require.NotNil(t, view.item)
	assert.Equal(t, contracts.KimiConversationRecordRoleResult, view.record.Role)
	assert.Equal(t, "33", view.item.ItemID)
	assert.Empty(t, refused.SupplementalContent)
	rig.publishConversationMediaResult(t, kimiMainAgentID, "after-drain", id)
	awaitKimiTestOutput(t, rig.agent.sink)
	rows = rig.sink.Messages()
	require.Len(t, rows, 68)
	assert.NotEmpty(t, rows[67].SupplementalContent, "media admission resumes after draining")
}

func TestKimiNativeMediaPreparedResultsKeepTheirAdmissionSlots(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startConversationMediaTurn(t, kimiMainAgentID)
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
	rig.publishConversationMediaResult(t, kimiMainAgentID, "first", heldID)
	for itemNumber := 2; itemNumber <= 32; itemNumber++ {
		rig.publishConversationMediaResult(t, kimiMainAgentID, fmt.Sprint(itemNumber), fastID)
	}
	// Four job workers cannot start all these requests without preparing earlier results.
	for range 32 {
		awaitKimiMediaRequest(t, entered)
	}
	for itemNumber := 33; itemNumber <= 64; itemNumber++ {
		rig.publishConversationMediaResult(t, kimiMainAgentID, fmt.Sprint(itemNumber), fastID)
	}
	release()
	awaitKimiTestOutput(t, rig.agent.sink)
	require.Len(t, rig.sink.Messages(), 128, "all admitted requests and original results persist")
	assert.EqualValues(t, 32, requests.Load(), "prepared rows behind the first reservation still consume admission slots")
}

func TestKimiNativeMediaPreparedSupplementsConsumeRetentionBytes(t *testing.T) {
	// Keep this large-memory case serial with the other package tests.
	rig := newKimiConversationRig(t)
	rig.startConversationMediaTurn(t, kimiMainAgentID)
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
	rig.publishConversationMediaResult(t, kimiMainAgentID, "first", heldID)
	awaitKimiMediaRequest(t, firstEntered)
	for itemNumber := 2; itemNumber <= 8; itemNumber++ {
		rig.publishConversationMediaResult(t, kimiMainAgentID, fmt.Sprint(itemNumber), largeID)
	}
	for itemNumber := 9; itemNumber <= 11; itemNumber++ {
		rig.publishConversationMediaResult(t, kimiMainAgentID, fmt.Sprint(itemNumber), barrierID)
	}
	// The first worker stays held. All three other workers reach these barriers
	// only after every earlier large result completed preparation.
	for range 3 {
		awaitKimiMediaRequest(t, barrierEntered)
	}
	release()
	awaitKimiTestOutput(t, rig.agent.sink)
	rows := rig.sink.Messages()
	require.Len(t, rows, 22, "all queued requests and original results persist")
	total, withImages := 0, 0
	for _, row := range rows {
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
	rig := newKimiConversationRig(t)
	rig.startConversationMediaTurn(t, kimiMainAgentID)
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
	largeOutput, err := json.Marshal(strings.Repeat("x", 2<<20))
	require.NoError(t, err)
	for itemNumber := 1; itemNumber <= 32; itemNumber++ {
		var item kimiConversationItem
		require.NoError(t, json.Unmarshal(kimiMediaFixtureItem(t, fmt.Sprint(itemNumber), id), &item))
		item.Output = largeOutput
		raw, err := json.Marshal(item)
		require.NoError(t, err)
		rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":`+string(raw)+`}`)
	}
	release()
	awaitKimiTestOutput(t, rig.agent.sink)
	rows := rig.sink.Messages()
	require.Len(t, rows, 64, "all large original results and their requests persist")
	assert.Less(t, requests.Load(), int32(32), "the byte cap refuses media before the job cap")
	assert.Empty(t, rows[63].SupplementalContent)
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
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	files, err := rig.agent.api.readMediaFiles(ctx, "session_1", []string{firstID, secondID}, 10, make(chan struct{}, kimiMediaConcurrency))
	require.ErrorContains(t, err, "remaining message size limit")
	assert.Empty(t, files)
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
	_, err := rig.agent.api.readMediaFiles(ctx, "session_1", []string{badID, heldID}, 4096, make(chan struct{}, kimiMediaConcurrency))
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
	base.UpdateSessionID(rig.sessionID())
	rig.startConversationMediaTurn(t, kimiMainAgentID)
	id, data := kimiMediaTestFile("reservation image")
	var requests atomic.Int32
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	})
	rig.publishConversationMediaResult(t, kimiMainAgentID, "image", id)
	assert.Empty(t, base.Messages())
	assert.Zero(t, requests.Load())
	state := rig.agent.run(kimiMainAgentID).conversation.document.turns["t0"].items["image"]
	require.NotNil(t, state)
	assert.True(t, state.projectionPending)
	sink.refuse.Store(false)
	rig.publishConversationMediaResult(t, kimiMainAgentID, "image", id)
	awaitKimiTestOutput(t, rig.agent.sink)
	rows := base.Messages()
	require.Len(t, rows, 2, "the replay retains the previously refused request and result")
	assert.NotEmpty(t, rows[1].SupplementalContent)
	assert.EqualValues(t, 1, requests.Load())
}

func TestKimiNativeMediaPersistenceRetryKeepsThePreparedSupplement(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startConversationMediaTurn(t, kimiMainAgentID)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"image","name":"ReadMediaFile","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"path":"original.png"}}}`)
	require.Len(t, rig.sink.Messages(), 1)
	rig.sink.PersistErr = errors.New("the first media storage attempt fails")
	id, data := kimiMediaTestFile("retry image")
	var requests atomic.Int32
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	})
	rig.publishConversationMediaResult(t, kimiMainAgentID, "image", id)
	awaitKimiMediaPreparation(t, rig.agent, "image")
	failed := rig.sink.Messages()
	require.Len(t, failed, 2)
	assert.True(t, failed[0].Stored())
	assert.False(t, failed[1].Stored())
	assert.NotEmpty(t, failed[1].SupplementalContent)
	service := rig.agent.mediaService()
	service.mu.Lock()
	assert.Len(t, service.jobs, 1)
	assert.Greater(t, service.retainedBytes, len(failed[1].Content))
	service.mu.Unlock()
	// Joining download workers must not require a successful storage retry.
	rig.agent.closeMedia()
	rig.sink.PersistErr = nil
	require.NoError(t, agent.FlushPendingOutput(rig.agent.sink))
	rows := rig.sink.Messages()
	require.Len(t, rows, 3)
	assert.True(t, rows[2].Stored())
	assert.Equal(t, failed[1].Content, rows[2].Content)
	assert.Equal(t, failed[1].SupplementalContent, rows[2].SupplementalContent)
	assert.EqualValues(t, 1, requests.Load())
	service.mu.Lock()
	assert.Empty(t, service.jobs)
	assert.Zero(t, service.retainedBytes)
	service.mu.Unlock()
}

func TestKimiNativeMediaSharesTheDownloadLimitAcrossMainAndChild(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.agent.SetAPITimeoutForTest(time.Minute)
	rig.startMediaProbeChild(t)
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
	rig.startConversationMediaTurn(t, "agent-0")
	rig.publishConversationMediaResult(t, kimiMainAgentID, "main-images", ids[:6]...)
	rig.publishConversationMediaResult(t, "agent-0", "child-images", ids[6:]...)
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
	rig := newKimiConversationRig(t)
	rig.startMediaProbeChild(t)
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
	rig.startConversationMediaTurn(t, "agent-0")
	rig.conversationAgentFrame(t, "agent-0", `{"type":"item.ended","turn_id":"t0","item":{"type":"user_message","item_id":"opener","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","prompt_ids":["image-prompt"],"placement":"opener","origin":{"kind":"user"},"text":"Read the image.","attachments":[],"skills":[]}}`)
	rig.publishConversationMediaResult(t, "agent-0", "image", id)
	awaitKimiMediaRequest(t, entered)
	rig.conversationAgentFrame(t, "agent-0", `{"type":"turn.ended","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	linked, present := rig.agent.children.get("agent-0")
	require.True(t, present)
	assert.True(t, linked.ended)
	release()
	waitFor(t, func() bool { return len(child.Messages()) == initial+3 }, "the captured child result survives cleanup")
	row := child.Messages()[initial+1]
	assert.Contains(t, string(row.Content), id)
	assert.NotEmpty(t, row.SupplementalContent)
	for _, parentRow := range rig.sink.Messages() {
		assert.NotContains(t, string(parentRow.Content), id)
	}
}

func TestKimiNativeMediaBodyFailureRetainsTheOriginalResult(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startConversationMediaTurn(t, kimiMainAgentID)
	id, data := kimiMediaTestFile("incomplete response body")
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", fmt.Sprint(len(data)+100))
		_, _ = w.Write(data)
	})
	rig.publishConversationMediaResult(t, kimiMainAgentID, "image", id)
	awaitKimiTestOutput(t, rig.agent.sink)
	rows := rig.sink.Messages()
	require.Len(t, rows, 2, "the incomplete media response retains its native closing row")
	row := rows[1]
	view, err := decodeKimiConversationRecord(row.Content)
	require.NoError(t, err)
	require.NotNil(t, view.item)
	require.Len(t, view.item.Media, 1)
	assert.Equal(t, id, view.item.Media[0].FileID)
	assert.Equal(t, contracts.KimiConversationRecordRoleResult, view.record.Role)
	assert.Empty(t, row.SupplementalContent)
}

func TestKimiNativeMediaRefusesNewDownloadsAfterStopIntent(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startConversationMediaTurn(t, kimiMainAgentID)
	id, data := kimiMediaTestFile("stop-window image")
	var requests atomic.Int32
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	})
	rig.agent.NoteIntentionalStop()
	require.False(t, rig.agent.IsStopped(), "the process remains live during graceful shutdown")
	rig.publishConversationMediaResult(t, kimiMainAgentID, "during-stop", id)
	awaitKimiTestOutput(t, rig.agent.sink)
	rows := rig.sink.Messages()
	require.Len(t, rows, 2, "the accepted original closing result persists during stop")
	assert.Zero(t, requests.Load(), "stop intent prevents a new optional media request")
	assert.Empty(t, rows[1].SupplementalContent)
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
	rig.startConversationMediaTurn(t, kimiMainAgentID)
	rig.publishConversationMediaResult(t, kimiMainAgentID, "image", id)
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
	rows := base.Messages()
	require.GreaterOrEqual(t, len(rows), 2)
	assert.Empty(t, rows[1].SupplementalContent)
}
