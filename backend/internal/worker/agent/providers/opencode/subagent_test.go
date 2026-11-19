package opencode

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeChildKeepsTheShellMetadataHook(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	a, _ := assemblyAgent(t, rpc)
	a.dispatch.Lock()
	run, err := a.installChildLocked(openCodeSessionInfo{ID: "ses_child", ParentID: "ses_1", Agent: "general"}, a.root, "spawn")
	a.dispatch.Unlock()
	require.NoError(t, err)
	a.dispatch.Lock()
	err = run.tools.metadata(&openCodeToolState{name: "shell", opened: true}, []byte(`{"shellID":"../invalid"}`))
	a.dispatch.Unlock()
	require.Error(t, err, "the child must retain native shell validation")
}

type nativeChildCloseFailure struct {
	agent.ProviderServices
	err error
}

func (services *nativeChildCloseFailure) CloseBackgroundTask(key string, status bgtask.Status) error {
	if services.err != nil {
		err := services.err
		services.err = nil
		return err
	}
	return services.ProviderServices.CloseBackgroundTask(key, status)
}

func TestNativeChildRetriesTaskClosureAfterTheDividerWasStored(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	a, sink := assemblyAgent(t, rpc)
	a.dispatch.Lock()
	run, err := a.installChildLocked(openCodeSessionInfo{ID: "ses_child", ParentID: "ses_1", Agent: "general"}, a.root, "spawn")
	a.dispatch.Unlock()
	require.NoError(t, err)
	failure := errors.New("injected task closure failure")
	run.sink = &nativeChildCloseFailure{ProviderServices: run.sink, err: failure}
	ended := assemblyEvent(t, "session.execution.succeeded", map[string]any{"sessionID": "ses_child"})
	require.ErrorIs(t, a.acceptNativeEvent(t.Context(), ended), failure)
	require.NoError(t, a.acceptNativeEvent(t.Context(), ended))
	item, ok := sink.BackgroundTask("ses_child")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, item.Status)
}

func TestNativeChildAdoptionKeepsTheExistingRun(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	a, _ := assemblyAgent(t, rpc)
	info := openCodeSessionInfo{ID: "ses_child", ParentID: "ses_1", Agent: "general"}
	a.dispatch.Lock()
	first, err := a.installChildLocked(info, a.root, "spawn")
	require.NoError(t, err)
	second, err := a.installChildLocked(info, a.root, "spawn")
	a.dispatch.Unlock()
	require.NoError(t, err)
	assert.Same(t, first, second)
}

func TestNativeChildRecoveryClosesAnAlreadyCompletedTask(t *testing.T) {
	t.Parallel()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session/ses_child":
			_, _ = w.Write([]byte(`{"data":{"id":"ses_child","parentID":"ses_1","agent":"general"}}`))
		case "/api/session/ses_child/message":
			_, _ = w.Write([]byte(`{"data":[{"id":"msg_done","type":"idle","outcome":"succeeded"}],"cursor":{}}`))
		case "/api/session/active":
			_, _ = w.Write([]byte(`{"data":{}}`))
		case "/api/session/ses_child/permission", "/api/session/ses_child/form":
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			t.Errorf("unexpected child recovery request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	a, sink := assemblyAgent(t, rpc)
	a.dispatch.Lock()
	run, err := a.installChildLocked(openCodeSessionInfo{ID: "ses_child", ParentID: "ses_1", Agent: "general"}, a.root, "spawn")
	a.dispatch.Unlock()
	require.NoError(t, err)
	require.NoError(t, a.restoreRun(t.Context(), run))
	item, ok := sink.BackgroundTask("ses_child")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, item.Status)
}

func TestNativeChildrenRequireAnOwnedAncestor(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"descendant", "foreign root", "cycle", "missing"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/message") {
					_, _ = w.Write([]byte(`{"data":[],"cursor":{}}`))
					return
				}
				id := strings.TrimPrefix(r.URL.Path, "/api/session/")
				parent := ""
				switch kind {
				case "descendant":
					if id == "ses_grandchild" {
						parent = "ses_child"
					} else {
						parent = "ses_1"
					}
				case "cycle":
					parent = "ses_child"
					if id == "ses_child" {
						parent = "ses_grandchild"
					}
				case "missing":
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_, err := fmt.Fprintf(w, `{"data":{"id":%q,"parentID":%q,"agent":"build"}}`, id, parent)
				assert.NoError(t, err)
			})
			a, sink := assemblyAgent(t, rpc)
			run, err := a.ensureOwnedRun(t.Context(), "ses_grandchild")
			if kind != "descendant" {
				require.Error(t, err)
				assert.Nil(t, run)
				assert.Empty(t, sink.ChildAgentIDs())
				assert.Len(t, a.runs, 1)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "ses_child", run.parentID)
			assert.Len(t, a.runs, 3)
			children := sink.ChildAgentIDs()
			require.Len(t, children, 1)
			childSink := sink.Child(children[0])
			assert.Len(t, childSink.ChildAgentIDs(), 1)
			var workers sync.WaitGroup
			for range 8 {
				workers.Go(func() {
					again, err := a.ensureOwnedRun(t.Context(), "ses_grandchild")
					assert.NoError(t, err)
					assert.Same(t, run, again)
				})
			}
			workers.Wait()
			assert.Equal(t, children, sink.ChildAgentIDs())
			assert.Len(t, childSink.ChildAgentIDs(), 1)
			a.dispatch.Lock()
			err = a.retireRunLocked(a.root, agent.MessageCompletionInterrupted)
			a.dispatch.Unlock()
			require.NoError(t, err)
			assert.Empty(t, a.runs)
			assert.Error(t, run.ctx.Err())
		})
	}
}
