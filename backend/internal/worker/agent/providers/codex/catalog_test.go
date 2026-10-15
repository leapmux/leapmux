package codex

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
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

	assert.Equal(t, []string{ProfileWorkspace, "team", "bare"}, optionIDs(a.permissionProfiles))
	assert.Equal(t, "Workspace", a.permissionProfiles[0].GetName(), "a built-in profile keeps its LeapMux name")
	assert.Equal(t, codexBuiltinProfiles[0].GetDescription(), a.permissionProfiles[0].GetDescription())
	assert.Equal(t, "team", a.permissionProfiles[1].GetName(), "a config.toml profile is named by its id")
	assert.Equal(t, "The team profile.", a.permissionProfiles[1].GetDescription())
	assert.Equal(t, "A permission profile from config.toml.", a.permissionProfiles[2].GetDescription())
	assert.Equal(t, codexPermissionSettings{mode: ModeAskForApproval, profile: ProfileWorkspace}, settled,
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

	assert.Equal(t, []string{ProfileWorkspace, "team"}, optionIDs(a.permissionProfiles))
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

	a.discoverPermissions("/work", time.Second)

	assert.Len(t, methodRequests(requests(), "permissionProfile/list"), codexPermissionProfileMaxPages)
	assert.Len(t, a.permissionProfiles, codexPermissionProfileMaxPages, "the profiles read so far stay")
}

func TestCodexDiscoverPermissionsFallsBackToTheSeed(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		reply agenttest.RPCReply
	}{
		{name: "a failed query", reply: agenttest.RPCReply{Error: json.RawMessage(`{"code":-32601,"message":"method not found"}`)}},
		{name: "an unreadable answer", reply: agenttest.RPCReply{Result: json.RawMessage(`{"data":"nope"}`)}},
		{name: "no allowed profile", reply: profilesPage(`{"id":":workspace","description":null,"allowed":false}`, "")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			a, _ := newCodexDiscoveryAgent(t, codexDiscoveryReplies{
				profiles: func(string) agenttest.RPCReply { return test.reply },
			})
			a.permissionProfile = "team"

			a.discoverPermissions("/work", time.Second)

			assert.Nil(t, a.permissionProfiles, "the static seed applies")
			assert.Equal(t, "team", a.permissionProfile, "without a catalog, Codex decides about the stored profile")
		})
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
		{name: "no requirements", requirements: `{"requirements":null}`, stored: ModeApproveForMe, wantSettled: ModeApproveForMe},
		{name: "the user as the only reviewer", requirements: `{"requirements":{"allowedApprovalsReviewers":["user"]}}`, stored: ModeApproveForMe,
			wantRefused: []string{ModeApproveForMe}, wantSettled: ModeAskForApproval},
		{name: "auto-review as the only reviewer", requirements: `{"requirements":{"allowedApprovalsReviewers":["auto_review"]}}`, stored: ModeNever,
			wantRefused: []string{ModeAskForApproval, ModeUnlessTrusted, ModeNever}, wantSettled: ModeApproveForMe},
		{name: "on-request as the only policy", requirements: `{"requirements":{"allowedApprovalPolicies":["on-request",{"granular":{"sandboxApproval":true}}]}}`, stored: ModeNever,
			wantRefused: []string{ModeUnlessTrusted, ModeNever}, wantSettled: ModeAskForApproval},
		{name: "a requirement that pins auto-review off", requirements: `{"requirements":{"featureRequirements":{"auto_review":false}}}`, stored: ModeApproveForMe,
			wantRefused: []string{ModeApproveForMe}, wantSettled: ModeAskForApproval},
		{name: "a requirement that pins auto-review on", requirements: `{"requirements":{"featureRequirements":{"auto_review":true}}}`, stored: ModeApproveForMe, wantSettled: ModeApproveForMe},
		{name: "a config that turns auto-review off", config: `{"config":{"features":{"guardian_approval":false}}}`, stored: ModeApproveForMe,
			wantRefused: []string{ModeApproveForMe}, wantSettled: ModeAskForApproval},
		{name: "a config that turns auto-review on", config: `{"config":{"features":{"guardian_approval":true}}}`, stored: ModeApproveForMe, wantSettled: ModeApproveForMe},
		{name: "a config feature that is not a boolean", config: `{"config":{"features":{"guardian_approval":{"enabled":false}}}}`, stored: ModeApproveForMe, wantSettled: ModeApproveForMe},
		{name: "every mode refused", requirements: `{"requirements":{"allowedApprovalPolicies":["on-failure"]}}`, stored: ModeNever,
			wantRefused: []string{ModeAskForApproval, ModeApproveForMe, ModeUnlessTrusted, ModeNever}, wantSettled: ModeNever},
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
	a.permissionMode = ModeApproveForMe

	settled := a.discoverPermissions("/work", time.Second)

	assert.Empty(t, a.refusedModes, "Codex still refuses a forbidden mode on the request itself")
	assert.Equal(t, ModeApproveForMe, settled.mode)
}

func TestSettledPermissionProfile(t *testing.T) {
	t.Parallel()

	workspaceAndTeam := []*leapmuxv1.AvailableOption{{Id: ProfileWorkspace}, {Id: "team"}}
	teamOnly := []*leapmuxv1.AvailableOption{{Id: "team"}, {Id: "other"}}
	assert.Equal(t, "gone", settledPermissionProfile("gone", nil), "no catalog: Codex decides")
	assert.Equal(t, "team", settledPermissionProfile("team", workspaceAndTeam))
	assert.Equal(t, ProfileWorkspace, settledPermissionProfile("gone", workspaceAndTeam))
	assert.Equal(t, "team", settledPermissionProfile("gone", teamOnly), "without the workspace, the first offered profile")
}
