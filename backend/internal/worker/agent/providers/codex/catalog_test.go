package codex

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each provider that carries the sentinel builds its row through
// agent.AccountDefaultModelEntry, so the row cannot drift between the providers.
// TestAccountDefaultModelEntry pins the shape of that row.
func TestCodexCatalogCarriesTheAccountDefaultEntry(t *testing.T) {
	t.Parallel()

	row := agent.FindAvailableModel(codexDefaultModels, agent.DefaultModelSentinel)
	require.NotNil(t, row)
	assert.Equal(t, "Default (recommended)", row.DisplayName)
	assert.Empty(t, row.SupportedEfforts)
	assert.Zero(t, row.ContextWindow)
}

// codexDiscoveryReplies answers the three discovery requests; a test overrides one.
type codexDiscoveryReplies struct {
	profiles     func(cursor string) agenttest.RPCReply
	requirements agenttest.RPCReply
	config       agenttest.RPCReply
}

func newCodexDiscoveryAgent(t *testing.T, replies codexDiscoveryReplies) (*Agent, func() []codexRecordedRequest) {
	t.Helper()
	if replies.requirements.Result == nil && replies.requirements.Error == nil {
		replies.requirements = agenttest.RPCReply{Result: json.RawMessage(`{"requirements":null}`)}
	}
	if replies.config.Result == nil && replies.config.Error == nil {
		replies.config = agenttest.RPCReply{Result: json.RawMessage(`{"config":{},"origins":{}}`)}
	}
	var requests func() []codexRecordedRequest
	var a *Agent
	a, _, requests = newCodexAgentForRPC(t, func(method string) agenttest.RPCReply {
		switch method {
		case "permissionProfile/list":
			cursor := ""
			sent := methodRequests(requests(), method)
			if last := sent[len(sent)-1]; last.Params["cursor"] != nil {
				cursor = last.Params["cursor"].(string)
			}
			return replies.profiles(cursor)
		case "configRequirements/read":
			return replies.requirements
		case "config/read":
			return replies.config
		}
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32601,"message":"unexpected"}`)}
	})
	return a, requests
}

func profilesPage(entries string, nextCursor string) agenttest.RPCReply {
	cursor := "null"
	if nextCursor != "" {
		cursor = fmt.Sprintf("%q", nextCursor)
	}
	return agenttest.RPCReply{Result: json.RawMessage(fmt.Sprintf(`{"data":[%s],"nextCursor":%s}`, entries, cursor))}
}

func optionIDs(options []*leapmuxv1.AvailableOption) []string {
	ids := make([]string, 0, len(options))
	for _, option := range options {
		ids = append(ids, option.GetId())
	}
	return ids
}

func TestCodexDiscoverPermissionsKeepsTheAllowedProfiles(t *testing.T) {
	t.Parallel()

	a, requests := newCodexDiscoveryAgent(t, codexDiscoveryReplies{
		profiles: func(string) agenttest.RPCReply {
			return profilesPage(`{"id":":workspace","description":"Codex says workspace","allowed":true},`+
				`{"id":":read-only","description":null,"allowed":false},`+
				`{"id":"team","description":"The team profile.","allowed":true},`+
				`{"id":"bare","description":null,"allowed":true},`+
				`{"id":"team","description":"a duplicate","allowed":true},`+
				`{"id":"","description":null,"allowed":true}`, "")
		},
	})
	a.permissionMode = ModeAskForApproval
	a.permissionProfile = ProfileReadOnly

	settled := a.discoverPermissions("/work", time.Second)

	assert.Equal(t, []string{contracts.CodexPermissionProfileWorkspace, "team", "bare"}, optionIDs(a.permissionProfiles.options))
	assert.Equal(t, "Workspace", a.permissionProfiles.options[0].GetName(), "a built-in profile keeps its LeapMux name")
	assert.Equal(t, codexBuiltinProfiles[0].GetDescription(), a.permissionProfiles.options[0].GetDescription())
	assert.Equal(t, "team", a.permissionProfiles.options[1].GetName(), "a config.toml profile is named by its id")
	assert.Equal(t, "The team profile.", a.permissionProfiles.options[1].GetDescription())
	assert.Equal(t, "A permission profile from config.toml.", a.permissionProfiles.options[2].GetDescription())
	assert.Equal(t, codexPermissionSettings{mode: ModeAskForApproval, profile: contracts.CodexPermissionProfileWorkspace}, settled,
		"a profile that the requirements refuse falls back to the workspace")
	assert.Empty(t, a.refusedModes)

	sent := methodRequests(requests(), "permissionProfile/list")
	require.Len(t, sent, 1)
	assert.Equal(t, "/work", sent[0].Params["cwd"], "project config layers resolve from the working directory")
	assert.Equal(t, "/work", methodRequests(requests(), "config/read")[0].Params["cwd"])
	assert.Len(t, methodRequests(requests(), "configRequirements/read"), 1)
}

func TestCodexDiscoverPermissionsReadsEveryPage(t *testing.T) {
	t.Parallel()

	a, requests := newCodexDiscoveryAgent(t, codexDiscoveryReplies{
		profiles: func(cursor string) agenttest.RPCReply {
			if cursor == "" {
				return profilesPage(`{"id":":workspace","description":null,"allowed":true}`, "page-2")
			}
			return profilesPage(`{"id":"team","description":null,"allowed":true}`, "")
		},
	})
	a.permissionProfile = "team"

	a.discoverPermissions("/work", time.Second)

	assert.Equal(t, []string{contracts.CodexPermissionProfileWorkspace, "team"}, optionIDs(a.permissionProfiles.options))
	assert.Equal(t, "team", a.permissionProfile)
	sent := methodRequests(requests(), "permissionProfile/list")
	require.Len(t, sent, 2)
	assert.Equal(t, "page-2", sent[1].Params["cursor"])
}

func TestCodexDiscoverPermissionsStopsAtThePageLimit(t *testing.T) {
	t.Parallel()

	a, requests := newCodexDiscoveryAgent(t, codexDiscoveryReplies{
		profiles: func(cursor string) agenttest.RPCReply {
			return profilesPage(fmt.Sprintf(`{"id":"profile-%s","description":null,"allowed":true}`, cursor), "next"+cursor)
		},
	})
	a.permissionProfile = "profile-after-the-limit"

	a.discoverPermissions("/work", time.Second)

	assert.Len(t, methodRequests(requests(), "permissionProfile/list"), codexCatalogMaxPages)
	assert.False(t, a.permissionProfiles.available, "an incomplete catalog cannot replace the full catalog")
	assert.Equal(t, "profile-after-the-limit", a.permissionProfile)
}

func TestCodexDiscoverPermissionsFallsBackToTheSeed(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		reply agenttest.RPCReply
	}{
		{name: "a failed query", reply: agenttest.RPCReply{Error: json.RawMessage(`{"code":-32601,"message":"method not found"}`)}},
		{name: "an unreadable answer", reply: agenttest.RPCReply{Result: json.RawMessage(`{"data":"nope"}`)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			a, _ := newCodexDiscoveryAgent(t, codexDiscoveryReplies{
				profiles: func(string) agenttest.RPCReply { return test.reply },
			})
			a.permissionProfile = "team"

			a.discoverPermissions("/work", time.Second)

			assert.False(t, a.permissionProfiles.available, "the static seed applies")
			assert.Equal(t, "team", a.permissionProfile, "without a catalog, Codex decides about the stored profile")
		})
	}
}

func TestCodexDiscoverPermissionsKeepsAnEmptyAllowedCatalog(t *testing.T) {
	t.Parallel()
	a, requests := newCodexDiscoveryAgent(t, codexDiscoveryReplies{
		profiles: func(string) agenttest.RPCReply {
			return profilesPage(`{"id":":workspace","allowed":false}`, "")
		},
	})
	a.permissionProfile = "managed"
	a.discoverPermissions("/work", time.Second)
	a.threadID = "thread-1"
	for _, group := range a.OptionGroups() {
		if group.GetId() == "permission_profile" {
			assert.Equal(t, []string{"managed"}, optionIDs(group.GetOptions()))
		}
	}
	a.UpdateSettings(map[string]string{"permission_profile": contracts.CodexPermissionProfileDangerFullAccess})
	assert.Equal(t, "managed", a.permissionProfile)
	assert.Empty(t, methodRequests(requests(), codexMethodThreadSettingsUpdate))
}

func TestCodexModelDiscoveryReadsEveryPage(t *testing.T) {
	t.Parallel()
	a, _, requests := newCodexAgentForRPCRequests(t, func(request codexRecordedRequest) agenttest.RPCReply {
		if request.Params["cursor"] == nil {
			return agenttest.RPCReply{Result: json.RawMessage(`{"data":[{"id":"first"}],"nextCursor":"second-page"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{"data":[{"id":"second"}],"nextCursor":null}`)}
	})
	models := a.queryAvailableModels(time.Second)
	require.Len(t, models, 2)
	assert.Equal(t, "first", models[0].GetId())
	assert.Equal(t, "second", models[1].GetId())
	assert.Equal(t, "second-page", requests()[1].Params["cursor"])
}

func TestCodexProfileDiscoverySharesOneDeadline(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	trap := clock.Trap().NewTimer(providerkit.AwaitResponseTimerTag, "permissionProfile/list")
	defer trap.Close()
	first, second := make(chan struct{}), make(chan struct{})
	defer close(second)
	a, _, _ := newCodexAgentForRPCRequests(t, func(request codexRecordedRequest) agenttest.RPCReply {
		if request.Params["cursor"] == nil {
			<-first
			return profilesPage(`{"id":"first","allowed":true}`, "page-2")
		}
		<-second
		return profilesPage(``, "")
	}, clock)
	done := make(chan struct{})
	go func() { a.queryPermissionProfiles("/work", time.Minute); close(done) }()
	require.Equal(t, time.Minute, testutil.WaitForTimer(t, ctx, trap))
	clock.Advance(20 * time.Second).MustWait(ctx)
	close(first)
	require.Equal(t, 40*time.Second, testutil.WaitForTimer(t, ctx, trap))
	clock.Advance(40 * time.Second).MustWait(ctx)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the catalog did not stop at its original deadline")
	}
}

func TestCodexProfileDiscoveryRejectsRepeatedCursors(t *testing.T) {
	t.Parallel()
	a, requests := newCodexDiscoveryAgent(t, codexDiscoveryReplies{
		profiles: func(string) agenttest.RPCReply { return profilesPage(`{"id":"first","allowed":true}`, "same") },
	})
	a.permissionProfile = "later"
	a.discoverPermissions("/work", time.Second)
	assert.Len(t, methodRequests(requests(), "permissionProfile/list"), 2)
	assert.Equal(t, "later", a.permissionProfile)
}

func TestCodexProfileDiscoveryRequiresTheDataArray(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{}`, `{"data":null}`, `{"data":"invalid"}`} {
		a, _ := newCodexDiscoveryAgent(t, codexDiscoveryReplies{profiles: func(string) agenttest.RPCReply {
			return agenttest.RPCReply{Result: json.RawMessage(raw)}
		}})
		assert.False(t, a.queryPermissionProfiles("/work", time.Second).available, raw)
	}
}

func TestCodexModelDiscoveryDiscardsAnIncompleteCatalog(t *testing.T) {
	t.Parallel()
	for _, reply := range []agenttest.RPCReply{
		{Error: json.RawMessage(`{"code":-32603,"message":"second page failed"}`)},
		{Result: json.RawMessage(`{"data":"invalid"}`)},
		{Result: json.RawMessage(`{"data":[{"id":"second"}],"nextCursor":"repeat"}`)},
	} {
		a, _, requests := newCodexAgentForRPCRequests(t, func(request codexRecordedRequest) agenttest.RPCReply {
			if request.Params["cursor"] == nil {
				return agenttest.RPCReply{Result: json.RawMessage(`{"data":[{"id":"first"}],"nextCursor":"repeat"}`)}
			}
			return reply
		})
		assert.Nil(t, a.queryAvailableModels(time.Second))
		assert.Len(t, requests(), 2)
	}
}

func TestCodexDiscoverPermissionsReadsTheRequirements(t *testing.T) {
	t.Parallel()

	allowedProfiles := func(string) agenttest.RPCReply {
		return profilesPage(`{"id":":workspace","description":null,"allowed":true}`, "")
	}
	for _, test := range []struct {
		name         string
		requirements string
		config       string
		stored       string
		wantRefused  []string
		wantSettled  string
	}{
		{name: "no requirements", requirements: `{"requirements":null}`, stored: contracts.CodexPermissionModeApproveForMe, wantSettled: contracts.CodexPermissionModeApproveForMe},
		{name: "the user as the only reviewer", requirements: `{"requirements":{"allowedApprovalsReviewers":["user"]}}`, stored: contracts.CodexPermissionModeApproveForMe,
			wantRefused: []string{contracts.CodexPermissionModeApproveForMe}, wantSettled: ModeAskForApproval},
		{name: "auto-review as the only reviewer", requirements: `{"requirements":{"allowedApprovalsReviewers":["auto_review"]}}`, stored: contracts.CodexPermissionModeNever,
			wantRefused: []string{ModeAskForApproval, ModeUnlessTrusted, contracts.CodexPermissionModeNever}, wantSettled: contracts.CodexPermissionModeApproveForMe},
		{name: "on-request as the only policy", requirements: `{"requirements":{"allowedApprovalPolicies":["on-request",{"granular":{"sandboxApproval":true}}]}}`, stored: contracts.CodexPermissionModeNever,
			wantRefused: []string{ModeUnlessTrusted, contracts.CodexPermissionModeNever}, wantSettled: ModeAskForApproval},
		{name: "a requirement that pins auto-review off", requirements: `{"requirements":{"featureRequirements":{"auto_review":false}}}`, stored: contracts.CodexPermissionModeApproveForMe,
			wantRefused: []string{contracts.CodexPermissionModeApproveForMe}, wantSettled: ModeAskForApproval},
		{name: "a requirement that pins auto-review on", requirements: `{"requirements":{"featureRequirements":{"auto_review":true}}}`, stored: contracts.CodexPermissionModeApproveForMe, wantSettled: contracts.CodexPermissionModeApproveForMe},
		{name: "a config that turns auto-review off", config: `{"config":{"features":{"guardian_approval":false}}}`, stored: contracts.CodexPermissionModeApproveForMe,
			wantRefused: []string{contracts.CodexPermissionModeApproveForMe}, wantSettled: ModeAskForApproval},
		{name: "a config that turns auto-review on", config: `{"config":{"features":{"guardian_approval":true}}}`, stored: contracts.CodexPermissionModeApproveForMe, wantSettled: contracts.CodexPermissionModeApproveForMe},
		{name: "a config feature that is not a boolean", config: `{"config":{"features":{"guardian_approval":{"enabled":false}}}}`, stored: contracts.CodexPermissionModeApproveForMe, wantSettled: contracts.CodexPermissionModeApproveForMe},
		{name: "every mode refused", requirements: `{"requirements":{"allowedApprovalPolicies":["on-failure"]}}`, stored: contracts.CodexPermissionModeNever,
			wantRefused: []string{ModeAskForApproval, contracts.CodexPermissionModeApproveForMe, ModeUnlessTrusted, contracts.CodexPermissionModeNever}, wantSettled: contracts.CodexPermissionModeNever},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			replies := codexDiscoveryReplies{profiles: allowedProfiles}
			if test.requirements != "" {
				replies.requirements = agenttest.RPCReply{Result: json.RawMessage(test.requirements)}
			}
			if test.config != "" {
				replies.config = agenttest.RPCReply{Result: json.RawMessage(test.config)}
			}
			a, _ := newCodexDiscoveryAgent(t, replies)
			a.permissionMode = test.stored

			settled := a.discoverPermissions("/work", time.Second)

			var refused []string
			for _, option := range codexPermissionModeOptions() {
				if _, isRefused := a.refusedModes[option.GetId()]; isRefused {
					refused = append(refused, option.GetId())
				}
			}
			assert.Equal(t, test.wantRefused, refused)
			assert.Equal(t, test.wantSettled, settled.mode)
		})
	}
}

func TestCodexDiscoverPermissionsAllowsEveryModeWhenTheQueriesFail(t *testing.T) {
	t.Parallel()

	a, _ := newCodexDiscoveryAgent(t, codexDiscoveryReplies{
		profiles:     func(string) agenttest.RPCReply { return profilesPage(``, "") },
		requirements: agenttest.RPCReply{Error: json.RawMessage(`{"code":-32603,"message":"internal"}`)},
		config:       agenttest.RPCReply{Result: json.RawMessage(`not json`)},
	})
	a.permissionMode = contracts.CodexPermissionModeApproveForMe

	settled := a.discoverPermissions("/work", time.Second)

	assert.Empty(t, a.refusedModes, "Codex still refuses a forbidden mode on the request itself")
	assert.Equal(t, contracts.CodexPermissionModeApproveForMe, settled.mode)
}

func TestSettledPermissionProfile(t *testing.T) {
	t.Parallel()

	workspaceAndTeam := codexPermissionProfileCatalog{available: true, options: []*leapmuxv1.AvailableOption{{Id: contracts.CodexPermissionProfileWorkspace}, {Id: "team"}}}
	teamOnly := codexPermissionProfileCatalog{available: true, options: []*leapmuxv1.AvailableOption{{Id: "team"}, {Id: "other"}}}
	assert.Equal(t, "gone", settledPermissionProfile("gone", codexPermissionProfileCatalog{}), "no catalog: Codex decides")
	assert.Equal(t, "team", settledPermissionProfile("team", workspaceAndTeam))
	assert.Equal(t, contracts.CodexPermissionProfileWorkspace, settledPermissionProfile("gone", workspaceAndTeam))
	assert.Equal(t, "team", settledPermissionProfile("gone", teamOnly), "without the workspace, the first offered profile")
}

func TestCodexModelDiscoveryKeepsAudioCapabilities(t *testing.T) {
	t.Parallel()
	a, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{"data":[{"id":"audio","inputModalities":["text","audio"],"hidden":true},{"id":"text","inputModalities":["text","image"]},{"id":"unknown"}],"nextCursor":null}`)}
	})
	models := a.queryAvailableModels(time.Second)
	require.Len(t, models, 3)
	assert.True(t, models[0].SupportsAudioInput)
	assert.True(t, models[0].Hidden)
	assert.False(t, models[1].SupportsAudioInput)
	assert.False(t, models[2].SupportsAudioInput)
}
