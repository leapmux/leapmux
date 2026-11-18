package cmd

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/reflect/protoreflect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
)

// TestTabTypeName_AllValues pins the lowercase short labels the CLI
// prints in a result. The strings have to match the --type values that
// `tab list` accepts (resolve.ParseTabType) and the
// $LEAPMUX_CONTROL_TAB_TYPE env var the resolver reads, so a script
// that reads a type from one result and feeds it back as a flag
// doesn't need a translation table.
//
// EVERY value, as the name says. A kind this table omits emits "" into
// the envelope, and the script that feeds that "" back reads as "no tab
// type given" rather than as an error.
func TestTabTypeName_AllValues(t *testing.T) {
	cases := map[leapmuxv1.TabType]string{
		leapmuxv1.TabType_TAB_TYPE_AGENT:       "agent",
		leapmuxv1.TabType_TAB_TYPE_TERMINAL:    "terminal",
		leapmuxv1.TabType_TAB_TYPE_FILE:        "file",
		leapmuxv1.TabType_TAB_TYPE_IMAGE:       "image",
		leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED: "",
	}
	for in, want := range cases {
		assert.Equal(t, want, tabTypeName(in), "tabTypeName(%v)", in)
	}
}

// TestAgentProviderName_UsesDisplayName guards the alignment between
// the CLI's envelope and the frontend's picker labels. Both sides'
// canonical strings are generated from contracts/providers.json
// (agentlabels.DisplayName on this side, AgentProviderIcon's
// agentProviderLabel on the other), so the alignment is structural --
// this test pins that the envelope actually routes through DisplayName
// rather than spelling its own copy.
func TestAgentProviderName_UsesDisplayName(t *testing.T) {
	for _, p := range agentlabels.AllProviders() {
		assert.Equalf(t, agentlabels.DisplayName(p), agentProviderName(p), "agentProviderName(%v)", p)
	}
	assert.Equal(t, "", agentProviderName(leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED),
		"the CLI envelope omits the name for an unspecified provider rather than rendering a generic word")
}

// enumToken derives the token of every enum from its descriptor: the value
// name without the enum's prefix, lowercase. The zero value prints "", so
// omitempty drops an unset field.
func TestEnumToken(t *testing.T) {
	cases := []struct {
		in   protoreflect.Enum
		want string
	}{
		{leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, "active"},
		{leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, "startup_failed"},
		{leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED, ""},
		{leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER, "waiting_for_user"},
		{leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, "exited"},
		{leapmuxv1.NodeKind_NODE_KIND_LEAF, "leaf"},
		{leapmuxv1.SplitDirection_SPLIT_DIRECTION_VERTICAL, "vertical"},
		{leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, "leapmux"},
		{leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, "completed"},
		{leapmuxv1.AgentStatus(99), ""},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, enumToken(tc.in), "%v", tc.in)
	}
	assert.Empty(t, enumToken(nil))
}

// A tab type has two rules for its token: the contract that the frontend
// shares, and enumToken. They must agree, or one tab would print two
// spellings in two commands.
func TestEnumTokenMatchesTheTabTypeContract(t *testing.T) {
	for value := range leapmuxv1.TabType_name {
		tt := leapmuxv1.TabType(value)
		assert.Equal(t, tabTypeName(tt), enumToken(tt), "%v", tt)
	}
}

// enumTokens lists every value but the zero value, and parseEnumToken reads
// each one back, in any case.
func TestEnumTokensRoundTrip(t *testing.T) {
	tokens := enumTokens(leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_UNSPECIFIED)
	assert.Equal(t, []string{"working", "head", "staged", "unified_diff", "split_diff"}, tokens)
	for _, token := range tokens {
		n, ok := parseEnumToken(leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_UNSPECIFIED, token)
		require.True(t, ok, token)
		assert.Equal(t, token, enumToken(leapmuxv1.TabFileViewMode(n)))
	}
	n, ok := parseEnumToken(leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_UNSPECIFIED, "STAGED")
	require.True(t, ok)
	assert.Equal(t, leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_STAGED, leapmuxv1.TabFileViewMode(n))
	for _, bad := range []string{"", "unspecified", "nope"} {
		_, ok := parseEnumToken(leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_UNSPECIFIED, bad)
		assert.False(t, ok, bad)
	}
}

// A tab row prints the tab type as its token, joins the Worker's title and
// state, and keeps the placement unchanged.
func TestTabRowOf(t *testing.T) {
	in := &leapmuxv1.WorkspaceTab{
		Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "tab-1"},
		Position:    "n",
		TileId:      "tile-1",
		WorkerId:    "worker-A",
		WorkspaceId: "ws-1",
	}
	got := tabRowOf(in, tabDetail{Title: "CLI Design", State: "working"}, "build-box")
	assert.Equal(t, tabRow{
		ID: "tab-1", Type: "agent", Title: "CLI Design", State: "working", Worker: "build-box",
		WorkerID: "worker-A", WorkspaceID: "ws-1", TileID: "tile-1", Position: "n",
	}, got)
}

// An agent row prints every enum as its token, and the activity state that
// `agent wait` waits for.
func TestAgentRowOf_RendersEnumsAsTokens(t *testing.T) {
	in := &leapmuxv1.AgentInfo{
		Id:    "ag-1",
		Title: "demo",
		// The model is projected from the "model" option group's current value.
		OptionGroups: []*leapmuxv1.AvailableOptionGroup{
			{Id: "model", CurrentValue: "claude-sonnet-4"},
		},
		Status:                leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE,
		ActivityState:         leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER,
		AgentProvider:         leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		WorkerId:              "worker-A",
		ActiveBackgroundTasks: 2,
	}
	got := agentRowOf(in, false)
	assert.Equal(t, "active", got.Status)
	assert.Equal(t, "waiting_for_user", got.State)
	assert.Equal(t, "Claude Code", got.Provider)
	assert.Equal(t, "ag-1", got.ID)
	assert.Equal(t, "demo", got.Title)
	assert.Equal(t, "claude-sonnet-4", got.Model)
	assert.Equal(t, "worker-A", got.WorkerID)
	assert.Equal(t, int32(2), got.BackgroundTasks)
	assert.Nil(t, got.Options, "the option catalog is left out unless asked for")
}

// The option catalog projects into snake_case keys, including the int64
// context window and the recursive sub-groups of a model choice -- the shape
// that `agent get --json` consumers parse.
func TestAgentRowOf_ProjectsTheOptionCatalog(t *testing.T) {
	in := &leapmuxv1.AgentInfo{
		Id: "ag-1",
		OptionGroups: []*leapmuxv1.AvailableOptionGroup{{
			Id:           "model",
			Label:        "Model",
			CurrentValue: "opus",
			DefaultValue: "opus",
			Mutable:      true,
			Order:        1,
			Options: []*leapmuxv1.AvailableOption{{
				Id:            "opus",
				Name:          "Opus",
				ContextWindow: 200000,
				SubGroups: []*leapmuxv1.AvailableOptionGroup{{
					Id:      "effort",
					Label:   "Effort",
					Options: []*leapmuxv1.AvailableOption{{Id: "high", Name: "High"}},
				}},
			}},
		}},
	}
	raw, err := json.Marshal(agentRowOf(in, true))
	require.NoError(t, err)
	var got struct {
		Options []struct {
			ID      string `json:"id"`
			Label   string `json:"label"`
			Current string `json:"current"`
			Choices []struct {
				ID            string `json:"id"`
				ContextWindow int64  `json:"context_window"`
				SubGroups     []struct {
					ID string `json:"id"`
				} `json:"sub_groups"`
			} `json:"choices"`
		} `json:"options"`
	}
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Len(t, got.Options, 1)
	assert.Equal(t, "model", got.Options[0].ID)
	assert.Equal(t, "Model", got.Options[0].Label)
	assert.Equal(t, "opus", got.Options[0].Current)
	require.Len(t, got.Options[0].Choices, 1)
	assert.Equal(t, int64(200000), got.Options[0].Choices[0].ContextWindow)
	require.Len(t, got.Options[0].Choices[0].SubGroups, 1)
	assert.Equal(t, "effort", got.Options[0].Choices[0].SubGroups[0].ID)
}

// A git status lists the kinds of change, and a directory outside a
// repository prints no git status at all.
func TestGitStatusOf(t *testing.T) {
	assert.Nil(t, gitStatusOf(nil))
	assert.Nil(t, gitStatusOf(&leapmuxv1.GitRepoStatus{}))
	got := gitStatusOf(&leapmuxv1.GitRepoStatus{Branch: "main", Toplevel: "/repo", Modified: true, Untracked: true, Ahead: 1})
	require.NotNil(t, got)
	assert.Equal(t, "main", got.Branch)
	assert.Equal(t, int32(1), got.Ahead)
	assert.Equal(t, []string{"modified", "untracked"}, got.Changes)
}
