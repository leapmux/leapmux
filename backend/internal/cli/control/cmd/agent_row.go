package cmd

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/util/optionids"
)

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
	State string `json:"state,omitempty" table:"STATE,state"`
	// Status is the lifecycle of the agent's process: "active",
	// "inactive", "starting" or "startup_failed".
	Status           string                  `json:"status" table:"STATUS,state"`
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
