package opencode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/kilo/kilotest"
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
	daemon := kilotest.NewFakeDaemon(t, EventRoute, QuestionRoot)
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
