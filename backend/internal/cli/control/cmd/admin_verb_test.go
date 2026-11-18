package cmd

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/captcha"
)

// workerIPCSock is any value: requireAdminClient refuses the transport on
// the presence of the variable, before it reads the address.
const workerIPCSock = "unix:/tmp/agent.sock"

// unreachableHub is a --hub value that no test connects to: every test that
// passes it fails or finishes before the dial.
const unreachableHub = "http://127.0.0.1:9"

// adminCase is one `control admin ...` leaf, with arguments that pass its
// own validation. The transport is then the only thing left to refuse it.
type adminCase struct {
	path []string
	args []string
}

func (c adminCase) name() string { return strings.Join(c.path, " ") }

// adminCases lists every `control admin ...` leaf.
// TestAdminCases_CoverEveryLeaf keeps the list complete.
func adminCases() []adminCase {
	return []adminCase{
		{[]string{"settings", "list"}, nil},
		{[]string{"settings", "get"}, []string{"theme"}},
		{[]string{"settings", "set"}, []string{"theme", `"dark"`}},
		{[]string{"settings", "set-secret"}, []string{"smtp", `{"password":"x"}`}},
		{[]string{"settings", "reset"}, []string{"theme"}},

		{[]string{"user", "list"}, nil},
		{[]string{"user", "get"}, []string{"amy"}},
		// --password is given, so the prompt never runs: the leaf must reach
		// the dial, and a prompt against a stdin that is not a terminal
		// would refuse it one step earlier.
		{[]string{"user", "create"}, []string{"amy", "--password", "hunter2"}},
		{[]string{"user", "update"}, []string{"id:u1", "--display-name", "Amy"}},
		{[]string{"user", "delete"}, []string{"id:u1"}},
		{[]string{"user", "grant-admin"}, []string{"id:u1"}},
		{[]string{"user", "revoke-admin"}, []string{"id:u1"}},
		{[]string{"user", "reset-password"}, []string{"id:u1", "--password", "hunter2"}},
		{[]string{"user", "list-sessions"}, []string{"id:u1"}},

		{[]string{"session", "list"}, nil},
		{[]string{"session", "revoke"}, []string{"s1"}},
		{[]string{"session", "revoke-user"}, []string{"id:u1"}},
		{[]string{"session", "purge-expired"}, nil},

		{[]string{"worker", "list"}, nil},
		{[]string{"worker", "get"}, []string{"w1"}},
		{[]string{"worker", "deregister"}, []string{"w1"}},
		{[]string{"worker", "reg-key", "list"}, nil},
		{[]string{"worker", "reg-key", "revoke"}, []string{"k1"}},
		{[]string{"worker", "reg-key", "purge-expired"}, nil},

		{[]string{"app", "list"}, nil},
		{[]string{"app", "register"}, []string{"an app", "--scope", "workspace:read"}},
		{[]string{"app", "update"}, []string{"c1", "--name", "renamed"}},
		{[]string{"app", "verify"}, []string{"c1"}},
		{[]string{"app", "unverify"}, []string{"c1"}},
		{[]string{"app", "allow-elevation"}, []string{"c1"}},
		{[]string{"app", "deny-elevation"}, []string{"c1"}},
		{[]string{"app", "revoke"}, []string{"c1"}},
		{[]string{"app", "delete"}, []string{"c1"}},

		{[]string{"idp", "add"}, []string{"--type", "github"}},
		{[]string{"idp", "list"}, nil},
		{[]string{"idp", "remove"}, []string{"p1"}},
		{[]string{"idp", "enable"}, []string{"p1"}},
		{[]string{"idp", "disable"}, []string{"p1"}},

		{[]string{"captcha", "get"}, nil},
		{[]string{"captcha", "set"}, []string{"--provider", "altcha"}},
		{[]string{"captcha", "enable"}, nil},
		{[]string{"captcha", "disable"}, nil},
		{[]string{"captcha", "reset"}, nil},

		{[]string{"rate-limit", "list"}, nil},
		{[]string{"rate-limit", "set"}, []string{"elevation", "--max-attempts", "5"}},
		{[]string{"rate-limit", "enable"}, []string{"elevation"}},
		{[]string{"rate-limit", "disable"}, []string{"elevation"}},
		{[]string{"rate-limit", "reset"}, []string{"elevation"}},

		{[]string{"api-token", "list"}, nil},
		{[]string{"api-token", "issue"}, []string{"id:u1", "--installation-name", "ci"}},
		{[]string{"api-token", "revoke"}, []string{"t1"}},
		{[]string{"delegation-token", "list"}, nil},
		{[]string{"delegation-token", "revoke"}, []string{"t1"}},
	}
}

// adminLeaves returns every runnable command under `control admin`, keyed by
// its path below `admin`.
func adminLeaves(t *testing.T) map[string]*cobra.Command {
	t.Helper()
	admin, _, err := NewCommand().Find([]string{"admin"})
	require.NoError(t, err)
	leaves := map[string]*cobra.Command{}
	var visit func(cmd *cobra.Command, path []string)
	visit = func(cmd *cobra.Command, path []string) {
		if cmd.Runnable() {
			leaves[strings.Join(path, " ")] = cmd
		}
		for _, child := range cmd.Commands() {
			visit(child, append(path[:len(path):len(path)], child.Name()))
		}
	}
	visit(admin, nil)
	require.NotEmpty(t, leaves)
	return leaves
}

// TestAdminVerbs_AllRefuseInsideATab is the tripwire for the rule that
// adminLeaf exists to make structural: every leaf obtains its client through
// requireAdminClient, which keeps admin commands out of agent reach.
//
// Each leaf runs with arguments that pass its own validation, so the
// transport is the only thing left to refuse it. A verb that built its own
// client would reach the network here and fail with something else. Each
// run passes --hub, because that flag would carry the user's stored
// credential and must not open a way around the refusal.
func TestAdminVerbs_AllRefuseInsideATab(t *testing.T) {
	isolateCLIEnv(t)
	t.Setenv("LEAPMUX_CONTROL_SOCK", workerIPCSock)

	for _, c := range adminCases() {
		t.Run(c.name(), func(t *testing.T) {
			args := append(append([]string{"admin"}, c.path...), c.args...)
			f := runControl(t, append(args, "--hub", unreachableHub)...).requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, "admin commands do not run inside a LeapMux tab",
				"the refusal must come from requireAdminClient, so the verb reached the dial and nothing else")
		})
	}
}

// TestAdminCases_CoverEveryLeaf keeps the table honest. An admin leaf that
// nobody lists there would otherwise inherit the transport rule untested,
// which reads as coverage and is not. An entry for a leaf that no longer
// exists fails too.
func TestAdminCases_CoverEveryLeaf(t *testing.T) {
	leaves := adminLeaves(t)
	listed := map[string]bool{}
	for _, c := range adminCases() {
		listed[c.name()] = true
		assert.Contains(t, leaves, c.name(), "adminCases lists a leaf that the tree does not have")
	}
	for path := range leaves {
		assert.True(t, listed[path],
			"admin %s has no entry in adminCases, so nothing checks that it refuses to run inside a tab", path)
	}
}

// Every leaf that declares a required argument refuses a missing one with a
// usage error, before any check of the transport.
func TestAdminVerbs_RefuseAMissingArgumentBeforeTheDial(t *testing.T) {
	isolateCLIEnv(t)
	t.Setenv("LEAPMUX_CONTROL_SOCK", workerIPCSock)

	for path, leaf := range adminLeaves(t) {
		var required []string
		for _, spec := range cli.ArgsOf(leaf) {
			if spec.Required {
				required = append(required, spec.Name)
			}
		}
		if len(required) == 0 {
			continue
		}
		t.Run(path, func(t *testing.T) {
			args := append([]string{"admin"}, strings.Fields(path)...)
			f := runControl(t, args...).requireFailure(t, cli.CodeUsage)
			assert.Equal(t, "missing argument <"+required[0]+">", f.Message)
		})
	}
}

// A verb that carries only --hub asks for no change. The empty-invocation
// refusal must therefore ignore the global flags: `captcha set --hub X`
// reported `updated: true` after writing nothing.
func TestAdminCaptchaSet_HubAloneIsAnEmptyInvocation(t *testing.T) {
	isolateCLIEnv(t)
	f := runControl(t, "admin", "captcha", "set", "--hub", unreachableHub).requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "nothing to set")
}

// runAdminProbe adds a leaf built from spec to `control admin` and runs it
// as `leapmux control admin probe <args...> --json`.
func runAdminProbe(t *testing.T, spec adminVerbSpec, args ...string) cliOutcome {
	t.Helper()
	if spec.Use == "" {
		spec.Use = "probe"
	}
	spec.Short = "a probe leaf"
	if spec.Run == nil {
		spec.Run = func(*control.Client, adminArgs) error { return nil }
	}
	tree := NewCommand()
	admin, _, err := tree.Find([]string{"admin"})
	require.NoError(t, err)
	admin.AddCommand(adminLeaf(spec))
	return runTree(t, tree, "", append(append([]string{"control", "admin", "probe"}, args...), "--json")...)
}

func TestAdminLeaf_BeforeDialRunsBeforeTheDialAndSeesFlagPresence(t *testing.T) {
	isolateCLIEnv(t)
	t.Setenv("LEAPMUX_CONTROL_SOCK", workerIPCSock)

	var seen adminArgs
	var ran bool
	var name string
	out := runAdminProbe(t, adminVerbSpec{
		Flags: func(fs *pflag.FlagSet) { fs.StringVar(&name, "name", "default", "") },
		BeforeDial: func(a adminArgs) error {
			seen, ran = a, true
			return nil
		},
		Run: func(*control.Client, adminArgs) error {
			t.Fatal("Run must not reach the body when the dial refuses")
			return nil
		},
	}, "--name", "", "--hub", "http://h")

	f := out.requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "inside a LeapMux tab")
	assert.True(t, ran, "BeforeDial runs even when the dial then refuses")
	assert.True(t, seen.Passed("name"), "an explicit empty value is a passed flag, not an absent one")
	assert.Empty(t, name, "the explicit empty value replaced the default")
	assert.True(t, seen.AnyPassed())
}

func TestAdminLeaf_BeforeDialRefusalWinsOverTheDial(t *testing.T) {
	isolateCLIEnv(t)
	t.Setenv("LEAPMUX_CONTROL_SOCK", workerIPCSock)

	out := runAdminProbe(t, adminVerbSpec{
		BeforeDial: func(adminArgs) error { return cli.Usagef("bad flag") },
		Run: func(*control.Client, adminArgs) error {
			t.Fatal("Run must not run after BeforeDial refuses")
			return nil
		},
	})

	f := out.requireFailure(t, cli.CodeUsage)
	assert.Equal(t, "bad flag", f.Message,
		"a bad flag must identify the flag, not report a transport that the operator never chose")
}

func TestAdminArgs_AnyPassedIgnoresTheGlobalFlags(t *testing.T) {
	isolateCLIEnv(t)
	t.Setenv("LEAPMUX_CONTROL_SOCK", workerIPCSock)

	var seen adminArgs
	runAdminProbe(t, adminVerbSpec{
		Flags:      func(fs *pflag.FlagSet) { fs.String("name", "", "") },
		BeforeDial: func(a adminArgs) error { seen = a; return nil },
	}, "--hub", "http://h").requireFailure(t, cli.CodeUsage)

	assert.False(t, seen.AnyPassed(), "--hub selects a hub and --json a format; neither asks for a change")
	assert.False(t, seen.Passed("name"))
	assert.True(t, seen.Passed("hub"), "Passed still reports a global flag by name")
	assert.True(t, seen.Passed("json"))
}

func TestAdminLeaf_TheUseLineSetsTheArgumentCount(t *testing.T) {
	isolateCLIEnv(t)
	for _, tc := range []struct {
		args    []string
		message string
	}{
		{nil, "missing argument <one>"},
		{[]string{"a"}, "missing argument <two>"},
		{[]string{"a", "b", "c"}, `unexpected argument "c"`},
	} {
		var reached bool
		f := runAdminProbe(t, adminVerbSpec{
			Use:        "probe <one> <two>",
			BeforeDial: func(adminArgs) error { reached = true; return nil },
		}, tc.args...).requireFailure(t, cli.CodeUsage)
		assert.Equal(t, tc.message, f.Message, "args %v", tc.args)
		assert.False(t, reached, "the count refusal comes before BeforeDial")
	}

	t.Setenv("LEAPMUX_CONTROL_SOCK", workerIPCSock)
	var rest []string
	var one, two string
	runAdminProbe(t, adminVerbSpec{
		Use: "probe <one> <two>",
		BeforeDial: func(a adminArgs) error {
			rest, one, two = a.Rest, a.Arg("one"), a.Arg("two")
			return nil
		},
	}, "a", "b").requireFailure(t, cli.CodeUsage)
	assert.Equal(t, []string{"a", "b"}, rest, "the exact count passes through to BeforeDial")
	assert.Equal(t, "a", one)
	assert.Equal(t, "b", two)
}

// The other half of the transport rule: when the transport IS allowed, the
// body runs and receives a client. Without this, every wrapper test above
// would still pass if adminLeaf never called Run at all.
func TestAdminLeaf_RunReceivesTheClientWhenTheTransportIsAllowed(t *testing.T) {
	// The Run body below makes no call, so no hub is contacted.
	isolateCLIEnv(t)

	var got *control.Client
	var seen adminArgs
	f := runAdminProbe(t, adminVerbSpec{
		Flags: func(fs *pflag.FlagSet) { fs.String("name", "", "") },
		Run: func(c *control.Client, a adminArgs) error {
			got, seen = c, a
			return errors.New("the body ran")
		},
	}, "--hub", "http://127.0.0.1:1", "--name", "x").requireFailure(t, cli.CodeInternal)

	assert.Equal(t, "the body ran", f.Message, "the leaf reports what the body returns")
	require.NotNil(t, got, "the body receives the client that requireAdminClient built")
	assert.False(t, got.IsWorkerIPC(), "and never the worker-IPC transport")
	assert.True(t, seen.Passed("name"), "the parsed command line reaches the body too")
}

func TestAdminLeaf_PageChecksTheLimitBeforeBeforeDial(t *testing.T) {
	isolateCLIEnv(t)
	want := fmt.Sprintf("limit must be between %d and %d", minListLimit, contracts.MaxPageLimit)
	for _, limit := range []int64{0, -1, contracts.MaxPageLimit + 1} {
		var page adminPageFlags
		var reached bool
		f := runAdminProbe(t, adminVerbSpec{
			Page:       &page,
			BeforeDial: func(adminArgs) error { reached = true; return nil },
		}, "--limit", strconv.FormatInt(limit, 10)).requireFailure(t, cli.CodeUsage)
		assert.Equal(t, want, f.Message, "--limit %d", limit)
		assert.False(t, reached, "the limit check comes before BeforeDial, and long before the dial")
	}
}

func TestAdminLeaf_PageBindsTheDefaultsAndTheCursor(t *testing.T) {
	isolateCLIEnv(t)
	t.Setenv("LEAPMUX_CONTROL_SOCK", workerIPCSock)

	var page adminPageFlags
	runAdminProbe(t, adminVerbSpec{Page: &page}, "--cursor", "abc").requireFailure(t, cli.CodeUsage)
	assert.Equal(t, int64(contracts.DefaultPageLimit), page.Limit, "the default page size survives the wrapper")
	assert.Equal(t, "abc", page.Cursor)
}

// `captcha get` and `captcha reset` both address the whole captcha key set.
// Deriving it from the hub's own descriptor list is what keeps the two in
// step, so a key dropped from that list is a defect this catches.
func TestCaptchaSettingKeys_CoverEveryCaptchaKey(t *testing.T) {
	keys := captchaSettingKeys()
	assert.Equal(t, []string{
		"captcha.enabled",
		"captcha.selected",
		"captcha.altcha",
		"captcha.recaptcha_v3",
		"captcha.turnstile",
	}, keys)

	// Every provider's row must be addressable, so that `captcha reset
	// --provider X` never asks the hub for a key that no verb lists.
	for _, alias := range captcha.SupportedProviders() {
		provider, err := captcha.ParseProvider(alias)
		require.NoError(t, err)
		assert.Contains(t, keys, captcha.DescriptorFor(provider).Name(), "provider %s", alias)
	}
}
