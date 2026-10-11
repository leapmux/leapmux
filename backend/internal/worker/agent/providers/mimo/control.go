package mimo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// MiMo's control requests.
//
// MiMo asks three things of the user, and blocks the asking tool until an
// answer arrives over HTTP:
//
//   - permission.asked: may a tool call run. It offers once, always and reject.
//   - question.asked: the `question` tool's questions. A question keyed
//     plan_exit requests plan approval.
//     mcp_elicitation requests a Model Context Protocol (MCP) server's confirmation.
//   - bash.interactive.asked: a shell command that waits for keyboard input.
//
// Permission and question events become LeapMux control requests.
// The stored payload keeps MiMo's original type and properties.
// The shared request header supplies the tool name and native part ID.
// Plan approval also carries the text from the plan file.
//
// ResolveControlResponse checks the browser answer. SendRawInput sends it to MiMo.
// The answer keeps the browser's shape because the persisted answer row uses that shape.
// parseElicitationAnswer converts the shared MCP action to MiMo's native answer.
//
// An interactive command is refused at once, and nothing about it is stored:
// the event carries the whole shell environment of the command.

// Control request ID prefixes keep each native request separate from other
// requests of this agent. The answer path reads its kind from the stored payload.
const (
	permissionRequestPrefix = "mimo-permission:"
	questionRequestPrefix   = "mimo-question:"
)

// mimoControlKind is what one control request asks.
type mimoControlKind int

const (
	controlPermission mimoControlKind = iota + 1
	controlQuestion
	controlPlan
)

// mimoControl is one control request LeapMux published and MiMo still waits on.
type mimoControl struct {
	kind mimoControlKind
	// nativeID is MiMo's own id, which the reply route takes.
	nativeID  string
	sessionID string
	// actorID is the actor whose tool call asked.
	actorID    string
	actorKnown bool
	messageID  string
	payload    []byte
}

// mimoControlHeader is the shared request header of a stored payload. The
// service reads the tool name to classify a plan approval, and the browser's
// shared controls read the tool call.
type mimoControlHeader struct {
	ToolName  string          `json:"tool_name"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
}

// mimoToolRef is the tool call a request came from.
type mimoToolRef struct {
	MessageID string `json:"messageID"`
	CallID    string `json:"callID"`
}

// mimoPermissionAsked is permission.asked.
type mimoPermissionAsked struct {
	ID         string          `json:"id"`
	SessionID  string          `json:"sessionID"`
	Permission string          `json:"permission"`
	Metadata   json.RawMessage `json:"metadata"`
	Tool       *mimoToolRef    `json:"tool"`
}

// mimoQuestionAsked is question.asked.
type mimoQuestionAsked struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	Questions []struct {
		Key    string            `json:"key"`
		Params map[string]string `json:"params"`
	} `json:"questions"`
	Tool *mimoToolRef `json:"tool"`
}

// mimoQuestionKeyPlanExit is the `key` of the one question that the plan
// agent's `plan_exit` tool asks. Only the worker reads it: it records the
// request under the plan tool's name, and the browser dispatches on that name.
const mimoQuestionKeyPlanExit = "plan_exit"

// planPath returns the path that a plan approval specifies.
// It returns false for other questions.
func (q mimoQuestionAsked) planPath() (string, bool) {
	if len(q.Questions) != 1 || q.Questions[0].Key != mimoQuestionKeyPlanExit {
		return "", false
	}
	return q.Questions[0].Params["plan"], true
}

// mimoSettled is question.replied, question.rejected and permission.replied.
type mimoSettled struct {
	SessionID string `json:"sessionID"`
	RequestID string `json:"requestID"`
}

// controlPayload builds a stored payload: MiMo's event, the shared header, and
// the plan text of a plan approval.
func controlPayload(eventType string, properties json.RawMessage, header mimoControlHeader, plan string) ([]byte, error) {
	payload := map[string]any{
		"type":       eventType,
		"properties": properties,
		"request":    header,
	}
	if plan != "" {
		payload[contracts.MiMoControlFieldPlan] = plan
	}
	return json.Marshal(payload)
}

func (a *Agent) handlePermissionAsked(properties json.RawMessage) {
	var asked mimoPermissionAsked
	if err := json.Unmarshal(properties, &asked); err != nil || asked.ID == "" {
		slog.Warn("mimo permission.asked unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}
	if !a.ownsSession(asked.SessionID) {
		// The previous session still waits for an answer.
		// Refusal releases its turn because no user can see that session's control.
		a.replyPermissionLater(asked.ID, mimoPermissionReplyBody{
			Reply: contracts.MiMoPermissionReplyReject, Message: "LeapMux no longer shows this session.",
		})
		return
	}
	actorID, spanID := a.requestActor(asked.SessionID, asked.Tool)
	header := mimoControlHeader{ToolName: asked.Permission, ToolUseID: spanID, Input: asked.Metadata}
	payload, err := controlPayload(contracts.MiMoEventPermissionAsked, properties, header, "")
	if err != nil {
		slog.Error("mimo marshal permission request", "agent_id", a.AgentID(), "error", err)
		a.replyPermissionLater(asked.ID, mimoPermissionReplyBody{Reply: contracts.MiMoPermissionReplyReject, Message: providerkit.ControlPublicationFailure})
		return
	}
	requestID := permissionRequestPrefix + asked.ID
	a.publishControl(requestID, &mimoControl{
		kind: controlPermission, nativeID: asked.ID, sessionID: asked.SessionID, actorID: actorID, messageID: controlMessageID(asked.Tool), payload: payload,
	})
}

func (a *Agent) handleQuestionAsked(properties json.RawMessage) {
	var asked mimoQuestionAsked
	if err := json.Unmarshal(properties, &asked); err != nil || asked.ID == "" {
		slog.Warn("mimo question.asked unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}
	if !a.ownsSession(asked.SessionID) {
		a.rejectQuestionLater(asked.ID)
		return
	}
	actorID, spanID := a.requestActor(asked.SessionID, asked.Tool)
	requestID := questionRequestPrefix + asked.ID
	kind, header, plan := controlQuestion, mimoControlHeader{ToolName: contracts.MiMoToolQuestion, ToolUseID: spanID}, ""
	if path, ok := asked.planPath(); ok {
		kind = controlPlan
		header.ToolName = contracts.MiMoToolPlanExit
		plan = a.readPlan(path)
		a.Mu.Lock()
		_, restated := a.controls[requestID]
		a.Mu.Unlock()
		// The reconnect path restates a pending approval, and its plan is already
		// the agent's plan.
		if plan != "" && !restated {
			compressed, compression := msgcodec.Compress([]byte(plan))
			a.sink.UpdatePlan(compressed, compression, providerkit.ExtractPlanTitle(plan))
		}
	}
	payload, err := controlPayload(contracts.OpenCodeEventQuestionAsked, properties, header, plan)
	if err != nil {
		slog.Error("mimo marshal question", "agent_id", a.AgentID(), "error", err)
		a.rejectQuestionLater(asked.ID)
		return
	}
	a.publishControl(requestID, &mimoControl{
		kind: kind, nativeID: asked.ID, sessionID: asked.SessionID, actorID: actorID, messageID: controlMessageID(asked.Tool), payload: payload,
	})
}

func controlMessageID(tool *mimoToolRef) string {
	if tool == nil {
		return ""
	}
	return tool.MessageID
}

// requestActor reads the actor and exact native part that asked. A
// request that states no call is taken as the main agent's, so the end of the
// main agent's turn retires it.
func (a *Agent) requestActor(sessionID string, tool *mimoToolRef) (actorID, spanID string) {
	if tool == nil {
		return mainActorID, ""
	}
	actorID = mainActorID
	if tool.MessageID != "" {
		actorID = a.messageRecord(sessionID, tool.MessageID).actorID
	}
	if tool.MessageID == "" || tool.CallID == "" {
		return actorID, ""
	}
	a.Mu.Lock()
	if sessionID == "" {
		sessionID = a.sessionID
	}
	matches := 0
	for partID, call := range a.tools {
		if call.sessionID == sessionID && call.messageID == tool.MessageID && call.callID == tool.CallID {
			spanID = partID
			matches++
		}
	}
	a.Mu.Unlock()
	if matches == 1 {
		return actorID, spanID
	}
	if matches > 1 {
		return actorID, ""
	}
	message, err := a.rpc.messageWithParts(a.Context(), sessionID, tool.MessageID)
	if err != nil {
		slog.Warn("mimo read the control tool identity", "agent_id", a.AgentID(), "message_id", tool.MessageID, "error", err)
		return actorID, ""
	}
	if message.Info.ID != tool.MessageID || message.Info.SessionID != sessionID {
		return actorID, ""
	}
	actorID = recordFromInfo(message.Info).actorID
	for _, part := range message.Parts {
		if part.Type == contracts.MiMoPartTypeTool && part.ID != "" && part.SessionID == sessionID && part.MessageID == tool.MessageID && part.CallID == tool.CallID {
			spanID = part.ID
			matches++
		}
	}
	if matches != 1 {
		spanID = ""
	}
	return actorID, spanID
}

// publishControl records a control request and publishes it.
//
// The reconnect path republishes each native pending request.
// An identical payload keeps its claim token and the user's existing card.
// A failed publication refuses the native request because MiMo waits for an
// answer that no user can supply.
func (a *Agent) publishControl(requestID string, control *mimoControl) {
	a.Mu.Lock()
	if control.sessionID == "" {
		control.sessionID = a.sessionID
	}
	control.actorKnown = control.messageID == "" || control.actorID != mainActorID && control.actorID != ""
	if record := a.messages[control.messageID]; record != nil && record.sessionID == control.sessionID {
		control.actorKnown = control.actorKnown || record.identityKnown && record.actorKnown
	}
	if existing := a.controls[requestID]; existing != nil && bytes.Equal(existing.payload, control.payload) {
		control = existing
	} else {
		a.controls[requestID] = control
	}
	a.Mu.Unlock()
	err := a.sink.PublishControlRequest(agent.ControlRequest{RequestID: requestID, Payload: control.payload})
	if err == nil {
		return
	}
	slog.Error("mimo publish control request", "agent_id", a.AgentID(), "request_id", requestID, "error", err)
	a.forgetControl(requestID)
	if control.kind == controlPermission {
		a.replyPermissionLater(control.nativeID, mimoPermissionReplyBody{Reply: contracts.MiMoPermissionReplyReject, Message: providerkit.ControlPublicationFailure})
		return
	}
	a.rejectQuestionLater(control.nativeID)
}

// forgetControl drops a request and reports whether LeapMux held it.
func (a *Agent) forgetControl(requestID string) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	_, held := a.controls[requestID]
	delete(a.controls, requestID)
	return held
}

// handlePermissionReplied and handleQuestionSettled retire the card of a
// request that another client answered, or that LeapMux itself answered.
func (a *Agent) handlePermissionReplied(event mimoEvent) {
	a.retireSettled(event, permissionRequestPrefix)
}

func (a *Agent) handleQuestionSettled(event mimoEvent) {
	a.retireSettled(event, questionRequestPrefix)
}

func (a *Agent) retireSettled(event mimoEvent, prefix string) {
	var settled mimoSettled
	if err := json.Unmarshal(event.Properties, &settled); err != nil || settled.RequestID == "" {
		return
	}
	requestID := prefix + settled.RequestID
	if a.forgetControl(requestID) {
		a.sink.CancelControlRequest(requestID)
	}
}

// retireMainControls withdraws the main agent's requests when its turn ends.
//
// MiMo removes a pending permission without an event after abort.
// MiMo 0.1.14 also keeps an unanswered question without an event.
// The finished turn cannot accept an answer.
// The worker withdraws its cards and rejects its remaining native questions.
func (a *Agent) retireMainControls() {
	a.retireControls(func(control *mimoControl) bool {
		return control.actorKnown && (control.actorID == mainActorID || control.actorID == "")
	})
}

// retireActorControls withdraws a subagent's requests when its turn ends.
func (a *Agent) retireActorControls(actorID string) {
	a.retireControls(func(control *mimoControl) bool { return control.actorID == actorID })
}

// retireAllControls withdraws every request, for a session this agent leaves.
func (a *Agent) retireAllControls() {
	a.retireControls(func(*mimoControl) bool { return true })
}

func (a *Agent) retireControls(match func(*mimoControl) bool) {
	a.Mu.Lock()
	retired := map[string]*mimoControl{}
	for requestID, control := range a.controls {
		if match(control) {
			retired[requestID] = control
			delete(a.controls, requestID)
		}
	}
	a.Mu.Unlock()
	for requestID, control := range retired {
		a.sink.CancelControlRequest(requestID)
		if control.kind != controlPermission {
			a.rejectQuestionLater(control.nativeID)
		}
	}
}

// --- plan text ---

// mimoMaxPlanBytes caps the plan text a plan approval carries. A plan is prose
// the model wrote, and a file past this size is not one a reader reviews in a
// banner.
const mimoMaxPlanBytes = 1 << 20

// readPlan reads the path that the approval specifies.
// MiMo resolves that path from the project root.
// The root can be the working directory or one of its ancestors.
// The worker tries each directory in order.
//
// The read is limited to a Markdown file of limited size, because the path is
// text from the agent's process. A failed read leaves the approval without plan text.
// The transcript still holds the native write that produced the plan.
func (a *Agent) readPlan(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || !strings.EqualFold(filepath.Ext(path), ".md") {
		return ""
	}
	var candidates []string
	if filepath.IsAbs(path) {
		candidates = []string{filepath.Clean(path)}
	} else {
		for dir := filepath.Clean(a.workingDir); ; dir = filepath.Dir(dir) {
			candidates = append(candidates, filepath.Join(dir, path))
			if parent := filepath.Dir(dir); parent == dir {
				break
			}
		}
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if info.Size() > mimoMaxPlanBytes {
			slog.Warn("mimo plan file is too large to show", "agent_id", a.AgentID(), "path", candidate, "bytes", info.Size())
			return ""
		}
		data, err := os.ReadFile(candidate)
		if err != nil {
			slog.Warn("mimo read plan file", "agent_id", a.AgentID(), "path", candidate, "error", err)
			return ""
		}
		if !utf8.Valid(data) {
			return ""
		}
		return string(data)
	}
	return ""
}

// --- interactive commands ---

// interactiveRefusal is the output MiMo's shell tool returns to the model for a
// command that waits for keyboard input. It states why and what to do instead.
const interactiveRefusal = "LeapMux cannot run an interactive command: no user can type into it here. " +
	"Run the command without interactive mode. Supply input on the command line or through a file. " +
	"Use a non-interactive flag such as --yes when the command supports it."

// interactiveRefusalExitCode is the exit code of a refused interactive command.
const interactiveRefusalExitCode = 1

// mimoBashAsked is bash.interactive.asked. The event also carries the
// command's whole environment, which this type does not read, so no copy of it
// reaches a log or a row.
type mimoBashAsked struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

// handleBashInteractiveAsked refuses an interactive command immediately.
// The shell tool waits for an answer.
// Refusal releases that wait and returns its reason as native tool output.
func (a *Agent) handleBashInteractiveAsked(properties json.RawMessage) {
	var asked mimoBashAsked
	if err := json.Unmarshal(properties, &asked); err != nil || asked.ID == "" {
		slog.Warn("mimo bash.interactive.asked unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}
	slog.Info("mimo refused an interactive command", "agent_id", a.AgentID(), "request_id", asked.ID, "description", asked.Description)
	id := asked.ID
	go func() {
		err := a.rpc.replyBashInteractive(a.Context(), id, mimoBashReplyBody{Output: interactiveRefusal, ExitCode: interactiveRefusalExitCode})
		if err != nil {
			slog.Warn("mimo refuse interactive command", "agent_id", a.AgentID(), "request_id", id, "error", err)
		}
	}()
}

// --- replies ---

// replyPermissionLater and rejectQuestionLater send native answers outside the stream goroutine.
// The stream can read new events while the HTTP request waits.
func (a *Agent) replyPermissionLater(permissionID string, body mimoPermissionReplyBody) {
	go func() {
		if err := a.rpc.replyPermission(a.Context(), permissionID, body); err != nil {
			slog.Warn("mimo permission reply", "agent_id", a.AgentID(), "permission_id", permissionID, "error", err)
		}
	}()
}

func (a *Agent) rejectQuestionLater(questionID string) {
	go func() {
		if err := a.rpc.rejectQuestion(a.Context(), questionID); err != nil && !providerkit.IsHTTPStatus(err, http.StatusNotFound) {
			slog.Debug("mimo question reject", "agent_id", a.AgentID(), "question_id", questionID, "error", err)
		}
	}()
}

// mimoAnswer is one browser answer, read into what MiMo takes.
type mimoAnswer struct {
	requestID  string
	permission mimoPermissionReplyBody
	answers    [][]string
	reject     bool
	// elicitation marks answers that the browser's elicitation form wrote as an
	// MCP action. Only a question that offers MiMo's word for that action can take
	// them (see answerFitsRequest).
	elicitation bool
}

// Plan-approval answers. MiMo's plan tool approves on "Yes", keeps plan mode on
// "No", and reads any other text as the user's feedback.
const (
	planAnswerYes = "Yes"
	planAnswerNo  = "No"
)

// MCP elicitation answers. MiMo HEAD asks an MCP server's confirmation as one
// question with these three options (mcp/elicitation.ts). It reads "Accept" and
// "Decline" as those MCP actions, and any other answer as cancel.
const (
	elicitationAnswerAccept  = "Accept"
	elicitationAnswerDecline = "Decline"
	elicitationAnswerCancel  = "Cancel"
)

// mimoJSONRPCAnswer is the answer envelope a browser control writes for a
// question or a chosen permission option. The two answer fields come from the
// OpenCode contract; TestMiMoAnswerTagsMatchTheContract pins the tags.
type mimoJSONRPCAnswer struct {
	Result struct {
		Answers  [][]string `json:"answers"`
		Rejected bool       `json:"rejected"`
		Outcome  *struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	} `json:"result"`
}

// readControlAnswer reads one browser answer to a stored request of kind, and
// checks it against that request. Both the answer check and the reply read here,
// so the two cannot accept different answers. Its error is the reason the reader
// sees for the refusal.
func readControlAnswer(kind mimoControlKind, payload, content []byte) (mimoAnswer, error) {
	answer, err := parseControlAnswer(kind, content)
	if err != nil {
		return mimoAnswer{}, err
	}
	if err := answerFitsRequest(answer, payload); err != nil {
		return mimoAnswer{}, err
	}
	return answer, nil
}

// answerFitsRequest checks the answer against its native request.
// An elicitation answer can select only an MCP action that the question offers.
// An ordinary question cannot interpret Accept as that action because the user did not select it.
func answerFitsRequest(answer mimoAnswer, payload []byte) error {
	if !answer.elicitation {
		return nil
	}
	var stored struct {
		Properties struct {
			Questions []struct {
				Options []struct {
					Label string `json:"label"`
				} `json:"options"`
			} `json:"questions"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(payload, &stored); err != nil {
		return errors.New(agent.RefusalUnreadableRequest)
	}
	word := answer.answers[0][0]
	if questions := stored.Properties.Questions; len(questions) == 1 {
		for _, option := range questions[0].Options {
			if option.Label == word {
				return nil
			}
		}
	}
	return errors.New(agent.RefusalUnofferedOption(leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE, word))
}

// parseElicitationAnswer reads the shared elicitation envelope and its MCP action.
// MiMo requests that confirmation as a question.
// The handler converts the selected action to MiMo's native answer.
// ok is false for other answer shapes.
func parseElicitationAnswer(kind mimoControlKind, content []byte) (answer mimoAnswer, ok bool, err error) {
	var envelope providerkit.MCPElicitationControlResponse
	if json.Unmarshal(content, &envelope) != nil || envelope.Response.Response.Action == "" {
		return mimoAnswer{}, false, nil
	}
	if kind != controlQuestion {
		return mimoAnswer{}, true, errors.New("an elicitation action answers only a question")
	}
	requestID := strings.TrimSpace(envelope.Response.RequestID)
	if requestID == "" {
		return mimoAnswer{}, true, errors.New("the answer states no request")
	}
	var word string
	switch action := envelope.Response.Response.Action; action {
	case contracts.MCPElicitationActionAccept:
		word = elicitationAnswerAccept
	case contracts.MCPElicitationActionDecline:
		word = elicitationAnswerDecline
	case contracts.MCPElicitationActionCancel:
		word = elicitationAnswerCancel
	default:
		return mimoAnswer{}, true, fmt.Errorf("unknown elicitation action %q", action)
	}
	return mimoAnswer{requestID: requestID, answers: [][]string{{word}}, elicitation: true}, true, nil
}

// parseControlAnswer reads one browser answer to a request of kind.
//
// Three envelopes reach here. The shared allow/deny envelope answers a
// permission and a plan approval, and a deny answers a question as a rejection.
// The shared elicitation envelope answers an MCP server's confirmation, which
// MiMo asks as a question. The JSON-RPC result answers a question with its
// answers, and a permission with the option the user picked.
func parseControlAnswer(kind mimoControlKind, content []byte) (mimoAnswer, error) {
	if requestID, behavior, message, ok := agent.DecodeControlBehavior(content); ok && requestID != "" && behavior != "" {
		answer := mimoAnswer{requestID: requestID}
		switch {
		case behavior != agent.ControlBehaviorAllow && behavior != agent.ControlBehaviorDeny:
			return mimoAnswer{}, errors.New(agent.RefusalNoDecision)
		case kind == controlPermission && behavior == agent.ControlBehaviorAllow:
			answer.permission = mimoPermissionReplyBody{Reply: contracts.MiMoPermissionReplyOnce}
		case kind == controlPermission:
			answer.permission = mimoPermissionReplyBody{Reply: contracts.MiMoPermissionReplyReject, Message: message}
		case kind == controlPlan && behavior == agent.ControlBehaviorAllow:
			answer.answers = [][]string{{planAnswerYes}}
		case kind == controlPlan:
			answer.answers = [][]string{{planFeedback(message)}}
		case behavior == agent.ControlBehaviorDeny:
			answer.reject = true
		default:
			return mimoAnswer{}, errors.New("an allow does not answer a question")
		}
		return answer, nil
	}
	if answer, ok, err := parseElicitationAnswer(kind, content); ok {
		return answer, err
	}
	_, requestID, ok := agent.ExtractJSONRPCID(content)
	if !ok || requestID == "" {
		return mimoAnswer{}, errors.New("the answer states no request")
	}
	var envelope mimoJSONRPCAnswer
	if err := json.Unmarshal(content, &envelope); err != nil {
		return mimoAnswer{}, errors.New(agent.RefusalUnreadableAnswer)
	}
	answer := mimoAnswer{requestID: requestID}
	switch kind {
	case controlPermission:
		outcome := envelope.Result.Outcome
		if outcome == nil {
			return mimoAnswer{}, errors.New("the permission answer states no option")
		}
		if outcome.Outcome != contracts.ACPPermissionOutcomeSelected {
			answer.permission = mimoPermissionReplyBody{Reply: contracts.MiMoPermissionReplyReject}
			return answer, nil
		}
		switch outcome.OptionID {
		case contracts.MiMoPermissionReplyOnce, contracts.MiMoPermissionReplyAlways, contracts.MiMoPermissionReplyReject:
			answer.permission = mimoPermissionReplyBody{Reply: outcome.OptionID}
		default:
			return mimoAnswer{}, errors.New(agent.RefusalUnofferedOption(leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE, outcome.OptionID))
		}
	case controlQuestion:
		switch {
		case envelope.Result.Rejected:
			answer.reject = true
		case envelope.Result.Answers != nil:
			answer.answers = envelope.Result.Answers
		default:
			return mimoAnswer{}, errors.New("the question answer carries neither answers nor a rejection")
		}
	case controlPlan:
		if !envelope.Result.Rejected {
			return mimoAnswer{}, errors.New("a plan approval takes an allow or a deny")
		}
		answer.answers = [][]string{{planAnswerNo}}
	default:
		return mimoAnswer{}, fmt.Errorf("unknown control kind %d", kind)
	}
	return answer, nil
}

// planFeedback is the answer that rejects a plan: the user's feedback, or "No"
// when the user supplied none. Feedback that matches an option would select that option.
// Yes would approve the plan. The added period keeps that text as feedback.
func planFeedback(message string) string {
	message = strings.TrimSpace(message)
	switch message {
	case "":
		return planAnswerNo
	case planAnswerYes, planAnswerNo:
		return message + "."
	default:
		return message
	}
}

// controlKindOfPayload reads the kind of a stored payload.
func controlKindOfPayload(payload []byte) (mimoControlKind, bool) {
	var stored struct {
		Type    string            `json:"type"`
		Request mimoControlHeader `json:"request"`
	}
	if err := json.Unmarshal(payload, &stored); err != nil {
		return 0, false
	}
	switch stored.Type {
	case contracts.MiMoEventPermissionAsked:
		return controlPermission, true
	case contracts.OpenCodeEventQuestionAsked:
		if stored.Request.ToolName == contracts.MiMoToolPlanExit {
			return controlPlan, true
		}
		return controlQuestion, true
	default:
		return 0, false
	}
}

// executeControlReply sends one answer to MiMo. The request must be one that
// MiMo still waits on and LeapMux published.
func (a *Agent) executeControlReply(data []byte) error {
	requestID := mimoProvider{}.ControlResponseRequestID(data)
	a.Mu.Lock()
	control := a.controls[requestID]
	a.Mu.Unlock()
	if control == nil {
		return fmt.Errorf("MiMo holds no pending request %q", requestID)
	}
	answer, err := readControlAnswer(control.kind, control.payload, data)
	if err != nil {
		return fmt.Errorf("read the answer to %q: %w", requestID, err)
	}
	ctx := a.Context()
	switch control.kind {
	case controlPermission:
		err = a.rpc.replyPermission(ctx, control.nativeID, answer.permission)
	default:
		if answer.reject {
			err = a.rpc.rejectQuestion(ctx, control.nativeID)
		} else {
			err = a.rpc.replyQuestion(ctx, control.nativeID, answer.answers)
		}
	}
	if providerkit.IsHTTPStatus(err, http.StatusNotFound) {
		// MiMo no longer holds the request: its turn ended, or another client
		// answered it. No answer can reach it now, so its card goes.
		if a.forgetControl(requestID) {
			a.sink.CancelControlRequest(requestID)
		}
		return fmt.Errorf("MiMo no longer waits on %q: %w", requestID, err)
	}
	if err != nil {
		return fmt.Errorf("send the answer to %q: %w", requestID, err)
	}
	a.forgetControl(requestID)
	if control.kind == controlPlan && len(answer.answers) == 1 && len(answer.answers[0]) == 1 && answer.answers[0][0] == planAnswerYes {
		// MiMo moves the session to build as it takes the answer. The next prompt
		// must say build too, or it would put the session back into plan mode.
		a.adoptMode(contracts.MiMoModeBuild)
	}
	return nil
}

// restatePendingControls reconciles the cards with the requests MiMo holds,
// after the event stream reconnected. A request raised while the stream was
// down gets its card, and a card whose request MiMo no longer holds is retired.
// A list that cannot be read changes nothing, because a failed read is not a
// statement that MiMo holds none.
func (a *Agent) restatePendingControls(ctx context.Context) {
	permissions, permissionErr := a.rpc.pendingPermissions(ctx)
	questions, questionErr := a.rpc.pendingQuestions(ctx)
	held := map[string]bool{}
	if permissionErr == nil {
		for _, raw := range permissions {
			var asked mimoPermissionAsked
			if json.Unmarshal(raw, &asked) == nil && asked.ID != "" {
				held[permissionRequestPrefix+asked.ID] = true
				a.handlePermissionAsked(raw)
			}
		}
	} else {
		slog.Debug("mimo list pending permissions", "agent_id", a.AgentID(), "error", permissionErr)
	}
	if questionErr == nil {
		for _, raw := range questions {
			var asked mimoQuestionAsked
			if json.Unmarshal(raw, &asked) == nil && asked.ID != "" {
				held[questionRequestPrefix+asked.ID] = true
				a.handleQuestionAsked(raw)
			}
		}
	} else {
		slog.Debug("mimo list pending questions", "agent_id", a.AgentID(), "error", questionErr)
	}
	a.retireControls(func(control *mimoControl) bool {
		listRead := permissionErr == nil
		prefix := permissionRequestPrefix
		if control.kind != controlPermission {
			listRead = questionErr == nil
			prefix = questionRequestPrefix
		}
		return listRead && !held[prefix+control.nativeID]
	})
	if interactive, err := a.rpc.pendingBashInteractive(ctx); err == nil {
		for _, raw := range interactive {
			a.handleBashInteractiveAsked(raw)
		}
	}
}
