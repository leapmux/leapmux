package fastagent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rootPreparationRefusalServices struct {
	agent.ProviderServices
}

func (services *rootPreparationRefusalServices) OutputQueue() *agent.OutputQueue {
	return agent.OutputQueueFor(services.ProviderServices)
}

func (*rootPreparationRefusalServices) TranscriptScopeFor(agent.CapturedTranscript) (agent.TranscriptScope, error) {
	return nil, errors.New("the transcript scope is unavailable")
}

func unpreparedRootFixture(t *testing.T, a *Agent, delegate agent.ProviderServices) *rootTranscript {
	t.Helper()
	root, ok := a.openRootTranscript(acp.RootTranscriptStart{
		SessionID: a.CurrentSessionID(), NativeModeID: "agent",
	}, delegate).(*rootTranscript)
	require.True(t, ok)
	return root
}

func TestFastagentRootPreparationFailureReleasesItsUnsentGroup(t *testing.T) {
	for _, failure := range []string{"scope refusal", "journal directory is a file"} {
		t.Run(failure, func(t *testing.T) {
			a, sink := newFastagentRootSubject(t)
			delegate := a.Sink()
			if failure == "scope refusal" {
				delegate = &rootPreparationRefusalServices{ProviderServices: delegate}
			} else {
				a.home = filepath.Join(t.TempDir(), "home-file")
				require.NoError(t, os.WriteFile(a.home, []byte("a regular file"), 0o600))
			}
			root := unpreparedRootFixture(t, a, delegate)
			require.Error(t, root.Prepare())

			// ACP finishes a claimed prompt even when preparation prevents its write.
			_ = root.Finish(acp.RootTranscriptCompletion{
				Reason: acp.RootTranscriptPromptNotSent, Completion: agent.MessageCompletionError,
			})
			a.rootArchiveWG.Wait()

			queue := agent.OutputQueueFor(delegate)
			require.NotNil(t, queue)
			require.NoError(t, queue.Flush())
			assert.False(t, queue.HasPending())
			assert.Empty(t, sink.Messages())
			require.NoError(t, delegate.PersistMessage(
				leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
				agent.MessageContent{Original: []byte(`{"live":"healthy after preparation refusal"}`)},
				agent.SpanInfo{},
			))
			rows := sink.Messages()
			require.Len(t, rows, 1)
			assert.JSONEq(t, `{"live":"healthy after preparation refusal"}`, string(rows[0].Content))
		})
	}
}

func TestFastagentPendingInputCanBeEnrichedBeforeRootPreparation(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	root := unpreparedRootFixture(t, a, a.Sink())
	position := root.Reserve()
	writer := position.Writer()
	overlay, ok := writer.(acp.PendingToolRequestWriter)
	require.True(t, ok)
	receipt := agent.NewTranscriptWriteReceipt()
	original := []byte(`{"request":"accepted before preparation"}`)
	require.NoError(t, writer.PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: original, WriteReceipt: receipt},
		agent.SpanInfo{SpanID: "pending-request", SpanType: "read"},
	))

	var updated bool
	var enrichErr error
	require.NotPanics(t, func() {
		updated, enrichErr = overlay.EnrichPendingToolRequest(
			receipt, 0, original, []byte(`{"path":"the complete late input"}`),
		)
	})
	require.NoError(t, enrichErr)
	require.True(t, updated)
	snapshot, found, err := overlay.ReadPendingToolRequest(receipt)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(1), snapshot.Revision)
	assert.Same(t, receipt, snapshot.Content.WriteReceipt)
	assert.JSONEq(t, `{"path":"the complete late input"}`, string(snapshot.Content.Supplemental))
	position.Complete()

	require.NoError(t, root.Prepare())
	require.NoError(t, root.Finish(acp.RootTranscriptCompletion{
		Reason: acp.RootTranscriptWorkEnded, Completion: agent.MessageCompletionComplete,
	}))
	a.rootArchiveWG.Wait()
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.JSONEq(t, string(original), string(rows[0].Content))
	assert.JSONEq(t, `{"path":"the complete late input"}`, string(rows[0].SupplementalContent))
	_, committed := receipt.StoredMessageSequence()
	assert.True(t, committed)
}

func TestFastagentDiscardedRootWithUnprovedInputPublishesNoWarning(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	root := unpreparedRootFixture(t, a, a.Sink())
	require.NoError(t, root.Prepare())
	// Native slash commands convert embedded resources beyond the proof mapper.
	// An unproved candidate normally chooses complete live fallback.
	require.NoError(t, root.PrepareInput([]json.RawMessage{
		json.RawMessage(`{"type":"text","text":"/model"}`),
		json.RawMessage(`{"type":"resource","resource":{"uri":"file:///work/input.txt","mimeType":"text/plain","text":"attached input"}}`),
	}))
	appendRootFixture(t, root, `{"live":"discarded output"}`)

	require.NoError(t, root.Finish(acp.RootTranscriptCompletion{
		Reason: acp.RootTranscriptPromptEnded, Completion: agent.MessageCompletionComplete, Discard: true,
	}))
	a.rootArchiveWG.Wait()
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.LeapMuxNotifications(), "discarding the turn suppresses its fallback warning also")
	assert.Empty(t, sink.PersistedNotifications())
	require.NoError(t, a.Sink().PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"live":"healthy after discard"}`)},
		agent.SpanInfo{},
	))
	require.Len(t, sink.Messages(), 1)
	assert.False(t, agent.OutputQueueFor(a.Sink()).HasPending())
}

type rootRecoverableScopeServices struct {
	agent.ProviderServices
	mu      sync.Mutex
	refused bool
	anchors []agent.CapturedTranscript
}

func (services *rootRecoverableScopeServices) OutputQueue() *agent.OutputQueue {
	return agent.OutputQueueFor(services.ProviderServices)
}

func (services *rootRecoverableScopeServices) TranscriptScopeFor(anchor agent.CapturedTranscript) (agent.TranscriptScope, error) {
	services.mu.Lock()
	services.anchors = append(services.anchors, anchor)
	refused := services.refused
	services.mu.Unlock()
	if refused {
		return nil, errors.New("the transcript scope is temporarily unavailable")
	}
	return services.ProviderServices.TranscriptScopeFor(anchor)
}

func TestFastagentPreparationScopeFailureRetainsItsAcceptedPrefixAndOriginalOwner(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	services := &rootRecoverableScopeServices{ProviderServices: a.Sink(), refused: true}
	root := unpreparedRootFixture(t, a, services)
	appendRootFixture(t, root, `{"live":"first accepted prefix row"}`)
	appendRootFixture(t, root, `{"live":"second accepted prefix row"}`)
	require.ErrorContains(t, root.Prepare(), "temporarily unavailable")
	require.NoError(t, root.Finish(acp.RootTranscriptCompletion{
		Reason: acp.RootTranscriptPromptNotSent, Completion: agent.MessageCompletionError,
	}))
	a.rootArchiveWG.Wait()
	queue := services.OutputQueue()
	require.True(t, queue.HasPending(), "the refused scope must retain the accepted prefix in the output queue")
	assert.Empty(t, sink.Messages())
	require.ErrorContains(t, queue.Flush(), "temporarily unavailable")
	assert.Empty(t, sink.Messages())

	// The retry occurs after the sink has retired the original native session.
	// Every stored prefix row must still use the original captured publication.
	sink.UpdateSessionID("replacement-session")
	services.mu.Lock()
	services.refused = false
	services.mu.Unlock()
	require.NoError(t, queue.Flush())
	rows := sink.Messages()
	require.Len(t, rows, 2)
	for index, expected := range []string{"first accepted prefix row", "second accepted prefix row"} {
		assert.Contains(t, string(rows[index].Content), expected)
		assert.Equal(t, "session-1", rows[index].AgentSessionID)
	}
	assert.False(t, queue.HasPending())
	require.NoError(t, queue.Flush())
	assert.Equal(t, rows, sink.Messages(), "a completed retry must not persist either prefix row again")
	services.mu.Lock()
	anchors := append([]agent.CapturedTranscript(nil), services.anchors...)
	services.mu.Unlock()
	require.GreaterOrEqual(t, len(anchors), 3)
	publication := anchors[0].FrozenContent().Publication
	require.NotNil(t, publication)
	for _, anchor := range anchors[1:] {
		assert.Same(t, publication.Owner(), anchor.FrozenContent().Publication.Owner(), "scope retries must retain the original publication owner")
	}
}

type rootRecoverableStorageServices struct {
	agent.ProviderServices
	mu      sync.Mutex
	refused bool
}

func (services *rootRecoverableStorageServices) OutputQueue() *agent.OutputQueue {
	return agent.OutputQueueFor(services.ProviderServices)
}

func (services *rootRecoverableStorageServices) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	services.mu.Lock()
	refused := services.refused
	services.mu.Unlock()
	if refused {
		return errors.New("the prefix storage is temporarily unavailable")
	}
	return services.ProviderServices.PersistMessage(source, content, span)
}

func TestFastagentJournalPreparationFailureRetainsItsAcceptedPrefixWithoutAJournal(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	services := &rootRecoverableStorageServices{ProviderServices: a.Sink(), refused: true}
	root := unpreparedRootFixture(t, a, services)
	position := root.Reserve()
	receipt := agent.NewTranscriptWriteReceipt()
	original := []byte(`{"live":"accepted before the journal failed"}`)
	require.NoError(t, position.Writer().PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: original, WriteReceipt: receipt}, agent.SpanInfo{},
	))
	position.Complete()
	original[0] = 'x'
	a.home = filepath.Join(t.TempDir(), "home-file")
	require.NoError(t, os.WriteFile(a.home, []byte("a regular file"), 0o600))
	require.Error(t, root.Prepare())
	require.NoError(t, root.Finish(acp.RootTranscriptCompletion{
		Reason: acp.RootTranscriptPromptNotSent, Completion: agent.MessageCompletionError,
	}))
	a.rootArchiveWG.Wait()
	queue := services.OutputQueue()
	require.True(t, queue.HasPending(), "journal setup failure must not release an accepted prefix")
	assert.Empty(t, sink.Messages())
	_, committed := receipt.StoredMessageSequence()
	assert.False(t, committed)

	sink.UpdateSessionID("replacement-session")
	services.mu.Lock()
	services.refused = false
	services.mu.Unlock()
	require.NoError(t, queue.Flush())
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.JSONEq(t, `{"live":"accepted before the journal failed"}`, string(rows[0].Content))
	assert.Equal(t, "session-1", rows[0].AgentSessionID)
	_, committed = receipt.StoredMessageSequence()
	assert.True(t, committed)
	assert.False(t, queue.HasPending())
	require.NoError(t, queue.Flush())
	assert.Equal(t, rows, sink.Messages())
}

func TestFastagentPreprepareEnrichmentLimitAndFailedCASPreserveAcceptedInput(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	root := unpreparedRootFixture(t, a, a.Sink())
	position := root.Reserve()
	overlay, ok := position.Writer().(acp.PendingToolRequestWriter)
	require.True(t, ok)
	receipt := agent.NewTranscriptWriteReceipt()
	original := []byte(`{"request":"preserve this opener"}`)
	require.NoError(t, position.Writer().PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: original, WriteReceipt: receipt},
		agent.SpanInfo{SpanID: "preparation-limit-request", SpanType: "read"},
	))
	tooLarge := []byte(`{"input":"` + strings.Repeat("x", 8<<20) + `"}`)
	var updated bool
	var enrichErr error
	require.NotPanics(t, func() {
		updated, enrichErr = overlay.EnrichPendingToolRequest(receipt, 0, original, tooLarge)
	})
	require.Error(t, enrichErr)
	assert.False(t, updated)
	snapshot, found, err := overlay.ReadPendingToolRequest(receipt)
	require.NoError(t, err)
	require.True(t, found)
	assert.Zero(t, snapshot.Revision)
	assert.Empty(t, snapshot.Content.Supplemental)
	assert.Equal(t, original, snapshot.Content.Original)

	accepted := []byte(`{"input":"the accepted complete input"}`)
	updated, err = overlay.EnrichPendingToolRequest(receipt, 0, original, accepted)
	require.NoError(t, err)
	require.True(t, updated, "rejecting an unaccepted supplement must preserve capacity for valid input")
	updated, err = overlay.EnrichPendingToolRequest(receipt, 0, original, []byte(`{"input":"stale replacement"}`))
	require.NoError(t, err)
	assert.False(t, updated)
	snapshot, found, err = overlay.ReadPendingToolRequest(receipt)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(1), snapshot.Revision)
	assert.Equal(t, accepted, snapshot.Content.Supplemental)
	position.Complete()
	require.NoError(t, root.Prepare())
	require.NoError(t, root.Finish(acp.RootTranscriptCompletion{
		Reason: acp.RootTranscriptWorkEnded, Completion: agent.MessageCompletionComplete,
	}))
	a.rootArchiveWG.Wait()
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Equal(t, original, rows[0].Content)
	assert.JSONEq(t, string(accepted), string(rows[0].SupplementalContent))
}

func TestFastagentPreparingMigrationFailureReturnsTheErrorAndKeepsAcceptedInput(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	root := unpreparedRootFixture(t, a, a.Sink())
	storage, err := newRootJournal(a.home, a.CurrentSessionID(), a.AgentID(), "migration-failure")
	require.NoError(t, err)
	journal := &refusedRootJournal{rootJournalStore: storage, refuse: true}
	t.Cleanup(func() {
		// abort schedules Stop asynchronously. Release its process wait before joining it.
		a.SimulateExitForTest()
		a.rootFailureWork.CloseAndWait()
		root.group.batch.Discard()
		require.NoError(t, storage.Cleanup())
	})
	position := root.Reserve()
	receipt := agent.NewTranscriptWriteReceipt()
	original := []byte(`{"request":"accepted before migration"}`)
	require.NoError(t, position.Writer().PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: original, WriteReceipt: receipt},
		agent.SpanInfo{SpanID: "preparing-migration", SpanType: "read"},
	))
	position.Complete()
	original[0] = 'x'
	root.group.mu.Lock()
	root.group.journal = journal
	root.group.mu.Unlock()

	err = root.group.migratePreparingEntries(journal)
	require.ErrorContains(t, err, "the private output storage is unavailable", "Prepare must receive the migration error before it admits a native prompt")
	root.group.mu.Lock()
	entries := append([]*rootJournalEntry(nil), root.group.entries...)
	root.group.mu.Unlock()
	require.Len(t, entries, 1)
	entry := *entries[0]
	retained, err := root.group.readEntry(&entry)
	require.NoError(t, err)
	assert.JSONEq(t, `{"request":"accepted before migration"}`, string(retained.content.Original))
	assert.Same(t, receipt, retained.content.WriteReceipt)
	_, committed := receipt.StoredMessageSequence()
	assert.False(t, committed)
	assert.Empty(t, sink.Messages(), "a rejected migration must not publish or discard its accepted prefix")
}
