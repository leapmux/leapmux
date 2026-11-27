package cmd

import (
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
	"github.com/leapmux/leapmux/internal/hub/captcha"
)

// workerIPCSock is any value: requireAdminClient refuses the transport on
// the presence of the variable, before it reads the address.
const workerIPCSock = "unix:/tmp/agent.sock"

// unreachableHub is a --hub value that no test connects to: every test that
// passes it fails or finishes before the dial.
const unreachableHub = "http://127.0.0.1:9"

// adminCase is one `admin ...` leaf, with arguments that pass its own
// validation. The transport is then the only thing left to refuse it.
type adminCase struct {
	path []string
	args []string
}

func (c adminCase) name() string { return strings.Join(c.path, " ") }

// adminCases lists every `admin ...` leaf.
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
		{[]string{"user", "create"}, []string{"amy", "--password", "hunter2"}},
		{[]string{"user", "update"}, []string{"id:u1", "--display-name", "Amy"}},
		{[]string{"user", "delete"}, []string{"id:u1"}},
		{[]string{"user", "grant-admin"}, []string{"id:u1"}},
		{[]string{"user", "revoke-admin"}, []string{"id:u1"}},
		{[]string{"user", "reset-password"}, []string{"id:u1", "--password", "hunter2"}},

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
		{[]string{"idp", "rotate-secret"}, []string{"p1", "--client-secret", "new-secret"}},

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

		{[]string{"network", "status"}, nil},
	}
}

// adminLeaves returns every runnable command under `admin`, keyed by its path below `admin`.
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

// TestAdminVerbs_AllRefuseInsideATab verifies every admin leaf refuses to run
// inside a LeapMux tab, even with an explicit --hub flag.
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

func TestAdminVerbs_RefuseAnEmptyArgumentBeforeTheDial(t *testing.T) {
	isolateCLIEnv(t)
	t.Setenv("LEAPMUX_CONTROL_SOCK", workerIPCSock)
	leaves := adminLeaves(t)

	for _, c := range adminCases() {
		leaf := leaves[c.name()]
		var required []cli.ArgSpec
		for _, spec := range cli.ArgsOf(leaf) {
			if spec.Required {
				required = append(required, spec)
			}
		}
		if len(required) == 0 {
			continue
		}
		for pos, spec := range required {
			blanks := []string{"", "   "}
			if spec.Name == "value" {
				// Whitespace-only <value> is stored as a JSON string; only "" is rejected.
				blanks = []string{""}
			}
			for _, blank := range blanks {
				t.Run(fmt.Sprintf("%s/arg%d/%q", c.name(), pos, blank), func(t *testing.T) {
					callArgs := append([]string{"admin"}, c.path...)
					posArgs := append([]string{}, c.args...)
					posArgs[pos] = blank
					callArgs = append(callArgs, posArgs...)
					f := runControl(t, append(callArgs, "--hub", unreachableHub)...).requireFailure(t, cli.CodeUsage)
					assert.NotContains(t, f.Message, "admin commands do not run inside a LeapMux tab",
						"empty positional argument %d (%s) must be rejected before requireAdminClient", pos, spec.Name)
				})
			}
		}
	}
}

func TestAdminCaptchaSet_HubAloneIsAnEmptyInvocation(t *testing.T) {
	isolateCLIEnv(t)
	f := runControl(t, "admin", "captcha", "set", "--hub", unreachableHub).requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "nothing to set")
}

func TestAdminPageFlags_ValidatesLimitBeforeDial(t *testing.T) {
	isolateCLIEnv(t)
	t.Setenv("LEAPMUX_CONTROL_SOCK", workerIPCSock)
	want := fmt.Sprintf("limit must be between %d and %d", minListLimit, contracts.MaxPageLimit)
	for _, limit := range []int64{0, -1, contracts.MaxPageLimit + 1} {
		f := runControl(t, "admin", "user", "list", "--limit", strconv.FormatInt(limit, 10)).requireFailure(t, cli.CodeUsage)
		assert.Equal(t, want, f.Message, "--limit %d", limit)
	}
}

func TestAnyLocalFlagChanged_IgnoresGlobalFlags(t *testing.T) {
	isolateCLIEnv(t)
	var changed bool
	tree := NewCommand()
	admin, _, err := tree.Find([]string{"admin"})
	require.NoError(t, err)
	admin.AddCommand(cli.NewLeaf(cli.Leaf{
		Use:   "probe",
		Short: "Probe local flag change detection",
		Flags: func(fs *pflag.FlagSet) { fs.String("name", "", "a local flag") },
		Run: func(inv *cli.Invocation) (any, error) {
			changed = anyLocalFlagChanged(inv)
			return nil, nil
		},
	}))

	o := runTree(t, tree, "", "admin", "probe", "--hub", "http://h", "--json")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	assert.False(t, changed, "--hub and --json are global flags")

	o = runTree(t, tree, "", "admin", "probe", "--name", "", "--json")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	assert.True(t, changed, "an explicit empty local flag counts as changed")
}

func TestCaptchaSettingKeys_CoverEveryCaptchaKey(t *testing.T) {
	keys := captchaSettingKeys()
	assert.Equal(t, []string{
		"captcha.enabled",
		"captcha.selected",
		"captcha.altcha",
		"captcha.recaptcha_v3",
		"captcha.turnstile",
	}, keys)

	for _, alias := range captcha.SupportedProviders() {
		provider, err := captcha.ParseProvider(alias)
		require.NoError(t, err)
		assert.Contains(t, keys, captcha.DescriptorFor(provider).Name(), "provider %s", alias)
	}
}
