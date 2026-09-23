package cursor

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/tooltranscript"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const (
	ModeAgent = "agent"
	ModePlan  = "plan"
	ModeAsk   = "ask"

	cursorCLIModelAuto     = "auto"
	cursorCLIModelAutoWire = "default[]"
)

// Agent manages a single Cursor CLI ACP process.
type Agent struct {
	acp.Base

	// taskToolCalls holds the toolCallId of every `task` tool call the spawn
	// hook claimed. The closing hook needs it: Cursor reports "this ran in the
	// background" only in the final update's rawOutput, and the tool's identity
	// only in rawInput, which that update does not always carry. Without the
	// note, a backgrounded task and a backgrounded shell are the same wire
	// shape.
	//
	// Guarded by Base's Mu, which neither handleToolCall nor
	// handleToolCallUpdate holds while it calls a hook, so the hooks can take it.
	// An entry is dropped on the final update. A `task` call that never reaches
	// one keeps its entry -- one bool and one id -- for the life of the agent,
	// which matches how Base's subagentPrompts holds a spawn's prompt.
	taskToolCalls map[string]bool
	// taskReports joins the live cursor/task extension with the local-store
	// result. Cursor replays the store record but not the extension, so only a
	// state that saw both can publish a report. Guarded by Base.Mu and capped
	// in tool_transcript.go.
	taskReports map[string]cursorTaskReportState

	// transcript is the sink that configure installed, held under its own type so
	// the extension handler can reach EnrichToolSpan. The transcript is the single
	// writer of a row's supplemental content, and the `cursor/*` frames land on a
	// row that its store pass also enriches -- see EnrichToolSpan for what a second
	// writer would destroy. It is written once, in configure, before the reader
	// goroutine starts.
	transcript *tooltranscript.Transcript
}

// clearTaskToolCalls drops every note. ClearContext calls it: the notes are
// keyed by the OUTGOING session's tool-call ids, which send no closing update
// once that session is gone, and a new session that reuses an id would read a
// stale note and file a backgrounded shell as a subagent.
func (a *Agent) clearTaskToolCalls() {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	clear(a.taskToolCalls)
	clear(a.taskReports)
}

// rememberTaskToolCall notes that toolCallID is Cursor's `task` tool.
func (a *Agent) rememberTaskToolCall(toolCallID string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.taskToolCalls == nil {
		a.taskToolCalls = make(map[string]bool)
	}
	a.taskToolCalls[toolCallID] = true
}

// forgetTaskToolCall drops the note for toolCallID and reports whether one was
// there. The call is over when this runs, so the entry cannot accumulate.
func (a *Agent) forgetTaskToolCall(toolCallID string) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	was := a.taskToolCalls[toolCallID]
	delete(a.taskToolCalls, toolCallID)
	return was
}

// spawnObservation runs Cursor's spawn detector and remembers what it claimed,
// so the closing hook does not have to ask the wire a second time.
//
// A tool call that arrives ALREADY final gets no note. handleToolCall applies
// this observation and returns, so no closing update follows to drop one --
// a `session/load` replay of a finished task would leave an entry for the life
// of the agent, and a later call that reuses the id would read it and file a
// backgrounded shell as a subagent. The note has no reader on that path either:
// this observation already carries the kind and the title.
// A Cursor subagent's child transcript arrives in ONE piece, when the task ends:
// neither observation here carries a ChildTranscriptPayload, so nothing reaches
// the child tab between the spawn prompt and the final report. Goose streams by
// setting that field per update, and ZCode and Codex write into the child sink
// directly. Cursor's own wire format nests the child's updates inside the
// parent's `tool_call_delta`, so the shape exists; whether `cursor-agent`
// forwards it over ACP is untested. See
// https://github.com/leapmux/leapmux/issues/487.
func (a *Agent) spawnObservation(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	obs := cursorSubagentFromToolCall(tc)
	if obs != nil && !acp.StatusIsFinal(tc.Status) {
		a.rememberTaskToolCall(tc.ToolCallID)
	}
	return obs
}

// finishedObservation answers "was this the task tool" from the tool call
// itself, and falls back to the note the spawn left when this update carries no
// input of its own.
func (a *Agent) finishedObservation(tcu acp.ToolCallUpdateEnvelope) *acp.SubagentObservation {
	wasTaskTool := cursorToolCallIsTaskTool(tcu.RawInput)
	// The row is over on a final status, so drop the note whatever it said.
	if acp.StatusIsFinal(tcu.Status) && a.forgetTaskToolCall(tcu.ToolCallID) {
		wasTaskTool = true
	}
	return cursorSubagentFromToolCallUpdate(tcu, wasTaskTool)
}

// Start starts a Cursor CLI ACP agent process and performs the handshake.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	return acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration: Registration(),
		ProviderName: "cursor",
		BaseArgs:     []string{"acp"},
		NewAgent:     func() *Agent { return &Agent{} },
		Base:         func(a *Agent) *acp.Base { return &a.Base },
		Configure: func(a *Agent, sink agent.ProviderServices) acp.Hooks {
			storeQuery := agent.StoredSessionQuery{HomeDir: opts.HomeDir, WorkingDir: opts.WorkingDir}
			transcript := newCursorToolTranscriptWithObserver(
				ctx,
				sink,
				func() string { return cursorACPStorePath(storeQuery, a.CurrentSessionID()) },
				a.observeCursorTaskRecord,
			)
			a.transcript = transcript
			return acp.Hooks{
				Sink: transcript,
				// Cursor stores the normalized (display) model id, not the wire form. The
				// registration hands the same normalizeCursorModelID to NormalizeModelID, so
				// the offline-label and live paths can't diverge.
				InitialModel:      normalizeCursorModelID(opts.Model()),
				ModelIDNormalizer: normalizeCursorModelID,
				// Cursor writes models through setCursorModel (id -> wire form); the base
				// UpdateSettings / reapply / refresh use this via effectiveSetModel, so Cursor
				// needs no overrides of its own.
				ModelSetter:    a.setCursorModel,
				ModelDecorator: decorateCursorModel,
				ModeChannel:    acp.ModeChannelPermissionMode,
				ExtraMethod:    a.handleExtraMethod,
				// Cursor's Task tool supplies the prompt. Its local store supplies the
				// report that ACP omits. The tool-call id links both to one child.
				SubagentFromToolCall:       a.spawnObservation,
				SubagentFromToolCallUpdate: a.finishedObservation,
				ClearProviderState: func() {
					a.clearTaskToolCalls()
					transcript.Reset()
				},
			}
		},
		AfterHandshake: func(a *Agent, handshake *acp.SessionResult, opts agent.Options) error {
			return a.ApplyPermissionModeStartup(handshake, opts, ModeAgent, normalizeCursorModelID(opts.Model()))
		},
	})
}

func normalizeCursorModelID(model string) string {
	if model == cursorCLIModelAutoWire {
		return cursorCLIModelAuto
	}
	return model
}

// cursorModelBracketParams extracts the key=value metadata Cursor bakes into a model
// id's trailing brackets, e.g. "claude-fable-5[thinking=true,context=300k,effort=high]"
// -> {thinking:true, context:300k, effort:high}. Returns nil when there is no bracket
// or it is empty (e.g. "default[]"). The bracketed id IS the wire id Cursor expects, so
// callers parse it for display metadata without rewriting the id.
func cursorModelBracketParams(id string) map[string]string {
	open := strings.IndexByte(id, '[')
	if open < 0 || !strings.HasSuffix(id, "]") {
		return nil
	}
	inner := id[open+1 : len(id)-1]
	if inner == "" {
		return nil
	}
	params := make(map[string]string)
	for _, pair := range strings.Split(inner, ",") {
		if k, v, ok := strings.Cut(pair, "="); ok && strings.TrimSpace(k) != "" {
			params[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return params
}

// parseCursorContextWindow parses a Cursor context value like "300k"/"272k"/"200000"
// into a token count, or 0 when unparseable.
func parseCursorContextWindow(v string) int64 {
	if v == "" {
		return 0
	}
	mult := int64(1)
	switch v[len(v)-1] {
	case 'k', 'K':
		mult, v = 1000, v[:len(v)-1]
	case 'm', 'M':
		mult, v = 1_000_000, v[:len(v)-1]
	}
	n, err := strconv.ParseFloat(v, 64)
	// ParseFloat accepts "inf"/"nan"; int64(±Inf) and int64(NaN) are
	// implementation-defined, so reject non-finite values rather than surfacing a
	// garbage context window in the picker.
	if err != nil || n <= 0 || math.IsInf(n, 0) || math.IsNaN(n) {
		return 0
	}
	// A finite but out-of-int64-range value (an absurd server-reported context like
	// "99999999999999999999k") also converts to an implementation-defined garbage int64
	// (it saturates to MaxInt64 on arm64, wraps to MinInt64 on amd64), so reject it too.
	// float64(math.MaxInt64) rounds up to 2^63, so >= catches everything that overflows.
	scaled := n * float64(mult)
	if scaled >= float64(math.MaxInt64) {
		return 0
	}
	return int64(scaled)
}

// decorateCursorModel surfaces the metadata Cursor bakes into a model id (which the
// server reports only inside the opaque bracketed id, not in the model's name) as the
// ModelInfo's ContextWindow and a human-readable Description, so the picker shows the
// effort / reasoning / extended-thinking / context window each variant carries. It also
// replaces the server's bare-id model name with a friendly display name.
func decorateCursorModel(m *agent.ModelInfo) {
	// Cursor's server reports a model's name as the bare bracket-less id
	// ("composer-2.5", "claude-opus-4-8"); humanize it into a friendly display name when
	// the server didn't already supply a better one (it does only for "Auto"). Done
	// before the params early-return so bracket-less variants ("gemini-3.1-pro[]") are
	// humanized too.
	humanized := false
	if bare := stripModelIDBrackets(m.Id); m.DisplayName == "" || strings.EqualFold(m.DisplayName, bare) {
		m.DisplayName = humanizeModelID(m.Id)
		humanized = true
	}
	params := cursorModelBracketParams(m.Id)
	if len(params) == 0 {
		return
	}
	if cw := parseCursorContextWindow(params["context"]); cw > 0 {
		m.ContextWindow = cw
	}
	// Append the variant's distinguishing attribute to the display name so two variants of
	// the same base model don't collapse to identical picker labels: the reasoning-effort
	// level when present ("GPT 5.5" -> "GPT 5.5 Medium"), else the extended-thinking or fast
	// flag ("Composer 2.5" -> "Composer 2.5 Fast"). Only an AUTO-humanized name is suffixed
	// -- a real server-provided name is trusted to already disambiguate, so it is left as-is
	// (no "Composer 2.5 (Fast) Fast"). The fuller form stays in the tooltip Description below.
	if humanized {
		if suffix := cursorModelNameSuffix(params); suffix != "" {
			m.DisplayName += " " + suffix
		}
	}
	var parts []string
	if params["thinking"] == "true" {
		parts = append(parts, "Extended thinking")
	}
	// Cursor spells a model's reasoning-effort level three ways and means one thing by all
	// of them (cursorReasoningAttribute), mutually exclusive in practice. Show it ONCE, in
	// that function's order, so this tooltip cannot disagree with the name suffix, which
	// reads the same function. (A model reporting more than one -- which would contradict
	// the same-concept assumption -- then renders consistently in both places.)
	if level, noun := cursorReasoningAttribute(params); level != "" {
		parts = append(parts, providerkit.CapitalizeFirst(level)+" "+noun)
	}
	if params["fast"] == "true" {
		parts = append(parts, "Fast")
	}
	if len(parts) == 0 {
		return
	}
	suffix := strings.Join(parts, " · ")
	if m.Description != "" {
		m.Description += " · " + suffix
	} else {
		m.Description = suffix
	}
}

// cursorReasoningAttribute returns a model's reasoning-effort level and the noun that
// renders it. Cursor's catalogue spells the one concept three ways -- "effort" on Claude
// models, "reasoning" on GPT models, "reasoning_effort" on Grok -- so this is the single
// place that states their order of preference, and both the name suffix and the tooltip
// read it. Returns "" when the id carries none of the three.
//
// "reasoning_effort" is an effort level and says so in Cursor's own tooltip ("low
// effort"), so it takes the effort noun rather than a third wording.
func cursorReasoningAttribute(params map[string]string) (level string, noun string) {
	if level := params["effort"]; level != "" {
		return level, "effort"
	}
	if level := params["reasoning"]; level != "" {
		return level, "reasoning"
	}
	return params["reasoning_effort"], "effort"
}

// cursorReasoningLevel returns the level alone, for the callers that render it through
// the shared effort-label table rather than as a tooltip sentence.
func cursorReasoningLevel(params map[string]string) string {
	level, _ := cursorReasoningAttribute(params)
	return level
}

// cursorModelNameSuffix returns the short distinguishing suffix for a model variant's
// display name, preferring the reasoning-effort level (cased like cursorEffortLabel),
// then the extended-thinking flag, then the fast flag. Returns "" when the variant carries
// no distinguishing attribute. Keeps variants of the same base model from rendering as
// identical labels in the picker; the fuller attribute list lives in the Description.
func cursorModelNameSuffix(params map[string]string) string {
	if level := cursorReasoningLevel(params); level != "" {
		return cursorEffortLabel(level)
	}
	if params["thinking"] == "true" {
		return "Thinking"
	}
	if params["fast"] == "true" {
		return "Fast"
	}
	return ""
}

// cursorEffortLabel renders a Cursor reasoning-effort level for the model-name
// suffix. It reads the shared table, so an id Cursor shows in a model name and
// the same id in another provider's effort picker cannot be spelled differently
// -- "xhigh" used to render "XHigh" here and "Extra High" everywhere else.
func cursorEffortLabel(level string) string {
	return providerkit.EffortLabel(level)
}

func cursorModelIDForWire(model string) string {
	if model == cursorCLIModelAuto {
		return cursorCLIModelAutoWire
	}
	return model
}

func fallbackCursorCLIModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: ModeAgent, Name: "Agent"},
		{Id: ModePlan, Name: "Plan"},
		{Id: ModeAsk, Name: "Ask"},
	}
}

func (a *Agent) setCursorModel(model string) error {
	// Send the wire id but store the normalized (display) id, so b.model never
	// transiently holds the wire form "default[]" (see SetModelViaConfigOption).
	if err := a.SetModelViaConfigOption(cursorModelIDForWire(model)); err != nil {
		return err
	}
	a.SetCurrentModel(model)
	return nil
}

var cursorCLIAvailableModels = []*agent.ModelInfo{
	{Id: cursorCLIModelAuto, DisplayName: "Auto", Description: "Automatically selects the best available Cursor model", IsDefault: true},
}

// cursorStaticOptionGroups holds Cursor's static permission-mode group. The
// factory registration and Start both read this one value, so the
// static fallback and a running agent's fallback modes cannot drift.
var cursorStaticOptionGroups = acp.StaticSecondaryGroup(acp.ModeChannelPermissionMode, fallbackCursorCLIModes())

// Compile-time proof that Agent implements Agent. acp.Start is generic over
// T and can only assert this at runtime (any(a).(Agent)); this guard turns a
// dropped or renamed method into a build error rather than a launch-time
// "does not implement Agent".
var _ agent.Agent = (*Agent)(nil)

// cursorLocator finds the Cursor CLI on the user's PATH.
var cursorLocator = launch.Binaries("cursor-agent")

// Registration states everything the worker knows about Cursor before any
// of its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider:         leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		Plugin:           cursorProvider{},
		Start:            Start,
		Locator:          cursorLocator,
		DefaultModels:    cursorCLIAvailableModels,
		OptionGroups:     cursorStaticOptionGroups,
		NormalizeModelID: normalizeCursorModelID,
		PermissionDefaults: agent.PermissionDefaults{
			Fallback: ModeAgent,
		},
		EnvModelKey: "LEAPMUX_CURSOR_DEFAULT_MODEL",
	}
}

// cursorSubagentFromToolCall detects Cursor's Task delegation tool_call
// (rawInput._toolName == "task", title "Task: <description>"). The observed
// toolCallId can contain an embedded newline; the neutral layer sanitizes row
// keys before use. The tool call is the child key because Cursor exposes no
// separate child-session key.
func cursorSubagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	if !cursorToolCallIsTaskTool(tc.RawInput) {
		return nil
	}
	title := strings.TrimPrefix(tc.Title, "Task: ")
	if title == "" {
		title = "Cursor subagent"
	}
	var input struct {
		Prompt string `json:"prompt"`
	}
	_ = json.Unmarshal(tc.RawInput, &input)
	return &acp.SubagentObservation{
		RowKey:        tc.ToolCallID,
		Title:         title,
		Status:        bgtask.StatusRunning,
		ChildAgentKey: tc.ToolCallID,
		Prompt:        input.Prompt,
		Spawns:        true,
	}
}

// cursorToolCallIsTaskTool reports whether a Cursor tool call is the `task`
// delegation tool, which is the only Cursor tool that spawns a subagent. It
// reads ONE payload, so it answers only for a payload that carries the input.
//
// An absent rawInput gives false, which means "this payload does not say it is
// the task tool" and NOT "this call is not the task tool". Cursor does not
// always echo the input on an update, so the closing hook must not treat the
// two as the same: finishedObservation falls back to the note the spawn left
// (taskToolCalls) before it classifies a backgrounded call as a shell.
func cursorToolCallIsTaskTool(rawInput json.RawMessage) bool {
	if len(rawInput) == 0 {
		return false
	}
	var input struct {
		ToolName string `json:"_toolName"`
	}
	return json.Unmarshal(rawInput, &input) == nil && input.ToolName == contracts.CursorToolTask
}

// cursorToolCallRanInBackground reports whether a finished Cursor tool call was
// backgrounded, which Cursor states as rawOutput.isBackground.
func cursorToolCallRanInBackground(rawOutput json.RawMessage) bool {
	if len(rawOutput) == 0 {
		return false
	}
	var out struct {
		IsBackground bool `json:"isBackground"`
	}
	return json.Unmarshal(rawOutput, &out) == nil && out.IsBackground
}

// cursorSubagentFromToolCallUpdate maps Cursor's finished tool_call updates to
// registry rows. The final update fires for EVERY finished tool_call (not just
// spawns); a plain foreground tool is a close-only observation, so it does not
// create a spurious row. A backgrounded call carries an activity line and
// upserts before closing.
//
// A backgrounded call is a SHELL unless it is the `task` tool. Cursor's other
// tools are not subagents, and the neutral layer defaults a blank kind to
// Subagent -- so leaving the kind blank here put a shell in the sidebar under a
// Bot icon, in the subagent filter tab, labelled with its raw toolCallId. The
// task-tool branch leaves BOTH the kind and the title blank on purpose: the spawn
// observation already set them, and Item.PreservingBlanksFrom keeps an existing
// value only for a blank incoming one. Writing them here would flip a real
// subagent row to a shell and overwrite its trimmed title with the raw
// "Task: ..." string.
//
// wasTaskTool comes from the caller, not from tcu, because this update does not
// always carry rawInput. Reading the identity off tcu alone made an absent
// rawInput mean "not the task tool", so a backgrounded task whose final update
// omitted its input took the shell branch and flipped its own live row.
func cursorSubagentFromToolCallUpdate(tcu acp.ToolCallUpdateEnvelope, wasTaskTool bool) *acp.SubagentObservation {
	if !acp.StatusIsFinal(tcu.Status) {
		return nil
	}
	obs := &acp.SubagentObservation{
		RowKey:   tcu.ToolCallID,
		Status:   acp.FinalStatus(tcu.Status),
		CloseRow: true,
		Mode:     acp.ModeCloseOnly,
	}
	if !cursorToolCallRanInBackground(tcu.RawOutput) {
		return obs
	}
	obs.Mode = acp.ModeUpsert
	obs.Activity = "background task"
	if !wasTaskTool {
		obs.Kind = bgtask.KindShell
		// This update is the row's only event, so it is also the only chance to
		// give it a readable label. Without one the sidebar shows the raw
		// toolCallId. The row's TitleIsCommand stays false (no observation sets
		// it): Cursor's title is a label, not a verbatim command, and prose in
		// the monospace face reads worse than a command in the normal one.
		obs.Title = tcu.Title
	}
	return obs
}
