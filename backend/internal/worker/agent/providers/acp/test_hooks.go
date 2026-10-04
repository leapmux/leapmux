package acp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The methods and functions below are for the test of an ACP provider, which
// lives in the package of that provider and so cannot reach the base state. Each
// one exposes one piece of that state, or one step of the base, as a test needs
// it. Production code never refers to them. The sub-test "production code never
// refers to a test hook" of TestRepoInvariants (internal/audit) enforces that.
//
// A getter or a setter of a field takes no lock. The test owns the agent, and a
// test that races the reader takes b.Mu around the call itself, as it did
// around the field.

// AttachPeerForTest stands a fake peer in for the ACP runtime, for a test that
// answers the agent's requests itself. The agent writes each request to stdin,
// and the test answers each one through Deliver. ctx and cancel govern the fake
// process as they govern a real one. The agent then runs in session sessionID,
// as a handshake leaves it.
func (b *Base) AttachPeerForTest(ctx context.Context, cancel func(), stdin io.WriteCloser, agentID, sessionID string) {
	b.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID:     agentID,
		Stdin:       stdin,
		Ctx:         ctx,
		Cancel:      cancel,
		ProcessDone: make(chan struct{}),
		StderrDone:  make(chan struct{}),
	})
	b.sessionID = sessionID
	// No stderr reader runs, so nothing else ends the drain.
	b.SkipStderr()
}

// AvailableModelsForTest returns the model catalog of the session.
func (b *Base) AvailableModelsForTest() []*agent.ModelInfo {
	return b.availableModels
}

// SetAvailableModelsForTest sets the model catalog of the session.
func (b *Base) SetAvailableModelsForTest(v []*agent.ModelInfo) {
	b.availableModels = v
}

// SetAvailableModesForTest sets the permission modes that the session offers.
func (b *Base) SetAvailableModesForTest(v []*leapmuxv1.AvailableOption) {
	b.availableModes = v
}

// AvailablePrimaryAgentsForTest returns the primary agents that the session offers.
func (b *Base) AvailablePrimaryAgentsForTest() []*leapmuxv1.AvailableOption {
	return b.availablePrimaryAgents
}

// SetAvailablePrimaryAgentsForTest sets the primary agents that the session offers.
func (b *Base) SetAvailablePrimaryAgentsForTest(v []*leapmuxv1.AvailableOption) {
	b.availablePrimaryAgents = v
}

// CurrentPrimaryAgentForTest returns the current primary agent.
func (b *Base) CurrentPrimaryAgentForTest() string {
	return b.currentPrimaryAgent
}

// SetCurrentPrimaryAgentForTest sets the current primary agent.
func (b *Base) SetCurrentPrimaryAgentForTest(v string) {
	b.currentPrimaryAgent = v
}

// HooksForTest returns the hooks of the provider, which a test changes in place
// as the configure function of a start spec sets them.
func (b *Base) HooksForTest() *Hooks {
	return &b.hooks
}

// ModelForTest returns the current model.
func (b *Base) ModelForTest() string {
	return b.model
}

// SetModelForTest sets the current model.
func (b *Base) SetModelForTest(v string) {
	b.model = v
}

// ModelsFieldInfosForTest returns the models that the `models` field of the last full session response reported.
func (b *Base) ModelsFieldInfosForTest() []ModelInfo {
	return b.modelsFieldInfos
}

// SetModelsFieldInfosForTest sets the models that the `models` field of the last full session response reported.
func (b *Base) SetModelsFieldInfosForTest(v []ModelInfo) {
	b.modelsFieldInfos = v
}

// OptionsForTest returns the bookkeeping of the server-driven config options.
func (b *Base) OptionsForTest() *optionState {
	return &b.options
}

// PermissionModeForTest returns the current permission mode.
func (b *Base) PermissionModeForTest() string {
	return b.permissionMode
}

// SetPermissionModeForTest sets the current permission mode.
func (b *Base) SetPermissionModeForTest(v string) {
	b.permissionMode = v
}

// SetPromptActiveForTest sets whether a session/prompt runs.
func (b *Base) SetPromptActiveForTest(v bool) {
	b.promptActive = v
}

// SetReapplySettingsForTest sets the step that ClearContext runs after session/new to apply the settings again.
func (b *Base) SetReapplySettingsForTest(v func()) {
	b.reapplySettings = v
}

// SetRefreshFromSessionForTest sets the step that ClearContext runs to read the state of the new session.
func (b *Base) SetRefreshFromSessionForTest(v func(json.RawMessage)) {
	b.refreshFromSession = v
}

// SetSecondaryFallbackForTest sets the static option list of the secondary axis.
func (b *Base) SetSecondaryFallbackForTest(v []*leapmuxv1.AvailableOption) {
	b.secondaryFallback = v
}

// SessionIDForTest returns the session id.
func (b *Base) SessionIDForTest() string {
	return b.sessionID
}

// SetSessionIDForTest sets the session id.
func (b *Base) SetSessionIDForTest(v string) {
	b.sessionID = v
}

// SessionMuForTest returns the lock that serializes the session lifecycle
// against each session/* request.
func (b *Base) SessionMuForTest() *sync.RWMutex {
	return &b.sessionMu
}

// SetSinkForTest replaces the provider services of the agent.
func (b *Base) SetSinkForTest(sink agent.ProviderServices) {
	b.sink = sink
}

// SetSteerMethodForTest sets the steer method that the handshake advertised.
func (b *Base) SetSteerMethodForTest(v string) {
	b.steerMethod = v
}

// SetClosesSessionsForTest sets whether the handshake advertised session/close.
func (b *Base) SetClosesSessionsForTest(v bool) {
	b.closesSessions = v
}

// SteerRunIDForTest returns the run id of the running prompt.
func (b *Base) SteerRunIDForTest() string {
	return b.steerRunID
}

// SetSteerRunIDForTest sets the run id of the running prompt.
func (b *Base) SetSteerRunIDForTest(v string) {
	b.steerRunID = v
}

// TurnAssistantTextForTest returns the assistant text that the running turn
// assembled.
func (b *Base) TurnAssistantTextForTest() *strings.Builder {
	return &b.turnAssistantText
}

// TurnToolUsesForTest returns how many tools the running turn used.
func (b *Base) TurnToolUsesForTest() int {
	return b.turnToolUses
}

// SetTurnToolUsesForTest sets how many tools the running turn used.
func (b *Base) SetTurnToolUsesForTest(v int) {
	b.turnToolUses = v
}

// SetCompletedTerminalsForTest sets the results of the terminals that finished.
func (b *Base) SetCompletedTerminalsForTest(v map[string]contracts.ACPTerminalResult) {
	b.completedTerminals = v
}

// InterruptRequestedForTest reports whether the reader stopped the running turn.
func (b *Base) InterruptRequestedForTest() bool {
	return b.acpInterruptRequested()
}

// NoteInterruptRequestedForTest records that the reader stopped the running turn, as Interrupt does.
func (b *Base) NoteInterruptRequestedForTest() {
	b.noteACPInterruptRequested()
}

// ApplyHandshakeModeForTest applies the modes of a handshake, as the startup does.
func (b *Base) ApplyHandshakeModeForTest(handshake *SessionResult, defaultMode string) {
	b.applyHandshakeMode(handshake, defaultMode)
}

// ApplyHandshakeModelsForTest applies the models of a handshake, as the startup does.
func (b *Base) ApplyHandshakeModelsForTest(handshake *SessionResult) {
	b.applyHandshakeModels(handshake)
}

// ApplyOptionGroupsLockedForTest folds server-driven config options into the option groups. The caller holds b.Mu.
func (b *Base) ApplyOptionGroupsLockedForTest(options []ConfigOption) (valueChanged, listChanged bool) {
	return b.applyOptionGroupsLocked(options)
}

// ApplySessionRefreshForTest reads the state of a session response, as ClearContext does by default.
func (b *Base) ApplySessionRefreshForTest(resp json.RawMessage) {
	b.applySessionRefresh(resp)
}

// ApplyStartupOptionsForTest pushes the launch options to the session, as the startup does.
func (b *Base) ApplyStartupOptionsForTest(opts agent.Options) {
	b.applyStartupOptions(opts)
}

// BeginSessionUpdatesForTest starts to buffer session updates, as the handshake does.
func (b *Base) BeginSessionUpdatesForTest() {
	b.beginSessionUpdates()
}

// CancelSessionForTest sends session/cancel.
func (b *Base) CancelSessionForTest() error {
	return b.cancelSession()
}

// ConfigurePrimaryAgentsForTest installs the primary-agent list and the selection of a handshake.
func (b *Base) ConfigurePrimaryAgentsForTest(modes []ModeInfo, currentModeID, requestedPrimaryAgent string, fallback []*leapmuxv1.AvailableOption, defaultAgent string) error {
	return b.configurePrimaryAgents(modes, currentModeID, requestedPrimaryAgent, fallback, defaultAgent)
}

// FinishPromptRequestForTest ends a session/prompt request, as its response does.
func (b *Base) FinishPromptRequestForTest(sessionID string, response json.RawMessage, err error) {
	b.finishPromptRequest(sessionID, response, err)
}

// HandleConfigOptionUpdateForTest handles a config_option_update, as the reader does.
func (b *Base) HandleConfigOptionUpdateForTest(update json.RawMessage) {
	b.handleACPConfigOptionUpdate(update)
}

// HandlePromptResponseForTest handles a session/prompt response, as the reader does.
func (b *Base) HandlePromptResponseForTest(resp json.RawMessage) {
	b.handleACPPromptResponse(resp)
}

// HandleSessionUpdateForTest handles the params of a session/update notification, as the reader does.
func (b *Base) HandleSessionUpdateForTest(params json.RawMessage) {
	b.handleACPSessionUpdate(params)
}

// HandleUpdateForTest dispatches one session update, as the reader does.
func (b *Base) HandleUpdateForTest(update json.RawMessage) {
	b.handleACPUpdate(update)
}

// HandleToolCallForTest handles a tool_call update, as the reader does.
func (b *Base) HandleToolCallForTest(update json.RawMessage) {
	b.main().handleToolCall(update)
}

// HandleToolCallUpdateForTest handles a tool_call_update, as the reader does.
func (b *Base) HandleToolCallUpdateForTest(update json.RawMessage) {
	b.main().handleToolCallUpdate(update)
}

// ReapplyModelAndSecondaryForTest applies the model and the secondary setting again, the default reapply step of ClearContext.
func (b *Base) ReapplyModelAndSecondaryForTest() {
	b.reapplyModelAndSecondary()
}

// WireTurnActiveForTest connects the turn flag to the provider services, as the start does.
func (b *Base) WireTurnActiveForTest() {
	b.wireTurnActive()
}

// MarkKnownForTest records option as a config option that the server
// reported.
func (g *optionState) MarkKnownForTest(option ConfigOption) {
	g.markKnown(option)
}

// MarkSurfacedForTest records id as a config option that the agent surfaced.
func (g *optionState) MarkSurfacedForTest(id string) {
	g.markSurfaced(id)
}

// GroupsForTest returns the option groups of the server-driven config options.
func (g *optionState) GroupsForTest() []*leapmuxv1.AvailableOptionGroup {
	return g.groups
}

// SetGroupsForTest sets the option groups of the server-driven config options.
func (g *optionState) SetGroupsForTest(groups []*leapmuxv1.AvailableOptionGroup) {
	g.groups = groups
}

// StructureGenForTest returns the generation that counts the changes to the
// structure of the option groups.
func (g *optionState) StructureGenForTest() uint64 {
	return g.structureGen
}

// ValuesForTest returns the current value of each server-driven config option.
func (g *optionState) ValuesForTest() map[string]string {
	return g.values
}

// SetValuesForTest sets the current value of each server-driven config option.
func (g *optionState) SetValuesForTest(values map[string]string) {
	g.values = values
}

// BuildModelsForTest builds the model catalog from the models that a session
// reported.
func BuildModelsForTest(models []ModelInfo, currentModelID string, normalize func(string) string) []*agent.ModelInfo {
	return buildACPModels(models, currentModelID, normalize)
}

// BuildSessionRequestForTest builds the session/new or the resume request.
func BuildSessionRequestForTest(resumeSessionID, workingDir, newMethod, resumeMethod string) (method string, params []byte) {
	return buildACPSessionRequest(resumeSessionID, workingDir, newMethod, resumeMethod, nil)
}

// BuildConfigOptionSelectForTest builds the option list and the current value of
// a select config option.
func BuildConfigOptionSelectForTest(options []ConfigOption, hiddenFilter func(string) bool) (built []*leapmuxv1.AvailableOption, current string, ok bool) {
	return buildConfigOptionSelect(options, hiddenFilter)
}

// BuildOptionGroupForTest builds the option group of one server-driven config
// option.
func BuildOptionGroupForTest(option ConfigOption, current, effortConfigID string) *leapmuxv1.AvailableOptionGroup {
	return buildOptionGroup(option, current, effortConfigID)
}

// BuildOptionValuesForTest builds the option list of one config option.
func BuildOptionValuesForTest(option ConfigOption, hiddenFilter func(string) bool) []*leapmuxv1.AvailableOption {
	return buildOptionValues(option, hiddenFilter)
}

// IsEffortConfigOptionForTest reports whether option is the effort axis of a
// provider whose convention id is effortConfigID.
func IsEffortConfigOptionForTest(option ConfigOption, effortConfigID string) bool {
	return isEffortConfigOption(option, effortConfigID)
}

// ParseSessionResultForTest parses the channels of a session response.
func ParseSessionResultForTest(resp json.RawMessage) (*SessionResult, error) {
	return parseACPSessionResult(resp)
}
