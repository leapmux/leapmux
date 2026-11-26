package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

func newAgentGoalCommand() *cobra.Command {
	return cli.Group("goal", "Read, set, pause, resume, or clear an agent's standing session goal",
		cli.NewLeaf(cli.Leaf{
			Use:              "get [agent]",
			Short:            "Show an agent's current standing goal (default: this agent)",
			QuietUnsupported: true,
			Examples: []cli.Example{
				{Command: "leapmux agent goal get"},
				{Command: "leapmux agent goal get 87a19d5f"},
			},
			Flags:    addTargetWorkspaceFlag,
			Complete: completeTargets,
			Run:      runAgentGoalGet,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "set <agent> <objective>",
			Short:   "Set a standing session goal on a running agent",
			Mutates: true,
			Args:    map[string]string{"objective": "The standing goal objective text."},
			Examples: []cli.Example{
				{Command: `leapmux agent goal set 87a19d5f "Fix all failing tests and keep CI green"`},
			},
			Flags:    addTargetWorkspaceFlag,
			Complete: completeTargets,
			Run: func(inv *cli.Invocation) (any, error) {
				obj := strings.TrimSpace(inv.Arg("objective"))
				if obj == "" {
					return nil, cli.Usagef("the objective is empty")
				}
				return runAgentGoalUpdate(inv, leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_SET, obj)
			},
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "pause <agent>",
			Short:   "Pause an agent's active standing goal",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux agent goal pause 87a19d5f"},
			},
			Flags:    addTargetWorkspaceFlag,
			Complete: completeTargets,
			Run: func(inv *cli.Invocation) (any, error) {
				return runAgentGoalUpdate(inv, leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_PAUSE, "")
			},
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "resume <agent>",
			Short:   "Resume a paused standing goal",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux agent goal resume 87a19d5f"},
			},
			Flags:    addTargetWorkspaceFlag,
			Complete: completeTargets,
			Run: func(inv *cli.Invocation) (any, error) {
				return runAgentGoalUpdate(inv, leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_RESUME, "")
			},
		}),
		cli.NewLeaf(cli.Leaf{
			Use:         "clear <agent>",
			Short:       "Clear an agent's standing goal",
			Mutates:     true,
			Destructive: true,
			Examples: []cli.Example{
				{Command: "leapmux agent goal clear 87a19d5f"},
			},
			Flags:    addTargetWorkspaceFlag,
			Complete: completeTargets,
			Run: func(inv *cli.Invocation) (any, error) {
				return runAgentGoalUpdate(inv, leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_CLEAR, "")
			},
		}),
	)
}

type agentGoalView struct {
	AgentID          string   `json:"agent_id"`
	Objective        string   `json:"objective,omitempty"`
	Status           string   `json:"status,omitempty"`
	StatusDetail     string   `json:"status_detail,omitempty"`
	CreatedAt        string   `json:"created_at,omitempty"`
	UpdatedAt        string   `json:"updated_at,omitempty"`
	SupportedActions []string `json:"supported_actions,omitempty"`
}

func runAgentGoalGet(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	var resp leapmuxv1.ListAgentMessagesResponse
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure,
		&leapmuxv1.ListAgentMessagesRequest{
			AgentId: got.ID,
			Anchor:  leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST,
			Limit:   1,
		}, &resp); err != nil {
		return nil, err
	}
	snap := resp.GetGoal()
	view := agentGoalView{AgentID: got.ID}
	if snap != nil {
		view.UpdatedAt = snap.GetGoalUpdatedAt()
		for _, act := range snap.GetSupportedActions() {
			if tok := enumToken(act); tok != "" {
				view.SupportedActions = append(view.SupportedActions, tok)
			}
		}
		if g := snap.GetGoal(); g != nil {
			view.Objective = g.GetObjective()
			view.Status = enumToken(g.GetStatus())
			view.StatusDetail = g.GetStatusDetail()
			view.CreatedAt = g.GetCreatedAt()
		}
	}
	return view, nil
}

type agentGoalActionResult struct {
	ID        string `json:"id"`
	Action    string `json:"action"`
	Objective string `json:"objective,omitempty"`
}

func runAgentGoalUpdate(inv *cli.Invocation, action leapmuxv1.AgentGoalAction, objective string) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceUpdateAgentGoalProcedure,
		&leapmuxv1.UpdateAgentGoalRequest{AgentId: got.ID, Action: action, Objective: objective}, nil); err != nil {
		return nil, err
	}
	return agentGoalActionResult{ID: got.ID, Action: enumToken(action), Objective: objective}, nil
}

// --- agent todos ---

func newAgentTodosCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "todos [agent]",
		Short: "List an agent's current to-do items (default: this agent)",
		Examples: []cli.Example{
			{Command: "leapmux agent todos"},
			{Command: "leapmux agent todos 87a19d5f"},
		},
		Flags:    addTargetWorkspaceFlag,
		Complete: completeTargets,
		Run:      runAgentTodos,
	})
}

type agentTodoRow struct {
	ID          string `json:"id,omitempty" table:"ID"`
	Status      string `json:"status" table:"STATUS,state"`
	Content     string `json:"content" table:"CONTENT"`
	ActiveForm  string `json:"active_form,omitempty" table:"ACTIVE"`
	Description string `json:"description,omitempty"`
}

type agentTodoList struct {
	Todos []agentTodoRow `json:"todos"`
}

func (l agentTodoList) QuietLines() []string {
	out := make([]string, 0, len(l.Todos))
	for _, t := range l.Todos {
		if t.ID != "" {
			out = append(out, t.ID)
		} else {
			out = append(out, t.Content)
		}
	}
	return out
}

func runAgentTodos(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	var resp leapmuxv1.ListAgentMessagesResponse
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure,
		&leapmuxv1.ListAgentMessagesRequest{
			AgentId: got.ID,
			Anchor:  leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST,
			Limit:   1,
		}, &resp); err != nil {
		return nil, err
	}
	items := resp.GetTodos().GetTodos()
	rows := make([]agentTodoRow, 0, len(items))
	for _, item := range items {
		rows = append(rows, agentTodoRow{
			ID:          item.GetId(),
			Status:      enumToken(item.GetStatus()),
			Content:     item.GetContent(),
			ActiveForm:  item.GetActiveForm(),
			Description: item.GetDescription(),
		})
	}
	return agentTodoList{Todos: rows}, nil
}

// --- agent tasks ---

func newAgentTasksCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "tasks [agent]",
		Short: "List an agent's background tasks and subagent runs (default: this agent)",
		Examples: []cli.Example{
			{Command: "leapmux agent tasks"},
			{Command: "leapmux agent tasks 87a19d5f --active"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.Bool("active", false, "show only running or pending background tasks")
			addTargetWorkspaceFlag(fs)
		},
		Complete: completeTargets,
		Run:      runAgentTasks,
	})
}

type agentTaskRow struct {
	ID            string `json:"id" table:"ID,short"`
	Kind          string `json:"kind" table:"KIND"`
	Status        string `json:"status" table:"STATUS,state"`
	Title         string `json:"title" table:"TITLE"`
	ChildAgentID  string `json:"child_agent_id,omitempty" table:"CHILD,short"`
	GroupLabel    string `json:"group_label,omitempty" table:"GROUP"`
	ParentAgentID string `json:"parent_agent_id,omitempty"`
	ActiveForm    string `json:"active_form,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	EndedAt       string `json:"ended_at,omitempty"`
}

type agentTaskList struct {
	Tasks []agentTaskRow `json:"tasks"`
}

func (l agentTaskList) QuietLines() []string {
	out := make([]string, 0, len(l.Tasks))
	for _, t := range l.Tasks {
		if t.ChildAgentID != "" {
			out = append(out, t.ChildAgentID)
		} else {
			out = append(out, t.ID)
		}
	}
	return out
}

func runAgentTasks(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	var resp leapmuxv1.ListAgentMessagesResponse
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure,
		&leapmuxv1.ListAgentMessagesRequest{
			AgentId: got.ID,
			Anchor:  leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST,
			Limit:   1,
		}, &resp); err != nil {
		return nil, err
	}
	activeOnly := boolFlag(inv, "active")
	items := resp.GetBackgroundTasks().GetTasks()
	rows := make([]agentTaskRow, 0, len(items))
	for _, item := range items {
		if activeOnly && item.GetStatus() != leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING &&
			item.GetStatus() != leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_PENDING {
			continue
		}
		rows = append(rows, agentTaskRow{
			ID:            item.GetId(),
			Kind:          enumToken(item.GetKind()),
			Status:        enumToken(item.GetStatus()),
			Title:         item.GetTitle(),
			ChildAgentID:  item.GetChildAgentId(),
			GroupLabel:    item.GetGroupLabel(),
			ParentAgentID: item.GetParentAgentId(),
			ActiveForm:    item.GetActiveForm(),
			CreatedAt:     item.GetCreatedAt(),
			EndedAt:       item.GetEndedAt(),
		})
	}
	return agentTaskList{Tasks: rows}, nil
}

// --- agent usage ---

func newAgentUsageCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              "usage [agent]",
		Short:            "Show an agent's live cost, context usage, rate limits, and running tool (default: this agent)",
		QuietUnsupported: true,
		Examples: []cli.Example{
			{Command: "leapmux agent usage"},
			{Command: "leapmux agent usage 87a19d5f --json"},
		},
		Flags:    addTargetWorkspaceFlag,
		Complete: completeTargets,
		Run:      runAgentUsage,
	})
}

type agentUsageView struct {
	AgentID      string         `json:"agent_id"`
	TotalCost    *float64       `json:"total_cost_usd,omitempty"`
	Context      any            `json:"context_usage,omitempty"`
	RateLimits   any            `json:"rate_limits,omitempty"`
	RunningTool  any            `json:"running_tool,omitempty"`
	GoalProgress any            `json:"goal_progress,omitempty"`
	RetryOffer   any            `json:"turn_retry_offer,omitempty"`
	SessionInfo  map[string]any `json:"session_info,omitempty"`
}

func (v agentUsageView) RenderText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "agent_id: %s\n", cli.SanitizeSingleLine(v.AgentID)); err != nil {
		return err
	}
	if v.TotalCost != nil {
		if _, err := fmt.Fprintf(w, "cost:     $%.4f\n", *v.TotalCost); err != nil {
			return err
		}
	}
	if ctxMap, ok := v.Context.(map[string]any); ok {
		pct, hasPct := asFloat64(ctxMap[contracts.ContextUsageFieldUsagePercent])
		used, hasUsed := asFloat64(ctxMap[contracts.ContextUsageFieldContextTokens])
		limit, hasLimit := asFloat64(ctxMap[contracts.ContextUsageFieldContextWindow])
		switch {
		case hasUsed && hasLimit && limit > 0:
			if !hasPct {
				pct = (used / limit) * 100
			}
			if _, err := fmt.Fprintf(w, "context:  %.0f / %.0f tokens (%.1f%%)\n", used, limit, pct); err != nil {
				return err
			}
		case hasPct:
			if _, err := fmt.Fprintf(w, "context:  %.1f%%\n", pct); err != nil {
				return err
			}
		}
	}
	if toolMap, ok := v.RunningTool.(map[string]any); ok {
		spanID, _ := toolMap[contracts.RunningToolFieldSpanId].(string)
		elapsed, hasElapsed := asFloat64(toolMap[contracts.RunningToolFieldElapsedSeconds])
		if spanID != "" {
			if hasElapsed {
				if _, err := fmt.Fprintf(w, "tool:     span %s (%.0fs)\n", cli.SanitizeSingleLine(spanID), elapsed); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprintf(w, "tool:     span %s\n", cli.SanitizeSingleLine(spanID)); err != nil {
					return err
				}
			}
		}
	}
	if limits := extractRateLimitRows(v.RateLimits); len(limits) > 0 {
		for _, rl := range limits {
			if _, err := fmt.Fprintf(w, "limit:    %s\n", cli.SanitizeSingleLine(rl)); err != nil {
				return err
			}
		}
	}
	if gp, ok := v.GoalProgress.(map[string]any); ok {
		var parts []string
		if iters, ok := asFloat64(gp[contracts.GoalProgressFieldIterations]); ok {
			parts = append(parts, fmt.Sprintf("%.0f iterations", iters))
		}
		if used, ok := asFloat64(gp[contracts.GoalProgressFieldTokensUsed]); ok {
			if budget, ok := asFloat64(gp[contracts.GoalProgressFieldTokenBudget]); ok && budget > 0 {
				parts = append(parts, fmt.Sprintf("%.0f/%.0f tokens", used, budget))
			} else {
				parts = append(parts, fmt.Sprintf("%.0f tokens", used))
			}
		}
		if secs, ok := asFloat64(gp[contracts.GoalProgressFieldTimeUsedSeconds]); ok {
			parts = append(parts, fmt.Sprintf("%.0fs", secs))
		}
		if len(parts) > 0 {
			if _, err := fmt.Fprintf(w, "goal:     %s\n", strings.Join(parts, ", ")); err != nil {
				return err
			}
		}
	}
	if ro, ok := v.RetryOffer.(map[string]any); ok {
		turnID, _ := ro[contracts.TurnRetryOfferFieldTurnId].(string)
		model, _ := ro[contracts.TurnRetryOfferFieldModel].(string)
		if turnID != "" || model != "" {
			if _, err := fmt.Fprintf(w, "retry:    turn=%s model=%s\n", cli.SanitizeSingleLine(turnID), cli.SanitizeSingleLine(model)); err != nil {
				return err
			}
		}
	}
	if len(v.SessionInfo) == 0 {
		_, err := fmt.Fprintln(w, "status:   no live usage metrics reported yet")
		return err
	}
	return nil
}

func asFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func extractRateLimitRows(raw any) []string {
	var entries []any
	switch v := raw.(type) {
	case []any:
		entries = v
	case map[string]any:
		switch vals := v[contracts.RateLimitUpdateFieldValues].(type) {
		case []any:
			entries = vals
		case map[string]any:
			keys := make([]string, 0, len(vals))
			for k := range vals {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				entries = append(entries, vals[k])
			}
		default:
			entries = []any{v}
		}
	default:
		return nil
	}
	var out []string
	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		label, _ := m[contracts.RateLimitFieldLabel].(string)
		if label == "" {
			label, _ = m[contracts.RateLimitFieldRateLimitType].(string)
		}
		status, _ := m[contracts.RateLimitFieldStatus].(string)
		util, hasUtil := asFloat64(m[contracts.RateLimitFieldUtilization])
		var parts []string
		if label != "" {
			parts = append(parts, label)
		}
		if status != "" {
			parts = append(parts, status)
		}
		if hasUtil {
			parts = append(parts, fmt.Sprintf("%.0f%%", util*100))
		}
		if len(parts) > 0 {
			out = append(out, strings.Join(parts, " "))
		}
	}
	return out
}

func runAgentUsage(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	var resp leapmuxv1.GetAgentSessionInfoResponse
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceGetAgentSessionInfoProcedure,
		&leapmuxv1.GetAgentSessionInfoRequest{AgentId: got.ID}, &resp); err != nil {
		return nil, err
	}
	info := map[string]any{}
	if raw := strings.TrimSpace(resp.GetSessionInfoJson()); raw != "" {
		_ = json.Unmarshal([]byte(raw), &info)
	}
	view := agentUsageView{
		AgentID:     got.ID,
		SessionInfo: info,
	}
	if cost, ok := info[contracts.SessionInfoKeyTotalCostUsd].(float64); ok {
		view.TotalCost = &cost
	}
	view.Context = info[contracts.SessionInfoKeyContextUsage]
	view.RateLimits = info[contracts.SessionInfoKeyRateLimits]
	view.RunningTool = info[contracts.SessionInfoKeyRunningTool]
	view.GoalProgress = info[contracts.SessionInfoKeyGoalProgress]
	view.RetryOffer = info[contracts.SessionInfoKeyTurnRetryOffer]
	return view, nil
}
