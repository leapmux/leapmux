//go:build unix

package cursor

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startCursorNativeLifecycleTestAgent(t *testing.T, sink *agenttest.Sink) *Agent {
	t.Helper()
	installFakeCursorCLI(t, "new")
	provider, err := Start(context.Background(), agent.Options{
		AgentID: "cursor-new", WorkingDir: t.TempDir(), Shell: testutil.TestShell(), LoginShell: false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
	}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	a := provider.(*Agent)
	t.Cleanup(func() { a.Stop(); _ = a.Wait() })
	return a
}

func TestStartCursorCLI_AdvertisesNativeSubagents(t *testing.T) {
	a := startCursorNativeLifecycleTestAgent(t, &agenttest.Sink{})
	assert.Equal(t, true, a.HooksForTest().ClientCapabilityMeta["subagents"])
	assert.Equal(t, true, a.HooksForTest().ClientCapabilityMeta["parameterizedModelPicker"])
	require.NotNil(t, a.HooksForTest().SessionMetadataHandler)
}

func TestCursorParameterizedAutoModel(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "auto", normalizeCursorModelID("default"))
	assert.Equal(t, "default", cursorModelIDForWire("auto"))
}

func TestCursorBypassStartup(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		wantArgs    []string
		invalid     bool
	}{
		{"default", "", []string{"acp"}, false},
		{"off", "false", []string{"acp"}, false},
		{"on", "true", []string{"--force", "acp"}, false},
		{"invalid", "yes", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argsFile := filepath.Join(t.TempDir(), "argv")
			agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
				Binary: "cursor-agent", HelperRun: "TestHelperProcessCursorCLI", WantEnv: "GO_WANT_HELPER_PROCESS_CURSOR",
				Env: []string{"LEAPMUX_CURSOR_TEST_SCENARIO=new"}, ArgsFile: argsFile, ForwardArgs: true,
			})
			started, err := Start(t.Context(), agent.Options{
				AgentID: "cursor-bypass", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
				Options:       map[string]string{"bypassPermissions": tc.value},
			}, agent.NewProviderServices(&agenttest.Sink{}))
			if started != nil {
				t.Cleanup(func() { started.Stop(); _ = started.Wait() })
			}
			if tc.invalid {
				require.ErrorContains(t, err, "bypass")
				assert.NoFileExists(t, argsFile)
				return
			}
			require.NoError(t, err)
			raw, err := os.ReadFile(argsFile)
			require.NoError(t, err)
			assert.Equal(t, tc.wantArgs, strings.Fields(string(raw)))
			wantValue := tc.value
			if wantValue == "" {
				wantValue = "false"
			}
			assert.Equal(t, wantValue, started.SettingsSnapshot().ConfirmedOptions()["bypassPermissions"])
			before := started.SettingsSnapshot().ConfirmedOptions()
			invalid := started.UpdateSettings(map[string]string{"bypassPermissions": "yes", "model": "must-not-apply"})
			assert.True(t, invalid.AppliedLive, "invalid flag input must not restart a usable native process")
			assert.Equal(t, before, invalid.ConfirmedOptions(), "invalid input must not change another setting")

			if tc.value == "true" {
				group := optionids.GroupByID(started.OptionGroups(), "bypassPermissions")
				require.NotNil(t, group)
				assert.False(t, group.GetMutable())
				require.Len(t, group.GetOptions(), 1)
				assert.Equal(t, "true", group.GetOptions()[0].GetId())
				assert.Contains(t, group.GetReadOnlyReason(), "Start a new session")
				result := started.UpdateSettings(map[string]string{"bypassPermissions": "false", "model": "gpt-5.4"})
				assert.True(t, result.AppliedLive, "reject Off without restarting")
				assert.Equal(t, before, result.ConfirmedOptions(), "reject the entire batch before any live write")
			}
		})
	}
}

func TestCursorBypassResumeStartup(t *testing.T) {
	for _, tc := range []struct {
		name, meta, requested string
		want                  bool
		invalid               bool
	}{
		{"stored on overrides off", `{"agentId":"cursor-resume","isRunEverything":true}`, "false", true, false},
		{"stored on without choice", `{"agentId":"cursor-resume","isRunEverything":true}`, "", true, false},
		{"stored off", `{"agentId":"cursor-resume","isRunEverything":false}`, "false", false, false},
		{"missing optional flag", `{"agentId":"cursor-resume"}`, "", false, false},
		{"enable stored off", `{"agentId":"cursor-resume","isRunEverything":false}`, "true", true, false},
		{"invalid flag", `{"agentId":"cursor-resume","isRunEverything":"true"}`, "false", false, true},
		{"mismatched identity", `{"agentId":"another","isRunEverything":false}`, "false", false, true},
		{"missing store", "", "false", false, true},
		{"requested on still validates resume", `null`, "true", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.meta != "" {
				writeCursorACPPolicyFixture(t, home, "cursor-resume", tc.meta)
			}
			argsFile := filepath.Join(t.TempDir(), "argv")
			agenttest.InstallFakeCLI(t, agenttest.FakeCLI{Binary: "cursor-agent", HelperRun: "TestHelperProcessCursorCLI", WantEnv: "GO_WANT_HELPER_PROCESS_CURSOR", Env: []string{"LEAPMUX_CURSOR_TEST_SCENARIO=load"}, ArgsFile: argsFile, ForwardArgs: true})
			started, err := Start(t.Context(), agent.Options{AgentID: "cursor-resumed", HomeDir: home, WorkingDir: t.TempDir(), ResumeSessionID: "cursor-resume", Shell: testutil.TestShell(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, Options: map[string]string{"bypassPermissions": tc.requested}}, agent.NewProviderServices(&agenttest.Sink{}))
			if started != nil {
				t.Cleanup(func() { started.Stop(); _ = started.Wait() })
			}
			if tc.invalid {
				require.Error(t, err)
				assert.NoFileExists(t, argsFile)
				return
			}
			require.NoError(t, err)
			raw, err := os.ReadFile(argsFile)
			require.NoError(t, err)
			wantArgs := []string{"acp"}
			value := "false"
			if tc.want {
				wantArgs = []string{"--force", "acp"}
				value = "true"
			}
			assert.Equal(t, wantArgs, strings.Fields(string(raw)))
			assert.Equal(t, value, started.SettingsSnapshot().ConfirmedOptions()["bypassPermissions"])
		})
	}
}

func writeCursorACPPolicyFixture(t *testing.T, home, id, metadata string) {
	t.Helper()
	db := agenttest.NewFixtureDB(t, cursorACPStorePath(agent.StoredSessionQuery{HomeDir: home}, id), cursorStoreDDL)
	_, err := db.Exec(`PRAGMA journal_mode=WAL`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO meta (key,value) VALUES (?,?)`, cursorMetaKey, hex.EncodeToString([]byte(metadata)))
	require.NoError(t, err)
	// Keep the writer open so the reader must see the WAL-backed metadata.
}
