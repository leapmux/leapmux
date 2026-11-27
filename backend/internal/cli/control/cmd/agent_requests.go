package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/semanticevents"
)

// The kinds of a control request, as `agent requests` prints them.
const (
	controlKindPermission  = "permission"
	controlKindPlan        = "plan"
	controlKindElicitation = "elicitation"
	// controlKindUnreadable is a request whose payload the Worker cannot
	// read. It takes no answer.
	controlKindUnreadable = "unreadable"
	// controlKindUnknown is a prompt of a kind that this CLI does not know,
	// from a Worker of another version.
	controlKindUnknown = "unknown"
)

// controlPromptKind returns the kind token of prompt.
func controlPromptKind(prompt *leapmuxv1.ControlPrompt) string {
	return promptKindToken(semanticevents.PromptKind(prompt))
}

func promptKindToken(kind leapmuxv1.SemanticControlPromptKind) string {
	if token := enumToken(kind); token != "" {
		return token
	}
	return controlKindUnknown
}

// controlRequestRow is one pending control request of an agent: a
// permission prompt, a plan to approve, questions, an MCP elicitation form,
// or an extension's dialog. Exactly one of the prompt fields is set, as Kind
// states.
type controlRequestRow struct {
	// ID is the request's handle: the claim token of this request instance.
	// `agent approve`, `agent deny` and `agent answer` take it, or a unique
	// prefix of it. A request that the agent replaced has a new handle, so an
	// answer cannot reach a request that the caller did not read.
	ID      string `json:"id"`
	AgentID string `json:"agent_id"`
	// RequestID is the provider's own id of the request.
	RequestID string `json:"request_id"`
	// Kind is "permission", "plan", "question", "elicitation", "dialog" or
	// "unreadable".
	Kind string `json:"kind"`
	// Blocking reports whether the agent waits for the answer. An agent goes
	// on working beside a request that does not block it.
	Blocking bool `json:"blocking"`
	// State is "ready" for a request that has no answer yet. Any other state
	// is the delivery of an answer that the Worker already holds: "pending",
	// "uncertain", "delivered", "completed" or "canceled".
	State       string           `json:"state"`
	Permission  *permissionView  `json:"permission,omitempty"`
	Plan        *planView        `json:"plan,omitempty"`
	Questions   []questionView   `json:"questions,omitempty"`
	Elicitation *elicitationView `json:"elicitation,omitempty"`
	Dialog      *dialogView      `json:"dialog,omitempty"`
	// Fault says why LeapMux cannot read an unreadable request: "malformed"
	// (not JSON), "not_an_object", or "unrecognized" (a JSON object that the
	// agent's provider does not recognize as a request).
	Fault string `json:"fault,omitempty"`
}

// permissionView asks whether one call may run.
type permissionView struct {
	Title            string `json:"title,omitempty"`
	Reason           string `json:"reason,omitempty"`
	Text             string `json:"text,omitempty"`
	Command          string `json:"command,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	// Input is the call's arguments.
	Input   json.RawMessage        `json:"input,omitempty"`
	Options []permissionOptionView `json:"options"`
}

// permissionOptionView is one answer to a permission prompt. `agent answer
// --option <n>` chooses it.
type permissionOptionView struct {
	N  int    `json:"n"`
	ID string `json:"id"`
	// Kind is "allow_once", "allow_always", "reject_once", "reject_always"
	// or "other".
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	// Scope is how far a remembered answer ("allow_always" or
	// "reject_always") reaches: "session", "workspace", "project" or "user",
	// when the provider states it.
	Scope string `json:"scope,omitempty"`
	// ScopeLabel is the provider's own word for the scope, such as Codex's
	// "Command rule" or "Host rule". It tells apart two remembered answers of
	// one scope that keep different rules.
	ScopeLabel string `json:"scope_label,omitempty"`
	// EndsTurn reports a refusal that also ends the agent's turn, rather than
	// refusing only this call.
	EndsTurn bool `json:"ends_turn,omitempty"`
}

// planView asks to approve a plan.
type planView struct {
	Text        string               `json:"text,omitempty"`
	Permissions []planPermissionView `json:"permissions,omitempty"`
	Details     []string             `json:"details,omitempty"`
	// Choices are the answers that the runtime offers beside a plain
	// approval and refusal. `agent answer --option <n>` chooses one.
	Choices []planChoiceView `json:"choices,omitempty"`
}

type planPermissionView struct {
	Tool   string `json:"tool"`
	Prompt string `json:"prompt"`
}

type planChoiceView struct {
	N           int    `json:"n"`
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	// Effect is what the choice does with the plan: "approve", "refuse", or
	// "other" for a choice such as saving the plan for later.
	Effect string `json:"effect"`
}

// questionView is one question. `agent answer` answers every question of a
// request at once.
type questionView struct {
	N      int    `json:"n"`
	ID     string `json:"id,omitempty"`
	Header string `json:"header,omitempty"`
	// Context is text that the question shows above itself.
	Context  string               `json:"context,omitempty"`
	Question string               `json:"question"`
	Options  []questionOptionView `json:"options"`
	// InitialSelections are the values of the options that the agent selected
	// for the question. An empty list is an answer of no option. `agent answer`
	// sends them for a question that its command line does not answer.
	InitialSelections *[]string `json:"initial_selections,omitempty"`
	// MultiSelect allows any number of options.
	MultiSelect bool `json:"multi_select"`
	// TypedAnswer allows a typed answer.
	TypedAnswer bool `json:"typed_answer"`
	// TypedWithOptions allows a typed answer beside chosen options. A
	// multiple-choice question always allows it, and the answer then holds
	// the options and the text.
	TypedWithOptions bool `json:"typed_with_options"`
	// AllowEmpty allows no answer at all.
	AllowEmpty        bool   `json:"allow_empty"`
	MinimumSelections *int32 `json:"minimum_selections,omitempty"`
	MaximumSelections *int32 `json:"maximum_selections,omitempty"`
}

type questionOptionView struct {
	N int `json:"n"`
	// Value is what the answer sends for the option.
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Preview     string `json:"preview,omitempty"`
}

// elicitationView is a form that an MCP server asks the user to fill in.
type elicitationView struct {
	// Purpose is "form", or "permission" for a form that asks to approve a
	// call.
	Purpose     string `json:"purpose"`
	Mode        string `json:"mode,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Message     string `json:"message,omitempty"`
	Server      string `json:"server,omitempty"`
	URL         string `json:"url,omitempty"`
	// Schema is the JSON Schema of the form. `agent answer --field` fills
	// its properties.
	Schema         json.RawMessage `json:"schema,omitempty"`
	Arguments      json.RawMessage `json:"arguments,omitempty"`
	ArgumentNotice string          `json:"argument_notice,omitempty"`
	// AcceptChoices are the accept answers that the server offers beside a
	// plain accept. `agent answer --option <n>` chooses one.
	AcceptChoices []acceptChoiceView `json:"accept_choices,omitempty"`
	// FieldRules state the fields of the schema that the form keeps hidden,
	// and the fields that it asks only under conditions.
	FieldRules []fieldRuleView `json:"field_rules,omitempty"`
}

type acceptChoiceView struct {
	N     int    `json:"n"`
	Key   string `json:"key"`
	Label string `json:"label"`
}

// dialogView is a dialog that an extension opened through the runtime.
type dialogView struct {
	// Variant is "confirm", "input" or "editor".
	Variant     string `json:"variant"`
	Title       string `json:"title,omitempty"`
	Message     string `json:"message,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Prefill     string `json:"prefill,omitempty"`
	// TimeoutMS is how long the runtime waits before it answers itself.
	TimeoutMS *int64 `json:"timeout_ms,omitempty"`
}

// controlRequestRowOf projects one request of the agent agentID.
func controlRequestRowOf(agentID string, r *leapmuxv1.AgentControlRequest) controlRequestRow {
	prompt := r.GetPrompt()
	row := controlRequestRow{
		ID:        r.GetClaimToken(),
		AgentID:   agentID,
		RequestID: r.GetRequestId(),
		Kind:      controlPromptKind(prompt),
		Blocking:  !r.GetNonBlocking(),
		State:     controlResponseStateToken(r.GetResponseState()),
	}
	switch kind := prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Permission:
		row.Permission = permissionViewOf(kind.Permission)
	case *leapmuxv1.ControlPrompt_Plan:
		row.Plan = planViewOf(kind.Plan)
	case *leapmuxv1.ControlPrompt_Question:
		row.Questions = questionViewsOf(kind.Question)
	case *leapmuxv1.ControlPrompt_Elicitation:
		row.Elicitation = elicitationViewOf(kind.Elicitation)
	case *leapmuxv1.ControlPrompt_Dialog:
		row.Dialog = dialogViewOf(kind.Dialog)
	case *leapmuxv1.ControlPrompt_Unreadable:
		row.Fault = enumToken(kind.Unreadable)
	}
	return row
}

// controlResponseStateToken is the token of a response state. The Worker
// states one for every listed request, so an unstated one prints
// "unknown", never "".
func controlResponseStateToken(state leapmuxv1.ControlResponseState) string {
	if token := enumToken(state); token != "" {
		return token
	}
	return agentStateUnknown
}

// jsonText returns text as raw JSON for a result, or nil for empty text.
// Text that is not JSON becomes a JSON string, so a result never carries
// invalid JSON and still shows what arrived.
func jsonText(text string) json.RawMessage {
	if text == "" {
		return nil
	}
	if json.Valid([]byte(text)) {
		return json.RawMessage(text)
	}
	quoted, _ := json.Marshal(text)
	return quoted
}

func permissionViewOf(p *leapmuxv1.PermissionPrompt) *permissionView {
	view := &permissionView{
		Title:            p.GetTitle(),
		Reason:           p.GetReason(),
		Text:             p.GetText(),
		Command:          p.GetCommand(),
		WorkingDirectory: p.GetWorkingDirectory(),
		Input:            jsonText(p.GetInputJson()),
		Options:          make([]permissionOptionView, 0, len(p.GetOptions())),
	}
	for i, option := range p.GetOptions() {
		view.Options = append(view.Options, permissionOptionView{
			N:          i + 1,
			ID:         option.GetOptionId(),
			Kind:       permissionOptionKindToken(option.GetKind()),
			Name:       option.GetName(),
			Scope:      enumToken(option.GetScope()),
			ScopeLabel: option.GetScopeLabel(),
			EndsTurn:   option.GetEndsTurn(),
		})
	}
	return view
}

// permissionOptionKindToken is the token of an option kind. An option of no
// stated kind is "other", as the Worker states a kind outside the four.
func permissionOptionKindToken(kind leapmuxv1.PermissionOptionKind) string {
	if token := enumToken(kind); token != "" {
		return token
	}
	return enumToken(leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_OTHER)
}

// planChoiceEffectToken is the token of a choice's effect. A choice of no
// stated effect is "other": nothing says what it does with the plan.
func planChoiceEffectToken(effect leapmuxv1.PlanChoiceEffect) string {
	if token := enumToken(effect); token != "" {
		return token
	}
	return enumToken(leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_OTHER)
}

func planViewOf(p *leapmuxv1.PlanPrompt) *planView {
	view := &planView{Text: p.GetText(), Details: p.GetDetails()}
	for _, permission := range p.GetPermissions() {
		view.Permissions = append(view.Permissions, planPermissionView{Tool: permission.GetTool(), Prompt: permission.GetPrompt()})
	}
	for i, choice := range p.GetChoices() {
		view.Choices = append(view.Choices, planChoiceView{
			N: i + 1, ID: choice.GetId(), Label: choice.GetLabel(), Description: choice.GetDescription(), Effect: planChoiceEffectToken(choice.GetEffect()),
		})
	}
	return view
}

func questionViewsOf(p *leapmuxv1.QuestionPrompt) []questionView {
	views := make([]questionView, 0, len(p.GetQuestions()))
	for i, q := range p.GetQuestions() {
		typed := q.AllowCustomText == nil || q.GetAllowCustomText()
		view := questionView{
			N:                 i + 1,
			ID:                q.GetId(),
			Header:            q.GetHeader(),
			Context:           q.GetContext(),
			Question:          q.GetQuestion(),
			Options:           make([]questionOptionView, 0, len(q.GetOptions())),
			MultiSelect:       q.GetMultiSelect(),
			TypedAnswer:       typed,
			TypedWithOptions:  typed && (q.GetMultiSelect() || p.GetPreservesSelectionNotes()),
			AllowEmpty:        q.GetAllowEmpty(),
			MinimumSelections: q.MinimumSelections,
			MaximumSelections: q.MaximumSelections,
		}
		if initial := q.GetInitialSelections(); initial != nil {
			values := append([]string{}, initial.GetValues()...)
			view.InitialSelections = &values
		}
		for j, option := range q.GetOptions() {
			value := option.GetLabel()
			if option.Value != nil {
				value = option.GetValue()
			}
			view.Options = append(view.Options, questionOptionView{
				N: j + 1, Value: value, Label: option.GetLabel(), Description: option.GetDescription(), Preview: option.GetPreview(),
			})
		}
		views = append(views, view)
	}
	return views
}

func elicitationViewOf(e *leapmuxv1.ElicitationPrompt) *elicitationView {
	purpose := "form"
	if e.GetPurpose() == leapmuxv1.ElicitationPurpose_ELICITATION_PURPOSE_PERMISSION {
		purpose = enumToken(e.GetPurpose())
	}
	view := &elicitationView{
		Purpose:        purpose,
		Mode:           e.GetMode(),
		Title:          e.GetTitle(),
		Description:    e.GetDescription(),
		Message:        e.GetMessage(),
		Server:         e.GetServer(),
		URL:            e.GetUrl(),
		Schema:         jsonText(e.GetSchemaJson()),
		Arguments:      jsonText(e.GetArgumentsJson()),
		ArgumentNotice: e.GetArgumentNotice(),
	}
	for i, choice := range e.GetAcceptChoices() {
		view.AcceptChoices = append(view.AcceptChoices, acceptChoiceView{N: i + 1, Key: choice.GetKey(), Label: choice.GetLabel()})
	}
	if len(e.GetFieldRules()) > 0 {
		view.FieldRules = fieldRuleViewsOf(e.GetFieldRules())
	}
	return view
}

func dialogViewOf(d *leapmuxv1.DialogPrompt) *dialogView {
	variant := enumToken(d.GetVariant())
	if variant == "" {
		// The browser draws a dialog of no stated variant as a confirm.
		variant = enumToken(leapmuxv1.DialogVariant_DIALOG_VARIANT_CONFIRM)
	}
	return &dialogView{
		Variant:     variant,
		Title:       d.GetTitle(),
		Message:     d.GetMessage(),
		Placeholder: d.GetPlaceholder(),
		Prefill:     d.GetPrefill(),
		TimeoutMS:   d.TimeoutMs,
	}
}

// controlRequestList is the result of `agent requests`.
type controlRequestList struct {
	Requests    []controlRequestRow `json:"requests"`
	multiTarget bool
}

// QuietLines prints one request handle per line. When listing across multiple
// targets (or all agents), each line is "<agent_id>\t<handle>" so scripts can
// pass both tokens consistently to `agent approve`.
func (l controlRequestList) QuietLines() []string {
	if len(l.Requests) == 0 {
		return nil
	}
	multiAgent := l.multiTarget
	if !multiAgent {
		firstAgent := l.Requests[0].AgentID
		for _, r := range l.Requests[1:] {
			if r.AgentID != firstAgent {
				multiAgent = true
				break
			}
		}
	}
	out := make([]string, 0, len(l.Requests))
	for _, r := range l.Requests {
		if multiAgent {
			out = append(out, r.AgentID+"\t"+r.ID)
		} else {
			out = append(out, r.ID)
		}
	}
	return out
}

// runAgentRequests lists the pending requests of the given agents, or of
// every agent when none is given.
func runAgentRequests(inv *cli.Invocation) (any, error) {
	inputs := inv.Variadic("agent")
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	targets, err := requestTargets(s, inputs)
	if err != nil {
		return nil, err
	}
	groups, agentIDs := groupAgentsByWorker(targets)
	listed := listControlRequests(s.ctx, s.workerChannelOpener(), groups)
	singleExplicit := len(inputs) == 1 && !strings.EqualFold(strings.TrimSpace(inputs[0]), "children")
	result := controlRequestList{Requests: []controlRequestRow{}, multiTarget: !singleExplicit}
	var failures []error
	for _, id := range agentIDs {
		got := listed[id]
		if got.err != nil {
			failures = append(failures, got.err)
			continue
		}
		result.Requests = append(result.Requests, got.rows...)
	}
	return reportControlRequests(inv.Printer, result, failures, len(agentIDs))
}

// listWorkspaceAgentTargets lists every agent tab in the session's workspace
// scope that has a hosting Worker.
func (s *session) listWorkspaceAgentTargets() ([]tabTarget, error) {
	tabs, err := s.listWorkspaceTabs(leapmuxv1.TabType_TAB_TYPE_AGENT)
	if err != nil {
		return nil, err
	}
	var targets []tabTarget
	for _, t := range tabs {
		// A tab that no Worker hosts has no agent to ask.
		if t.GetWorkerId() != "" {
			targets = append(targets, tabTarget{ID: t.GetTab().GetTabId(), Type: leapmuxv1.TabType_TAB_TYPE_AGENT, WorkerID: t.GetWorkerId()})
		}
	}
	return targets, nil
}

// workerChannelOpener adapts withWorkerChannel to workerChannelFunc.
func (s *session) workerChannelOpener() workerChannelFunc {
	return func(workerID string, body func(call workerCaller) error) error {
		return withWorkerChannel(s.ctx, s.c, workerID, func(w workerCall) error {
			return body(func(method string, in, out proto.Message) error { return w.Call(s.ctx, method, in, out) })
		})
	}
}

// listAgentControlRequestProtos queries every agent in targets for its raw
// pending control request protos, returning per-agent results and the ordered
// agent IDs.
func (s *session) listAgentControlRequestProtos(targets []tabTarget) (map[string]listedRequestProtos, []string) {
	groups, agentIDs := groupAgentsByWorker(targets)
	return listControlRequestProtos(s.ctx, s.workerChannelOpener(), groups), agentIDs
}

// requestTargets resolves the agents of `agent requests`. With no input it
// lists every agent tab, of one workspace when --workspace gives one.
// "children" expands to all direct child subagents of the calling agent.
func requestTargets(s *session, inputs []string) ([]tabTarget, error) {
	if len(inputs) > 0 {
		return s.resolveVariadicAgentTargets(inputs, resolve.Need{WorkerID: true})
	}
	return s.listWorkspaceAgentTargets()
}

// reportControlRequests returns the result of `agent requests`. When every
// agent failed, the first failure is the result. Otherwise the result
// prints, each failure is a notice, and the exit status is the first
// failure's, so a script never reads a partial list as a complete one.
func reportControlRequests(p *cli.Printer, result controlRequestList, failures []error, agents int) (any, error) {
	if len(failures) == 0 {
		if len(result.Requests) == 0 && p.Format == cli.FormatText {
			_ = p.Notice("no agent waits for an answer")
		}
		return result, nil
	}
	if len(failures) == agents {
		return nil, failures[0]
	}
	if err := p.Result(result); err != nil {
		return nil, err
	}
	for _, failure := range failures {
		_ = p.Notice("%s", cli.FromError(failure).Message)
	}
	return nil, &cli.ExitStatus{Code: cli.FromError(failures[0]).ExitCode()}
}

// listedRequests is what one agent's listing gave: its projected requests, or
// why it failed.
type listedRequests struct {
	rows []controlRequestRow
	err  error
}

// listedRequestProtos is what one agent's listing gave in raw proto form, or
// why it failed.
type listedRequestProtos struct {
	requests []*leapmuxv1.AgentControlRequest
	err      error
}

// workerChannelFunc runs body with a caller of one Worker. It is the seam of
// listControlRequestProtos: production opens the Worker's channel, and a test
// answers the calls itself.
type workerChannelFunc func(workerID string, body func(call workerCaller) error) error

// listControlRequestProtos asks each Worker for the raw pending requests of
// its agents. Every Worker runs at once, and so does every agent of one
// Worker: the Worker's channel carries concurrent calls. A failure belongs to
// its agent, so the other agents still list.
func listControlRequestProtos(ctx context.Context, open workerChannelFunc, groups []workerAgents) map[string]listedRequestProtos {
	var mu sync.Mutex
	out := map[string]listedRequestProtos{}
	record := func(agentID string, got listedRequestProtos) {
		mu.Lock()
		defer mu.Unlock()
		out[agentID] = got
	}
	var workers errgroup.Group
	workers.SetLimit(detailFanoutLimit)
	for _, group := range groups {
		workers.Go(func() error {
			err := open(group.workerID, func(call workerCaller) error {
				var agents errgroup.Group
				agents.SetLimit(detailFanoutLimit)
				for _, agentID := range group.agentIDs {
					agents.Go(func() error {
						var resp leapmuxv1.ListControlRequestsResponse
						if err := call(leapmuxv1connect.WorkerAgentServiceListControlRequestsProcedure, &leapmuxv1.ListControlRequestsRequest{AgentId: agentID}, &resp); err != nil {
							record(agentID, listedRequestProtos{err: fmt.Errorf("list the requests of agent %s: %w", agentID, err)})
							return nil
						}
						record(agentID, listedRequestProtos{requests: resp.GetRequests()})
						return nil
					})
				}
				return agents.Wait()
			})
			if err != nil {
				// The channel did not open: every agent of the Worker fails
				// with it.
				for _, agentID := range group.agentIDs {
					record(agentID, listedRequestProtos{err: fmt.Errorf("list the requests of agent %s: %w", agentID, err)})
				}
			}
			return nil
		})
	}
	_ = workers.Wait()
	if ctx.Err() != nil {
		for _, group := range groups {
			for _, agentID := range group.agentIDs {
				if _, ok := out[agentID]; !ok {
					out[agentID] = listedRequestProtos{err: errors.Join(fmt.Errorf("list the requests of agent %s", agentID), context.Cause(ctx))}
				}
			}
		}
	}
	return out
}

// listControlRequests asks each Worker for the pending requests of its agents
// and projects each request into a controlRequestRow.
func listControlRequests(ctx context.Context, open workerChannelFunc, groups []workerAgents) map[string]listedRequests {
	raw := listControlRequestProtos(ctx, open, groups)
	out := make(map[string]listedRequests, len(raw))
	for agentID, got := range raw {
		if got.err != nil {
			out[agentID] = listedRequests{err: got.err}
			continue
		}
		rows := make([]controlRequestRow, 0, len(got.requests))
		for _, r := range got.requests {
			rows = append(rows, controlRequestRowOf(agentID, r))
		}
		out[agentID] = listedRequests{rows: rows}
	}
	return out
}
