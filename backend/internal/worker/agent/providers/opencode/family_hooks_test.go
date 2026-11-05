package opencode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodetest"
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
	_, err := f.ReadDaemon(ctx, "/member/route")
	require.ErrorContains(t, err, "daemon address", "no route before the address is known")
	f.BeginQuestionsForTest(ctx, agent.NewProviderServices(&agenttest.ControlSink{}), "agent-1", daemon.Server.URL)
	select {
	case <-ready:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the bridge did not report the daemon address")
	}
	raw, err := f.ReadDaemon(ctx, "/member/route")
	require.NoError(t, err)
	assert.JSONEq(t, `{"answer":42}`, string(raw))
	_, err = f.ReadDaemon(ctx, "/missing/route")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

// parkingControlSink parks the first CancelControlRequest, so the bridge's
// cancelAll -- and with it the whole bridge goroutine -- stays inside the
// answer a test controls.
type parkingControlSink struct {
	*agenttest.Sink
	release chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (s *parkingControlSink) CancelControlRequest(requestID string) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
}

// The hook the family binds must join the bridge: the run loop defers
// cancelAll, whose writes go through CancelControlRequest, and Base.Wait calls
// the hook after it admits its final cleanup.
func TestAfterWaitCleanupJoinsTheQuestionBridge(t *testing.T) {
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
	f.Questions.pending = map[string]string{"req-1": "daemon-1"}
	f.Questions.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
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
	select {
	case <-joined:
		t.Fatal("AfterWaitCleanup returned while cancelAll was parked in the sink")
	case <-time.After(50 * time.Millisecond):
	}

	releaseFn()
	select {
	case <-joined:
	case <-time.After(5 * time.Second):
		t.Fatal("AfterWaitCleanup did not return after the release")
	}
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
