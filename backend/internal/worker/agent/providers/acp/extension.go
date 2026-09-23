package acp

import (
	"encoding/json"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
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
	// supports, in the `_meta` of the initialize request.
	ClientCapabilityMeta map[string]any
	// AdvertisedSteerMethod reads the steer method that the initialize response
	// advertises. It returns "" when the response advertises none.
	AdvertisedSteerMethod func(initializeResponse []byte) string
	// ExtraMethod handles a request or a notification that the base does not
	// know. It returns true for a line that it handled. The base refuses an
	// unhandled request and persists an unhandled notification.
	ExtraMethod MethodHandler
	// SessionMetadataHandler reads the provider `_meta` of a session update. It
	// returns true for an update that it consumed, and the base then does not
	// dispatch the update.
	SessionMetadataHandler func(updateType string, metadata map[string]json.RawMessage) bool
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
	// ModelDecorator, when set, changes each built model in place. Cursor parses
	// the metadata in its bracketed model ids (effort, thinking, context) into the
	// Description and the ContextWindow of the ModelInfo, which the bare name that
	// the server reports omits.
	ModelDecorator func(*agent.ModelInfo)
	// ClearProviderState drops the provider state that the tool-call ids of the
	// outgoing session key. ClearContext calls it.
	ClearProviderState func()
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
