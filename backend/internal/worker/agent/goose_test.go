//go:build unix

package agent

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
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
			if scenario == "new-refused" {
				return `{"code":-32000,"message":"workspace is not readable"}`, true, true
			}
			return `{"sessionId":"goose-new","models":{"currentModelId":"default-model","availableModels":[{"modelId":"default-model","name":"Default Model","description":"Default"},{"modelId":"fast-model","name":"Fast Model","description":"Fast"}]},"modes":{"currentModeId":"auto","availableModes":[{"id":"auto","name":"Auto"},{"id":"approve","name":"Approve"},{"id":"smart_approve","name":"Smart Approve"},{"id":"chat","name":"Chat"}]},"configOptions":[{"id":"mode","currentValue":"auto","options":[{"value":"auto","name":"Auto"},{"value":"approve","name":"Approve"},{"value":"smart_approve","name":"Smart Approve"},{"value":"chat","name":"Chat"}]},{"id":"model","currentValue":"default-model","options":[{"value":"default-model","name":"Default Model"},{"value":"fast-model","name":"Fast Model"}]}]}`, false, true
		case acpMethodSessionLoad:
			if scenario == "load-refused" {
				return `{"code":-32000,"message":"session not found"}`, true, true
			}
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
	assert.Equal(t, contracts.GooseModeAuto, agent.permissionMode)
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
	assert.Equal(t, []string{"Smart Approve", "Auto", "Approve", "Chat"}, modeNames)
}

func TestFallbackGooseModesPutSmartApproveFirst(t *testing.T) {
	t.Parallel()

	modes := fallbackGooseCLIModes()
	require.NotEmpty(t, modes)
	assert.Equal(t, contracts.GooseModeSmartApprove, modes[0].GetId())
	// The rest keep Goose's own order, so the fallback and a live catalog agree.
	rest := make([]string, 0, len(modes)-1)
	for _, mode := range modes[1:] {
		rest = append(rest, mode.GetId())
	}
	assert.Equal(t, []string{contracts.GooseModeAuto, contracts.GooseModeApprove, contracts.GooseModeChat}, rest)
}

// orderModesPreferredFirst is the one ordering rule every rebuilt permission list uses,
// so its edges decide what the picker shows and which option position 0 badges as the
// group default.
func TestOrderModesPreferredFirst(t *testing.T) {
	t.Parallel()

	ids := func(modes []*leapmuxv1.AvailableOption) []string {
		out := make([]string, 0, len(modes))
		for _, mode := range modes {
			out = append(out, mode.GetId())
		}
		return out
	}
	modes := func(values ...string) []*leapmuxv1.AvailableOption {
		out := make([]*leapmuxv1.AvailableOption, 0, len(values))
		for _, value := range values {
			out = append(out, &leapmuxv1.AvailableOption{Id: value})
		}
		return out
	}

	cases := []struct {
		name      string
		in        []*leapmuxv1.AvailableOption
		preferred string
		want      []string
	}{
		{"moves the preferred mode to the front", modes("a", "b", "c"), "c", []string{"c", "a", "b"}},
		{"leaves a list already led by it alone", modes("c", "a", "b"), "c", []string{"c", "a", "b"}},
		{"leaves a list that omits it alone", modes("a", "b"), "c", []string{"a", "b"}},
		{"orders nothing without a preferred mode", modes("a", "b"), "", []string{"a", "b"}},
		{"handles an empty list", modes(), "c", []string{}},
		// A server that reports the id twice must not rotate the others: both copies
		// land at the front and every other mode keeps its reported position.
		{"keeps the order under a duplicate", modes("c", "a", "c", "b"), "c", []string{"c", "c", "a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orderModesPreferredFirst(tc.in, tc.preferred)
			assert.Equal(t, tc.want, ids(tc.in))
		})
	}
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
	assert.Equal(t, contracts.GooseModeApprove, agent.permissionMode)
}

// A resume the agent refuses fails the whole start, for every ACP provider:
// startACPAgent is the one handshake behind all of them. It used to answer a
// refused session/load with session/new, which opened an EMPTY session and
// reported success -- the user got a tab with no history and no report of why.
func TestStartGooseCLI_RefusedResumeFailsTheStart(t *testing.T) {
	installFakeGooseCLI(t, "load-refused")

	provider, err := StartGooseCLI(context.Background(), Options{
		AgentID:         "goose-load-refused",
		WorkingDir:      t.TempDir(),
		ResumeSessionID: "goose-resume",
		Shell:           testutil.TestShell(),
		LoginShell:      false,
		AgentProvider:   leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
	}, &testSink{})
	require.Error(t, err)
	assert.Nil(t, provider, "a start that fails hands back no agent to talk to")
	assert.Contains(t, err.Error(), "goose-resume", "the handle that failed is what the user has to replace")
	assert.Contains(t, err.Error(), "session not found", "the agent's own reason must reach the tab")
	assert.Contains(t, err.Error(), "/clear", "the failure must state the command that recovers the tab")
}

// A start that carries no resume handle must not report a resume failure. The
// wrap is keyed on the handle, which is also what picks session/load over
// session/new, so the two can never disagree.
func TestStartGooseCLI_RefusedNewSessionIsNotReportedAsAResume(t *testing.T) {
	installFakeGooseCLI(t, "new-refused")

	provider, err := StartGooseCLI(context.Background(), Options{
		AgentID:       "goose-new-refused",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
	}, &testSink{})
	require.Error(t, err)
	assert.Nil(t, provider)
	assert.Contains(t, err.Error(), "workspace is not readable")
	assert.NotContains(t, err.Error(), "could not resume", "nothing was asked to resume")
	assert.NotContains(t, err.Error(), "/clear", "`/clear` restarts on a fresh session, which is what already failed")
}

func TestGooseUpdateSettingsSendsLiveACPRequests(t *testing.T) {
	agent, requests := newGooseAgentForRPC(t)
	agent.availableModes = []*leapmuxv1.AvailableOption{
		{Id: contracts.GooseModeAuto, Name: "Auto"},
		{Id: contracts.GooseModeApprove, Name: "Approve"},
	}

	updated := agent.UpdateSettings(map[string]string{
		OptionIDModel:          "fast-model",
		OptionIDPermissionMode: contracts.GooseModeApprove,
	})
	require.True(t, updated.AppliedLive)
	assert.Equal(t, "fast-model", agent.model)
	assert.Equal(t, contracts.GooseModeApprove, agent.permissionMode)

	recorded := requests()
	require.Len(t, recorded, 2)
	assert.Equal(t, acpMethodSessionSetConfigOption, recorded[0].Method)
	assert.Equal(t, acpConfigOptionIDModel, recorded[0].Params["configId"])
	assert.Equal(t, "fast-model", recorded[0].Params["value"])
	assert.Equal(t, acpMethodSessionSetMode, recorded[1].Method)
	assert.Equal(t, contracts.GooseModeApprove, recorded[1].Params["modeId"])
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
	assert.Equal(t, contracts.GooseModeSmartApprove, groups[0].GetOptions()[0].GetId())
}

func TestDefaultModel_GooseUsesEnvOverride(t *testing.T) {
	t.Setenv("LEAPMUX_GOOSE_DEFAULT_MODEL", "custom-model")
	assert.Equal(t, "custom-model", DefaultModel(leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE))
}
