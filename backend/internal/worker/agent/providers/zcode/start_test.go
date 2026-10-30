package zcode

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
		case MethodUpdateProviderRegistry:
			if os.Getenv("LEAPMUX_ZCODE_FIXTURE_PROTOCOL") == "legacy" {
				reply["error"] = map[string]any{"code": -32602, "message": "the legacy registry requires providers"}
			} else {
				reply["error"] = map[string]any{"code": ErrMethodNotFound, "message": "method not found"}
			}
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

func zcodeStartupFixture(t *testing.T, protocol, legacyConfig string) (agent.Options, string) {
	t.Helper()
	shell, err := exec.LookPath("sh")
	require.NoError(t, err)
	executable, err := os.Executable()
	require.NoError(t, err)
	home, working := t.TempDir(), t.TempDir()
	configDir := filepath.Join(home, ".zcode", "v2")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	if legacyConfig != "" {
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.json"), []byte(legacyConfig), 0o600))
	}
	// The native personal file is independent of the legacy registry and OAuth store.
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "provider_config.json"), []byte(`{"schemaVersion":1,"config":{"providerConfigRules":{"providerRules":[{"providerId":"personal:work","enabled":true,"config":{"access":{"type":"api-key","apiKey":"fixture-key"},"api":{"type":"openai-chat-completions","baseUrl":"https://model.invalid/v1"},"personalModelIds":["m"]}}]}}}`), 0o600))
	builtin := filepath.Join(t.TempDir(), "builtin.json")
	require.NoError(t, os.WriteFile(builtin, []byte(zcodeBuiltinZAIAccountConfig), 0o600))
	calls := filepath.Join(t.TempDir(), "calls.log")
	key := zcodeLaunchKey{shellPath: shell}
	previous, existed := zcodeLaunchCache.Load(key)
	zcodeLaunchCache.Store(key, zcodeLaunchCacheEntry{res: launch.Found, spec: launch.Spec{
		Program: executable, PrefixArgs: []string{"-test.run=^TestZCodeStartupProcess$", "--"},
		Env: []string{"LEAPMUX_ZCODE_STARTUP_FIXTURE=1", "LEAPMUX_ZCODE_FIXTURE_PROTOCOL=" + protocol,
			"LEAPMUX_ZCODE_FIXTURE_DATABASE=" + filepath.Join(home, "native.db"), "LEAPMUX_ZCODE_FIXTURE_CALLS=" + calls,
			zcodeBuiltinProviderConfigEnv + "=" + builtin},
	}})
	t.Cleanup(func() {
		if existed {
			zcodeLaunchCache.Store(key, previous)
		} else {
			zcodeLaunchCache.Delete(key)
		}
	})
	return agent.Options{AgentID: "personal-only", HomeDir: home, WorkingDir: working, Shell: shell,
		StartupTimeout: 30 * time.Second, APITimeout: 30 * time.Second,
		ExtraEnv: []string{"HOME=" + home, "USERPROFILE=" + home}}, calls
}

func TestZCodeStart_UsesNativePersonalProvidersWithoutALegacyRegistry(t *testing.T) {
	// Launch resolution is a package cache, so these fixtures run serially.
	for _, legacyConfig := range []string{"", `{"provider":{}}`} {
		t.Run(legacyConfig, func(t *testing.T) {
			opts, _ := zcodeStartupFixture(t, "current", legacyConfig)
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

func TestZCodeStart_RequiresARegistryOnlyAfterLegacyProtocolDetection(t *testing.T) {
	for _, legacyConfig := range []string{"", `{"provider":{}}`} {
		t.Run(legacyConfig, func(t *testing.T) {
			opts, calls := zcodeStartupFixture(t, "legacy", legacyConfig)
			started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
			require.Error(t, err)
			assert.Nil(t, started)
			data, readErr := os.ReadFile(calls)
			require.NoError(t, readErr)
			assert.Contains(t, string(data), MethodUpdateProviderRegistry)
			assert.NotContains(t, string(data), MethodSessionCreate)
		})
	}
}

func TestZCodeStart_ReportsNativePersonalConfigurationFailure(t *testing.T) {
	opts, _ := zcodeStartupFixture(t, "current", "")
	opts.ExtraEnv = append(opts.ExtraEnv, "LEAPMUX_ZCODE_FIXTURE_INVALID_PERSONAL=1")
	started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
	require.ErrorContains(t, err, "native provider configuration has no executable model")
	assert.Nil(t, started)
}

// A subscribe reply that proves nothing fails the whole start: the agent must
// not run without a live event stream.
func TestZCodeStart_ReportsAnInvalidSubscribeReceipt(t *testing.T) {
	opts, _ := zcodeStartupFixture(t, "current", "")
	opts.ExtraEnv = append(opts.ExtraEnv, "LEAPMUX_ZCODE_FIXTURE_BAD_SUBSCRIBE=1")
	started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
	require.ErrorContains(t, err, MethodSessionSubscribe)
	assert.Nil(t, started)
}

func TestZCodeStart_RejectsMalformedLegacyConfigurationBeforeTheServer(t *testing.T) {
	opts, calls := zcodeStartupFixture(t, "current", "{")
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
	opts, calls := zcodeStartupFixture(t, "current", "")
	opts.ExtraEnv = append(opts.ExtraEnv, "LEAPMUX_ZCODE_FIXTURE_BAD_REGISTRY_REFRESH=1")
	started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
	require.Error(t, err)
	assert.Nil(t, started)
	assert.Equal(t, strings.Join([]string{
		MethodUpdateProviderRegistry, MethodUpdateAccountConfig, MethodProviderRefreshRegistry,
	}, "\n"), zcodeStartupCalls(t, calls),
		"a refused registry refresh stops the start before the catalog read")
}

// A complete catalog controls choices without changing the observed current model.
func TestZCodeCatalog_RetainsAnUnavailableCurrentModelAtStartup(t *testing.T) {
	opts, calls := zcodeStartupFixture(t, "current", "")
	opts.ExtraEnv = append(opts.ExtraEnv, "LEAPMUX_ZCODE_FIXTURE_REVOKED_MODEL=1")
	started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)
	t.Cleanup(func() { started.Stop(); _ = started.Wait() })
	native := started.(*Agent)
	assert.Equal(t, "personal:work/revoked", native.model)
	snapshot := native.SettingsSnapshot()
	assert.Equal(t, agent.OptionSettlementUnresolved, snapshot.Settlements[agent.OptionIDModel].State)
	assert.Equal(t, strings.Join([]string{
		MethodUpdateProviderRegistry, MethodUpdateAccountConfig, MethodProviderRefreshRegistry,
		MethodSessionCreate, MethodSetMode, MethodSessionSubscribe,
	}, "\n"), zcodeStartupCalls(t, calls), "startup must not choose a different model")
}

// A persisted selection that the live catalog still holds stays: the repair adds
// no setter call of its own.
func TestZCodeModelRepair_RetainsAValidPersistedSelection(t *testing.T) {
	opts, calls := zcodeStartupFixture(t, "current", "")
	started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)
	t.Cleanup(func() { started.Stop(); _ = started.Wait() })
	native := started.(*Agent)
	native.Mu.Lock()
	defer native.Mu.Unlock()
	assert.Equal(t, "personal:work/m", native.model)
	assert.Equal(t, strings.Join([]string{
		MethodUpdateProviderRegistry, MethodUpdateAccountConfig, MethodProviderRefreshRegistry,
		MethodSessionCreate, MethodSetModel, MethodSetMode, MethodSessionSubscribe,
	}, "\n"), zcodeStartupCalls(t, calls),
		"the normal reassertion is the only setter call")
}

// An explicitly empty live catalog is the authoritative answer: no setter call,
// and the model is unresolved.
func TestZCodeModelRepair_AnExplicitlyEmptyCatalogMarksTheModelUnresolved(t *testing.T) {
	opts, calls := zcodeStartupFixture(t, "current", "")
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
		MethodUpdateProviderRegistry, MethodUpdateAccountConfig, MethodProviderRefreshRegistry,
		MethodSessionCreate, MethodSetMode, MethodSessionSubscribe,
	}, "\n"), zcodeStartupCalls(t, calls),
		"an explicitly empty catalog makes no setter call")
}
