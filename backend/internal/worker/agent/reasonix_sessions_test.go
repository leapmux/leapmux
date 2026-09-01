package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeReasonixSession writes the `.jsonl.meta` listing sidecar, and optionally
// the `.acp.json` one beside it.
func writeReasonixSession(t *testing.T, sessionsDir, id, metaJSON, acpJSON string) {
	t.Helper()
	writeFixtureFile(t, filepath.Join(sessionsDir, id+reasonixSessionMetaSuffix), metaJSON)
	if acpJSON != "" {
		writeFixtureFile(t, filepath.Join(sessionsDir, id+reasonixACPSuffix), acpJSON)
	}
}

func TestReasonixWorkspaceSlug(t *testing.T) {
	t.Parallel()
	slug, ok := reasonixWorkspaceSlug("/Users/trustin/Workspaces/leapmux")
	require.True(t, ok)
	assert.Equal(t, "-Users-trustin-Workspaces-leapmux", slug)

	// Past the per-component budget Reasonix appends an FNV-1a hash this code
	// does not reproduce, so it declines the project directory and lets the
	// global root answer instead.
	_, ok = reasonixWorkspaceSlug("/" + strings.Repeat("a", reasonixSlugMaxLen))
	assert.False(t, ok)
}

func TestReasonixStoredSessions_ProjectRoot(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	slug, _ := reasonixWorkspaceSlug(dir)
	sessions := filepath.Join(home, ".reasonix", "projects", slug, "sessions")

	writeReasonixSession(t, sessions, "sess-new",
		`{"id":"sess-new","turns":3,"updated_at":"2026-08-28T06:13:36.681811Z","topic_title":"Newest topic","preview":"a prompt","workspace_root":"`+dir+`"}`, "")
	writeReasonixSession(t, sessions, "sess-old",
		`{"id":"sess-old","turns":1,"updated_at":"2026-08-27T06:13:36.681811Z","preview":"older prompt","workspace_root":"`+dir+`"}`, "")

	got, err := reasonixStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-new", "sess-old"}, handlesOf(got))
	assert.Equal(t, "Newest topic", got[0].Title)
	assert.Equal(t, "older prompt", got[1].Title, "the stored preview is the last resort")
	assert.Equal(t, time.Date(2026, 8, 28, 6, 13, 36, 681811000, time.UTC), got[0].UpdatedAt)
}

// TestReasonixStoredSessions_GlobalRootNeedsTheACPSidecar covers the layout
// LeapMux actually produces: sessions in the GLOBAL root, whose `.meta` may
// carry no workspace_root, placed by the ACP sidecar's cwd.
func TestReasonixStoredSessions_GlobalRootNeedsTheACPSidecar(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	global := filepath.Join(home, ".reasonix", "sessions")

	writeReasonixSession(t, global, "sess-acp",
		`{"id":"sess-acp","turns":2,"updated_at":"2026-08-28T06:13:36.681811Z","preview":"from acp"}`,
		`{"sessionId":"sess-acp","cwd":"`+dir+`","title":"ACP title","updatedAt":"2026-08-28T06:13:36.707655Z"}`)
	// No workspace_root and no ACP sidecar: the session cannot be placed, so
	// offering it under this directory would be a guess.
	writeReasonixSession(t, global, "sess-nowhere",
		`{"id":"sess-nowhere","turns":2,"updated_at":"2026-08-29T06:13:36.681811Z","preview":"unplaceable"}`, "")
	// A different working directory.
	writeReasonixSession(t, global, "sess-elsewhere",
		`{"id":"sess-elsewhere","turns":2,"updated_at":"2026-08-30T06:13:36.681811Z","workspace_root":"/other/dir"}`, "")

	got, err := reasonixStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-acp"}, handlesOf(got))
	// Both sidecars derive their string from the first prompt, so the two are
	// near-identical in practice. The ACP title is preferred because it is what
	// Reasonix hands the client, while `preview` is a truncated copy.
	assert.Equal(t, "ACP title", got[0].Title)
}

func TestReasonixStoredSessions_TitlePrecedence(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"

	cases := []struct {
		name string
		meta string
		acp  string
		want string
	}{
		{
			name: "a title the user set wins",
			meta: `{"id":"s","turns":1,"custom_title":"mine","topic_title":"topic","name":"branch","preview":"prompt","workspace_root":"` + dir + `"}`,
			want: "mine",
		},
		{
			name: "then the derived topic",
			meta: `{"id":"s","turns":1,"topic_title":"topic","name":"branch","preview":"prompt","workspace_root":"` + dir + `"}`,
			want: "topic",
		},
		{
			name: "then the branch name",
			meta: `{"id":"s","turns":1,"name":"branch","preview":"prompt","workspace_root":"` + dir + `"}`,
			want: "branch",
		},
		{
			name: "then the ACP sidecar's title",
			meta: `{"id":"s","turns":1,"workspace_root":"` + dir + `"}`,
			acp:  `{"sessionId":"s","cwd":"` + dir + `","title":"acp title"}`,
			want: "acp title",
		},
		{
			name: "and finally the stored preview",
			meta: `{"id":"s","turns":1,"preview":"prompt","workspace_root":"` + dir + `"}`,
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

			got, err := reasonixStoredSessions(context.Background(), StoredSessionQuery{
				WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
			})
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tc.want, got[0].Title)
		})
	}
}

func TestReasonixStoredSessions_ExcludesBranchesAndEmptySessions(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	slug, _ := reasonixWorkspaceSlug(dir)
	sessions := filepath.Join(home, ".reasonix", "projects", slug, "sessions")

	writeReasonixSession(t, sessions, "sess-real",
		`{"id":"sess-real","turns":1,"preview":"real","workspace_root":"`+dir+`"}`, "")
	writeReasonixSession(t, sessions, "sess-branch",
		`{"id":"sess-branch","turns":5,"parent_id":"sess-real","preview":"branch","workspace_root":"`+dir+`"}`, "")
	// Reasonix's own lister drops a session with no turns, and so does this:
	// an empty session has nothing to resume into.
	writeReasonixSession(t, sessions, "sess-empty",
		`{"id":"sess-empty","turns":0,"preview":"never used","workspace_root":"`+dir+`"}`, "")

	got, err := reasonixStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-real"}, handlesOf(got))
}

// TestReasonixStoredSessions_DedupesAcrossRoots covers a session recorded under
// both the project root and the global one.
func TestReasonixStoredSessions_DedupesAcrossRoots(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	slug, _ := reasonixWorkspaceSlug(dir)

	writeReasonixSession(t, filepath.Join(home, ".reasonix", "projects", slug, "sessions"), "sess-both",
		`{"id":"sess-both","turns":1,"custom_title":"project copy","workspace_root":"`+dir+`"}`, "")
	writeReasonixSession(t, filepath.Join(home, ".reasonix", "sessions"), "sess-both",
		`{"id":"sess-both","turns":1,"custom_title":"global copy","workspace_root":"`+dir+`"}`, "")

	got, err := reasonixStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "project copy", got[0].Title, "the project root is walked first and wins")
}

func TestReasonixStoredSessions_HonoursTheHomeOverride(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	writeReasonixSession(t, filepath.Join(home, "alt-reasonix", "sessions"), "s",
		`{"id":"s","turns":1,"preview":"over here","workspace_root":"`+dir+`"}`, "")

	got, err := reasonixStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home,
		Getenv: fixtureEnv(map[string]string{"REASONIX_HOME": "~/alt-reasonix"}),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"s"}, handlesOf(got))
}

func TestReasonixStoredSessions_AbsentStoreIsEmpty(t *testing.T) {
	t.Parallel()
	got, err := reasonixStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: "/Users/dev/project", HomeDir: t.TempDir(), Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestReasonixStoredSessions_SkipsCorruptSidecars(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	slug, _ := reasonixWorkspaceSlug(dir)
	sessions := filepath.Join(home, ".reasonix", "projects", slug, "sessions")

	writeReasonixSession(t, sessions, "good",
		`{"id":"good","turns":1,"preview":"fine","workspace_root":"`+dir+`"}`, "")
	writeFixtureFile(t, filepath.Join(sessions, "broken"+reasonixSessionMetaSuffix), "{ not json")

	got, err := reasonixStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"good"}, handlesOf(got))
}
