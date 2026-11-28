package opencode

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeProviderSupportsFork(t *testing.T) {
	t.Parallel()
	assert.True(t, Registration().Plugin.SupportsFork())
}

func TestOpenCodeRPCForkSession(t *testing.T) {
	t.Parallel()

	t.Run("posts to /api/session/<id>/fork and adopts new session ID", func(t *testing.T) {
		t.Parallel()
		var called atomic.Int32
		rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
			called.Add(1)
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/api/session/ses_source/fork", r.URL.Path)
			_, _ = w.Write([]byte(`{"data":{"id":"ses_forked","agent":"build","model":{"providerID":"anthropic","id":"claude-sonnet-4"},"location":{"directory":"/work"}}}`))
		})

		info, err := rpc.forkSession(context.Background(), "ses_source")
		require.NoError(t, err)
		assert.EqualValues(t, 1, called.Load())
		assert.Equal(t, "ses_forked", info.ID)
		assert.Equal(t, "build", info.Agent)
		require.NotNil(t, info.Model)
		assert.Equal(t, "anthropic", info.Model.ProviderID)
		assert.Equal(t, "claude-sonnet-4", info.Model.ID)
	})

	t.Run("rejects invalid source session ID without sending a request", func(t *testing.T) {
		t.Parallel()
		var called atomic.Int32
		rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
			called.Add(1)
			w.WriteHeader(http.StatusOK)
		})
		_, err := rpc.forkSession(context.Background(), "../escape")
		require.Error(t, err)
		assert.Zero(t, called.Load())
	})

	t.Run("rejects fork reply returning source ID or empty ID", func(t *testing.T) {
		t.Parallel()
		for _, body := range []string{
			`{"data":{"id":"ses_source"}}`,
			`{"data":{"id":""}}`,
			`{"data":null}`,
		} {
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			})
			_, err := rpc.forkSession(context.Background(), "ses_source")
			require.Error(t, err, body)
		}
	})

	t.Run("openRootSession routes ForkFromSessionID to fork endpoint", func(t *testing.T) {
		t.Parallel()
		rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/api/session/ses_orig/fork", r.URL.Path)
			_, _ = w.Write([]byte(`{"data":{"id":"ses_new","agent":"build","location":{"directory":"/work"}}}`))
		})
		a := &Agent{rpc: rpc}
		info, err := a.openRootSession(context.Background(), agent.Options{
			WorkingDir:        "/work",
			ForkFromSessionID: "ses_orig",
		})
		require.NoError(t, err)
		assert.Equal(t, "ses_new", info.ID)
	})

	t.Run("openRootSession rejects simultaneous resume and fork", func(t *testing.T) {
		t.Parallel()
		a := &Agent{}
		_, err := a.openRootSession(context.Background(), agent.Options{
			ResumeSessionID:   "ses_a",
			ForkFromSessionID: "ses_b",
		})
		require.Error(t, err)
	})
}
