package muse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type museControlLogHandler struct {
	handle func(slog.Record)
}

func (*museControlLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (handler *museControlLogHandler) Handle(_ context.Context, record slog.Record) error {
	handler.handle(record)
	return nil
}
func (handler *museControlLogHandler) WithAttrs([]slog.Attr) slog.Handler { return handler }
func (handler *museControlLogHandler) WithGroup(string) slog.Handler      { return handler }

func museControlCallbackCanReenter(t *testing.T, a *Agent) bool {
	t.Helper()
	if !a.dispatchMu.TryLock() {
		t.Error("the control callback retains the dispatch mutex")
		return false
	}
	a.dispatchMu.Unlock()
	if !a.stateMu.TryLock() {
		t.Error("the control callback retains the state mutex")
		return false
	}
	a.stateMu.Unlock()
	return true
}

func museApprovalStageRequest(t *testing.T, stage int64) (string, []byte) {
	t.Helper()
	raw := approvalRequest("session", stage)
	var frame map[string]any
	require.NoError(t, json.Unmarshal(raw, &frame))
	frame["method"] = contracts.MuseMethodApprovalUpdated
	frame["futureNativeValue"] = "  Native bytes 界\n"
	return testApprovalKey("session", "approval", stage), []byte(mustMuseChoiceJSON(t, frame))
}

func museApprovalSettlement(t *testing.T, stage int64) []byte {
	t.Helper()
	return []byte(mustMuseChoiceJSON(t, map[string]any{
		"method": contracts.MuseMethodApprovalResolved,
		"params": map[string]any{
			"sessionId": "session", "approvalId": "approval", "decision": contracts.MuseApprovalDecisionApproved,
			"stageEvidence": []any{map[string]any{
				"requirementId": map[string]any{"approvalId": "approval", "sourceIndex": stage},
				"position":      0, "totalStages": 1, "argv": []any{}, "resolution": map[string]any{"kind": "approved"},
			}},
		},
	}))
}

func TestMuseControlPublicationReleasesDispatchBeforeRetirement(t *testing.T) {
	a, sink := museControlPublicationAgent(t)
	key, raw := museApprovalStageRequest(t, 0)
	original := slices.Clone(raw)
	entered := false
	sink.onPublish = func(request agent.ControlRequest) {
		assert.Equal(t, original, []byte(request.Payload))
		if !museControlCallbackCanReenter(t, a) {
			return
		}
		a.retireHost(agent.MessageCompletionInterrupted)
		entered = true
	}
	a.HandleOutput(raw)
	assert.True(t, entered, "the real callback must execute actual retirement")
	assert.True(t, a.sessions["session"].retired)
	assert.Empty(t, a.sessions["session"].controls)
	assert.Empty(t, sink.pending)
	assert.Equal(t, []string{key}, sink.canceled)
	require.Len(t, sink.requests, 1)
	assert.Equal(t, original, []byte(sink.requests[0].Payload))
	assert.Equal(t, original, raw)
}

func TestMuseControlWarningReleasesDispatchBeforeLoggerCallback(t *testing.T) {
	a, sink := museControlPublicationAgent(t)
	nextKey, next := museApprovalStageRequest(t, 1)
	var malformed map[string]any
	require.NoError(t, json.Unmarshal(approvalRequest("session", 0), &malformed))
	malformed["method"] = contracts.MuseMethodApprovalUpdated
	malformed["params"].(map[string]any)["currentRequirementId"] = map[string]any{"approvalId": "approval", "sourceIndex": -1}
	malformedRaw := []byte(mustMuseChoiceJSON(t, malformed))
	previousLogger := slog.Default()
	previousWriter, previousFlags := log.Writer(), log.Flags()
	restoreLogger := func() {
		// The initial slog handler does not restore the standard log writer.
		slog.SetDefault(previousLogger)
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	}
	t.Cleanup(restoreLogger)
	entered := false
	slog.SetDefault(slog.New(&museControlLogHandler{handle: func(record slog.Record) {
		assert.Equal(t, "read a Muse control identity", record.Message)
		hasError := false
		record.Attrs(func(attr slog.Attr) bool {
			hasError = hasError || attr.Key == "error"
			return true
		})
		assert.True(t, hasError)
		if !a.dispatchMu.TryLock() {
			t.Error("the control warning retains the dispatch mutex")
			panic("the control logger panics")
		}
		a.dispatchMu.Unlock()
		a.HandleOutput(next)
		entered = true
		panic("the control logger panics")
	}}))
	require.PanicsWithValue(t, "the control logger panics", func() { a.HandleOutput(malformedRaw) })
	assert.True(t, entered, "the logger callback must enter the real next control path")
	if assert.True(t, a.dispatchMu.TryLock(), "a logger panic must release the dispatch mutex") {
		a.dispatchMu.Unlock()
	}
	restoreLogger()
	assert.Same(t, previousLogger, slog.Default())
	assert.Same(t, previousWriter, log.Writer())
	assert.Equal(t, previousFlags, log.Flags())
	a.HandleOutput(slices.Clone(next))
	assert.Equal(t, nextKey, a.sessions["session"].controls["approval"].key)
	require.Len(t, sink.requests, 1)
	assert.Equal(t, next, []byte(sink.requests[0].Payload))
}

func TestMuseControlPublicationOrdersANewerStageFromTheCallback(t *testing.T) {
	a, sink := museControlPublicationAgent(t)
	firstKey, first := museApprovalStageRequest(t, 0)
	nextKey, next := museApprovalStageRequest(t, 1)
	originalFirst, originalNext := slices.Clone(first), slices.Clone(next)
	entered := false
	sink.onPublish = func(request agent.ControlRequest) {
		if request.RequestID != firstKey || !museControlCallbackCanReenter(t, a) {
			return
		}
		a.HandleOutput(next)
		entered = true
	}
	a.HandleOutput(first)
	assert.True(t, entered, "the real callback must publish the newer native stage")
	assert.Equal(t, nextKey, a.sessions["session"].controls["approval"].key)
	assert.Equal(t, []string{firstKey}, sink.canceled)
	require.Len(t, sink.requests, 2)
	assert.Equal(t, originalFirst, []byte(sink.requests[0].Payload))
	assert.Equal(t, originalNext, []byte(sink.requests[1].Payload))
	require.Len(t, sink.pending, 1)
	assert.Equal(t, originalNext, []byte(sink.pending[nextKey].Payload))
	assert.Equal(t, originalFirst, first)
	assert.Equal(t, originalNext, next)
}

func TestMuseControlPublicationOrdersSettlementFromTheCallback(t *testing.T) {
	a, sink := museControlPublicationAgent(t)
	key, raw := museApprovalStageRequest(t, 0)
	settlement := museApprovalSettlement(t, 0)
	original := slices.Clone(raw)
	entered := false
	sink.onPublish = func(agent.ControlRequest) {
		if !museControlCallbackCanReenter(t, a) {
			return
		}
		a.HandleOutput(settlement)
		entered = true
	}
	a.HandleOutput(raw)
	assert.True(t, entered, "the real callback must execute the native settlement")
	assert.Empty(t, a.sessions["session"].controls)
	assert.Empty(t, sink.pending)
	assert.Equal(t, []string{key}, sink.canceled)
	assert.Equal(t, original, raw)
}

func TestMuseApprovalSettlementDuringReplacementUsesTheLatestAdmittedStage(t *testing.T) {
	a, sink := museControlPublicationAgent(t)
	firstKey, first := museApprovalStageRequest(t, 0)
	nextKey, next := museApprovalStageRequest(t, 1)
	settlement := museApprovalSettlement(t, 0)
	a.HandleOutput(first)
	entered := false
	sink.onPublish = func(request agent.ControlRequest) {
		if request.RequestID != nextKey || !museControlCallbackCanReenter(t, a) {
			return
		}
		a.HandleOutput(settlement)
		entered = true
	}
	a.HandleOutput(next)
	assert.True(t, entered, "the replacement callback must admit whole-approval settlement")
	assert.Empty(t, a.sessions["session"].controls)
	assert.Empty(t, sink.pending)
	assert.Equal(t, []string{firstKey, nextKey}, sink.canceled)
	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 1)
	assert.Equal(t, settlement, notifications[0].Content)
}

func TestMuseSettlementCancellationPanicRetainsPersistenceProgress(t *testing.T) {
	a, sink := museControlPublicationAgent(t)
	key, request := museApprovalStageRequest(t, 0)
	settlement := museApprovalSettlement(t, 0)
	original := slices.Clone(settlement)
	a.HandleOutput(request)
	sink.onCancel = func(string) { panic("the cancellation observer panics") }
	require.PanicsWithValue(t, "the cancellation observer panics", func() { a.HandleOutput(settlement) })
	assert.Equal(t, []string{key}, sink.canceled)
	assert.Empty(t, sink.PersistedNotifications())
	settlement[0] = '['
	sink.onCancel = nil
	var nextFrame map[string]any
	require.NoError(t, json.Unmarshal(approvalRequest("session", 0), &nextFrame))
	nextParams := nextFrame["params"].(map[string]any)
	nextParams["approvalId"] = "next-approval"
	nextParams["currentRequirementId"] = map[string]any{"approvalId": "next-approval", "sourceIndex": 0}
	next := []byte(mustMuseChoiceJSON(t, nextFrame))
	a.HandleOutput(next)
	assert.Equal(t, []string{key}, sink.canceled)
	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 1)
	assert.Equal(t, original, notifications[0].Content)
}

func TestMuseControlPublicationCapturesQueuedBytesBeforeCallerMutation(t *testing.T) {
	a, sink := museControlPublicationAgent(t)
	firstKey, first := museApprovalStageRequest(t, 0)
	nextKey, next := museApprovalStageRequest(t, 1)
	originalNext := slices.Clone(next)
	entered := false
	sink.onPublish = func(request agent.ControlRequest) {
		if request.RequestID != firstKey || !museControlCallbackCanReenter(t, a) {
			return
		}
		a.HandleOutput(next)
		next[0] = '['
		entered = true
	}
	a.HandleOutput(first)
	assert.True(t, entered, "the callback must mutate the caller's frame after native admission")
	assert.False(t, bytes.Equal(originalNext, next))
	assert.Equal(t, nextKey, a.sessions["session"].controls["approval"].key)
	require.Len(t, sink.requests, 2)
	assert.Equal(t, originalNext, []byte(sink.requests[1].Payload))
	assert.Equal(t, originalNext, []byte(sink.pending[nextKey].Payload))
}

func TestMuseControlPublicationCoalescesANestedExactReplay(t *testing.T) {
	a, sink := museControlPublicationAgent(t)
	key, raw := museApprovalStageRequest(t, 0)
	original := slices.Clone(raw)
	entered := false
	sink.onPublish = func(agent.ControlRequest) {
		if entered || !museControlCallbackCanReenter(t, a) {
			return
		}
		entered = true
		a.HandleOutput(slices.Clone(raw))
	}
	a.HandleOutput(raw)
	assert.True(t, entered, "the callback must enter the actual duplicate public path")
	assert.Equal(t, 1, sink.attempts)
	require.Len(t, sink.requests, 1)
	require.Len(t, sink.pending, 1)
	assert.Equal(t, key, a.sessions["session"].controls["approval"].key)
	assert.Equal(t, original, []byte(sink.pending[key].Payload))
	assert.Equal(t, original, raw)
}

func TestMuseControlPublicationRetainsTheLatestSuccessfulStage(t *testing.T) {
	for _, indices := range [][2]int64{{0, 1}, {1, 0}} {
		t.Run(string(mustMuseChoiceJSON(t, indices)), func(t *testing.T) {
			a, sink := museControlPublicationAgent(t)
			firstKey, first := museApprovalStageRequest(t, indices[0])
			nextKey, next := museApprovalStageRequest(t, indices[1])
			entered := false
			sink.onPublish = func(request agent.ControlRequest) {
				if request.RequestID != firstKey || !museControlCallbackCanReenter(t, a) {
					return
				}
				a.HandleOutput(next)
				entered = true
			}
			a.HandleOutput(first)
			assert.True(t, entered)
			assert.Equal(t, nextKey, a.sessions["session"].controls["approval"].key)
			assert.Equal(t, []string{firstKey}, sink.canceled)
			require.Len(t, sink.requests, 2)
			require.Len(t, sink.pending, 1)
			assert.Equal(t, next, []byte(sink.pending[nextKey].Payload))
		})
	}
}

func TestMuseControlPublicationDrainsTheRecoveredViewOutsideDispatch(t *testing.T) {
	a, sink := museControlPublicationAgent(t)
	firstKey, first := museApprovalStageRequest(t, 0)
	nextKey, next := museApprovalStageRequest(t, 1)
	var frame map[string]any
	require.NoError(t, json.Unmarshal(first, &frame))
	frame["params"].(map[string]any)["viewCursor"] = "recovered"
	first = []byte(mustMuseChoiceJSON(t, frame))
	state := a.sessions["session"]
	recovery := &viewRecovery{after: "before", next: "recovered"}
	state.viewRecovery = recovery
	pages := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodViewPage, method)
		pages++
		var params struct {
			Cursor string `json:"cursor"`
		}
		require.NoError(t, json.Unmarshal(raw, &params))
		assert.Equal(t, "before", params.Cursor)
		return agenttest.RPCReply{Result: mustMuseChoiceJSON(t, map[string]any{"events": []json.RawMessage{first}, "nextCursor": nil})}
	}})
	entered := false
	sink.onPublish = func(request agent.ControlRequest) {
		if request.RequestID != firstKey || !museControlCallbackCanReenter(t, a) {
			return
		}
		a.HandleOutput(next)
		entered = true
	}
	a.runViewRecovery("session", state, recovery)
	assert.Equal(t, 1, pages)
	assert.True(t, entered, "the recovered frame must call the actual nested public path")
	assert.Nil(t, state.viewRecovery)
	assert.Equal(t, nextKey, state.controls["approval"].key)
	require.Len(t, sink.requests, 2)
	assert.Equal(t, first, []byte(sink.requests[0].Payload))
	assert.Equal(t, next, []byte(sink.requests[1].Payload))
	assert.Equal(t, []string{firstKey}, sink.canceled)
	require.Len(t, sink.pending, 1)
	assert.Equal(t, next, []byte(sink.pending[nextKey].Payload))
}

func TestMuseApprovalSettlementCancelsOnlyItsExactStage(t *testing.T) {
	for _, evidence := range []struct {
		label   string
		indices []int64
	}{
		{label: "empty native evidence"},
		{label: "earlier requirement evidence", indices: []int64{0}},
		{label: "current requirement evidence", indices: []int64{1}},
		{label: "both requirement tokens", indices: []int64{0, 1}},
		{label: "reversed requirement tokens", indices: []int64{1, 0}},
	} {
		t.Run(evidence.label, func(t *testing.T) {
			a, sink := museControlPublicationAgent(t)
			firstKey, first := museApprovalStageRequest(t, 0)
			nextKey, next := museApprovalStageRequest(t, 1)
			a.HandleOutput(first)
			a.HandleOutput(next)
			var otherFrame map[string]any
			require.NoError(t, json.Unmarshal(first, &otherFrame))
			otherParams := otherFrame["params"].(map[string]any)
			otherParams["approvalId"] = "other-approval"
			otherParams["currentRequirementId"] = map[string]any{"approvalId": "other-approval", "sourceIndex": 0}
			otherRaw := []byte(mustMuseChoiceJSON(t, otherFrame))
			otherKey := testApprovalKey("session", "other-approval", 0)
			a.HandleOutput(otherRaw)
			assert.Equal(t, nextKey, a.sessions["session"].controls["approval"].key)
			assert.Equal(t, []string{firstKey}, sink.canceled)
			require.Len(t, sink.pending, 2)
			assert.Equal(t, next, []byte(sink.pending[nextKey].Payload))

			var settlement map[string]any
			require.NoError(t, json.Unmarshal(museApprovalSettlement(t, 0), &settlement))
			params := settlement["params"].(map[string]any)
			stages := make([]any, 0, len(evidence.indices))
			for _, index := range evidence.indices {
				stages = append(stages, map[string]any{
					"requirementId": map[string]any{"approvalId": "approval", "sourceIndex": index},
					"position":      1, "totalStages": 2, "argv": []any{}, "resolution": map[string]any{"kind": "unresolved"},
				})
			}
			params["stageEvidence"] = stages
			params["decision"] = contracts.MuseApprovalDecisionAbort
			raw := []byte(mustMuseChoiceJSON(t, settlement))
			original := slices.Clone(raw)
			a.HandleOutput(raw)
			assert.NotContains(t, a.sessions["session"].controls, "approval")
			assert.Equal(t, otherKey, a.sessions["session"].controls["other-approval"].key)
			require.Len(t, sink.pending, 1)
			assert.Equal(t, otherRaw, []byte(sink.pending[otherKey].Payload))
			assert.Equal(t, []string{firstKey, nextKey}, sink.canceled)
			notifications := sink.PersistedNotifications()
			require.Len(t, notifications, 1)
			assert.Equal(t, original, notifications[0].Content)
			assert.Equal(t, original, raw)
			a.HandleOutput(slices.Clone(raw))
			assert.Equal(t, []string{firstKey, nextKey}, sink.canceled)
			assert.Len(t, sink.PersistedNotifications(), 1)
		})
	}
}

func TestMuseQuestionSettlementRequiresAReadableNativeOutcome(t *testing.T) {
	type outcomeCase struct {
		label        string
		value        any
		present      bool
		mutateParams func(map[string]any)
	}
	cases := []outcomeCase{{label: "absent outcome"}}
	for _, outcome := range []any{nil, 0, false, []any{}, map[string]any{}, "", " \t\n"} {
		cases = append(cases, outcomeCase{label: string(mustMuseChoiceJSON(t, outcome)), value: outcome, present: true})
	}
	for _, identity := range []struct {
		label  string
		field  string
		value  any
		absent bool
	}{
		{label: "absent session", field: "sessionId", absent: true},
		{label: "null session", field: "sessionId"},
		{label: "numeric session", field: "sessionId", value: 0},
		{label: "empty session", field: "sessionId", value: ""},
		{label: "foreign session", field: "sessionId", value: "foreign-session"},
		{label: "absent input", field: "userInputId", absent: true},
		{label: "null input", field: "userInputId"},
		{label: "numeric input", field: "userInputId", value: 0},
		{label: "empty input", field: "userInputId", value: ""},
		{label: "foreign input", field: "userInputId", value: "foreign-input"},
	} {
		identity := identity
		cases = append(cases, outcomeCase{
			label: identity.label, value: "answered", present: true,
			mutateParams: func(params map[string]any) {
				if identity.absent {
					delete(params, identity.field)
					return
				}
				params[identity.field] = identity.value
			},
		})
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			a, sink := museControlPublicationAgent(t)
			key, request := questionRequest(t, []any{museNativeQuestion("native", "single")})
			a.HandleOutput(request)
			params := map[string]any{"sessionId": "session", "userInputId": "question", "answers": []any{}}
			if tc.present {
				params["outcome"] = tc.value
			}
			if tc.mutateParams != nil {
				tc.mutateParams(params)
			}
			settlement := mustMuseChoiceJSON(t, map[string]any{"method": contracts.MuseMethodUserInputSettled, "params": params})
			a.HandleOutput(settlement)
			assert.Equal(t, key, a.sessions["session"].controls["question"].key)
			require.Len(t, sink.pending, 1)
			assert.Equal(t, request, []byte(sink.pending[key].Payload))
			assert.Empty(t, sink.canceled)
		})
	}
}

func TestMuseQuestionSettlementKeepsUnknownNativeFinality(t *testing.T) {
	for _, outcome := range []string{"answered", "cancelled", "interrupted", "clarified", "timedOut", "aborted", "futureSettlement"} {
		t.Run(outcome, func(t *testing.T) {
			a, sink := museControlPublicationAgent(t)
			key, request := questionRequest(t, []any{museNativeQuestion("native", "single")})
			a.HandleOutput(request)
			settlement := []byte(mustMuseChoiceJSON(t, map[string]any{"method": contracts.MuseMethodUserInputSettled, "params": map[string]any{"sessionId": "session", "userInputId": "question", "outcome": outcome, "answers": []any{}, "futureNativeValue": " \t\n界"}}))
			original := slices.Clone(settlement)
			a.HandleOutput(settlement)
			assert.Empty(t, a.sessions["session"].controls)
			assert.Empty(t, sink.pending)
			assert.Equal(t, []string{key}, sink.canceled)
			notifications := sink.PersistedNotifications()
			require.Len(t, notifications, 1)
			assert.Equal(t, original, notifications[0].Content)
			assert.Equal(t, original, settlement)
		})
	}
}

func waitMuseControlSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
	defer cancel()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatal(message)
	}
}

func TestMuseControlPublicationKeepsTheClaimAfterConcurrentAdmission(t *testing.T) {
	a, sink := museControlPublicationAgent(t)
	firstKey, first := museApprovalStageRequest(t, 0)
	nextKey, next := museApprovalStageRequest(t, 1)
	entered, release := make(chan struct{}), make(chan struct{})
	var closeRelease sync.Once
	firstDone := make(chan struct{})
	var nextDone chan struct{}
	t.Cleanup(func() {
		closeRelease.Do(func() { close(release) })
		waitMuseControlSignal(t, firstDone, "the first native publication did not finish during cleanup")
		if nextDone != nil {
			waitMuseControlSignal(t, nextDone, "the concurrent native publication did not finish during cleanup")
		}
	})
	sink.onPublish = func(request agent.ControlRequest) {
		if request.RequestID == firstKey {
			close(entered)
			<-release
		}
	}
	go func() { defer close(firstDone); a.HandleOutput(first) }()
	waitMuseControlSignal(t, entered, "the first native publication did not reach its callback")
	if !museControlCallbackCanReenter(t, a) {
		closeRelease.Do(func() { close(release) })
		waitMuseControlSignal(t, firstDone, "the first native publication did not return")
		return
	}
	nextDone = make(chan struct{})
	go func() { defer close(nextDone); a.HandleOutput(next) }()
	waitMuseControlSignal(t, nextDone, "the concurrent native admission waits for the active callback")
	assert.Len(t, sink.requests, 1)
	closeRelease.Do(func() { close(release) })
	waitMuseControlSignal(t, firstDone, "the active drainer did not finish the admitted records")
	assert.Equal(t, nextKey, a.sessions["session"].controls["approval"].key)
	require.Len(t, sink.requests, 2)
	assert.Equal(t, first, []byte(sink.requests[0].Payload))
	assert.Equal(t, next, []byte(sink.requests[1].Payload))
	assert.Equal(t, []string{firstKey}, sink.canceled)
	require.Len(t, sink.pending, 1)
	assert.Equal(t, next, []byte(sink.pending[nextKey].Payload))
}

func TestMuseControlPublicationReleasesItsClaimAfterCallbackPanic(t *testing.T) {
	a, sink := museControlPublicationAgent(t)
	firstKey, first := museApprovalStageRequest(t, 0)
	nextKey, next := museApprovalStageRequest(t, 1)
	nested := false
	sink.onPublish = func(agent.ControlRequest) {
		if museControlCallbackCanReenter(t, a) {
			a.HandleOutput(next)
			nested = true
		}
		panic("the control observer panics")
	}
	require.PanicsWithValue(t, "the control observer panics", func() { a.HandleOutput(first) })
	assert.True(t, nested, "the actual nested request must remain available after the panic")
	sink.onPublish = nil
	a.HandleOutput(slices.Clone(first))
	assert.Equal(t, nextKey, a.sessions["session"].controls["approval"].key)
	require.Len(t, sink.requests, 3)
	assert.Equal(t, first, []byte(sink.requests[0].Payload))
	assert.Equal(t, first, []byte(sink.requests[1].Payload))
	assert.Equal(t, next, []byte(sink.requests[2].Payload))
	assert.Equal(t, []string{firstKey}, sink.canceled)
	require.Len(t, sink.pending, 1)
	assert.Equal(t, next, []byte(sink.pending[nextKey].Payload))
}

func TestMuseControlCancellationCanPublishTheNextRequest(t *testing.T) {
	a, sink := museControlPublicationAgent(t)
	firstKey, first := museApprovalStageRequest(t, 0)
	var nextFrame map[string]any
	require.NoError(t, json.Unmarshal(approvalRequest("session", 0), &nextFrame))
	nextParams := nextFrame["params"].(map[string]any)
	nextParams["approvalId"] = "next-approval"
	nextParams["currentRequirementId"] = map[string]any{"approvalId": "next-approval", "sourceIndex": 0}
	next := []byte(mustMuseChoiceJSON(t, nextFrame))
	nextKey := testApprovalKey("session", "next-approval", 0)
	a.HandleOutput(first)
	entered := false
	sink.onCancel = func(key string) {
		if key != firstKey || !museControlCallbackCanReenter(t, a) {
			return
		}
		a.HandleOutput(next)
		entered = true
	}
	a.HandleOutput(museApprovalSettlement(t, 0))
	assert.True(t, entered, "the cancellation callback must execute the actual public request path")
	assert.Equal(t, nextKey, a.sessions["session"].controls["next-approval"].key)
	require.Len(t, sink.pending, 1)
	assert.Equal(t, next, []byte(sink.pending[nextKey].Payload))
	assert.Equal(t, []string{firstKey}, sink.canceled)
}

func TestMuseControlPublicationRetainsFailedBytesBeforeExactReplay(t *testing.T) {
	a, sink := museControlPublicationAgent(t)
	firstKey, first := museApprovalStageRequest(t, 0)
	nextKey, next := museApprovalStageRequest(t, 1)
	originalFirst := slices.Clone(first)
	sink.err = errors.New("the native control store refuses the first write")
	a.HandleOutput(first)
	assert.Empty(t, sink.requests)
	assert.Empty(t, a.sessions["session"].controls)
	first[0] = '['
	sink.err = nil
	a.HandleOutput(next)
	require.Len(t, sink.requests, 2)
	assert.Equal(t, originalFirst, []byte(sink.requests[0].Payload))
	assert.Equal(t, next, []byte(sink.requests[1].Payload))
	assert.Equal(t, nextKey, a.sessions["session"].controls["approval"].key)
	assert.Equal(t, []string{firstKey}, sink.canceled)
	require.Len(t, sink.pending, 1)
	assert.Equal(t, next, []byte(sink.pending[nextKey].Payload))
	a.HandleOutput(slices.Clone(next))
	assert.Equal(t, 3, sink.attempts)
	assert.Len(t, sink.requests, 2)
	for _, kind := range []string{"approval", "question"} {
		t.Run("settlement retry/"+kind, func(t *testing.T) {
			a, sink := museControlPublicationAgent(t)
			var key string
			var request, settlement []byte
			if kind == "approval" {
				key, request = museApprovalStageRequest(t, 0)
				settlement = museApprovalSettlement(t, 0)
			} else {
				key, request = questionRequest(t, []any{museNativeQuestion("native", "single")})
				settlement = []byte(mustMuseChoiceJSON(t, map[string]any{"method": contracts.MuseMethodUserInputSettled, "params": map[string]any{"sessionId": "session", "userInputId": "question", "viewCursor": "question-settled", "outcome": "answered", "answers": []any{}, "futureNativeValue": "界\n"}}))
			}
			a.HandleOutput(request)
			original := slices.Clone(settlement)
			sink.notificationErr = errors.New("the native settlement store refuses the first write")
			a.HandleOutput(settlement)
			assert.Empty(t, sink.pending)
			assert.Equal(t, []string{key}, sink.canceled)
			require.Len(t, sink.notificationAttempts, 1)
			assert.Equal(t, original, sink.notificationAttempts[0])
			assert.Empty(t, sink.PersistedNotifications())
			settlement[0] = '['
			sink.notificationErr = nil
			var next map[string]any
			require.NoError(t, json.Unmarshal(approvalRequest("session", 0), &next))
			nextParams := next["params"].(map[string]any)
			nextParams["approvalId"] = "next-approval"
			nextParams["currentRequirementId"] = map[string]any{"approvalId": "next-approval", "sourceIndex": 0}
			nextRaw := []byte(mustMuseChoiceJSON(t, next))
			nextKey := testApprovalKey("session", "next-approval", 0)
			a.HandleOutput(nextRaw)
			require.Len(t, sink.notificationAttempts, 2)
			assert.Equal(t, original, sink.notificationAttempts[1])
			notifications := sink.PersistedNotifications()
			require.Len(t, notifications, 1)
			assert.Equal(t, original, notifications[0].Content)
			assert.Equal(t, []string{key}, sink.canceled)
			require.Len(t, sink.pending, 1)
			assert.Equal(t, nextRaw, []byte(sink.pending[nextKey].Payload))
			a.HandleOutput(slices.Clone(original))
			assert.Equal(t, []string{key}, sink.canceled)
			assert.Len(t, sink.PersistedNotifications(), 1)
		})
	}

}

func TestMuseControlPublicationKeepsRootAndChildAuthoritySeparate(t *testing.T) {
	a, rootSink := museControlPublicationAgent(t)
	childSink := &museControlPublicationSink{Sink: &agenttest.Sink{}}
	childState := &sessionState{sink: agent.NewProviderServices(childSink), childID: "child-agent", items: make(map[string]*itemState), completed: make(map[string]bool), log: newNativeLog("child-session")}
	a.sessions["child-session"] = childState
	rootKey, rootRequest := questionRequest(t, []any{museNativeQuestion("native", "single")})
	var childRequest map[string]any
	require.NoError(t, json.Unmarshal(rootRequest, &childRequest))
	childRequest["params"].(map[string]any)["sessionId"] = "child-session"
	childRaw := []byte(mustMuseChoiceJSON(t, childRequest))
	childKey, err := controlID(controlParams{SessionID: "child-session", UserInputID: "question"})
	require.NoError(t, err)
	a.HandleOutput(rootRequest)
	entered := false
	childSink.onPublish = func(agent.ControlRequest) {
		if !museControlCallbackCanReenter(t, a) {
			return
		}
		a.retireHost(agent.MessageCompletionInterrupted)
		entered = true
	}
	a.HandleOutput(childRaw)
	assert.True(t, entered)
	assert.Empty(t, a.sessions["session"].controls)
	assert.Empty(t, childState.controls)
	assert.Empty(t, rootSink.pending)
	assert.Empty(t, childSink.pending)
	assert.Equal(t, []string{rootKey}, rootSink.canceled)
	assert.Equal(t, []string{childKey}, childSink.canceled)
	require.Len(t, rootSink.requests, 1)
	require.Len(t, childSink.requests, 1)
	assert.Equal(t, rootRequest, []byte(rootSink.requests[0].Payload))
	assert.Equal(t, childRaw, []byte(childSink.requests[0].Payload))
}

func TestMuseApprovalSettlementRejectsMalformedStageEvidence(t *testing.T) {
	stage := func(requirement any) map[string]any {
		return map[string]any{"requirementId": requirement, "position": 1, "totalStages": 2, "argv": []any{}, "resolution": map[string]any{"kind": "unresolved"}}
	}
	type evidenceCase struct {
		label        string
		value        any
		present      bool
		decision     any
		omitDecision bool
		mutateParams func(map[string]any)
	}
	cases := []evidenceCase{
		{label: "absent evidence", decision: contracts.MuseApprovalDecisionAbort},
		{label: "null evidence", present: true, decision: contracts.MuseApprovalDecisionAbort},
		{label: "numeric evidence", value: 0, present: true, decision: contracts.MuseApprovalDecisionAbort},
		{label: "Boolean evidence", value: false, present: true, decision: contracts.MuseApprovalDecisionAbort},
		{label: "object evidence", value: map[string]any{}, present: true, decision: contracts.MuseApprovalDecisionAbort},
		{label: "null stage", value: []any{nil}, present: true, decision: contracts.MuseApprovalDecisionAbort},
		{label: "numeric stage", value: []any{0}, present: true, decision: contracts.MuseApprovalDecisionAbort},
		{label: "null requirement", value: []any{stage(nil)}, present: true, decision: contracts.MuseApprovalDecisionAbort},
		{label: "numeric requirement", value: []any{stage(0)}, present: true, decision: contracts.MuseApprovalDecisionAbort},
	}
	for _, decision := range []any{nil, false, 0, []any{}, map[string]any{}, "", " \t\n"} {
		cases = append(cases, evidenceCase{
			label: "decision/" + string(mustMuseChoiceJSON(t, decision)),
			value: []any{}, present: true, decision: decision,
		})
	}
	cases = append(cases, evidenceCase{
		label: "absent decision", value: []any{}, present: true, omitDecision: true,
	})
	for _, identity := range []struct {
		label  string
		field  string
		value  any
		absent bool
	}{
		{label: "absent session", field: "sessionId", absent: true},
		{label: "null session", field: "sessionId"},
		{label: "numeric session", field: "sessionId", value: 0},
		{label: "empty session", field: "sessionId", value: ""},
		{label: "foreign session", field: "sessionId", value: "foreign-session"},
		{label: "absent approval", field: "approvalId", absent: true},
		{label: "null approval", field: "approvalId"},
		{label: "numeric approval", field: "approvalId", value: 0},
		{label: "empty approval", field: "approvalId", value: ""},
		{label: "foreign approval", field: "approvalId", value: "foreign-approval"},
	} {
		identity := identity
		cases = append(cases, evidenceCase{
			label: identity.label, value: []any{}, present: true,
			decision: contracts.MuseApprovalDecisionAbort,
			mutateParams: func(params map[string]any) {
				if identity.absent {
					delete(params, identity.field)
					return
				}
				params[identity.field] = identity.value
			},
		})
	}
	for _, requirement := range []any{
		map[string]any{}, map[string]any{"approvalId": "approval"},
		map[string]any{"sourceIndex": 0}, map[string]any{"approvalId": nil, "sourceIndex": 0},
		map[string]any{"approvalId": " ", "sourceIndex": 0}, map[string]any{"approvalId": "foreign", "sourceIndex": 0},
		map[string]any{"approvalId": "approval", "sourceIndex": nil},
		map[string]any{"approvalId": "approval", "sourceIndex": -1},
		map[string]any{"approvalId": "approval", "sourceIndex": "0"},
		map[string]any{"approvalId": "approval", "sourceIndex": 0.5},
	} {
		cases = append(cases, evidenceCase{
			label: "requirement/" + string(mustMuseChoiceJSON(t, requirement)),
			value: []any{stage(requirement)}, present: true, decision: contracts.MuseApprovalDecisionAbort,
		})
	}
	for _, field := range []string{"argv", "position", "requirementId", "resolution", "totalStages"} {
		for _, supplied := range []struct {
			label   string
			present bool
			value   any
		}{
			{label: "absent"}, {label: "null", present: true}, {label: "wrong type", present: true, value: "wrong"},
		} {
			invalid := stage(map[string]any{"approvalId": "approval", "sourceIndex": 0})
			if supplied.present {
				invalid[field] = supplied.value
			} else {
				delete(invalid, field)
			}
			cases = append(cases, evidenceCase{label: field + "/" + supplied.label, value: []any{invalid}, present: true, decision: contracts.MuseApprovalDecisionAbort})
		}
	}
	for _, values := range []any{[]any{nil}, []any{false}, []any{0}, []any{[]any{}}, []any{map[string]any{}}} {
		invalid := stage(map[string]any{"approvalId": "approval", "sourceIndex": 0})
		invalid["argv"] = values
		cases = append(cases, evidenceCase{label: "argv/" + string(mustMuseChoiceJSON(t, values)), value: []any{invalid}, present: true, decision: contracts.MuseApprovalDecisionAbort})
	}
	for _, resolution := range []any{
		map[string]any{}, map[string]any{"kind": nil}, map[string]any{"kind": false},
		map[string]any{"kind": 0}, map[string]any{"kind": []any{}}, map[string]any{"kind": map[string]any{}},
	} {
		invalid := stage(map[string]any{"approvalId": "approval", "sourceIndex": 0})
		invalid["resolution"] = resolution
		cases = append(cases, evidenceCase{label: "resolution/" + string(mustMuseChoiceJSON(t, resolution)), value: []any{invalid}, present: true, decision: contracts.MuseApprovalDecisionAbort})
	}
	valid := stage(map[string]any{"approvalId": "approval", "sourceIndex": 0})
	cases = append(cases,
		evidenceCase{label: "duplicate requirement", value: []any{valid, valid}, present: true, decision: contracts.MuseApprovalDecisionAbort},
		evidenceCase{label: "malformed later sibling", value: []any{valid, nil}, present: true, decision: contracts.MuseApprovalDecisionAbort},
	)
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			a, sink := museControlPublicationAgent(t)
			key, request := museApprovalStageRequest(t, 0)
			a.HandleOutput(request)
			params := map[string]any{"sessionId": "session", "approvalId": "approval", "decision": tc.decision}
			if tc.omitDecision {
				delete(params, "decision")
			}
			if tc.mutateParams != nil {
				tc.mutateParams(params)
			}
			if tc.present {
				params["stageEvidence"] = tc.value
			}
			settlement := []byte(mustMuseChoiceJSON(t, map[string]any{"method": contracts.MuseMethodApprovalResolved, "params": params}))
			original := slices.Clone(settlement)
			a.HandleOutput(settlement)
			assert.Equal(t, key, a.sessions["session"].controls["approval"].key)
			require.Len(t, sink.pending, 1)
			assert.Equal(t, request, []byte(sink.pending[key].Payload))
			assert.Empty(t, sink.canceled)
			assert.Empty(t, sink.PersistedNotifications())
			assert.Equal(t, original, settlement)
		})
	}
}

func TestMuseReplacementCancellationPanicKeepsSuccessfulPublication(t *testing.T) {
	a, sink := museControlPublicationAgent(t)
	firstKey, first := museApprovalStageRequest(t, 0)
	nextKey, next := museApprovalStageRequest(t, 1)
	original := slices.Clone(next)
	a.HandleOutput(first)
	sink.onCancel = func(key string) {
		assert.Equal(t, firstKey, key)
		assert.True(t, museControlCallbackCanReenter(t, a))
		panic("the replacement cancellation observer panics")
	}
	require.PanicsWithValue(t, "the replacement cancellation observer panics", func() { a.HandleOutput(next) })
	assert.Equal(t, nextKey, a.sessions["session"].controls["approval"].key)
	require.Len(t, sink.requests, 2)
	assert.Equal(t, []string{firstKey}, sink.canceled)
	sink.onCancel = nil
	a.HandleOutput(slices.Clone(next))
	assert.Len(t, sink.requests, 2, "the drain must not repeat a successful publication")
	assert.Equal(t, 2, sink.attempts)
	assert.Equal(t, []string{firstKey}, sink.canceled)
	require.Len(t, sink.pending, 1)
	assert.Equal(t, original, []byte(sink.pending[nextKey].Payload))
	assert.Equal(t, original, next)
}

func TestMuseRetirementCancellationPanicKeepsCompletedCancellation(t *testing.T) {
	a, sink := museControlPublicationAgent(t)
	key, request := museApprovalStageRequest(t, 0)
	a.HandleOutput(request)
	sink.onCancel = func(actual string) {
		assert.Equal(t, key, actual)
		assert.True(t, museControlCallbackCanReenter(t, a))
		panic("the retirement cancellation observer panics")
	}
	require.PanicsWithValue(t, "the retirement cancellation observer panics", func() { a.retireHost(agent.MessageCompletionInterrupted) })
	assert.True(t, a.sessions["session"].retired)
	assert.Empty(t, a.sessions["session"].controls)
	assert.Equal(t, []string{key}, sink.canceled)
	assert.Empty(t, sink.pending)
	sink.onCancel = nil
	a.HandleOutput(slices.Clone(request))
	assert.Equal(t, []string{key}, sink.canceled, "the drain must not repeat a completed cancellation")
	assert.Len(t, sink.requests, 1)
	assert.Empty(t, sink.pending)
}
