package ohmypi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManualCompactionEventsPreserveTheNativeOutcome(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		end  string
	}{
		{"completed", `{"type":"manual_compaction_end","aborted":false,"result":{"summary":"retained context","tokensBefore":12000,"tokensAfter":3000}}`},
		{"cancelled", `{"type":"manual_compaction_end","aborted":true,"errorMessage":"Compaction cancelled"}`},
		{"skipped", `{"type":"manual_compaction_end","aborted":false,"skipped":true,"errorMessage":"Nothing to compact"}`},
		{"failed", `{"type":"manual_compaction_end","aborted":false,"errorMessage":"The summary failed"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.emit(`{"type":"manual_compaction_start","mode":"handoff"}`)
			assert.Empty(t, r.sink.Messages(), "the native start is live state")
			assert.Contains(t, r.sink.SessionInfos(), map[string]interface{}{
				contracts.SessionInfoKeyCompaction: map[string]interface{}{contracts.CompactionFieldTrigger: "manual"},
			})
			r.emit(test.end)
			assert.Empty(t, r.sink.Messages(), "native lifecycle frames must not become raw message rows")
			notifications := r.sink.PersistedNotifications()
			require.Len(t, notifications, 1)
			assert.JSONEq(t, test.end, string(notifications[0].Content))
			assert.Contains(t, r.sink.SessionInfos(), map[string]interface{}{contracts.SessionInfoKeyCompaction: nil})
			r.agent.finishOutput(agent.MessageCompletionInterrupted)
			assert.Empty(t, r.sink.Notifications(), "a native end prevents another synthetic end")
		})
	}
}

type blockedCompactionEndWriter struct {
	agent.ProviderServices
	entered, released chan struct{}
	once              sync.Once
}

func (writer *blockedCompactionEndWriter) BroadcastSessionInfo(info map[string]interface{}) {
	if value, present := info[contracts.SessionInfoKeyCompaction]; present && value == nil {
		writer.once.Do(func() { close(writer.entered); <-writer.released })
	}
	writer.ProviderServices.BroadcastSessionInfo(info)
}

func TestCompactionPublicationsKeepTheirAdmissionOrder(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	entered, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	r.agent.sink = agent.DecorateProviderServices(r.agent.sink, func(base agent.ProviderServices) agent.ProviderServices {
		return &blockedCompactionEndWriter{ProviderServices: base, entered: entered, released: released}
	})
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == contracts.OhMyPiCommandCompact {
			return &rigReply{Skip: true}
		}
		return nil
	})
	r.emit(`{"type":"manual_compaction_start"}`)
	finished := make(chan struct{})
	go func() {
		r.emit(`{"type":"manual_compaction_end","aborted":false,"result":{"summary":"first pass"}}`)
		close(finished)
	}()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the first pass did not reach its held publication")
	}
	require.NoError(t, r.agent.CompactContext(), "the publisher must hold no provider lock")
	release()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("the first publication did not finish")
	}
	assert.Equal(t, agent.TurnState{Active: true}, r.agent.PublishTurnActive())
	infos := r.sink.SessionInfos()
	require.NotEmpty(t, infos)
	assert.Equal(t, map[string]interface{}{contracts.CompactionFieldTrigger: "manual"}, infos[len(infos)-1][contracts.SessionInfoKeyCompaction], "the old end must precede the new start")
}

type reentrantCompactionWriter struct {
	agent.ProviderServices
	call func()
	once sync.Once
}

func (writer *reentrantCompactionWriter) BroadcastSessionInfo(info map[string]interface{}) {
	if value, present := info[contracts.SessionInfoKeyCompaction]; present && value != nil {
		writer.once.Do(writer.call)
	}
	writer.ProviderServices.BroadcastSessionInfo(info)
}

func TestCompactContextAllowsAReentrantPublication(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := testutil.DeadlineContext(t)
	result := make(chan error, 1)
	completedInCallback := make(chan bool, 1)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == contracts.OhMyPiCommandCompact {
			return &rigReply{Skip: true}
		}
		return nil
	})
	r.agent.sink = agent.DecorateProviderServices(r.agent.sink, func(base agent.ProviderServices) agent.ProviderServices {
		return &reentrantCompactionWriter{ProviderServices: base, call: func() {
			go func() { result <- r.agent.CompactContext() }()
			select {
			case err := <-result:
				assert.ErrorIs(t, err, agent.ErrAgentBusy)
				completedInCallback <- true
			case <-ctx.Done():
				completedInCallback <- false
			}
		}}
	})
	require.NoError(t, r.agent.CompactContext())
	completed := <-completedInCallback
	if !completed {
		assert.ErrorIs(t, <-result, agent.ErrAgentBusy)
	}
	require.True(t, completed, "a callback must be able to inspect public input admission before it returns")
}

type compactionWriteFailure struct{ beforeFailure func() }

func (writer *compactionWriteFailure) Write(data []byte) (int, error) {
	writer.beforeFailure()
	return len(data), errors.New("the transport closed after delivery")
}

func (*compactionWriteFailure) Close() error { return nil }

func TestCompactionWriteFailurePreservesAnObservedNativeStart(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	sink := &agenttest.ControlSink{}
	var a *Agent
	writer := &compactionWriteFailure{beforeFailure: func() { a.HandleOutput([]byte(`{"type":"manual_compaction_start"}`)) }}
	a = &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "compaction-write-failure", ProviderName: "omp", Stdin: writer, Ctx: ctx, Cancel: cancel}),
		sink:    agent.NewModelProgressResetSink(agent.NewProviderServices(sink)),
	}
	a.SkipStderr()
	require.Error(t, a.CompactContext())
	assert.Equal(t, agent.TurnState{Active: true}, a.PublishTurnActive(), "the observed native pass survives its command write refusal")
	a.HandleOutput([]byte(`{"type":"manual_compaction_end","aborted":true,"errorMessage":"Cancelled"}`))
	assert.Equal(t, agent.TurnState{}, a.PublishTurnActive())
}

func TestCompactContextUsesTheNativeEndBeforeItsReceipt(t *testing.T) {
	t.Parallel()
	for _, outcome := range []struct{ name, end, data, failure string }{
		{"completed", `{"type":"manual_compaction_end","aborted":false,"result":{"summary":"short","tokensBefore":12}}`, `{"summary":"short","tokensBefore":12}`, ""},
		{"cancelled", `{"type":"manual_compaction_end","aborted":true,"errorMessage":"Cancelled"}`, "", "Cancelled"},
		{"skipped", `{"type":"manual_compaction_end","aborted":false,"skipped":true,"errorMessage":"Nothing to compact"}`, "", "Nothing to compact"},
		{"failed", `{"type":"manual_compaction_end","aborted":false,"errorMessage":"Summary failed"}`, "", "Summary failed"},
	} {
		t.Run(outcome.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.respond(func(command recordedCommand) *rigReply {
				if command.Type != contracts.OhMyPiCommandCompact {
					return nil
				}
				return &rigReply{Before: []string{`{"type":"manual_compaction_start"}`, outcome.end}, Data: json.RawMessage(outcome.data), Error: outcome.failure}
			})
			require.NoError(t, r.agent.CompactContext())
			waitFor(t, func() bool { active, _ := r.sink.LastTurnActive(); return !active && r.sink.NotificationCount() == 1 })
			notifications := r.sink.PersistedNotifications()
			require.Len(t, notifications, 1)
			assert.JSONEq(t, outcome.end, string(notifications[0].Content))
		})
	}
}

func TestCompactContextKeepsTheReceiptAndANewerNativePassSeparate(t *testing.T) {
	t.Parallel()
	for _, newerPass := range []bool{false, true} {
		t.Run(fmt.Sprintf("newer_native_pass=%t", newerPass), func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.respond(func(command recordedCommand) *rigReply {
				if command.Type == contracts.OhMyPiCommandCompact {
					return &rigReply{Skip: true}
				}
				return nil
			})
			require.NoError(t, r.agent.CompactContext())
			r.waitForCommand(contracts.OhMyPiCommandCompact, 1)
			command := r.commandsOfType(contracts.OhMyPiCommandCompact)[0]
			r.emit(`{"type":"manual_compaction_start"}`, `{"type":"manual_compaction_end","aborted":false,"result":{"summary":"first"}}`)
			assert.Equal(t, agent.TurnState{Active: true}, r.agent.PublishTurnActive())
			assert.ErrorIs(t, agenttest.SendInput(r.agent, "Wait for the receipt.", nil), agent.ErrAgentBusy)
			assert.ErrorIs(t, agenttest.SteerInput(r.agent, agent.SteerRequest{Content: "Do not steer maintenance."}), agent.ErrAgentBusy)
			assert.ErrorIs(t, r.agent.CompactContext(), agent.ErrAgentBusy)
			if newerPass {
				r.emit(`{"type":"manual_compaction_start","mode":"handoff"}`)
			}
			r.emit(fmt.Sprintf(`{"type":"response","id":%q,"command":"compact","success":true,"data":{"summary":"first"}}`, command.ID))
			waitFor(t, func() bool { r.agent.Mu.Lock(); defer r.agent.Mu.Unlock(); return r.agent.compactionCommand == nil })
			assert.Equal(t, agent.TurnState{Active: newerPass}, r.agent.PublishTurnActive())
			assert.Equal(t, 1, r.sink.NotificationCount(), "the receipt must not duplicate the first native end")
			if newerPass {
				r.emit(`{"type":"manual_compaction_end","aborted":true,"errorMessage":"Second pass cancelled"}`)
				assert.Equal(t, 2, r.sink.NotificationCount())
				assert.Equal(t, agent.TurnState{}, r.agent.PublishTurnActive())
			}
		})
	}
}

func TestOldCompactionResponseCannotEndANewerPass(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	root := r.agent.rootConversation()
	first := r.agent.beginCompaction(root, "manual")
	r.emit(`{"type":"manual_compaction_end","aborted":false,"result":{"summary":"first pass"}}`)
	r.agent.beginCompaction(root, "manual")
	before := len(r.sink.SessionInfos())
	r.agent.recordCompactionResult(first, json.RawMessage(`{"type":"response","id":"old-pass","command":"compact","success":true,"data":{"summary":"old pass"}}`), nil)
	r.agent.Mu.Lock()
	open := root.compaction != nil
	r.agent.Mu.Unlock()
	assert.True(t, open, "an old response must not close the newer pass")
	assert.Len(t, r.sink.SessionInfos(), before, "an old response must not withdraw the newer live state")
	notifications := r.sink.PersistedNotifications()
	require.Len(t, notifications, 1, "the old receipt cannot duplicate its native end")
	assert.Contains(t, string(notifications[0].Content), "first pass")
}

func TestNativeCompactionKeepsMaintenanceSeparateFromPromptActivity(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"auto", "manual"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.emit(fmt.Sprintf(`{"type":%q}`, kind+"_compaction_start"))
			assert.Equal(t, agent.TurnState{Active: true}, r.agent.PublishTurnActive())
			assert.ErrorIs(t, agenttest.SendInput(r.agent, "After maintenance.", nil), agent.ErrAgentBusy)
			assert.ErrorIs(t, agenttest.SteerInput(r.agent, agent.SteerRequest{Content: "Maintenance takes no steer."}), agent.ErrAgentBusy)
			r.emit(`{"type":"agent_start"}`, frameAgentEnd)
			assert.Equal(t, agent.TurnState{Active: true}, r.agent.PublishTurnActive(), "ending a prompt does not end maintenance")
			r.emit(fmt.Sprintf(`{"type":%q,"aborted":false,"result":{"summary":"done"}}`, kind+"_compaction_end"))
			assert.Equal(t, agent.TurnState{}, r.agent.PublishTurnActive())
		})
	}
}

func TestChildManualCompactionKeepsItsOwnNotification(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, frameStarted)
	r.agent.Mu.Lock()
	childID := r.agent.subagents["Probe"].conv.childID
	r.agent.Mu.Unlock()
	parentBefore := r.agent.PublishTurnActive()
	end := `{"type":"manual_compaction_end","aborted":false,"result":{"summary":"child context","tokensBefore":120}}`
	r.emit(subagentEvent("Probe", `{"type":"manual_compaction_start"}`), subagentEvent("Probe", end))
	assert.Equal(t, parentBefore, r.agent.PublishTurnActive())
	assert.Empty(t, r.sink.PersistedNotifications())
	notifications := r.sink.Child(childID).PersistedNotifications()
	require.Len(t, notifications, 1)
	assert.JSONEq(t, end, string(notifications[0].Content))
}

func TestAFrameErrorDoesNotLetAnOldReceiptCloseTheNextCompaction(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == contracts.OhMyPiCommandCompact {
			return &rigReply{Skip: true}
		}
		return nil
	})
	require.NoError(t, r.agent.CompactContext())
	r.waitForCommand(contracts.OhMyPiCommandCompact, 1)
	command := r.commandsOfType(contracts.OhMyPiCommandCompact)[0]
	r.emit(`{"type":"manual_compaction_start"}`,
		`{"type":"rpc_frame_error","error":"The completed compaction frame could not be sent."}`,
		`{"type":"manual_compaction_start","mode":"handoff"}`)
	r.emit(fmt.Sprintf(`{"type":"response","id":%q,"command":"compact","success":true,"data":{"summary":"first pass","tokensBefore":12}}`, command.ID))
	waitFor(t, func() bool { return r.sink.NotificationCount() == 2 })
	assert.Equal(t, agent.TurnState{Active: true}, r.agent.PublishTurnActive(), "the next native pass still owns maintenance activity")
	assert.Contains(t, string(r.sink.LastNotification().Content), "first pass", "a missing native end retains the old receipt's known outcome")
	r.emit(`{"type":"manual_compaction_end","aborted":true,"errorMessage":"Next pass cancelled"}`)
	assert.Equal(t, 3, r.sink.NotificationCount())
	assert.Equal(t, agent.TurnState{}, r.agent.PublishTurnActive())
}
