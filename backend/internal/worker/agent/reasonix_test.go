//go:build unix

package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func newReasonixAgentForRPC(t *testing.T) (*ReasonixAgent, func() []recordedRequest) {
	return newACPAgentForRPC(t,
		func() *ReasonixAgent { return &ReasonixAgent{} },
		func(a *ReasonixAgent) *acpBase { return &a.acpBase },
	)
}

// installFakeReasonixCLI puts a fake `reasonix` on PATH. The launcher records the
// argv it was invoked with to LEAPMUX_REASONIX_ARGS_FILE so a test can assert the
// startup `--model` flag, then exec's the helper process that speaks ACP.
func installFakeReasonixCLI(t *testing.T, argsFile string) {
	t.Helper()

	dir := t.TempDir()
	launcher := filepath.Join(dir, "reasonix")
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" > %q\nexec %q -test.run=TestHelperProcessReasonixCLI --\n", argsFile, os.Args[0])
	require.NoError(t, os.WriteFile(launcher, []byte(script), 0o755))

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GO_WANT_HELPER_PROCESS_REASONIX", "1")
}

// TestHelperProcessReasonixCLI is the fake Reasonix ACP server. Reasonix's
// session/new returns ONLY a sessionId -- no models/modes/configOptions channel.
func TestHelperProcessReasonixCLI(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS_REASONIX") != "1" {
		return
	}

	scanner := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer func() { _ = writer.Flush() }()

	writeResponse := func(id json.RawMessage, body string) {
		_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", string(id), body)
		_ = writer.Flush()
	}

	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}

		switch req.Method {
		case acpMethodInitialize:
			writeResponse(req.ID, `{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"promptCapabilities":{"image":false,"audio":false,"embeddedContext":true}}}`)
		case acpMethodSessionNew:
			// Minimal: only a sessionId, no models/modes/configOptions.
			writeResponse(req.ID, `{"sessionId":"reasonix-new"}`)
		case acpMethodSessionLoad:
			writeResponse(req.ID, `{}`)
		case acpMethodSessionPrompt:
			writeResponse(req.ID, `{}`)
		}
	}
	os.Exit(0)
}

func TestStartReasonix_NewSessionHandshakePassesModelFlag(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	installFakeReasonixCLI(t, argsFile)

	provider, err := StartReasonix(context.Background(), Options{
		AgentID:       "reasonix-new",
		Model:         "deepseek-flash",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*ReasonixAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "reasonix-new", agent.sessionID)
	assert.Equal(t, "deepseek-flash", agent.model)
	// Reasonix exposes no permission-mode / config-option groups.
	assert.Nil(t, agent.AvailableOptionGroups())

	// The model is fixed at startup via the `--model` flag.
	recorded, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.Contains(t, string(recorded), "acp --model deepseek-flash")
}

func TestStartReasonix_OmitsModelFlagWhenUnset(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	installFakeReasonixCLI(t, argsFile)

	provider, err := StartReasonix(context.Background(), Options{
		AgentID:       "reasonix-default",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*ReasonixAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	// No --model: Reasonix falls back to its config default_model.
	recorded, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.NotContains(t, string(recorded), "--model")
	assert.Equal(t, "acp", strings.TrimSpace(string(recorded)))
}

func TestStartReasonix_LoadSessionUsesResumeID(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	installFakeReasonixCLI(t, argsFile)

	provider, err := StartReasonix(context.Background(), Options{
		AgentID:         "reasonix-load",
		Model:           "deepseek-pro",
		WorkingDir:      t.TempDir(),
		ResumeSessionID: "reasonix-resume",
		Shell:           testutil.TestShell(),
		LoginShell:      false,
		AgentProvider:   leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*ReasonixAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	// session/load returns no sessionId, so the handshake keeps the resume id.
	assert.Equal(t, "reasonix-resume", agent.sessionID)
	assert.Equal(t, "deepseek-pro", agent.model)
}

// TestReasonixUpdateSettingsRelaunchesOnModelChange pins the relaunch contract:
// Reasonix can't switch its model over ACP, so a model change returns false
// (the service falls back to stop+restart with the new --model); an unchanged or
// empty model is a no-op that needs no restart.
func TestReasonixUpdateSettingsRelaunchesOnModelChange(t *testing.T) {
	agent := &ReasonixAgent{}
	agent.model = "deepseek-flash"

	assert.False(t, agent.UpdateSettings(&leapmuxv1.AgentSettings{Model: "deepseek-pro"}),
		"a model change must signal a relaunch")
	assert.True(t, agent.UpdateSettings(&leapmuxv1.AgentSettings{Model: "deepseek-flash"}),
		"the same model needs no restart")
	assert.True(t, agent.UpdateSettings(&leapmuxv1.AgentSettings{Model: ""}),
		"an empty model is a no-op")
	// UpdateSettings must not mutate the stored model (the relaunch carries it).
	assert.Equal(t, "deepseek-flash", agent.model)
}

func TestReasonixCancelSessionSendsACPMethod(t *testing.T) {
	agent, requests := newReasonixAgentForRPC(t)

	require.NoError(t, agent.cancelSession())
	testutil.AssertEventually(t, func() bool {
		recorded := requests()
		return len(recorded) == 1 && recorded[0].Method == acpMethodSessionCancel
	}, "expected session/cancel notification to be recorded")
}

func TestReasonixAvailableOptionGroupsIsNil(t *testing.T) {
	agent := &ReasonixAgent{}
	assert.Nil(t, agent.AvailableOptionGroups())
}

// TestReasonixIgnoresConfigOptionModelUpdate pins the modelFixedAtLaunch guard:
// Reasonix's model is set once via the --model launch flag and cannot change
// over ACP, so a server config_option_update advertising a different model must
// not overwrite the stored model (which would desync it from the running
// process).
func TestReasonixIgnoresConfigOptionModelUpdate(t *testing.T) {
	agent, _ := newReasonixAgentForRPC(t)
	agent.sink = &testSink{}
	agent.modelFixedAtLaunch = true
	agent.model = "deepseek-flash"

	agent.handleACPConfigOptionUpdate(json.RawMessage(
		`{"configOptions":[{"id":"model","currentValue":"deepseek-pro","options":[{"value":"deepseek-flash"},{"value":"deepseek-pro"}]}]}`,
	))

	assert.Equal(t, "deepseek-flash", agent.model,
		"a launch-fixed model must not be overwritten by a config_option_update")
}

func TestReasonixModelCatalog(t *testing.T) {
	// The static catalog mirrors Reasonix's built-in provider entries; the id is
	// the bare provider-entry name its `--model` flag accepts.
	ids := make([]string, 0, len(reasonixAvailableModels))
	defaults := 0
	for _, m := range reasonixAvailableModels {
		ids = append(ids, m.GetId())
		if m.GetIsDefault() {
			defaults++
		}
	}
	assert.Equal(t, []string{"deepseek-flash", "deepseek-pro", "mimo-pro", "mimo-flash"}, ids)
	assert.Equal(t, 1, defaults, "exactly one model is the default")
	assert.True(t, reasonixAvailableModels[0].GetIsDefault(), "deepseek-flash is the default")
}

func TestDefaultModel_ReasonixDefaultsToDeepseekFlash(t *testing.T) {
	t.Setenv("LEAPMUX_REASONIX_DEFAULT_MODEL", "")
	assert.Equal(t, "deepseek-flash", DefaultModel(leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX))
}

func TestDefaultModel_ReasonixUsesEnvOverride(t *testing.T) {
	t.Setenv("LEAPMUX_REASONIX_DEFAULT_MODEL", "deepseek-pro")
	assert.Equal(t, "deepseek-pro", DefaultModel(leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX))
}
