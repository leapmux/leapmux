package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// zcodeAgent manages a single `zcode app-server --stdio` process.
//
// The wire format is line-delimited JSON that resembles JSON-RPC 2.0 without
// being it, so -- like PiAgent -- this type does NOT embed jsonrpcBase. It shares
// only the pending-map mechanics, through responseCorrelator[int64]. See
// zcode_protocol.go for the framing rules and zcode_rpc.go for the transport.
type zcodeAgent struct {
	processBase
	responseCorrelator[int64]

	// nextReqID mints the monotonic request ids the correlator keys on.
	nextReqID atomic.Int64
	// dispatchMu serializes dispatchZCodeEvent across the read loop and a replaying
	// re-subscribe. See that function for why the whole body needs it.
	dispatchMu sync.Mutex
	// Control requests can arrive concurrently while the server repeats an unanswered request.
	controlMu sync.Mutex

	sink       ProviderServices
	workingDir string
	workspace  zcodeWorkspace

	// catalog is the translated view of ZCode's own configuration: the provider
	// registry payload (with inline API keys) and the per-model capabilities. Read
	// once at startup and never mutated, so it needs no lock.
	catalog zcodeCatalog
	// registryRevision labels this agent's registry push. The app-server compares
	// `generatedAt` between pushes and ignores an older snapshot, so the revision
	// only has to identify the pusher.
	registryRevision string

	// --- guarded by mu ---

	sessionID string
	// stateRevision is the app-server's optimistic-concurrency counter, taken
	// from the last runtime state observed. session/goal sends it as
	// expectedRevision so a goal write that races a change the agent just made
	// is refused rather than silently overwriting it.
	stateRevision int64
	model         string // the composite catalog id, providerId/modelId
	thoughtLevel  string
	mode          string
	// modeObserved is true once the RUNNING session reported its own mode through
	// `settings.mode.current`. Until then `mode` holds what the launch asked for, not
	// what the app-server settled on, and the two must not be compared. It resets with
	// the session, because the next one reports its own.
	modeObserved bool
	// unresolvedSettings records successful setter calls whose response omitted
	// the authoritative axis snapshot.
	unresolvedSettings map[string]struct{}
	// lastSeq is the highest event sequence number observed. A re-subscribe asks
	// for events after it, so a subscription that lapsed replays what it missed
	// instead of dropping it.
	lastSeq    int64
	turnActive bool
	// backgroundTurn is true while the running turn was started by a background
	// task rather than by the user. Such a turn must not end the user's turn.
	backgroundTurn bool
	// stoppedTurnTimer ends a turn whose stop the app-server accepted and then never
	// reported. Armed by Interrupt, restarted by every session event, dropped when the
	// turn ends on its own. Guarded by a.mu. See armStoppedZCodeTurn.
	stoppedTurnTimer *time.Timer
	// afterFunc is time.AfterFunc. A test replaces it to fire the window itself.
	afterFunc func(time.Duration, func()) *time.Timer

	// observedThoughtLevels is the thought-level list the app-server reports for
	// the CURRENT model (settings.thoughtLevel.available). It is authoritative and
	// model-dependent, so it overrides the catalog's variants for the running model.
	observedThoughtLevels  []*EffortInfo
	observedThoughtDefault string

	// toolCalls holds everything a.mu knows about each tool call, keyed by its id.
	toolCalls     map[string]*zcodeToolCall
	nextToolOrder uint64

	// pendingControls maps the request id of each control prompt LeapMux forwarded to the
	// user, and that nothing resolved yet, to the payload of its FIRST announcement. It
	// answers two questions.
	//
	// It de-duplicates the app-server's RE-ANNOUNCEMENTS. An unanswered interaction
	// request is re-sent every second -- the interval doubles to ten -- with the same
	// `requestId` and a FRESH wire id, until it is answered. Each repeat republishes the
	// STORED payload, so a banner the user already holds is left alone (the frontend
	// de-duplicates on the payload) and one that is gone comes back.
	//
	// It also tells an echo apart from an automatic decision: the app-server emits
	// `permission.resolved` for EVERY decision, including the one it just received
	// from us.
	pendingControls map[string]json.RawMessage

	// latestContextUsage is the broadcast-shaped context-usage map, kept so a
	// reconnecting frontend can be rehydrated from the persisted turn end.
	latestContextUsage map[string]any
	contextWindow      int64
	// sessionCostUsd is the accrued cost the app-server reports on
	// runtime.contextUsage. sessionCostKnown distinguishes "no cost reported" from a
	// genuine zero, which a free plan does report.
	sessionCostUsd   float64
	sessionCostKnown bool

	generationBuffer GenerationBuffer
	// toolCallPrompts holds an Agent spawn's full prompt until the child transcript
	// exists to receive it as its first message.
	toolCallPrompts pendingPrompts
	// children maps a subagent spawn's tool-call id to the child transcript that
	// holds that subagent's work. See zcode_subagent.go.
	children zcodeChildIndex
}

// zcodeStopTimeout limits a session/stop. The app-server aborts the turn's
// controller synchronously and replies with an empty object, so a longer wait only
// makes a user-driven interrupt look slower than it is.
const zcodeStopTimeout = 2 * time.Second

// zcodeSendRetryWindow limits how long SendInput retries a send the app-server
// refused because a turn is already running. The refusal is transient -- the turn
// ends -- but the wait needs a limit, because SendInput must return long
// before the browser's own deadline.
const (
	zcodeSendRetryWindow   = 3 * time.Second
	zcodeSendRetryInterval = 250 * time.Millisecond
)

// StartZCode starts a ZCode app-server and performs the startup handshake.
func StartZCode(ctx context.Context, opts Options, sink ProviderServices) (Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	// Resolve the launch FIRST. A machine with no ZCode at all fails both this and the
	// catalog load below, and "ZCode is not installed on this machine" is the honest one:
	// the other tells the user to sign in with an application they do not have.
	spec, err := resolveProviderLaunch(ctx, opts.Shell, opts.LoginShell, leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE)
	if err != nil {
		cancel()
		return nil, err
	}

	// ZCode's credentials are read BEFORE the process starts. Without a provider
	// registry every turn fails with a message that identifies the app-server rather
	// than the missing configuration, so the real cause is reported here instead.
	catalog, err := loadZCodeCatalog(opts.HomeDir)
	if err != nil {
		cancel()
		return nil, err
	}

	// The app-server has no working-directory flag: it takes the workspace path in
	// every request. buildShellWrappedCommand still sets cmd.Dir, which is what the
	// tools it runs inherit.
	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(ctx, shellWrapSpec{
		Shell:      opts.Shell,
		LoginShell: opts.LoginShell,
		Launch:     spec,
		BaseArgs:   []string{"app-server", "--stdio"},
		WorkingDir: opts.WorkingDir,
	})
	// buildShellWrappedCommand already prepended spec.PrefixArgs and seeded
	// spec.Env onto cmd, so this is the same finalization every provider runs.
	cmd.Env = FinalizeAgentEnv(cmd.Environ(), opts)

	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &zcodeAgent{
		processBase:      newProcessBase(opts, "zcode", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix),
		sink:             sink,
		workingDir:       opts.WorkingDir,
		workspace:        zcodeWorkspaceFor(opts.WorkingDir),
		catalog:          catalog,
		registryRevision: "leapmux-" + opts.AgentID,
		mode:             contracts.ZCodeDefaultMode,
		toolCalls:        map[string]*zcodeToolCall{},
		pendingControls:  map[string]json.RawMessage{},
	}
	a.sink = newModelProgressResetSink(a.sink)
	storeLocation := zcodeToolStorePaths(StoredSessionQuery{HomeDir: opts.HomeDir, WorkingDir: opts.WorkingDir})
	a.sink = newZCodeToolTranscript(ctx, a.sink, func() zcodeToolStoreLocation {
		a.mu.Lock()
		defer a.mu.Unlock()
		location := storeLocation
		location.sessionID = a.sessionID
		return location
	})
	// A requested model may be spelled bare ("GLM-5.3"); the catalog resolves it to
	// the composite id the option groups carry. An unresolvable request leaves the
	// model empty, and the app-server then picks the registry default -- which the
	// settings snapshot reports back, so the user still sees the truth.
	if model, ok := catalog.resolveModelID(opts.Model()); ok {
		a.model = model
	}
	a.thoughtLevel = opts.Effort()
	if mode := opts.PermissionMode(); mode != "" {
		a.mode = mode
	}
	// Capture the launch request BEFORE the session exists. Opening one folds the
	// app-server's own settings over these fields, so this is the last point at which
	// what the USER asked for is still readable. applyStartupSettings takes it back.
	launchRequest := zcodeSettingsRequest{Model: a.model, ThoughtLevel: a.thoughtLevel, Mode: a.mode}

	if err := a.startCmd(cmd, cancel); err != nil {
		return nil, err
	}
	a.drainStderr(stderrPipe)

	scanner := newStdoutScanner(stdout)
	go a.readOutput(scanner, a.interceptResponse, a.handleOutput)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}
	timeout := opts.startupTimeout()

	if err := a.pushProviderRegistry(timeout); err != nil {
		cleanup()
		return nil, a.formatStartupError(ZCodeMethodUpdateProviderRegistry, err)
	}

	if err := a.openSession(opts.ResumeSessionID, timeout); err != nil {
		cleanup()
		return nil, a.formatStartupError("session open", err)
	}

	// Model, thought level and mode are applied AFTER the session exists, and each
	// setter reports the value the app-server settled on rather than the requested
	// one. A failure here is not fatal: the session runs on the app-server's own
	// choice, which the snapshot already recorded, and the user can change it.
	a.applyStartupSettings(launchRequest, timeout)

	if err := a.subscribe(timeout); err != nil {
		cleanup()
		return nil, a.formatStartupError(ZCodeMethodSessionSubscribe, err)
	}

	a.mu.Lock()
	sessionID := a.sessionID
	a.mu.Unlock()
	// a.sink, never the raw constructor parameter: a.sink is the thinking-reset wrapper
	// installed above, and a call that holds the pre-wrap reference bypasses whatever the
	// wrapper overrides.
	a.sink.UpdateSessionID(sessionID)
	a.sink.BroadcastStatusActive(sessionID)

	return a, nil
}

// pushProviderRegistry hands the app-server the model providers it may use.
func (a *zcodeAgent) pushProviderRegistry(timeout time.Duration) error {
	params := a.catalog.registryPayload(a.workspace, a.registryRevision, time.Now().UnixMilli())
	raw, err := a.sendZCodeRequest(ZCodeMethodUpdateProviderRegistry, params, timeout)
	if err != nil {
		return err
	}
	var resp struct {
		Status        string `json:"status"`
		ProviderCount int    `json:"providerCount"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		// The registry was accepted (no error object) but the acknowledgement did not
		// parse. That is a diagnostic loss, not a startup failure.
		slog.Warn("zcode provider registry response unmarshal failed", "agent_id", a.agentID, "error", err)
		return nil
	}
	if resp.Status == "failed" {
		return fmt.Errorf("the app-server refused the provider registry")
	}
	slog.Debug("zcode provider registry applied", "agent_id", a.agentID, "status", resp.Status, "providers", resp.ProviderCount)
	return nil
}

// zcodeStateSnapshot is the state document every session RPC that changes
// something returns: session/create, session/resume, and each setter.
type zcodeStateSnapshot struct {
	Session struct {
		SessionID string `json:"sessionId"`
		Mode      string `json:"mode"`
		Title     string `json:"title"`
		// Target is where the app-server actually states the session goal. ZCode
		// calls the goal a target in its storage and runtime, and the shipped
		// build sends it HERE and nowhere else: a session/create reply carries
		// `session.target: null`, and a session/goal reply carries the whole
		// object. Raw for the same three-case reason as Goal below.
		Target json.RawMessage `json:"target"`
	} `json:"session"`
	Settings *zcodeSettingsSnapshot `json:"settings"`
	// Runtime carries stateRevision as well as eventSeq, because session/create
	// and session/resume are the ONLY places a resumed session learns its
	// revision before a turn ends. Reading eventSeq alone left stateRevision at
	// 0, and session/goal then sent expectedRevision 0 against a live session
	// whose revision was higher -- the app-server refused every goal action
	// with a conflict that did not exist.
	Runtime struct {
		EventSeq      int64 `json:"eventSeq"`
		StateRevision int64 `json:"stateRevision"`
	} `json:"runtime"`
	Projection struct {
		ContextUsed   int64 `json:"contextUsed"`
		ContextWindow int64 `json:"contextWindow"`
	} `json:"projection"`
	// Goal is RAW so the three cases stay distinct: absent (key missing),
	// null (no goal), and an object. A decoded pointer would fold the first
	// two together, and a snapshot with no goal must CLEAR one that a previous
	// process stored.
	//
	// The shipped build sends no `goal` key at all -- see Session.Target, which
	// is where it puts one. This stays because it costs nothing and a build that
	// does send the documented key keeps working.
	Goal json.RawMessage `json:"goal"`
}

// goalState returns the goal a state document states, from whichever key carries
// it.
//
// The `goal` key is what ZCode's protocol documents. `session.target` is what the
// shipped app-server sends, under the name its own storage uses. A document that
// carries both is read from `goal`, because that is the documented one.
func (s zcodeStateSnapshot) goalState() json.RawMessage {
	if len(s.Goal) > 0 {
		return s.Goal
	}
	return s.Session.Target
}

// openSession creates a fresh session, or resumes the one the client specified.
//
// A resume that does not hold fails the whole start. See resumeFailedError.
func (a *zcodeAgent) openSession(resumeID string, timeout time.Duration) error {
	if resumeID != "" {
		params := map[string]any{
			"sessionId": resumeID,
			"workspace": a.workspace,
		}
		raw, err := a.sendZCodeRequest(ZCodeMethodSessionResume, params, timeout)
		if err != nil {
			return resumeFailedError(resumeID, err)
		}
		// "unknown" is the app-server's placeholder for "no session exists", so a
		// reply that carries it describes no session -- although it still holds a
		// sequence, a mode and a context window. The id is tested BEFORE the fold
		// for that reason, and an unusable document is abandoned WHOLE: a carried
		// eventSeq becomes a watermark that makes dispatchZCodeEvent drop later
		// events as duplicates, and `yolo` is a mode that nothing asked for.
		snap, ok := a.parseStateSnapshot(raw)
		if !ok || !zcodeUsableSessionID(snap.Session.SessionID) {
			return resumeFailedError(resumeID, fmt.Errorf("%s returned no session id", ZCodeMethodSessionResume))
		}
		a.applyParsedStateSnapshot(snap)
		// A resume RESTATES the session's goal, so it is reported as a snapshot:
		// it updates the panel and writes no transcript row for a goal that may
		// be hours old. Outside applyParsedStateSnapshot, which holds a.mu for
		// its whole body and must not call into the sink.
		a.reportZCodeGoal(snap.goalState(), true)
		return nil
	}

	params := map[string]any{"workspace": a.workspace}
	a.mu.Lock()
	mode, model := a.mode, a.model
	a.mu.Unlock()
	if mode != "" {
		params["mode"] = mode
	}
	if ref, ok := a.catalog.refs[model]; ok {
		params["model"] = ref
	}
	raw, err := a.sendZCodeRequest(ZCodeMethodSessionCreate, params, timeout)
	if err != nil {
		return err
	}
	snap, ok := a.parseStateSnapshot(raw)
	if !ok || !zcodeUsableSessionID(snap.Session.SessionID) {
		return fmt.Errorf("%s returned no session id", ZCodeMethodSessionCreate)
	}
	a.applyParsedStateSnapshot(snap)
	// A create RESTATES the session's goal -- normally that it has none, which
	// is what clears one a previous session left on the row.
	a.reportZCodeGoal(snap.goalState(), true)
	return nil
}

// applyStartupSettings pins the model, thought level and mode the launch asked
// for. Each step is best-effort and reports what the app-server settled on.
func (a *zcodeAgent) applyStartupSettings(req zcodeSettingsRequest, timeout time.Duration) {
	// Re-pin the REQUEST before any setter runs. `openSession` folded the create/resume
	// reply through applySettingsSnapshotLocked, which overwrote a.thoughtLevel with the
	// app-server's own `thoughtLevel.current` -- its fallback, the LOWEST level, not the
	// default the model declares. Reading the fields back after that fold would compare
	// the observed level against itself, so a launch that asked for `max` would send no
	// setter, and Auto would never reach the catalog default that applyZCodeModel
	// resolves it to. Where the launch asked for nothing, the observed value stands.
	a.mu.Lock()
	// The mode the opened session RUNS in, read before the request is pinned over it.
	// `settings.mode.current` is the app-server's own live value, so it decides whether
	// the mode setter below has any work to do -- and modeObserved says whether the
	// opened session reported that value at all.
	observedMode, modeObserved := a.mode, a.modeObserved
	if req.Model != "" {
		a.model = req.Model
	}
	if req.ThoughtLevel != "" {
		a.thoughtLevel = req.ThoughtLevel
	}
	if req.Mode != "" {
		a.mode = req.Mode
	}
	// The model and the level fall back to the observed value, because the app-server
	// resolves both and a launch that asked for neither still runs on something. The
	// MODE does not: a launch that asked for no mode has nothing to pin, and the mode
	// the opened session chose is already the right one.
	model, level, mode := a.model, a.thoughtLevel, req.Mode
	a.mu.Unlock()

	if model != "" {
		if err := a.applyZCodeModel(model, timeout); err != nil {
			slog.Warn("zcode setModel on startup failed", "agent_id", a.agentID, "model", model, "error", err)
		}
	}
	// EffortAuto is LeapMux's sentinel for "send no thought level at all", so the
	// app-server keeps whatever default it resolved for the model.
	if level != "" && level != EffortAuto {
		if err := a.applyZCodeThoughtLevel(level, timeout); err != nil {
			slog.Warn("zcode setThoughtLevel on startup failed", "agent_id", a.agentID, "level", level, "error", err)
		}
	}
	// session/create HONORS its mode parameter, and openSession sends it, so the
	// session usually already runs in the requested mode and the setter is one
	// blocking RPC of pure repetition. The comparison is against
	// `settings.mode.current` from the create or resume reply -- the app-server's live
	// mode -- and never against `session.mode`, which reports the projection's seed
	// (`build`) whatever the session runs in. A reply that reported no mode is no
	// evidence, and the setter runs: the cost of a redundant RPC is a round trip, and
	// the cost of a wrong skip is a session that runs in another session's mode.
	if mode != "" && (!modeObserved || mode != observedMode) {
		if err := a.applyZCodeMode(mode, timeout); err != nil {
			slog.Warn("zcode setMode on startup failed", "agent_id", a.agentID, "mode", mode, "error", err)
		}
	}
}

// subscribe opens the event stream.
//
// `includeSnapshot` stays false: a snapshot is O(messages) and building it is the
// main cause of subscribe timeouts on a long session. `afterSeq` is the sequence
// the state snapshot already reported, so a RESUMED session does not replay its
// whole history into a transcript LeapMux already persisted.
func (a *zcodeAgent) subscribe(timeout time.Duration) error {
	a.mu.Lock()
	sessionID, afterSeq := a.sessionID, a.lastSeq
	a.mu.Unlock()

	raw, err := a.sendZCodeRequest(ZCodeMethodSessionSubscribe, map[string]any{
		"sessionId":       sessionID,
		"deliveryKind":    ZCodeDeliveryContinuous,
		"includeSnapshot": false,
		"afterSeq":        afterSeq,
	}, timeout)
	if err != nil {
		return err
	}
	var resp struct {
		EventSeq int64                `json:"eventSeq"`
		Events   []zcodeEventEnvelope `json:"events"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		slog.Warn("zcode subscribe response unmarshal failed", "agent_id", a.agentID, "error", err)
		return nil
	}
	// Events the subscription replayed arrive in the RESPONSE, not as notifications,
	// so they are dispatched here or they are lost.
	for _, event := range resp.Events {
		a.dispatchZCodeEvent(event)
	}
	a.mu.Lock()
	if resp.EventSeq > a.lastSeq {
		a.lastSeq = resp.EventSeq
	}
	a.mu.Unlock()
	return nil
}

// applyStateSnapshot folds a state document into the agent's own state.
//
// It does NOT report the goal, and that omission is the point. Three settings
// setters fold their reply documents through here -- applyZCodeModel,
// applyZCodeThoughtLevel and applyZCodeMode -- so reporting the goal from this
// function would let a model, effort or permission-mode change write the goal
// columns. Worse, reportZCodeGoal reads a `null` goal as "the goal is gone", so
// a settings reply that spells it would DELETE a live goal and broadcast the
// removal with no transcript row.
//
// The goal is reported from the two calls that genuinely restate a session,
// openSession's create and resume branches, which is where a session snapshot
// is the authority on what the goal is.
func (a *zcodeAgent) applyStateSnapshot(raw json.RawMessage) (zcodeStateSnapshot, bool) {
	if snap, ok := a.parseStateSnapshot(raw); ok {
		a.applyParsedStateSnapshot(snap)
		return snap, true
	}
	return zcodeStateSnapshot{}, false
}

// parseStateSnapshot decodes a state document. ok is false for an absent or a
// malformed document, which is a diagnostic loss rather than a failure.
//
// The parse is separate from the fold because openSession must READ a resume reply
// before it decides to keep it, and a document that it abandons must leave nothing
// behind.
func (a *zcodeAgent) parseStateSnapshot(raw json.RawMessage) (zcodeStateSnapshot, bool) {
	var snap zcodeStateSnapshot
	if len(raw) == 0 {
		return snap, false
	}
	// A session/goal reply is not a bare state document: it wraps one under
	// `snapshot`, beside its own `response` text and a `startedTurn` flag.
	// Parsing that envelope as a document read nothing at all -- not the goal,
	// and not the `stateRevision` the next goal action needs for its
	// expectedRevision. Unwrapping here keeps one parser for both shapes.
	var envelope struct {
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if json.Unmarshal(raw, &envelope) == nil && len(envelope.Snapshot) > 0 {
		raw = envelope.Snapshot
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		slog.Warn("zcode state snapshot unmarshal failed", "agent_id", a.agentID, "error", err)
		return snap, false
	}
	return snap, true
}

// applyParsedStateSnapshot folds a decoded state document into the agent's state.
func (a *zcodeAgent) applyParsedStateSnapshot(snap zcodeStateSnapshot) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if zcodeUsableSessionID(snap.Session.SessionID) {
		if snap.Session.SessionID != a.sessionID {
			// Sequence and revision counters belong to one native session.
			a.lastSeq = 0
			a.stateRevision = 0
			a.modeObserved = false
		}
		a.sessionID = snap.Session.SessionID
	}
	if snap.Runtime.EventSeq > a.lastSeq {
		a.lastSeq = snap.Runtime.EventSeq
	}
	// Monotonic, for the reason noteZCodeStateRevision gives. Written inline
	// rather than through it because this function already holds a.mu.
	if snap.Runtime.StateRevision > a.stateRevision {
		a.stateRevision = snap.Runtime.StateRevision
	}
	if snap.Projection.ContextWindow > 0 {
		a.contextWindow = snap.Projection.ContextWindow
	}
	if snap.Settings != nil && snap.Settings.Model != nil && snap.Settings.Model.Current.ModelID != "" {
		delete(a.unresolvedSettings, OptionIDModel)
	}
	if snap.Settings != nil && snap.Settings.ThoughtLevel != nil &&
		(!snap.Settings.ThoughtLevel.Enabled || snap.Settings.ThoughtLevel.Current != "") {
		delete(a.unresolvedSettings, OptionIDEffort)
	}
	if snap.Settings != nil && snap.Settings.Mode != nil && snap.Settings.Mode.Current != "" {
		delete(a.unresolvedSettings, OptionIDPermissionMode)
	}
	a.applySettingsSnapshotLocked(snap.Settings)
}

func (a *zcodeAgent) markSettingUnresolved(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.unresolvedSettings == nil {
		a.unresolvedSettings = make(map[string]struct{})
	}
	a.unresolvedSettings[id] = struct{}{}
}

// zcodeUsableSessionID reports whether a state document's session id can be
// adopted. The app-server builds that field as
// `String(session?.id ?? app?.sessionId ?? "unknown")`, so "unknown" is its
// placeholder for a document that belongs to no session, and an RPC that carries
// the placeholder back is rejected.
func zcodeUsableSessionID(id string) bool {
	return id != "" && id != "unknown"
}

// SendInput delivers a user message.
//
// It returns on the app-server's `{accepted:true}` acknowledgement and NEVER waits
// for the turn -- the Agent.SendInput contract. A refusal because a turn is already
// running is retried briefly, because it is transient by construction.
func (a *zcodeAgent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(content, attachments, "")
}

// SupportsSteering always reports true. The ZCode app-server accepts
// session/send with requestedDelivery:"guide" during any turn, so the
// capability needs no handshake discovery.
func (a *zcodeAgent) SupportsSteering() bool { return true }

func (a *zcodeAgent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(content, attachments, "guide")
}

func (a *zcodeAgent) sendInput(content string, attachments []*leapmuxv1.Attachment, requestedDelivery string) error {
	return a.sendInputForSession(nil, content, attachments, requestedDelivery)
}

func (a *zcodeAgent) sendInputForSession(expected *string, content string, attachments []*leapmuxv1.Attachment, requestedDelivery string) error {
	a.mu.Lock()
	if err := checkInputSession(expected, a.sessionID); err != nil {
		a.mu.Unlock()
		return err
	}
	if a.stopped {
		a.mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	sessionID, model, turnActive := a.sessionID, a.model, a.turnActive
	a.mu.Unlock()
	if sessionID == "" {
		return fmt.Errorf("agent has no ZCode session")
	}
	if requestedDelivery == "" && turnActive {
		return ErrAgentBusy
	}
	if requestedDelivery != "" && !turnActive {
		return ErrNoActiveTurn
	}

	text, wire, err := a.buildZCodeInput(content, attachments, model)
	if err != nil {
		return err
	}

	params := map[string]any{
		"sessionId": sessionID,
		"content":   text,
		"inputId":   generateRequestID(),
	}
	if len(wire) > 0 {
		params["attachments"] = wire
	}
	if requestedDelivery != "" {
		params["requestedDelivery"] = requestedDelivery
	}

	deadline := time.Now().Add(zcodeSendRetryWindow)
	for {
		raw, err := a.sendZCodeRequest(ZCodeMethodSessionSend, params, a.APITimeout())
		if err == nil {
			var ack struct {
				Accepted bool `json:"accepted"`
			}
			if json.Unmarshal(raw, &ack) == nil && !ack.Accepted {
				return fmt.Errorf("the app-server did not accept the message")
			}
			if requestedDelivery != "" {
				a.mu.Lock()
				stillActive := a.turnActive
				a.mu.Unlock()
				if !stillActive {
					return ErrNoActiveTurn
				}
			}
			return nil
		}
		if !zcodeIsPromptRunning(err) || time.Now().After(deadline) {
			return classifyZCodeInputDeliveryError(err)
		}
		select {
		case <-time.After(zcodeSendRetryInterval):
		case <-a.processDone:
			return a.processExitError()
		case <-a.ctx.Done():
			return a.ctx.Err()
		}
	}
}

func classifyZCodeInputDeliveryError(err error) error {
	var responseErr *zcodeError
	if errors.As(err, &responseErr) {
		return err
	}
	return fmt.Errorf("%w: ZCode did not confirm session/send delivery: %v", ErrDeliveryUncertain, err)
}

// zcodeStoppedSilenceWindow is how long a stopped turn may say NOTHING before
// LeapMux ends it locally.
//
// The window has to outlast the gaps a live turn leaves between its own frames. A
// census of a hundred and twenty reads left gaps up to eighteen seconds while the
// agent was plainly still working, so thirty seconds is the margin above that.
const zcodeStoppedSilenceWindow = 30 * time.Second

// armStoppedZCodeTurn starts the window that ends a turn the app-server accepted a
// stop for and then never reported.
//
// It replaces an immediate local end. The app-server accepts `session/stop` in both
// of the cases LeapMux must tell apart: the one where the abort cuts the model
// stream, and the one where the turn goes on making tool calls for minutes. Ending
// the turn on the accepted stop alone read the second case as an idle chat while the
// agent was still writing to it.
//
// Silence is the signal, not elapsed time. Every session event restarts the window,
// so a turn that still speaks keeps the indicator it has earned, and a turn that
// stops speaking ends once.
func (a *zcodeAgent) armStoppedZCodeTurn() {
	a.mu.Lock()
	if !a.turnActive {
		a.mu.Unlock()
		return
	}
	after := a.afterFunc
	if after == nil {
		after = time.AfterFunc
	}
	if a.stoppedTurnTimer != nil {
		a.stoppedTurnTimer.Stop()
	}
	a.stoppedTurnTimer = after(zcodeStoppedSilenceWindow, a.endStoppedZCodeTurn)
	a.mu.Unlock()
}

// refreshStoppedZCodeTurn restarts the window, because the agent just spoke.
//
// A no-op unless a stop armed the window, so an ordinary turn pays nothing.
func (a *zcodeAgent) refreshStoppedZCodeTurn() {
	a.mu.Lock()
	armed := a.stoppedTurnTimer != nil
	a.mu.Unlock()
	if armed {
		a.armStoppedZCodeTurn()
	}
}

// cancelStoppedZCodeTurn drops the window, because the turn ended on its own.
func (a *zcodeAgent) cancelStoppedZCodeTurn() {
	a.mu.Lock()
	timer := a.stoppedTurnTimer
	a.stoppedTurnTimer = nil
	a.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

// Interrupt aborts the running turn.
//
// A no-op when no turn is active, so a caller need not probe first. session/stop
// is the same request Stop issues; the app-server aborts the turn's controller and
// replies with an empty object.
func (a *zcodeAgent) Interrupt() error {
	a.mu.Lock()
	stopped, turnActive, sessionID := a.stopped, a.turnActive, a.sessionID
	a.mu.Unlock()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}
	if !turnActive || sessionID == "" {
		return nil
	}
	if _, err := a.sendZCodeRequest(ZCodeMethodSessionStop, map[string]any{"sessionId": sessionID}, zcodeStopTimeout); err != nil {
		// The stop stopped nothing, so the turn is still running. Reporting it
		// finished would hide a live agent behind an idle chat.
		return err
	}
	a.armStoppedZCodeTurn()
	return nil
}

// endStoppedZCodeTurn records what session/stop actually cut.
//
// The app-server's handler aborts the running model stream and answers an empty
// object. It announces the end of the turn ONLY sometimes: a turn whose model stream
// the abort cut has been observed to send no turn.completed and no turn.failed at all,
// which left the turn active for the rest of the session -- the thinking indicator ran
// without end, the durable input queue could never drain, and the model output the
// turn had already produced was never stored. Every other provider reports the end of
// the turn it cancelled, so every other provider's Interrupt can wait for that report.
//
// What this does NOT do is close the turn's tool calls. The abort reaches the model
// stream, not a command the runtime already launched: an interrupted `sleep 90` ran
// its full ninety seconds and then reported success. Marking that call interrupted
// would state an outcome the runtime goes on to contradict, so an open call keeps its
// running card until its own update arrives, or until the process ends.
//
// No PROVIDER row is written either. A turn-end divider is built from the frame that
// reports the end, the app-server sent none, and LeapMux invents no provider output. A
// turn.completed that arrives later still writes its own divider through
// finishZCodeTurn. What the reader gets instead is LeapMux's own row, from
// persistZCodeStopRow.
//
// See ZC-001 in docs/provider-parity/protocol-evidence.md.
func (a *zcodeAgent) endStoppedZCodeTurn() {
	a.mu.Lock()
	a.turnActive = false
	a.backgroundTurn = false
	if a.stoppedTurnTimer != nil {
		a.stoppedTurnTimer.Stop()
		a.stoppedTurnTimer = nil
	}
	a.mu.Unlock()
	defer a.PublishTurnActive()

	// The text the model had already produced is a real segment that ended early.
	// The buffer is empty when the abort found the stream idle, so this costs
	// nothing in the case where a tool was running.
	a.flushZCodeGeneration(MessageCompletionInterrupted)
	a.persistZCodeStopRow()
	a.resetCumulativeOutput()
	a.sink.ReportProgress(ResetProgress())
}

// persistZCodeStopRow states the stop that the app-server never reported.
//
// Without it the transcript held NOTHING about the stop: a census pressed Stop on
// `sleep 45` for ten providers, and ZCode alone drew neither a result row nor a turn
// divider. The reader saw a running command card stop being updated.
//
// The row is LeapMux's own, and it states only what LeapMux did: it asked for the
// stop, the app-server accepted it, and the turn then went silent for the whole
// window. It carries the shared `interrupted` notification type, which is the row
// Claude Code's transcript already draws for a stopped turn, so the two read alike.
//
// The spans stay OPEN, unlike the Claude path, which resets them here. The abort
// reaches the model stream and not a command the runtime already launched, so a call
// that is still running needs its span for the update it still sends -- the same
// reason endStoppedZCodeTurn closes no tool call.
func (a *zcodeAgent) persistZCodeStopRow() {
	content, err := json.Marshal(map[string]string{"type": contracts.NotificationTypeInterrupted})
	if err != nil {
		slog.Error("zcode marshal stop row", "agent_id", a.agentID, "error", err)
		return
	}
	if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, content); err != nil {
		slog.Error("zcode persist stop row", "agent_id", a.agentID, "error", err)
	}
}

// Stop aborts a running turn, then tears the process down.
//
// The stop is issued SYNCHRONOUSLY before processBase.Stop sets stopped and closes
// stdin: on a goroutine it would race that flag and be dropped in the common case.
func (a *zcodeAgent) Stop() {
	a.noteIntentionalStop()
	a.cancelStoppedZCodeTurn()
	a.mu.Lock()
	stopped, turnActive, sessionID := a.stopped, a.turnActive, a.sessionID
	a.mu.Unlock()
	if !stopped && turnActive && sessionID != "" {
		// Best-effort: a failure falls through to the hard tear-down below.
		_, _ = a.sendZCodeRequest(ZCodeMethodSessionStop, map[string]any{"sessionId": sessionID}, zcodeStopTimeout)
	}
	a.processBase.Stop()
	a.flushZCodeGeneration(MessageCompletionInterrupted)
	a.persistIncompleteZCodeTools(MessageCompletionInterrupted)
	a.sink.ReportProgress(ResetProgress())
}

// Wait retains unfinished model output after an unexpected process exit.
func (a *zcodeAgent) Wait() error {
	err := a.processBase.Wait()
	a.cancelStoppedZCodeTurn()
	completion := a.processExitCompletion()
	a.flushZCodeGeneration(completion)
	a.persistIncompleteZCodeTools(completion)
	a.sink.ReportProgress(ResetProgress())
	return err
}

// ClearContext starts a fresh session on the same workspace.
//
// Every piece of per-session state is dropped with it. The per-tool-call side
// tables matter most: tool.updated is their only other removal, and it never
// arrives for a call the replaced session was still running, so without this a
// spawn prompt is retained for the life of the process and a reused tool-call id
// would open the next child transcript on the previous session's instruction.
func (a *zcodeAgent) ClearContext() (string, error) {
	timeout := a.APITimeout()
	a.mu.Lock()
	// The three axes the user currently runs on are the request for the fresh session.
	// Reading them AFTER openSession would read the new session's defaults instead, and
	// a context clear would silently drop the level and the model back to them.
	current := zcodeSettingsRequest{Model: a.model, ThoughtLevel: a.thoughtLevel, Mode: a.mode}
	a.mu.Unlock()

	if err := a.openSession("", timeout); err != nil {
		return "", err
	}
	a.applyStartupSettings(current, timeout)
	subscribeErr := a.subscribe(timeout)
	a.cancelStoppedZCodeTurn()
	a.flushZCodeGeneration(MessageCompletionInterrupted)
	a.persistIncompleteZCodeTools(MessageCompletionInterrupted)

	a.mu.Lock()
	a.turnActive = false
	a.backgroundTurn = false
	a.turnToolUses = 0
	a.latestContextUsage = nil
	clear(a.toolCalls)
	a.nextToolOrder = 0
	clear(a.pendingControls)
	sessionID := a.sessionID
	a.mu.Unlock()
	a.PublishTurnActive()
	a.toolCallPrompts.clear()
	a.children.clear()
	a.resetCumulativeOutput()
	a.generationBuffer.Reset()
	a.sink.ReportProgress(ResetProgress())
	a.sink.ResetSpans()
	// A goal belongs to a SESSION, and this call replaced the session. The
	// create snapshot omits the `goal` key rather than spelling it null, and
	// reportZCodeGoal returns early on an absent key, so nothing else removes
	// the previous session's goal.
	a.sink.ClearGoal(false)

	if sessionID == "" {
		return "", fmt.Errorf("the new ZCode session has no ID")
	}
	a.sink.UpdateSessionID(sessionID)
	return sessionID, subscribeErr
}

// currentZCodeContextWindow returns the context window to label usage with,
// preferring what the app-server reported and falling back to the catalog.
func (a *zcodeAgent) currentZCodeContextWindow() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.contextWindow > 0 {
		return a.contextWindow
	}
	for _, m := range a.catalog.Models {
		if m.GetId() == a.model && m.GetContextWindow() > 0 {
			return m.GetContextWindow()
		}
	}
	return 0
}

// zcodeToolCallName returns the tool name cached for a call id, or "".
func (a *zcodeAgent) zcodeToolCallName(id string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if tc := a.toolCalls[id]; tc != nil {
		return tc.name
	}
	return ""
}

// zcodeToolCall is everything a.mu knows about ONE tool call.
//
// The three facts have different lifetimes -- the name and the input are spent when the
// call OPENS, while `final` outlives its close -- but they share one key, so they share
// one record. Three parallel maps meant three deletions at every teardown, and
// forgetting one of them was twice a real defect.
type zcodeToolCall struct {
	// name and input cache what model.streaming said before tool.updated opens the call.
	// The scheduled update reports `inputOmitted: true, inputRef: "model_stream"` and
	// carries no input of its own, so the stream is the ONLY place the input is sent.
	name  string
	input json.RawMessage
	// progress is the last progress payload, PARSED. The subagent hooks read the
	// routing fields it carries; the transcript row reads lastFrame instead.
	progress zcodeToolUpdated
	// lastFrame is the last tool.updated event the agent sent for this call, byte
	// for byte. A turn that ends while the call runs stores THAT frame, so the
	// transcript never holds an event the agent did not send. An empty value means
	// the agent never announced the call, which also means no row opened its span.
	lastFrame []byte
	order     uint64
	// final marks a call that already reached a final state, so the batch summary that
	// follows it does not reopen or re-close it. It is cleared at the TURN end rather
	// than at the call's own close, because the batch arrives after the results it
	// summarizes.
	final bool
}

// zcodeToolCallLocked returns the record for id, creating it on first write. The caller
// holds a.mu.
func (a *zcodeAgent) zcodeToolCallLocked(id string) *zcodeToolCall {
	tc := a.toolCalls[id]
	if tc == nil {
		tc = &zcodeToolCall{order: a.nextToolOrder}
		a.nextToolOrder++
		a.toolCalls[id] = tc
	}
	return tc
}

func init() {
	registerAgentFactory(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE,
		StartZCode,
		zcodeFallbackModels,
		zcodeStaticOptionGroups(),
		"LEAPMUX_ZCODE_DEFAULT_MODEL",
		"LEAPMUX_ZCODE_DEFAULT_EFFORT",
		zcodeBinaryCandidates...,
	)
	// ZCode ships no executable of its own, so the "probe a bare name in the login
	// shell" model cannot find it -- see zcode_resolve.go.
	registerLaunchResolver(leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE, resolveZCodeLaunch)
	// The model-dependent axis is ZCode's thought level, which is not the generic
	// "Effort" label, so the static fallback and the model-switch sub_groups match
	// what the live OptionGroups reports.
	setModelSubGroups(leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE, effortSubGroupsFunc(ZCodeThoughtLevelLabel))
	setAdditionalOptionIDs(leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE, OptionIDEffort)
	// A composite model id is spelled with `/` by the app-server itself; the
	// normalizer accepts a backslash spelling so a re-spelling is not read as a
	// model switch.
	setModelIDNormalizer(leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE, normalizeZCodeModelID)
	// ZCode ships no safe-mode preset; Build is the mode a session with none takes.
	setPermissionDefaults(leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE, PermissionDefaults{
		Fallback: contracts.ZCodeDefaultMode,
	})
}
