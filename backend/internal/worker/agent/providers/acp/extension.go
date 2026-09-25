package acp

import (
	"encoding/json"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// This file holds what an ACP provider changes about the shared base, and the
// methods that the code of a provider calls to read or write the base state
// while the process runs. The state is unexported, so a provider reaches it
// only through exported methods, and each method here takes b.Mu itself. A
// provider block that read two fields under one lock thus stays one atomic
// read.

// Hooks is what one ACP provider changes about the shared base. The Configure
// function of the StartSpec returns it once, before the process starts. The
// base keeps it in a field that no provider writes, so no hook changes after the
// start, and the base reads each hook without b.Mu.
//
// A zero hook keeps the base behavior that the hook controls.
type Hooks struct {
	// Sink replaces the provider services that the start received. Cursor and
	// Reasonix wrap them in a tool transcript. Nil keeps them.
	Sink agent.ProviderServices
	// InitialModel replaces the launch model until the session reports its
	// current model. Empty keeps the launch model.
	InitialModel string
	// ModeChannel selects how the configOptions `mode` select maps to the
	// secondary setting of this provider, and thereby the family of the provider.
	// One field makes the illegal state "both families" impossible, and each site
	// that depends on the family reads this field.
	ModeChannel ModeChannel
	// PreferredFirstMode, when non-empty, is the permission-mode id that this
	// provider lists first in every rebuilt permission list. Goose declares
	// smart_approve, its safe mode for a new session. "" keeps the order that the
	// server reports.
	//
	// Position 0 is not only a display order: secondaryGroup stamps
	// defaultOrFirstOption(options) as the DefaultValue of the group, and
	// reconcileCurrentOptionID seeds the current selection from the same option
	// again. The preferred mode is thus also the mode that this provider falls
	// back to.
	PreferredFirstMode string
	// EffortConfigID is the daemon config-option id that this provider drives its
	// reasoning-effort axis through, when that id is a provider CONVENTION and not
	// the well-known "effort" (Goose "thinking_effort"). isEffortConfigOption also
	// reads it, so the strongest-first sort and the none/off raise recognize an
	// uncategorized axis under this id and under no convention id of another
	// provider.
	//
	// It is "" for a provider whose effort axis IS "effort" (OpenCode and Kilo,
	// where the override maps to "effort" directly) or that has no effort axis
	// (Cursor). applyStartupOptions maps the env-effort override, which LeapMux
	// stores under the well-known "effort" id, onto this id, so the start pushes
	// the operator default again whatever id the daemon uses.
	//
	// Each provider declares the id. A scan of the live option set for ANY
	// well-known effort id could mistake a second axis that a daemon advertises
	// by coincidence for the effort axis, and push the override onto it too. A
	// daemon that tags its axis with the ACP `thought_level` category, a signal
	// that the spec defines, is still discovered without it; see
	// startupEffortConfigID.
	EffortConfigID string
	// PrimaryAgentHiddenFilter, when set, marks the primary-agent ids that the
	// provider treats as internal pseudo-agents to hide from the picker
	// (OpenCode's compaction, title and summary). Each site that builds the
	// primary-agent list applies it.
	PrimaryAgentHiddenFilter func(string) bool
	// ClientCapabilityMeta advertises the provider extensions that LeapMux
	// supports, in the `_meta` of the initialize request's clientCapabilities.
	ClientCapabilityMeta map[string]any
	// InitializeMeta is the `_meta` of the initialize request itself, beside its
	// clientCapabilities. Grok Build reads the client identity there, which
	// selects its option lists and scopes its approval-mode notification.
	InitializeMeta map[string]any
	// InitializeResponse reads the initialize response, once, before the base
	// opens the session. A provider whose agent states something there that no
	// later update restates -- Grok Build lists its commands -- records it.
	InitializeResponse func(response []byte)
	// ControlRequestObserver reads each control request that
	// PublishSessionControlRequest publishes: a permission request and an MCP
	// elicitation of the base, and each dialog of the provider's own. It runs
	// before the publication, and never for a request that the base refuses
	// because its session is one that the agent does not serve. A provider whose
	// agent withdraws such a request by an event of its own, rather than by the
	// protocol's cancel notification, records what identifies it here.
	ControlRequestObserver func(line *providerkit.ParsedLine)
	// AnswerlessControlsOutliveTurns states that each control request which the
	// provider publishes with no cancel answer asks about the session or the
	// process, not about the running turn. A stop and a context clear then
	// answer and retire only the requests that carry a cancel answer, and keep
	// the rest open for the reader.
	//
	// Grok Build sets it: its one such request asks whether a repository may
	// load its own configuration. No turn owns that question, and Grok keeps
	// waiting for the answer, so a stop that retired the card left the
	// repository untrusted until the agent restarted. The default retires every
	// open request, which is right for Cursor's question: its turn owns the
	// question, and Cursor defines no outcome for a withdrawn one.
	AnswerlessControlsOutliveTurns bool
	// RetireSession stops the work that an outgoing session left running, for an
	// agent that offers a route to that work beside session/close. A context
	// clear calls it once with the id of the session that it replaced, after the
	// swap, and after it sent that session session/cancel and, when the agent
	// advertises it, session/close. It runs on the goroutine of the clear, so it
	// must not wait on the agent: send each request detached.
	RetireSession func(sessionID string)
	// AdvertisedSteerMethod reads the steer method that the initialize response
	// advertises. It returns "" when the response advertises none.
	AdvertisedSteerMethod func(initializeResponse []byte) string
	// ExtraMethod handles a request or a notification that the base does not
	// know. It returns true for a line that it handled. The base refuses an
	// unhandled request and persists an unhandled notification.
	ExtraMethod MethodHandler
	// SessionMetadataHandler reads the provider `_meta` of a session update of
	// the main session. It returns true for an update that it consumed, and the
	// base then does not dispatch the update. update is the whole update, for a
	// provider that keeps an update it consumed as a record: Kiro ends a turn
	// that it started by itself with an update, which becomes the turn-end row.
	SessionMetadataHandler func(updateType string, metadata map[string]json.RawMessage, update json.RawMessage) bool
	// SubagentFromToolCall and SubagentFromToolCallUpdate translate a tool_call
	// and a tool_call_update into a neutral SubagentObservation. The
	// observation drives the background-task registry and the child transcripts.
	// A nil hook leaves that path inactive.
	//
	// A hook need not be a pure function of its envelope: Cursor binds both hooks
	// to its agent, because the closing update cannot tell a backgrounded task
	// from a backgrounded shell without what the spawn recorded.
	SubagentFromToolCall       func(tc ToolCallEnvelope) *SubagentObservation
	SubagentFromToolCallUpdate func(tcu ToolCallUpdateEnvelope) *SubagentObservation
	// ToolOutput reads ONE live update into everything that it states about the
	// output of a running call, for a provider whose output arrives OUTSIDE the
	// content of the update. Goose is the one: its `live_output` chunks ride in
	// `_meta.toolNotification`, so the path that reads the content never sees
	// them.
	//
	// ONE hook gives the count and the text, because they are one observation of
	// one state. Two hooks read that state under two lock acquisitions, so the
	// chunk of another goroutine could land between them, and the row then drew
	// a byte count from before that chunk beside a tail from after it.
	ToolOutput func(tcu ToolCallUpdateEnvelope) (ToolOutputObservation, bool)
	// ToolNotification reads a LIVE notification that rides the update of one
	// running call and is neither its output nor its byte count. Goose is the one
	// provider that sends any: a `progress` sentence and the `platform_event` of
	// an extension. It returns true for an update that it claimed, and the caller
	// then skips the merge, because nothing in such an update changes the row.
	ToolNotification func(tcu ToolCallUpdateEnvelope) bool
	// ToolOutputComplete drops the provider state for the output of one call,
	// when the base completes the output of that call.
	ToolOutputComplete func(toolCallID string)
	// ModelIDNormalizer, when set, rewrites each model id that the base parses
	// from configOptions (for example Cursor's auto<->default[] aliasing) before
	// the id reaches availableModels.
	ModelIDNormalizer func(string) string
	// ModelSetter, when set, replaces how the base writes a model over ACP.
	// Cursor maps the model id to its wire form (setCursorModel) before
	// session/set_model. Nil falls back to the base setModel. effectiveSetModel
	// resolves it, and UpdateSettings and the reapply path use that, so one body
	// serves Cursor and the plain providers alike.
	ModelSetter func(string) error
	// ModelWriteRevealsOptions states that the agent reports some config
	// options of a model only after the client writes that model. Kiro reports
	// the effort axis of a model this way: its session/new and session/load
	// responses omit the axis, although the model has one. The startup then
	// writes the model even when the session already runs it, so the options
	// exist before the startup applies the requested values to them. False
	// skips a model write that changes nothing.
	ModelWriteRevealsOptions bool
	// ModelDecorator, when set, changes each built model in place. meta is the
	// `_meta` that the session reported beside the model:
	//
	//   - For an entry of the `models` field, the `_meta` of that entry.
	//   - For a value of the configOptions `model` select, the `_meta` of that
	//     value (ConfigOptionValue.Meta). Kiro states the credit rate of each
	//     model there.
	//   - nil when the session reported no `_meta` for the model.
	//
	// A model that both channels list takes the entry of the `models` field and
	// its `_meta` (see mergeModelInfos), so the decorator reads one source for
	// each model. Cursor parses the metadata in its bracketed model ids (effort,
	// thinking, context) into the Description and the ContextWindow of the
	// ModelInfo, which the bare name that the server reports omits. Grok Build
	// and Qwen Code state the context window in meta.
	ModelDecorator func(model *agent.ModelInfo, meta json.RawMessage)
	// ClearProviderState drops the provider state that the tool-call ids of the
	// outgoing session key. ClearContext calls it.
	ClearProviderState func()
	// ChildUpdateRoute reads the registry row key of the subagent that one
	// update of the main session belongs to, from the update type and its
	// `_meta`. It returns "" for an update of the main session. Qwen Code tags
	// each update of a foreground subagent with the id of the tool call that
	// spawned it. See children.go for the other route, a session of its own.
	ChildUpdateRoute func(updateType string, metadata map[string]json.RawMessage) string
	// ChunkMessageID reads the identity of the message that one
	// agent_message_chunk or agent_thought_chunk belongs to, from the `_meta` of
	// the update. It returns "" for a chunk that states none, and such a chunk
	// continues the buffered text. The protocol states no message boundary, so a
	// provider whose agent sends two answers with no update between them states
	// the boundary here: Kiro gives each answer its own `_meta.kiro.replayId`,
	// and its plan mode hands a plan to the default mode that way. A chunk of
	// another message stores the buffered text first, as a message of its own.
	// Nil keeps a run of chunks one message until another update ends it.
	ChunkMessageID func(metadata map[string]json.RawMessage) string
	// ChildUserMessages states that a user_message_chunk that reaches the
	// transcript of a subagent is a message that the agent gave that subagent:
	// the instruction that opens a workflow step of Kiro, in the step's own
	// session. The reader never types into such a transcript. The base writes
	// the message into the child transcript: the first one as the prompt that
	// opens it, and each later one as a user message. The agent must send each
	// message whole, in one update. Without the hook the base drops the chunk,
	// as it drops a user_message_chunk of the main session.
	ChildUserMessages bool
	// SteersByOwnRoute states that the provider steers a running turn by a
	// route of its own rather than by a method that the initialize response
	// advertises: a second prompt on the same session (OpenCode, Kilo), a method
	// that no handshake states (Grok Build), or a queue that the agent drains
	// between two tool batches (Qwen Code). The base then reports steering, and
	// publishes each running turn as steerable.
	SteersByOwnRoute bool
	// FollowUpPrompt returns input that the provider accepted for the running
	// turn and that the turn ended before it could read. The base sends it as the
	// next prompt at once, before it reports the turn as over, so a message that
	// the worker queued later cannot overtake it. ok is false when nothing is
	// left. stopped states that the reader stopped the turn that ended: the base
	// then starts no new turn whatever the answer, and the provider decides what
	// becomes of the input -- Qwen Code drops it and says so in the transcript,
	// because the reader stopped the turn that the input was for.
	FollowUpPrompt func(stopped bool) (content string, attachments []*leapmuxv1.Attachment, ok bool)
	// PromptEnded reads how a prompt of LeapMux's ended, before the base writes
	// the turn-end row or the failure note of that prompt. err is the error of a
	// prompt that failed, and nil for a prompt that returned a result. stopped
	// states that the reader stopped the prompt or that the agent stopped: the
	// base then writes no failure note for an error. The base calls it only for
	// a prompt of the current session. A provider that holds state for the
	// running prompt settles it here: Kiro holds the display error of a prompt,
	// and writes it unless the prompt's failure note states the same text.
	PromptEnded func(err error, stopped bool)
	// DisableHostTerminal withholds the host terminal capability from the
	// initialize request. A provider that runs its shell commands itself and
	// would otherwise route them through the host terminal of LeapMux sets it:
	// Grok Build then loses its own background commands and their completion
	// events.
	DisableHostTerminal bool
	// PromptParams adjusts the params of each session/prompt before the base
	// sends it, the follow-up prompt included. Grok Build states the prompt id
	// there, so it can tell its own prompts from the turns the agent starts.
	PromptParams func(params map[string]any)
	// SessionParams adjusts the params of a session/new, a session/load or a
	// session/resume request before the base sends it. A provider adds its own
	// `_meta` keys there, or states the cwd that its store recorded for a
	// resumed session.
	SessionParams func(method string, params map[string]any)
	// LocalOptionGroups returns the option groups that the provider keeps
	// itself, because the agent neither reports nor stores them. The base serves
	// them after the groups of the session, and routes a change of one of them to
	// ApplyLocalOption. Each group carries its current value.
	LocalOptionGroups func() []*leapmuxv1.AvailableOptionGroup
	// ApplyLocalOption applies a new value of one local option group. It returns
	// handled=false for an id that no local group owns.
	ApplyLocalOption func(id, value string) (handled bool, err error)
}

// applyHooks keeps the hooks that the Configure function of one provider
// returned. The start calls it once, before the process starts.
//
// Sink and InitialModel seed the base fields b.sink and b.model, which the
// handshake and the session change later. The kept hooks hold neither, so no
// stale copy of either stays behind for a later reader to take.
func (b *Base) applyHooks(h Hooks) {
	if h.Sink != nil {
		b.sink = h.Sink
	}
	if h.InitialModel != "" {
		b.model = h.InitialModel
	}
	h.Sink, h.InitialModel = nil, ""
	b.hooks = h
}

// Sink returns the provider services of the agent. The start sets them before
// the reader goroutine exists, and nothing changes them after that, so a read
// takes no lock.
func (b *Base) Sink() agent.ProviderServices {
	return b.sink
}

// PromptActive reports whether a session/prompt runs now.
func (b *Base) PromptActive() bool {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	return b.promptActive
}

// SteerTarget returns what a steer request needs, from one critical section:
// the advertised steer method, whether a prompt runs, the session id, and the
// run id of the running prompt.
func (b *Base) SteerTarget() (method string, active bool, sessionID, runID string) {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	return b.steerMethod, b.promptActive, b.sessionID, b.steerRunID
}

// SteerRunActive reports whether a prompt still runs, and whether runID is still
// its run id.
func (b *Base) SteerRunActive(runID string) bool {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	return b.promptActive && b.steerRunID == runID
}

// SetSteerRunID records the run id of the running prompt, which a steer request
// must state.
func (b *Base) SetSteerRunID(runID string) {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	b.steerRunID = runID
}

// SetCurrentModel records model as the current model, after a provider wrote it
// to the session itself.
func (b *Base) SetCurrentModel(model string) {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	b.model = model
}

// SetPermissionMode records mode as the current permission mode, after a
// provider wrote it to the session itself.
func (b *Base) SetPermissionMode(mode string) {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	b.permissionMode = mode
}

// AvailableModes returns the permission modes that the session offers now. The
// base replaces the whole list on each change, and never changes it in place,
// so the caller may read the result without the lock.
func (b *Base) AvailableModes() []*leapmuxv1.AvailableOption {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	return b.availableModes
}
