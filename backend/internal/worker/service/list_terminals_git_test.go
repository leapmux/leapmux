package service

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/pathutil"
)

// ListTerminals runs git before it reads the state of each terminal, so it
// states which directory each terminal asks about before it knows which
// terminals the Manager holds. Each reply entry must still carry the status of
// its own directory, whether the shell runs (the Manager gives the directory)
// or the terminal has only a row (the row gives it).
func TestListTerminals_AttachesTheGitStatusOfEachTerminalsOwnDirectory(t *testing.T) {
	t.Parallel()

	ctx := testutil.DeadlineContext(t)
	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	// A live shell whose row names a plain directory, and whose shell start
	// directory is a repo: the start directory decides, as ResolveGitDir states.
	liveRepo := testutil.NewGitRepo(t)
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: "live", WorkingDir: t.TempDir(), HomeDir: "/tmp", ShellStartDir: liveRepo,
		Cols: 80, Rows: 24, Screen: []byte{},
	}))
	require.NoError(t, svc.Terminals.StartTerminal(ctx, terminal.Options{
		ID: "live", Shell: testutil.TestShell(), WorkingDir: t.TempDir(), ShellStartDir: liveRepo, Cols: 80, Rows: 24,
	}, func([]byte, int64, []terminal.Signal) {}, nil))
	testutil.RegisterTerminalCleanup(t, svc.Terminals, "live")

	// A terminal with only a row, in another repo.
	rowRepo := testutil.NewGitRepo(t)
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: "row-only", WorkingDir: rowRepo, HomeDir: "/tmp",
		Cols: 80, Rows: 24, Screen: []byte("screen"),
	}))

	// A terminal in a directory that is no repo has no git status.
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: "plain", WorkingDir: t.TempDir(), HomeDir: "/tmp",
		Cols: 80, Rows: 24, Screen: []byte("screen"),
	}))

	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{TabIds: []string{"live", "row-only", "plain"}}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	byID := make(map[string]*leapmuxv1.TerminalInfo, len(resp.GetTerminals()))
	for _, info := range resp.GetTerminals() {
		byID[info.GetTerminalId()] = info
	}
	require.Len(t, byID, 3)

	assertToplevel := func(id, repo string) {
		t.Helper()
		want, err := filepath.EvalSymlinks(repo)
		require.NoError(t, err)
		got := byID[id].GetGitStatus().GetToplevel()
		assert.True(t, pathutil.SamePath(want, got), "terminal %q: want the repo %q, got %q", id, want, got)
	}
	assertToplevel("live", liveRepo)
	assertToplevel("row-only", rowRepo)
	assert.Nil(t, byID["plain"].GetGitStatus(), "a directory that is no repo has no git status")
}

// A terminal that is in neither source has no entry, and the call states that
// with a verdict. The git batch that now runs first must not turn an empty set
// of directories into a failure.
func TestListTerminals_AnIdWithNoTerminalRunsNoGit(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{TabIds: []string{"missing"}}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetTerminals())
	require.Len(t, resp.GetVerdicts(), 1)
	assert.Equal(t, "missing", resp.GetVerdicts()[0].GetTabId())
	assert.Equal(t, leapmuxv1.TabHydrationStatus_TAB_HYDRATION_STATUS_ABSENT.String(), resp.GetVerdicts()[0].GetStatus().String())
}
