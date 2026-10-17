package reasonix

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReasonixStoredSessions_KeepsTimestampTies(t *testing.T) {
	t.Parallel()
	for _, project := range []bool{false, true} {
		t.Run(fmt.Sprintf("project root %t", project), func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			workDir := filepath.Join(home, "project")
			root := filepath.Join(home, ".reasonix", "sessions")
			if project {
				slug, ok := reasonixWorkspaceSlug(workDir)
				require.True(t, ok)
				root = filepath.Join(home, ".reasonix", "projects", slug, "sessions")
			}
			stamp := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
			for i, id := range []string{"a-old", "z-new"} {
				writeReasonixSession(t, root, id, fmt.Sprintf(`{"id":%q,"turns":1,"updated_at":%q,"workspace_root":%s}`, id, stamp.Add(time.Duration(i)*time.Hour).Format(time.RFC3339), agenttest.JSONString(workDir)), "")
				agenttest.TouchFixture(t, filepath.Join(root, id+reasonixSessionMetaSuffix), stamp)
			}
			got, err := reasonixStoredSessions(t.Context(), agent.StoredSessionQuery{WorkingDir: workDir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil), Limit: 1})
			require.NoError(t, err)
			assert.Equal(t, []string{"z-new"}, agenttest.Handles(got))
		})
	}
}

func TestReasonixStoredSessionsSharesTheBudgetAcrossRoots(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	workDir := filepath.Join(home, "project")
	slug, ok := reasonixWorkspaceSlug(workDir)
	require.True(t, ok)
	project := filepath.Join(home, ".reasonix", "projects", slug, "sessions")
	global := filepath.Join(home, ".reasonix", "sessions")
	stamp := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	write := func(root, id, title string, activity time.Time) {
		writeReasonixSession(t, root, id, fmt.Sprintf(`{"id":%q,"turns":1,"custom_title":%q,"updated_at":%q,"workspace_root":%s}`, id, title, activity.Format(time.RFC3339), agenttest.JSONString(workDir)), "")
		agenttest.TouchFixture(t, filepath.Join(root, id+reasonixSessionMetaSuffix), stamp)
	}
	write(project, "shared", "project copy", stamp.Add(3*time.Hour))
	write(global, "shared", "global copy", stamp.Add(4*time.Hour))
	write(global, "a-old", "old", stamp)
	write(global, "z-new", "new", stamp.Add(time.Hour))
	query := agent.StoredSessionQuery{WorkingDir: workDir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil), Limit: 2}
	got, err := reasonixStoredSessions(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, []string{"shared", "z-new"}, agenttest.Handles(got))
	assert.Equal(t, "project copy", got[0].Title)
	query.Limit = 1
	got, err = reasonixStoredSessions(t.Context(), query)
	require.NoError(t, err)
	require.Equal(t, []string{"shared"}, agenttest.Handles(got))
	assert.Equal(t, "project copy", got[0].Title)
}

// writeReasonixSession writes the `.jsonl.meta` listing sidecar, and optionally
// the `.acp.json` one beside it.
func writeReasonixSession(t *testing.T, sessionsDir, id, metaJSON, acpJSON string) {
	t.Helper()
	agenttest.WriteFixtureFile(t, filepath.Join(sessionsDir, id+reasonixSessionMetaSuffix), metaJSON)
	if acpJSON != "" {
		agenttest.WriteFixtureFile(t, filepath.Join(sessionsDir, id+reasonixACPSuffix), acpJSON)
	}
}

func TestReasonixWorkspaceSlug(t *testing.T) {
	t.Parallel()

	// The expectation depends on the host. Reasonix ignores path case only on Windows. Each case
	// verifies the spelling that its platform produces.
	if runtime.GOOS == "windows" {
		slug, ok := reasonixWorkspaceSlug(`C:\Users\trustin\Workspaces\leapmux`)
		require.True(t, ok)
		assert.Equal(t, "c--users-trustin-workspaces-leapmux", slug,
			"the drive letter keeps its own separator, and the whole slug folds")
	} else {
		slug, ok := reasonixWorkspaceSlug("/Users/trustin/Workspaces/leapmux")
		require.True(t, ok)
		assert.Equal(t, "-Users-trustin-Workspaces-leapmux", slug)
	}

	// Reasonix appends an FNV-1a hash beyond its component limit. This reader does not reproduce
	// the hash. It uses the global root instead.
	_, ok := reasonixWorkspaceSlug("/" + strings.Repeat("a", reasonixSlugMaxLen))
	assert.False(t, ok)
}

func TestReasonixStoredSessions_ProjectRoot(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	home := t.TempDir()
	slug, _ := reasonixWorkspaceSlug(dir)
	sessions := filepath.Join(home, ".reasonix", "projects", slug, "sessions")

	writeReasonixSession(t, sessions, "sess-new",
		`{"id":"sess-new","turns":3,"updated_at":"2026-08-28T06:13:36.681811Z","topic_title":"Newest topic","preview":"a prompt","workspace_root":`+agenttest.JSONString(dir)+`}`, "")
	writeReasonixSession(t, sessions, "sess-old",
		`{"id":"sess-old","turns":1,"updated_at":"2026-08-27T06:13:36.681811Z","preview":"older prompt","workspace_root":`+agenttest.JSONString(dir)+`}`, "")

	got, err := reasonixStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-new", "sess-old"}, agenttest.Handles(got))
	assert.Equal(t, "Newest topic", got[0].Title)
	assert.Equal(t, "older prompt", got[1].Title, "the stored preview is the last resort")
	assert.Equal(t, time.Date(2026, 8, 28, 6, 13, 36, 681811000, time.UTC), got[0].UpdatedAt)
}

// TestReasonixStoredSessions_GlobalRootNeedsTheACPSidecar covers the layout
// LeapMux actually produces: sessions in the GLOBAL root, whose `.meta` may
// carry no workspace_root, placed by the ACP sidecar's cwd.
func TestReasonixStoredSessions_GlobalRootNeedsTheACPSidecar(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	home := t.TempDir()
	global := filepath.Join(home, ".reasonix", "sessions")

	writeReasonixSession(t, global, "sess-acp",
		`{"id":"sess-acp","turns":2,"updated_at":"2026-08-28T06:13:36.681811Z","preview":"from acp"}`,
		`{"sessionId":"sess-acp","cwd":`+agenttest.JSONString(dir)+`,"title":"ACP title","updatedAt":"2026-08-28T06:13:36.707655Z"}`)
	// No workspace_root and no ACP sidecar: the session cannot be placed, so
	// offering it under this directory would be a guess.
	writeReasonixSession(t, global, "sess-nowhere",
		`{"id":"sess-nowhere","turns":2,"updated_at":"2026-08-29T06:13:36.681811Z","preview":"unplaceable"}`, "")
	// A different working directory.
	writeReasonixSession(t, global, "sess-elsewhere",
		`{"id":"sess-elsewhere","turns":2,"updated_at":"2026-08-30T06:13:36.681811Z","workspace_root":`+
			agenttest.JSONString(testutil.NativeAbsPath("/other/dir"))+`}`, "")

	got, err := reasonixStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-acp"}, agenttest.Handles(got))
	// Both sidecars derive their string from the first prompt, so the two are
	// near-identical in practice. The ACP title is preferred because it is what
	// Reasonix hands the client, while `preview` is a truncated copy.
	assert.Equal(t, "ACP title", got[0].Title)
}

func TestReasonixStoredSessions_TitlePrecedence(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")

	cases := []struct {
		name string
		meta string
		acp  string
		want string
	}{
		{
			name: "a title the user set wins",
			meta: `{"id":"s","turns":1,"custom_title":"mine","topic_title":"topic","name":"branch","preview":"prompt","workspace_root":` + agenttest.JSONString(dir) + `}`,
			want: "mine",
		},
		{
			name: "then the derived topic",
			meta: `{"id":"s","turns":1,"topic_title":"topic","name":"branch","preview":"prompt","workspace_root":` + agenttest.JSONString(dir) + `}`,
			want: "topic",
		},
		{
			name: "then the branch name",
			meta: `{"id":"s","turns":1,"name":"branch","preview":"prompt","workspace_root":` + agenttest.JSONString(dir) + `}`,
			want: "branch",
		},
		{
			name: "then the ACP sidecar's title",
			meta: `{"id":"s","turns":1,"workspace_root":` + agenttest.JSONString(dir) + `}`,
			acp:  `{"sessionId":"s","cwd":` + agenttest.JSONString(dir) + `,"title":"acp title"}`,
			want: "acp title",
		},
		{
			name: "and finally the stored preview",
			meta: `{"id":"s","turns":1,"preview":"prompt","workspace_root":` + agenttest.JSONString(dir) + `}`,
			want: "prompt",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			slug, _ := reasonixWorkspaceSlug(dir)
			writeReasonixSession(t, filepath.Join(home, ".reasonix", "projects", slug, "sessions"),
				"s", tc.meta, tc.acp)

			got, err := reasonixStoredSessions(context.Background(), agent.StoredSessionQuery{
				WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
			})
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tc.want, got[0].Title)
		})
	}
}

func TestReasonixStoredSessions_ExcludesBranchesAndEmptySessions(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	home := t.TempDir()
	slug, _ := reasonixWorkspaceSlug(dir)
	sessions := filepath.Join(home, ".reasonix", "projects", slug, "sessions")

	writeReasonixSession(t, sessions, "sess-real",
		`{"id":"sess-real","turns":1,"preview":"real","workspace_root":`+agenttest.JSONString(dir)+`}`, "")
	writeReasonixSession(t, sessions, "sess-branch",
		`{"id":"sess-branch","turns":5,"parent_id":"sess-real","preview":"branch","workspace_root":`+agenttest.JSONString(dir)+`}`, "")
	// Reasonix's own lister drops a session with no turns, and so does this:
	// an empty session has nothing to resume into.
	writeReasonixSession(t, sessions, "sess-empty",
		`{"id":"sess-empty","turns":0,"preview":"never used","workspace_root":`+agenttest.JSONString(dir)+`}`, "")

	got, err := reasonixStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-real"}, agenttest.Handles(got))
}

// TestReasonixStoredSessions_DedupesAcrossRoots covers a session recorded under
// both the project root and the global one.
func TestReasonixStoredSessions_DedupesAcrossRoots(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	home := t.TempDir()
	slug, _ := reasonixWorkspaceSlug(dir)

	writeReasonixSession(t, filepath.Join(home, ".reasonix", "projects", slug, "sessions"), "sess-both",
		`{"id":"sess-both","turns":1,"custom_title":"project copy","workspace_root":`+agenttest.JSONString(dir)+`}`, "")
	writeReasonixSession(t, filepath.Join(home, ".reasonix", "sessions"), "sess-both",
		`{"id":"sess-both","turns":1,"custom_title":"global copy","workspace_root":`+agenttest.JSONString(dir)+`}`, "")

	got, err := reasonixStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "project copy", got[0].Title, "the project root is walked first and wins")
}

func TestReasonixStoredSessions_HonoursTheHomeOverride(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	home := t.TempDir()
	writeReasonixSession(t, filepath.Join(home, "alt-reasonix", "sessions"), "s",
		`{"id":"s","turns":1,"preview":"over here","workspace_root":`+agenttest.JSONString(dir)+`}`, "")

	got, err := reasonixStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home,
		Getenv: agenttest.FixtureEnv(map[string]string{"REASONIX_HOME": "~/alt-reasonix"}),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"s"}, agenttest.Handles(got))
}

func TestReasonixStoredSessions_AbsentStoreIsEmpty(t *testing.T) {
	t.Parallel()
	got, err := reasonixStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: testutil.NativeAbsPath("/Users/dev/project"), HomeDir: t.TempDir(), Getenv: agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestReasonixStoredSessions_SkipsCorruptSidecars(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	home := t.TempDir()
	slug, _ := reasonixWorkspaceSlug(dir)
	sessions := filepath.Join(home, ".reasonix", "projects", slug, "sessions")

	writeReasonixSession(t, sessions, "good",
		`{"id":"good","turns":1,"preview":"fine","workspace_root":`+agenttest.JSONString(dir)+`}`, "")
	agenttest.WriteFixtureFile(t, filepath.Join(sessions, "broken"+reasonixSessionMetaSuffix), "{ not json")

	got, err := reasonixStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"good"}, agenttest.Handles(got))
}

// Reasonix checks cancellation before each root scan. The shared collector checks it before each
// candidate read.
// This test verifies the provider's cancellation behavior.
func TestReasonixStoredSessions_StopsOnACancelledContext(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	home := t.TempDir()
	slug, _ := reasonixWorkspaceSlug(dir)
	sessions := filepath.Join(home, ".reasonix", "projects", slug, "sessions")

	writeReasonixSession(t, sessions, "sess-a",
		`{"id":"sess-a","turns":1,"preview":"work","workspace_root":`+agenttest.JSONString(dir)+`}`, "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := reasonixStoredSessions(ctx, agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err, "a dismissed dialog is not a failure")
	assert.Empty(t, got, "a cancelled scan reads no sidecar")
}

// A decoder can populate earlier fields before it rejects a later field. A rejected ACP sidecar
// must supply neither the title nor the timestamp.
func TestReasonixStoredSessions_IgnoresARejectedACPSidecar(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	home := t.TempDir()
	slug, _ := reasonixWorkspaceSlug(dir)
	sessions := filepath.Join(home, ".reasonix", "projects", slug, "sessions")

	// The `.acp.json` parses as far as `title`, then fails on a numeric
	// `updatedAt` where a string belongs.
	writeReasonixSession(t, sessions, "sess-a",
		`{"id":"sess-a","turns":1,"preview":"the honest fallback","workspace_root":`+agenttest.JSONString(dir)+`}`,
		`{"sessionId":"sess-a","cwd":`+agenttest.JSONString(dir)+`,"title":"from a rejected document","updatedAt":12345}`)

	got, err := reasonixStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "the honest fallback", got[0].Title,
		"the title must come from the `.meta`, never from the sidecar that failed to decode")
}

func TestReasonixReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, Registration().Plugin, func(t *testing.T, home, dir string) string {
		slug, ok := reasonixWorkspaceSlug(dir)
		require.True(t, ok)
		writeReasonixSession(t, filepath.Join(home, ".reasonix", "projects", slug, "sessions"),
			"reasonix-session",
			`{"id":"reasonix-session","turns":1,"preview":"work","workspace_root":`+agenttest.JSONString(dir)+`}`, "")
		return "reasonix-session"
	})
}
