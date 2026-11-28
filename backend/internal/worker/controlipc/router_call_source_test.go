package controlipc_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

// fakeCallSources describes the tabs of this Worker as the service does, and
// records each tab that it was asked about. mu guards asked.
type fakeCallSources struct {
	mu     sync.Mutex
	asked  []string
	source *leapmuxv1.CallSource
	err    error
}

func (f *fakeCallSources) DescribeCallSource(_ context.Context, tabID string) (*leapmuxv1.CallSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, tabID)
	return f.source, f.err
}

func (f *fakeCallSources) questions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// agentSource is the description of the agent tab agent-1 of worker-A.
func agentSource() *leapmuxv1.CallSource {
	return &leapmuxv1.CallSource{TabId: "agent-1", Agent: &leapmuxv1.CallSourceAgent{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, Title: "Fix flaky spec",
	}}
}

func relayRouter(cross *fakeCrossWorker, sources *fakeCallSources) *controlipc.Router {
	return &controlipc.Router{
		WorkerID:    "worker-A",
		UserID:      userid.MustNew("user-1"),
		CrossWorker: cross,
		Sources:     sources,
	}
}

const enqueueMethod = "worker." + leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure

// A call that a tab's process relays to a sibling Worker states the tab, as
// this Worker describes it, so the sibling stamps the same sender that this
// Worker would.
func TestRouter_CallInner_RelaysTheSourceOfTheCallingTab(t *testing.T) {
	t.Parallel()
	cross := &fakeCrossWorker{resp: []byte("from-B")}
	sources := &fakeCallSources{source: agentSource()}
	r := relayRouter(cross, sources)

	_, err := r.CallInner(context.Background(), controlipc.TokenInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-A", TabID: "agent-1"},
		enqueueMethod, []byte("hi"), "worker-B")
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-1"}, sources.questions())
	assert.True(t, proto.Equal(agentSource(), cross.callSource), "source: %v", cross.callSource)
	assert.Equal(t, []byte("hi"), cross.callPayload)
}

// A relayed stream states the source as a unary call does.
func TestRouter_StreamInner_RelaysTheSourceOfTheCallingTab(t *testing.T) {
	t.Parallel()
	cross := &fakeCrossWorker{resp: []byte("frame")}
	sources := &fakeCallSources{source: agentSource()}
	r := relayRouter(cross, sources)

	require.NoError(t, r.StreamInner(context.Background(), controlipc.TokenInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-A", TabID: "agent-1"},
		"worker."+leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, []byte("{}"), "worker-B", "",
		func(*leapmuxv1.InnerStreamMessage) error { return nil }))
	assert.True(t, proto.Equal(agentSource(), cross.callSource), "source: %v", cross.callSource)
}

// A process in no tab, such as a quake terminal, states no source, and the
// router asks nothing about a tab.
func TestRouter_CallInner_RelaysNoSourceForACallerInNoTab(t *testing.T) {
	t.Parallel()
	cross := &fakeCrossWorker{resp: []byte("from-B")}
	sources := &fakeCallSources{source: agentSource()}
	r := relayRouter(cross, sources)

	_, err := r.CallInner(context.Background(), controlipc.TokenInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-A"},
		enqueueMethod, []byte("hi"), "worker-B")
	require.NoError(t, err)
	assert.Empty(t, sources.questions())
	assert.Nil(t, cross.callSource)
}

// A call that stays on this Worker is dispatched with the token's tab, and the
// service describes it there; the router asks nothing.
func TestRouter_CallInner_DescribesNoTabForALocalCall(t *testing.T) {
	t.Parallel()
	local := &fakeLocalDispatcher{respPayload: []byte("ok")}
	sources := &fakeCallSources{source: agentSource()}
	r := &controlipc.Router{WorkerID: "worker-A", UserID: userid.MustNew("user-1"), LocalDispatcher: local, Sources: sources}

	_, err := r.CallInner(context.Background(), controlipc.TokenInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-A", TabID: "agent-1"},
		enqueueMethod, []byte("hi"), "")
	require.NoError(t, err)
	assert.Empty(t, sources.questions())
}

// A relay without its source would reach the sibling as the user's own call,
// so a tab that the Worker cannot describe fails the call, and the sibling
// receives nothing.
func TestRouter_CallInner_RefusesARelayWhoseTabItCannotDescribe(t *testing.T) {
	t.Parallel()
	for name, sources := range map[string]*fakeCallSources{
		"a failed read":    {err: errors.New("database is locked")},
		"no describer set": nil,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cross := &fakeCrossWorker{resp: []byte("from-B")}
			r := &controlipc.Router{WorkerID: "worker-A", UserID: userid.MustNew("user-1"), CrossWorker: cross}
			if sources != nil {
				r.Sources = sources
			}
			_, err := r.CallInner(context.Background(), controlipc.TokenInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-A", TabID: "agent-1"},
				enqueueMethod, []byte("hi"), "worker-B")
			require.Error(t, err)
			assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
			assert.Zero(t, cross.calls, "the sibling receives nothing")

			streamErr := r.StreamInner(context.Background(), controlipc.TokenInfo{UserID: userid.MustNew("user-1"), WorkerID: "worker-A", TabID: "agent-1"},
				"worker."+leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, []byte("{}"), "worker-B", "",
				func(*leapmuxv1.InnerStreamMessage) error { return nil })
			require.Error(t, streamErr)
			assert.Equal(t, connect.CodeInternal, connect.CodeOf(streamErr))
			assert.Zero(t, cross.calls)
		})
	}
}

func TestRouter_CallInner_TerminalTabSkipsAgentSourceLookup(t *testing.T) {
	t.Parallel()
	cross := &fakeCrossWorker{resp: []byte("from-B")}
	sources := &fakeCallSources{err: errors.New("must not query agents for terminal tab")}
	r := relayRouter(cross, sources)

	_, err := r.CallInner(context.Background(), controlipc.TokenInfo{
		UserID:   userid.MustNew("user-1"),
		WorkerID: "worker-A",
		TabID:    "term-1",
		TabType:  leapmuxv1.TabType_TAB_TYPE_TERMINAL,
	}, enqueueMethod, []byte("hi"), "worker-B")
	require.NoError(t, err)
	assert.Empty(t, sources.questions())
	assert.True(t, proto.Equal(&leapmuxv1.CallSource{TabId: "term-1"}, cross.callSource))
}
