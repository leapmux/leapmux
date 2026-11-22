package zcode

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

const zcodePersonalSessionFixture = `{"session":{"sessionId":"native-personal","target":null},"settings":{"model":{"current":{"providerId":"personal:work","modelId":"m"},"available":[{"ref":{"providerId":"personal:work","modelId":"m"},"label":"M","providerLabel":"Personal","properties":{"inputFormat":{"supportsText":true}}}]}},"runtime":{"eventSeq":0,"stateRevision":1}}`

// zcodeRevokedModelSessionFixture reports a session whose current model is gone
// from the live catalog: one live model remains, and it declares its reasoning
// default.
const zcodeRevokedModelSessionFixture = `{"session":{"sessionId":"native-personal","target":null},"settings":{"model":{"current":{"providerId":"personal:work","modelId":"revoked"},"available":[{"ref":{"providerId":"personal:work","modelId":"m"},"label":"M","providerLabel":"Personal","reasoning":{"levels":[{"value":"low"},{"value":"high"}],"defaultLevel":"high"},"properties":{"inputFormat":{"supportsText":true}}}]}},"runtime":{"eventSeq":0,"stateRevision":1}}`

// zcodeEmptyCatalogSessionFixture reports an explicitly empty live catalog
// beside a current model it does not hold.
const zcodeEmptyCatalogSessionFixture = `{"session":{"sessionId":"native-personal","target":null},"settings":{"model":{"current":{"providerId":"personal:work","modelId":"revoked"},"available":[]}},"runtime":{"eventSeq":0,"stateRevision":1}}`

// zcodeSetModelEchoFixture reports the selection that a session/setModel request
// carried, the way the app-server reports what it settled on.
func zcodeSetModelEchoFixture(params json.RawMessage) string {
	var request struct {
		Model struct {
			ProviderID string `json:"providerId"`
			ModelID    string `json:"modelId"`
			Options    struct {
				ReasoningLevel string `json:"reasoningLevel"`
			} `json:"options"`
		} `json:"model"`
	}
	_ = json.Unmarshal(params, &request)
	return `{"session":{"sessionId":"native-personal","target":null},"settings":{"model":{"current":{"providerId":"` +
		request.Model.ProviderID + `","modelId":"` + request.Model.ModelID +
		`"}},"thoughtLevel":{"enabled":true,"current":"` + request.Model.Options.ReasoningLevel +
		`","available":[{"value":"low"},{"value":"high"}]}}}`
}

// TestZCodeStartupProcess is the subprocess fixture for real Start handshakes.
func TestZCodeStartupProcess(t *testing.T) {
	if os.Getenv("LEAPMUX_ZCODE_STARTUP_FIXTURE") != "1" {
		return
	}
	encoder := json.NewEncoder(os.Stdout)
	decoder := json.NewDecoder(os.Stdin)
	if slices.Contains(os.Args, "--prepare-storage") {
		if os.Getenv("LEAPMUX_ZCODE_FIXTURE_STORAGE_FAILURE") == "1" {
			os.Exit(47)
		}
		_ = encoder.Encode(map[string]any{"method": zcodeStoragePathMethod, "params": map[string]string{"path": os.Getenv("LEAPMUX_ZCODE_FIXTURE_DATABASE")}})
		var acknowledgement any
		if decoder.Decode(&acknowledgement) != nil {
			os.Exit(2)
		}
		_ = encoder.Encode(map[string]any{"method": zcodeStoragePreparedMethod, "params": map[string]any{}})
		os.Exit(0)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		if file, err := os.OpenFile(os.Getenv("LEAPMUX_ZCODE_FIXTURE_CALLS"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			_, _ = file.WriteString(request.Method + "\n")
			_ = file.Close()
		}
		reply := map[string]any{"id": request.ID}
		switch request.Method {
		case MethodUpdateAccountConfig:
			var params struct {
				Revision string `json:"revision"`
			}
			if json.Unmarshal(request.Params, &params) != nil {
				os.Exit(2)
			}
			reply["result"] = map[string]any{"receivedRevision": params.Revision, "status": "received"}
		case MethodProviderRefreshRegistry:
			if os.Getenv("LEAPMUX_ZCODE_FIXTURE_BAD_REGISTRY_REFRESH") == "1" {
				reply["result"] = map[string]any{}
			} else {
				reply["result"] = map[string]any{"refreshed": true}
			}
		case MethodSessionCreate:
			switch {
			case os.Getenv("LEAPMUX_ZCODE_FIXTURE_INVALID_PERSONAL") == "1":
				reply["error"] = map[string]any{"code": -32602, "message": "native provider configuration has no executable model"}
			case os.Getenv("LEAPMUX_ZCODE_FIXTURE_REVOKED_MODEL") == "1":
				reply["result"] = json.RawMessage(zcodeRevokedModelSessionFixture)
			case os.Getenv("LEAPMUX_ZCODE_FIXTURE_EMPTY_CATALOG") == "1":
				reply["result"] = json.RawMessage(zcodeEmptyCatalogSessionFixture)
			default:
				reply["result"] = json.RawMessage(zcodePersonalSessionFixture)
			}
		case MethodSetModel:
			reply["result"] = json.RawMessage(zcodeSetModelEchoFixture(request.Params))
		case MethodSessionSubscribe:
			if os.Getenv("LEAPMUX_ZCODE_FIXTURE_BAD_SUBSCRIBE") == "1" {
				reply["result"] = map[string]any{}
			} else {
				var params struct {
					SessionID string `json:"sessionId"`
				}
				_ = json.Unmarshal(request.Params, &params)
				reply["result"] = map[string]any{"sessionId": params.SessionID, "eventSeq": 0, "events": []any{}}
			}
		default:
			reply["result"] = map[string]any{}
		}
		if encoder.Encode(reply) != nil {
			os.Exit(2)
		}
	}
	os.Exit(0)
}

func zcodeStartupFixture(t *testing.T, desktopConfig string) (agent.Options, string) {
	t.Helper()
	shell, err := exec.LookPath("sh")
	require.NoError(t, err)
	executable, err := os.Executable()
	require.NoError(t, err)
	home, working := t.TempDir(), t.TempDir()
	configDir := filepath.Join(home, ".zcode", "v2")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	if desktopConfig != "" {
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.json"), []byte(desktopConfig), 0o600))
	}
	// The native personal file is independent of the legacy registry and OAuth store.
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "provider_config.json"), []byte(`{"schemaVersion":1,"config":{"providerConfigRules":{"providerRules":[{"providerId":"personal:work","enabled":true,"config":{"access":{"type":"api-key","apiKey":"fixture-key"},"api":{"type":"openai-chat-completions","baseUrl":"https://model.invalid/v1"},"personalModelIds":["m"]}}]}}}`), 0o600))
	builtin := filepath.Join(t.TempDir(), "builtin.json")
	require.NoError(t, os.WriteFile(builtin, []byte(zcodeBuiltinZAIAccountConfig), 0o600))
	calls := filepath.Join(t.TempDir(), "calls.log")
	previous := zcodeLocator
	zcodeLocator = launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
		return launch.Spec{
			Program: executable, PrefixArgs: []string{"-test.run=^TestZCodeStartupProcess$", "--"},
			Env: []string{"LEAPMUX_ZCODE_STARTUP_FIXTURE=1",
				"LEAPMUX_ZCODE_FIXTURE_DATABASE=" + filepath.Join(home, "native.db"), "LEAPMUX_ZCODE_FIXTURE_CALLS=" + calls,
				zcodeBuiltinProviderConfigEnv + "=" + builtin},
		}, launch.Found
	})
	t.Cleanup(func() { zcodeLocator = previous })
	return agent.Options{AgentID: "personal-only", HomeDir: home, WorkingDir: working, Shell: shell,
		StartupTimeout: 30 * time.Second, APITimeout: 30 * time.Second,
		ExtraEnv: []string{"HOME=" + home, "USERPROFILE=" + home}}, calls
}

func TestZCodeStart_UsesNativePersonalProvidersWithoutALegacyRegistry(t *testing.T) {
	// The fixtures replace the registered locator, so they run serially.
	for _, legacyConfig := range []string{"", `{"provider":{}}`} {
		t.Run(legacyConfig, func(t *testing.T) {
			opts, _ := zcodeStartupFixture(t, legacyConfig)
			started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
			require.NoError(t, err)
			t.Cleanup(func() { started.Stop(); _ = started.Wait() })
			native := started.(*Agent)
			native.Mu.Lock()
			defer native.Mu.Unlock()
			assert.Equal(t, "native-personal", native.sessionID)
			assert.Equal(t, "personal:work/m", native.model)
		})
	}
}

func TestZCodeStart_ReportsNativePersonalConfigurationFailure(t *testing.T) {
	opts, _ := zcodeStartupFixture(t, "")
	opts.ExtraEnv = append(opts.ExtraEnv, "LEAPMUX_ZCODE_FIXTURE_INVALID_PERSONAL=1")
	started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
	require.ErrorContains(t, err, "native provider configuration has no executable model")
	assert.Nil(t, started)
}

// A subscribe reply that proves nothing fails the whole start: the agent must
// not run without a live event stream.
func TestZCodeStart_ReportsAnInvalidSubscribeReceipt(t *testing.T) {
	opts, _ := zcodeStartupFixture(t, "")
	opts.ExtraEnv = append(opts.ExtraEnv, "LEAPMUX_ZCODE_FIXTURE_BAD_SUBSCRIBE=1")
	started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
	require.ErrorContains(t, err, MethodSessionSubscribe)
	assert.Nil(t, started)
}

func TestZCodeStart_RejectsMalformedLegacyConfigurationBeforeTheServer(t *testing.T) {
	opts, calls := zcodeStartupFixture(t, "{")
	started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
	require.Error(t, err)
	assert.Nil(t, started)
	assert.Contains(t, err.Error(), "parse")
	data, _ := os.ReadFile(calls)
	assert.Empty(t, strings.TrimSpace(string(data)))
}

// zcodeStartupCalls returns the trimmed call log of the startup fixture.
func zcodeStartupCalls(t *testing.T, calls string) string {
	t.Helper()
	data, err := os.ReadFile(calls)
	require.NoError(t, err)
	return strings.TrimSpace(string(data))
}

// The registry refresh sits between the initial account snapshot and the startup
// catalog read, and its acknowledgement is required.
func TestZCodeStart_RequiresARefreshedRegistryAcknowledgement(t *testing.T) {
	opts, calls := zcodeStartupFixture(t, "")
	opts.ExtraEnv = append(opts.ExtraEnv, "LEAPMUX_ZCODE_FIXTURE_BAD_REGISTRY_REFRESH=1")
	started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
	require.Error(t, err)
	assert.Nil(t, started)
	assert.Equal(t, strings.Join([]string{
		MethodUpdateAccountConfig, MethodProviderRefreshRegistry,
	}, "\n"), zcodeStartupCalls(t, calls),
		"a refused registry refresh stops the start before the catalog read")
}

// A complete catalog controls choices without changing the observed current model.
func TestZCodeCatalog_RetainsAnUnavailableCurrentModelAtStartup(t *testing.T) {
	opts, calls := zcodeStartupFixture(t, "")
	opts.ExtraEnv = append(opts.ExtraEnv, "LEAPMUX_ZCODE_FIXTURE_REVOKED_MODEL=1")
	started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)
	t.Cleanup(func() { started.Stop(); _ = started.Wait() })
	native := started.(*Agent)
	assert.Equal(t, "personal:work/revoked", native.model)
	snapshot := native.SettingsSnapshot()
	assert.Equal(t, agent.OptionSettlementUnresolved, snapshot.Settlements[agent.OptionIDModel].State)
	assert.Equal(t, strings.Join([]string{
		MethodUpdateAccountConfig, MethodProviderRefreshRegistry,
		MethodSessionCreate, MethodSetMode, MethodSessionSubscribe,
	}, "\n"), zcodeStartupCalls(t, calls), "startup must not choose a different model")
}

// A persisted selection that the live catalog still holds stays: the repair adds
// no setter call of its own.
func TestZCodeModelRepair_RetainsAValidPersistedSelection(t *testing.T) {
	opts, calls := zcodeStartupFixture(t, "")
	started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)
	t.Cleanup(func() { started.Stop(); _ = started.Wait() })
	native := started.(*Agent)
	native.Mu.Lock()
	defer native.Mu.Unlock()
	assert.Equal(t, "personal:work/m", native.model)
	assert.Equal(t, strings.Join([]string{
		MethodUpdateAccountConfig, MethodProviderRefreshRegistry,
		MethodSessionCreate, MethodSetModel, MethodSetMode, MethodSessionSubscribe,
	}, "\n"), zcodeStartupCalls(t, calls),
		"the normal reassertion is the only setter call")
}

// An explicitly empty live catalog is the authoritative answer: no setter call,
// and the model is unresolved.
func TestZCodeModelRepair_AnExplicitlyEmptyCatalogMarksTheModelUnresolved(t *testing.T) {
	opts, calls := zcodeStartupFixture(t, "")
	opts.ExtraEnv = append(opts.ExtraEnv, "LEAPMUX_ZCODE_FIXTURE_EMPTY_CATALOG=1")
	started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)
	t.Cleanup(func() { started.Stop(); _ = started.Wait() })
	native := started.(*Agent)
	native.Mu.Lock()
	defer native.Mu.Unlock()
	assert.Equal(t, "personal:work/revoked", native.model, "an empty catalog manufactures no model")
	assert.True(t, native.zcodeCurrentModelUnavailableLocked(), "the current model remains unavailable")
	assert.Equal(t, strings.Join([]string{
		MethodUpdateAccountConfig, MethodProviderRefreshRegistry,
		MethodSessionCreate, MethodSetMode, MethodSessionSubscribe,
	}, "\n"), zcodeStartupCalls(t, calls),
		"an explicitly empty catalog makes no setter call")
}

// zcodeBundleStartupFixture installs a private bundle with a real Node capability
// probe and the startup test peer behind its app-server script.
func zcodeBundleStartupFixture(t *testing.T) (agent.Options, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX bundled runtime fixture")
	}
	bash, err := exec.LookPath("bash")
	require.NoError(t, err)
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	opts, calls := zcodeStartupFixture(t, "")
	fixture, err := zcodeLocator.Resolve(t.Context(), opts.Shell, false, "ZCode startup peer")
	require.NoError(t, err)
	zcodeLocator = launch.Custom(resolveZCodeLaunch)
	opts.Shell = bash
	opts.ExtraEnv = append(opts.ExtraEnv, fixture.Env...)

	t.Setenv("HOME", opts.HomeDir)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("BASH_ENV", "")
	t.Setenv("NODE_OPTIONS", "")
	t.Setenv("NODE_PATH", "")
	t.Setenv(zcodeScriptEnvOverride, "")
	t.Setenv(zcodeNodeEnvOverride, "")
	var script, runtimePath string
	if runtime.GOOS == "darwin" {
		contents := filepath.Join(opts.HomeDir, "Applications", "ZCode.app", "Contents")
		script = filepath.Join(contents, "Resources", "glm", "zcode.cjs")
		runtimePath = filepath.Join(contents, "MacOS", "ZCode")
	} else {
		install := filepath.Join(opts.HomeDir, ".local", "share", "ZCode")
		script = filepath.Join(install, "resources", "glm", "zcode.cjs")
		runtimePath = filepath.Join(install, "zcode")
	}
	witness := filepath.Join(t.TempDir(), "bundle.log")
	t.Setenv("LEAPMUX_ZCODE_BUNDLE_WITNESS", witness)
	t.Setenv("LEAPMUX_ZCODE_BUNDLE_PEER", fixture.Program)
	require.NoError(t, os.MkdirAll(filepath.Dir(script), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Dir(runtimePath), 0o700))
	require.NoError(t, os.WriteFile(script, []byte(`const {appendFileSync} = require('node:fs');
const {spawnSync} = require('node:child_process');
const args = process.argv.slice(2);
if (args[0] !== 'app-server' || args[1] !== '--stdio') process.exit(34);
appendFileSync(process.env.LEAPMUX_ZCODE_BUNDLE_WITNESS,
  args.includes('--prepare-storage') ? 'storage\n' : 'main\n');
const peer = spawnSync(process.env.LEAPMUX_ZCODE_BUNDLE_PEER,
  ['-test.run=^TestZCodeStartupProcess$', '--', ...args], {stdio: 'inherit'});
process.exit(peer.status ?? 35);
`), 0o600))
	program := shellquote.POSIX.Quote(node)
	testutil.WriteExecutable(t, runtimePath, `#!/bin/sh
[ "$ELECTRON_RUN_AS_NODE" = 1 ] || exit 31
if [ "$1" = -e ]; then
    [ "$#" -eq 2 ] && [ "$2" = "require('node:sqlite')" ] || exit 32
    `+program+` "$@" || exit $?
    printf 'probe\n' >> "$LEAPMUX_ZCODE_BUNDLE_WITNESS"
    exit $?
fi
[ "$1" = `+shellquote.POSIX.Quote(script)+` ] || exit 33
exec `+program+` "$@"
`)
	return opts, calls, witness
}

func TestZCodeStartDiscoversAndRunsTheBundledRuntime(t *testing.T) {
	opts, calls, witness := zcodeBundleStartupFixture(t)
	profile := filepath.Join(t.TempDir(), "profile")
	require.NoError(t, os.WriteFile(profile, []byte("unset ELECTRON_RUN_AS_NODE\n"), 0o600))
	t.Setenv("BASH_ENV", profile)
	started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)
	t.Cleanup(func() { started.Stop(); _ = started.Wait() })

	assert.Equal(t, "probe\nstorage\nmain", zcodeStartupCalls(t, witness))
	assert.Contains(t, zcodeStartupCalls(t, calls), MethodSessionSubscribe)
}

func TestZCodeStartDoesNotTryTheBundleAfterTheSelectedProfileCommandFails(t *testing.T) {
	opts, _, bundleWitness := zcodeBundleStartupFixture(t)
	started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)
	started.Stop()
	require.NoError(t, started.Wait())
	require.Equal(t, "probe\nstorage\nmain", zcodeStartupCalls(t, bundleWitness))
	require.NoError(t, os.Remove(bundleWitness))

	profile := filepath.Join(t.TempDir(), "profile")
	wrapperWitness := filepath.Join(t.TempDir(), "wrapper.log")
	t.Setenv("BASH_ENV", profile)
	t.Setenv("LEAPMUX_ZCODE_WRAPPER_WITNESS", wrapperWitness)
	require.NoError(t, os.WriteFile(profile, []byte(`zcode() {
    if [ "${3-}" = --prepare-storage ]; then
        "$LEAPMUX_ZCODE_BUNDLE_PEER" '-test.run=^TestZCodeStartupProcess$' -- "$@" || return $?
        printf 'storage\n' >> "$LEAPMUX_ZCODE_WRAPPER_WITNESS"
        return
    fi
    printf 'main\n' >> "$LEAPMUX_ZCODE_WRAPPER_WITNESS"
    status=37
    printf 'selected ZCode profile command failed with status %s\n' "$status" >&2
    return "$status"
}
`), 0o600))

	started, err = Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
	if started != nil {
		t.Cleanup(func() { started.Stop(); _ = started.Wait() })
	}
	require.Nil(t, started)
	require.ErrorContains(t, err, "selected ZCode profile command failed with status 37")
	assert.NotErrorIs(t, err, launch.ErrNoCandidate)
	assert.Equal(t, "storage\nmain", zcodeStartupCalls(t, wrapperWitness))
	assert.NoFileExists(t, bundleWitness, "the proven bundle must not be probed or invoked")
}

func TestZCodeNushellLaunchFilesFollowTheProcessOwnership(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses an isolated Unix Nushell configuration")
	}
	shell, err := exec.LookPath("nu")
	if err != nil {
		t.Skip("Nushell is unavailable")
	}
	for _, test := range []struct {
		name, catalog, wantError       string
		profileFailure, storageFailure bool
	}{
		{name: "account probe rolls back the main files", wantError: "shell did not report its account environment", profileFailure: true},
		{name: "malformed catalog rolls back the main files", catalog: "{", wantError: "parse"},
		{name: "storage failure releases both sets of files", wantError: "resolve the ZCode native store", storageFailure: true},
		{name: "successful handoff transfers only the main files"},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts, _ := zcodeStartupFixture(t, test.catalog)
			original, err := zcodeLocator.Resolve(t.Context(), opts.Shell, false, "ZCode test peer")
			require.NoError(t, err)
			opts.Shell, opts.LoginShell = shell, true
			root := t.TempDir()
			configRoot := filepath.Join(root, "config")
			require.NoError(t, os.MkdirAll(filepath.Join(configRoot, "nushell"), 0o700))
			t.Setenv("TMPDIR", root)
			opts.ExtraEnv = append(opts.ExtraEnv, "XDG_CONFIG_HOME="+configRoot)
			witness := filepath.Join(root, "preparations.jsonl")
			const candidate = "leapmux-zcode-test-peer"
			pattern := filepath.Join(root, "leapmux-nu-launch-*")
			profile := "let paths = (glob " + strconv.Quote(pattern) + " | sort | to json -r)\n" +
				"$\"($paths)(char nl)\" | save --append " + strconv.Quote(witness) + "\n"
			if test.profileFailure {
				profile += "exit 41\n"
			}
			profile += "def --wrapped " + candidate + " [...arguments: string] { ^" + strconv.Quote(original.Program) +
				" '-test.run=^TestZCodeStartupProcess$' '--' ...$arguments }\n"
			require.NoError(t, os.WriteFile(filepath.Join(configRoot, "nushell", "config.nu"), []byte(profile), 0o600))
			if test.storageFailure {
				opts.ExtraEnv = append(opts.ExtraEnv, "LEAPMUX_ZCODE_FIXTURE_STORAGE_FAILURE=1")
			}
			spec := launch.ShellCommands(candidate)
			spec.Env = original.Env
			zcodeLocator = launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
				return spec, launch.Found
			})

			started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
			if started != nil {
				t.Cleanup(func() { started.Stop(); _ = started.Wait() })
			}
			if test.wantError == "" {
				require.NoError(t, err)
				require.NotNil(t, started)
			} else {
				require.ErrorContains(t, err, test.wantError)
				require.Nil(t, started)
			}
			data, err := os.ReadFile(witness)
			require.NoError(t, err)
			var observed [][]string
			for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
				var paths []string
				require.NoError(t, json.Unmarshal([]byte(line), &paths))
				observed = append(observed, paths)
			}
			require.NotEmpty(t, observed)
			require.Len(t, observed[0], 1, "the account probe must see the main preparation")
			mainPath := observed[0][0]
			if test.storageFailure || test.wantError == "" {
				require.GreaterOrEqual(t, len(observed), 2)
				require.Len(t, observed[1], 2, "the storage query must see two distinct preparations")
				require.Contains(t, observed[1], mainPath)
				for _, path := range observed[1] {
					if path != mainPath {
						assert.NoDirExists(t, path)
					}
				}
			} else {
				require.Len(t, observed, 1, "startup must stop before preparing the storage command")
			}
			if started != nil {
				data, err := os.ReadFile(filepath.Join(mainPath, candidate+".nu"))
				require.NoError(t, err)
				assert.Contains(t, string(data), candidate)
				started.Stop()
				_ = started.Wait()
			}
			assert.NoDirExists(t, mainPath)
			leftover, err := filepath.Glob(pattern)
			require.NoError(t, err)
			assert.Empty(t, leftover)
		})
	}
}

func TestStart_FailsBeforeSpawningSubprocessWhenStartupPromptCanceled(t *testing.T) {
	opts, calls := zcodeStartupFixture(t, "")
	t.Setenv(zcodeCredentialSecretEnv, "")
	t.Setenv(zcodeCredentialSecretFileEnv, "")

	osKey, _ := zcodeTestRandomOSKey(t)
	ct := zcodeTestEncryptV2Credential(t, osKey, zcodeCredentialSourceOS, "oauth-token")
	credPath := filepath.Join(opts.HomeDir, ".zcode", "v2", "credentials.json")
	require.NoError(t, os.WriteFile(credPath, []byte(`{"zcodejwttoken":"`+ct+`"}`), 0o600))

	prevProbe := zcodeDefaultOSKeyProbe
	zcodeDefaultOSKeyProbe = func(string, string) zcodeOSKeyAccessState {
		return zcodeOSKeyAccessMayPrompt
	}
	t.Cleanup(func() { zcodeDefaultOSKeyProbe = prevProbe })

	promptCalled := false
	opts.ConfirmStartupPrompt = func(_ context.Context, prompt agent.StartupPrompt) (bool, error) {
		promptCalled = true
		assert.Equal(t, "macOS Keychain access required", prompt.Title)
		assert.Equal(t, "Proceed", prompt.ConfirmLabel)
		assert.Equal(t, "Cancel", prompt.CancelLabel)
		assert.NotEmpty(t, prompt.Steps)
		return false, nil
	}

	started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
	if started != nil {
		t.Cleanup(func() { started.Stop(); _ = started.Wait() })
	}
	require.Nil(t, started)
	require.ErrorIs(t, err, agent.ErrStartupCanceled)
	assert.True(t, promptCalled)
	assert.NoFileExists(t, calls, "zcode app-server must never be spawned when startup confirmation is canceled")
}
