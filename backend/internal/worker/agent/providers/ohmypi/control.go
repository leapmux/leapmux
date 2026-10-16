package ohmypi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// dialogHeader takes the routing fields of an extension_ui_request frame.
//
// Every dialog blocks omp until the host answers it with an extension_ui_response
// that echoes `id`. The whole frame is what the control request publishes, so the
// browser can read every method-specific field.
type dialogHeader struct {
	ID       string   `json:"id"`
	Method   string   `json:"method"`
	Title    string   `json:"title"`
	TargetID string   `json:"targetId"`
	Options  []string `json:"options"`
	// Timeout is the wait in milliseconds after which omp answers the dialog
	// itself, with its default. Zero states no limit.
	Timeout float64 `json:"timeout"`
}

type pendingDialog struct {
	header          dialogHeader
	raw             []byte
	ready           bool
	published       bool
	answering       bool
	automaticFailed bool
	publicationDone chan struct{}
	publicationOnce sync.Once
}

func (h dialogHeader) approvalTool() string {
	if h.Method != contracts.OhMyPiDialogMethodSelect || !strings.HasPrefix(h.Title, contracts.OhMyPiApprovalDialogTitlePrefix) {
		return ""
	}
	name, _, _ := strings.Cut(strings.TrimPrefix(h.Title, contracts.OhMyPiApprovalDialogTitlePrefix), "\n")
	return strings.TrimSpace(name)
}

func (h dialogHeader) isToolApproval() bool {
	return h.approvalTool() != "" && slices.Contains(h.Options, contracts.OhMyPiApprovalDialogApprove) &&
		slices.Contains(h.Options, contracts.OhMyPiApprovalDialogDeny)
}

// maxDialogTimeoutMillis is the largest timeout that a time.Duration holds. A
// larger one states no limit in practice, and a float conversion past it has no
// defined result.
const maxDialogTimeoutMillis = float64(math.MaxInt64 / int64(time.Millisecond))

// deadline returns the wait after which omp answers the dialog itself, or zero
// when omp waits with no limit.
func (h dialogHeader) deadline() time.Duration {
	if h.Timeout <= 0 || h.Timeout >= maxDialogTimeoutMillis {
		return 0
	}
	return time.Duration(h.Timeout * float64(time.Millisecond))
}

// handleExtensionUIRequest routes one extension_ui_request frame.
//
// The four dialog methods block omp until the host answers. A dialog of the `ask`
// tool goes to the question bridge (ask.go), and every other dialog -- a tool
// approval, or a dialog an extension raised -- is published as it is. `cancel`
// withdraws a dialog omp no longer waits for. The fire-and-forget methods drive
// omp's own terminal display.
func (a *Agent) handleExtensionUIRequest(raw []byte) {
	var head dialogHeader
	if err := json.Unmarshal(raw, &head); err != nil {
		slog.Warn("omp extension_ui_request decode failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	switch head.Method {
	case contracts.OhMyPiDialogMethodSelect, contracts.OhMyPiDialogMethodConfirm,
		contracts.OhMyPiDialogMethodInput, contracts.OhMyPiDialogMethodEditor:
		if head.ID == "" {
			slog.Warn("omp dialog without an id", "agent_id", a.AgentID(), "method", head.Method)
			return
		}
		if a.routeAskDialog(head) {
			return
		}
		a.publishDialog(head, raw)
	case contracts.OhMyPiExtensionMethodCancel:
		a.withdrawDialog(head.TargetID)
	case contracts.OhMyPiExtensionMethodNotify, contracts.OhMyPiExtensionMethodOpenURL:
		// A notice the reader must see, and a URL the reader must open. omp sends
		// the second only for a `login` command, which LeapMux never sends, so it
		// persists rather than disappearing if a later build sends it for more.
		if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}); err != nil {
			slog.Error("omp persist extension notification", "agent_id", a.AgentID(), "method", head.Method, "error", err)
		}
	case contracts.OhMyPiExtensionMethodSetStatus, contracts.OhMyPiExtensionMethodSetWidget,
		contracts.OhMyPiExtensionMethodSetTitle, contracts.OhMyPiExtensionMethodSetEditorText:
		// omp's own terminal display: a status line, a widget, the terminal title,
		// the editor text. LeapMux has no surface for them. The bundled
		// autoresearch extension sends an empty widget around every run.
	default:
		// A method this build does not know. It reaches the transcript as an
		// inspectable card rather than disappearing.
		if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}); err != nil {
			slog.Error("omp persist unknown extension request", "agent_id", a.AgentID(), "method", head.Method, "error", err)
		}
	}
}

// publishDialog publishes one dialog as a control request.
//
// A dialog that cannot be published would block omp for good, because nothing
// else answers it, so a failed publish answers it with a cancellation.
//
// omp answers a dialog whose deadline passes itself, with its default, and sends
// no `cancel` for it (`requestRpcDialog`), so the worker withdraws the card when
// that deadline passes. The deadline is armed, and the dialog recorded as open,
// BEFORE the publish, so an answer that the reader sends at once finds both to
// clear.
func (a *Agent) publishDialog(head dialogHeader, raw []byte) {
	a.Mu.Lock()
	if a.openDialogs[head.ID] != nil || a.StoppedLocked() {
		a.Mu.Unlock()
		return
	}
	if a.openDialogs == nil {
		a.openDialogs = make(map[string]*pendingDialog)
	}
	bypass := a.approvalMode == contracts.OhMyPiApprovalModeYolo && head.isToolApproval()
	pending := &pendingDialog{header: head, raw: append([]byte(nil), raw...), ready: bypass, publicationDone: make(chan struct{})}
	a.openDialogs[head.ID] = pending
	a.Mu.Unlock()
	if bypass {
		a.approvalWork.Run("permissions", a.approvePendingDialogs)
		return
	}
	a.publishRegisteredDialog(pending)
}

func (a *Agent) publishRegisteredDialog(pending *pendingDialog) {
	head := pending.header
	a.Mu.Lock()
	if a.openDialogs[head.ID] != pending || a.StoppedLocked() {
		if a.openDialogs[head.ID] == pending {
			delete(a.openDialogs, head.ID)
		}
		pending.publicationOnce.Do(func() { close(pending.publicationDone) })
		a.Mu.Unlock()
		return
	}
	a.Mu.Unlock()
	a.dialogDeadlines.Arm(a.Clock(), head.ID, head.deadline(), func() { a.withdrawDialog(head.ID) })
	err := a.sink.PublishControlRequest(agent.ControlRequest{
		RequestID: head.ID,
		Payload:   pending.raw,
		SourceSeq: a.approvalSourceSeq(head),
	})
	a.Mu.Lock()
	pending.ready = true
	pending.published = err == nil
	current := a.openDialogs[head.ID] == pending && !a.StoppedLocked()
	if (err != nil || !current) && a.openDialogs[head.ID] == pending {
		delete(a.openDialogs, head.ID)
	}
	bypass := a.approvalMode == contracts.OhMyPiApprovalModeYolo && !pending.automaticFailed
	pending.publicationOnce.Do(func() { close(pending.publicationDone) })
	a.Mu.Unlock()
	if err == nil {
		if !current {
			a.sink.CancelControlRequest(head.ID)
		} else if bypass {
			a.approvalWork.Run("permissions", a.approvePendingDialogs)
		}
		return
	}
	a.dialogDeadlines.Disarm(head.ID)
	slog.Error("omp publish dialog", "agent_id", a.AgentID(), "request_id", head.ID, "error", err)
	if current {
		a.cancelDialog(head.ID)
	}
}

// forgetOpenDialog drops a dialog that omp no longer waits on: the reader answered
// it, omp withdrew it, or the worker cancelled it.
func (a *Agent) forgetOpenDialog(id string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if pending := a.openDialogs[id]; pending != nil {
		pending.publicationOnce.Do(func() { close(pending.publicationDone) })
	}
	delete(a.openDialogs, id)
}

// approvePendingDialogs claims native tool approvals under the current policy.
// Extension dialogs and the question bridge keep their own answers.
func (a *Agent) approvePendingDialogs() {
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return
	}
	claimed := make(map[string]*pendingDialog)
	var publish []*pendingDialog
	for id, pending := range a.openDialogs {
		if !pending.ready || pending.answering {
			continue
		}
		if a.approvalMode == contracts.OhMyPiApprovalModeYolo && pending.header.isToolApproval() && !pending.automaticFailed {
			pending.answering = true
			claimed[id] = pending
		} else if !pending.published {
			pending.ready = false
			publish = append(publish, pending)
		}
	}
	a.Mu.Unlock()
	for _, pending := range publish {
		a.publishRegisteredDialog(pending)
	}
	for id, pending := range claimed {
		err := a.answerDialog(id, contracts.OhMyPiApprovalDialogApprove)
		a.finishDialogAnswer(pending, err, true)
		if err != nil {
			slog.Warn("omp could not approve a pending tool", "request_id", id, "error", err)
		}
	}
}

func (a *Agent) finishDialogAnswer(pending *pendingDialog, err error, automatic bool) {
	id := pending.header.ID
	a.Mu.Lock()
	current := a.openDialogs[id] == pending
	retire := err == nil || errors.Is(err, agent.ErrDeliveryUncertain) || a.StoppedLocked()
	published := pending.published
	if current {
		if retire {
			delete(a.openDialogs, id)
			pending.publicationOnce.Do(func() { close(pending.publicationDone) })
		} else {
			pending.answering = false
			pending.automaticFailed = pending.automaticFailed || automatic
			if !published {
				pending.ready = false
			}
		}
	}
	a.Mu.Unlock()
	if !current {
		return
	}
	if retire {
		a.dialogDeadlines.Disarm(id)
		a.forgetAskDialog(id)
		if automatic && published {
			a.sink.CancelControlRequest(id)
		}
	} else if !published {
		a.publishRegisteredDialog(pending)
	}
}

// settleOpenDialogs answers each dialog that omp still waits on with a
// cancellation, and withdraws its card.
//
// Interrupt calls it, for a user stop and for a preemption alike. omp's abort
// settles each dialog of the run that it stops, and withdraws each one with a
// `cancel` frame. omp runs the handler of an extension command outside any run and
// with no abort signal (omp's `AgentSession.#tryExecuteExtensionCommand`), so the
// abort leaves a dialog of that handler waiting. A user stop withdraws every card
// of the agent, and a preemption waits for the turn to end. Without this answer,
// omp waits on a dialog that nobody can answer, the command never ends, and the
// turn that the command armed keeps the agent busy.
func (a *Agent) settleOpenDialogs() {
	a.Mu.Lock()
	if a.StoppedLocked() {
		// A stopped process answers nothing, and its own path withdraws the cards.
		a.Mu.Unlock()
		return
	}
	ids := make([]string, 0, len(a.openDialogs))
	for id, pending := range a.openDialogs {
		if !pending.answering {
			ids = append(ids, id)
		}
		pending.publicationOnce.Do(func() { close(pending.publicationDone) })
	}
	clear(a.openDialogs)
	a.Mu.Unlock()
	// Answer in a stable order, so one interrupt always writes the same frames.
	slices.Sort(ids)
	for _, id := range ids {
		a.cancelDialog(id)
		a.withdrawDialog(id)
	}
}

// cancelDialog answers one dialog with a cancellation.
func (a *Agent) cancelDialog(id string) {
	response, err := json.Marshal(map[string]any{
		contracts.OhMyPiDialogResponseType:      contracts.OhMyPiEventExtensionUIResponse,
		contracts.OhMyPiDialogResponseID:        id,
		contracts.OhMyPiDialogResponseCancelled: true,
	})
	if err != nil {
		slog.Error("omp encode dialog cancellation", "agent_id", a.AgentID(), "error", err)
		return
	}
	if err := a.Process.SendRawInput(response, agent.StopContext{}); err != nil {
		slog.Warn("omp send dialog cancellation", "agent_id", a.AgentID(), "request_id", id, "error", err)
	}
}

// answerDialog answers one dialog with a value, as the question bridge does for
// each dialog after the first.
func (a *Agent) answerDialog(id, value string) error {
	response, err := json.Marshal(map[string]any{
		contracts.OhMyPiDialogResponseType:  contracts.OhMyPiEventExtensionUIResponse,
		contracts.OhMyPiDialogResponseID:    id,
		contracts.OhMyPiDialogResponseValue: value,
	})
	if err != nil {
		return err
	}
	return a.Process.SendRawInput(response, agent.StopContext{})
}

// withdrawDialog cancels the control request of a dialog omp no longer waits for.
// omp sends `cancel` when the run was aborted, the dialog's deadline answers a
// dialog that omp timed out itself (see publishDialog), and an interrupt answers a
// dialog that the abort left open (see settleOpenDialogs).
func (a *Agent) withdrawDialog(targetID string) {
	if targetID == "" {
		return
	}
	a.dialogDeadlines.Disarm(targetID)
	a.forgetAskDialog(targetID)
	a.forgetOpenDialog(targetID)
	a.sink.CancelControlRequest(targetID)
}

// approvalSourceSeq returns the transcript row of the tool call a tool approval
// dialog asks about, or 0.
//
// omp's approval dialog states the tool NAME in its title ("Allow tool: bash") and
// no call id. omp sends it after the call's tool_execution_start, so the call is
// one of the calls in progress: when exactly one call of that name runs, it is the
// one, and the control request carries its row so the browser can show the call's
// arguments. With two or more, no row is stated rather than a wrong one.
func (a *Agent) approvalSourceSeq(head dialogHeader) int64 {
	name := head.approvalTool()
	if name == "" {
		return 0
	}
	a.Mu.Lock()
	root := a.rootConversationLocked()
	callID := ""
	for id, tool := range root.tools {
		if tool == nil || tool.ToolName != name {
			continue
		}
		if callID != "" {
			a.Mu.Unlock()
			return 0
		}
		callID = id
	}
	a.Mu.Unlock()
	if callID == "" {
		return 0
	}
	stored, err := a.sink.ReadToolRequest(callID)
	if err != nil {
		slog.Warn("omp read the approval's tool call", "agent_id", a.AgentID(), "tool_call_id", callID, "error", err)
		return 0
	}
	if stored == nil {
		return 0
	}
	return stored.Seq
}

// SendRawInput writes one answer to omp.
//
// An answer to the question bridge's request is not omp's own frame: the bridge
// turns it into the dialog chain omp waits on (see answerAsk). Every other frame
// reaches omp unchanged, and a cancellation of the bridge's request also ends the
// bridge's state for that call.
func (a *Agent) SendRawInput(data []byte, stop agent.StopContext) error {
	var head struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		Cancelled bool   `json:"cancelled"`
	}
	if json.Unmarshal(data, &head) == nil {
		switch head.Type {
		case contracts.OhMyPiAskTypeAnswer:
			return a.answerAsk(data)
		case contracts.OhMyPiEventExtensionUIResponse:
			if head.Cancelled {
				if handled, err := a.cancelAsk(head.ID, data, stop); handled {
					return err
				}
			}
			a.Mu.Lock()
			pending := a.openDialogs[head.ID]
			unpublished := pending != nil && pending.ready && !pending.published
			a.Mu.Unlock()
			if pending == nil || unpublished {
				return errors.New("the omp dialog no longer waits for a manual answer")
			}
			select {
			case <-pending.publicationDone:
			case <-a.Context().Done():
				return a.Context().Err()
			}
			a.Mu.Lock()
			if a.openDialogs[head.ID] != pending || pending.answering || !pending.published {
				a.Mu.Unlock()
				return errors.New("the omp dialog no longer waits for a manual answer")
			}
			pending.answering = true
			a.Mu.Unlock()
			err := a.Process.SendRawInput(data, stop)
			a.finishDialogAnswer(pending, err, false)
			return err
		}
	}
	return a.Process.SendRawInput(data, stop)
}
