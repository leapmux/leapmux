package agent_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errTestStart is what testRegistration's Start returns, so a test can tell the
// registered start function from any other.
var errTestStart = errors.New("test start")

type nilProviderPlugin struct{ agent.ProviderDefaults }

// testRegistration is a Registration that NewRegistry accepts: a plugin, a start
// function, and a locator that finds a program without probing anything.
func testRegistration(provider leapmuxv1.AgentProvider) agent.Registration {
	return agent.Registration{
		Provider: provider,
		Plugin:   agent.ProviderDefaults{},
		Start: func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
			return nil, errTestStart
		},
		Locator: agenttest.AnsweringLocator(launch.Spec{Program: "prog"}, launch.Found),
	}
}

func TestNewRegistryRefusesAnUnusableRegistration(t *testing.T) {
	t.Parallel()

	withPermissionGroup := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	withPermissionGroup.OptionGroups = []*leapmuxv1.AvailableOptionGroup{{Id: agent.OptionIDPermissionMode}}
	withPermissionGroup.FixedPermissionModes = true
	_, err := agent.NewRegistry(withPermissionGroup)
	require.NoError(t, err, "fixed permission modes with a static permission-mode group is valid")

	// Each case states the reason that the error must give, so a registration
	// that fails for some other reason fails the case.
	for name, tc := range map[string]struct {
		mutate func(*agent.Registration)
		reason string
	}{
		"an UNSPECIFIED provider": {
			func(r *agent.Registration) { r.Provider = leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED },
			"not a registrable provider",
		},
		"a value outside the enum": {
			func(r *agent.Registration) { r.Provider = leapmuxv1.AgentProvider(999) },
			"not a registrable provider",
		},
		"a nil plugin":       {func(r *agent.Registration) { r.Plugin = nil }, "nil Plugin"},
		"a typed nil plugin": {func(r *agent.Registration) { r.Plugin = (*nilProviderPlugin)(nil) }, "nil Plugin"},
		"a nil start":        {func(r *agent.Registration) { r.Start = nil }, "nil Start"},
		"a zero locator":     {func(r *agent.Registration) { r.Locator = launch.Locator{} }, "no single way"},
		"an empty name list": {func(r *agent.Registration) { r.Locator = launch.Binaries() }, "no single way"},
		"a nil resolver":     {func(r *agent.Registration) { r.Locator = launch.Custom(nil) }, "no single way"},
		"fixed permission modes with no permission-mode group": {
			func(r *agent.Registration) { r.FixedPermissionModes = true },
			"FixedPermissionModes with no static permission-mode group",
		},
	} {
		t.Run(name, func(t *testing.T) {
			reg := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
			tc.mutate(&reg)
			r, err := agent.NewRegistry(reg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.reason)
			assert.Nil(t, r)
		})
	}
}

func TestNewRegistryRefusesADuplicateProvider(t *testing.T) {
	t.Parallel()

	_, err := agent.NewRegistry(
		testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI),
		testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "registered twice")
}

// Every refusal is reported at once, so a wiring mistake in two providers costs
// one failed start rather than two.
func TestNewRegistryReportsEveryRefusal(t *testing.T) {
	t.Parallel()

	noPlugin := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI)
	noPlugin.Plugin = nil
	noStart := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	noStart.Start = nil
	_, err := agent.NewRegistry(noPlugin, noStart)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil Plugin")
	assert.Contains(t, err.Error(), "nil Start")
}

func TestNewRegistryDefaultsTheModelSubGroups(t *testing.T) {
	t.Parallel()

	r := agenttest.MustNewRegistry(testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI))
	reg, ok := r.Registration(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI)
	require.True(t, ok)
	require.NotNil(t, reg.ModelSubGroups, "a nil ModelSubGroups selects EffortSubGroups")
	model := &agent.ModelInfo{Id: "m", SupportedEfforts: []*agent.EffortInfo{{Id: agent.EffortHigh, Name: "High"}}}
	assert.Equal(t, agent.EffortSubGroups(model), reg.ModelSubGroups(model))
}

func TestRegistryProvidersIsSortedAndOwnedByTheCaller(t *testing.T) {
	t.Parallel()

	r := agenttest.MustNewRegistry(
		testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE),
		testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE),
		testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI),
	)
	want := []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
	}
	got := r.Providers()
	assert.Equal(t, want, got)
	got[0] = leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	assert.Equal(t, want, r.Providers(), "a caller that changes its copy cannot change the registry")
}

// An unregistered provider -- only UNSPECIFIED reaches the lookups -- answers
// every question with the neutral default rather than a nil plugin.
func TestRegistryAnswersTheNeutralDefaultForAnUnregisteredProvider(t *testing.T) {
	t.Parallel()

	r := agenttest.MustNewRegistry(testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI))
	unknown := leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED
	assert.Equal(t, agent.Provider(agent.ProviderDefaults{}), r.Plugin(unknown))
	assert.Equal(t, map[string]bool{agent.OptionIDModel: true}, r.KnownOptionIDs(unknown))
	assert.Empty(t, r.DefaultModel(unknown))
	assert.Empty(t, r.FallbackPermissionMode(unknown))
	assert.Nil(t, r.StaticOptionGroups(unknown))
	assert.False(t, r.ManagesEffort(unknown))
	assert.Equal(t, "as-is", r.NormalizeModelID(unknown, "as-is"))
	_, ok := r.Registration(unknown)
	assert.False(t, ok)
}

// interruptPlugin answers IsInterrupt from its own rule, so a test can tell
// that the registry asked the registered plugin.
type interruptPlugin struct{ agent.ProviderDefaults }

func (interruptPlugin) IsInterrupt(content string) bool { return content == "stop" }

// Each lookup answers from the Registration of the provider that it identifies,
// and never from the Registration of another provider.
func TestRegistryAnswersFromTheRegistrationOfTheProvider(t *testing.T) {
	t.Parallel()

	permissionGroup := &leapmuxv1.AvailableOptionGroup{Id: agent.OptionIDPermissionMode}
	full := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	full.Plugin = interruptPlugin{}
	full.OptionGroups = []*leapmuxv1.AvailableOptionGroup{permissionGroup}
	full.FixedPermissionModes = true
	full.AdditionalOptionIDs = []string{agent.OptionIDEffort, "sandbox"}
	full.PersistedOnlyOptionIDs = []string{"backend"}
	full.ProviderOptionDefaults = map[string]string{"sandbox": "read-only"}
	full.PermissionDefaults = agent.PermissionDefaults{
		NewSession: map[string]string{agent.OptionIDPermissionMode: "ask"},
		Fallback:   "ask",
	}
	full.ManagesEffort = true
	bare := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI)
	r := agenttest.MustNewRegistry(full, bare)
	codex, pi := full.Provider, bare.Provider

	assert.True(t, r.IsInterrupt(codex, "stop"), "the registered plugin decides")
	assert.False(t, r.IsInterrupt(codex, "go"))
	assert.False(t, r.IsInterrupt(pi, "stop"), "another provider's plugin never decides")

	assert.True(t, r.HasFixedPermissionModes(codex))
	assert.False(t, r.HasFixedPermissionModes(pi))

	assert.Equal(t, full.PermissionDefaults.NewSession, r.NewAgentOptionDefaults(codex))
	assert.Empty(t, r.NewAgentOptionDefaults(pi))
	assert.Equal(t, "ask", r.FallbackPermissionMode(codex))
	assert.Equal(t, full.ProviderOptionDefaults, r.ProviderOptionDefaults(codex))
	assert.Nil(t, r.ProviderOptionDefaults(pi))
	assert.Equal(t, full.OptionGroups, r.StaticOptionGroups(codex))

	assert.Equal(t, map[string]bool{
		agent.OptionIDModel:          true,
		agent.OptionIDPermissionMode: true,
		agent.OptionIDEffort:         true,
		"sandbox":                    true,
		"backend":                    true,
	}, r.KnownOptionIDs(codex), "the model axis, the static groups, the additional ids, and the persisted-only ids")
	assert.Equal(t, map[string]bool{agent.OptionIDModel: true}, r.KnownOptionIDs(pi))

	persisted := r.PersistedOnlyOptionIDs(codex)
	assert.Equal(t, map[string]bool{"backend": true}, persisted)
	persisted["model"] = true
	assert.Equal(t, map[string]bool{"backend": true}, r.PersistedOnlyOptionIDs(codex),
		"each call builds a new set, so a caller cannot change the registry")
	assert.Empty(t, r.PersistedOnlyOptionIDs(pi))

	assert.True(t, r.ManagesEffort(codex), "the flag alone is enough, with no catalog")
	assert.False(t, r.ManagesEffort(pi))
}

// ManagesEffort without the flag reads the static catalog: one model that lists
// an effort tier is enough, and a nil entry is skipped.
func TestRegistryManagesEffortReadsTheCatalog(t *testing.T) {
	t.Parallel()

	withTiers := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	withTiers.DefaultModels = []*agent.ModelInfo{
		nil,
		{Id: "plain"},
		{Id: "tiered", SupportedEfforts: []*agent.EffortInfo{{Id: agent.EffortHigh, Name: "High"}}},
	}
	withoutTiers := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE)
	withoutTiers.DefaultModels = []*agent.ModelInfo{nil, {Id: "plain"}}
	r := agenttest.MustNewRegistry(withTiers, withoutTiers)

	assert.True(t, r.ManagesEffort(withTiers.Provider))
	assert.False(t, r.ManagesEffort(withoutTiers.Provider))
}

// PermissionModeOrDefault replaces an empty mode with the fallback. It replaces
// the stored sentinel "default" only when the provider falls back to some other
// real mode.
func TestRegistryPermissionModeOrDefault(t *testing.T) {
	t.Parallel()

	with := func(provider leapmuxv1.AgentProvider, fallback string) agent.Registration {
		reg := testRegistration(provider)
		reg.PermissionDefaults.Fallback = fallback
		return reg
	}
	codex := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	pi := leapmuxv1.AgentProvider_AGENT_PROVIDER_PI
	r := agenttest.MustNewRegistry(
		with(codex, "ask"),
		with(claude, agent.PermissionModeStoredSentinel),
		with(pi, ""),
	)
	sentinel := agent.PermissionModeStoredSentinel

	for _, tc := range []struct {
		name     string
		provider leapmuxv1.AgentProvider
		mode     string
		want     string
	}{
		{"an empty mode takes the fallback", codex, "", "ask"},
		{"the sentinel takes a different fallback", codex, sentinel, "ask"},
		{"an explicit mode stays", codex, "never", "never"},
		{"the sentinel stays where it is the fallback", claude, sentinel, sentinel},
		{"the sentinel stays with no permission axis", pi, sentinel, sentinel},
		{"an empty mode stays empty with no permission axis", pi, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, r.PermissionModeOrDefault(tc.provider, tc.mode))
		})
	}
}

// The registry reads the operator overrides from the variables that the
// Registration gives. A provider that gives no variable has no override, even
// while the variables of another provider are set.
func TestRegistryReadsTheOperatorOverrides(t *testing.T) {
	withKeys := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	withKeys.EnvModelKey = "LEAPMUX_TEST_REGISTRY_OVERRIDE_MODEL"
	withKeys.EnvEffortKey = "LEAPMUX_TEST_REGISTRY_OVERRIDE_EFFORT"
	withKeys.DefaultModels = []*agent.ModelInfo{{Id: "catalog", IsDefault: true}}
	withoutKeys := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI)
	r := agenttest.MustNewRegistry(withKeys, withoutKeys)
	codex, pi := withKeys.Provider, withoutKeys.Provider

	t.Setenv(withKeys.EnvModelKey, "")
	t.Setenv(withKeys.EnvEffortKey, "")
	assert.Empty(t, r.DefaultModelEnvOverride(codex), "an unset variable is no override")
	assert.Empty(t, r.EffortEnvOverride(codex))
	assert.Equal(t, "catalog", r.DefaultModel(codex))

	t.Setenv(withKeys.EnvModelKey, "from-env")
	t.Setenv(withKeys.EnvEffortKey, agent.EffortHigh)
	assert.Equal(t, "from-env", r.DefaultModelEnvOverride(codex))
	assert.Equal(t, "from-env", r.DefaultModel(codex), "the override wins over the catalog")
	assert.Equal(t, agent.EffortHigh, r.EffortEnvOverride(codex))

	assert.Empty(t, r.DefaultModelEnvOverride(pi))
	assert.Empty(t, r.EffortEnvOverride(pi))
}

func TestRegistrationDefaultModel(t *testing.T) {
	reg := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI)
	assert.Empty(t, reg.DefaultModel(), "no catalog and no override has no default")

	reg.DefaultModels = []*agent.ModelInfo{{Id: "first"}, {Id: "marked", IsDefault: true}}
	assert.Equal(t, "marked", reg.DefaultModel(), "the entry the catalog marks wins over the first")

	reg.DefaultModels = []*agent.ModelInfo{{Id: "first"}, {Id: "second"}}
	assert.Equal(t, "first", reg.DefaultModel(), "with no marked entry the first one is the default")

	reg.EnvModelKey = "LEAPMUX_TEST_REGISTRY_DEFAULT_MODEL"
	t.Setenv(reg.EnvModelKey, "from-env")
	assert.Equal(t, "from-env", reg.DefaultModel(), "the operator override wins over the catalog")
}

// ListAvailable turns each provider's own answer into one scan result: found
// providers are listed, and a single provider that established nothing makes the
// whole scan retryable.
func TestRegistryListAvailable(t *testing.T) {
	t.Parallel()

	with := func(provider leapmuxv1.AgentProvider, res launch.Resolution) agent.Registration {
		reg := testRegistration(provider)
		reg.Locator = agenttest.AnsweringLocator(launch.Spec{Program: "prog"}, res)
		return reg
	}
	r := agenttest.MustNewRegistry(
		with(leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE, launch.Found),
		with(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, launch.Missing),
		with(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, launch.Found),
	)
	providers, complete := r.ListAvailable(context.Background(), "/bin/sh", false)
	assert.True(t, complete)
	assert.Equal(t, []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
	}, providers, "found providers in enum order")

	incomplete := agenttest.MustNewRegistry(
		with(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, launch.Found),
		with(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, launch.Unknown),
	)
	providers, complete = incomplete.ListAvailable(context.Background(), "/bin/sh", false)
	assert.False(t, complete, "one provider that established nothing makes the scan retryable")
	assert.Nil(t, providers)

	empty := agenttest.MustNewRegistry()
	providers, complete = empty.ListAvailable(context.Background(), "/bin/sh", false)
	assert.True(t, complete, "a registry with no provider scans nothing, completely")
	assert.Empty(t, providers)
}

func TestNewManagerRequiresARegistry(t *testing.T) {
	t.Parallel()

	assert.PanicsWithValue(t, "agent: NewManager requires a registry", func() { agent.NewManager(nil, nil) })
	r := agenttest.MustNewRegistry(testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI))
	assert.Same(t, r, agent.NewManager(r, nil).Registry())
}

// StartAgent starts the provider through its registered Start, and StartAgentWith
// through the one the caller supplies.
func TestManagerStartsThroughTheRegistration(t *testing.T) {
	t.Parallel()

	m := agent.NewManager(agenttest.MustNewRegistry(testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI)), nil)
	opts := agent.Options{AgentID: "a1", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_PI}
	_, err := m.StartAgent(context.Background(), opts, nil)
	assert.ErrorIs(t, err, errTestStart, "StartAgent runs the registered Start")

	errSupplied := errors.New("supplied start")
	_, err = m.StartAgentWith(context.Background(), opts, nil, func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
		return nil, errSupplied
	})
	assert.ErrorIs(t, err, errSupplied, "StartAgentWith runs the start function it is given")

	opts.AgentProvider = leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	_, err = m.StartAgent(context.Background(), opts, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported agent provider")
}

// A provider that registers no normalizer gets its model id back unchanged.
func TestRegistryNormalizeModelIDKeepsTheIDWithoutANormalizer(t *testing.T) {
	t.Parallel()
	r := agenttest.MustNewRegistry(testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE))
	assert.Equal(t, "gpt-5.5", r.NormalizeModelID(leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, "gpt-5.5"))
}

// A shell that never ran is not evidence that no provider is installed.
//
// Completeness used to be read from ctx.Err() alone, so every NON-deadline way a
// probe proves nothing -- a $SHELL that cannot exec, a missing interpreter,
// EACCES, a fork failure under load, a login profile that exits non-zero --
// returned an AUTHORITATIVE empty list. The worker sends "provider scan did not
// finish; retry" only on !complete, so the client showed "no agent providers
// installed" with no retry, although every CLI was installed.
func TestListAvailableProvidersIncompleteWhenTheShellCannotStart(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-shell")
	reg := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	reg.Locator = launch.Binaries("codex")
	r := agenttest.MustNewRegistry(reg)

	providers, complete := r.ListAvailable(context.Background(), missing, false)
	assert.Empty(t, providers)
	assert.False(t, complete,
		"a shell that never ran is not evidence that no provider is installed")
}

// PutAgentForTest refuses an agent that the manager cannot compare by identity,
// at the call that planted it, and registers a pointer.
func TestPutAgentForTestRefusesAnAgentTheManagerCannotCompare(t *testing.T) {
	t.Parallel()

	m := agent.NewManager(agenttest.MustNewRegistry(testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI)), nil)
	assert.PanicsWithValue(t,
		fmt.Sprintf("agent: PutAgentForTest needs a comparable agent, such as a pointer; got %T", agenttest.GroupsAgent{}),
		func() { m.PutAgentForTest("value", agenttest.GroupsAgent{}) })

	planted := &agenttest.GroupsAgent{}
	m.PutAgentForTest("pointer", planted)
	assert.True(t, m.HasAgent("pointer"))
}

func TestListAvailableProviders_ExpiredContextIsIncomplete(t *testing.T) {
	// A provider found by a PATH probe, so the scan runs the shell that the
	// cancelled context stops.
	reg := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	reg.Locator = launch.Binaries("codex")
	r := agenttest.MustNewRegistry(reg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	providers, complete := r.ListAvailable(ctx, "/bin/sh", false)
	assert.False(t, complete, "a cancelled scan is not evidence of absence")
	assert.Empty(t, providers)
}

// The companion case: when every provider's probe RUNS and answers absent, the
// scan is complete, and an empty list is then the truth. The shell half -- a
// shell that answers "absent" is a conclusive Missing -- is pinned by
// TestLaunchLocatorAvailableIsMissingWhenTheShellAnswers; this pins that the
// scan turns conclusive answers into a complete result.
func TestListAvailableProvidersCompleteWhenTheShellAnswers(t *testing.T) {
	t.Parallel()
	var regs []agent.Registration
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
	} {
		reg := testRegistration(provider)
		reg.Locator = agenttest.AnsweringLocator(launch.Spec{}, launch.Missing)
		regs = append(regs, reg)
	}
	r := agenttest.MustNewRegistry(regs...)

	providers, complete := r.ListAvailable(context.Background(), "/bin/sh", false)
	assert.Empty(t, providers)
	assert.True(t, complete, "every probe answered")
}
