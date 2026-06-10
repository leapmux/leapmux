package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

type refreshTestFixture struct {
	svc  *Context
	sink agent.OutputSink
	mock *mockResponseWriter
}

func newRefreshTestFixture(t *testing.T, seed db.UpdateAgentAllSettingsParams) refreshTestFixture {
	t.Helper()
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Model:         seed.Model,
		Effort:        seed.Effort,
	}))
	seed.ID = "agent-1"
	require.NoError(t, svc.Queries.UpdateAgentAllSettings(ctx, seed))

	w, mock := newTestWatcher("ch-1")
	svc.Watchers.WatchAgent("agent-1", w)

	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	return refreshTestFixture{svc: svc, sink: sink, mock: mock}
}

// TestPersistSettingsRefresh_SkipsWhenUnchanged verifies that a refresh
// matching the persisted row does no DB write and fires no StatusChange
// event. Refresh runs at startup and after every UpdateSettings, and the
// common case is that the CLI confirms values we already stored — skipping
// the no-op path avoids redundant DB churn and avoids waking every
// connected frontend to re-render identical settings.
func TestPersistSettingsRefresh_SkipsWhenUnchanged(t *testing.T) {
	f := newRefreshTestFixture(t, db.UpdateAgentAllSettingsParams{
		Model:          "opus",
		Effort:         "high",
		PermissionMode: "default",
		ExtraSettings:  `{"output_style":"default"}`,
	})

	f.sink.PersistSettingsRefresh("opus", "high", "default", map[string]string{"output_style": "default"})

	assert.Equal(t, int64(0), f.mock.streamCount.Load(),
		"a refresh that matches the DB should not broadcast")

	dbAgent, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "opus", dbAgent.Model, "DB untouched on no-op refresh")
	assert.Equal(t, "high", dbAgent.Effort)
	assert.Equal(t, "default", dbAgent.PermissionMode)
}

// TestPersistSettingsRefresh_WritesAndBroadcastsOnChange verifies the
// positive path: when at least one field changes, the row is rewritten and
// a StatusChange event reaches watchers.
func TestPersistSettingsRefresh_WritesAndBroadcastsOnChange(t *testing.T) {
	f := newRefreshTestFixture(t, db.UpdateAgentAllSettingsParams{
		Model:          "opus",
		Effort:         "auto",
		PermissionMode: "default",
	})

	// CLI resolves "auto" → "high" and reports it back.
	f.sink.PersistSettingsRefresh("opus", "high", "default", nil)

	assert.Equal(t, int64(1), f.mock.streamCount.Load(),
		"a refresh that changes effort should broadcast exactly once")

	dbAgent, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "high", dbAgent.Effort, "effort should be persisted")
}

func TestPersistSettingsRefresh_PreservesPermissionModeWhenUnreported(t *testing.T) {
	f := newRefreshTestFixture(t, db.UpdateAgentAllSettingsParams{
		Model:          "opus",
		Effort:         "auto",
		PermissionMode: "plan",
	})

	// Some provider refreshes report model/effort/extras but not permission
	// mode. Empty means "unreported"; it must not clear a user-selected mode.
	f.sink.PersistSettingsRefresh("opus", "high", "", nil)

	dbAgent, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "high", dbAgent.Effort)
	assert.Equal(t, "plan", dbAgent.PermissionMode)
}

// A nil extraSettings means "unreported": it must leave the stored extras intact,
// even when another field (the model) changes and forces a write. The ACP
// permission-mode providers pass nil here; without this they would clear the row's
// extra_settings to "{}".
func TestPersistSettingsRefresh_PreservesExtrasWhenUnreported(t *testing.T) {
	f := newRefreshTestFixture(t, db.UpdateAgentAllSettingsParams{
		Model:          "opus",
		PermissionMode: "default",
		ExtraSettings:  `{"output_style":"verbose"}`,
	})

	f.sink.PersistSettingsRefresh("sonnet", "", "default", nil)

	dbAgent, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "sonnet", dbAgent.Model, "the changed model is persisted")
	assert.Equal(t, `{"output_style":"verbose"}`, dbAgent.ExtraSettings,
		"nil extras preserves the stored value rather than clearing it")
}

// An empty effort means "unreported": it must leave the stored effort intact,
// even when another field (the model) changes and forces a write. The ACP
// providers never track effort and always pass ""; without this, a runtime
// model switch would clobber a stored effort (e.g. "auto", set by a prior
// UpdateAgentSettings model change) down to "".
func TestPersistSettingsRefresh_PreservesEffortWhenUnreported(t *testing.T) {
	f := newRefreshTestFixture(t, db.UpdateAgentAllSettingsParams{
		Model:          "openai/gpt-4o",
		Effort:         "auto",
		PermissionMode: "default",
	})

	// An ACP runtime model switch: new model, no effort reported.
	f.sink.PersistSettingsRefresh("openai/gpt-5", "", "default", nil)

	dbAgent, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "openai/gpt-5", dbAgent.Model, "the changed model is persisted")
	assert.Equal(t, "auto", dbAgent.Effort,
		"empty effort preserves the stored value rather than clearing it")
}

// An empty model means "unreported": it must leave the stored model intact, even
// when another field (the extras) changes and forces a write. The ACP primary-agent
// providers (OpenCode/Kilo) can leave their in-memory model "" when the server
// advertises no current model, so a primary-agent-only config_option_update passes
// an empty model here; without the keep-stored rule that would clobber the stored
// model to "".
func TestPersistSettingsRefresh_PreservesModelWhenUnreported(t *testing.T) {
	f := newRefreshTestFixture(t, db.UpdateAgentAllSettingsParams{
		Model:          "openai/gpt-5",
		PermissionMode: "default",
		ExtraSettings:  `{"primaryAgent":"build"}`,
	})

	// An ACP primary-agent switch: new extras, no model reported.
	f.sink.PersistSettingsRefresh("", "", "default", map[string]string{"primaryAgent": "plan"})

	assert.Equal(t, int64(1), f.mock.streamCount.Load(),
		"a refresh that changes extras should broadcast exactly once")

	dbAgent, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "openai/gpt-5", dbAgent.Model,
		"empty model preserves the stored value rather than clearing it")
}

// A concrete effort still overwrites the stored value: the keep-stored rule is
// gated on "" only, so providers that do report effort (Codex/Pi/Claude) are
// unaffected.
func TestPersistSettingsRefresh_ConcreteEffortOverwrites(t *testing.T) {
	f := newRefreshTestFixture(t, db.UpdateAgentAllSettingsParams{
		Model:          "gpt-5-codex",
		Effort:         "auto",
		PermissionMode: "default",
	})

	f.sink.PersistSettingsRefresh("gpt-5-codex", "high", "default", map[string]string{})

	dbAgent, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "high", dbAgent.Effort, "a reported effort overwrites the stored value")
}

// An explicit (non-nil) empty map is distinct from nil: it clears the stored
// extras to "{}". This keeps the providers that do manage extras able to remove
// them.
func TestPersistSettingsRefresh_ClearsExtrasWithEmptyMap(t *testing.T) {
	f := newRefreshTestFixture(t, db.UpdateAgentAllSettingsParams{
		Model:          "opus",
		PermissionMode: "default",
		ExtraSettings:  `{"output_style":"verbose"}`,
	})

	f.sink.PersistSettingsRefresh("opus", "", "default", map[string]string{})

	dbAgent, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "{}", dbAgent.ExtraSettings, "an empty map clears the stored extras")
}
