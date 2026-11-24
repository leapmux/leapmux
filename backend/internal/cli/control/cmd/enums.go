package cmd

import (
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/util/optionids"
)

// Every enum in a result prints as one lowercase token, which a flag that
// takes the same enum accepts back:
//
//   - A proto enum prints its value name without the enum's own prefix:
//     NODE_KIND_LEAF is "leaf", and AGENT_ACTIVITY_STATE_WAITING_FOR_USER is
//     "waiting_for_user". enumToken derives the token from the descriptor,
//     so a new enum value needs no table here.
//   - A TabType prints the token of contracts/tab-types.json, which the
//     frontend shares. TestEnumTokenMatchesTheTabTypeContract keeps the two
//     rules in agreement.
//   - An AgentProvider prints its canonical CLI alias from agentlabels,
//     which --provider accepts.
//
// The zero value of every enum prints "", so `omitempty` drops an unset
// field.

// enumToken returns the lowercase token of a proto enum value.
func enumToken(e protoreflect.Enum) string {
	if e == nil || e.Number() == 0 {
		return ""
	}
	desc := e.Descriptor()
	value := desc.Values().ByNumber(e.Number())
	if value == nil {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(string(value.Name()), enumPrefix(desc)))
}

// enumPrefix is the prefix that every value name of the enum shares: the
// name of its zero value without "UNSPECIFIED". TAB_TYPE_UNSPECIFIED gives
// "TAB_TYPE_".
func enumPrefix(desc protoreflect.EnumDescriptor) string {
	zero := desc.Values().ByNumber(0)
	if zero == nil {
		return ""
	}
	return strings.TrimSuffix(string(zero.Name()), "UNSPECIFIED")
}

// enumTokens lists the token of every value of the enum but the zero value,
// in declaration order, for the help of a flag that takes the enum.
func enumTokens(e protoreflect.Enum) []string {
	desc := e.Descriptor()
	values := desc.Values()
	prefix := enumPrefix(desc)
	out := make([]string, 0, values.Len())
	for i := range values.Len() {
		v := values.Get(i)
		if v.Number() == 0 {
			continue
		}
		out = append(out, strings.ToLower(strings.TrimPrefix(string(v.Name()), prefix)))
	}
	return out
}

// parseEnumToken returns the number of the value whose token is token, and
// false for an empty or unknown token.
func parseEnumToken(e protoreflect.Enum, token string) (protoreflect.EnumNumber, bool) {
	if token == "" {
		return 0, false
	}
	desc := e.Descriptor()
	values := desc.Values()
	prefix := enumPrefix(desc)
	for i := range values.Len() {
		v := values.Get(i)
		if v.Number() != 0 && strings.EqualFold(strings.TrimPrefix(string(v.Name()), prefix), token) {
			return v.Number(), true
		}
	}
	return 0, false
}

// tabTypeName projects a TabType to the token every result spells it with.
// The table is generated from contracts/tab-types.json, so the CLI and the
// frontend cannot spell a kind differently.
func tabTypeName(t leapmuxv1.TabType) string {
	return contracts.TabTypeWireToken[t]
}

func agentProviderName(p leapmuxv1.AgentProvider) string {
	if p == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
		return ""
	}
	return agentlabels.DisplayName(p)
}

// kindLabel returns the token of a NodeKind for a message: "leaf",
// "split" or "grid".
func kindLabel(k leapmuxv1.NodeKind) string {
	if token := enumToken(k); token != "" {
		return token
	}
	// A message that prints the kind must not lose it: "tile X is a  node"
	// says nothing.
	return "unknown"
}

// gitStatus is a working tree's git state in a result.
type gitStatus struct {
	Branch     string `json:"branch,omitempty"`
	Ahead      int32  `json:"ahead,omitempty"`
	Behind     int32  `json:"behind,omitempty"`
	Toplevel   string `json:"toplevel,omitempty"`
	OriginURL  string `json:"origin_url,omitempty"`
	IsWorktree bool   `json:"is_worktree,omitempty"`
	// Changes lists the kinds of change that the working tree holds:
	// "modified", "added", "deleted", "renamed", "type_changed",
	// "untracked", "conflicted" and "stashed".
	Changes []string `json:"changes,omitempty"`
}

func gitStatusOf(g *leapmuxv1.GitRepoStatus) *gitStatus {
	if g == nil || g.GetToplevel() == "" && g.GetBranch() == "" {
		return nil
	}
	out := &gitStatus{
		Branch:     g.GetBranch(),
		Ahead:      g.GetAhead(),
		Behind:     g.GetBehind(),
		Toplevel:   g.GetToplevel(),
		OriginURL:  g.GetOriginUrl(),
		IsWorktree: g.GetIsWorktree(),
	}
	for _, c := range []struct {
		set  bool
		name string
	}{
		{g.GetModified(), "modified"},
		{g.GetAdded(), "added"},
		{g.GetDeleted(), "deleted"},
		{g.GetRenamed(), "renamed"},
		{g.GetTypeChanged(), "type_changed"},
		{g.GetUntracked(), "untracked"},
		{g.GetConflicted(), "conflicted"},
		{g.GetStashed(), "stashed"},
	} {
		if c.set {
			out.Changes = append(out.Changes, c.name)
		}
	}
	return out
}

// agentStartupPromptView is a pending startup prompt on an agent.
type agentStartupPromptView struct {
	PromptID     string   `json:"prompt_id"`
	Title        string   `json:"title"`
	Description  string   `json:"description,omitempty"`
	Steps        []string `json:"steps,omitempty"`
	Note         string   `json:"note,omitempty"`
	ConfirmLabel string   `json:"confirm_label,omitempty"`
	CancelLabel  string   `json:"cancel_label,omitempty"`
}

func agentStartupPromptOf(p *leapmuxv1.AgentStartupPrompt) *agentStartupPromptView {
	if p == nil || p.GetPromptId() == "" {
		return nil
	}
	return &agentStartupPromptView{
		PromptID:     p.GetPromptId(),
		Title:        p.GetTitle(),
		Description:  p.GetDescription(),
		Steps:        p.GetSteps(),
		Note:         p.GetNote(),
		ConfirmLabel: p.GetConfirmLabel(),
		CancelLabel:  p.GetCancelLabel(),
	}
}

// agentRow is one agent in a result.
type agentRow struct {
	ID       string `json:"id" table:"ID,short"`
	Title    string `json:"title" table:"TITLE"`
	Provider string `json:"provider" table:"PROVIDER"`
	// State is what the agent does: "idle", "working" or
	// "waiting_for_user". `agent wait` waits for it.
	State string `json:"state,omitempty" table:"STATE"`
	// Status is the lifecycle of the agent's process: "active",
	// "inactive", "starting" or "startup_failed".
	Status           string                  `json:"status" table:"STATUS"`
	Model            string                  `json:"model,omitempty" table:"MODEL"`
	Effort           string                  `json:"effort,omitempty"`
	PermissionMode   string                  `json:"permission_mode,omitempty"`
	Settings         []agentSetting          `json:"settings,omitempty"`
	WorkerID         string                  `json:"worker_id"`
	Worker           string                  `json:"worker_name,omitempty" table:"WORKER"`
	WorkspaceID      string                  `json:"workspace_id,omitempty" table:"WORKSPACE,short"`
	Branch           string                  `json:"branch,omitempty" table:"BRANCH"`
	WorkingDir       string                  `json:"working_dir,omitempty"`
	SessionID        string                  `json:"session_id,omitempty"`
	ParentAgentID    string                  `json:"parent_agent_id,omitempty"`
	RootAgentID      string                  `json:"root_agent_id,omitempty"`
	SpawnSpanID      string                  `json:"spawn_span_id,omitempty"`
	AcceptsMessages  bool                    `json:"accepts_messages,omitempty"`
	AcceptsInterrupt bool                    `json:"accepts_interrupt,omitempty"`
	BackgroundTasks  int32                   `json:"background_tasks,omitempty"`
	CreatedAt        string                  `json:"created_at,omitempty"`
	ClosedAt         string                  `json:"closed_at,omitempty"`
	StartupError     string                  `json:"startup_error,omitempty"`
	StartupMessage   string                  `json:"startup_message,omitempty"`
	StartupPrompt    *agentStartupPromptView `json:"startup_prompt,omitempty"`
	Git              *gitStatus              `json:"git,omitempty"`
	Options          optionCatalog           `json:"options,omitempty"`
}

// agentRowOf projects an AgentInfo. withOptions includes the whole option
// catalog, which runs to hundreds of lines: `agent get` includes it only when
// --options is passed.
func agentRowOf(a *leapmuxv1.AgentInfo, withOptions bool) agentRow {
	groups := a.GetOptionGroups()
	git := gitStatusOf(a.GetGitStatus())
	branch := ""
	if git != nil {
		branch = git.Branch
	}
	row := agentRow{
		ID:               a.GetId(),
		Title:            a.GetTitle(),
		Provider:         agentlabels.CLIAlias(a.GetAgentProvider()),
		State:            enumToken(a.GetActivityState()),
		Status:           enumToken(a.GetStatus()),
		Model:            optionids.CurrentValue(groups, optionids.Model),
		Effort:           optionids.CurrentValue(groups, optionids.Effort),
		PermissionMode:   optionids.CurrentValue(groups, optionids.PermissionMode),
		WorkerID:         a.GetWorkerId(),
		Branch:           branch,
		WorkingDir:       a.GetWorkingDir(),
		SessionID:        a.GetAgentSessionId(),
		ParentAgentID:    a.GetParentAgentId(),
		RootAgentID:      a.GetRootAgentId(),
		SpawnSpanID:      a.GetSpawnSpanId(),
		AcceptsMessages:  a.GetAcceptsMessages(),
		AcceptsInterrupt: a.GetAcceptsInterrupt(),
		BackgroundTasks:  a.GetActiveBackgroundTasks(),
		CreatedAt:        a.GetCreatedAt(),
		ClosedAt:         a.GetClosedAt(),
		StartupError:     a.GetStartupError(),
		StartupMessage:   a.GetStartupMessage(),
		StartupPrompt:    agentStartupPromptOf(a.GetStartupPrompt()),
		Git:              git,
	}
	if withOptions {
		row.Options = optionGroupsOf(groups)
	}
	return row
}
