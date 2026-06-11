package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

type refreshTestFixture struct {
	svc  *Context
	sink agent.OutputSink
	mock *mockResponseWriter
}

// settingsSeed mirrors the legacy db.UpdateAgentAllSettingsParams scalar fields
// the refresh tests seed an agent with. They are now persisted into the single
// agents.options JSON column, so this helper folds the scalars plus any
// Options JSON into that map before storing.
type settingsSeed struct {
	Model          string
	Effort         string
	PermissionMode string
	Options        string // JSON object, e.g. `{"output_style":"default"}`
}

func (s settingsSeed) options() string {
	opts := map[string]string{}
	if s.Options != "" {
		opts = parseOptions(s.Options)
	}
	if s.Model != "" {
		opts[agent.OptionIDModel] = s.Model
	}
	if s.Effort != "" {
		opts[agent.OptionIDEffort] = s.Effort
	}
	if s.PermissionMode != "" {
		opts[agent.OptionIDPermissionMode] = s.PermissionMode
	}
	return marshalOptions(opts)
}

func newRefreshTestFixture(t *testing.T, seed settingsSeed) refreshTestFixture {
	t.Helper()
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Options:       seed.options(),
	}))

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
	f := newRefreshTestFixture(t, settingsSeed{
		Model:          "opus",
		Effort:         "high",
		PermissionMode: "default",
		Options:        `{"output_style":"default"}`,
	})

	f.sink.PersistSettingsRefresh(map[string]string{
		agent.OptionIDModel:          "opus",
		agent.OptionIDEffort:         "high",
		agent.OptionIDPermissionMode: "default",
		"output_style":               "default",
	})

	assert.Equal(t, int64(0), f.mock.streamCount.Load(),
		"a refresh that matches the DB should not broadcast")

	dbAgent, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	options := parseOptions(dbAgent.Options)
	assert.Equal(t, "opus", options[agent.OptionIDModel], "DB untouched on no-op refresh")
	assert.Equal(t, "high", options[agent.OptionIDEffort])
	assert.Equal(t, "default", options[agent.OptionIDPermissionMode])
}

// TestPersistSettingsRefresh_WritesAndBroadcastsOnChange verifies the
// positive path: when at least one field changes, the row is rewritten and
// a StatusChange event reaches watchers.
func TestPersistSettingsRefresh_WritesAndBroadcastsOnChange(t *testing.T) {
	f := newRefreshTestFixture(t, settingsSeed{
		Model:          "opus",
		Effort:         "auto",
		PermissionMode: "default",
	})

	// CLI resolves "auto" → "high" and reports it back.
	f.sink.PersistSettingsRefresh(map[string]string{
		agent.OptionIDModel:          "opus",
		agent.OptionIDEffort:         "high",
		agent.OptionIDPermissionMode: "default",
	})

	assert.Equal(t, int64(1), f.mock.streamCount.Load(),
		"a refresh that changes effort should broadcast exactly once")

	dbAgent, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "high", parseOptions(dbAgent.Options)[agent.OptionIDEffort], "effort should be persisted")
}

// TestPersistSettingsRefresh_CASPreservesConcurrentWrite verifies the compare-and-swap:
// a refresh merged off a stale snapshot must not clobber a concurrent writer's key. The
// first CAS misses (the row moved on), and the retry re-merges onto the current row, so
// both changes survive instead of the last full-map write winning.
func TestPersistSettingsRefresh_CASPreservesConcurrentWrite(t *testing.T) {
	f := newRefreshTestFixture(t, settingsSeed{Model: "opus", Effort: "high"})
	ctx := context.Background()

	// Capture a snapshot, then simulate a concurrent writer landing a DIFFERENT key after it.
	stale, err := f.svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	require.NoError(t, f.svc.Queries.SetAgentOptions(ctx, db.SetAgentOptionsParams{
		Options: marshalOptions(map[string]string{
			agent.OptionIDModel:          "opus",
			agent.OptionIDEffort:         "high",
			agent.OptionIDPermissionMode: "plan",
		}),
		ID: "agent-1",
	}))

	sink := f.sink.(*agentOutputSink)
	settled, wrote, err := sink.casPersistOptions(stale.Options, map[string]string{agent.OptionIDEffort: "low"})
	require.NoError(t, err)
	require.True(t, wrote)

	got := parseOptions(settled)
	assert.Equal(t, "low", got[agent.OptionIDEffort], "our change is applied")
	assert.Equal(t, "plan", got[agent.OptionIDPermissionMode], "the concurrent writer's key is NOT lost")
	assert.Equal(t, "opus", got[agent.OptionIDModel])

	dbAgent, err := f.svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	persisted := parseOptions(dbAgent.Options)
	assert.Equal(t, "low", persisted[agent.OptionIDEffort], "the merged result is what was persisted")
	assert.Equal(t, "plan", persisted[agent.OptionIDPermissionMode])
}

// TestPersistSettingsRefresh_CASStaleClearDoesNotClobberConcurrentSet is the [S1] regression
// guard: a refresh delta that CLEARS a key the caller's snapshot never held is a no-op against
// that snapshot, hence stale. On the no-op re-read path the CAS re-decides against the LIVE row,
// but it must re-assert only SET (non-empty) keys -- never re-apply the stale clear, which would
// DELETE a value a concurrent writer set after the snapshot.
func TestPersistSettingsRefresh_CASStaleClearDoesNotClobberConcurrentSet(t *testing.T) {
	f := newRefreshTestFixture(t, settingsSeed{Model: "opus", Effort: "high"})
	ctx := context.Background()

	// Snapshot the row BEFORE it holds permissionMode -- the key the stale clear will target.
	stale, err := f.svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)

	// A concurrent writer sets permissionMode on the live row after the snapshot.
	require.NoError(t, f.svc.Queries.SetAgentOptions(ctx, db.SetAgentOptionsParams{
		Options: marshalOptions(map[string]string{
			agent.OptionIDModel:          "opus",
			agent.OptionIDEffort:         "high",
			agent.OptionIDPermissionMode: "plan",
		}),
		ID: "agent-1",
	}))

	// Our refresh CLEARS permissionMode -- but our snapshot never held it, so the clear is a no-op
	// against the snapshot and therefore stale relative to the concurrent set.
	sink := f.sink.(*agentOutputSink)
	settled, wrote, err := sink.casPersistOptions(stale.Options, map[string]string{agent.OptionIDPermissionMode: ""})
	require.NoError(t, err)
	assert.False(t, wrote, "a stale clear of a key absent from our snapshot is a no-op, not a write")

	got := parseOptions(settled)
	assert.Equal(t, "plan", got[agent.OptionIDPermissionMode],
		"the concurrent writer's value is NOT clobbered by the stale clear")

	dbAgent, err := f.svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "plan", parseOptions(dbAgent.Options)[agent.OptionIDPermissionMode],
		"the live row still holds the concurrent set")
}

// TestPersistSettingsRefresh_CASReassertStillAppliesOverConcurrentClear is the complement of the
// stale-clear guard above: dropping stale CLEAR keys on the no-op re-read path must NOT weaken the
// re-ASSERT case the path exists for. A non-empty refresh value the agent confirmed must still be
// written back over a key a concurrent writer cleared.
func TestPersistSettingsRefresh_CASReassertStillAppliesOverConcurrentClear(t *testing.T) {
	f := newRefreshTestFixture(t, settingsSeed{Model: "opus", Effort: "high"})
	ctx := context.Background()

	// Snapshot the row while it still holds effort=high, then a concurrent writer CLEARS effort.
	stale, err := f.svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	require.NoError(t, f.svc.Queries.SetAgentOptions(ctx, db.SetAgentOptionsParams{
		Options: marshalOptions(map[string]string{agent.OptionIDModel: "opus"}),
		ID:      "agent-1",
	}))

	// Our refresh re-asserts effort=high. Against our snapshot it is a no-op (effort already high),
	// but against the live row (effort cleared) it must re-apply.
	sink := f.sink.(*agentOutputSink)
	settled, wrote, err := sink.casPersistOptions(stale.Options, map[string]string{agent.OptionIDEffort: "high"})
	require.NoError(t, err)
	assert.True(t, wrote, "the re-assert is applied over the concurrent clear")
	assert.Equal(t, "high", parseOptions(settled)[agent.OptionIDEffort])

	dbAgent, err := f.svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "high", parseOptions(dbAgent.Options)[agent.OptionIDEffort],
		"the agent's confirmed value is re-asserted on the live row")
}

// TestPersistSettingsRefresh_CASMixedSetAndStaleClearDoesNotClobber guards the [S1] hole the
// pure-clear test above does NOT cover: a refresh that MIXES a genuine SET with a stale CLEAR.
// The genuine set makes the merge non-trivial (newOptions != base), so the delta takes the CAS
// RETRY path -- not the no-op branch. The stale clear (a key the snapshot never held) must still
// be dropped there, or the re-merge after a lost CAS would DELETE a value a concurrent writer set
// on the cleared key. Before the fix, the stale-clear narrowing fired only in the no-op branch,
// so this mixed delta clobbered the concurrent set.
func TestPersistSettingsRefresh_CASMixedSetAndStaleClearDoesNotClobber(t *testing.T) {
	f := newRefreshTestFixture(t, settingsSeed{Model: "opus", Effort: "high"})
	ctx := context.Background()

	// Snapshot the row BEFORE it holds permissionMode -- the key the stale clear targets.
	stale, err := f.svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)

	// A concurrent writer sets permissionMode on the live row after the snapshot.
	require.NoError(t, f.svc.Queries.SetAgentOptions(ctx, db.SetAgentOptionsParams{
		Options: marshalOptions(map[string]string{
			agent.OptionIDModel:          "opus",
			agent.OptionIDEffort:         "high",
			agent.OptionIDPermissionMode: "plan",
		}),
		ID: "agent-1",
	}))

	// Our refresh pairs a GENUINE set (effort high->low) with a STALE clear of permissionMode (a
	// key our snapshot never held). The set forces the CAS retry path; the clear must be ignored.
	sink := f.sink.(*agentOutputSink)
	settled, wrote, err := sink.casPersistOptions(stale.Options, map[string]string{
		agent.OptionIDEffort:         "low",
		agent.OptionIDPermissionMode: "",
	})
	require.NoError(t, err)
	require.True(t, wrote, "the genuine effort change is a real write")

	got := parseOptions(settled)
	assert.Equal(t, "low", got[agent.OptionIDEffort], "our genuine set is applied")
	assert.Equal(t, "plan", got[agent.OptionIDPermissionMode],
		"the concurrent writer's value is NOT clobbered by the stale clear riding alongside the set")
	assert.Equal(t, "opus", got[agent.OptionIDModel])

	dbAgent, err := f.svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	persisted := parseOptions(dbAgent.Options)
	assert.Equal(t, "low", persisted[agent.OptionIDEffort])
	assert.Equal(t, "plan", persisted[agent.OptionIDPermissionMode],
		"the live row still holds the concurrent set")
}

// TestPersistSettingsRefresh_CASGenuineClearAlongsideSetStillApplies is the complement: a clear of
// a key the snapshot DOES hold is genuine and must still delete that key even when paired with a
// set, so the stale-clear narrowing doesn't over-suppress real clears.
func TestPersistSettingsRefresh_CASGenuineClearAlongsideSetStillApplies(t *testing.T) {
	f := newRefreshTestFixture(t, settingsSeed{Model: "opus", Effort: "high", PermissionMode: "plan"})
	ctx := context.Background()

	stale, err := f.svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)

	// Our snapshot holds permissionMode=plan, so clearing it is genuine; pair it with an effort set.
	sink := f.sink.(*agentOutputSink)
	settled, wrote, err := sink.casPersistOptions(stale.Options, map[string]string{
		agent.OptionIDEffort:         "low",
		agent.OptionIDPermissionMode: "",
	})
	require.NoError(t, err)
	require.True(t, wrote)

	got := parseOptions(settled)
	assert.Equal(t, "low", got[agent.OptionIDEffort], "the set is applied")
	_, hasMode := got[agent.OptionIDPermissionMode]
	assert.False(t, hasMode, "the genuine clear of a key the snapshot held still removes it")

	dbAgent, err := f.svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	_, persistedMode := parseOptions(dbAgent.Options)[agent.OptionIDPermissionMode]
	assert.False(t, persistedMode, "the live row reflects the genuine clear")
}

// During the startup window the agent is still inside its provider handshake.
// A settings change made then (e.g. a model switch) is written to the DB by
// UpdateAgentSettings but can't be applied to the not-yet-ready agent, so
// persisting the agent's confirmed LAUNCH settings here would clobber it.
// PersistSettingsRefresh must skip the DB write while startup is in progress;
// runAgentStartup's final handoff persists the confirmed settings with the
// user's change preserved (and relaunches to apply it).
func TestPersistSettingsRefresh_SkipsWriteDuringStartup(t *testing.T) {
	f := newRefreshTestFixture(t, settingsSeed{
		Model:          "opus[1m]", // the model the user switched to mid-startup
		Effort:         "auto",
		PermissionMode: "default",
	})
	f.svc.Output.SetAgentStartingFunc(func(agentID string) bool { return agentID == "agent-1" })

	// The still-starting agent reports its stale launch model; without the gate
	// this write would overwrite the user's opus[1m] choice with sonnet.
	f.sink.PersistSettingsRefresh(map[string]string{
		agent.OptionIDModel:          "sonnet",
		agent.OptionIDEffort:         "low",
		agent.OptionIDPermissionMode: "default",
	})

	dbAgent, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "opus[1m]", parseOptions(dbAgent.Options)[agent.OptionIDModel],
		"a model switched to during startup must survive the launch-settings refresh")
}

func TestPersistSettingsRefresh_PreservesPermissionModeWhenUnreported(t *testing.T) {
	f := newRefreshTestFixture(t, settingsSeed{
		Model:          "opus",
		Effort:         "auto",
		PermissionMode: "plan",
	})

	// Some provider refreshes report model/effort/extras but not permission
	// mode. Empty means "unreported"; it must not clear a user-selected mode.
	f.sink.PersistSettingsRefresh(map[string]string{
		agent.OptionIDModel:  "opus",
		agent.OptionIDEffort: "high",
	})

	dbAgent, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	options := parseOptions(dbAgent.Options)
	assert.Equal(t, "high", options[agent.OptionIDEffort])
	assert.Equal(t, "plan", options[agent.OptionIDPermissionMode])
}

// A nil extraSettings means "unreported": it must leave the stored extras intact,
// even when another field (the model) changes and forces a write. The ACP
// permission-mode providers pass nil here; without this they would clear the row's
// extra_settings to "{}".
func TestPersistSettingsRefresh_PreservesExtrasWhenUnreported(t *testing.T) {
	f := newRefreshTestFixture(t, settingsSeed{
		Model:          "opus",
		PermissionMode: "default",
		Options:        `{"output_style":"verbose"}`,
	})

	f.sink.PersistSettingsRefresh(map[string]string{
		agent.OptionIDModel:          "sonnet",
		agent.OptionIDPermissionMode: "default",
	})

	dbAgent, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	options := parseOptions(dbAgent.Options)
	assert.Equal(t, "sonnet", options[agent.OptionIDModel], "the changed model is persisted")
	assert.Equal(t, "verbose", options["output_style"],
		"nil extras preserves the stored value rather than clearing it")
}

// An empty effort means "unreported": it must leave the stored effort intact,
// even when another field (the model) changes and forces a write. The ACP
// providers never track effort and always pass ""; without this, a runtime
// model switch would clobber a stored effort (e.g. "auto", set by a prior
// UpdateAgentSettings model change) down to "".
func TestPersistSettingsRefresh_PreservesEffortWhenUnreported(t *testing.T) {
	f := newRefreshTestFixture(t, settingsSeed{
		Model:          "openai/gpt-4o",
		Effort:         "auto",
		PermissionMode: "default",
	})

	// An ACP runtime model switch: new model, no effort reported.
	f.sink.PersistSettingsRefresh(map[string]string{
		agent.OptionIDModel:          "openai/gpt-5",
		agent.OptionIDPermissionMode: "default",
	})

	dbAgent, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	options := parseOptions(dbAgent.Options)
	assert.Equal(t, "openai/gpt-5", options[agent.OptionIDModel], "the changed model is persisted")
	assert.Equal(t, "auto", options[agent.OptionIDEffort],
		"empty effort preserves the stored value rather than clearing it")
}

// An empty model means "unreported": it must leave the stored model intact, even
// when another field (the extras) changes and forces a write. The ACP primary-agent
// providers (OpenCode/Kilo) can leave their in-memory model "" when the server
// advertises no current model, so a primary-agent-only config_option_update passes
// an empty model here; without the keep-stored rule that would clobber the stored
// model to "".
func TestPersistSettingsRefresh_PreservesModelWhenUnreported(t *testing.T) {
	f := newRefreshTestFixture(t, settingsSeed{
		Model:          "openai/gpt-5",
		PermissionMode: "default",
		Options:        `{"primaryAgent":"build"}`,
	})

	// An ACP primary-agent switch: new extras, no model reported.
	f.sink.PersistSettingsRefresh(map[string]string{
		agent.OptionIDPermissionMode: "default",
		"primaryAgent":               "plan",
	})

	assert.Equal(t, int64(1), f.mock.streamCount.Load(),
		"a refresh that changes extras should broadcast exactly once")

	dbAgent, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "openai/gpt-5", parseOptions(dbAgent.Options)[agent.OptionIDModel],
		"empty model preserves the stored value rather than clearing it")
}

// A concrete effort still overwrites the stored value: the keep-stored rule is
// gated on "" only, so providers that do report effort (Codex/Pi/Claude) are
// unaffected.
func TestPersistSettingsRefresh_ConcreteEffortOverwrites(t *testing.T) {
	f := newRefreshTestFixture(t, settingsSeed{
		Model:          "gpt-5-codex",
		Effort:         "auto",
		PermissionMode: "default",
	})

	f.sink.PersistSettingsRefresh(map[string]string{
		agent.OptionIDModel:          "gpt-5-codex",
		agent.OptionIDEffort:         "high",
		agent.OptionIDPermissionMode: "default",
	})

	dbAgent, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "high", parseOptions(dbAgent.Options)[agent.OptionIDEffort], "a reported effort overwrites the stored value")
}

// Clearing a stored extra requires reporting it as an EXPLICIT empty value, not
// omitting it: the uniform merge deletes a present-empty key but preserves an
// absent one. This is how the ACP generic axes clear (mergeOptionValues
// emits a cleared surfaced option as ""), so a provider that manages an extra can
// still remove it.
func TestPersistSettingsRefresh_ClearsExtraWithEmptyValue(t *testing.T) {
	f := newRefreshTestFixture(t, settingsSeed{
		Model:          "opus",
		PermissionMode: "default",
		Options:        `{"output_style":"verbose"}`,
	})

	f.sink.PersistSettingsRefresh(map[string]string{
		agent.OptionIDModel:          "opus",
		agent.OptionIDPermissionMode: "default",
		"output_style":               "", // explicit empty -> delete the stored value
	})

	dbAgent, err := f.svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	options := parseOptions(dbAgent.Options)
	assert.NotContains(t, options, "output_style", "an explicit empty value clears the stored extra")
	// The other keys survive.
	assert.Equal(t, "opus", options[agent.OptionIDModel])
	assert.Equal(t, "default", options[agent.OptionIDPermissionMode])
}

// TestPersistCatalogIfChanged covers the post-handshake catalog-discovery persistence
// (BroadcastStatusActive's hook): a running ACP provider that discovers options after the
// startup handoff persisted a narrower catalog must have the grown catalog written to the
// row, so the post-exit offline read surfaces it -- but a transiently EMPTY live catalog
// must never wipe the persisted one.
func TestPersistCatalogIfChanged(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

	const copilot = leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: copilot,
		Options:       marshalOptions(map[string]string{agent.OptionIDModel: "gpt-5"}),
	}))
	// Seed the row with a NARROW catalog (just the model group) -- what the startup
	// handoff would persist before the model list / generics arrive.
	narrow := []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", CurrentValue: "gpt-5", Options: []*leapmuxv1.AvailableOption{{Id: "gpt-5"}}},
	}
	require.NoError(t, svc.Queries.SetAgentOptionGroups(ctx, db.SetAgentOptionGroupsParams{
		OptionGroups: mustMarshalOptionGroups(t, narrow),
		ID:           "agent-1",
	}))

	sink := svc.Output.NewSink("agent-1", copilot).(*agentOutputSink)
	existing, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)

	// The running provider discovered a richer catalog (model + a server-driven config option).
	grown := append(narrow,
		&leapmuxv1.AvailableOptionGroup{Id: "reasoning_effort", Mutable: true, Options: []*leapmuxv1.AvailableOption{{Id: "high"}, {Id: "low"}}})
	sink.persistCatalogIfChanged(existing, grown)

	got, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.NotNil(t, optionids.GroupByID(parseOptionGroups(got.OptionGroups), "reasoning_effort"),
		"a grown catalog is persisted so the post-exit offline read surfaces it")

	// A transiently empty live catalog must NOT wipe the persisted one.
	sink.persistCatalogIfChanged(got, nil)
	after, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.NotNil(t, optionids.GroupByID(parseOptionGroups(after.OptionGroups), "reasoning_effort"),
		"an empty live catalog must not wipe the persisted catalog")

	// [S3] A live catalog with an UNMARSHALABLE group (invalid-UTF-8 label) must NOT overwrite the
	// persisted catalog with a truncated one -- the marshal error skips the write entirely.
	unmarshalable := append(grown,
		&leapmuxv1.AvailableOptionGroup{Id: "broken", Label: "\xff\xfe", Options: []*leapmuxv1.AvailableOption{{Id: "x"}}})
	sink.persistCatalogIfChanged(after, unmarshalable)
	stillThere, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, after.OptionGroups, stillThere.OptionGroups,
		"a catalog that fails to marshal leaves the prior catalog intact rather than truncating it")
}

// TestPersistSettingsRefresh_PersistsGrownCatalog is the catalog-persist regression guard. A
// settings refresh that changes an option VALUE while the RUNNING provider serves a catalog
// richer than the persisted option_groups column must write the grown catalog to the row, not
// just the changed value. A single server-initiated config_option_update that BOTH changes a
// value AND grows the model list routes through PersistSettingsRefresh -- the value change wins
// handleACPConfigOptionUpdate's mutually-exclusive switch, so the listChanged branch that
// persists the catalog via BroadcastStatusActive never runs. Without persisting here the grown
// catalog is lost from the column and the post-exit offline picker serves the stale, narrower one.
func TestPersistSettingsRefresh_PersistsGrownCatalog(t *testing.T) {
	const claude = leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	f := newRefreshTestFixture(t, settingsSeed{Model: "opus", Effort: "auto", PermissionMode: "default"})
	ctx := context.Background()

	// Seed the row with a NARROW catalog (just a model group) -- what the startup handoff would
	// persist before the running provider's richer live catalog is discovered.
	narrow := []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", CurrentValue: "opus", Options: []*leapmuxv1.AvailableOption{{Id: "opus"}}},
	}
	require.NoError(t, f.svc.Queries.SetAgentOptionGroups(ctx, db.SetAgentOptionGroupsParams{
		OptionGroups: mustMarshalOptionGroups(t, narrow),
		ID:           "agent-1",
	}))

	// Register a running Claude agent so the live catalog (Fast Mode + thinking + model groups)
	// is richer than the narrow seed.
	_, err := f.svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID:    "agent-1",
		WorkingDir: t.TempDir(),
		Options:    map[string]string{agent.OptionIDModel: "opus"},
	}, f.sink)
	require.NoError(t, err)
	defer f.svc.Agents.StopAgent("agent-1")

	// Precondition: the running Claude catalog surfaces Fast Mode, which the narrow seed lacks.
	require.NotNil(t, optionids.GroupByID(f.svc.Agents.OptionGroups("agent-1", claude, "opus"), agent.ClaudeOptionFastMode),
		"precondition: the running catalog is richer than the persisted seed")

	// A refresh that changes a VALUE (effort auto -> high) -- the case that routes here rather
	// than through BroadcastStatusActive.
	f.sink.PersistSettingsRefresh(map[string]string{
		agent.OptionIDModel:          "opus",
		agent.OptionIDEffort:         "high",
		agent.OptionIDPermissionMode: "default",
	})

	got, err := f.svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.NotNil(t, optionids.GroupByID(parseOptionGroups(got.OptionGroups), agent.ClaudeOptionFastMode),
		"a value-change refresh on a running agent persists the grown live catalog, not just the value")
}

// TestPersistSettingsRefresh_SkipsCatalogPersistDuringStartup verifies the catalog persist is
// gated the same way the options DB write is: while the agent is still inside its startup
// handshake, a refresh must NOT write the grown catalog. runAgentStartup's final handoff persists
// the confirmed catalog atomically, and a mid-startup write here could clobber a user change.
func TestPersistSettingsRefresh_SkipsCatalogPersistDuringStartup(t *testing.T) {
	const claude = leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	f := newRefreshTestFixture(t, settingsSeed{Model: "opus", Effort: "auto", PermissionMode: "default"})
	ctx := context.Background()

	narrow := []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", CurrentValue: "opus", Options: []*leapmuxv1.AvailableOption{{Id: "opus"}}},
	}
	require.NoError(t, f.svc.Queries.SetAgentOptionGroups(ctx, db.SetAgentOptionGroupsParams{
		OptionGroups: mustMarshalOptionGroups(t, narrow),
		ID:           "agent-1",
	}))

	_, err := f.svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID:    "agent-1",
		WorkingDir: t.TempDir(),
		Options:    map[string]string{agent.OptionIDModel: "opus"},
	}, f.sink)
	require.NoError(t, err)
	defer f.svc.Agents.StopAgent("agent-1")
	require.NotNil(t, optionids.GroupByID(f.svc.Agents.OptionGroups("agent-1", claude, "opus"), agent.ClaudeOptionFastMode),
		"precondition: the running catalog is richer than the persisted seed")

	f.svc.Output.SetAgentStartingFunc(func(agentID string) bool { return agentID == "agent-1" })

	f.sink.PersistSettingsRefresh(map[string]string{
		agent.OptionIDModel:          "opus",
		agent.OptionIDEffort:         "high",
		agent.OptionIDPermissionMode: "default",
	})

	got, err := f.svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Nil(t, optionids.GroupByID(parseOptionGroups(got.OptionGroups), agent.ClaudeOptionFastMode),
		"during startup the grown catalog must not be persisted; the narrow seed is left for the handoff")
	assert.NotNil(t, optionids.GroupByID(parseOptionGroups(got.OptionGroups), agent.OptionIDModel),
		"the narrow seed's model group is left intact")
}
