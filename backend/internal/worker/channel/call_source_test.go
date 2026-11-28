package channel

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// errorWriter records the code and the message of each error that a dispatch
// answers with. mu guards both slices, because DispatchAsync answers from its
// own goroutine.
type errorWriter struct {
	recordingWriter
	mu       sync.Mutex
	codes    []int32
	messages []string
}

func (w *errorWriter) SendError(code int32, message string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.codes = append(w.codes, code)
	w.messages = append(w.messages, message)
	return nil
}

func (w *errorWriter) refusals() ([]int32, []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]int32(nil), w.codes...), append([]string(nil), w.messages...)
}

// relayCaller is the caller of a channel that the sibling Worker worker-A
// opened to relay the calls of its tabs.
func relayCaller() Caller {
	caller := NewCaller(userid.MustNew("user-1"), authscope.UnscopedGrant())
	caller.RelayWorkerID = "worker-A"
	return caller
}

// sourceRequest is a request that states source.
func sourceRequest(source *leapmuxv1.CallSource) *leapmuxv1.InnerRpcRequest {
	return &leapmuxv1.InnerRpcRequest{Method: "inspect", Source: source}
}

// dispatchCapturing dispatches req as caller through DispatchWith and returns
// the caller that the handler received, or false when the handler never ran.
func dispatchCapturing(t *testing.T, caller Caller, req *leapmuxv1.InnerRpcRequest, w ResponseWriter) (Caller, bool) {
	t.Helper()
	d := NewDispatcher()
	var got Caller
	ran := false
	d.Handle("inspect", Registration{}, func(_ context.Context, c Caller, _ *leapmuxv1.InnerRpcRequest, _ ResponseWriter) {
		got, ran = c, true
	})
	d.DispatchWith(context.Background(), caller, req, w)
	return got, ran
}

// A call source states which tab of a sibling Worker made a relayed call. Only
// a channel that a sibling Worker opened may state one: a browser or a CLI that
// could state it would claim to be an agent of its choosing.
func TestDispatcher_RefusesACallSourceOnAChannelThatNoSiblingOpened(t *testing.T) {
	t.Parallel()
	source := &leapmuxv1.CallSource{TabId: "agent-on-A", Agent: &leapmuxv1.CallSourceAgent{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, Title: "Fix flaky spec",
	}}
	callers := map[string]Caller{
		"the browser":       NewCaller(userid.MustNew("user-1"), authscope.NonAdminGrant()),
		"a local IPC token": LocalAgentCaller(userid.MustNew("user-1")),
	}
	for name, caller := range callers {
		t.Run(name+" through DispatchWith", func(t *testing.T) {
			t.Parallel()
			w := &errorWriter{}
			_, ran := dispatchCapturing(t, caller, sourceRequest(source), w)
			assert.False(t, ran, "a refused source never reaches the handler")
			got, messages := w.refusals()
			assert.Equal(t, []int32{int32(codes.InvalidArgument)}, got)
			require.Len(t, messages, 1)
			assert.Contains(t, messages[0], "only a sibling Worker")
		})
		t.Run(name+" through DispatchAsync", func(t *testing.T) {
			t.Parallel()
			d := NewDispatcher()
			ran := make(chan struct{}, 1)
			d.Handle("inspect", Registration{}, func(context.Context, Caller, *leapmuxv1.InnerRpcRequest, ResponseWriter) {
				ran <- struct{}{}
			})
			w := &errorWriter{}
			d.DispatchAsync(context.Background(), caller, sourceRequest(source), w)
			require.True(t, d.WaitHandlers(30*time.Second), "the dispatch must finish")
			select {
			case <-ran:
				t.Fatal("a refused source never reaches the handler")
			default:
			}
			got, _ := w.refusals()
			assert.Equal(t, []int32{int32(codes.InvalidArgument)}, got)
		})
	}
}

// On a relay channel the source becomes the caller's: the handler stamps the
// tab of the sibling Worker, and the agent that the sibling stated, as it
// stamps a local tab.
func TestDispatcher_ReadsTheCallSourceOfARelayedCall(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		source    *leapmuxv1.CallSource
		wantTabID string
		wantAgent SourceAgent
	}{
		{
			name: "an agent tab",
			source: &leapmuxv1.CallSource{TabId: "agent-on-A", Agent: &leapmuxv1.CallSourceAgent{
				Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, Title: "Fix flaky spec",
			}},
			wantTabID: "agent-on-A",
			wantAgent: SourceAgent{Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, Title: "Fix flaky spec"},
		},
		{
			name:      "an agent with no title",
			source:    &leapmuxv1.CallSource{TabId: "agent-on-A", Agent: &leapmuxv1.CallSourceAgent{Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX}},
			wantTabID: "agent-on-A",
			wantAgent: SourceAgent{Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX},
		},
		{name: "a terminal tab", source: &leapmuxv1.CallSource{TabId: "term-on-A"}, wantTabID: "term-on-A"},
		// A quake terminal of the sibling has no tab, so the sibling states no
		// source at all.
		{name: "no tab", source: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := &errorWriter{}
			got, ran := dispatchCapturing(t, relayCaller(), sourceRequest(tc.source), w)
			require.True(t, ran)
			codes, _ := w.refusals()
			assert.Empty(t, codes)
			assert.Equal(t, tc.wantTabID, got.SourceTabID)
			assert.Equal(t, tc.wantAgent, got.SourceAgent)
			assert.Equal(t, "worker-A", got.RelayWorkerID, "the caller keeps the relay")
			assert.Equal(t, "user-1", got.UserID.String())
		})
	}
}

// A source that specifies no tab, or an agent of no known provider, cannot be
// stamped, so the call fails instead of running with half a provenance.
func TestDispatcher_RefusesAMalformedCallSource(t *testing.T) {
	t.Parallel()
	cases := map[string]*leapmuxv1.CallSource{
		"no tab":                  {Agent: &leapmuxv1.CallSourceAgent{Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}},
		"an agent of no provider": {TabId: "agent-on-A", Agent: &leapmuxv1.CallSourceAgent{Title: "Fix flaky spec"}},
		"an agent of an unknown provider": {TabId: "agent-on-A", Agent: &leapmuxv1.CallSourceAgent{
			Provider: leapmuxv1.AgentProvider(9999),
		}},
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := &errorWriter{}
			_, ran := dispatchCapturing(t, relayCaller(), sourceRequest(source), w)
			assert.False(t, ran)
			got, _ := w.refusals()
			assert.Equal(t, []int32{int32(codes.InvalidArgument)}, got)
		})
	}
}
