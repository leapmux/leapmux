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

// zcodeSendRetryWindow limits how long SendInput retries a send the app-server
// refused because a turn is already running. The refusal is transient -- the turn
// ends -- but the wait needs a limit, because SendInput must return long
// before the browser's own deadline.
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
	return a.sendInput(content, attachments, false)
}

// Agent steers. Manager.SupportsSteering answers false, with no build error, for a
// provider that stops satisfying InputSteerer, so this assertion makes that
// regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)

// SupportsSteering always reports true. A plain session/send during an active
// turn needs no handshake discovery: the app-server admits it itself, steering
// the text into the running turn when the turn is steerable and otherwise
// queueing it as a follow-up that drains at the next boundary. Both paths are
// observable on the event stream (turn.steerQueued / turn.steerDrained), which
// the dispatcher persists as notifications.
func (a *Agent) SupportsSteering() bool { return true }

func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(content, attachments, true)
}

func (a *Agent) sendInput(content string, attachments []*leapmuxv1.Attachment, steer bool) error {
	return a.sendInputForSession(nil, content, attachments, steer)
}

func (a *Agent) sendInputForSession(expected *string, content string, attachments []*leapmuxv1.Attachment, steer bool) error {
	a.Mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.sessionID); err != nil {
		a.Mu.Unlock()
		return err
	}
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	sessionID, model, turnActive := a.sessionID, a.model, a.turnActive
	a.Mu.Unlock()
	if sessionID == "" {
		return fmt.Errorf("agent has no ZCode session")
	}
	if !steer && turnActive {
		return agent.ErrAgentBusy
	}
	if steer && !turnActive {
		return agent.ErrNoActiveTurn
	}

	text, wire, err := a.buildZCodeInput(content, attachments, model)
	if err != nil {
		return err
	}

	// The params carry NO delivery hint. The app-server's session/send schema is
	// strict and rejects any key it does not know (a `requestedDelivery` field
	// fails the whole request with -32602), and the server already decides
	// steer-or-queue itself for a send that lands during a turn.
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
			if steer {
				a.Mu.Lock()
				stillActive := a.turnActive
				a.Mu.Unlock()
				if !stillActive {
					return agent.ErrNoActiveTurn
				}
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
	return a.sendInputForSession(&sessionID, content, attachments, false)
}
