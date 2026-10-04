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

// The question bridge.
//
// omp's `ask` tool asks its questions through a CHAIN of dialogs, one select per
// question: a multi-select question toggles one option per select, and "Other"
// opens an editor. LeapMux's question surface asks the whole set at once and
// returns every answer together. The bridge joins the two:
//
//  1. The assistant message that calls `ask` states the questions. The bridge
//     keeps them (rememberAskCalls).
//  2. The FIRST dialog of the call arrives before the call's tool_execution_start.
//     The bridge checks that it asks the first question, then publishes ONE
//     control request for the whole call under that dialog's id
//     (contracts.OhMyPiAskRequest).
//  3. The browser answers with one contracts.OhMyPiAskAnswer. The bridge answers
//     the first dialog, and then every later dialog of the chain as it arrives,
//     from that one answer.
//
// A dialog that does not have the shape the bridge expects is published as it is,
// so the reader can still answer it by hand, and the bridge answers nothing more
// for that call.
//
// omp cannot finish a multi-select question inside a multi-question call the
// usual way: in that mode it offers no "Done" option, because it expects the
// arrow keys of its own terminal to move to the next question, and RPC forwards
// no key. The bridge finishes such a question through "Other" instead, and the
// model then reads the chosen labels as the reader's own text.

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
		var args struct {
			Questions []contracts.OhMyPiAskQuestion `json:"questions"`
		}
		if json.Unmarshal(block.Arguments, &args) != nil || len(args.Questions) == 0 {
			// omp refuses such a call before it opens any dialog, so there is no
			// chain to follow.
			continue
		}
		calls = append(calls, &askCall{toolCallID: block.ID, questions: args.Questions})
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
	if head.Method != contracts.OhMyPiDialogMethodSelect && head.Method != contracts.OhMyPiDialogMethodEditor {
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
		call.plan = nil
		a.Mu.Unlock()
		return fmt.Errorf("answer the question: %w", err)
	}
	return nil
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

// opensAsk reports whether a dialog is the first dialog of a call: the select of
// its first question.
func opensAsk(questions []contracts.OhMyPiAskQuestion, head dialogHeader) bool {
	if head.Method != contracts.OhMyPiDialogMethodSelect || len(questions) == 0 {
		return false
	}
	plan := &askPlan{questions: questions}
	return plan.matchesSelect(0, 0, head) == nil
}

// askPlan answers one call's dialog chain from the reader's answers.
type askPlan struct {
	questions []contracts.OhMyPiAskQuestion
	answers   []contracts.OhMyPiAskQuestionAnswer
	// index is the question the next dialog asks.
	index int
	// toggled counts the options a multi-select question has toggled so far.
	toggled int
	// other is true after the bridge chose "Other"; an editor dialog follows.
	other bool
}

// newAskPlan checks the reader's answers against the questions and orders them
// like the questions.
func newAskPlan(questions []contracts.OhMyPiAskQuestion, answers []contracts.OhMyPiAskQuestionAnswer) (*askPlan, error) {
	byID := make(map[string]contracts.OhMyPiAskQuestionAnswer, len(answers))
	for _, answer := range answers {
		if _, duplicate := byID[answer.ID]; duplicate {
			return nil, fmt.Errorf("the answer states question %q twice", answer.ID)
		}
		byID[answer.ID] = answer
	}
	ordered := make([]contracts.OhMyPiAskQuestionAnswer, len(questions))
	for i, question := range questions {
		answer, ok := byID[question.ID]
		if !ok {
			return nil, fmt.Errorf("the answer states nothing for question %q", question.ID)
		}
		labels := make([]string, len(question.Options))
		for j, option := range question.Options {
			labels[j] = option.Label
		}
		seen := make(map[string]bool, len(answer.Selected))
		for _, label := range answer.Selected {
			if !slices.Contains(labels, label) {
				return nil, fmt.Errorf("question %q offers no option %q", question.ID, label)
			}
			if seen[label] {
				return nil, fmt.Errorf("question %q selects option %q twice", question.ID, label)
			}
			seen[label] = true
		}
		switch {
		case strings.TrimSpace(answer.Custom) != "":
		case question.Multi && len(answer.Selected) > 0:
		case !question.Multi && len(answer.Selected) == 1:
		case !question.Multi && len(answer.Selected) > 1:
			return nil, fmt.Errorf("question %q takes one option", question.ID)
		default:
			return nil, fmt.Errorf("question %q has no answer", question.ID)
		}
		ordered[i] = answer
	}
	return &askPlan{questions: questions, answers: ordered}, nil
}

// multiQuestion reports whether the call asks more than one question. omp then
// numbers each title, and offers no "Done" option for a multi-select question.
func (p *askPlan) multiQuestion() bool { return len(p.questions) > 1 }

// title returns the title omp gives the select of one question, with the number
// of options already toggled.
func (p *askPlan) title(index, toggled int) string {
	title := p.questions[index].Question
	if p.multiQuestion() {
		title += " (" + strconv.Itoa(index+1) + "/" + strconv.Itoa(len(p.questions)) + ")"
	}
	if toggled > 0 {
		title = askSelectedPrefixOpen + strconv.Itoa(toggled) + askSelectedPrefixClose + title
	}
	return title
}

// askRowEllipsis ends a title row that omp shortened to fit its terminal.
const askRowEllipsis = "…"

// editorContinuesQuestion reports whether an editor title opens with the question
// row of the select before it, then a blank line.
//
// omp shortens that row to the width of its terminal, less the panel's chrome
// (tools/ask.ts, clampLineToWidth). In RPC mode stdout is a pipe, so the width is
// omp's default, and the width appears nowhere in the dialog. So the row is either
// the whole title, or a prefix of it that ends with an ellipsis. The select titles
// are not shortened, so they keep the exact match of matchesSelect.
func editorContinuesQuestion(editorTitle, question string) bool {
	row, rest, found := strings.Cut(editorTitle, "\n")
	if !found || !strings.HasPrefix(rest, "\n") {
		return false
	}
	if row == question {
		return true
	}
	prefix, shortened := strings.CutSuffix(row, askRowEllipsis)
	return shortened && prefix != "" && strings.HasPrefix(question, prefix)
}

// errAskShape reports a dialog that does not have the shape the bridge expects.
var errAskShape = errors.New("the dialog does not continue the question chain")

// matchesSelect checks that a select dialog asks one question, after `toggled`
// toggles. The options must start with the question's own labels, in order, and
// end with "Other".
func (p *askPlan) matchesSelect(index, toggled int, head dialogHeader) error {
	if head.Method != contracts.OhMyPiDialogMethodSelect || index >= len(p.questions) {
		return errAskShape
	}
	if head.Title != p.title(index, toggled) {
		return fmt.Errorf("%w: title %q", errAskShape, head.Title)
	}
	question := p.questions[index]
	options := head.Options
	extra := len(options) - len(question.Options)
	// "Other" always; "Done" after the first toggle of a single multi-select.
	if extra < 1 || extra > 2 || options[len(options)-1] != askOtherOption {
		return fmt.Errorf("%w: %d options", errAskShape, len(options))
	}
	for i, option := range question.Options {
		if !strings.HasPrefix(options[i], option.Label) {
			return fmt.Errorf("%w: option %d is %q", errAskShape, i, options[i])
		}
	}
	return nil
}

// respond returns the value that answers one dialog of the chain, and advances the
// plan past it.
func (p *askPlan) respond(head dialogHeader) (string, error) {
	if p.index >= len(p.questions) {
		return "", fmt.Errorf("%w: every question is answered", errAskShape)
	}
	question := p.questions[p.index]
	answer := p.answers[p.index]
	if head.Method == contracts.OhMyPiDialogMethodEditor {
		if !p.other || !editorContinuesQuestion(head.Title, p.title(p.index, p.toggled)) {
			return "", fmt.Errorf("%w: editor %q", errAskShape, head.Title)
		}
		text := answer.Custom
		if question.Multi && p.multiQuestion() {
			text = joinedAnswer(answer)
		}
		p.advance()
		return text, nil
	}
	if err := p.matchesSelect(p.index, p.toggled, head); err != nil {
		return "", err
	}
	other := head.Options[len(head.Options)-1]
	if !question.Multi {
		if strings.TrimSpace(answer.Custom) != "" {
			p.other = true
			return other, nil
		}
		value := head.Options[slices.IndexFunc(question.Options, func(o contracts.OhMyPiAskOption) bool { return o.Label == answer.Selected[0] })]
		p.advance()
		return value, nil
	}
	if p.toggled < len(answer.Selected) {
		label := answer.Selected[p.toggled]
		index := slices.IndexFunc(question.Options, func(o contracts.OhMyPiAskOption) bool { return o.Label == label })
		p.toggled++
		return head.Options[index], nil
	}
	if p.multiQuestion() || strings.TrimSpace(answer.Custom) != "" {
		// Finish through "Other": the editor that follows carries the text.
		p.other = true
		return other, nil
	}
	// A single multi-select question finishes on "Done", which omp adds after the
	// first toggle, just before "Other".
	if len(head.Options) != len(question.Options)+2 {
		return "", fmt.Errorf("%w: no option finishes the selection", errAskShape)
	}
	done := head.Options[len(question.Options)]
	p.advance()
	return done, nil
}

// advance moves the plan to the next question.
func (p *askPlan) advance() {
	p.index++
	p.toggled = 0
	p.other = false
}

// joinedAnswer is the text that finishes a multi-select question inside a
// multi-question call: the chosen labels, then the reader's own text.
func joinedAnswer(answer contracts.OhMyPiAskQuestionAnswer) string {
	parts := make([]string, 0, 2)
	if len(answer.Selected) > 0 {
		parts = append(parts, strings.Join(answer.Selected, ", "))
	}
	if custom := strings.TrimSpace(answer.Custom); custom != "" {
		parts = append(parts, custom)
	}
	return strings.Join(parts, "; ")
}
