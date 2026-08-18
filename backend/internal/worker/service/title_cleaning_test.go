package service

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"google.golang.org/grpc/codes"
)

// The five writers of a `title` column in this package -- OpenAgent,
// RenameAgent, UpdateTerminalTitle, EnsureChildAgent and UpsertBackgroundTask
// -- clean the title they are given and never refuse it. The table below drives
// all five, because one rule that five writers share is the property these
// tests exist to hold: a case added here has to pass for every writer, and a
// writer that skips validate.CleanName fails on the first case.
//
// The first three take the title from a CLIENT; the last two take it from the
// MODEL (a Claude Task description, a Codex collab prompt line, an ACP spawn
// label, a Pi subagent title). The source changes nothing about the rule --
// neither one has a user who can read an error and correct it.
//
// The plan auto-rename applies the same rule at its own source; see
// TestExtractPlanTitleCapsBytes in the agent package.
var titleCleaningCases = []struct {
	name  string
	title string
	want  string
}{
	{
		// 50 CJK characters is 50 characters and 150 bytes. A rune-based cap
		// accepts it and the byte-based one does not, so this is the case that
		// tells the two rules apart -- and the case the RPC used to REFUSE.
		name:  "over-long CJK title is cut to the byte limit",
		title: strings.Repeat("一", 50),
		want:  strings.Repeat("一", 42),
	},
	{
		name:  "over-long ASCII title is cut to the byte limit",
		title: strings.Repeat("a", 200),
		want:  strings.Repeat("a", 128),
	},
	{
		name:  "a control character is stripped",
		title: "Hello\x00World",
		want:  "HelloWorld",
	},
	{
		name:  "the templating characters are stripped",
		title: "100% of $HOME \"quoted\" c:\\path",
		want:  "100 of HOME quoted c:path",
	},
	{
		name:  "surrounding whitespace is trimmed",
		title: "   Deploy the hub   ",
		want:  "Deploy the hub",
	},
}

// emptyingTitle cleans to nothing: every character is stripped or trimmed. It
// is the input each handler answers with its own fallback.
const emptyingTitle = "  $$%%  "

// lastResponse decodes the newest response the writer captured into msg. The
// title tests need the reply body, not only the absence of an error: a handler
// that stores the right title and reports a different one is exactly the defect
// the `title` response field exists to prevent.
func lastResponse(t *testing.T, w *testResponseWriter, msg proto.Message) {
	t.Helper()

	w.mu.Lock()
	defer w.mu.Unlock()
	require.NotEmpty(t, w.responses, "the handler sent no response")
	require.NoError(t, proto.Unmarshal(w.responses[len(w.responses)-1].GetPayload(), msg))
}

// openAgentWithTitle dispatches OpenAgent and returns the title of the row it
// created.
func openAgentWithTitle(t *testing.T, title string) string {
	t.Helper()

	repoDir := initRepo(t)
	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir: repoDir,
		Title:      title,
	}, w)

	require.Empty(t, w.errors, "a title must never fail the RPC")
	require.Len(t, w.responses, 1)

	ids, err := svc.Queries.ListAllAgentIDs(context.Background())
	require.NoError(t, err)
	require.Len(t, ids, 1, "the spawn must create exactly one agent row")
	row, err := svc.Queries.GetAgentByID(context.Background(), ids[0])
	require.NoError(t, err)
	return row.Title
}

func TestOpenAgent_CleansTitleInsteadOfRefusingIt(t *testing.T) {
	t.Parallel()

	for _, tc := range titleCleaningCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, openAgentWithTitle(t, tc.title))
		})
	}
}

// A title that cleaning empties leaves OpenAgent with no title at all, so it
// falls back to the same random "Agent <Name>" it picks when the client sends
// no title. Writing "" instead would leave the tab with a blank label.
func TestOpenAgent_PicksANameWhenCleaningEmptiesTheTitle(t *testing.T) {
	t.Parallel()

	title := openAgentWithTitle(t, emptyingTitle)
	assert.NotEmpty(t, title, "an emptied title must not reach the column")
	assert.True(t, strings.HasPrefix(title, "Agent "),
		"the fallback is pickAgentTitle(), which prefixes the pooled name with %q, got %q", "Agent ", title)
}

// subscribeTabRenames registers a subscriber on the worker-private bus and
// returns the channel that every TabRenamed event lands on.
//
// The seam is the snapshot callback: SnapshotAndSubscribe invokes it right
// after it commits the registration, so waiting for it waits on the state that
// ends the race. A sleep here would be a window sized by a real timer, which
// holds on an idle laptop and misses under a loaded -race run.
func subscribeTabRenames(t *testing.T, svc *Service) <-chan *leapmuxv1.TabRenamed {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	renames := make(chan *leapmuxv1.TabRenamed, 8)
	registered := make(chan struct{})
	go func() {
		_ = svc.PrivateEvents.SnapshotAndSubscribe(ctx, userid.MustNew("user-1"),
			func(userid.UserID) []*leapmuxv1.WorkerPrivateEvent {
				close(registered)
				return nil
			},
			func(evt *leapmuxv1.WorkerPrivateEvent) error {
				if renamed := evt.GetTabRenamed(); renamed != nil {
					renames <- renamed
				}
				return nil
			})
	}()
	select {
	case <-registered:
	case <-time.After(10 * time.Second):
		t.Fatal("the private-events subscriber never registered")
	}
	return renames
}

// nextRename returns the next TabRenamed event, failing the test if none
// arrives.
func nextRename(t *testing.T, renames <-chan *leapmuxv1.TabRenamed) *leapmuxv1.TabRenamed {
	t.Helper()

	select {
	case renamed := <-renames:
		return renamed
	case <-time.After(10 * time.Second):
		t.Fatal("no TabRenamed event arrived")
		return nil
	}
}

func TestRenameAgent_CleansTitleInsteadOfRefusingIt(t *testing.T) {
	t.Parallel()

	for _, tc := range titleCleaningCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, d, w := setupTestService(t)
			seedAgent(t, svc, "agent-1")
			renames := subscribeTabRenames(t, svc)

			dispatch(d, "RenameAgent", &leapmuxv1.RenameAgentRequest{
				AgentId: "agent-1",
				Title:   tc.title,
			}, w)

			require.Empty(t, w.errors, "a title must never fail the RPC")
			require.Len(t, w.responses, 1)

			row, err := svc.Queries.GetAgentByID(context.Background(), "agent-1")
			require.NoError(t, err)
			assert.Equal(t, tc.want, row.Title, "the column holds the cleaned title")

			// The reply reports the STORED title. A control-CLI caller or a
			// script does not apply the cleaning rule locally, so echoing the
			// request's title back would tell it the tab carries a name the
			// worker never wrote.
			var resp leapmuxv1.RenameAgentResponse
			lastResponse(t, w, &resp)
			assert.Equal(t, tc.want, resp.GetTitle(), "the reply reports the cleaned title")

			// The broadcast has to carry what the column holds. It used to
			// carry the request's raw title, which left every OTHER client of
			// the owner showing a title the worker never stored.
			renamed := nextRename(t, renames)
			assert.Equal(t, tc.want, renamed.GetTitle(), "the broadcast carries the cleaned title")
			assert.Equal(t, "agent-1", renamed.GetTabId())
			assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, renamed.GetTabType())
		})
	}
}

// A rename whose title cleaning empties keeps the title the agent already
// holds. The alternative -- writing "" -- leaves the tab with no label, and
// refusing is what the whole change removes, so the handler answers OK and
// changes nothing.
func TestRenameAgent_KeepsTheCurrentTitleWhenCleaningEmptiesIt(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	seedAgent(t, svc, "agent-1")
	_, err := svc.Queries.RenameAgent(context.Background(), db.RenameAgentParams{
		Title: "Agent Olivia",
		ID:    "agent-1",
	})
	require.NoError(t, err)
	renames := subscribeTabRenames(t, svc)

	dispatch(d, "RenameAgent", &leapmuxv1.RenameAgentRequest{
		AgentId: "agent-1",
		Title:   emptyingTitle,
	}, w)

	require.Empty(t, w.errors, "an emptied title is a no-op, not a failure")
	require.Len(t, w.responses, 1)

	row, err := svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "Agent Olivia", row.Title, "the stored title must survive an emptied rename")

	// This is the case the reply's title field exists for: the handler stored
	// nothing, so a client that echoes its own request believes the tab is now
	// called "  $$%%  ". The reply reports the title that is IN FORCE.
	var resp leapmuxv1.RenameAgentResponse
	lastResponse(t, w, &resp)
	assert.Equal(t, "Agent Olivia", resp.GetTitle(),
		"the reply reports the title still in force, not the empty string it refused")

	// Prove the absence of a broadcast by ordering rather than by waiting:
	// this second rename publishes after the first would have, and the bus
	// delivers in order, so seeing "Agent Second" FIRST proves the emptied
	// rename published nothing. A sleep would pass just by not waiting long
	// enough -- the same result a broken handler would produce.
	dispatch(d, "RenameAgent", &leapmuxv1.RenameAgentRequest{
		AgentId: "agent-1",
		Title:   "Agent Second",
	}, w)
	require.Empty(t, w.errors)
	assert.Equal(t, "Agent Second", nextRename(t, renames).GetTitle(),
		"the first event on the bus must be the SECOND rename's")
}

// getAgentTitleHeader is the sqlc header of the single-column title read that
// RenameAgent makes on its empty-title path. Every generated statement carries
// its own header, so matching on it selects one query and survives a reword of
// the SQL beneath it.
const getAgentTitleHeader = "-- name: GetAgentTitle :one"

// renameAgentWithTitleReadFault drives RenameAgent against an agent whose id
// gate passes and whose title read then fails, and returns the errors the
// handler sent.
//
// substitute replaces the title read. The two failures the handler separates
// -- a vanished row and a store fault -- are otherwise unreachable: the gate
// ahead of the handler reads the SAME table through GetAgentID, so damaging
// the schema refuses the request before the arm under test runs.
func renameAgentWithTitleReadFault(t *testing.T, substitute string) []testError {
	t.Helper()

	var faulted atomic.Int64
	svc, d, w := setupTestService(t, withQueryRewrite(func(query string) string {
		if strings.Contains(query, getAgentTitleHeader) {
			faulted.Add(1)
			return substitute
		}
		return query
	}))
	seedAgent(t, svc, "agent-1")

	dispatch(d, "RenameAgent", &leapmuxv1.RenameAgentRequest{
		AgentId: "agent-1",
		Title:   emptyingTitle,
	}, w)

	require.Equal(t, int64(1), faulted.Load(),
		"the handler must read the title exactly once through GetAgentTitle")
	require.Empty(t, w.responses, "a failed title read must send no response")

	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]testError(nil), w.errors...)
}

// A title read that finds no row means another caller deleted the agent
// between the id gate and this read. The handler reports NotFound, which is
// what the caller can act on -- retrying tells it the tab is gone.
func TestRenameAgent_ReportsAVanishedRowAsNotFound(t *testing.T) {
	t.Parallel()

	// The substitute selects the same column from the same table and matches
	// nothing, so the scan returns sql.ErrNoRows exactly as a deleted row
	// would. The bound parameter stays, because sqlc passes the id either way.
	errs := renameAgentWithTitleReadFault(t, "SELECT title FROM agents WHERE id = ? AND 0")

	require.Len(t, errs, 1)
	assert.Equal(t, int32(codes.NotFound), errs[0].code,
		"a missing row is the caller's problem, not the worker's")
	assert.Equal(t, "agent not found", errs[0].message)
}

// Any other title-read failure is a store fault. The handler reports Internal
// and never answers with an empty title, which would tell the client the tab
// lost its name.
func TestRenameAgent_ReportsATitleReadFaultAsInternal(t *testing.T) {
	t.Parallel()

	errs := renameAgentWithTitleReadFault(t, "SELECT no_such_column FROM agents WHERE id = ?")

	require.Len(t, errs, 1)
	assert.Equal(t, int32(codes.Internal), errs[0].code,
		"a store fault is the worker's problem, not the caller's")
	assert.Equal(t, "failed to read agent title", errs[0].message)
}

// The reply on the emptied-title path reports the title the MANAGER holds
// while the terminal is live, not the one the row holds.
//
// The two diverge in production, and the row is the stale side:
// runTerminalStartup writes the post-spawn title to the manager only, with no
// row write, so a terminal renamed by its startup sequence carries the new
// title in memory and the old one on disk. ListTerminals applies the same
// precedence, so a client that polls either RPC has to read one answer.
//
// A live PTY is the only way to reach this branch: the manager creates the
// meta entry at StartTerminal, and UpdateTitle refuses an id it does not hold.
func TestUpdateTerminalTitle_AnEmptiedTitleReportsTheLiveTitleNotTheStoredOne(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	workingDir := startTestTerminal(t, svc, ctx, "term-1")

	// The row keeps the title the terminal started with; the manager moves on.
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:         "term-1",
		WorkingDir: workingDir,
		HomeDir:    "/tmp",
		Title:      "Terminal Stored",
		Cols:       80,
		Rows:       24,
		Screen:     []byte{},
	}))
	require.True(t, svc.Terminals.UpdateTitle("term-1", "Terminal Live"),
		"the manager must hold a meta entry for a live terminal")

	dispatch(d, "UpdateTerminalTitle", &leapmuxv1.UpdateTerminalTitleRequest{
		TerminalId: "term-1",
		Title:      emptyingTitle,
	}, w)

	require.Empty(t, w.errors, "an emptied title is a no-op, not a failure")
	require.Len(t, w.responses, 1)

	var resp leapmuxv1.UpdateTerminalTitleResponse
	lastResponse(t, w, &resp)
	assert.Equal(t, "Terminal Live", resp.GetTitle(),
		"the manager wins while the terminal is live")

	// Neither side moved: the handler stored nothing.
	row, err := svc.Queries.GetTerminal(ctx, "term-1")
	require.NoError(t, err)
	assert.Equal(t, "Terminal Stored", row.Title, "the row must survive an emptied rename")
	meta, ok := svc.Terminals.GetMeta("term-1")
	require.True(t, ok)
	assert.Equal(t, "Terminal Live", meta.Title, "the manager must survive an emptied rename")
}

// seedTerminalWithTitle creates a terminal row that already holds a title, so
// a test can tell "kept the current title" from "wrote an empty one".
func seedTerminalWithTitle(t *testing.T, svc *Service, terminalID, title string) {
	t.Helper()
	require.NoError(t, svc.Queries.UpsertTerminal(context.Background(), db.UpsertTerminalParams{
		ID:         terminalID,
		WorkingDir: t.TempDir(),
		HomeDir:    t.TempDir(),
		Title:      title,
		Screen:     []byte{},
	}))
}

func TestUpdateTerminalTitle_CleansTitleInsteadOfRefusingIt(t *testing.T) {
	t.Parallel()

	for _, tc := range titleCleaningCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, d, w := setupTestService(t)
			seedTerminalWithTitle(t, svc, "term-1", "Terminal Liam")
			renames := subscribeTabRenames(t, svc)

			dispatch(d, "UpdateTerminalTitle", &leapmuxv1.UpdateTerminalTitleRequest{
				TerminalId: "term-1",
				Title:      tc.title,
			}, w)

			require.Empty(t, w.errors, "a title must never fail the RPC")
			require.Len(t, w.responses, 1)

			row, err := svc.Queries.GetTerminal(context.Background(), "term-1")
			require.NoError(t, err)
			assert.Equal(t, tc.want, row.Title, "the column holds the cleaned title")

			var resp leapmuxv1.UpdateTerminalTitleResponse
			lastResponse(t, w, &resp)
			assert.Equal(t, tc.want, resp.GetTitle(), "the reply reports the cleaned title")

			renamed := nextRename(t, renames)
			assert.Equal(t, tc.want, renamed.GetTitle(), "the broadcast carries the cleaned title")
			assert.Equal(t, "term-1", renamed.GetTabId())
			assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, renamed.GetTabType())
		})
	}
}

// The terminal mirror of the agent case: an emptied title leaves the row (and
// the in-memory manager) holding the title they already have.
func TestUpdateTerminalTitle_KeepsTheCurrentTitleWhenCleaningEmptiesIt(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	seedTerminalWithTitle(t, svc, "term-1", "Terminal Liam")
	renames := subscribeTabRenames(t, svc)

	dispatch(d, "UpdateTerminalTitle", &leapmuxv1.UpdateTerminalTitleRequest{
		TerminalId: "term-1",
		Title:      emptyingTitle,
	}, w)

	require.Empty(t, w.errors, "an emptied title is a no-op, not a failure")
	require.Len(t, w.responses, 1)

	row, err := svc.Queries.GetTerminal(context.Background(), "term-1")
	require.NoError(t, err)
	assert.Equal(t, "Terminal Liam", row.Title, "the stored title must survive an emptied rename")

	var resp leapmuxv1.UpdateTerminalTitleResponse
	lastResponse(t, w, &resp)
	assert.Equal(t, "Terminal Liam", resp.GetTitle(),
		"the reply reports the title still in force, not the empty string it refused")

	// Same ordering proof as the agent case: the second update's broadcast
	// arriving first shows the emptied one published nothing.
	dispatch(d, "UpdateTerminalTitle", &leapmuxv1.UpdateTerminalTitleRequest{
		TerminalId: "term-1",
		Title:      "Terminal Second",
	}, w)
	require.Empty(t, w.errors)
	assert.Equal(t, "Terminal Second", nextRename(t, renames).GetTitle(),
		"the first event on the bus must be the SECOND update's")
}

// --- the registry: the model-supplied title ---

// newRegistryRoot provisions a service holding one root agent, which owns the
// background-task registry every test below writes to. It returns the writer
// bound to that service's channel; a caller that asserts on a broadcast
// registers a watch on it FIRST (see spawnChildWithTitle).
func newRegistryRoot(t *testing.T) (*Service, *testResponseWriter) {
	t.Helper()

	svc, _, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID:            "root-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	return svc, w
}

// registryRootSink returns a root sink for the agent newRegistryRoot created.
func registryRootSink(svc *Service) agent.OutputSink {
	return svc.Output.NewSink("root-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
}

// spawnChildWithTitle drives EnsureChildAgent the way every provider does and
// returns the service, the writer that captured the registry broadcast, and the
// new child agent id.
//
// The watch registers BEFORE the spawn, because EnsureChildAgent publishes the
// registry snapshot inside the call: a watch that registers afterwards sees
// nothing, and the broadcast assertion would pass vacuously.
func spawnChildWithTitle(t *testing.T, title string) (*Service, *testResponseWriter, string) {
	t.Helper()

	svc, w := newRegistryRoot(t)
	registerAgentWatch(svc, w.channelID, "root-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, w)

	childID, err := registryRootSink(svc).EnsureChildAgent("span-1", "task-1", title)
	require.NoError(t, err)
	require.NotEmpty(t, childID)
	return svc, w, childID
}

// upsertTaskWithTitle drives UpsertBackgroundTask the way every provider does
// and returns the service plus the writer that captured the registry
// broadcast. Same watch-before-write ordering as spawnChildWithTitle.
func upsertTaskWithTitle(t *testing.T, task bgtask.Upsert) (*Service, *testResponseWriter) {
	t.Helper()

	svc, w := newRegistryRoot(t)
	registerAgentWatch(svc, w.channelID, "root-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, w)

	require.NoError(t, registryRootSink(svc).UpsertBackgroundTask(task))
	return svc, w
}

// registryRow reads the one registry row the spawn created under root-1.
func registryRow(t *testing.T, svc *Service) db.AgentBackgroundTask {
	t.Helper()

	rows, err := svc.Queries.ListAgentBackgroundTasksNewestFirst(context.Background(),
		db.ListAgentBackgroundTasksNewestFirstParams{OwnerAgentID: "root-1", Limit: 100})
	require.NoError(t, err)
	require.Len(t, rows, 1, "the spawn must create exactly one registry row")
	return rows[0]
}

// broadcastTask returns the row the newest AgentBackgroundTasksChanged event
// carries for rowKey. The whole item, not only its title: the title rule also
// decides title_is_command, and the broadcast is what every watching client
// renders from.
func broadcastTask(t *testing.T, w *testResponseWriter, rowKey string) *leapmuxv1.BackgroundTaskItem {
	t.Helper()

	var found *leapmuxv1.BackgroundTaskItem
	for _, e := range decodeAgentEvents(w) {
		changed := e.GetBackgroundTasksChanged()
		if changed == nil {
			continue
		}
		for _, task := range changed.GetTasks() {
			if task.GetId() == rowKey {
				found = task
			}
		}
	}
	require.NotNil(t, found, "no background-task broadcast carried row %q", rowKey)
	return found
}

// A subagent title comes from the MODEL, so nothing upstream limits its length
// or its characters. EnsureChildAgent cleans it once, and that one call has to
// cover all three places the title lands: the child's `agents` row, the
// registry row, and the broadcast that tells every watching client.
//
// Before the fix each of the three held the model's raw string.
func TestEnsureChildAgent_CleansTheModelSuppliedTitle(t *testing.T) {
	t.Parallel()

	for _, tc := range titleCleaningCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, w, childID := spawnChildWithTitle(t, tc.title)

			child, err := svc.Queries.GetAgentByID(context.Background(), childID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, child.Title, "the child agent row holds the cleaned title")
			assert.Equal(t, tc.want, registryRow(t, svc).Title, "the registry row holds the cleaned title")
			assert.Equal(t, tc.want, broadcastTask(t, w, "task-1").GetTitle(), "the broadcast carries the cleaned title")
		})
	}
}

// A model title that cleaning empties leaves the child's `agents` row with no
// title at all, and that row IS the tab label once the user opens the subagent
// transcript. Nothing rewrites it later -- a second EnsureChildAgent for the
// same spawn resolves the existing row and only re-links the registry -- so a
// blank written here is permanent. The row takes the same pooled "Agent <Name>"
// OpenAgent takes when its own title empties.
//
// The REGISTRY row is deliberately left blank instead. A blank title in a
// bgtask.Upsert means "keep the title this row already holds"
// (bgtask.Item.PreservingBlanksFrom), which is how a provider refreshes a row's
// status without restating its title. Writing the pooled name there would
// overwrite a real subagent title with a placeholder.
func TestEnsureChildAgent_NamesTheAgentRowWhenCleaningEmptiesTheTitle(t *testing.T) {
	t.Parallel()

	svc, _, childID := spawnChildWithTitle(t, emptyingTitle)

	child, err := svc.Queries.GetAgentByID(context.Background(), childID)
	require.NoError(t, err)
	assert.NotEmpty(t, child.Title, "a blank title must never reach the child agent row")
	assert.True(t, strings.HasPrefix(child.Title, "Agent "),
		"the fallback is pickAgentTitle(), which prefixes the pooled name with %q, got %q", "Agent ", child.Title)

	assert.Empty(t, registryRow(t, svc).Title,
		"the registry row keeps the blank, because blank means 'keep the current title' there")
}

// The blank-means-keep contract, proved on the exact sequence Claude runs: a
// task_started upserts the registry row with the model's title, and the spawn
// that follows links a child transcript to it. The link upsert reaches
// linkRegistryRow because the row exists with no child id yet, so its title
// DOES reach the column.
//
// When that second title cleans to empty, the row must keep the title the
// upsert gave it. Before the fix EnsureChildAgent passed the raw "  $$%%  "
// through and overwrote "Ship the parser" with it. Substituting the agent
// row's pooled fallback on the registry path would overwrite it just as badly,
// with "Agent <Name>".
func TestEnsureChildAgent_AnEmptiedTitleKeepsTheRegistryRowTitle(t *testing.T) {
	t.Parallel()

	svc, _ := newRegistryRoot(t)
	sink := registryRootSink(svc)

	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent,
		Title: "Ship the parser", Status: bgtask.StatusRunning,
	}))
	childID, err := sink.EnsureChildAgent("span-1", "task-1", emptyingTitle)
	require.NoError(t, err)

	row := registryRow(t, svc)
	assert.Equal(t, childID, row.ChildAgentID, "the link upsert must have run, or the assertion below is vacuous")
	assert.Equal(t, "Ship the parser", row.Title,
		"an emptied title must not overwrite a registry row that already has one")

	// The agent row has no earlier title to keep, so it takes the fallback.
	child, err := svc.Queries.GetAgentByID(context.Background(), childID)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(child.Title, "Agent "),
		"the agent row takes the pooled fallback, got %q", child.Title)
}

// --- UpsertBackgroundTask: the registry's own title write ---

// Every provider writes a registry title through UpsertBackgroundTask, and the
// title comes from the MODEL, so nothing upstream limits its length or its
// characters. The one clean at applyBackgroundTaskUpsertLocked has to cover
// both places the title lands: the registry row and the broadcast that tells
// every watching client.
//
// Before the fix each of the two held the model's raw string, and only a row
// that later spawned a child transcript was ever corrected -- by
// EnsureChildAgent's link upsert, one frame late. A shell row and a workflow
// row never spawn a child, so nothing corrected them at all.
func TestUpsertBackgroundTask_CleansTheModelSuppliedTitle(t *testing.T) {
	t.Parallel()

	for _, tc := range titleCleaningCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, w := upsertTaskWithTitle(t, bgtask.Upsert{
				RowKey: "task-1", Kind: bgtask.KindSubagent,
				Title: tc.title, Status: bgtask.StatusRunning,
			})

			assert.Equal(t, tc.want, registryRow(t, svc).Title, "the registry row holds the cleaned title")
			assert.Equal(t, tc.want, broadcastTask(t, w, "task-1").GetTitle(), "the broadcast carries the cleaned title")
		})
	}
}

// The accepted cost of one rule for every title column, asserted rather than
// left as a surprise: a shell row whose title IS the command loses the
// templating characters the rule strips. `$`, `%`, `"` and `\` all go.
//
// The row still reports title_is_command, so the client still sets it as code.
// What the row carries is a LABEL for a command that already runs somewhere
// else -- it is not a command to copy back and run.
func TestUpsertBackgroundTask_StripsTemplatingFromAShellCommandTitle(t *testing.T) {
	t.Parallel()

	svc, w := upsertTaskWithTitle(t, bgtask.Upsert{
		RowKey: "term-1", Kind: bgtask.KindShell,
		Title:          `npm test --grep "$FOO" 100% c:\tmp`,
		TitleIsCommand: true,
		Status:         bgtask.StatusRunning,
	})

	const want = `npm test --grep FOO 100 c:tmp`
	row := registryRow(t, svc)
	assert.Equal(t, want, row.Title, "the shell command title meets the same rule as every other title")
	assert.EqualValues(t, 1, row.TitleIsCommand, "a stripped command is still a command, so the flag survives")

	broadcast := broadcastTask(t, w, "term-1")
	assert.Equal(t, want, broadcast.GetTitle(), "the broadcast carries the cleaned command")
	assert.True(t, broadcast.GetTitleIsCommand(), "the broadcast keeps the flag")
}

// A supplied title that cleaning empties says the same thing a blank one says:
// this call carries no usable title. So it takes the same answer -- the row
// keeps the title it already holds. Writing "" instead would blank a real
// title, and writing a placeholder would replace it with a name the model
// never wrote.
func TestUpsertBackgroundTask_AnEmptiedTitleKeepsTheStoredTitle(t *testing.T) {
	t.Parallel()

	svc, _ := newRegistryRoot(t)
	sink := registryRootSink(svc)

	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent,
		Title: "Ship the parser", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent,
		Title: emptyingTitle, Status: bgtask.StatusRunning,
	}))

	assert.Equal(t, "Ship the parser", registryRow(t, svc).Title,
		"an emptied title must not overwrite a registry row that already has one")
}

// The other half of the emptied-title rule: a row that does not exist yet has
// no title to keep, so it is born untitled. The client falls back to the
// description and then to the row key, which is what it already does for a
// provider that had no title to give.
//
// title_is_command goes with it. The flag describes the title, so a row with no
// title must not claim its blank is a command -- that renders an empty
// monospace line.
func TestUpsertBackgroundTask_AnEmptiedTitleLeavesANewRowUntitled(t *testing.T) {
	t.Parallel()

	svc, w := upsertTaskWithTitle(t, bgtask.Upsert{
		RowKey: "term-1", Kind: bgtask.KindShell,
		Title: emptyingTitle, TitleIsCommand: true, Status: bgtask.StatusRunning,
	})

	row := registryRow(t, svc)
	assert.Empty(t, row.Title, "a new row has no title to keep, so the emptied one leaves it blank")
	assert.EqualValues(t, 0, row.TitleIsCommand, "the command flag cannot outlive the title it describes")

	broadcast := broadcastTask(t, w, "term-1")
	assert.Empty(t, broadcast.GetTitle(), "the broadcast carries the blank the row holds")
	assert.False(t, broadcast.GetTitleIsCommand(), "the broadcast drops the flag with the title")
}

// The preserve-blanks contract, unchanged by the cleaning: a provider that
// refreshes a row's status without restating its title sends Title "" and the
// row keeps the one it holds. Claude's task_notification is exactly this call,
// and Codex's collabChildTitle answers "" for a thread it has not titled yet.
func TestUpsertBackgroundTask_ABlankTitleKeepsTheStoredTitle(t *testing.T) {
	t.Parallel()

	svc, _ := newRegistryRoot(t)
	sink := registryRootSink(svc)

	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent,
		Title: "Ship the parser", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent,
		Description: "/tmp/out.txt", Status: bgtask.StatusCompleted,
	}))

	row := registryRow(t, svc)
	assert.Equal(t, "Ship the parser", row.Title, "a blank title still means 'keep the stored one'")
	assert.Equal(t, "/tmp/out.txt", row.Description, "the rest of the partial upsert still lands")
	assert.Equal(t, "completed", row.Status)
}

// Claude's real sequence for a Task spawn: task_started upserts the registry
// row with the model's raw title, then the spawn links a child transcript to
// it. Both writes now apply the same rule, so the boundary the link upsert used
// to hold is gone -- the FIRST write already stores the cleaned title, and the
// link upsert passes an already-cleaned title through byte-identical.
//
// The replay at the end pins the cleaning to the correct side of the no-op
// guard. The guard compares the merged candidate against the stored row, so
// cleaning has to run BEFORE it. Cleaning after it would make the raw title
// differ from the stored clean one on every replay, and each replay would
// rewrite the row and broadcast again for the life of the agent.
func TestUpsertBackgroundTask_PassesAnAlreadyCleanedTitleThrough(t *testing.T) {
	t.Parallel()

	svc, _ := newRegistryRoot(t)
	sink := registryRootSink(svc)

	raw := strings.Repeat("一", 50)
	cleaned := strings.Repeat("一", 42)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, Title: raw, Status: bgtask.StatusRunning,
	}))
	require.Equal(t, cleaned, registryRow(t, svc).Title, "the bare upsert stores the cleaned title")

	// EnsureChildAgent cleans before it reaches the same path, so the link
	// upsert hands over `cleaned` and must not change the row's title.
	childID, err := sink.EnsureChildAgent("span-1", "task-1", raw)
	require.NoError(t, err)

	row := registryRow(t, svc)
	assert.Equal(t, childID, row.ChildAgentID, "the link upsert must have run, or the assertion below is vacuous")
	assert.Equal(t, cleaned, row.Title, "an already-cleaned title passes through byte-identical")
	writtenAt := row.UpdatedAt

	// A replayed RAW title is a no-op against the stored cleaned one. The
	// replay carries what Claude's task_started carries -- no child id, which
	// the blank-means-keep rule fills from the stored row.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, Title: raw, Status: bgtask.StatusRunning,
	}))
	replayed := registryRow(t, svc)
	assert.Equal(t, cleaned, replayed.Title)
	assert.Equal(t, writtenAt, replayed.UpdatedAt,
		"a replayed raw title cleans to the stored one, so the no-op guard must skip the rewrite")
}
