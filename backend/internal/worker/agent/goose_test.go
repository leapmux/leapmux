//go:build unix

package agent

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
)

func newGooseAgentForRPC(t *testing.T) (*GooseCLIAgent, func() []recordedRequest) {
	return newACPAgentForRPC(t,
		func() *GooseCLIAgent {
			a := &GooseCLIAgent{}
			a.modeChannel = modeChannelPermissionMode
			return a
		},
		func(a *GooseCLIAgent) *acpBase { return &a.acpBase },
	)
}

func newGooseAgentForRPCWithResponder(t *testing.T, respond func(method string) json.RawMessage) (*GooseCLIAgent, func() []recordedRequest) {
	return newACPAgentForRPCWithResponder(t,
		func() *GooseCLIAgent {
			a := &GooseCLIAgent{}
			a.modeChannel = modeChannelPermissionMode
			return a
		},
		func(a *GooseCLIAgent) *acpBase { return &a.acpBase },
		respond,
	)
}

func installFakeGooseCLI(t *testing.T, scenario string) {
	installFakeACPCLI(t, fakeACPCLISpec{
		binary:    "goose",
		helperRun: "TestHelperProcessGooseCLI",
		wantEnv:   "GO_WANT_HELPER_PROCESS_GOOSE",
		env:       []string{"LEAPMUX_GOOSE_TEST_SCENARIO=" + scenario},
	})
}

func TestHelperProcessGooseCLI(*testing.T) {
	scenario := os.Getenv("LEAPMUX_GOOSE_TEST_SCENARIO")
	runFakeACPServer("GO_WANT_HELPER_PROCESS_GOOSE", func(method string) (string, bool, bool) {
		switch method {
		case acpMethodInitialize:
			return `{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}`, false, true
		case acpMethodSessionNew:
			return `{"sessionId":"goose-new","models":{"currentModelId":"default-model","availableModels":[{"modelId":"default-model","name":"Default Model","description":"Default"},{"modelId":"fast-model","name":"Fast Model","description":"Fast"}]},"modes":{"currentModeId":"auto","availableModes":[{"id":"auto","name":"Auto"},{"id":"approve","name":"Approve"},{"id":"smart_approve","name":"Smart Approve"},{"id":"chat","name":"Chat"}]},"configOptions":[{"id":"mode","currentValue":"auto","options":[{"value":"auto","name":"Auto"},{"value":"approve","name":"Approve"},{"value":"smart_approve","name":"Smart Approve"},{"value":"chat","name":"Chat"}]},{"id":"model","currentValue":"default-model","options":[{"value":"default-model","name":"Default Model"},{"value":"fast-model","name":"Fast Model"}]}]}`, false, true
		case acpMethodSessionLoad:
			if scenario == "load" {
				return `{"models":{"currentModelId":"fast-model","availableModels":[{"modelId":"fast-model","name":"Fast Model"}]},"modes":{"currentModeId":"approve","availableModes":[{"id":"auto","name":"Auto"},{"id":"approve","name":"Approve"},{"id":"smart_approve","name":"Smart Approve"},{"id":"chat","name":"Chat"}]}}`, false, true
			}
			return "", false, false
		case acpMethodSessionSetConfigOption, acpMethodSessionSetModel, acpMethodSessionSetMode, acpMethodSessionPrompt:
			return `{}`, false, true
		default:
			return "", false, false
		}
	})
}

func TestStartGooseCLI_NewSessionHandshake(t *testing.T) {
	installFakeGooseCLI(t, "new")

	provider, err := StartGooseCLI(context.Background(), Options{
		AgentID:       "goose-new",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*GooseCLIAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "goose-new", agent.sessionID)
	assert.Equal(t, "default-model", agent.model)
	assert.Equal(t, GooseCLIModeAuto, agent.permissionMode)
	require.Len(t, agent.availableModels, 2)
	assert.Equal(t, "default-model", agent.availableModels[0].GetId())
	groups := agent.OptionGroups()
	modeGroup := optionids.GroupByID(groups, OptionIDPermissionMode)
	require.NotNil(t, modeGroup)
	// Verify mode names are capitalized (e.g. "smart_approve" → "Smart Approve").
	modeNames := make([]string, 0, len(modeGroup.GetOptions()))
	for _, opt := range modeGroup.GetOptions() {
		modeNames = append(modeNames, opt.GetName())
	}
	assert.Equal(t, []string{"Auto", "Approve", "Smart Approve", "Chat"}, modeNames)
}

func TestStartGooseCLI_LoadSessionUsesResumeID(t *testing.T) {
	installFakeGooseCLI(t, "load")

	provider, err := StartGooseCLI(context.Background(), Options{
		AgentID:         "goose-load",
		WorkingDir:      t.TempDir(),
		ResumeSessionID: "goose-resume",
		Shell:           testutil.TestShell(),
		LoginShell:      false,
		AgentProvider:   leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*GooseCLIAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "goose-resume", agent.sessionID)
	assert.Equal(t, "fast-model", agent.model)
	assert.Equal(t, GooseCLIModeApprove, agent.permissionMode)
}

func TestGooseUpdateSettingsSendsLiveACPRequests(t *testing.T) {
	agent, requests := newGooseAgentForRPC(t)
	agent.availableModes = []*leapmuxv1.AvailableOption{
		{Id: GooseCLIModeAuto, Name: "Auto"},
		{Id: GooseCLIModeApprove, Name: "Approve"},
	}

	updated := agent.UpdateSettings(map[string]string{
		OptionIDModel:          "fast-model",
		OptionIDPermissionMode: GooseCLIModeApprove,
	})
	require.True(t, updated)
	assert.Equal(t, "fast-model", agent.model)
	assert.Equal(t, GooseCLIModeApprove, agent.permissionMode)

	recorded := requests()
	require.Len(t, recorded, 2)
	assert.Equal(t, acpMethodSessionSetConfigOption, recorded[0].Method)
	assert.Equal(t, acpConfigOptionIDModel, recorded[0].Params["configId"])
	assert.Equal(t, "fast-model", recorded[0].Params["value"])
	assert.Equal(t, acpMethodSessionSetMode, recorded[1].Method)
	assert.Equal(t, GooseCLIModeApprove, recorded[1].Params["modeId"])
}

func TestGooseCancelSessionSendsACPMethod(t *testing.T) {
	agent, requests := newGooseAgentForRPC(t)

	require.NoError(t, agent.cancelSession())
	testutil.AssertEventually(t, func() bool {
		recorded := requests()
		return len(recorded) == 1 && recorded[0].Method == acpMethodSessionCancel
	}, "expected session/cancel notification to be recorded")
}

func TestGooseAvailableOptionGroupsFallsBack(t *testing.T) {
	// configure sets both the channel and the static fallback list; OptionGroups serves
	// that fallback before the session reports a permission-mode catalog.
	agent := &GooseCLIAgent{acpBase: acpBase{
		modeChannel:       modeChannelPermissionMode,
		secondaryFallback: fallbackGooseCLIModes(),
	}}

	groups := agent.OptionGroups()
	require.Len(t, groups, 1)
	assert.Equal(t, "permissionMode", groups[0].GetId())
	assert.Equal(t, GooseCLIModeAuto, groups[0].GetOptions()[0].GetId())
}

func TestDefaultModel_GooseUsesEnvOverride(t *testing.T) {
	t.Setenv("LEAPMUX_GOOSE_DEFAULT_MODEL", "custom-model")
	assert.Equal(t, "custom-model", DefaultModel(leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE))
}
