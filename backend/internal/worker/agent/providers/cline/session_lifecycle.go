package cline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Session open, rebuild, and context clear.
//
// session.create opens a runtime session on the daemon. Cline fixes a session's
// mode, its tools and its system prompt when it builds the session, so a change of
// mode builds the session again: the worker reads the conversation and the
// compaction state, detaches the runtime, and creates a new one under the same
// session id with both. That is what Cline's own CLI does for a mode switch
// (restartWithMessages in apps/cli/src/runtime/interactive/session-runtime.ts).
// A resume creates the session under its stored id with its stored
// conversation, as the CLI's startResumedSession does, and a context clear
// creates a new session.
//
// The worker creates each session with:
//
//   - `toolPolicies: {"*": {"autoApprove": false}}`, so every tool call asks
//     the worker, which decides from the current mode (control.go).
//   - The question executor, so `ask_question` reaches the user.
//   - In Plan mode, the `switch_to_act_mode` tool, which Cline's CLI also adds
//     in Plan mode alone.
//   - Subagents and agent teams on, as in Cline's interactive CLI.
//   - In Plan and Act, the text extensions alone, so no hook or plugin runs
//     (clineSettings.configExtensions).

// The words of a session's run status that the worker reads.
const (
	sessionStatusRunning = "running"
	sessionStatusIdle    = "idle"
)

// agentKindLead is the agent kind of the lead's own notices and usage.
const agentKindLead = "lead"

// sessionSource is the source that Cline's session history states for a
// session that LeapMux created.
const sessionSource = "leapmux"

// errSessionHosted refuses to open a session that another agent of this
// worker runs: two runtimes of one session would each rewrite its stored
// conversation, and one would lose the other's turns.
var errSessionHosted = errors.New("another LeapMux agent already runs this Cline session; close that tab first")

// errSessionHeldElsewhere refuses to resume a session that a Cline process
// outside this agent still holds: that process and the agent's daemon would each
// rewrite the session's stored conversation.
var errSessionHeldElsewhere = errors.New("another Cline process holds this session")

// hostedSessions records the Cline sessions that an agent of this worker runs,
// by session id. One session in two daemons of one worker corrupts it, so an
// agent claims its session before it opens it.
var hostedSessions = struct {
	sync.Mutex
	owners map[string]string
}{owners: make(map[string]string)}

// claimSession records that agentID runs sessionID, or reports that another
// agent does. An agent claims through claimLocked, which records the claim so
// that the teardown releases it.
func claimSession(sessionID, agentID string) error {
	hostedSessions.Lock()
	defer hostedSessions.Unlock()
	if owner, ok := hostedSessions.owners[sessionID]; ok && owner != agentID {
		return errSessionHosted
	}
	hostedSessions.owners[sessionID] = agentID
	return nil
}

// releaseSession forgets that agentID runs sessionID.
func releaseSession(sessionID, agentID string) {
	if sessionID == "" {
		return
	}
	hostedSessions.Lock()
	defer hostedSessions.Unlock()
	if hostedSessions.owners[sessionID] == agentID {
		delete(hostedSessions.owners, sessionID)
	}
}

// claimLocked claims sessionID for the agent and records the claim. The caller
// holds sessionMu.
func (a *Agent) claimLocked(sessionID string) error {
	if err := claimSession(sessionID, a.AgentID()); err != nil {
		return err
	}
	if a.claims == nil {
		a.claims = make(map[string]bool)
	}
	a.claims[sessionID] = true
	return nil
}

// releaseLocked releases the agent's claim of sessionID, when it holds one. The
// caller holds sessionMu.
func (a *Agent) releaseLocked(sessionID string) {
	if !a.claims[sessionID] {
		return
	}
	releaseSession(sessionID, a.AgentID())
	delete(a.claims, sessionID)
}

// releaseClaims releases every claim of the agent. It waits for a session
// operation that runs, so a claim that a context clear takes during a stop is
// released too.
func (a *Agent) releaseClaims() {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	for sessionID := range a.claims {
		releaseSession(sessionID, a.AgentID())
	}
	a.claims = nil
}

// pendingModeChange is a change of settings that waits for the running turn's
// end. continuePlan records that the change is the Act mode of an approved
// plan, after which the worker asks the model to go on with the plan.
type pendingModeChange struct {
	settings     clineSettings
	continuePlan bool
}

// contributions are the client contributions of a session in mode.
func contributions(mode string) []map[string]any {
	list := []map[string]any{{
		"kind":           contributionToolExecutor,
		"capabilityName": contracts.ClineCapabilityAskQuestion,
		"executor":       executorAskQuestion,
	}}
	if mode == sessionModePlan {
		list = append(list, map[string]any{
			"kind":           contributionTool,
			"capabilityName": capabilitySwitchToActMode,
			"name":           contracts.ClineToolSwitchToActMode,
			"description":    switchToActModeDescription,
			"inputSchema":    map[string]any{"type": "object", "properties": map[string]any{}},
			"lifecycle":      map[string]any{"completesRun": true},
		})
	}
	return list
}

// sessionSpec is what one session.create states beside the settings.
type sessionSpec struct {
	// sessionID is the id the session takes, or "" for a new id.
	sessionID string
	// messages is the conversation the session starts with.
	messages json.RawMessage
	// compaction is the compaction state the session starts with.
	compaction json.RawMessage
}

// createPayload is the payload of session.create.
func (a *Agent) createPayload(settings clineSettings, spec sessionSpec) map[string]any {
	sessionConfig := map[string]any{
		"providerId": a.selection.Provider,
		"modelId":    settings.model,
	}
	if spec.sessionID != "" {
		sessionConfig["sessionId"] = spec.sessionID
	}
	runtimeOptions := map[string]any{
		"mode":                settings.sessionMode(),
		"enableSpawn":         true,
		"enableTeams":         true,
		"clientContributions": contributions(settings.sessionMode()),
	}
	for key, value := range reasoningFields(settings.effort) {
		runtimeOptions[key] = value
	}
	if extensions := settings.configExtensions(); extensions != nil {
		runtimeOptions["configExtensions"] = extensions
	}
	payload := map[string]any{
		"workspaceRoot":  a.workspaceRoot,
		"cwd":            a.opts.WorkingDir,
		"sessionConfig":  sessionConfig,
		"toolPolicies":   map[string]any{"*": map[string]any{"autoApprove": false}},
		"metadata":       map[string]any{"source": sessionSource, "interactive": true},
		"runtimeOptions": runtimeOptions,
	}
	if hasJSON(spec.messages) {
		payload["initialMessages"] = spec.messages
	}
	if hasJSON(spec.compaction) {
		payload["initialCompactionState"] = spec.compaction
	}
	return payload
}

// hasJSON reports whether raw holds a value other than null.
func hasJSON(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

// createSession creates a runtime session and returns its id.
func (a *Agent) createSession(ctx context.Context, settings clineSettings, spec sessionSpec) (string, error) {
	reply, err := a.hub.command(ctx, commandSessionCreate, spec.sessionID, a.createPayload(settings, spec))
	if err != nil {
		return "", fmt.Errorf("create the Cline session: %w", err)
	}
	var created struct {
		Session struct {
			SessionID string `json:"sessionId"`
		} `json:"session"`
	}
	if err := json.Unmarshal(reply, &created); err != nil || created.Session.SessionID == "" {
		return "", fmt.Errorf("create the Cline session: the reply states no session id")
	}
	return created.Session.SessionID, nil
}

// readMessages reads a session's stored conversation.
func (a *Agent) readMessages(ctx context.Context, sessionID string) (json.RawMessage, error) {
	reply, err := a.hub.command(ctx, commandSessionMessages, sessionID, map[string]any{"sessionId": sessionID})
	if err != nil {
		return nil, err
	}
	var read struct {
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(reply, &read); err != nil {
		return nil, fmt.Errorf("read the Cline session %s: %w", sessionID, err)
	}
	return read.Messages, nil
}

// readCompaction reads a session's compaction state, or nil when it has none.
func (a *Agent) readCompaction(ctx context.Context, sessionID string) (json.RawMessage, error) {
	reply, err := a.hub.command(ctx, commandSessionCompactionGet, sessionID, map[string]any{"sessionId": sessionID})
	if err != nil {
		return nil, err
	}
	var read struct {
		State json.RawMessage `json:"state"`
	}
	if err := json.Unmarshal(reply, &read); err != nil {
		return nil, fmt.Errorf("read the compaction state of the Cline session %s: %w", sessionID, err)
	}
	return read.State, nil
}

// sessionContext limits a session operation: several commands in sequence.
func (a *Agent) sessionContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(a.ctx, 3*a.APITimeout())
}

// openSession opens the agent's first session: a new one, or the stored
// session resumeID with its conversation, and makes it the agent's session. It
// releases each claim it took when it fails.
func (a *Agent) openSession(ctx context.Context, settings clineSettings, resumeID string) (opened string, err error) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	var claimed []string
	defer func() {
		if err != nil {
			for _, sessionID := range claimed {
				a.releaseLocked(sessionID)
			}
		}
	}()
	spec := sessionSpec{}
	if resumeID != "" {
		if err := a.claimLocked(resumeID); err != nil {
			return "", err
		}
		claimed = append(claimed, resumeID)
		holder, err := a.sessionHolder(ctx, resumeID)
		if err != nil {
			return "", err
		}
		if holder != 0 {
			return "", fmt.Errorf("%w: Cline process %d holds this session. Close it in Cline, then resume it here", errSessionHeldElsewhere, holder)
		}
		messages, err := a.readMessages(ctx, resumeID)
		if err != nil {
			if code, refused := hubErrorCode(err); refused && code == "session_not_found" {
				return "", fmt.Errorf("the Cline session %s is not in Cline's session store", resumeID)
			}
			return "", fmt.Errorf("read the Cline session %s: %w", resumeID, err)
		}
		spec = sessionSpec{sessionID: resumeID, messages: messages}
	}
	sessionID, err := a.createSession(ctx, settings, spec)
	if err != nil {
		return "", err
	}
	if sessionID != resumeID {
		a.releaseLocked(resumeID)
		if err := a.claimLocked(sessionID); err != nil {
			return "", err
		}
		claimed = append(claimed, sessionID)
	}
	if err := a.subscribe(ctx, sessionID); err != nil {
		return "", fmt.Errorf("subscribe to the Cline session: %w", err)
	}
	a.Mu.Lock()
	a.sessionID = sessionID
	a.loadedExtensions = settings.configExtensions()
	a.Mu.Unlock()
	return sessionID, nil
}

// sessionHolder returns the process that holds sessionID outside this agent, or
// 0 when none does. Cline's row of the session states the process that last ran
// it (`metadata.pid`) and a status: `running`, `pending` and `idle` keep the
// session resident in that process, and `completed`, `failed` and `aborted` do
// not. The agent's own daemon holds nothing that matters, and the claims cover
// every other agent of this worker, so a holder is another worker's daemon, a
// daemon that a stopped worker left, or the user's own Cline.
//
// A session that Cline does not know has no holder, and the read of its
// conversation then states that it is missing.
func (a *Agent) sessionHolder(ctx context.Context, sessionID string) (int, error) {
	reply, err := a.hub.command(ctx, commandSessionGet, sessionID, map[string]any{"sessionId": sessionID})
	if err != nil {
		if code, refused := hubErrorCode(err); refused && code == "session_not_found" {
			return 0, nil
		}
		return 0, fmt.Errorf("read the Cline session %s: %w", sessionID, err)
	}
	var read struct {
		Session struct {
			Status   string `json:"status"`
			Metadata struct {
				PID int `json:"pid"`
			} `json:"metadata"`
		} `json:"session"`
	}
	if err := json.Unmarshal(reply, &read); err != nil {
		return 0, fmt.Errorf("read the Cline session %s: %w", sessionID, err)
	}
	pid := read.Session.Metadata.PID
	switch {
	case !residentSessionStatuses[read.Session.Status]:
		return 0, nil
	case pid <= 0 || pid == a.record.PID:
		return 0, nil
	case !providerkit.ProcessRuns(pid):
		return 0, nil
	default:
		return pid, nil
	}
}

// residentSessionStatuses are the statuses of a session that a Cline process
// keeps resident (mapLocalStatusToHubStatus in hub-session-records.ts of Cline
// 3.0.64).
var residentSessionStatuses = map[string]bool{
	sessionStatusRunning: true,
	"pending":            true,
	sessionStatusIdle:    true,
}

// rebuildSession builds the session again with settings, under the same id and
// with the same conversation and compaction state. The caller holds sessionMu,
// and no turn runs.
//
// A rebuild that fails after the detach builds the session again with the
// settings it had, so the agent keeps a session.
func (a *Agent) rebuildSession(settings clineSettings) error {
	ctx, cancel := a.sessionContext()
	defer cancel()
	a.Mu.Lock()
	sessionID, have := a.sessionID, a.settings
	a.Mu.Unlock()
	messages, err := a.readMessages(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("read the conversation to rebuild: %w", err)
	}
	compaction, err := a.readCompaction(ctx, sessionID)
	if err != nil {
		// The conversation alone still carries the session; the next compaction
		// then starts from nothing.
		slog.Warn("cline read the compaction state to rebuild", "agent_id", a.AgentID(), "error", err)
	}
	if _, err := a.hub.command(ctx, commandSessionDetach, sessionID, map[string]any{"sessionId": sessionID}); err != nil {
		return fmt.Errorf("detach the session to rebuild: %w", err)
	}
	spec := sessionSpec{sessionID: sessionID, messages: messages, compaction: compaction}
	if _, err := a.createSession(ctx, settings, spec); err != nil {
		if _, restoreErr := a.createSession(ctx, have, spec); restoreErr != nil {
			return fmt.Errorf("rebuild the session: %w; restore it: %v", err, restoreErr)
		}
		a.Mu.Lock()
		a.loadedExtensions = have.configExtensions()
		a.Mu.Unlock()
		return fmt.Errorf("rebuild the session: %w", err)
	}
	a.Mu.Lock()
	a.settings = settings
	a.loadedExtensions = settings.configExtensions()
	a.Mu.Unlock()
	return nil
}

// setPlanExitMode records the mode that an approved plan switches the session
// to when the turn ends: the mode the user picked, or Act.
func (a *Agent) setPlanExitMode(mode string) {
	if !validPermissionMode(mode) || mode == contracts.ClinePermissionModePlan {
		mode = contracts.ClinePermissionModeAct
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	next := a.settings
	if a.modeRebuild != nil {
		next = a.modeRebuild.settings
	}
	next.permissionMode = mode
	a.modeRebuild = &pendingModeChange{settings: next, continuePlan: true}
}

// afterTurn starts the mode change that waited for the turn that just ended.
// endTurn armed a settling turn for it, so the input queue holds the next
// message until the session is ready. The caller holds dispatchMu, so the
// change runs on a goroutine of its own: it waits for replies. continuePlan
// states that the turn approved a plan, and that the model goes on with it in
// Act mode.
func (a *Agent) afterTurn(continuePlan bool) {
	a.background.Add(1)
	go func() {
		defer a.background.Done()
		a.applyModeChange(continuePlan)
	}()
}

// applyModeChange applies the change that waits for the turn's end, reports
// the settings that apply, and ends the settling turn that held the input
// queue. After an approved plan it asks the model to go on in Act mode
// instead, as Cline's CLI does, and that request is the next turn. An
// interrupt of the settling turn stops that request: the user stopped the
// agent, and the approved mode applies with no continuation.
//
// It takes the change when it holds sessionMu, not when the turn ends: a
// settings change that comes between the two joins the change (UpdateSettings),
// and the rebuild then applies the user's last choice. It ends the settling
// turn before it releases sessionMu, so a settings change that waits for it
// finds the new session with no turn, and rebuilds it at once.
func (a *Agent) applyModeChange(continuePlan bool) {
	if a.ctx.Err() != nil {
		return
	}
	a.sessionMu.Lock()
	a.Mu.Lock()
	change := a.modeRebuild
	a.modeRebuild = nil
	before, loaded := a.settings, a.loadedExtensions
	a.Mu.Unlock()
	var err error
	switch {
	case change == nil:
		// A settings change during the settling turn needs no new runtime any
		// more.
	case before.needsRebuild(change.settings, loaded):
		err = a.rebuildSession(change.settings)
	default:
		a.applyLive(before, change.settings)
	}
	a.Mu.Lock()
	after, sessionID := a.settings, a.sessionID
	continuePlan = continuePlan && err == nil && after.sessionMode() == sessionModeAct && !a.turn.interruptRequested
	if continuePlan {
		a.turn = turnState{active: true, steerable: true, startedAt: a.clock.Now()}
	} else {
		a.turn = turnState{}
	}
	a.Mu.Unlock()
	a.sessionMu.Unlock()
	if err != nil {
		slog.Error("cline apply the mode change", "agent_id", a.AgentID(), "error", err)
	}
	if before.permissionMode != after.permissionMode {
		a.sink.UpdatePermissionMode(after.permissionMode)
	}
	a.sink.PersistSettingsRefresh(agent.CurrentOptions(a.OptionGroups()))
	a.PublishTurnActive()
	if !continuePlan {
		return
	}
	input := clineInput{prompt: actModeContinuationPrompt}
	if err := a.deliver(sessionID, input.payload(sessionID, after.sessionMode(), "")); err != nil {
		slog.Warn("cline continue the approved plan", "agent_id", a.AgentID(), "error", err)
		if !errors.Is(err, agent.ErrDeliveryUncertain) {
			a.disarmTurn()
		}
	}
}

// ClearContext opens a new session in the agent's mode, and leaves the old one.
// The old session's turn ends as an interruption, and its requests are
// withdrawn.
func (a *Agent) ClearContext() (string, error) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	a.Mu.Lock()
	oldSessionID, settings, busy := a.sessionID, a.settings, a.turn.active
	a.Mu.Unlock()

	ctx, cancel := a.sessionContext()
	defer cancel()
	if busy && oldSessionID != "" {
		if _, err := a.hub.command(ctx, commandRunAbort, oldSessionID, map[string]any{"sessionId": oldSessionID, "reason": "The user cleared the context."}); err != nil {
			slog.Warn("cline abort the previous session's turn", "agent_id", a.AgentID(), "error", err)
		}
	}
	sessionID, err := a.createSession(ctx, settings, sessionSpec{})
	if err != nil {
		return "", err
	}
	if err := a.claimLocked(sessionID); err != nil {
		// The new session belongs to this client, and nothing drives it.
		if _, detachErr := a.hub.command(ctx, commandSessionDetach, sessionID, map[string]any{"sessionId": sessionID}); detachErr != nil {
			slog.Warn("cline detach a session that another agent claimed", "agent_id", a.AgentID(), "error", detachErr)
		}
		return "", err
	}
	if err := a.subscribe(ctx, sessionID); err != nil {
		a.releaseLocked(sessionID)
		return "", fmt.Errorf("subscribe to the new Cline session: %w", err)
	}

	// Close out the previous session's output BEFORE the switch, while its rows
	// still belong to the transcript they started in.
	a.dispatchMu.Lock()
	a.Mu.Lock()
	lead := a.out.lead
	a.Mu.Unlock()
	a.withdrawAllControls()
	a.settleSpawns(agent.MessageCompletionInterrupted)
	a.flushPending(lead, oldSessionID, agent.MessageCompletionInterrupted)
	a.closeOpenTools(lead, agent.MessageCompletionInterrupted)
	a.closeTeamRuns(bgtask.StatusStopped)
	a.Mu.Lock()
	a.sessionID = sessionID
	a.loadedExtensions = settings.configExtensions()
	a.turn = turnState{}
	a.modeRebuild = nil
	a.out = newOutputState()
	a.team = teamState{}
	a.contextUsage = nil
	a.Mu.Unlock()
	a.dispatchMu.Unlock()
	a.sink.ResetSpans()
	a.PublishTurnActive()
	a.sink.ReportProgress(agent.ResetProgress())

	if oldSessionID != "" {
		a.hub.unsubscribe(oldSessionID)
		if _, err := a.hub.command(ctx, commandSessionDetach, oldSessionID, map[string]any{"sessionId": oldSessionID}); err != nil {
			slog.Warn("cline detach the previous session", "agent_id", a.AgentID(), "error", err)
		}
		a.releaseLocked(oldSessionID)
	}
	a.sink.UpdateSessionID(sessionID)
	return sessionID, nil
}
