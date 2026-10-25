package zcode

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Workspace hook trust.
//
// ZCode runs a project's hooks (`hooks` in `.zcode/config.json`) only after the
// user trusts each one. When the first turn of a session starts, ZCode states
// the hooks that wait for trust in a `hookAdmission` update, and the turn runs
// without them. ZCode's desktop application asks through a hook review, which
// ZCode shows only to a client that subscribes to the session's conversation
// (`v4/conversation/subscribe`), so the stdio app-server asks LeapMux nothing.
// LeapMux asks through providerkit.WorkspaceTrust instead. Trust holds the
// conversation subscription open (conversation.go), asks ZCode for the review
// (`requestWorkspaceHookReview`), trusts every hook that waits for trust
// (`respondWorkspaceHookReview`), and lets the subscription close unless a
// workflow run still needs it. ZCode keeps the grant for each hook, and the
// running session runs the hooks from its next prompt. A Reject sends nothing,
// so the hooks stay blocked.
//
// The question waits for the end of the turn that stated the admission. The
// hooks run only from the next prompt anyway, and an interrupt of the turn
// withdraws every control request that the turn published, so a question
// published during the turn could vanish unanswered.

// zcodeTrustedConfiguration is what ZCode's hook trust controls, as the
// question states it.
var zcodeTrustedConfiguration = []string{"hooks"}

// zcodeHookReviewKind is the kind of a pending hook review.
const zcodeHookReviewKind = "workspaceHookReview"

// zcodeHookTrustAction trusts the review items that the answer lists.
const zcodeHookTrustAction = "trust_selected"

// zcodeHookTrustPending lists the trust states of a hook that waits for trust.
// They are the states that ZCode counts in an admission's pendingCount.
var zcodeHookTrustPending = map[string]bool{"pending_trust": true, "revoked": true, "stale_digest": true}

// zcodeHookAdmission is the latest hook admission of a session.
type zcodeHookAdmission struct {
	sessionID         string
	workspaceIdentity string
	bundleDigest      string
	pendingCount      int
}

// zcodeWorkspaceTrustHost is ZCode's hook trust.
type zcodeWorkspaceTrustHost struct{ a *Agent }

var _ providerkit.WorkspaceTrustHost = zcodeWorkspaceTrustHost{}

// WorkspaceTrusted reports whether no hook of the current session waits for
// trust.
func (h zcodeWorkspaceTrustHost) WorkspaceTrusted() (bool, error) {
	h.a.Mu.Lock()
	defer h.a.Mu.Unlock()
	admission, ok := h.a.currentHookAdmissionLocked()
	return !ok || admission.pendingCount == 0, nil
}

// currentHookAdmissionLocked returns the hook admission of the current session.
// The event dispatcher admits an event while LeapMux knows no session yet, so
// an admission also counts while no session is known. The caller holds a.Mu.
func (a *Agent) currentHookAdmissionLocked() (zcodeHookAdmission, bool) {
	admission := a.hookAdmission
	if admission.sessionID == "" || (a.sessionID != "" && admission.sessionID != a.sessionID) {
		return zcodeHookAdmission{}, false
	}
	return admission, true
}

// TrustWorkspace trusts every hook of the current session that waits for trust.
func (h zcodeWorkspaceTrustHost) TrustWorkspace() error {
	return h.a.trustZCodeHooks()
}

// observeZCodeHookAdmission records the hook admission of the event's session,
// and asks the reader to trust the hooks when some wait for trust: at once when
// no turn runs, and otherwise at the end of the turn (askZCodeHookTrustWhenIdle).
func (a *Agent) observeZCodeHookAdmission(sessionID string, payload zcodeSessionUpdatedPayload) {
	var pending int
	if json.Unmarshal(payload.PendingCount, &pending) != nil || pending < 0 {
		slog.Warn("zcode hook admission states no pending count", "agent_id", a.AgentID())
		return
	}
	digest, _ := providerkit.RawString(payload.BundleDigest)
	identity, _ := providerkit.RawString(payload.WorkspaceIdentity)
	a.Mu.Lock()
	owner := sessionID
	if owner == "" {
		owner = a.sessionID
	}
	if owner == "" || (a.sessionID != "" && owner != a.sessionID) {
		// No session states it, or a session that LeapMux left behind.
		a.Mu.Unlock()
		return
	}
	a.hookAdmission = zcodeHookAdmission{sessionID: owner, workspaceIdentity: identity, bundleDigest: digest, pendingCount: pending}
	a.hookTrustDeferred = pending > 0
	a.Mu.Unlock()
	a.askZCodeHookTrustWhenIdle()
}

// askZCodeHookTrustWhenIdle asks the deferred hook-trust question once no turn
// runs. The host's check reads the recorded admission and no ZCode route, so
// the question is published on the caller's goroutine, as ZCode's own control
// requests are published on the read loop.
func (a *Agent) askZCodeHookTrustWhenIdle() {
	a.Mu.Lock()
	admission, current := a.currentHookAdmissionLocked()
	busy := a.turnActive || a.compactionSessionID != ""
	if !a.hookTrustDeferred || busy || a.StoppedLocked() {
		a.Mu.Unlock()
		return
	}
	a.hookTrustDeferred = false
	a.Mu.Unlock()
	if current {
		a.workspaceTrust.Ask(a.sink, zcodeWorkspaceTrustHost{a}, admission.sessionID, a.workingDir, zcodeTrustedConfiguration)
	}
}

// zcodeHookReviewRequest is the payload of requestWorkspaceHookReview.
type zcodeHookReviewRequest struct {
	SessionID         string `json:"sessionId"`
	WorkspaceIdentity string `json:"workspaceIdentity"`
	BundleDigest      string `json:"bundleDigest"`
}

// zcodeHookReview is the part of a pending hook review that its answer repeats,
// and its items.
type zcodeHookReview struct {
	Kind              string `json:"kind"`
	SessionID         string `json:"sessionId"`
	TaskID            string `json:"taskId"`
	RunID             string `json:"runId"`
	RemoteSessionID   string `json:"remoteSessionId,omitempty"`
	WorkspaceIdentity string `json:"workspaceIdentity"`
	BundleDigest      string `json:"bundleDigest"`
	ReviewFlowID      string `json:"reviewFlowId"`
	Generation        int64  `json:"generation"`
	InteractionID     string `json:"interactionId"`
	Items             []struct {
		ReviewItemID string `json:"reviewItemId"`
		TrustState   string `json:"trustState"`
	} `json:"items"`
}

// pendingItemIDs lists the review items that wait for trust.
func (r zcodeHookReview) pendingItemIDs() []string {
	var ids []string
	for _, item := range r.Items {
		if item.ReviewItemID != "" && zcodeHookTrustPending[item.TrustState] {
			ids = append(ids, item.ReviewItemID)
		}
	}
	return ids
}

// zcodeHookReviewAnswer is the payload of respondWorkspaceHookReview. ZCode
// refuses a field that it does not know, so it carries the review's own
// identity fields and the decision, and nothing more.
type zcodeHookReviewAnswer struct {
	SessionID         string                  `json:"sessionId"`
	TaskID            string                  `json:"taskId"`
	RunID             string                  `json:"runId"`
	RemoteSessionID   string                  `json:"remoteSessionId,omitempty"`
	WorkspaceIdentity string                  `json:"workspaceIdentity"`
	BundleDigest      string                  `json:"bundleDigest"`
	ReviewFlowID      string                  `json:"reviewFlowId"`
	Generation        int64                   `json:"generation"`
	InteractionID     string                  `json:"interactionId"`
	Decision          zcodeHookReviewDecision `json:"decision"`
}

// zcodeHookReviewDecision trusts the listed review items.
type zcodeHookReviewDecision struct {
	Action        string   `json:"action"`
	ReviewItemIDs []string `json:"reviewItemIds"`
}

// zcodeHookReviewWait waits for the review of one trust. The frames of the
// conversation subscription feed it on the read loop.
type zcodeHookReviewWait struct {
	bundleDigest string
	found        chan zcodeHookReview
}

// zcodeHookReviewTimerTag tags the wait for a hook review.
const zcodeHookReviewTimerTag = "zcode-hook-review"

// trustZCodeHooks trusts every hook of the current session that waits for
// trust. It runs on the goroutine of the reader's answer, never on the read
// loop, which delivers the frames it waits for.
func (a *Agent) trustZCodeHooks() error {
	a.Mu.Lock()
	admission, ok := a.currentHookAdmissionLocked()
	if !ok || admission.pendingCount == 0 {
		a.Mu.Unlock()
		return nil
	}
	sessionID := admission.sessionID
	if a.hookReview != nil {
		a.Mu.Unlock()
		return errors.New("another trust of the ZCode hooks is in progress")
	}
	wait := &zcodeHookReviewWait{bundleDigest: admission.bundleDigest, found: make(chan zcodeHookReview, 1)}
	a.hookReview = wait
	a.Mu.Unlock()
	defer func() {
		a.Mu.Lock()
		if a.hookReview == wait {
			a.hookReview = nil
		}
		a.Mu.Unlock()
		if err := a.reconcileZCodeConversation(); err != nil {
			slog.Warn("zcode conversation subscription", "agent_id", a.AgentID(), "error", err)
		}
	}()

	if err := a.reconcileZCodeConversation(); err != nil {
		return err
	}
	ack, err := a.sendZCodeCommand(zcodeCommandEnvelope{
		CommandID: "hook-review-" + id.Short(),
		SessionID: sessionID,
		Type:      zcodeCommandRequestWorkspaceHookReview,
		Payload:   zcodeHookReviewRequest{SessionID: sessionID, WorkspaceIdentity: admission.workspaceIdentity, BundleDigest: admission.bundleDigest},
	})
	if err != nil {
		return fmt.Errorf("ask ZCode for the hook review: %w", err)
	}
	if ack.Status != zcodeCommandAccepted {
		return fmt.Errorf("ZCode did not start the hook review: %s", zcodeCommandRefusal(ack))
	}
	review, err := a.awaitZCodeHookReview(wait)
	if err != nil {
		return err
	}
	if ids := review.pendingItemIDs(); len(ids) > 0 {
		ack, err = a.sendZCodeCommand(zcodeCommandEnvelope{
			CommandID: "hook-trust-" + id.Short(),
			SessionID: sessionID,
			Type:      zcodeCommandRespondWorkspaceHookReview,
			Payload: zcodeHookReviewAnswer{
				SessionID: review.SessionID, TaskID: review.TaskID, RunID: review.RunID, RemoteSessionID: review.RemoteSessionID,
				WorkspaceIdentity: review.WorkspaceIdentity, BundleDigest: review.BundleDigest,
				ReviewFlowID: review.ReviewFlowID, Generation: review.Generation, InteractionID: review.InteractionID,
				Decision: zcodeHookReviewDecision{Action: zcodeHookTrustAction, ReviewItemIDs: ids},
			},
		})
		if err != nil {
			return fmt.Errorf("trust the ZCode hooks: %w", err)
		}
		if ack.Status != zcodeCommandAccepted {
			return fmt.Errorf("ZCode did not trust the hooks: %s", zcodeCommandRefusal(ack))
		}
	}
	a.Mu.Lock()
	if a.hookAdmission == admission {
		a.hookAdmission.pendingCount = 0
	}
	a.Mu.Unlock()
	return nil
}

// awaitZCodeHookReview waits for the review that the subscription delivers. A
// review that was already pending reaches no new frame, because ZCode raises no
// second review for it, so the newest pending interactions are read first.
func (a *Agent) awaitZCodeHookReview(wait *zcodeHookReviewWait) (zcodeHookReview, error) {
	a.Mu.Lock()
	review, pending := zcodeHookReviewInList(a.conversation.pendingInteractions, wait.bundleDigest)
	a.Mu.Unlock()
	if pending {
		return review, nil
	}
	timer := a.Clock().NewTimer(a.APITimeout(), zcodeHookReviewTimerTag)
	defer timer.Stop()
	select {
	case review := <-wait.found:
		return review, nil
	case <-timer.C:
		return zcodeHookReview{}, errors.New("ZCode raised no hook review in time")
	case <-a.ProcessDone():
		return zcodeHookReview{}, a.ProcessExitError()
	case <-a.Context().Done():
		return zcodeHookReview{}, a.Context().Err()
	}
}

// zcodeHookReviewInList finds the hook review of the bundle in one list of
// pending interactions.
func zcodeHookReviewInList(list json.RawMessage, bundleDigest string) (zcodeHookReview, bool) {
	var interactions []struct {
		Kind    string          `json:"kind"`
		Payload zcodeHookReview `json:"payload"`
	}
	if len(list) == 0 || json.Unmarshal(list, &interactions) != nil {
		return zcodeHookReview{}, false
	}
	for _, interaction := range interactions {
		review := interaction.Payload
		if interaction.Kind == zcodeHookReviewKind && review.Kind == zcodeHookReviewKind && review.BundleDigest == bundleDigest {
			return review, true
		}
	}
	return zcodeHookReview{}, false
}
