//go:build unix

package worker

import (
	"bytes"
	"context"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// A CLI that no longer waits for its helper sends it an interrupt or a
// termination signal. The helper's context ends then, so the helper can stop
// its work and answer. The helper holds the signals while it runs, so neither
// one ends this test process. Not parallel: the signal reaches the whole
// process.
func TestRunAgentHelperEndsTheContextAtASignal(t *testing.T) {
	getenv := func(key string) string {
		if key == contracts.EnvAgentHelper {
			return "/spec.json"
		}
		return ""
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			code, handled := runAgentHelper(nil, getenv, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{},
				func(ctx context.Context, _ string, _ agent.HelperInvocation) int {
					require.NoError(t, ctx.Err())
					require.NoError(t, syscall.Kill(os.Getpid(), sig))
					select {
					case <-ctx.Done():
						return 5
					case <-testutil.DeadlineContext(t).Done():
						t.Error("the signal did not end the helper's context")
						return 0
					}
				})
			assert.True(t, handled)
			assert.Equal(t, 5, code, "the helper answers after its context ends")
		})
	}
}
