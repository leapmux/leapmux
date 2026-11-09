package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/util/drain"
)

// awaitClosed fails the test unless ch closes before ctx ends.
func awaitClosed(t *testing.T, ctx context.Context, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-ctx.Done():
		t.Fatal(msg)
	}
}

// installMockShutdownDeadline gives svc a shutdown deadline on a mock clock of
// its own and starts it, so a test passes the deadline with one Advance and no
// other timer of the service stands in the way.
func installMockShutdownDeadline(t *testing.T, svc *Service) func(ctx context.Context) {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	svc.ShutdownDeadline = drain.NewDeadline(clock, lifecycle.ShutdownDrainTimeout)
	svc.ShutdownDeadline.Start()
	return func(ctx context.Context) {
		clock.Advance(lifecycle.ShutdownDrainTimeout).MustWait(ctx)
	}
}

// A provider that never finishes its startup handshake used to hold Shutdown
// until its own startup timeout (five minutes by default), past the SIGKILL
// that launchd and systemd send after three. At the shutdown deadline the
// drain cancels the startup instead, and Shutdown runs its remaining steps.
// The cancelled startup records no startup failure: the next boot finds the
// tab as one whose process ended with the worker.
func TestShutdownCancelsAStartupStillInFlightAtTheDeadline(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	logs := testutil.CaptureDefaultLogger(t)
	svc, d, w := setupTestService(t)

	entered := make(chan struct{})
	cancelled := make(chan struct{})
	svc.startAgentFn = func(startCtx context.Context, _ agent.Options, _ agent.ProviderServices) (map[string]string, error) {
		close(entered)
		<-startCtx.Done()
		close(cancelled)
		return nil, startCtx.Err()
	}
	dispatch(d, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var opened leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &opened))
	agentID := opened.GetAgent().GetId()
	require.NotEmpty(t, agentID)
	awaitClosed(t, ctx, entered, "the startup never reached its provider handshake")

	// startupCore's own test pins that the drain waits until the deadline;
	// this one pins what the service does once it passed.
	passDeadline := installMockShutdownDeadline(t, svc)
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		svc.Shutdown()
	}()

	passDeadline(ctx)
	awaitClosed(t, ctx, cancelled, "the deadline did not cancel the startup still in flight")
	awaitClosed(t, ctx, shutdownDone, "Shutdown did not return after the deadline cancelled the startup")

	require.Error(t, svc.Output.shutdownCtx.Err(), "Shutdown must run its later steps through to the last one")
	assert.Contains(t, logs.String(), agentID, "the drain must log the startup it cancelled")

	// The cancelled startup unwinds after Shutdown moved on. Once it did, it
	// must have left no record of a failure behind.
	svc.AgentStartup.WaitForInFlight()
	row, err := svc.getAgentByID(context.Background(), agentID)
	require.NoError(t, err)
	assert.Empty(t, row.StartupError, "a startup that the shutdown cancelled must record no startup failure")
	_, startupError, _, held := svc.AgentStartup.status(agentID)
	assert.False(t, held, "the registry must not keep a STARTUP_FAILED entry for the cancelled startup; held %q", startupError)
}

// A wait without a limit of its own ends at the shutdown deadline. A close
// handler that never returns stands for any of them.
func TestShutdownStopsWaitingForACloseHandlerAtTheDeadline(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	logs := testutil.CaptureDefaultLogger(t)
	svc, d, _ := setupTestService(t)
	d.CloseAdmission()
	svc.Cleanup.Add()
	t.Cleanup(svc.Cleanup.Done)

	passDeadline := installMockShutdownDeadline(t, svc)
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		svc.Shutdown()
	}()

	passDeadline(ctx)
	awaitClosed(t, ctx, shutdownDone, "Shutdown did not stop waiting for the close handler at the deadline")
	assert.Contains(t, logs.String(), "close handlers still ran", "Shutdown must log the close handler it abandoned")
	require.Error(t, svc.Output.shutdownCtx.Err(), "Shutdown must run its later steps through to the last one")
}

// The background loops get the same deadline: Shutdown hands its own to the
// stopper, which is the one bootstrap composes from the real loops.
func TestShutdownHandsItsDeadlineToTheBackgroundLoopStopper(t *testing.T) {
	svc, d, _ := setupTestService(t)
	d.CloseAdmission()
	var received *drain.Deadline
	svc.SetStopBackgroundLoops(func(deadline *drain.Deadline) { received = deadline })

	svc.Shutdown()

	require.NotNil(t, received)
	assert.Same(t, svc.ShutdownDeadline, received, "the stopper must wait under the service's own deadline")
}

// service.New installs a deadline when the caller passes none, so a Service
// that a test or a tool builds directly still has one. Nothing starts it, so
// every limit stays as it is.
func TestNewInstallsAnUnstartedShutdownDeadline(t *testing.T) {
	svc, _, _ := setupTestService(t)
	require.NotNil(t, svc.ShutdownDeadline)
	assert.Equal(t, lifecycle.ShutdownDrainTimeout+1, svc.ShutdownDeadline.Cap(lifecycle.ShutdownDrainTimeout+1),
		"an unstarted deadline must leave every limit as it is")
}
