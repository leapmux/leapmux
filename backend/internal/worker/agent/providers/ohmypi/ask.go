package ohmypi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// The choice-form bridge retains a native HTML ask call until its first select.
// LeapMux asks the choice fields together, then answers the native selects and
// custom input dialogs from the submitted values. Unexpected dialogs remain
// available through the native dialog surface. No native answer is guessed.

// askBridge is the question bridge's state. Guarded by Process.Mu.
type askBridge struct {
	// calls holds the `ask` calls of the current run that have not ended, in the
	// order the assistant stated them. omp runs `ask` alone in its tool batch, so
	// the first call that has not ended owns the next dialog.
	calls []*askCall
}

// askCall is one `ask` call the bridge follows.
type askCall struct {
	toolCallID string
	questions  []contracts.OhMyPiAskQuestion
	// requestID is the id of the control request the bridge published, which is
	// the id of the call's first dialog. Empty until the first dialog arrives.
	requestID string
	// firstDialog is the call's first dialog, which omp waits on until the reader
	// answers the request.
	firstDialog dialogHeader
	// plan answers the chain. Nil until the reader answers.
	plan *askPlan
	// handOff marks a call whose chain left the expected shape. Every later
	// dialog of the call is published as it is.
	handOff bool
}

// clearLocked forgets every call. The caller holds Process.Mu.
func (b *askBridge) clearLocked() {
	b.calls = nil
}

// current returns the first call that has not ended, or nil.
func (b *askBridge) current() *askCall {
	if len(b.calls) == 0 {
		return nil
	}
	return b.calls[0]
}

// remove forgets one call by its tool call id.
func (b *askBridge) remove(toolCallID string) *askCall {
	for i, call := range b.calls {
		if call.toolCallID == toolCallID {
			b.calls = slices.Delete(b.calls, i, i+1)
			return call
		}
	}
	return nil
}

// byRequest returns the call whose control request has the given id.
func (b *askBridge) byRequest(requestID string) *askCall {
	if requestID == "" {
		return nil
	}
	for _, call := range b.calls {
		if call.requestID == requestID {
			return call
		}
	}
	return nil
}

// rememberAskCalls keeps the `ask` calls an assistant message states.
func (a *Agent) rememberAskCalls(content json.RawMessage) {
	var blocks []struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(content) == 0 || json.Unmarshal(content, &blocks) != nil {
		return
	}
	var calls []*askCall
	for _, block := range blocks {
		if block.Type != contentBlockToolCall || block.Name != contracts.OhMyPiToolAsk || block.ID == "" {
			continue
		}
		var args contracts.OhMyPiAskArguments
		if json.Unmarshal(block.Arguments, &args) != nil {
			continue
		}
		questions, err := readAskChoiceQuestions(args.HTML)
		if err != nil || len(questions) == 0 {
			// Unsupported forms keep their native dialogs. A form with no named
			// fields opens no dialogs.
			continue
		}
		calls = append(calls, &askCall{toolCallID: block.ID, questions: questions})
	}
	if len(calls) == 0 {
		return
	}
	a.Mu.Lock()
	a.asks.calls = append(a.asks.calls, calls...)
	a.Mu.Unlock()
}

// routeAskDialog offers one dialog to the question bridge, and reports whether the
// bridge took it: published it as the call's request, or answered it.
func (a *Agent) routeAskDialog(head dialogHeader) bool {
	if head.Method != contracts.OhMyPiDialogMethodSelect && head.Method != contracts.OhMyPiDialogMethodInput {
		return false
	}
	a.Mu.Lock()
	call := a.asks.current()
	if call == nil || call.handOff {
		a.Mu.Unlock()
		return false
	}
	if call.requestID == "" {
		if !opensAsk(call.questions, head) {
			// Not the first question of the call: a dialog of something else.
			// The call stays, and its own first dialog can still arrive.
			a.Mu.Unlock()
			return false
		}
		call.requestID = head.ID
		call.firstDialog = head
		request := contracts.OhMyPiAskRequest{
			Type:      contracts.OhMyPiAskTypeRequest,
			ID:        head.ID,
			Questions: call.questions,
		}
		a.Mu.Unlock()
		payload, err := json.Marshal(request)
		if err != nil {
			slog.Error("omp encode question request", "agent_id", a.AgentID(), "error", err)
			a.handOffAsk(call)
			return false
		}
		if len(payload) > contracts.MaxMessageSize {
			a.handOffAsk(call)
			return false
		}
		if err := a.sink.PublishControlRequest(agent.ControlRequest{RequestID: head.ID, Payload: payload}); err != nil {
			slog.Error("omp publish question request", "agent_id", a.AgentID(), "request_id", head.ID, "error", err)
			a.cancelDialog(head.ID)
		}
		return true
	}
	if call.plan == nil {
		// A second dialog before the reader answered the first. omp waits on the
		// first, so this belongs to something else.
		a.Mu.Unlock()
		return false
	}
	value, err := call.plan.respond(head)
	if err != nil {
		call.handOff = true
		a.Mu.Unlock()
		slog.Warn("omp question dialog has an unexpected shape; the reader answers it by hand",
			"agent_id", a.AgentID(), "request_id", head.ID, "error", err)
		return false
	}
	a.Mu.Unlock()
	if err := a.answerDialog(head.ID, value); err != nil {
		slog.Warn("omp answer question dialog", "agent_id", a.AgentID(), "request_id", head.ID, "error", err)
	}
	return true
}

// handOffAsk marks a call so that every later dialog of it is published as it is.
func (a *Agent) handOffAsk(call *askCall) {
	a.Mu.Lock()
	call.handOff = true
	call.requestID = ""
	a.Mu.Unlock()
}

// answerAsk answers the bridge's request: it checks the reader's answers against
// the call's questions, and answers the dialog omp waits on.
//
// An answer the bridge cannot use fails here, before anything reaches omp, so the
// request stays open for another answer.
func (a *Agent) answerAsk(data []byte) error {
	var answer contracts.OhMyPiAskAnswer
	if err := json.Unmarshal(data, &answer); err != nil {
		return fmt.Errorf("decode the question answer: %w", err)
	}
	a.Mu.Lock()
	call := a.asks.byRequest(answer.ID)
	if call != nil && call.plan != nil {
		a.Mu.Unlock()
		return fmt.Errorf("the question %q is already answered", answer.ID)
	}
	if call == nil || call.handOff {
		// A handed-off call's first dialog is gone: the reader cancelled it, omp
		// withdrew it, or it was never published. omp ignores an answer to it.
		a.Mu.Unlock()
		return fmt.Errorf("the question %q is no longer open", answer.ID)
	}
	plan, err := newAskPlan(call.questions, answer.Answers)
	if err != nil {
		a.Mu.Unlock()
		return err
	}
	value, err := plan.respond(call.firstDialog)
	if err != nil {
		a.Mu.Unlock()
		return err
	}
	call.plan = plan
	requestID := call.requestID
	a.Mu.Unlock()
	if err := a.answerDialog(requestID, value); err != nil {
		a.Mu.Lock()
		if !errors.Is(err, agent.ErrDeliveryUncertain) {
			call.plan = nil
		}
		a.Mu.Unlock()
		return fmt.Errorf("answer the question: %w", err)
	}
	return nil
}

// cancelAsk claims a question cancellation before it reaches the native dialog.
// A definite write failure keeps the question available for another answer.
func (a *Agent) cancelAsk(requestID string, data []byte, stop agent.StopContext) (bool, error) {
	a.Mu.Lock()
	call := a.asks.byRequest(requestID)
	if call == nil {
		a.Mu.Unlock()
		return false, nil
	}
	if call.handOff || call.plan != nil {
		a.Mu.Unlock()
		return true, fmt.Errorf("the question %q is no longer open", requestID)
	}
	call.handOff = true
	a.Mu.Unlock()
	err := a.Process.SendRawInput(data, stop)
	if err != nil && !errors.Is(err, agent.ErrDeliveryUncertain) {
		a.Mu.Lock()
		if a.asks.byRequest(requestID) == call {
			call.handOff = false
		}
		a.Mu.Unlock()
	}
	return true, err
}

// forgetAskDialog drops the call whose request has the given id: the reader
// cancelled it, or omp withdrew it. A later dialog of that call, if any, is
// published as it is.
func (a *Agent) forgetAskDialog(requestID string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if call := a.asks.byRequest(requestID); call != nil {
		call.handOff = true
	}
}

// finishAsk drops a call that ended. A request the reader never answered -- omp
// answered it on a timeout, or the call failed -- is withdrawn.
func (a *Agent) finishAsk(toolCallID string) {
	a.Mu.Lock()
	call := a.asks.remove(toolCallID)
	a.Mu.Unlock()
	if call != nil && call.requestID != "" && call.plan == nil && !call.handOff {
		a.sink.CancelControlRequest(call.requestID)
	}
}

// opensAsk matches the first select, including its native preselected count.
func opensAsk(questions []contracts.OhMyPiAskQuestion, head dialogHeader) bool {
	if len(questions) == 0 {
		return false
	}
	plan := &askPlan{questions: questions}
	return plan.matchesSelect(0, len(questions[0].InitialSelections), head) == nil
}

type askActionKind uint8

const (
	askSelectOption askActionKind = iota
	askSelectOther
	askInputOther
	askSelectDone
)

type askDialogAction struct {
	questionIndex int
	selectedCount int
	kind          askActionKind
	value         string
}

// askPlan retains exact native actions compiled from one submitted answer set.
type askPlan struct {
	questions     []contracts.OhMyPiAskQuestion
	actions       []askDialogAction
	nextAction    int
	questionIndex int
}

func askOptionValue(option contracts.OhMyPiAskOption) string {
	if option.Value != "" {
		return option.Value
	}
	return option.Label
}

func newAskPlan(questions []contracts.OhMyPiAskQuestion, answers []contracts.OhMyPiAskQuestionAnswer) (*askPlan, error) {
	if len(questions) == 0 {
		return nil, fmt.Errorf("the form has no questions")
	}
	byID := make(map[string]contracts.OhMyPiAskQuestionAnswer, len(answers))
	for _, answer := range answers {
		if _, duplicate := byID[answer.ID]; duplicate {
			return nil, fmt.Errorf("the answer states question %q twice", answer.ID)
		}
		byID[answer.ID] = answer
	}
	plan := &askPlan{questions: questions}
	for questionIndex, question := range questions {
		answer, exists := byID[question.ID]
		if !exists {
			return nil, fmt.Errorf("the answer states nothing for question %q", question.ID)
		}
		delete(byID, question.ID)
		offered := make(map[string]string, len(question.Options))
		for _, option := range question.Options {
			value := askOptionValue(option)
			if _, duplicate := offered[value]; duplicate {
				return nil, fmt.Errorf("question %q repeats an option value", question.ID)
			}
			offered[value] = option.Label
		}
		desired := make(map[string]bool, len(answer.Selected))
		for _, value := range answer.Selected {
			if _, exists := offered[value]; !exists {
				return nil, fmt.Errorf("question %q offers no option %q", question.ID, value)
			}
			if desired[value] {
				return nil, fmt.Errorf("question %q selects option %q twice", question.ID, value)
			}
			desired[value] = true
		}
		custom := strings.TrimSpace(answer.Custom) != ""
		if custom && !question.AllowCustomText {
			return nil, fmt.Errorf("question %q accepts selected options only", question.ID)
		}
		if !question.Multi && len(answer.Selected) > 1 {
			return nil, fmt.Errorf("question %q takes one option", question.ID)
		}
		if !custom && len(desired) == 0 && (!question.Multi || !question.AllowEmpty) {
			return nil, fmt.Errorf("question %q has no answer", question.ID)
		}
		initial := make(map[string]bool, len(question.InitialSelections))
		for _, value := range question.InitialSelections {
			initial[value] = true
		}
		count := len(initial)
		appendAction := func(kind askActionKind, value string) {
			plan.actions = append(plan.actions, askDialogAction{questionIndex: questionIndex, selectedCount: count, kind: kind, value: value})
		}
		if !question.Multi {
			if custom {
				appendAction(askSelectOther, "")
				appendAction(askInputOther, answer.Custom)
			} else {
				appendAction(askSelectOption, offered[answer.Selected[0]])
			}
			continue
		}
		// Disabled preselected options remain in the native count. Only offered
		// choices can be toggled; the browser sends the desired enabled values.
		for _, option := range question.Options {
			value := askOptionValue(option)
			if initial[value] && !desired[value] {
				appendAction(askSelectOption, option.Label)
				count--
			}
		}
		for _, option := range question.Options {
			value := askOptionValue(option)
			if !initial[value] && desired[value] {
				appendAction(askSelectOption, option.Label)
				count++
			}
		}
		if custom {
			appendAction(askSelectOther, "")
			appendAction(askInputOther, answer.Custom)
			count++
		}
		appendAction(askSelectDone, "")
	}
	if len(byID) > 0 {
		return nil, fmt.Errorf("the answer includes a question the form did not ask")
	}
	return plan, nil
}

func (plan *askPlan) title(questionIndex, selectedCount int) string {
	question := plan.questions[questionIndex]
	title := question.Question
	if question.Multi && selectedCount > 0 {
		title = "(" + strconv.Itoa(selectedCount) + " selected) " + title
	}
	if question.Context != "" {
		return question.Context + "\n\n" + title
	}
	return title
}

func nativeAskOther(question contracts.OhMyPiAskQuestion) string {
	value := askOtherOption
	taken := make(map[string]bool, len(question.Options))
	for _, option := range question.Options {
		taken[option.Label] = true
	}
	for suffix := 2; taken[value]; suffix++ {
		value = askOtherOption + " (" + strconv.Itoa(suffix) + ")"
	}
	return value
}

var errAskShape = errors.New("the dialog does not continue the question chain")

func isAskDoneLabel(value string) bool {
	const text = "Done selecting"
	index := strings.LastIndex(value, text)
	if index < 0 || index > 0 && value[index-1] != ' ' {
		return false
	}
	suffix := value[index+len(text):]
	if suffix == "" {
		return true
	}
	if !strings.HasPrefix(suffix, " (") || !strings.HasSuffix(suffix, ")") {
		return false
	}
	digits := suffix[2 : len(suffix)-1]
	number, err := strconv.Atoi(digits)
	return err == nil && number >= 2 && strconv.Itoa(number) == digits
}

func (plan *askPlan) matchesSelect(questionIndex, selectedCount int, head dialogHeader) error {
	if questionIndex < 0 || questionIndex >= len(plan.questions) || selectedCount < 0 || head.Method != contracts.OhMyPiDialogMethodSelect {
		return errAskShape
	}
	question := plan.questions[questionIndex]
	if head.Title != plan.title(questionIndex, selectedCount) {
		return fmt.Errorf("%w: title %q", errAskShape, head.Title)
	}
	expected := len(question.Options)
	if question.AllowCustomText {
		expected++
	}
	done := question.Multi && (question.AllowEmpty || selectedCount > 0)
	if done {
		expected++
	}
	if len(head.Options) != expected {
		return fmt.Errorf("%w: %d options", errAskShape, len(head.Options))
	}
	for index, option := range question.Options {
		if head.Options[index] != option.Label {
			return fmt.Errorf("%w: option %d", errAskShape, index)
		}
	}
	if question.AllowCustomText && head.Options[len(question.Options)] != nativeAskOther(question) {
		return fmt.Errorf("%w: no native custom choice", errAskShape)
	}
	if done && !isAskDoneLabel(head.Options[len(head.Options)-1]) {
		return fmt.Errorf("%w: no native finish choice", errAskShape)
	}
	return nil
}

func (plan *askPlan) respond(head dialogHeader) (string, error) {
	if plan.nextAction >= len(plan.actions) {
		return "", fmt.Errorf("%w: every question is answered", errAskShape)
	}
	action := plan.actions[plan.nextAction]
	question := plan.questions[action.questionIndex]
	value := action.value
	switch action.kind {
	case askInputOther:
		if head.Method != contracts.OhMyPiDialogMethodInput || head.Title != plan.title(action.questionIndex, 0) {
			return "", fmt.Errorf("%w: native custom input", errAskShape)
		}
	case askSelectOption, askSelectOther, askSelectDone:
		if err := plan.matchesSelect(action.questionIndex, action.selectedCount, head); err != nil {
			return "", err
		}
		if action.kind == askSelectOther {
			value = nativeAskOther(question)
		}
		if action.kind == askSelectDone {
			value = head.Options[len(head.Options)-1]
		}
	default:
		return "", fmt.Errorf("%w: unsupported question action", errAskShape)
	}
	plan.nextAction++
	plan.questionIndex = len(plan.questions)
	if plan.nextAction < len(plan.actions) {
		plan.questionIndex = plan.actions[plan.nextAction].questionIndex
	}
	return value, nil
}
