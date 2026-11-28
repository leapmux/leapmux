package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodetest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func TestFamilyHooksReportEachTurnEnd(t *testing.T) {
	t.Parallel()
	f := &FamilyBase{}
	f.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	ends := 0
	f.TurnEnded = func() { ends++ }
	hooks := FamilyHooks(f)
	hooks.PromptEnded(nil, false)
	hooks.PromptEnded(errors.New("the prompt failed"), false)
	hooks.PromptEnded(nil, true)
	assert.Equal(t, 3, ends, "a completed, a failed and a stopped prompt each end a turn")

	quiet := &FamilyBase{}
	quiet.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	assert.NotPanics(t, func() { FamilyHooks(quiet).PromptEnded(nil, false) }, "a member without the hook")
}

type questionFamilySink struct {
	*agenttest.ControlSink
	child agent.ProviderServices
}

func (s *questionFamilySink) ChildSink(string) agent.ProviderServices { return s.child }

func TestFamilyQuestionsKeepTheChildOwnerThroughWithdrawal(t *testing.T) {
	t.Parallel()
	child := &agenttest.ControlSink{}
	parent := &questionFamilySink{ControlSink: &agenttest.ControlSink{}, child: agent.NewProviderServices(child)}
	services := agent.NewProviderServices(parent)
	family := &FamilyBase{}
	family.SetSinkForTest(services)
	family.SetSessionIDForTest("parent-session")
	family.QuestionTurnID = func(_ context.Context, sessionID, messageID string) string {
		assert.Equal(t, "child-session", sessionID)
		assert.Equal(t, "child-assistant", messageID)
		return "child-turn"
	}
	family.configureQuestions(services)
	family.ApplySubagentObservation(&acp.SubagentObservation{
		RowKey: "child-row", ChildAgentKey: "child-row", Status: bgtask.StatusRunning,
	})
	require.True(t, family.AttachChildSession(acp.ChildSessionTarget{
		RootSessionID: "parent-session", SessionID: "child-session", RowKey: "child-row",
	}))
	properties := []byte(`{"id":"que-child","sessionID":"child-session","tool":{"messageID":"child-assistant"}}`)
	requestID := family.Questions.publish(t.Context(), properties)
	require.Equal(t, "opencode-question:que-child", requestID)
	assert.Empty(t, parent.PublishedControls())
	controls := child.PublishedControls()
	require.Len(t, controls, 1)
	assert.Equal(t, "child-session", controls[0].AgentSessionID)
	assert.Equal(t, "child-turn", controls[0].NativeTurnID)
	assert.Equal(t, "child-session", child.LastSessionID())

	family.SetSessionIDForTest("next-parent-session")
	assert.Equal(t, requestID, family.Questions.publish(t.Context(), properties))
	assert.Len(t, child.PublishedControls(), 1)
	family.Questions.withdraw([]byte(`{"requestID":"que-child"}`))
	assert.Equal(t, []string{requestID}, child.CanceledControls())
	assert.Empty(t, parent.CanceledControls())
}

func TestFamilyQuestionsLeaveAnUnservedSessionAlone(t *testing.T) {
	t.Parallel()
	daemon := opencodetest.NewFakeDaemon(t, EventRoute, QuestionRoot)
	sink := &agenttest.ControlSink{}
	family := &FamilyBase{}
	family.SetSessionIDForTest("parent-session")
	family.configureQuestions(agent.NewProviderServices(sink))
	family.Daemon().setBaseURL(daemon.Server.URL)

	assert.Empty(t, family.Questions.publish(t.Context(), []byte(opencodetest.FakeQuestion)))
	assert.Empty(t, sink.PublishedControls())
	assert.Empty(t, daemon.Answers())
}

func TestFamilyBaseSessionControlOwnerUsesWrappedSink(t *testing.T) {
	t.Parallel()
	rawSink := &agenttest.ControlSink{}
	rawServices := agent.NewProviderServices(rawSink)
	wrappedServices := agent.NewModelProgressResetSink(rawServices)

	family := &FamilyBase{}
	family.SetSessionIDForTest("parent-session")
	family.SetSinkForTest(wrappedServices)
	family.configureQuestions(rawServices)

	owner, err := family.sessionControlOwner("parent-session")
	require.NoError(t, err)
	require.NoError(t, owner.PublishControlRequest(agent.ControlRequest{RequestID: "q-1", Payload: []byte(`{}`)}))

	var sawReset bool
	for _, update := range rawSink.ProgressUpdates() {
		if update.Operation == agent.ProgressModelReset {
			sawReset = true
			break
		}
	}
	assert.True(t, sawReset, "root question publication must route through modelProgressResetControl")
}

func TestDaemonReadyRunsOnceTheAddressIsKnown(t *testing.T) {
	t.Parallel()
	daemon := opencodetest.NewFakeDaemon(t, EventRoute, QuestionRoot)
	daemon.HandleFunc("/member/route", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"answer":42}`)
	})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	f := &FamilyBase{}
	ready := make(chan struct{})
	f.DaemonReady = func(context.Context) { close(ready) }
	_, err := f.Daemon().Get(ctx, "/member/route")
	require.ErrorContains(t, err, "daemon address", "no route before the address is known")
	f.BeginQuestionsForTest(ctx, agent.NewProviderServices(&agenttest.ControlSink{}), "agent-1", daemon.Server.URL)
	select {
	case <-ready:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the bridge did not report the daemon address")
	}
	require.NoError(t, f.Daemon().WaitReady(ctx))
	raw, err := f.Daemon().Get(ctx, "/member/route")
	require.NoError(t, err)
	assert.JSONEq(t, `{"answer":42}`, string(raw))
	_, err = f.Daemon().Get(ctx, "/missing/route")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestFamilyDaemonWaitReadyAndIdempotency(t *testing.T) {
	t.Parallel()

	t.Run("unblocks on unavailable with ErrDaemonUnavailable", func(t *testing.T) {
		t.Parallel()
		var d FamilyDaemon
		done := make(chan error, 1)
		go func() { done <- d.WaitReady(t.Context()) }()
		d.MarkUnavailableForTest(errors.New("discovery timed out"))
		err := <-done
		require.ErrorIs(t, err, ErrDaemonUnavailable)
		assert.Contains(t, err.Error(), "discovery timed out")
		// Repeated transitions are no-ops and do not panic.
		assert.False(t, d.markUnavailable(nil))
		assert.False(t, d.markReady("http://127.0.0.1:1"))
	})

	t.Run("markReady wakes a waiting caller and is idempotent", func(t *testing.T) {
		t.Parallel()
		var d FamilyDaemon
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.ErrorIs(t, d.WaitReady(ctx), context.Canceled)

		waiting := make(chan error, 1)
		go func() { waiting <- d.WaitReady(t.Context()) }()
		testutil.RequireEventually(t, func() bool {
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.readyCh != nil
		})
		assert.True(t, d.markReady("http://127.0.0.1:9999"))
		require.NoError(t, <-waiting)
		assert.False(t, d.markReady("http://127.0.0.1:9999"))
		require.NoError(t, d.WaitReady(t.Context()))
	})

	t.Run("MarkUnavailableForTest with nil cause returns ErrDaemonUnavailable", func(t *testing.T) {
		t.Parallel()
		var d FamilyDaemon
		d.MarkUnavailableForTest(nil)
		err := d.WaitReady(t.Context())
		require.ErrorIs(t, err, ErrDaemonUnavailable)
		assert.Equal(t, ErrDaemonUnavailable, err)
	})
}

func TestFamilyDaemonGetPagePostAndSizeCap(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			assert.Equal(t, http.MethodGet, r.Method)
			w.Header().Set(openCodeNextCursorHeader, "cursor-2")
			_, _ = fmt.Fprint(w, `[{"id":"m1"}]`)
		case "/submit":
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			body, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			assert.JSONEq(t, `{"ok":true}`, string(body))
			_, _ = fmt.Fprint(w, `{"saved":true}`)
		case "/oversized":
			_, _ = io.WriteString(w, strings.Repeat("a", openCodeQuestionMaxEvent+1))
		default:
			http.Error(w, "missing", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)

	var d FamilyDaemon
	d.markReady(server.URL)

	raw, cursor, err := d.GetPage(t.Context(), "/page")
	require.NoError(t, err)
	assert.JSONEq(t, `[{"id":"m1"}]`, string(raw))
	assert.Equal(t, "cursor-2", cursor)

	posted, err := d.Post(t.Context(), "/submit", json.RawMessage(`{"ok":true}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"saved":true}`, string(posted))

	_, err = d.Post(t.Context(), "/reject", json.RawMessage(`{}`))
	require.ErrorContains(t, err, "400")

	_, err = d.Get(t.Context(), "/oversized")
	require.ErrorContains(t, err, "more than")
}

// parkingControlSink parks the first CancelControlRequestInstance, so the
// bridge's cancelAll -- and with it the whole bridge goroutine -- stays inside
// the answer a test controls.
type parkingControlSink struct {
	*agenttest.Sink
	release chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (s *parkingControlSink) CancelControlRequestInstance(requestID, instanceID string) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
}

// The hook the family binds must join the bridge: the run loop defers
// cancelAll, whose writes go through CancelControlRequestInstance, and
// Base.Wait calls the hook after it admits its final cleanup.
func TestAfterWaitCleanupJoinsTheQuestionBridge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var releaseOnce sync.Once
		releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(releaseFn)
		sink := &parkingControlSink{Sink: &agenttest.Sink{}, release: release, entered: make(chan struct{})}
		f := &FamilyBase{}
		f.SetSinkForTest(agent.NewProviderServices(sink))
		f.Questions.Configure(agent.NewProviderServices(sink))

		// The pending question is what cancelAll answers for, so it is in place
		// before the bridge can reach its deferred cancelAll.
		f.Questions.mu.Lock()
		f.Questions.pending = map[string]*openCodePendingQuestion{
			"req-1": {requestID: "req-1", instanceID: "inst-1", nativeID: "daemon-1", owner: f.Questions.sink},
		}
		f.Questions.mu.Unlock()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		f.Questions.beginAt(ctx, testutil.NewQuartzMock(t), "agent-1", "http://127.0.0.1:1")
		// The run loop sees the ended context on its first turn and falls into
		// its deferred cancelAll with the pending question above.
		cancel()

		select {
		case <-sink.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("the bridge never reached cancelAll")
		}

		joined := make(chan struct{})
		go func() {
			FamilyHooks(f).AfterWaitCleanup()
			close(joined)
		}()
		synctest.Wait()
		select {
		case <-joined:
			t.Fatal("AfterWaitCleanup returned while cancelAll was parked in the sink")
		default:
		}

		releaseFn()
		select {
		case <-joined:
		case <-time.After(5 * time.Second):
			t.Fatal("AfterWaitCleanup did not return after the release")
		}

	})
}

// A bridge whose discovery fails ends its goroutine all the same, and the
// join answers.
func TestWaitReturnsAfterDiscoveryFails(t *testing.T) {
	f := &FamilyBase{}
	f.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f.Questions.Begin(ctx, testutil.NewQuartzMock(t), "agent-1", 1)

	joined := make(chan struct{})
	go func() {
		f.Questions.wait()
		close(joined)
	}()
	select {
	case <-joined:
	case <-time.After(5 * time.Second):
		t.Fatal("wait never returned after the discovery failed")
	}
}

// A member whose bridge never started joins nothing.
func TestWaitWithoutABridge(t *testing.T) {
	f := &FamilyBase{}
	f.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))

	joined := make(chan struct{})
	go func() {
		f.Questions.wait()
		close(joined)
	}()
	select {
	case <-joined:
	case <-time.After(5 * time.Second):
		t.Fatal("wait without a bridge must return at once")
	}
}
