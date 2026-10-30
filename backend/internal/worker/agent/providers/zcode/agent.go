package zcode

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/internal/util/id"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/tooltranscript"
)

// Agent manages a single `zcode app-server --stdio` process.
//
// The wire format is line-delimited JSON that resembles JSON-RPC 2.0 without
// being it, so -- like pi.Agent -- this type does NOT embed JSONRPCProcess. It shares
// only the pending-map mechanics, through Correlator[int64]. See
// protocol.go for the framing rules and rpc.go for the transport.
type Agent struct {
	providerkit.Process
	providerkit.Correlator[int64]

	// nextReqID mints the monotonic request ids the correlator keys on.
	nextReqID atomic.Int64
	// dispatchMu serializes dispatchZCodeEvent across the read loop and a replaying
	// re-subscribe. See that function for why the whole body needs it.
	dispatchMu sync.Mutex
	// Control requests can arrive concurrently while the server repeats an unanswered request.
	// runtimeHeaderMu guards runtimeHeaderAnswers. It is a lock of its own: a
	// blocked reply write must never block the cancellation of another answer.
	runtimeHeaderMu sync.Mutex
	// runtimeHeaderAnswers holds the in-flight answer of each
	// interaction/requestProviderRuntimeHeaders, keyed by its LOGICAL
	// params.requestId -- never by the JSON-RPC envelope id. See
	// zcodeRuntimeHeaderAnswer.
	runtimeHeaderAnswers map[string]*zcodeRuntimeHeaderAnswer
	// steerMu serializes each steer command against the withdrawals, so a
	// withdrawal sees every steer sent before it, and no steer is sent between
	// an interrupt's withdrawal and its stop. See steering.go.
	steerMu sync.Mutex
	// Serialize session adoption with catalog reads of the active native session.
	sessionLifecycleMu sync.Mutex
	// catalogRefresh coalesces the registry-plus-catalog refresh passes that
	// account changes and workspace modelCatalog patches request. Guarded by its
	// own mutex. See catalog_refresh.go.
	catalogRefresh zcodeCatalogRefreshOwner
	usageRefresh   providerkit.CoalescingWork

	// dispatchMu protects deferred observation effects and their writer.
	sessionObservationWriter    agent.CapturedSessionObservationWriter
	pendingSessionObservation   *zcodeSessionObservationEffects
	sessionPublicationDeferrals int
	sink                        agent.ProviderServices
	workingDir                  string
	workspace                   zcodeWorkspace
	// homeDir is the finalized home directory used at startup. The refresh owner
	// reloads account files from it.
	homeDir string

	// catalog is the translated view of ZCode's own configuration: the provider
	// registry payload (with inline API keys), the per-model capabilities, and the
	// account bridge. Provider and model data stay immutable after startup.
	// Accepted account snapshots replace the bridge under Mu. Every catalog
	// method that copies or reads the bridge holds Mu too.
	catalog zcodeCatalog
	// Only a legacy app-server requires the legacy registry to be usable.
	legacyConfigurationError error
	// registryRevision identifies this agent's provider configuration. Legacy
	// builds use it on the workspace registry, and current builds use it on the
	// account snapshot.
	registryRevision string
	// builtinProviderConfigPath identifies the installed release that the account
	// provider snapshot overlays. Launch resolution sets it whenever it finds an
	// installed release, also for a `zcode` launcher on PATH beside an installed
	// application, and the environment can set it too. Empty otherwise, and the
	// account snapshot then fails.
	builtinProviderConfigPath string
	// credentials reads the credential store that the desktop and its app-server
	// share. Runtime requests and the account refresh owner read current credentials.
	credentials zcodeCredentialStore
	// identity is how the Start Plan entitlement check identifies its client, as
	// the desktop does.
	identity zcodeClientIdentity
	// startPlanHTTP sends the Start Plan entitlement check. A test replaces it, so
	// no test reaches ZCode's billing API.
	startPlanHTTP *http.Client
	// projectTokens mints the project tokens of coding plans. Production agents
	// share zcodeProjectTokens; a test replaces it, so no test reaches the project
	// API.
	projectTokens *zcodeProjectTokenClient

	// --- guarded by Mu ---

	// account is the account snapshot that the app-server accepted, or nil before
	// it accepts one and on a legacy build.
	account             *zcodeAccountState
	sessionID           string
	sessionEpoch        uint64
	sessionChanging     bool
	observationSequence uint64
	// stateRevision is the app-server's optimistic-concurrency counter, taken
	// from the last runtime state observed. session/goal sends it as
	// expectedRevision so a goal write that races a change the agent just made
	// is refused rather than silently overwriting it.
	stateRevision        int64
	model                string // the composite catalog id, providerId/modelId
	thoughtLevel         string
	modelObserved        bool
	thoughtLevelObserved bool
	// mode is the session's native mode, `settings.mode.current`: build, edit, yolo or
	// auto. Plan mode is not one of them. ZCode keeps it in planEnabled, beside this
	// value, and LeapMux's permission-mode axis combines the two -- see
	// permissionModeLocked.
	mode string
	// modeObserved is true once the RUNNING session reported its own mode through
	// `settings.mode.current`. Until then `mode` holds what the launch asked for, not
	// what the app-server settled on, and the two must not be compared. It resets with
	// the session, because the next one reports its own.
	modeObserved bool
	// planEnabled is the session's plan flag. ZCode keeps plan mode in this flag and
	// leaves the native mode unchanged under it, so a session in plan mode reports
	// `build` in `settings.mode.current`. No state document and no state.updated patch
	// carries the flag. Only ZCode's SessionModeChanged event states it, and the rule
	// by which ZCode folds a mode request decides it for an accepted request.
	planEnabled bool
	// planObserved is true once LeapMux knows planEnabled for the RUNNING session: an
	// accepted session/create or session/setMode stated it, or a SessionModeChanged
	// event reported it. A resume restores the flag from ZCode's own store and reports
	// nothing, so the flag stays unknown until one of those arrives. It resets with
	// the session.
	planObserved bool
	// modeChanges counts the SessionModeChanged events that this agent folded. The
	// mode setter compares the count across its RPC, so the rule for its request does
	// not overwrite an event that reported a later state.
	modeChanges uint64
	// pendingExitMode is the permission mode an approved plan exit chose, waiting
	// for the event that reports the exit turned the plan flag off. session/setMode
	// turns the flag off itself, so sending the mode before the exit ran makes
	// ExitPlanMode fail; see DeferPlanExitMode.
	pendingExitMode string
	// unresolvedSettings records successful setter calls whose response omitted
	// the authoritative axis snapshot.
	unresolvedSettings map[string]struct{}
	// lastSeq is the highest contiguously dispatched sequence. A re-subscribe asks
	// for events after it, so a subscription that lapsed replays what it missed
	// instead of dropping it.
	lastSeq int64
	// turnActive mirrors "any owner in turns with settled == false", kept
	// consistent by refreshTurnActiveLocked. It is what Interrupt, Stop and the
	// input queue read to decide the agent is processing.
	turnActive bool
	// turnGeneration mints the unique generation each turn record stores at its
	// acceptance (and each compaction bumps without a record). Stop and the
	// stopped-turn recovery compare the generation stored on their OWNING turn,
	// never this latest value, so a background start cannot invalidate them.
	turnGeneration uint64
	// steeringUnsupported is true once the app-server refused the command
	// inbox as an unknown method. SupportsSteering reports its inverse.
	steeringUnsupported bool
	// steers maps the command id of each steer that ZCode accepted and has not
	// resolved to its bookkeeping. See steering.go.
	steers map[string]*zcodeSteer
	// withdrawnSteerInputs holds the queued inputs that a withdrawal deleted,
	// until ZCode reports their discard, so that event is known as LeapMux's own.
	withdrawnSteerInputs map[string]struct{}
	// commandRevision is the newest session revision that a command
	// acknowledgement stated. A compare-and-set command starts from it.
	commandRevision int64
	// steerLedger reports accepted steers to the queue in send order: the
	// queue holds each steer's row until ZCode states what happened to it.
	steerLedger providerkit.SteerLedger
	// A native compaction runs outside the prompt turn. Keep its session until a
	// state.updated result ends it, so the input queue stays busy meanwhile.
	compactionSessionID    string
	compactionGeneration   uint64
	codingPlanEntitlements map[string]zcodeAccountEntitlement
	compactionLive         bool
	// turns holds the owner record of every turn the session runs, keyed by the
	// turn id the event envelope carries. A turn the wire identifies by no id is
	// keyed by a synthetic one (see startZCodeTurnLocked). Each record owns its
	// turn's generation content, its tool calls and its cleanup; several records
	// coexist while a background turn runs beside the user's. Guarded by a.Mu.
	turns map[string]*zcodeTurnState
	// inputAdmissions retains native ownership until the matching send resolves.
	// Native events can precede both its receipt and the sender's next instruction.
	inputAdmissions map[string]*zcodeInputAdmission
	// foregroundTurnID is the key in turns of the user's own turn, empty while
	// the user runs none. A background start never replaces it. Guarded by a.Mu.
	foregroundTurnID string
	retryResolution  providerkit.RetryResolution
	// stopWindow ends a turn whose stop the app-server accepted and then never
	// reported. Guarded by a.Mu. See stoppedTurnWindow and armStoppedZCodeTurnLocked.
	stopWindow stoppedTurnWindow
	// workspaceTrust holds the hook-trust question that LeapMux asks for ZCode.
	// See workspace_trust.go.
	workspaceTrust providerkit.WorkspaceTrust
	// hookAdmission is the latest hook admission of the current session.
	// Guarded by a.Mu.
	hookAdmission zcodeHookAdmission
	// hookTrustDeferred is true while the hook-trust question waits for the end
	// of the turn that stated the admission. Guarded by a.Mu.
	hookTrustDeferred bool
	// hookReview is the wait of the trust that runs now, which the frames of
	// the conversation subscription feed. Guarded by a.Mu.
	hookReview *zcodeHookReviewWait
	// conversationMu keeps one reconcile of the conversation subscription at a
	// time. See conversation.go.
	conversationMu sync.Mutex
	// conversation is the conversation subscription. Guarded by a.Mu.
	conversation zcodeConversation
	// workflows tracks the session's workflow runs. Guarded by a.Mu. See
	// workflow.go.
	workflows zcodeWorkflowRuns
	// stopsInFlight counts the session/stop requests that Interrupt and
	// SendRawInput sent and that ZCode has not answered yet. Guarded by a.Mu.
	stopsInFlight int
	// stopCancelledRequest records a native permission cancellation during a stop.
	stopCancelledRequest bool
	// afterFunc is time.AfterFunc. A test replaces it to fire the window itself.
	afterFunc func(time.Duration, func()) *time.Timer

	// replay is the gap-safe event dispatch bookkeeping. Guarded by dispatchMu
	// except its fetch mutex. See event_replay.go.
	replay zcodeReplayState
	// replayScheduler starts a replay pass. Nil selects the default goroutine.
	// A test replaces it to suppress automatic scheduling and call
	// replayZCodeEvents directly.
	replayScheduler func(func())

	// observedThoughtLevels is the thought-level list the app-server reports for
	// the CURRENT model (settings.thoughtLevel.available). It is authoritative and
	// model-dependent, so it overrides the catalog's variants for the running model.
	observedThoughtLevels  []*agent.EffortInfo
	observedThoughtDefault string
	// liveModels is the one normalized record per model id. liveModelOrder keeps
	// the app-server's order without copying each record's metadata into parallel
	// maps that can drift.
	liveModels     map[string]zcodeLiveModelRecord
	liveModelOrder []string
	// Partial setter/read snapshots cannot expand a complete account catalog.
	liveCatalogComplete bool

	// toolCalls holds everything a.Mu knows about each tool call, keyed by its id.
	toolInvocations map[string]*zcodeToolInvocation
	nextToolOrder   uint64

	// pendingControls keeps the first payload and wire identity until native settlement.
	// Reannouncements reuse the publication instance. An accepted answer keeps its claim,
	// so a late reannouncement cannot send that answer twice.
	pendingControls   map[string]*pendingControl
	controlPublishers map[string]*zcodeControlPublisher
	approvalWork      providerkit.CoalescingWork

	// latestContextUsage is the broadcast-shaped context-usage map, kept so a
	// reconnecting frontend can be rehydrated from the persisted turn end.
	latestContextUsage map[string]any
	contextWindow      int64
	// lastGoalUpdate is the last goal this agent reported. The goal check's
	// lifecycle re-reports that goal with its own StatusDetail, so the verifier's
	// "verifying" rides the same objective and status the card already shows.
	lastGoalUpdate agent.GoalUpdate
	// sessionCostUsd is the accrued cost the app-server reports on
	// runtime.contextUsage. sessionCostKnown distinguishes "no cost reported" from a
	// genuine zero, which a free plan does report.
	sessionCostUsd   float64
	sessionCostKnown bool

	generationBuffer providerkit.GenerationBuffer
	toolTranscript   *tooltranscript.Transcript
	// toolCallPrompts holds an Agent spawn's full prompt until the child transcript
	// exists to receive it as its first message.
	toolCallPrompts providerkit.PendingPrompts
	// children maps a subagent spawn's tool-call id to the child transcript that
	// holds that subagent's work. See subagent.go.
	children zcodeChildIndex
}

// zcodeTurnState is the owner record of one turn: what it is, and what its
// cleanup must touch.
//
// Every started turn gets one, keyed by the id the event envelope carries --
// several coexist while a background turn runs beside the user's. The record
// outlives its settlement while any of its tool calls remain open, so a late
// result still routes to the turn that opened the call; it is dropped only
// after settlement AND its last tool result. Guarded by a.Mu.
type zcodeTurnState struct {
	// id is the key in Agent.turns: the envelope turn id, or a synthetic key
	// for a turn the wire identified by none.
	id string
	// generation is the unique Agent.turnGeneration value minted at acceptance.
	// Stop and the stopped-turn recovery compare it, so a start of ANOTHER turn
	// never invalidates what they captured about this one.
	generation uint64
	// background is true for a turn the runtime started for itself (a non-empty
	// turn.started inputSource): a background task reporting back, a subagent's
	// reply, a todo reminder. Such a turn must not end the user's turn or reset
	// its state.
	background bool
	// foregroundExecutionID is the turn.started foregroundExecutionId, recorded
	// for background turns too: a runtime turn states the user execution it
	// belongs to.
	foregroundExecutionID string
	// settled marks a turn whose end LeapMux already recorded -- its final
	// event, or the stopped-turn window's local end. turnActive derives from it.
	settled bool
	// finalized distinguishes a native or recovered end from local stop settlement.
	// A local stop still permits the native end to supply its own divider later.
	finalized         bool
	settlementWriting bool
	// recovery belongs to this stable owner, including while its native ID changes.
	recovery *zcodeTurnRecoveryState
	// canonicalRequired freezes batchable appends until raw messages restore this turn.
	canonicalRequired bool
	// pendingNative retains a received native end while its canonical output is read.
	pendingNative *zcodePendingNativeSettlement
	// toolUses counts completed calls of this owner, including late results.
	toolUses int
	// acceptedAt is when LeapMux observed the turn.started.
	acceptedAt time.Time
	// admissionRevision is the session state revision the turn was admitted
	// under.
	admissionRevision int64
	// anchorMessageID is the turn.started messageId, and inputID its inputId.
	anchorMessageID string
	inputID         string
	lastEventSeq    int64
	// contentByScope retains the text and reasoning delivered for this turn,
	// keyed by generation-buffer scope, so it survives buffer persistence for a
	// later reconciliation.
	contentByScope            map[string]*zcodeScopeContent
	currentAssistantMessageID string
	acceptedResponses         []zcodeAcceptedModelResponse
	// toolCallIDs holds the tool-call ids belonging to this turn. Openness is
	// read from the tool-call records themselves (see dropZCodeTurnLocked).
	toolInvocationIDs     map[string]struct{}
	invocationsByNativeID map[string]map[string]*zcodeToolInvocation
}

// zcodeScopeContent separates observed bytes from immutable queued output.
// submitted includes retained writes whose storage retry has not finished.
type zcodeScopeContent struct {
	observed  []byte
	submitted int
}

func (content *zcodeScopeContent) text() string {
	if content == nil {
		return ""
	}
	return string(content.observed)
}

type zcodeAcceptedModelResponse struct {
	messageID string
	text      string
}

type zcodeInputAdmission struct {
	epoch     uint64
	sessionID string
	owner     *zcodeTurnState
}

var _ agent.Agent = (*Agent)(nil)
var _ agent.ContextCompactor = (*Agent)(nil)

// zcodeSendRetryWindow limits how long SendInput retries a native busy refusal.
// A recently ended turn can hold the prompt lock briefly. SendInput still
// returns before the browser's own deadline if the lock does not clear.
const (
	zcodeSendRetryWindow   = 3 * time.Second
	zcodeSendRetryInterval = 250 * time.Millisecond
)

// SendInput delivers a user message.
//
// It returns on the app-server's `{accepted:true}` acknowledgement and NEVER waits
// for the turn -- the Agent.SendInput contract. A refusal because a turn is already
// running is retried briefly, because it is transient by construction.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(nil, content, attachments)
}

// CompactContext starts ZCode's native asynchronous context summary.
// The state.updated completion closes the queue turn after this RPC accepts it.
func (a *Agent) CompactContext() error {
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	sessionID := a.sessionID
	if sessionID == "" {
		a.Mu.Unlock()
		return fmt.Errorf("agent has no ZCode session")
	}
	if a.turnActive || a.compactionSessionID != "" {
		a.Mu.Unlock()
		return agent.ErrAgentBusy
	}
	a.compactionSessionID = sessionID
	a.compactionGeneration++
	generation := a.compactionGeneration
	a.Mu.Unlock()
	a.PublishTurnActive()

	raw, err := a.sendZCodeRequest(MethodSessionCompact, map[string]any{
		"sessionId": sessionID,
		"inputId":   id.Short(),
	}, a.APITimeout())
	if err != nil {
		a.finishZCodeCompactionGeneration(sessionID, generation)
		var responseErr *zcodeError
		if errors.Is(err, agent.ErrInputSessionChanged) {
			return err
		}
		if errors.As(err, &responseErr) {
			return err
		}
		return fmt.Errorf("%w: ZCode did not confirm session/compact delivery: %v", agent.ErrDeliveryUncertain, err)
	}
	var ack struct {
		Compact struct {
			State string `json:"state"`
		} `json:"compact"`
	}
	if json.Unmarshal(raw, &ack) != nil || (ack.Compact.State != "accepted" && ack.Compact.State != "already_running") {
		a.finishZCodeCompactionGeneration(sessionID, generation)
		return fmt.Errorf("%w: ZCode returned no valid compaction receipt", agent.ErrDeliveryUncertain)
	}
	return nil
}

// finishZCodeCompaction ends the live compaction and releases its pending manual request.
// A completion from a replaced session cannot change the current session.
func (a *Agent) finishZCodeCompaction(sessionID string) {
	a.finishZCodeCompactionGeneration(sessionID, 0)
}

func (a *Agent) finishZCodeCompactionGeneration(sessionID string, generation uint64) {
	a.Mu.Lock()
	if generation != 0 && generation != a.compactionGeneration {
		a.Mu.Unlock()
		return
	}
	if sessionID != "" && sessionID != a.sessionID {
		a.Mu.Unlock()
		return
	}
	pending := a.compactionSessionID != "" && (sessionID == "" || a.compactionSessionID == sessionID)
	if pending {
		a.compactionSessionID = ""
		if !a.turnActive {
			a.cancelStoppedZCodeTurnLocked()
		}
	}
	a.Mu.Unlock()
	a.endZCodeCompaction()
	if pending {
		a.PublishTurnActive()
	}
}

// ZCode's installed app-server refuses session/send while a prompt runs.
// The Worker therefore waits until that turn ends before it sends queued input.
//
// The reply must be the full {sessionId, accepted, stateRevision} receipt for
// the session the request specified. accepted:false is an explicit rejection; any
// reply that does not prove acceptance is delivery-uncertain, because a
// malformed result must never confirm delivery.
//
// A valid receipt creates a provisional foreground turn owner: the accepted
// input is the user's turn even before turn.started identifies it. The owner carries
// the sent inputId, the acceptance time and the receipt's state revision.
// watchZCodeTurn starts immediately so a turn that never reports a start is
// still reconciled. A receipt whose session was replaced while the send was in
// flight folds no revision and creates no owner on the replacement.
func (a *Agent) sendInputForSession(expected *string, content string, attachments []*leapmuxv1.Attachment) error {
	a.Mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.sessionID); err != nil {
		a.Mu.Unlock()
		return err
	}
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	if a.zcodeCurrentModelUnavailableLocked() {
		a.Mu.Unlock()
		return errZCodeModelSelectionRequired
	}
	identity := a.captureZCodeObservationIdentityLocked()
	if !a.zcodeSessionLifetimeCurrentLocked(identity) {
		a.Mu.Unlock()
		return agent.ErrInputSessionChanged
	}
	sessionID, model, turnActive := a.sessionID, a.model, a.turnActive || a.compactionSessionID != ""
	a.Mu.Unlock()
	if sessionID == "" {
		return fmt.Errorf("agent has no ZCode session")
	}
	if turnActive {
		return agent.ErrAgentBusy
	}

	text, wire, err := a.buildZCodeInput(content, attachments, model)
	if err != nil {
		return err
	}

	inputID := id.Short()
	a.Mu.Lock()
	if !a.zcodeSessionLifetimeCurrentLocked(identity) {
		a.Mu.Unlock()
		return agent.ErrInputSessionChanged
	}
	if a.zcodeCurrentModelUnavailableLocked() {
		a.Mu.Unlock()
		return errZCodeModelSelectionRequired
	}
	if a.inputAdmissions == nil {
		a.inputAdmissions = make(map[string]*zcodeInputAdmission)
	}
	admission := &zcodeInputAdmission{sessionID: sessionID, epoch: identity.epoch}
	a.inputAdmissions[inputID] = admission
	a.Mu.Unlock()
	defer func() {
		a.Mu.Lock()
		delete(a.inputAdmissions, inputID)
		a.Mu.Unlock()
	}()
	// The app-server's session/send schema rejects delivery hints such as
	// requestedDelivery with -32602.
	params := map[string]any{
		"sessionId": sessionID,
		"content":   text,
		"inputId":   inputID,
	}
	if len(wire) > 0 {
		params["attachments"] = wire
	}

	deadline := a.Clock().Now().Add(zcodeSendRetryWindow)
	for {
		a.Mu.Lock()
		unavailable := a.zcodeCurrentModelUnavailableLocked()
		a.Mu.Unlock()
		if unavailable {
			return errZCodeModelSelectionRequired
		}
		raw, err := a.sendZCodeSessionRequest(a.Context(), identity, MethodSessionSend, params, a.APITimeout(), func(raw json.RawMessage) {
			if receipt, err := decodeZCodeSendReceipt(raw, sessionID); err == nil {
				a.createProvisionalZCodeTurnForSession(identity, inputID, *receipt.StateRevision)
			}
		})
		if err == nil {
			_, err := decodeZCodeSendReceipt(raw, sessionID)
			return err
		}
		if !zcodeIsPromptRunning(err) || !a.Clock().Now().Before(deadline) {
			return classifyZCodeDeliveryError(MethodSessionSend, err)
		}
		timer := a.Clock().NewTimer(zcodeSendRetryInterval, zcodeSendRetryTimerTag)
		select {
		case <-timer.C:
			timer.Stop()
		case <-a.ProcessDone():
			timer.Stop()
			return a.ProcessExitError()
		case <-a.Context().Done():
			timer.Stop()
			return a.Context().Err()
		}
	}
}

// zcodeSendReceipt is a session/send reply. The app-server promises
// {sessionId, accepted, stateRevision}, and each field is required: the
// pointers tell an absent field from a present zero, because a zero revision
// is a valid answer on a fresh session.
type zcodeSendReceipt struct {
	SessionID     *string `json:"sessionId"`
	Accepted      *bool   `json:"accepted"`
	StateRevision *int64  `json:"stateRevision"`
}

func decodeZCodeSendReceipt(raw json.RawMessage, sessionID string) (zcodeSendReceipt, error) {
	var receipt zcodeSendReceipt
	if json.Unmarshal(raw, &receipt) != nil {
		return receipt, fmt.Errorf("%w: ZCode returned no valid %s receipt", agent.ErrDeliveryUncertain, MethodSessionSend)
	}
	// A refusal is certain even when the response omits the acceptance-only fields.
	if receipt.Accepted != nil && !*receipt.Accepted {
		return receipt, fmt.Errorf("the app-server did not accept the message")
	}
	// Zero is a valid revision. Missing fields cannot prove delivery.
	if receipt.Accepted == nil || receipt.SessionID == nil || *receipt.SessionID != sessionID ||
		receipt.StateRevision == nil || *receipt.StateRevision < 0 {
		return receipt, fmt.Errorf("%w: ZCode returned no valid %s receipt", agent.ErrDeliveryUncertain, MethodSessionSend)
	}
	return receipt, nil
}

// createProvisionalZCodeTurn records the accepted input as a foreground turn
// owner before turn.started identifies it. The native ID is empty; a later
// turn.started or a session/read snapshot adopts the real one. The watcher
// starts immediately so recovery can reconcile a turn that never reports a
// start.
//
// expectedSessionID must still be the current session. A send that crossed a
// session replacement belongs to the replaced session: its receipt then folds
// no revision and creates no owner on the replacement, or a stale counter would
// move the fresh session's revision and a phantom turn would run against it.
func (a *Agent) createProvisionalZCodeTurn(expectedSessionID, inputID string, receiptRevision int64) {
	a.Mu.Lock()
	identity := a.captureZCodeObservationIdentityLocked()
	a.Mu.Unlock()
	identity.sessionID = expectedSessionID
	a.createProvisionalZCodeTurnForSession(identity, inputID, receiptRevision)
}

func (a *Agent) createProvisionalZCodeTurnForSession(identity zcodeSessionObservationIdentity, inputID string, receiptRevision int64) {
	expectedSessionID := identity.sessionID
	a.dispatchMu.Lock()
	defer a.unlockZCodeDispatch()
	a.Mu.Lock()
	if !a.zcodeSessionLifetimeCurrentLocked(identity) {
		a.Mu.Unlock()
		return
	}
	a.noteZCodeStateRevisionLocked(receiptRevision)
	if admission := a.inputAdmissions[inputID]; admission != nil && admission.sessionID == expectedSessionID && admission.owner != nil {
		// The native start, or even its end, already established this input's owner.
		admission.owner.admissionRevision = receiptRevision
		a.Mu.Unlock()
		return
	}
	admissionRevision := receiptRevision
	if admissionRevision == 0 {
		admissionRevision = a.stateRevision
	}
	turn := a.createZCodeTurnLocked(zcodeTurnState{
		acceptedAt:        a.Clock().Now(),
		admissionRevision: admissionRevision,
		inputID:           inputID,
	})
	if admission := a.inputAdmissions[inputID]; admission != nil && admission.sessionID == expectedSessionID {
		admission.owner = turn
	}
	a.Mu.Unlock()
	a.PublishTurnActive()
}

const zcodeSendRetryTimerTag = "zcode-send-retry"

func classifyZCodeDeliveryError(method string, err error) error {
	var responseErr *zcodeError
	if errors.As(err, &responseErr) {
		return err
	}
	return fmt.Errorf("%w: ZCode did not confirm %s delivery: %v", agent.ErrDeliveryUncertain, method, err)
}

func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments)
}
