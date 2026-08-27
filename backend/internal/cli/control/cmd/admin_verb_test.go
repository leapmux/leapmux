package cmd

import (
	"errors"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/captcha"
)

// workerIPCSock is any value: requireAdminClient refuses the transport on
// the presence of the variable, before it reads the address.
const workerIPCSock = "unix:/tmp/agent.sock"

// adminLeaf is one `control admin ...` command, with arguments that pass
// its own validation. The transport is then the only thing left to refuse
// it.
type adminLeaf struct {
	// fn is the exported entry point, so the coverage check below can
	// identify a verb that nobody added here.
	fn string
	// name is the command path, for the subtest name.
	name string
	run  func() error
}

// adminLeaves lists every `control admin ...` leaf. A verb that takes a
// trailing bool appears under both of its command names, because the two
// names are two leaves of the command tree.
func adminLeaves() []adminLeaf {
	return []adminLeaf{
		{"RunAdminSettingsList", "settings list", func() error { return RunAdminSettingsList(nil, nil) }},
		{"RunAdminSettingsGet", "settings get", func() error { return RunAdminSettingsGet(nil, []string{"theme"}) }},
		{"RunAdminSettingsSet", "settings set", func() error {
			return RunAdminSettingsSet(nil, []string{"theme", `"dark"`})
		}},
		{"RunAdminSettingsSetSecret", "settings set-secret", func() error {
			return RunAdminSettingsSetSecret(nil, []string{"smtp", `{"password":"x"}`})
		}},
		{"RunAdminSettingsReset", "settings reset", func() error { return RunAdminSettingsReset(nil, []string{"theme"}) }},

		{"RunAdminAppList", "app list", func() error { return RunAdminAppList(nil, nil) }},
		{"RunAdminAppRegister", "app register", func() error {
			return RunAdminAppRegister(nil, []string{"--name", "an app", "--scope", "workspace:read"})
		}},
		{"RunAdminAppUpdate", "app update", func() error {
			return RunAdminAppUpdate(nil, []string{"--client-id", "c1", "--name", "renamed"})
		}},
		{"RunAdminAppSetElevation", "app allow-elevation", func() error {
			return RunAdminAppSetElevation(nil, []string{"--client-id", "c1"}, true)
		}},
		{"RunAdminAppSetElevation", "app deny-elevation", func() error {
			return RunAdminAppSetElevation(nil, []string{"--client-id", "c1"}, false)
		}},
		{"RunAdminAppSetVerified", "app verify", func() error {
			return RunAdminAppSetVerified(nil, []string{"--client-id", "c1"}, true)
		}},
		{"RunAdminAppSetVerified", "app unverify", func() error {
			return RunAdminAppSetVerified(nil, []string{"--client-id", "c1"}, false)
		}},
		{"RunAdminAppRevoke", "app revoke", func() error { return RunAdminAppRevoke(nil, []string{"--client-id", "c1"}) }},
		{"RunAdminAppDelete", "app delete", func() error { return RunAdminAppDelete(nil, []string{"--client-id", "c1"}) }},

		{"RunAdminUserList", "user list", func() error { return RunAdminUserList(nil, nil) }},
		{"RunAdminUserGet", "user get", func() error { return RunAdminUserGet(nil, []string{"--username", "amy"}) }},
		{"RunAdminUserCreate", "user create", func() error {
			return RunAdminUserCreate(nil, []string{"--username", "amy", "--password", "hunter2"})
		}},
		{"RunAdminUserUpdate", "user update", func() error {
			return RunAdminUserUpdate(nil, []string{"--id", "u1", "--display-name", "Amy"})
		}},
		{"RunAdminUserDelete", "user delete", func() error { return RunAdminUserDelete(nil, []string{"--id", "u1"}) }},
		{"RunAdminUserSetAdmin", "user grant-admin", func() error {
			return RunAdminUserSetAdmin(nil, []string{"--id", "u1"}, true)
		}},
		{"RunAdminUserSetAdmin", "user revoke-admin", func() error {
			return RunAdminUserSetAdmin(nil, []string{"--id", "u1"}, false)
		}},
		{"RunAdminUserResetPassword", "user reset-password", func() error {
			// --password is supplied so the prompt never runs: the leaf must
			// reach the dial, and a prompt against a non-terminal stdin
			// would refuse it one step earlier.
			return RunAdminUserResetPassword(nil, []string{"--id", "u1", "--password", "hunter2"})
		}},
		{"RunAdminUserListSessions", "user list-sessions", func() error {
			return RunAdminUserListSessions(nil, []string{"--id", "u1"})
		}},

		{"RunAdminSessionList", "session list", func() error { return RunAdminSessionList(nil, nil) }},
		{"RunAdminSessionRevoke", "session revoke", func() error {
			return RunAdminSessionRevoke(nil, []string{"--id", "s1"})
		}},
		{"RunAdminSessionRevokeUser", "session revoke-user", func() error {
			return RunAdminSessionRevokeUser(nil, []string{"--user-id", "u1"})
		}},
		{"RunAdminSessionPurgeExpired", "session purge-expired", func() error {
			return RunAdminSessionPurgeExpired(nil, nil)
		}},

		{"RunAdminAPITokenList", "api-token list", func() error { return RunAdminAPITokenList(nil, nil) }},
		{"RunAdminAPITokenIssue", "api-token issue", func() error {
			return RunAdminAPITokenIssue(nil, []string{"--user-id", "u1", "--installation-name", "ci"})
		}},
		{"RunAdminAPITokenRevoke", "api-token revoke", func() error {
			return RunAdminAPITokenRevoke(nil, []string{"--id", "t1"})
		}},
		{"RunAdminDelegationTokenList", "delegation-token list", func() error {
			return RunAdminDelegationTokenList(nil, nil)
		}},
		{"RunAdminDelegationTokenRevoke", "delegation-token revoke", func() error {
			return RunAdminDelegationTokenRevoke(nil, []string{"--id", "t1"})
		}},

		{"RunAdminWorkerList", "worker list", func() error { return RunAdminWorkerList(nil, nil) }},
		{"RunAdminWorkerGet", "worker get", func() error { return RunAdminWorkerGet(nil, []string{"--id", "w1"}) }},
		{"RunAdminWorkerDeregister", "worker deregister", func() error {
			return RunAdminWorkerDeregister(nil, []string{"--id", "w1"})
		}},
		{"RunAdminWorkerRegKeyList", "worker reg-key list", func() error { return RunAdminWorkerRegKeyList(nil, nil) }},
		{"RunAdminWorkerRegKeyRevoke", "worker reg-key revoke", func() error {
			return RunAdminWorkerRegKeyRevoke(nil, []string{"--id", "k1"})
		}},
		{"RunAdminWorkerRegKeyPurgeExpired", "worker reg-key purge-expired", func() error {
			return RunAdminWorkerRegKeyPurgeExpired(nil, nil)
		}},

		{"RunAdminOAuthProviderAdd", "idp add", func() error {
			return RunAdminOAuthProviderAdd(nil, []string{"--type", "github"})
		}},
		{"RunAdminOAuthProviderList", "idp list", func() error { return RunAdminOAuthProviderList(nil, nil) }},
		{"RunAdminOAuthProviderRemove", "idp remove", func() error {
			return RunAdminOAuthProviderRemove(nil, []string{"--id", "p1"})
		}},
		{"RunAdminOAuthProviderSetEnabled", "idp enable", func() error {
			return RunAdminOAuthProviderSetEnabled(nil, []string{"--id", "p1"}, true)
		}},
		{"RunAdminOAuthProviderSetEnabled", "idp disable", func() error {
			return RunAdminOAuthProviderSetEnabled(nil, []string{"--id", "p1"}, false)
		}},

		{"RunAdminCaptchaShow", "captcha show", func() error { return RunAdminCaptchaShow(nil, nil) }},
		{"RunAdminCaptchaSet", "captcha set", func() error {
			return RunAdminCaptchaSet(nil, []string{"--provider", "altcha"})
		}},
		{"RunAdminCaptchaSetEnabled", "captcha enable", func() error { return RunAdminCaptchaSetEnabled(nil, nil, true) }},
		{"RunAdminCaptchaSetEnabled", "captcha disable", func() error {
			return RunAdminCaptchaSetEnabled(nil, nil, false)
		}},
		{"RunAdminCaptchaReset", "captcha reset", func() error { return RunAdminCaptchaReset(nil, nil) }},

		{"RunAdminRateLimitList", "rate-limit list", func() error { return RunAdminRateLimitList(nil, nil) }},
		{"RunAdminRateLimitSet", "rate-limit set", func() error {
			return RunAdminRateLimitSet(nil, []string{"--operation", "elevation", "--max-attempts", "5"})
		}},
		{"RunAdminRateLimitSetEnabled", "rate-limit enable", func() error {
			return RunAdminRateLimitSetEnabled(nil, []string{"--operation", "elevation"}, true)
		}},
		{"RunAdminRateLimitSetEnabled", "rate-limit disable", func() error {
			return RunAdminRateLimitSetEnabled(nil, []string{"--operation", "elevation"}, false)
		}},
		{"RunAdminRateLimitReset", "rate-limit reset", func() error {
			return RunAdminRateLimitReset(nil, []string{"--operation", "elevation"})
		}},
	}
}

// TestAdminVerbs_AllRefuseWorkerIPC is the tripwire for the rule that
// adminVerb exists to make structural: every leaf obtains its client
// through requireAdminClient, which keeps admin commands out of agent
// reach.
//
// Each leaf runs with arguments that pass its own validation, so the
// transport is the only thing left to refuse it. A verb that built its own
// client, or that called the RPC without one, would reach the network here
// and fail with something else.
func TestAdminVerbs_AllRefuseWorkerIPC(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_SOCK", workerIPCSock)

	for _, leaf := range adminLeaves() {
		t.Run(leaf.name, func(t *testing.T) {
			err := leaf.run()
			require.Error(t, err, "the verb must refuse the worker-IPC transport")
			assert.Contains(t, err.Error(), "LEAPMUX_CONTROL_SOCK",
				"the refusal must come from requireAdminClient, so the verb reached the dial and nothing else")
			assert.True(t, control.IsEmitted(err), "the refusal uses the JSON envelope")
		})
	}
}

// TestAdminVerbs_TableCoversEveryEntryPoint keeps the table honest. A new
// admin verb that nobody lists there would otherwise inherit the transport
// rule untested, which reads as coverage and is not.
func TestAdminVerbs_TableCoversEveryEntryPoint(t *testing.T) {
	covered := map[string]bool{}
	for _, leaf := range adminLeaves() {
		covered[leaf.fn] = true
	}

	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var declared []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, parseErr, "parse %s", name)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "RunAdmin") {
				continue
			}
			declared = append(declared, fn.Name.Name)
		}
	}

	require.NotEmpty(t, declared, "the source scan must find the admin entry points")
	for _, name := range declared {
		assert.True(t, covered[name],
			"%s is an admin verb with no entry in adminLeaves, so nothing checks that it refuses the worker-IPC transport", name)
	}
}

// A verb that carries only --hub asks for no change. The empty-invocation
// refusal must therefore ignore the framework flags: `captcha set --hub X`
// reported `updated: true` after writing nothing.
func TestAdminCaptchaSet_HubAloneIsAnEmptyInvocation(t *testing.T) {
	err := RunAdminCaptchaSet(nil, []string{"--hub", "http://127.0.0.1:9"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nothing to set")
}

func TestAdminVerb_BeforeDialRunsBeforeTheDialAndSeesFlagPresence(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_SOCK", workerIPCSock)

	var seen adminArgs
	var ran bool
	var name string
	err := adminVerb(nil, []string{"--name", "", "--hub", "http://h"}, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) { fs.StringVar(&name, "name", "default", "") },
		BeforeDial: func(a adminArgs) error {
			seen, ran = a, true
			return nil
		},
		Run: func(*control.Client, adminArgs) error {
			t.Fatal("Run must not reach the body when the dial refuses")
			return nil
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "LEAPMUX_CONTROL_SOCK")
	assert.True(t, ran, "BeforeDial runs even when the dial then refuses")
	assert.True(t, seen.Passed("name"), "an explicit empty value is a passed flag, not an absent one")
	assert.Empty(t, name, "the explicit empty value replaced the default")
	assert.True(t, seen.AnyPassed())
}

func TestAdminVerb_BeforeDialRefusalWinsOverTheDial(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_SOCK", workerIPCSock)

	err := adminVerb(nil, nil, adminVerbSpec{
		BeforeDial: func(adminArgs) error { return control.EmitError("invalid_request", "bad flag") },
		Run: func(*control.Client, adminArgs) error {
			t.Fatal("Run must not run after BeforeDial refuses")
			return nil
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad flag",
		"a bad flag must name the flag, not report a transport that the operator never chose")
	assert.NotContains(t, err.Error(), "LEAPMUX_CONTROL_SOCK")
}

func TestAdminArgs_AnyPassedIgnoresTheFrameworkFlags(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_SOCK", workerIPCSock)

	var seen adminArgs
	err := adminVerb(nil, []string{"--hub", "http://h"}, adminVerbSpec{
		Flags:      func(fs *flag.FlagSet) { fs.String("name", "", "") },
		BeforeDial: func(a adminArgs) error { seen = a; return nil },
		Run:        func(*control.Client, adminArgs) error { return nil },
	})

	require.Error(t, err)
	assert.False(t, seen.AnyPassed(), "--hub selects a hub; it asks for no change")
	assert.False(t, seen.Passed("name"))
	assert.True(t, seen.Passed("hub"), "Passed still reports the framework flag by name")
}

func TestAdminVerb_PositionalCountUsesTheDeclaredUsage(t *testing.T) {
	const usage = "usage: leapmux control admin thing set KEY VALUE"
	for _, args := range [][]string{nil, {"one"}, {"one", "two", "three"}} {
		var reached bool
		err := adminVerb(nil, args, adminVerbSpec{
			Positionals: 2,
			Usage:       usage,
			BeforeDial:  func(adminArgs) error { reached = true; return nil },
			Run:         func(*control.Client, adminArgs) error { return nil },
		})
		require.Error(t, err, "%v is not two positionals", args)
		assert.Contains(t, err.Error(), usage)
		assert.False(t, reached, "the count refusal comes before BeforeDial")
	}

	t.Setenv("LEAPMUX_CONTROL_SOCK", workerIPCSock)
	var reached bool
	var rest []string
	err := adminVerb(nil, []string{"one", "two"}, adminVerbSpec{
		Positionals: 2,
		Usage:       usage,
		BeforeDial:  func(a adminArgs) error { reached, rest = true, a.Rest; return nil },
		Run:         func(*control.Client, adminArgs) error { return nil },
	})
	require.Error(t, err, "the dial still refuses")
	assert.True(t, reached, "the exact count passes through to BeforeDial")
	assert.Equal(t, []string{"one", "two"}, rest)
}

// The other half of the transport rule: when the transport IS allowed, the
// body runs and receives a client. Without this, every wrapper test above
// would still pass if adminVerb never called Run at all.
func TestAdminVerb_RunReceivesTheClientWhenTheTransportIsAllowed(t *testing.T) {
	// An empty value is an absent worker socket, so the admin refusal does
	// not fire. The Run body below makes no call, so no hub is contacted.
	t.Setenv("LEAPMUX_CONTROL_SOCK", "")

	sentinel := errors.New("the body ran")
	var got *control.Client
	var seen adminArgs
	err := adminVerb(nil, []string{"--hub", "http://127.0.0.1:1", "--name", "x"}, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) { fs.String("name", "", "") },
		Run: func(c *control.Client, a adminArgs) error {
			got, seen = c, a
			return sentinel
		},
	})

	require.ErrorIs(t, err, sentinel, "adminVerb returns what the body returns, unchanged")
	require.NotNil(t, got, "the body receives the client that requireAdminClient built")
	assert.False(t, got.IsWorkerIPC(), "and never the worker-IPC transport")
	assert.True(t, seen.Passed("name"), "the parsed command line reaches the body too")
}

// requireFlag is one wording for every verb that needs an id. The table
// above proves each of them ACCEPTS the flag; this proves each refuses
// without it, which is the half that a factory reading a stale value at
// bind time would break.
func TestAdminVerbs_RefuseAMissingRequiredID(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_SOCK", workerIPCSock)

	verbs := map[string]func() error{
		"session revoke":          func() error { return RunAdminSessionRevoke(nil, nil) },
		"api-token revoke":        func() error { return RunAdminAPITokenRevoke(nil, nil) },
		"delegation-token revoke": func() error { return RunAdminDelegationTokenRevoke(nil, nil) },
		"worker get":              func() error { return RunAdminWorkerGet(nil, nil) },
		"worker deregister":       func() error { return RunAdminWorkerDeregister(nil, nil) },
		"worker reg-key revoke":   func() error { return RunAdminWorkerRegKeyRevoke(nil, nil) },
		"idp remove":              func() error { return RunAdminOAuthProviderRemove(nil, nil) },
		"idp enable":              func() error { return RunAdminOAuthProviderSetEnabled(nil, nil, true) },
		"idp disable":             func() error { return RunAdminOAuthProviderSetEnabled(nil, nil, false) },
	}
	for name, run := range verbs {
		t.Run(name, func(t *testing.T) {
			err := run()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--id is required")
			assert.NotContains(t, err.Error(), "LEAPMUX_CONTROL_SOCK",
				"the flag refusal must come before the dial")
		})
	}
}

// A spec that declares a count but forgets the message must still say
// something. An empty envelope message tells an operator nothing.
func TestAdminVerb_PositionalCountWithoutAUsageStillExplains(t *testing.T) {
	err := adminVerb(nil, nil, adminVerbSpec{
		Positionals: 2,
		Run:         func(*control.Client, adminArgs) error { return nil },
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "takes exactly 2 positional arguments")
}

// A verb with no declared positionals must refuse a trailing token rather
// than ignore it.
func TestAdminVerb_RefusesAnUnexpectedPositional(t *testing.T) {
	err := adminVerb(nil, []string{"stray"}, adminVerbSpec{
		Run: func(*control.Client, adminArgs) error {
			t.Fatal("Run must not run for an unparsable command line")
			return nil
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected argument")
}

func TestAdminVerb_PageChecksTheLimitBeforeBeforeDial(t *testing.T) {
	for _, limit := range []string{"0", "-1", "501"} {
		var page adminPageFlags
		var reached bool
		err := adminVerb(nil, []string{"--limit", limit}, adminVerbSpec{
			Page:       &page,
			BeforeDial: func(adminArgs) error { reached = true; return nil },
			Run:        func(*control.Client, adminArgs) error { return nil },
		})
		require.Error(t, err, "--limit %s must be refused", limit)
		assert.Contains(t, err.Error(), "limit must be between 1 and 500")
		assert.False(t, reached, "the limit check comes before BeforeDial, and long before the dial")
	}
}

func TestAdminVerb_PageBindsTheDefaultsAndTheCursor(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_SOCK", workerIPCSock)

	var page adminPageFlags
	err := adminVerb(nil, []string{"--cursor", "abc"}, adminVerbSpec{
		Page: &page,
		Run:  func(*control.Client, adminArgs) error { return nil },
	})
	require.Error(t, err, "the dial still refuses")
	assert.Equal(t, int64(50), page.Limit, "the default page size survives the wrapper")
	assert.Equal(t, "abc", page.Cursor)
}

// `captcha show` and `captcha reset` both address the whole captcha key
// set. Deriving it from the hub's own descriptor list is what keeps the
// two in step, so a key dropped from that list is a defect this catches.
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
