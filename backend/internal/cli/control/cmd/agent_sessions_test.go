package cmd

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// sessionsWorker is a fake Worker that offers providers and answers
// ListAgentSessions from a table. It records each request. mu guards every
// field.
type sessionsWorker struct {
	mu        sync.Mutex
	providers []leapmuxv1.AgentProvider
	sessions  []*leapmuxv1.AgentSessionSummary
	// refuse, when set, is the InvalidArgument message that
	// ListAgentSessions answers instead.
	refuse   string
	requests []*leapmuxv1.ListAgentSessionsRequest
	methods  []string
}

func (w *sessionsWorker) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.methods = append(w.methods, req.GetMethod())
	switch req.GetMethod() {
	case leapmuxv1connect.WorkerAgentServiceListAvailableProvidersProcedure:
		writeProtoResponse(rw, &leapmuxv1.ListAvailableProvidersResponse{Providers: w.providers})
	case leapmuxv1connect.WorkerAgentServiceListAgentSessionsProcedure:
		var in leapmuxv1.ListAgentSessionsRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		w.requests = append(w.requests, &in)
		if w.refuse != "" {
			_ = rw.SendError(int32(codes.InvalidArgument), w.refuse)
			return
		}
		writeProtoResponse(rw, &leapmuxv1.ListAgentSessionsResponse{Sessions: w.sessions})
	default:
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+req.GetMethod())
	}
}

func (w *sessionsWorker) recorded() ([]*leapmuxv1.ListAgentSessionsRequest, []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.requests), slices.Clone(w.methods)
}

// startSessionsWorker serves w as the Worker of the tab that runs the
// command, as an agent tab gives it.
func startSessionsWorker(t *testing.T, w *sessionsWorker) {
	t.Helper()
	startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, w)
	t.Setenv("LEAPMUX_CONTROL_WORKER_ID", "worker-A")
}

// The newest session first, as the Worker answers.
var listedSessions = []*leapmuxv1.AgentSessionSummary{
	{SessionId: "4f6c1e0a-9b7d-4c2e-8f3a-2d5b6c7e8f90", Title: "Fix the flaky spec", UpdatedAt: "2026-11-19T07:00:00.000Z"},
	{SessionId: "0a1b2c3d-0000-4000-8000-000000000001", UpdatedAt: "2026-11-18T07:00:00.000Z"},
	{SessionId: "0a1b2c3d-0000-4000-8000-000000000002", Title: "Started in a terminal"},
}

func TestAgentSessionsOverIPC_ListsTheSessionsThatResumeTakes(t *testing.T) {
	w := &sessionsWorker{sessions: listedSessions}
	startSessionsWorker(t, w)

	data := runControl(t, "agent", "sessions", "--provider", "claude", "--dir", "/src/app").Data(t)
	assert.Equal(t, []any{
		map[string]any{"id": "4f6c1e0a-9b7d-4c2e-8f3a-2d5b6c7e8f90", "title": "Fix the flaky spec", "updated_at": "2026-11-19T07:00:00.000Z"},
		map[string]any{"id": "0a1b2c3d-0000-4000-8000-000000000001", "updated_at": "2026-11-18T07:00:00.000Z"},
		map[string]any{"id": "0a1b2c3d-0000-4000-8000-000000000002", "title": "Started in a terminal"},
	}, data["sessions"], "the sessions keep the Worker's order, newest first")
	requests, methods := w.recorded()
	require.Len(t, requests, 1)
	assert.True(t, proto.Equal(&leapmuxv1.ListAgentSessionsRequest{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, WorkingDir: "/src/app"}, requests[0]), "request: %v", requests[0])
	assert.NotContains(t, methods, leapmuxv1connect.WorkerAgentServiceListAvailableProvidersProcedure, "a specified provider needs no lookup")

	o := runControlText(t, "agent", "sessions", "--provider", "claude", "--dir", "/src/app")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Contains(t, o.Stdout, "ID"+strings.Repeat(" ", 36)+"UPDATED", "the id column is as wide as the whole id")
	assert.Contains(t, o.Stdout, "4f6c1e0a-9b7d-4c2e-8f3a-2d5b6c7e8f90  2026-11-19T07:00:00.000Z  Fix the flaky spec", "the table prints the whole id, which --resume takes")

	o = runControlText(t, "agent", "sessions", "--provider", "claude", "--dir", "/src/app", "-q")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, "4f6c1e0a-9b7d-4c2e-8f3a-2d5b6c7e8f90\n0a1b2c3d-0000-4000-8000-000000000001\n0a1b2c3d-0000-4000-8000-000000000002\n", o.Stdout)
}

// Inside a tab, the provider and the directory default to the tab's, as they
// do for `agent open`. Without a provider anywhere, the Worker's only
// provider applies.
func TestAgentSessionsOverIPC_TakesTheDefaultsOfAgentOpen(t *testing.T) {
	t.Run("the variables of the tab", func(t *testing.T) {
		w := &sessionsWorker{}
		startSessionsWorker(t, w)
		t.Setenv("LEAPMUX_CONTROL_AGENT_PROVIDER", "codex")
		t.Setenv("LEAPMUX_CONTROL_WORKING_DIR", "/src/tab")

		runControl(t, "agent", "sessions").Data(t)
		requests, _ := w.recorded()
		require.Len(t, requests, 1)
		assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, requests[0].GetAgentProvider())
		assert.Equal(t, "/src/tab", requests[0].GetWorkingDir())
	})
	t.Run("the only provider of the worker", func(t *testing.T) {
		w := &sessionsWorker{providers: []leapmuxv1.AgentProvider{leapmuxv1.AgentProvider_AGENT_PROVIDER_PI}}
		startSessionsWorker(t, w)

		runControl(t, "agent", "sessions", "--dir", "/src/app").Data(t)
		requests, methods := w.recorded()
		require.Len(t, requests, 1)
		assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, requests[0].GetAgentProvider())
		assert.Equal(t, leapmuxv1connect.WorkerAgentServiceListAvailableProvidersProcedure, methods[0])
	})
	t.Run("a flag wins over the variable", func(t *testing.T) {
		w := &sessionsWorker{}
		startSessionsWorker(t, w)
		t.Setenv("LEAPMUX_CONTROL_AGENT_PROVIDER", "codex")
		t.Setenv("LEAPMUX_CONTROL_WORKING_DIR", "/src/tab")

		runControl(t, "agent", "sessions", "--provider", "claude", "--dir", "/src/flag").Data(t)
		requests, _ := w.recorded()
		require.Len(t, requests, 1)
		assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, requests[0].GetAgentProvider())
		assert.Equal(t, "/src/flag", requests[0].GetWorkingDir())
	})
	t.Run("padded provider variable is trimmed", func(t *testing.T) {
		w := &sessionsWorker{}
		startSessionsWorker(t, w)
		t.Setenv("LEAPMUX_CONTROL_AGENT_PROVIDER", "  codex  ")
		t.Setenv("LEAPMUX_CONTROL_WORKING_DIR", "/src/tab")

		runControl(t, "agent", "sessions").Data(t)
		requests, methods := w.recorded()
		require.Len(t, requests, 1)
		assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, requests[0].GetAgentProvider())
		assert.NotContains(t, methods, leapmuxv1connect.WorkerAgentServiceListAvailableProvidersProcedure)
	})
	t.Run("whitespace-only provider variable falls back to the worker's sole provider", func(t *testing.T) {
		w := &sessionsWorker{providers: []leapmuxv1.AgentProvider{leapmuxv1.AgentProvider_AGENT_PROVIDER_PI}}
		startSessionsWorker(t, w)
		t.Setenv("LEAPMUX_CONTROL_AGENT_PROVIDER", "   ")
		t.Setenv("LEAPMUX_CONTROL_WORKING_DIR", "/src/tab")

		runControl(t, "agent", "sessions").Data(t)
		requests, methods := w.recorded()
		require.Len(t, requests, 1)
		assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, requests[0].GetAgentProvider())
		assert.Equal(t, leapmuxv1connect.WorkerAgentServiceListAvailableProvidersProcedure, methods[0])
	})
	t.Run("saved context provider is trimmed when flag and variable are unset", func(t *testing.T) {
		w := &sessionsWorker{}
		startSessionsWorker(t, w)
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
		t.Setenv("LEAPMUX_CONTROL_AGENT_PROVIDER", "")
		t.Setenv("LEAPMUX_CONTROL_WORKING_DIR", "/src/tab")
		require.NoError(t, control.SaveContext(control.ContextConfig{Provider: "  codex  "}))

		runControl(t, "agent", "sessions").Data(t)
		requests, methods := w.recorded()
		require.Len(t, requests, 1)
		assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, requests[0].GetAgentProvider())
		assert.NotContains(t, methods, leapmuxv1connect.WorkerAgentServiceListAvailableProvidersProcedure)
	})
}

func TestAgentSessionsOverIPC_SaysWhenNoSessionExists(t *testing.T) {
	startSessionsWorker(t, &sessionsWorker{})

	data := runControl(t, "agent", "sessions", "--provider", "claude", "--dir", "/src/app").Data(t)
	assert.Equal(t, []any{}, data["sessions"], "no session prints an empty list, not null")

	o := runControlText(t, "agent", "sessions", "--provider", "claude", "--dir", "/src/app")
	require.Equal(t, cli.ExitOK, o.Code, "stderr: %s", o.Stderr)
	assert.Equal(t, "ID  UPDATED  TITLE\n", o.Stdout, "an empty table prints its header alone, as every list does")
	assert.Contains(t, o.Stderr, "Claude Code has no session to resume in /src/app")
}

func TestAgentSessionsOverIPC_Refusals(t *testing.T) {
	w := &sessionsWorker{providers: []leapmuxv1.AgentProvider{leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX}}
	startSessionsWorker(t, w)

	f := runControl(t, "agent", "sessions", "--provider", "claude").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "no directory selected")
	f = runControl(t, "agent", "sessions", "--provider", "claude", "--dir", " ").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "no directory selected")
	f = runControl(t, "agent", "sessions", "--provider", "no-such-agent", "--dir", "/src/app").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, `unknown --provider "no-such-agent"`)
	f = runControl(t, "agent", "sessions", "--dir", "/src/app").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "more than one provider")
	requests, _ := w.recorded()
	assert.Empty(t, requests, "no refused listing reached the Worker")

	w.mu.Lock()
	w.refuse = "path is empty"
	w.mu.Unlock()
	f = runControl(t, "agent", "sessions", "--provider", "claude", "--dir", "/src/app").requireFailure(t, cli.CodeInvalidArgument)
	assert.Contains(t, f.Message, "path is empty")
}
