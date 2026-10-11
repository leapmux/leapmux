package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

type reentrantTranscriptProvider struct {
	agent.Provider
	stage          string
	armed          atomic.Bool
	reenter        func()
	pairedObserved func([]byte)
}

func stopCapturedProgressTimers(publisher *generationProgressPublisher) {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if publisher.timer != nil {
		publisher.timer.Stop()
		publisher.timer = nil
	}
	if publisher.tailTimer != nil {
		publisher.tailTimer.Stop()
		publisher.tailTimer = nil
	}
}

func capturedTailSessionInfo(event *leapmuxv1.AgentEvent) (map[string]json.RawMessage, error) {
	message := event.GetAgentMessage()
	if message == nil || message.Seq >= 0 {
		return nil, nil
	}
	content, err := msgcodec.Decompress(message.Content, message.ContentCompression)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Info map[string]json.RawMessage `json:"info"`
	}
	if err := json.Unmarshal(content, &envelope); err != nil {
		return nil, err
	}
	return envelope.Info, nil
}

func TestCapturedAdmissionRejectsIncompleteOwnersBeforeEffects(t *testing.T) {
	t.Parallel()
	for _, missing := range []string{"owner", "sink", "activity", "scope", "session", "thread"} {
		for _, operation := range []string{"message", "divider", "notification", "handler message", "handler notification", "enrichment"} {
			t.Run(missing+"/"+operation, func(t *testing.T) {
				t.Parallel()
				svc, services := setupRootSink(t, "incomplete-owner-agent")
				services.UpdateSessionID("native-session")
				const agentID = "incomplete-owner-agent"
				original := []byte(`{"native":"unchanged"}`)
				span := agent.SpanInfo{SpanID: "same-call"}
				if operation == "enrichment" {
					require.NoError(t, services.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: original}, span))
				}
				output := requireRootOutputSink(t, svc.Output, agentID)
				services.ReportProgress(agent.NativeTokenProgress("current-model", 17))
				stopCapturedProgressTimers(output.progress)
				beforeProgress := output.progress.snapshotInfo()
				beforeRows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
				require.NoError(t, err)
				beforeAgent, err := svc.Queries.GetAgentByID(t.Context(), agentID)
				require.NoError(t, err)
				beforeTodos, err := svc.Output.LoadTodos(t.Context(), agentID)
				require.NoError(t, err)
				writer := &testResponseWriter{channelID: "incomplete-owner-wire"}
				registerAgentWatch(svc, writer.channelID, agentID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
				receipt := agent.NewTranscriptWriteReceipt()
				content := services.CaptureMessage(agent.MessageContent{Original: original, IdempotencyKey: "native-key", WriteReceipt: receipt}, span)
				validOwner := content.Publication.Owner().(*transcriptOwner)
				copy := *validOwner
				owner := &copy
				switch missing {
				case "owner":
					owner = nil
				case "sink":
					owner.sink = nil
				case "activity":
					owner.activity = nil
				case "scope":
					owner.scope = nil
				case "session":
					owner.session = nil
				case "thread":
					owner.thread = nil
				}
				content.Publication = &transcriptPublication{owner: owner}
				enrichmentReceipt := agent.NewMessageEnrichmentReceipt()
				var callErr error
				var accepted bool
				noPanic := assert.NotPanics(t, func() {
					switch operation {
					case "message":
						callErr = services.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span)
					case "divider":
						callErr = services.PersistTurnEnd(content, span)
					case "notification":
						accepted, callErr = services.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content)
					case "handler message":
						callErr = svc.Output.persistAndBroadcast(agentID, output.agentProvider, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span, output.tracker)
					case "handler notification":
						accepted, callErr = svc.Output.persistNotificationThreaded(agentID, output.agentProvider, output.plugin, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content)
					case "enrichment":
						accepted, callErr = services.EnrichMessage(agent.MessageEnrichment{Publication: content.Publication, AgentSessionID: content.AgentSessionID, SpanID: span.SpanID, OriginalContent: original, SupplementalContent: []byte(`{"new":true}`), WriteReceipt: enrichmentReceipt})
					}
				})
				assert.ErrorContains(t, callErr, "owner")
				assert.False(t, accepted)
				afterRows, queryErr := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
				require.NoError(t, queryErr)
				assert.Equal(t, beforeRows, afterRows)
				afterAgent, queryErr := svc.Queries.GetAgentByID(t.Context(), agentID)
				require.NoError(t, queryErr)
				assert.Equal(t, beforeAgent.MessageSeqHwm, afterAgent.MessageSeqHwm)
				afterTodos, queryErr := svc.Output.LoadTodos(t.Context(), agentID)
				require.NoError(t, queryErr)
				assert.Equal(t, beforeTodos, afterTodos)
				assert.Equal(t, beforeProgress, output.progress.snapshotInfo())
				assert.Empty(t, writer.streamsSnapshot())
				_, stored := receipt.StoredMessageSequence()
				assert.False(t, stored)
				assert.False(t, receipt.ClaimModelReset())
				assert.False(t, receipt.ClaimSourceObservation())
				assert.False(t, receipt.ClaimContextUsage())
				assert.False(t, receipt.ClaimOutputCompletion())
				_, _, _, enriched := enrichmentReceipt.CommittedEnrichment()
				assert.False(t, enriched)
				if noPanic && callErr != nil {
					content.Publication = &transcriptPublication{owner: validOwner}
					content.WriteReceipt = agent.NewTranscriptWriteReceipt()
					require.NoError(t, services.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span))
					rows, queryErr := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID})
					require.NoError(t, queryErr)
					assert.Len(t, rows, len(beforeRows)+1, "refusal must not claim the destination's native key")
				}
			})
		}
	}
}

func TestCapturedTailRetainsItsOwnerThroughDelayedWireDelivery(t *testing.T) {
	t.Parallel()
	for _, population := range []string{"root", "child"} {
		for _, change := range []string{"current owner", "sink replacement", "session change", "session A-B-A", "turn scope change"} {
			t.Run(population+" "+change, func(t *testing.T) {
				t.Parallel()
				ctx := testutil.DeadlineContext(t)
				const rootID = "captured-tail-root"
				svc, rootServices := setupRootSink(t, rootID)
				svc.Output.processRunning = func(string) bool { return true }
				subjectID := rootID
				services := rootServices
				if population == "child" {
					childID, err := rootServices.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "spawn", ProviderChildKey: "tail-child", Title: "Child"})
					require.NoError(t, err)
					subjectID = childID
					services = rootServices.ChildSink(childID)
				}
				services.UpdateSessionID("A")
				services.SetTurnState(agent.TurnState{Active: true}, 1)
				subject := svc.Output.sinkForAgent(subjectID)
				require.NotNil(t, subject)
				publisher := subject.progress
				originalSession := subject.currentMessageSessionFact()
				beforeRows, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: subjectID})
				require.NoError(t, err)
				beforeAgent, err := svc.Queries.GetAgentByID(ctx, subjectID)
				require.NoError(t, err)
				counterEntered, releaseCounter := make(chan struct{}), make(chan struct{})
				tailEntered, releaseTail := make(chan string, 1), make(chan struct{})
				currentCounter, currentTail := make(chan struct{}), make(chan struct{})
				var counterOnce, tailOnce, currentCounterOnce, currentTailOnce sync.Once
				var releaseCounterOnce, releaseTailOnce sync.Once
				finishCounter := func() { releaseCounterOnce.Do(func() { close(releaseCounter) }) }
				finishTail := func() { releaseTailOnce.Do(func() { close(releaseTail) }) }
				defer finishCounter()
				defer finishTail()
				writer := &turnAdmissionWatchingWriter{testResponseWriter: &testResponseWriter{channelID: "captured-tail-wire"}}
				writer.onEvent = func(event *leapmuxv1.AgentEvent) {
					if event.AgentId != subjectID {
						return
					}
					info, decodeErr := capturedTailSessionInfo(event)
					if !assert.NoError(t, decodeErr) {
						return
					}
					if raw, exists := info[contracts.SessionInfoKeyThinkingTokens]; exists {
						var value int64
						if !assert.NoError(t, json.Unmarshal(raw, &value)) {
							return
						}
						if value == 17 {
							counterOnce.Do(func() { close(counterEntered); <-releaseCounter })
						}
						if value == 23 {
							currentCounterOnce.Do(func() { close(currentCounter) })
						}
					}
					if raw, exists := info[contracts.SessionInfoKeyRunningTool]; exists {
						var running map[string]json.RawMessage
						if !assert.NoError(t, json.Unmarshal(raw, &running)) {
							return
						}
						var spanID string
						if !assert.NoError(t, json.Unmarshal(running[contracts.RunningToolFieldSpanId], &spanID)) {
							return
						}
						if spanID == "current-tail" {
							currentTailOnce.Do(func() { close(currentTail) })
							return
						}
						tailOnce.Do(func() { tailEntered <- spanID; <-releaseTail })
					}
				}
				registerAgentWatch(svc, writer.channelID, subjectID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
				services.ReportProgress(agent.NativeTokenProgress("model", 17))
				stopCapturedProgressTimers(publisher)
				publisher.flush()
				select {
				case <-counterEntered:
				case <-ctx.Done():
					t.Fatal("the actual counter frame did not reach its held sender")
				}
				captured := services.CaptureMessage(agent.MessageContent{Original: []byte(`{"native":"tail observation"}`)}, agent.SpanInfo{SpanID: "captured-span"})
				require.NotNil(t, captured.Publication)
				captured.Publication.ReportProgress(agent.OutputTailProgress("first-span", "first partial output", false))
				captured.Publication.ReportProgress(agent.OutputTailProgress("second-span", "second partial output", true))
				stopCapturedProgressTimers(publisher)
				publisher.flushTails()
				publisher.mu.Lock()
				queued := len(publisher.tailQueue)
				publisher.mu.Unlock()
				require.Equal(t, 2, queued, "both actual owned observations must enter the held publisher queue")
				finishCounter()
				var firstSpan string
				select {
				case firstSpan = <-tailEntered:
				case <-ctx.Done():
					t.Fatal("the first actual tail frame did not reach its held sender")
				}
				require.Contains(t, []string{"first-span", "second-span"}, firstSpan)
				switch change {
				case "sink replacement":
					rootServices = svc.Output.NewSink(rootID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
					services = rootServices
					if population == "child" {
						services = rootServices.ChildSink(subjectID)
					}
					services.UpdateSessionID("A")
					services.SetTurnState(agent.TurnState{Active: true}, 1)
				case "session change":
					services.UpdateSessionID("B")
				case "session A-B-A":
					services.UpdateSessionID("B")
					services.UpdateSessionID("A")
				case "turn scope change":
					services.SetTurnState(agent.TurnState{}, 2)
					services.SetTurnState(agent.TurnState{Active: true}, 3)
				case "current owner":
				}
				current := svc.Output.sinkForAgent(subjectID)
				require.NotNil(t, current)
				if change == "sink replacement" {
					assert.NotSame(t, subject, current)
				}
				if change == "session A-B-A" {
					assert.Equal(t, "A", current.currentMessageSessionID())
					assert.NotSame(t, originalSession, current.currentMessageSessionFact())
				}
				if change != "sink replacement" {
					latest := services.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
					latest.Publication.ReportProgress(agent.NativeTokenProgress("model", 23))
					stopCapturedProgressTimers(current.progress)
					current.progress.flush()
				}
				finishTail()
				if change == "sink replacement" {
					select {
					case <-publisher.done:
					case <-ctx.Done():
						t.Fatal("the retired publisher did not finish after its held send released")
					}
					latest := services.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
					latest.Publication.ReportProgress(agent.NativeTokenProgress("model", 23))
					stopCapturedProgressTimers(current.progress)
					current.progress.flush()
				}
				select {
				case <-currentCounter:
				case <-ctx.Done():
					t.Fatal("the exact current-owner counter did not complete its wire barrier")
				}
				latest := services.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
				latest.Publication.ReportProgress(agent.OutputTailProgress("current-tail", "current partial output", false))
				stopCapturedProgressTimers(current.progress)
				current.progress.flushTails()
				select {
				case <-currentTail:
				case <-ctx.Done():
					t.Fatal("the current owner's positive tail control did not reach the wire")
				}
				tails := make(map[string]int)
				for _, stream := range writer.streamsSnapshot() {
					event := decodeWatchAgentEvent(t, stream)
					if event.AgentId != subjectID {
						continue
					}
					info, decodeErr := capturedTailSessionInfo(event)
					require.NoError(t, decodeErr)
					if raw, exists := info[contracts.SessionInfoKeyRunningTool]; exists {
						var running map[string]json.RawMessage
						require.NoError(t, json.Unmarshal(raw, &running))
						var spanID, sessionID, text string
						var truncated bool
						require.NoError(t, json.Unmarshal(running[contracts.RunningToolFieldSpanId], &spanID))
						require.NoError(t, json.Unmarshal(running[contracts.RunningToolFieldAgentSessionId], &sessionID))
						require.NoError(t, json.Unmarshal(running[contracts.RunningToolFieldOutputTail], &text))
						require.NoError(t, json.Unmarshal(running[contracts.RunningToolFieldOutputTruncated], &truncated))
						t.Logf("received span=%s session=%s tail=%q truncated=%v", spanID, sessionID, text, truncated)
						tails[spanID]++
						if spanID == "current-tail" {
							assert.Equal(t, current.currentMessageSessionID(), sessionID)
						} else {
							assert.Equal(t, "A", sessionID)
						}
						if spanID == "first-span" {
							assert.Equal(t, "first partial output", text)
							assert.False(t, truncated)
						}
						if spanID == "second-span" {
							assert.Equal(t, "second partial output", text)
							assert.True(t, truncated)
						}
					}
				}
				assert.Equal(t, 1, tails[firstSpan], "the first tail already passed publication admission")
				otherSpan := "first-span"
				if firstSpan == otherSpan {
					otherSpan = "second-span"
				}
				if change == "current owner" {
					assert.Equal(t, 1, tails[otherSpan])
				} else {
					assert.Zero(t, tails[otherSpan], "the later retained tail must refuse after its owner retires")
				}
				assert.Equal(t, 1, tails["current-tail"])
				assert.Equal(t, int64(23), current.progress.snapshotInfo()[contracts.SessionInfoKeyThinkingTokens])
				afterRows, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: subjectID})
				require.NoError(t, err)
				assert.Equal(t, beforeRows, afterRows)
				afterAgent, err := svc.Queries.GetAgentByID(ctx, subjectID)
				require.NoError(t, err)
				assert.Equal(t, beforeAgent.MessageSeqHwm, afterAgent.MessageSeqHwm)
			})
		}
	}
}

func TestStoredMessageSequenceReceiptPrecedesWatcherPublication(t *testing.T) {
	t.Parallel()
	for _, todo := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary row", true: "to-do row"}[todo], func(t *testing.T) {
			t.Parallel()
			providerID := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
			provider := &reentrantTranscriptProvider{Provider: testRegistry.Plugin(providerID), stage: "extract", reenter: func() {}}
			provider.armed.Store(todo)
			svc, _, _ := setupTestService(t, withRegistry(registryWithPlugin(t, providerID, provider)))
			const ownerID = "sequence-receipt-owner"
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{ID: ownerID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: providerID}))
			sink := svc.Output.NewSink(ownerID, providerID)
			receipt := agent.NewTranscriptWriteReceipt()
			var inspected int
			writer := &turnAdmissionWatchingWriter{testResponseWriter: &testResponseWriter{channelID: "sequence-receipt-watch"}}
			writer.onEvent = func(event *leapmuxv1.AgentEvent) {
				if message := event.GetAgentMessage(); message != nil {
					inspected++
					sequence, committed := receipt.StoredMessageSequence()
					assert.True(t, committed)
					assert.Equal(t, message.Seq, sequence)
				}
			}
			registerAgentWatch(svc, writer.channelID, ownerID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
			require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
				agent.MessageContent{Original: []byte(`{"native":"stored"}`), WriteReceipt: receipt}, agent.SpanInfo{SpanID: "call"}))
			assert.Equal(t, 1, inspected)
			row, err := svc.Queries.GetLatestMessageByAgentID(t.Context(), ownerID)
			require.NoError(t, err)
			sequence, committed := receipt.StoredMessageSequence()
			assert.True(t, committed)
			assert.Equal(t, row.Seq, sequence)
		})
	}
}

func (provider *reentrantTranscriptProvider) ResolveProviderData(content agent.MessageContent) []byte {
	if provider.armed.Load() && (provider.stage == "resolve" ||
		provider.stage == "paired" && strings.Contains(string(content.Original), "paired-request")) {
		provider.reenter()
	}
	return provider.Provider.ResolveProviderData(content)
}

func (provider *reentrantTranscriptProvider) ExtractTodoEvent(_ string, _ []byte, paired func() []byte) (todoevents.Event, bool) {
	if !provider.armed.Load() {
		return todoevents.Event{}, false
	}
	if provider.stage == "extract" {
		provider.reenter()
	}
	if provider.stage == "paired" {
		paired()
	}
	if provider.pairedObserved != nil {
		provider.pairedObserved(paired())
		provider.pairedObserved(paired())
	}
	return todoevents.Event{Kind: todoevents.KindCreate, Item: todoevents.Item{ID: "old-task", Content: "Original task", Status: todoevents.StatusPending}}, true
}

func TestCapturedProviderPreparationKeepsOriginalPairedSession(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"message", "enrichment"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			providerID := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
			provider := &reentrantTranscriptProvider{Provider: testRegistry.Plugin(providerID), stage: "resolve"}
			svc, _, _ := setupTestService(t, withRegistry(registryWithPlugin(t, providerID, provider)))
			const ownerID = "paired-session-owner"
			require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{ID: ownerID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: providerID}))
			sink := svc.Output.NewSink(ownerID, providerID)
			sink.UpdateSessionID("session-s")
			sink.SetTurnState(agent.TurnState{Active: true}, 1)
			requestSpan := agent.SpanInfo{SpanID: "reused-native-span"}
			require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
				agent.MessageContent{Original: []byte(`{"request_owner":"session-s"}`)}, requestSpan))
			require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
				agent.MessageContent{Original: []byte(`{"request_owner":"session-t"}`), AgentSessionID: "session-t"}, requestSpan))
			span := agent.SpanInfo{SpanID: requestSpan.SpanID, Closing: true}
			content := sink.CaptureMessage(agent.MessageContent{Original: []byte(`{"native_result":"session-s"}`)}, span)
			if operation == "enrichment" {
				require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span))
			}
			counter := &countingDBTX{DBTX: svc.DB}
			svc.Output.queries = db.New(counter)
			observed := make(chan []byte, 8)
			provider.pairedObserved = func(original []byte) { observed <- append([]byte(nil), original...) }
			callbackResult := make(chan error, 1)
			sessionChanged := make(chan struct{})
			var once sync.Once
			provider.reenter = func() {
				invoke := false
				once.Do(func() { invoke = true })
				if !invoke {
					return
				}
				go func() { sink.UpdateSessionID("session-t"); close(sessionChanged) }()
				select {
				case <-sessionChanged:
					callbackResult <- nil
				case <-time.After(30 * time.Second):
					callbackResult <- errors.New("provider preparation holds the root lease across a session change")
				}
			}
			provider.armed.Store(true)
			if operation == "enrichment" {
				written, err := sink.EnrichMessage(agent.MessageEnrichment{Publication: content.Publication, AgentSessionID: content.AgentSessionID,
					SpanID: span.SpanID, OriginalContent: content.Original, SupplementalContent: []byte(`{"native_enrichment":true}`)})
				require.NoError(t, err)
				require.True(t, written)
			} else {
				require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span))
			}
			select {
			case <-sessionChanged:
			case <-ctx.Done():
				t.Fatal("the original provider session change did not finish after preparation")
			}
			assert.NoError(t, <-callbackResult)
			var pairedReads int
			for len(observed) > 0 {
				assert.JSONEq(t, `{"request_owner":"session-s"}`, string(<-observed))
				pairedReads++
			}
			assert.GreaterOrEqual(t, pairedReads, 2, "the provider must receive the original request on each paired lookup")
			assert.Equal(t, 1, counter.count("GetAgentMessageBySpanIDAndSource"), "sync.OnceValue must retain one exact original request read")
		})
	}
}

func TestCapturedProviderPreparationCanReplaceItsPublisher(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"message", "divider"} {
		for _, stage := range []string{"resolve", "extract", "paired"} {
			t.Run(operation+" "+stage, func(t *testing.T) {
				t.Parallel()
				ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
				defer cancel()
				providerID := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
				provider := &reentrantTranscriptProvider{Provider: testRegistry.Plugin(providerID), stage: stage}
				svc, _, _ := setupTestService(t, withRegistry(registryWithPlugin(t, providerID, provider)))
				const ownerID = "provider-preparation-owner"
				require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{ID: ownerID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: providerID}))
				sink := svc.Output.NewSink(ownerID, providerID)
				sink.UpdateSessionID("original-session")
				sink.SetTurnState(agent.TurnState{Active: true}, 1)
				require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
					agent.MessageContent{Original: []byte(`{"text":"paired-request"}`)}, agent.SpanInfo{SpanID: "paired-span"}))
				span := agent.SpanInfo{SpanID: "paired-span", Closing: true}
				record := agent.CaptureTranscript(sink, agent.MessageContent{Original: []byte(`{"text":"retained native bytes"}`)}, span)
				callbackResult := make(chan error, 1)
				replacementFinished := make(chan struct{})
				var once sync.Once
				provider.reenter = func() {
					invoke := false
					once.Do(func() { invoke = true })
					if !invoke {
						return
					}
					go func() {
						replacement := svc.Output.NewSink(ownerID, providerID)
						svc.Output.NoteAgentProcessStarted(ownerID)
						replacement.SetTurnState(agent.TurnState{Active: true}, 1)
						close(replacementFinished)
					}()
					select {
					case <-replacementFinished:
						callbackResult <- nil
					case <-time.After(30 * time.Second):
						callbackResult <- errors.New("provider preparation holds the root mutation lease across replacement")
					}
				}
				provider.armed.Store(true)
				var err error
				if operation == "message" {
					err = record.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
				} else {
					err = record.PersistTurnEnd()
				}
				require.NoError(t, err)
				select {
				case <-replacementFinished:
				case <-ctx.Done():
					t.Fatal("the provider replacement did not finish after preparation returned")
				}
				assert.NoError(t, <-callbackResult)
				rows, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
				require.NoError(t, err)
				require.Len(t, rows, 2)
				assert.Equal(t, "original-session", rows[1].AgentSessionID)
				assert.True(t, rows[1].TranscriptOnly)
				todos, err := svc.Output.LoadTodos(ctx, ownerID)
				require.NoError(t, err)
				assert.Empty(t, todos, "a prepared historical event must not change current to-dos")
			})
		}
	}
}

func TestRejectedTranscriptPreservesModelProgress(t *testing.T) {
	t.Parallel()
	svc, inner, ownerID, _ := setupBgTaskTestWithService(t)
	sink := agent.NewModelProgressResetSink(inner)
	sink.ReportProgress(agent.NativeTokenProgress("current-model", 17))
	sink.ReportProgress(agent.OutputDeltaProgress("current-tool", 8))
	_, err := svc.DB.ExecContext(t.Context(), `
		CREATE TRIGGER refuse_captured_divider BEFORE INSERT ON messages
		BEGIN SELECT RAISE(FAIL, 'The store refused the divider'); END`)
	require.NoError(t, err)

	err = sink.PersistTurnEnd(agent.MessageContent{
		Original: []byte(`{"type":"result","num_tool_uses":4}`),
	}, agent.SpanInfo{})
	require.ErrorContains(t, err, "The store refused the divider")
	info := requireRootOutputSink(t, svc.Output, ownerID).progress.snapshotInfo()
	require.NotNil(t, info)
	assert.Equal(t, int64(17), info[contracts.SessionInfoKeyThinkingTokens],
		"A rejected divider must preserve the current model progress")
	assert.Equal(t, int64(8), info[contracts.SessionInfoKeyOutputBytes])
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestCapturedUnknownSessionDoesNotAdoptTheFirstKnownSession(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	record := agent.CaptureTranscript(sink, agent.MessageContent{Original: []byte(`{"type":"assistant"}`)}, agent.SpanInfo{})
	sink.UpdateSessionID("first-known-session")
	require.NoError(t, record.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Empty(t, rows[0].AgentSessionID)
}

func TestCapturedNotificationKeepsItsOriginalNativeSession(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	sink.UpdateSessionID("session-a")
	record := agent.CaptureTranscript(sink, agent.MessageContent{Original: []byte(`{"type":"system","subtype":"notice"}`), IdempotencyKey: "native:0"}, agent.SpanInfo{})
	sink.UpdateSessionID("session-b")
	_, err := record.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	require.NoError(t, err)
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "session-a", rows[0].AgentSessionID)
}

func TestCapturedOldTurnCannotPublishCompletionForReplacementTurn(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	writer := &testResponseWriter{channelID: "captured-old-completion"}
	registerAgentWatch(svc, writer.channelID, ownerID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	sink.SetTurnState(agent.TurnState{Active: true}, 1)
	record := agent.CaptureTranscript(sink, agent.MessageContent{Original: []byte(`{"type":"result","num_tool_uses":4}`)}, agent.SpanInfo{})
	sink.SetTurnState(agent.TurnState{}, 2)
	sink.SetTurnState(agent.TurnState{Active: true}, 3)
	require.NoError(t, record.PersistTurnEnd())
	var completions int
	for _, stream := range writer.streamsSnapshot() {
		if decodeWatchAgentEvent(t, stream).GetTurnEnd() != nil {
			completions++
		}
	}
	assert.Zero(t, completions)
	activity := svc.Output.activityFor(ownerID, ownerID)
	activity.mu.Lock()
	assert.Nil(t, activity.settledToolUses)
	activity.mu.Unlock()
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
	require.NoError(t, err)
	assert.Len(t, rows, 1)
}

func TestCapturedKeyReplayPreservesReplacementModelProgress(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"message", "divider"} {
		t.Run(operation, func(t *testing.T) {
			svc, delegate, ownerID, _ := setupBgTaskTestWithService(t)
			sink := agent.NewModelProgressResetSink(delegate)
			sink.UpdateSessionID("native-session")
			sink.SetTurnState(agent.TurnState{Active: true}, 1)
			content := agent.MessageContent{Original: []byte(`{"type":"result","num_tool_uses":4}`), IdempotencyKey: "native-key"}
			persist := func(record agent.CapturedTranscript) error {
				if operation == "divider" {
					return record.PersistTurnEnd()
				}
				return record.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
			}
			require.NoError(t, persist(agent.CaptureTranscript(sink, content, agent.SpanInfo{})))
			sink.SetTurnState(agent.TurnState{}, 2)
			sink.SetTurnState(agent.TurnState{Active: true}, 3)
			sink.ReportProgress(agent.NativeTokenProgress("replacement-model", 17))
			sink.ReportProgress(agent.OutputDeltaProgress("replacement-tool", 8))
			require.NoError(t, persist(agent.CaptureTranscript(sink, content, agent.SpanInfo{})))
			info := requireRootOutputSink(t, svc.Output, ownerID).progress.snapshotInfo()
			assert.Equal(t, int64(17), info[contracts.SessionInfoKeyThinkingTokens])
			assert.Equal(t, int64(8), info[contracts.SessionInfoKeyOutputBytes])
			rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
			require.NoError(t, err)
			assert.Len(t, rows, 1)
		})
	}
}

func TestNotificationEntryNativeKeyDoesNotRepeatAnAppend(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	sink.UpdateSessionID("session-a")
	first := agent.MessageContent{Original: []byte(`{"type":"system","subtype":"notice","message":"first"}`), IdempotencyKey: "notice:0"}
	second := agent.MessageContent{Original: []byte(`{"type":"system","subtype":"notice","message":"second"}`), IdempotencyKey: "notice:1"}
	for _, entry := range []agent.MessageContent{first, second, first, second} {
		_, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, entry)
		require.NoError(t, err)
	}
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	wrapper := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
	assert.Len(t, wrapper.Messages, 2)
	assert.Equal(t, int64(2), rows[0].Seq)
}

func TestNotificationEntryColdCurrentOwnerDoesNotRestoreAStaleAggregate(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	sink.UpdateSessionID("native-session")
	sink.SetTurnState(agent.TurnState{Active: true}, 1)
	old := agent.CaptureTranscript(sink, agent.MessageContent{
		Original: []byte(`{"type":"context_cleared"}`), IdempotencyKey: "stale-context-clear",
	}, agent.SpanInfo{})
	sink.SetTurnState(agent.TurnState{}, 2)
	sink.SetTurnState(agent.TurnState{Active: true}, 3)
	_, err := old.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX)
	require.NoError(t, err)
	svc.Output.lastNotifThread.Delete(ownerID)
	cold := svc.Output.NewSink(ownerID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	_, err = cold.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX,
		agent.MessageContent{Original: []byte(`{"type":"current_notice"}`), IdempotencyKey: "current-notice"})
	require.NoError(t, err)
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.True(t, rows[0].TranscriptOnly)
	assert.False(t, rows[1].TranscriptOnly)
	assert.Len(t, decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression).Messages, 1)
	assert.Len(t, decodeNotifWrapper(t, rows[1].Content, rows[1].ContentCompression).Messages, 1)
}
