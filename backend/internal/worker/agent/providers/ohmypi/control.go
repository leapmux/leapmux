package ohmypi

import (
	"encoding/json"
	"log/slog"
	"math"
	"strings"
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
		if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, raw); err != nil {
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
		if _, err := a.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, raw); err != nil {
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
// that deadline passes. The deadline is armed BEFORE the publish, so an answer
// that the reader sends at once finds it to disarm.
func (a *Agent) publishDialog(head dialogHeader, raw []byte) {
	a.dialogDeadlines.Arm(a.Clock(), head.ID, head.deadline(), func() { a.withdrawDialog(head.ID) })
	err := a.sink.PublishControlRequest(agent.ControlRequest{
		RequestID: head.ID,
		Payload:   raw,
		SourceSeq: a.approvalSourceSeq(head),
	})
	if err == nil {
		return
	}
	a.dialogDeadlines.Disarm(head.ID)
	slog.Error("omp publish dialog", "agent_id", a.AgentID(), "request_id", head.ID, "error", err)
	a.cancelDialog(head.ID)
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
	if err := a.Process.SendRawInput(response); err != nil {
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
	return a.Process.SendRawInput(response)
}

// withdrawDialog cancels the control request of a dialog omp no longer waits for.
// omp sends `cancel` when the run was aborted, and the dialog's deadline answers
// a dialog that omp timed out itself (see publishDialog).
func (a *Agent) withdrawDialog(targetID string) {
	if targetID == "" {
		return
	}
	a.dialogDeadlines.Disarm(targetID)
	a.forgetAskDialog(targetID)
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
	if head.Method != contracts.OhMyPiDialogMethodSelect || !strings.HasPrefix(head.Title, contracts.OhMyPiApprovalDialogTitlePrefix) {
		return 0
	}
	name, _, _ := strings.Cut(strings.TrimPrefix(head.Title, contracts.OhMyPiApprovalDialogTitlePrefix), "\n")
	name = strings.TrimSpace(name)
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
func (a *Agent) SendRawInput(data []byte) error {
	var head struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if json.Unmarshal(data, &head) == nil {
		switch head.Type {
		case contracts.OhMyPiAskTypeAnswer:
			return a.answerAsk(data)
		case contracts.OhMyPiEventExtensionUIResponse:
			// The reader answered, so the dialog's deadline withdraws nothing.
			a.dialogDeadlines.Disarm(head.ID)
			a.forgetAskDialog(head.ID)
		}
	}
	return a.Process.SendRawInput(data)
}
