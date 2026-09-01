package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sessionStoreFixture seeds one provider's store under `home` for `workingDir`,
// and returns the handle the reader must find.
type sessionStoreFixture func(t *testing.T, home, workingDir string) string

// sessionStoreFixtures states, for every registered provider, how to build a
// store it must read. A provider absent from this table fails the sweep below.
//
// It is deliberately a TABLE and not a pointer comparison against
// noopProvider's default. Go promotes an embedded method, and nothing in
// reflect reports whether a plugin declared ListStoredSessions or inherited it
// -- and the six ACP providers all reach it through one shared method, so they
// are indistinguishable that way regardless. Seeding a store and asserting the
// provider finds it proves the thing that matters: the reader exists AND the
// registration wired it up. Forgetting `listStoredSessions:` on an ACP
// registration is exactly the mistake a weaker check would miss.
var sessionStoreFixtures = map[leapmuxv1.AgentProvider]sessionStoreFixture{
	leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE: func(t *testing.T, home, dir string) string {
		writeClaudeTranscript(t, filepath.Join(home, ".claude", "projects", mangleClaudePath(dir)),
			"claude-session", time.Now(), claudeUserRecord(dir, "claude-session", "work"))
		return "claude-session"
	},
	leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX: func(t *testing.T, home, dir string) string {
		seedCodexDB(t, filepath.Join(home, ".codex", "state_5.sqlite"), []codexThreadRow{
			{id: "codex-thread", cwd: dir, title: "work", updatedAtMS: 1_000},
		})
		return "codex-thread"
	},
	leapmuxv1.AgentProvider_AGENT_PROVIDER_PI: func(t *testing.T, home, dir string) string {
		writePiTranscript(t, filepath.Join(home, ".pi", "agent", "sessions"), dir,
			"2026-09-01T12-00-00-000Z_pi-session.jsonl", time.Now(),
			`{"type":"session","version":3,"id":"pi-session","cwd":"`+dir+`"}`)
		return "pi-session"
	},
	leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE: func(t *testing.T, home, dir string) string {
		seedOpenCodeFamilyDB(t, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), dir)
		return "ses_new"
	},
	leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE: func(t *testing.T, home, dir string) string {
		seedOpenCodeFamilyDB(t, filepath.Join(home, ".local", "share", "opencode", "opencode.db"), dir)
		return "ses_new"
	},
	leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO: func(t *testing.T, home, dir string) string {
		seedOpenCodeFamilyDB(t, filepath.Join(home, ".local", "share", "kilo", "kilo.db"), dir)
		return "ses_new"
	},
	leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE: func(t *testing.T, home, dir string) string {
		seedGooseDB(t, filepath.Join(home, ".local", "share", "goose", "sessions", "sessions.db"),
			[]gooseSessionRow{{
				id: "goose-session", name: "work", kind: "acp", workingDir: dir,
				createdAt: "2026-08-30 10:00:00", updatedAt: "2026-08-30 10:00:00",
			}})
		return "goose-session"
	},
	leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX: func(t *testing.T, home, dir string) string {
		slug, ok := reasonixWorkspaceSlug(dir)
		require.True(t, ok)
		writeReasonixSession(t, filepath.Join(home, ".reasonix", "projects", slug, "sessions"),
			"reasonix-session",
			`{"id":"reasonix-session","turns":1,"preview":"work","workspace_root":"`+dir+`"}`, "")
		return "reasonix-session"
	},
	leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR: func(t *testing.T, home, dir string) string {
		writeCursorSession(t, home, dir, "cursor-agent",
			`{"agentId":"cursor-agent","name":"work"}`, time.Now())
		return "cursor-agent"
	},
	leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT: func(t *testing.T, home, dir string) string {
		writeCopilotSession(t, home, "copilot-session", dir, "work",
			"2026-08-27T20:11:35.263Z", "2026-08-28T06:06:28.114Z")
		return "copilot-session"
	},
}

// TestEveryRegisteredProviderReadsItsSessionStore sweeps the registry rather
// than naming providers, so a provider added after this was written fails until
// somebody decides what its session store is.
func TestEveryRegisteredProviderReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	require.NotEmpty(t, providerRegistry, "the sweep proves nothing against an empty registry")

	for id := range providerRegistry {
		t.Run(id.String(), func(t *testing.T) {
			t.Parallel()
			seed, ok := sessionStoreFixtures[id]
			require.Truef(t, ok,
				"%v has no session-store fixture. Write its reader in that provider's own file "+
					"and add the fixture here, or state here why its sessions cannot be enumerated.", id)

			home := t.TempDir()
			// Under the temp home, never the developer's real one: this walks
			// provider stores, and a test must not read a person's history.
			dir := filepath.Join(home, "workspace", "project")
			want := seed(t, home, dir)

			got, err := ProviderFor(id).ListStoredSessions(context.Background(), StoredSessionQuery{
				WorkingDir: dir,
				HomeDir:    home,
				Getenv:     fixtureEnv(nil),
			})
			require.NoError(t, err)
			assert.Containsf(t, handlesOf(got), want,
				"%v must find the session seeded in its own store; check that its registration wires the reader", id)
		})
	}
}

// TestEveryRegisteredProviderTreatsAnAbsentStoreAsEmpty pins the other half of
// the contract: a CLI the user never ran lists nothing and fails nothing, so an
// empty picker never becomes an error banner.
func TestEveryRegisteredProviderTreatsAnAbsentStoreAsEmpty(t *testing.T) {
	t.Parallel()

	for id := range providerRegistry {
		t.Run(id.String(), func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			got, err := ProviderFor(id).ListStoredSessions(context.Background(), StoredSessionQuery{
				WorkingDir: filepath.Join(home, "workspace", "project"),
				HomeDir:    home,
				Getenv:     fixtureEnv(nil),
			})
			assert.NoErrorf(t, err, "%v: an absent store is the normal state, not a failure", id)
			assert.Emptyf(t, got, "%v", id)
		})
	}
}

// TestNoopProviderListsNothing pins the default a provider inherits by saying
// nothing: it offers nothing, and does not fail.
func TestNoopProviderListsNothing(t *testing.T) {
	t.Parallel()
	sessions, err := noopProvider{}.ListStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: "/some/dir",
	})
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

// TestACPProviderWithoutAReaderListsNothing pins the nil guard that keeps
// acpProvider provider-neutral: it knows a reader may be absent, and nothing
// about where any of them is.
func TestACPProviderWithoutAReaderListsNothing(t *testing.T) {
	t.Parallel()
	sessions, err := acpProvider{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR}.
		ListStoredSessions(context.Background(), StoredSessionQuery{WorkingDir: "/some/dir"})
	require.NoError(t, err)
	assert.Empty(t, sessions)
}
