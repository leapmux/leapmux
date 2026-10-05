package zcode

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/internal/util/id"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
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
	controlMu sync.Mutex

	sink       agent.ProviderServices
	workingDir string
	workspace  zcodeWorkspace

	// catalog is the translated view of ZCode's own configuration: the provider
	// registry payload (with inline API keys) and the per-model capabilities. Read
	// once at startup and never mutated, so it needs no lock.
	catalog zcodeCatalog
	// registryRevision identifies this agent's provider configuration. Legacy
	// builds use it on the workspace registry, and current builds use it on the
	// account snapshot.
	registryRevision string
	// builtinProviderConfigPath identifies the installed release that the account
	// provider snapshot overlays. Empty for a PATH launcher that owns its setup.
	builtinProviderConfigPath string

	// --- guarded by Mu ---

	// accountProviderConfig is true when the legacy registry method is absent and
	// provider/updateAccountConfig completed. It selects the current request shapes.
	accountProviderConfig bool
	sessionID             string
	// stateRevision is the app-server's optimistic-concurrency counter, taken
	// from the last runtime state observed. session/goal sends it as
	// expectedRevision so a goal write that races a change the agent just made
	// is refused rather than silently overwriting it.
	stateRevision int64
	model         string // the composite catalog id, providerId/modelId
	thoughtLevel  string
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
	// lastSeq is the highest event sequence number observed. A re-subscribe asks
	// for events after it, so a subscription that lapsed replays what it missed
	// instead of dropping it.
	lastSeq    int64
	turnActive bool
	// A native compaction runs outside the prompt turn. Keep its session until a
	// state.updated result ends it, so the input queue stays busy meanwhile.
	compactionSessionID string
	// backgroundTurn is true while the running turn was started by a background
	// task rather than by the user. Such a turn must not end the user's turn.
	backgroundTurn bool
	// stopWindow ends a turn whose stop the app-server accepted and then never
	// reported. Guarded by a.Mu. See stoppedTurnWindow and armStoppedZCodeTurnLocked.
	stopWindow stoppedTurnWindow
	// afterFunc is time.AfterFunc. A test replaces it to fire the window itself.
	afterFunc func(time.Duration, func()) *time.Timer

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

	// toolCalls holds everything a.Mu knows about each tool call, keyed by its id.
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

	generationBuffer providerkit.GenerationBuffer
	// toolCallPrompts holds an Agent spawn's full prompt until the child transcript
	// exists to receive it as its first message.
	toolCallPrompts providerkit.PendingPrompts
	// children maps a subagent spawn's tool-call id to the child transcript that
	// holds that subagent's work. See subagent.go.
	children zcodeChildIndex
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
	a.Mu.Unlock()
	a.PublishTurnActive()

	raw, err := a.sendZCodeRequest(MethodSessionCompact, map[string]any{
		"sessionId": sessionID,
		"inputId":   id.Short(),
	}, a.APITimeout())
	if err != nil {
		a.finishZCodeCompaction(sessionID)
		var responseErr *zcodeError
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
		a.finishZCodeCompaction(sessionID)
		return fmt.Errorf("%w: ZCode returned no valid compaction receipt", agent.ErrDeliveryUncertain)
	}
	return nil
}

// finishZCodeCompaction drops only the pending request for this session.
// A completion from a replaced session cannot release the new session's turn.
func (a *Agent) finishZCodeCompaction(sessionID string) {
	a.Mu.Lock()
	if a.compactionSessionID != sessionID {
		a.Mu.Unlock()
		return
	}
	a.compactionSessionID = ""
	if !a.turnActive {
		a.cancelStoppedZCodeTurnLocked()
	}
	a.Mu.Unlock()
	a.PublishTurnActive()
}

// ZCode's installed app-server refuses session/send while a prompt runs.
// The Worker therefore waits until that turn ends before it sends queued input.
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

	// The app-server's session/send schema rejects delivery hints such as
	// requestedDelivery with -32602.
	params := map[string]any{
		"sessionId": sessionID,
		"content":   text,
		"inputId":   id.Short(),
	}
	if len(wire) > 0 {
		params["attachments"] = wire
	}

	deadline := time.Now().Add(zcodeSendRetryWindow)
	for {
		raw, err := a.sendZCodeRequest(MethodSessionSend, params, a.APITimeout())
		if err == nil {
			var ack struct {
				Accepted bool `json:"accepted"`
			}
			if json.Unmarshal(raw, &ack) == nil && !ack.Accepted {
				return fmt.Errorf("the app-server did not accept the message")
			}
			return nil
		}
		if !zcodeIsPromptRunning(err) || time.Now().After(deadline) {
			return classifyZCodeInputDeliveryError(err)
		}
		select {
		case <-time.After(zcodeSendRetryInterval):
		case <-a.ProcessDone():
			return a.ProcessExitError()
		case <-a.Context().Done():
			return a.Context().Err()
		}
	}
}

func classifyZCodeInputDeliveryError(err error) error {
	var responseErr *zcodeError
	if errors.As(err, &responseErr) {
		return err
	}
	return fmt.Errorf("%w: ZCode did not confirm session/send delivery: %v", agent.ErrDeliveryUncertain, err)
}

func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments)
}
