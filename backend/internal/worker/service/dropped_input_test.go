package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

// droppingSteerAgent records each steer that reaches it. While dropDuringSteer
// is set, it also hands each steer back through its sink before SteerInput
// returns: the queue row of the input is then still DISPATCHING, and Accept has
// not yet written the transcript row, which is the window that a drop at an
// interrupt can hit.
type droppingSteerAgent struct {
	agenttest.IdleAgent
	sink            agent.ProviderServices
	dropDuringSteer atomic.Bool
	mu              sync.Mutex
	requests        []agent.SteerRequest
	sessions        []string
	dropErrs        []error
}

var _ agent.SessionInputSteerer = (*droppingSteerAgent)(nil)

func (a *droppingSteerAgent) SteerInput(request agent.SteerRequest) error {
	a.mu.Lock()
	a.requests = append(a.requests, request)
	a.mu.Unlock()
	if a.dropDuringSteer.Load() {
		err := a.sink.RequeueDroppedInput(request)
		a.mu.Lock()
		a.dropErrs = append(a.dropErrs, err)
		a.mu.Unlock()
	}
	return nil
}

func (a *droppingSteerAgent) SteerInputForSession(sessionID string, request agent.SteerRequest) error {
	a.mu.Lock()
	a.sessions = append(a.sessions, sessionID)
	a.mu.Unlock()
	return a.SteerInput(request)
}

func (*droppingSteerAgent) SupportsSteering() bool { return true }

// SteerSurvivesTurnEnd is what a provider that hands back a steer declares
// (see agent.TurnServices.RequeueDroppedInput).
func (*droppingSteerAgent) SteerSurvivesTurnEnd() bool { return true }

// lastRequest returns the last steer that reached the provider.
func (a *droppingSteerAgent) lastRequest(t *testing.T) agent.SteerRequest {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	require.NotEmpty(t, a.requests, "no steer reached the provider")
	return a.requests[len(a.requests)-1]
}

// dropOrder states when the provider hands a steer back, relative to the
// Accept that turns the input's queue row into its transcript row.
type dropOrder struct {
	name   string
	inside bool
}

// dropOrders are the two orders that a drop at an interrupt can take.
var dropOrders = []dropOrder{
	{name: "after the transcript row is written"},
	{name: "while the queue row is still dispatching", inside: true},
}

// newDropFixture is newRequeueFixture with a provider that records each steer
// and an active steerable turn, so each test steers the input it enqueues.
func newDropFixture(t *testing.T) (*Service, agent.ProviderServices, *droppingSteerAgent) {
	t.Helper()
	svc, sink := newRequeueFixture(t)
	provider := &droppingSteerAgent{sink: sink}
	svc.Agents.PutAgentForTest("agent-1", provider)
	_, err := svc.InputQueue.TurnStarted(t.Context(), "agent-1", true)
	require.NoError(t, err)
	return svc, sink, provider
}

// steerDropped enqueues input and then each of behind, steers input, and
// returns the request that reached the provider. When order.inside is set, the
// provider hands the steer back before Accept writes its transcript row.
// Otherwise the caller hands it back.
func steerDropped(t *testing.T, svc *Service, provider *droppingSteerAgent, order dropOrder, input inputqueue.NewItem, behind ...inputqueue.NewItem) agent.SteerRequest {
	t.Helper()
	for _, item := range append([]inputqueue.NewItem{input}, behind...) {
		item.AgentID = "agent-1"
		_, err := svc.InputQueue.Enqueue(t.Context(), item)
		require.NoError(t, err)
	}
	provider.dropDuringSteer.Store(order.inside)
	_, err := svc.InputQueue.Steer(t.Context(), "agent-1", input.ID)
	provider.dropDuringSteer.Store(false)
	require.NoError(t, err)
	if order.inside {
		provider.mu.Lock()
		require.Len(t, provider.dropErrs, 1)
		require.NoError(t, provider.dropErrs[0], "the hand-back inside the steer")
		provider.dropErrs = nil
		provider.mu.Unlock()
	}
	request := provider.lastRequest(t)
	require.Equal(t, input.ID, request.InputID, "the steer states its queue item")
	_, err = svc.Queries.GetMessageByAgentAndID(t.Context(), db.GetMessageByAgentAndIDParams{AgentID: "agent-1", ID: input.ID})
	require.NoError(t, err, "the queue accepts the original whichever order the drop takes")
	return request
}

// queuedItems returns the items that agent-1's queue holds.
func queuedItems(t *testing.T, svc *Service) []inputqueue.SnapshotItem {
	t.Helper()
	snapshot, err := svc.InputQueue.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	return snapshot.Items
}

// requeuedNotices counts the rows that state why a message appears again.
func requeuedNotices(t *testing.T, svc *Service) int {
	t.Helper()
	return len(findNotificationsByType(readAllNotifications(t, svc.Queries, "agent-1"), contracts.NotificationTypeInputRequeued))
}

// fixFlakySpec is the sender of the agent messages of these tests, with the
// title that it had when it sent them.
func fixFlakySpec() *inputqueue.AgentSender {
	return &inputqueue.AgentSender{TabID: senderAgentID, Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, Title: "Fix flaky spec"}
}

// A dropped message from another agent comes back as that agent's message:
// the sender, the title in its header, and the text that it wrote. It must
// never come back as the user's message with the header inside its text. The
// Worker finds the input in either place that a drop can meet it.
func TestRequeueDroppedInputKeepsTheSenderOfAnAgentMessage(t *testing.T) {
	t.Parallel()
	for _, order := range dropOrders {
		t.Run(order.name, func(t *testing.T) {
			t.Parallel()
			svc, sink, provider := newDropFixture(t)
			request := steerDropped(t, svc, provider, order, inputqueue.NewItem{
				ID: "from-agent", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
				Sender: fixFlakySpec(), Text: "Run the tests",
			})
			const delivered = "[Message from agent \"Fix flaky spec\" (Claude Code), not from your operator]\n\nRun the tests"
			require.Equal(t, delivered, request.Content, "fixture check: the provider read the header")
			if !order.inside {
				require.NoError(t, sink.RequeueDroppedInput(request))
			}

			items := queuedItems(t, svc)
			require.Len(t, items, 1)
			item := items[0]
			assert.Equal(t, droppedInputID("agent-1", "from-agent"), item.ID)
			assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AGENT_MESSAGE, item.Kind)
			assert.Equal(t, fixFlakySpec(), item.Sender, "the copy keeps the sender and the title that the header stated")
			assert.Equal(t, "Run the tests", item.TextPreview, "the copy holds the sender's text, without the header")
			assert.False(t, item.ReclassifyOnEdit)
			assert.Equal(t, inputqueue.ErrAgentMessageEdit.Error(), item.EditRefusal, "nobody edits the copy, as nobody edits the original")
			assert.Equal(t, 1, requeuedNotices(t, svc))

			_, err := svc.InputQueue.Steer(t.Context(), "agent-1", item.ID)
			require.NoError(t, err)
			copied := provider.lastRequest(t)
			assert.Equal(t, delivered, copied.Content, "the delivery of the copy puts the header before the text once")
			assert.Equal(t, item.ID, copied.InputID)

			require.NoError(t, sink.RequeueDroppedInput(copied), "a drop of the copy is another drop")
			items = queuedItems(t, svc)
			require.Len(t, items, 1)
			assert.Equal(t, droppedInputID("agent-1", item.ID), items[0].ID)
			assert.Equal(t, fixFlakySpec(), items[0].Sender, "a copy of a copy still comes from the sender")
			assert.Equal(t, "Run the tests", items[0].TextPreview)
			assert.Equal(t, 2, requeuedNotices(t, svc))
		})
	}
}

// A provider that hands back text other than what the Worker sent leaves the
// title unknown: the transcript row keeps none. The copy still comes from the
// sender, and its header identifies the sender by its tab, as for an untitled
// sender.
func TestRequeueDroppedAgentMessageWithChangedTextIdentifiesTheSenderByItsTab(t *testing.T) {
	t.Parallel()
	svc, sink, provider := newDropFixture(t)
	request := steerDropped(t, svc, provider, dropOrders[0], inputqueue.NewItem{
		ID: "from-agent", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Sender: fixFlakySpec(), Text: "Run the tests",
	})
	request.Content = "Run the tests"
	require.NoError(t, sink.RequeueDroppedInput(request))

	items := queuedItems(t, svc)
	require.Len(t, items, 1)
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AGENT_MESSAGE, items[0].Kind)
	assert.Equal(t, &inputqueue.AgentSender{TabID: senderAgentID, Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}, items[0].Sender)
	assert.Equal(t, "Run the tests", items[0].TextPreview)
	_, err := svc.InputQueue.Steer(t.Context(), "agent-1", items[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "[Message from agent \"sender-agent\" (Claude Code), not from your operator]\n\nRun the tests",
		provider.lastRequest(t).Content)
}

// A dropped message of the user comes back as the user's message, with its
// text and its attachments, behind what the queue holds. One row states why it
// appears again, and a repeat of the same drop adds neither.
func TestRequeueDroppedInputKeepsAUserMessage(t *testing.T) {
	t.Parallel()
	for _, order := range dropOrders {
		t.Run(order.name, func(t *testing.T) {
			t.Parallel()
			svc, sink, provider := newDropFixture(t)
			request := steerDropped(t, svc, provider, order, inputqueue.NewItem{
				ID: "typed-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "Also check the tests.",
				ReclassifyOnEdit: true,
				Attachments:      []inputqueue.Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("body")}},
			}, inputqueue.NewItem{
				ID: "typed-2", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "And the docs.",
			})
			if !order.inside {
				require.NoError(t, sink.RequeueDroppedInput(request))
			}

			items := queuedItems(t, svc)
			require.Len(t, items, 2)
			assert.Equal(t, "typed-2", items[0].ID, "the queue appends a copy behind what it holds")
			item := items[1]
			assert.Equal(t, droppedInputID("agent-1", "typed-1"), item.ID)
			assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, item.Kind)
			assert.Nil(t, item.Sender)
			assert.Equal(t, "Also check the tests.", item.TextPreview)
			assert.True(t, item.ReclassifyOnEdit, "an edit of the copy reads its text again, as an edit of a typed message does")
			require.Len(t, item.Metadata, 1)
			assert.Equal(t, "notes.txt", item.Metadata[0].Filename)
			assert.Equal(t, int64(4), item.Metadata[0].Size, "the copy keeps the attachment data that the provider handed back")
			assert.Equal(t, 1, requeuedNotices(t, svc))

			require.NoError(t, sink.RequeueDroppedInput(request), "a repeat is not a failure")
			assert.Len(t, queuedItems(t, svc), 2, "a repeat of one drop queues nothing")
			assert.Equal(t, 1, requeuedNotices(t, svc), "a repeat of one drop writes no second row")
		})
	}
}

// An input that the queue still keeps comes back by no hand-back: a removal
// or an edit of the user stands, and an input that the queue holds already
// waits for a delivery of its own.
func TestRequeueDroppedInputAddsNothingForAnInputThatTheQueueKeeps(t *testing.T) {
	t.Parallel()

	t.Run("the user edited the copy", func(t *testing.T) {
		t.Parallel()
		svc, sink, provider := newDropFixture(t)
		request := steerDropped(t, svc, provider, dropOrders[0], inputqueue.NewItem{
			ID: "typed-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "Also check the tests.", ReclassifyOnEdit: true,
		})
		require.NoError(t, sink.RequeueDroppedInput(request))
		copyID := droppedInputID("agent-1", "typed-1")
		snapshot, _, _, err := svc.InputQueue.BeginEdit(t.Context(), "agent-1", copyID, "browser-1", false)
		require.NoError(t, err)
		require.Len(t, snapshot.Items, 1)
		_, err = svc.InputQueue.Update(t.Context(), inputqueue.UpdateRequest{
			AgentID: "agent-1", InputID: copyID, ClientID: "browser-1",
			ExpectedVersion: snapshot.Items[0].Version, Text: "Also check the docs.",
		})
		require.NoError(t, err)

		require.NoError(t, sink.RequeueDroppedInput(request), "a repeat of the drop matches the edited copy")
		items := queuedItems(t, svc)
		require.Len(t, items, 1)
		assert.Equal(t, copyID, items[0].ID)
		assert.Equal(t, "Also check the docs.", items[0].TextPreview, "the copy keeps the edit")
		assert.Equal(t, 1, requeuedNotices(t, svc))
	})

	t.Run("the user removed the copy", func(t *testing.T) {
		t.Parallel()
		svc, sink, provider := newDropFixture(t)
		request := steerDropped(t, svc, provider, dropOrders[0], inputqueue.NewItem{
			ID: "from-agent", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Sender: fixFlakySpec(), Text: "Run the tests",
		})
		require.NoError(t, sink.RequeueDroppedInput(request))
		_, err := svc.InputQueue.Delete(t.Context(), "agent-1", droppedInputID("agent-1", "from-agent"))
		require.NoError(t, err)

		require.NoError(t, sink.RequeueDroppedInput(request))
		assert.Empty(t, queuedItems(t, svc))
		assert.Equal(t, 1, requeuedNotices(t, svc))
	})

	t.Run("the user removed the original", func(t *testing.T) {
		t.Parallel()
		svc, sink, _ := newDropFixture(t)
		_, err := svc.InputQueue.Enqueue(t.Context(), inputqueue.NewItem{
			ID: "typed-1", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "Also check the tests.",
		})
		require.NoError(t, err)
		_, err = svc.InputQueue.Delete(t.Context(), "agent-1", "typed-1")
		require.NoError(t, err)

		require.NoError(t, sink.RequeueDroppedInput(agent.SteerRequest{InputID: "typed-1", Content: "Also check the tests."}))
		assert.Empty(t, queuedItems(t, svc))
		assert.Zero(t, requeuedNotices(t, svc))
	})

	t.Run("the queue holds the original", func(t *testing.T) {
		t.Parallel()
		svc, sink, _ := newDropFixture(t)
		_, err := svc.InputQueue.Enqueue(t.Context(), inputqueue.NewItem{
			ID: "typed-1", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "Also check the tests.",
		})
		require.NoError(t, err)

		require.NoError(t, sink.RequeueDroppedInput(agent.SteerRequest{InputID: "typed-1", Content: "Also check the tests."}))
		items := queuedItems(t, svc)
		require.Len(t, items, 1)
		assert.Equal(t, "typed-1", items[0].ID, "the queue holds the original alone")
		assert.Zero(t, requeuedNotices(t, svc))
	})
}

// Every steerable kind other than a message comes back as a user message with
// the text that the model would have read. A control reply answered a request
// of the turn that the drop ended, and its copy has an id that no control
// response links, so it cannot be a reply. The queue admits an automatic retry
// only from the Worker's own schedule.
func TestRequeueDroppedInputRestoresEveryOtherKindAsAUserMessage(t *testing.T) {
	t.Parallel()

	t.Run("control feedback", func(t *testing.T) {
		t.Parallel()
		svc, sink, provider := newDropFixture(t)
		request := steerDropped(t, svc, provider, dropOrders[0], inputqueue.NewItem{
			ID: "feedback-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, Text: "Use the other API",
		})
		require.NoError(t, sink.RequeueDroppedInput(request))
		assertUserMessageCopy(t, svc, "feedback-1", "The user rejected the request. Their feedback follows:\n\nUse the other API")
	})

	t.Run("an automatic retry", func(t *testing.T) {
		t.Parallel()
		svc, sink, provider := newDropFixture(t)
		// A reset in the past schedules the retry at once.
		require.NoError(t, svc.InputQueue.ScheduleAutoRetry(t.Context(), "agent-1", retrySchedule(
			leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, time.Now().Add(-time.Hour))))
		items := queuedItems(t, svc)
		require.Len(t, items, 1)
		retryID := items[0].ID
		_, err := svc.InputQueue.Steer(t.Context(), "agent-1", retryID)
		require.NoError(t, err)
		request := provider.lastRequest(t)
		require.Equal(t, retryID, request.InputID)
		require.NoError(t, sink.RequeueDroppedInput(request))
		assertUserMessageCopy(t, svc, retryID, request.Content)
	})

	t.Run("a question answer", func(t *testing.T) {
		t.Parallel()
		svc, row, _ := setupQuestionDeliveryTest(t)
		sink := newTestRootOutputSink(t, svc.Output, row.ID, row.AgentProvider)
		provider := &droppingSteerAgent{sink: sink}
		svc.Agents.PutAgentForTest(row.ID, provider)
		_, err := svc.InputQueue.TurnStarted(t.Context(), row.ID, true)
		require.NoError(t, err)
		_, err = svc.InputQueue.SetPaused(t.Context(), row.ID, true)
		require.NoError(t, err)
		require.NoError(t, svc.processControlAnswer(row, workerAnswer{
			content: questionAnswerResponse("question-1", "Red"), claimToken: "claim-1",
		}))
		items := queuedItems(t, svc)
		require.Len(t, items, 1)
		_, err = svc.InputQueue.Steer(t.Context(), row.ID, items[0].ID)
		require.NoError(t, err)
		request := provider.lastRequest(t)
		require.Equal(t, questionReplyTestInput, request.Content, "fixture check: the provider read its own reply")
		require.Equal(t, []string{"session-1"}, provider.sessions, "fixture check: the answer took the session-checked steer")

		require.NoError(t, sink.RequeueDroppedInput(request))
		assertUserMessageCopy(t, svc, items[0].ID, questionReplyTestInput)
		copyID := droppedInputID(row.ID, items[0].ID)
		_, err = svc.InputQueue.Steer(t.Context(), row.ID, copyID)
		require.NoError(t, err, "the copy is delivered without the question that the original answered")
		assert.Equal(t, questionReplyTestInput, provider.lastRequest(t).Content)
	})
}

// assertUserMessageCopy asserts that agent-1's queue holds the copy of
// inputID alone, as a user message with text.
func assertUserMessageCopy(t *testing.T, svc *Service, inputID, text string) {
	t.Helper()
	items := queuedItems(t, svc)
	require.Len(t, items, 1)
	assert.Equal(t, droppedInputID("agent-1", inputID), items[0].ID)
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, items[0].Kind)
	assert.Nil(t, items[0].Sender)
	assert.Equal(t, text, items[0].TextPreview)
	assert.Equal(t, 1, requeuedNotices(t, svc))
}

// TestRequeueDroppedInputReportsARefusalAndStatesNothing pins the failure paths.
// The provider states the drop to the reader itself when the queue refuses, so a
// refusal must write no row that claims the message comes again.
func TestRequeueDroppedInputReportsARefusalAndStatesNothing(t *testing.T) {
	t.Parallel()

	typed := inputqueue.NewItem{ID: "typed-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "Also check the tests."}

	t.Run("a stopped queue", func(t *testing.T) {
		t.Parallel()
		svc, sink, provider := newDropFixture(t)
		request := steerDropped(t, svc, provider, dropOrders[0], typed)
		stopInputQueue(t, svc, "agent-1")
		require.ErrorIs(t, sink.RequeueDroppedInput(request), inputqueue.ErrManagerStopped)
		assert.Zero(t, requeuedNotices(t, svc))
	})

	t.Run("the same drop with different text", func(t *testing.T) {
		t.Parallel()
		svc, sink, provider := newDropFixture(t)
		request := steerDropped(t, svc, provider, dropOrders[0], typed)
		require.NoError(t, sink.RequeueDroppedInput(request))
		request.Content = "Something else."
		require.ErrorIs(t, sink.RequeueDroppedInput(request), inputqueue.ErrConflict)
		assert.Equal(t, 1, requeuedNotices(t, svc))
	})

	t.Run("a hand-back that holds nothing", func(t *testing.T) {
		t.Parallel()
		svc, sink, provider := newDropFixture(t)
		request := steerDropped(t, svc, provider, dropOrders[0], typed)
		request.Content = ""
		require.ErrorIs(t, sink.RequeueDroppedInput(request), inputqueue.ErrInvalidInput)
		assert.Empty(t, queuedItems(t, svc))
		assert.Zero(t, requeuedNotices(t, svc))
	})

	t.Run("an input that the Worker never held", func(t *testing.T) {
		t.Parallel()
		svc, sink, _ := newDropFixture(t)
		require.ErrorIs(t, sink.RequeueDroppedInput(agent.SteerRequest{InputID: "unknown", Content: "Also check the tests."}), inputqueue.ErrNotFound)
		assert.Empty(t, queuedItems(t, svc))
		assert.Zero(t, requeuedNotices(t, svc))
	})

	t.Run("an input of another agent", func(t *testing.T) {
		t.Parallel()
		svc, _, provider := newDropFixture(t)
		request := steerDropped(t, svc, provider, dropOrders[0], typed)
		require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
			ID: "agent-2", WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE,
		}))
		other := newTestRootOutputSink(t, svc.Output, "agent-2", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE)
		require.ErrorIs(t, other.RequeueDroppedInput(request), inputqueue.ErrNotFound,
			"an agent reads only its own inputs")
		assert.Empty(t, queuedItems(t, svc))
	})

	t.Run("a steer that states no queue item", func(t *testing.T) {
		t.Parallel()
		svc, sink, _ := newDropFixture(t)
		require.ErrorIs(t, sink.RequeueDroppedInput(agent.SteerRequest{Content: "Also check the tests."}), inputqueue.ErrInvalidInput)
		assert.Zero(t, requeuedNotices(t, svc))
	})

	t.Run("a subagent", func(t *testing.T) {
		t.Parallel()
		svc, sink := newRequeueFixture(t)
		childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
			RegistryRole: bgtask.RoleChildTask,
			SpawnSpanID:  "spawn-span", ProviderChildKey: "row-key", Title: "child task"})
		require.NoError(t, err)
		err = sink.ChildSink(childID).RequeueDroppedInput(agent.SteerRequest{InputID: "typed-1", Content: "Also check the tests."})
		require.ErrorContains(t, err, "states no queue item")
		snapshot, err := svc.InputQueue.Snapshot(context.Background(), childID)
		require.NoError(t, err)
		assert.Empty(t, snapshot.Items)
	})

	t.Run("no queue wired", func(t *testing.T) {
		t.Parallel()
		svc, sink := newRequeueFixture(t)
		svc.Output.SetRequeueDroppedInputFunc(nil)
		err := sink.RequeueDroppedInput(agent.SteerRequest{InputID: "typed-1", Content: "Also check the tests."})
		require.ErrorContains(t, err, "no input queue is wired")
		assert.Zero(t, requeuedNotices(t, svc))
	})
}

// A provider can hand back one drop from two goroutines at once, for example a
// turn end and a stop that race. The queue adds the item once, and one row
// states why the message appears again.
func TestRequeueDroppedInputAddsOneDropOnceAcrossConcurrentCalls(t *testing.T) {
	t.Parallel()

	svc, sink, provider := newDropFixture(t)
	request := steerDropped(t, svc, provider, dropOrders[0], inputqueue.NewItem{
		ID: "from-agent", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Sender: fixFlakySpec(), Text: "Run the tests",
	})
	const callers = 8
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			assert.NoError(t, sink.RequeueDroppedInput(request))
		})
	}
	wg.Wait()

	assert.Len(t, queuedItems(t, svc), 1)
	assert.Equal(t, 1, requeuedNotices(t, svc))
}

func TestDroppedInputIDKeepsEachAgentAndInputApart(t *testing.T) {
	t.Parallel()

	id := droppedInputID("agent-1", "input-1")
	assert.Regexp(t, `^dropped-[0-9a-f]{32}$`, id)
	assert.Equal(t, id, droppedInputID("agent-1", "input-1"), "one input gives one id")
	assert.NotEqual(t, id, droppedInputID("agent-2", "input-1"), "the id is unique across the Worker")
	assert.NotEqual(t, id, droppedInputID("agent-1", "input-2"))
	assert.NotEqual(t, id, droppedInputID("agent-1", id), "a drop of a copy is another drop")
	// An input id is a client's own text, so no separator keeps the pair apart.
	assert.NotEqual(t, droppedInputID("a\x00b", "c"), droppedInputID("a", "b\x00c"))
	assert.NotEqual(t, droppedInputID("ab", "c"), droppedInputID("a", "bc"))
	assert.NotEqual(t, droppedInputID("", "x"), droppedInputID("x", ""))
}
