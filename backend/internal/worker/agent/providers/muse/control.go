package muse

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type controlParams struct {
	SessionID   string          `json:"sessionId"`
	ApprovalID  string          `json:"approvalId"`
	UserInputID string          `json:"userInputId"`
	Requirement json.RawMessage `json:"currentRequirementId"`
	Choices     json.RawMessage `json:"availableChoices"`
	Questions   json.RawMessage `json:"questions"`
}

type controlState struct {
	key         string
	fingerprint [32]byte
}

type controlRecordKind uint8

const (
	controlPublish controlRecordKind = iota
	controlCancel
	controlSettlement
	controlWarning
)

type controlRecord struct {
	kind        controlRecordKind
	state       *sessionState
	sessionID   string
	nativeID    string
	key         string
	fingerprint [32]byte
	raw         []byte
	publishDone bool
	cancelKey   string
	cancelDone  bool
	err         error
}

func approvalControlID(params controlParams) (string, error) {
	var requirement struct {
		ApprovalID  string `json:"approvalId"`
		SourceIndex *int64 `json:"sourceIndex"`
	}
	if params.SessionID == "" || params.ApprovalID == "" || json.Unmarshal(params.Requirement, &requirement) != nil || requirement.ApprovalID != params.ApprovalID || requirement.SourceIndex == nil || *requirement.SourceIndex < 0 {
		return "", fmt.Errorf("the Muse approval supplies no valid requirement identity")
	}
	raw, err := json.Marshal([]any{params.SessionID, params.ApprovalID, *requirement.SourceIndex})
	if err != nil {
		return "", err
	}
	return "muse:approval:" + string(raw), nil
}

func controlID(params controlParams) (string, error) {
	if params.ApprovalID != "" {
		return approvalControlID(params)
	}
	if params.SessionID == "" || params.UserInputID == "" {
		return "", fmt.Errorf("the Muse question supplies no valid identity")
	}
	raw, err := json.Marshal([]string{params.SessionID, params.UserInputID})
	if err != nil {
		return "", err
	}
	return "muse:userInput:" + string(raw), nil
}

func canonicalControl(raw []byte) ([32]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return [32]byte{}, err
	}
	encoded, err := json.Marshal(value)
	return sha256.Sum256(encoded), err
}

func (a *Agent) publishControl(line *providerkit.ParsedLine) {
	var params controlParams
	if json.Unmarshal(line.Params, &params) != nil {
		return
	}
	key, err := controlID(params)
	if err != nil {
		a.enqueueControl(&controlRecord{kind: controlWarning, err: err})
		return
	}
	fingerprint, err := canonicalControl(line.Params)
	if err != nil {
		return
	}
	nativeID := params.ApprovalID
	if nativeID == "" {
		nativeID = params.UserInputID
	}
	a.stateMu.Lock()
	state := a.sessions[params.SessionID]
	if state == nil || state.retired || state.childID == "" && params.SessionID != a.sessionID {
		a.stateMu.Unlock()
		return
	}
	previous := state.controls[nativeID]
	if previous.key == key && previous.fingerprint == fingerprint {
		a.stateMu.Unlock()
		return
	}
	a.stateMu.Unlock()
	a.enqueueControl(&controlRecord{kind: controlPublish, state: state, sessionID: params.SessionID, nativeID: nativeID, key: key, fingerprint: fingerprint, raw: slices.Clone(line.Raw)})
}

func (a *Agent) settleControl(line *providerkit.ParsedLine, state *sessionState) {
	params, nativeID, ok := validSettlement(line)
	if !ok {
		return
	}
	a.stateMu.Lock()
	pending := state.controls[nativeID]
	a.stateMu.Unlock()
	if queued := a.queuedControlKey(state, nativeID); queued != "" {
		pending.key = queued
	}
	if pending.key != "" {
		a.enqueueControl(&controlRecord{kind: controlSettlement, state: state, sessionID: params.SessionID, nativeID: nativeID, key: pending.key, raw: slices.Clone(line.Raw)})
	}
}

func (a *Agent) queuedControlKey(state *sessionState, nativeID string) string {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	for index := len(a.controlQueue) - 1; index >= 0; index-- {
		record := a.controlQueue[index]
		if record.kind == controlPublish && record.state == state && record.nativeID == nativeID {
			return record.key
		}
	}
	return ""
}

func (a *Agent) enqueueControl(record *controlRecord) {
	a.controlMu.Lock()
	if record.kind == controlPublish {
		for _, queued := range a.controlQueue {
			if queued.kind == controlPublish && queued.state == record.state && queued.key == record.key && queued.fingerprint == record.fingerprint {
				a.controlMu.Unlock()
				return
			}
		}
	}
	a.controlQueue = append(a.controlQueue, record)
	a.controlMu.Unlock()
}

func (a *Agent) drainControls() {
	a.controlMu.Lock()
	if a.controlDraining {
		a.controlMu.Unlock()
		return
	}
	a.controlDraining = true
	a.controlMu.Unlock()
	normalReturn := false
	defer func() {
		if normalReturn {
			return
		}
		value := recover()
		a.controlMu.Lock()
		a.controlDraining = false
		a.controlMu.Unlock()
		panic(value)
	}()
	for {
		a.controlMu.Lock()
		if len(a.controlQueue) == 0 {
			a.controlDraining = false
			a.controlMu.Unlock()
			normalReturn = true
			return
		}
		record := a.controlQueue[0]
		a.controlMu.Unlock()
		if !a.applyControlRecord(record) {
			a.controlMu.Lock()
			a.controlDraining = false
			a.controlMu.Unlock()
			normalReturn = true
			return
		}
		a.controlMu.Lock()
		if len(a.controlQueue) > 0 && a.controlQueue[0] == record {
			a.controlQueue[0] = nil
			a.controlQueue = a.controlQueue[1:]
		}
		a.controlMu.Unlock()
	}
}

func (a *Agent) applyControlRecord(record *controlRecord) bool {
	switch record.kind {
	case controlWarning:
		slog.Warn("read a Muse control identity", "error", record.err)
		return true
	case controlCancel:
		record.cancel(record.key)
		return true
	case controlSettlement:
		if !record.cancelDone {
			a.stateMu.Lock()
			current := a.currentSessionState(record.sessionID, record.state) && record.state.controls[record.nativeID].key == record.key
			if current {
				delete(record.state.controls, record.nativeID)
			}
			a.stateMu.Unlock()
			if !current {
				return true
			}
			record.cancel(record.key)
		}
		if _, err := record.state.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: record.raw}); err != nil {
			slog.Warn("persist a Muse control settlement", "error", err)
			return false
		}
		return true
	case controlPublish:
		if record.publishDone {
			record.cancel(record.cancelKey)
			return true
		}
		a.stateMu.Lock()
		current := a.currentSessionState(record.sessionID, record.state)
		a.stateMu.Unlock()
		if !current {
			return true
		}
		request := agent.ControlRequest{AgentSessionID: record.sessionID, RequestID: record.key, Payload: slices.Clone(record.raw)}
		if err := record.state.sink.PublishControlRequest(request); err != nil {
			slog.Warn("publish a Muse control request", "error", err)
			return false
		}
		record.publishDone = true
		a.stateMu.Lock()
		if !a.currentSessionState(record.sessionID, record.state) {
			a.stateMu.Unlock()
			record.cancelKey = record.key
			record.cancel(record.cancelKey)
			return true
		}
		previous := record.state.controls[record.nativeID]
		if record.state.controls == nil {
			record.state.controls = make(map[string]controlState)
		}
		record.state.controls[record.nativeID] = controlState{key: record.key, fingerprint: record.fingerprint}
		a.stateMu.Unlock()
		if previous.key != "" && previous.key != record.key {
			record.cancelKey = previous.key
			record.cancel(record.cancelKey)
		}
		return true
	default:
		return true
	}
}

func (record *controlRecord) cancel(key string) {
	if record.cancelDone || key == "" {
		return
	}
	// Record cancellation admission before the observer can reenter or panic.
	record.cancelDone = true
	record.state.sink.CancelControlRequest(key)
}

func validSettlement(line *providerkit.ParsedLine) (controlParams, string, bool) {
	fields, ok := controlJSONObject(line.Params)
	if !ok {
		return controlParams{}, "", false
	}
	sessionID, sessionOK := controlJSONString(fields["sessionId"])
	if !sessionOK || sessionID == "" {
		return controlParams{}, "", false
	}
	if line.Method == contracts.MuseMethodUserInputSettled {
		inputID, inputOK := controlJSONString(fields["userInputId"])
		outcome, outcomeOK := controlJSONString(fields["outcome"])
		if !inputOK || inputID == "" || !outcomeOK || strings.TrimSpace(outcome) == "" {
			return controlParams{}, "", false
		}
		return controlParams{SessionID: sessionID, UserInputID: inputID}, inputID, true
	}
	if line.Method != contracts.MuseMethodApprovalResolved {
		return controlParams{}, "", false
	}
	approvalID, approvalOK := controlJSONString(fields["approvalId"])
	decision, decisionOK := controlJSONString(fields["decision"])
	stages, stagesOK := controlJSONArray(fields["stageEvidence"])
	if !approvalOK || approvalID == "" || !decisionOK || strings.TrimSpace(decision) == "" || !stagesOK {
		return controlParams{}, "", false
	}
	seen := make(map[int64]bool, len(stages))
	for _, raw := range stages {
		stage, stageOK := controlJSONObject(raw)
		if !stageOK || !validApprovalStage(stage, approvalID, seen) {
			return controlParams{}, "", false
		}
	}
	return controlParams{SessionID: sessionID, ApprovalID: approvalID}, approvalID, true
}

func validApprovalStage(stage map[string]json.RawMessage, approvalID string, seen map[int64]bool) bool {
	requirement, ok := controlJSONObject(stage["requirementId"])
	if !ok {
		return false
	}
	referenceApproval, approvalOK := controlJSONString(requirement["approvalId"])
	sourceIndex, indexOK := controlJSONInteger(requirement["sourceIndex"])
	if !approvalOK || strings.TrimSpace(referenceApproval) == "" || referenceApproval != approvalID || !indexOK || sourceIndex < 0 || seen[sourceIndex] {
		return false
	}
	seen[sourceIndex] = true
	_, positionOK := controlJSONInteger(stage["position"])
	_, totalOK := controlJSONInteger(stage["totalStages"])
	if !positionOK || !totalOK {
		return false
	}
	argv, argvOK := controlJSONArray(stage["argv"])
	if !argvOK {
		return false
	}
	for _, raw := range argv {
		if _, ok := controlJSONString(raw); !ok {
			return false
		}
	}
	resolution, resolutionOK := controlJSONObject(stage["resolution"])
	_, kindOK := controlJSONString(resolution["kind"])
	return resolutionOK && kindOK
}

func (museProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	result := agent.ControlResponseResolution{Content: ctx.ResponseContent}
	if len(ctx.RequestPayload) == 0 {
		return result
	}
	var request struct {
		Method string        `json:"method"`
		Params controlParams `json:"params"`
	}
	if json.Unmarshal(ctx.RequestPayload, &request) != nil || !validControlRequestMethod(request.Method, request.Params) {
		result.Refuse(agent.RefusalUnreadableRequest)
		return result
	}
	key, err := controlID(request.Params)
	if err != nil {
		result.Refuse(agent.RefusalUnreadableRequest)
		return result
	}
	if ctx.RequestID != key {
		result.Refuse(agent.RefusalOtherRequest)
		return result
	}
	method, params, err := nativeControlReply(request.Params, ctx.ResponseContent, key)
	if err != nil {
		result.Refuse(err.Error())
		return result
	}
	encoded, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": key, "method": method, "params": params})
	if err != nil {
		result.Refuse(agent.RefusalUnencodableReply)
		return result
	}
	result.Content = encoded
	return result
}

func validControlRequestMethod(method string, params controlParams) bool {
	if params.ApprovalID != "" && params.UserInputID != "" {
		return false
	}
	if params.ApprovalID != "" {
		switch method {
		case contracts.MuseMethodApprovalRequest, contracts.MuseMethodApprovalRequested, contracts.MuseMethodApprovalUpdated:
			return true
		default:
			return false
		}
	}
	return params.UserInputID != "" && (method == contracts.MuseMethodUserInputRequest || method == contracts.MuseMethodUserInputRequested)
}

func nativeControlReply(request controlParams, raw []byte, key string) (string, map[string]any, error) {
	params := map[string]any{"sessionId": request.SessionID}
	if request.ApprovalID != "" {
		choice, feedback, err := approvalReplyChoice(request.Choices, raw, key)
		if err != nil {
			return "", nil, err
		}
		params["approvalId"] = request.ApprovalID
		params["choiceId"] = choice
		params["requirementId"] = request.Requirement
		if feedback != "" {
			params["feedback"] = feedback
		}
		return methodApprovalDecide, params, nil
	}
	envelope, ok := controlJSONObject(raw)
	if !ok {
		return "", nil, errors.New(agent.RefusalUnreadableAnswer)
	}
	fields, err := nativeReplyFields(envelope, key)
	if err != nil {
		return "", nil, err
	}
	params["userInputId"] = request.UserInputID
	if value, present := fields["cancelled"]; present {
		var cancelled *bool
		if json.Unmarshal(value, &cancelled) != nil || cancelled == nil {
			return "", nil, errors.New(agent.RefusalUnreadableAnswer)
		}
		if *cancelled {
			reason := ""
			if value, present := fields["reason"]; present {
				var reasonOK bool
				reason, reasonOK = controlJSONString(value)
				if !reasonOK {
					return "", nil, errors.New(agent.RefusalUnreadableAnswer)
				}
			}
			params["reason"] = reason
			return methodUserInputCancel, params, nil
		}
	}
	questions, ok := readNativeQuestions(request.Questions)
	answers, readable := controlJSONArray(fields["answers"])
	if !ok || !readable || !validQuestionAnswers(questions, answers) {
		return "", nil, errors.New(agent.RefusalAnswersDoNotFit)
	}
	params["answers"] = answers
	return methodUserInputAnswer, params, nil
}

type nativeApprovalChoice struct {
	ID       string
	Decision string
	Scope    string
	Feedback bool
}

func readApprovalChoices(raw json.RawMessage) ([]nativeApprovalChoice, bool) {
	values, ok := controlJSONArray(raw)
	if !ok || len(values) == 0 {
		return nil, false
	}
	choices := make([]nativeApprovalChoice, 0, len(values))
	ids := make(map[string]bool, len(values))
	for _, value := range values {
		fields, ok := controlJSONObject(value)
		if !ok {
			return nil, false
		}
		id, idOK := controlJSONString(fields["choiceId"])
		label, labelOK := controlJSONString(fields["label"])
		decision, decisionOK := controlJSONString(fields["decision"])
		scope, scopeOK := controlJSONString(fields["scope"])
		if !idOK || strings.TrimSpace(id) == "" || ids[id] || !labelOK || strings.TrimSpace(label) == "" || !decisionOK || !scopeOK {
			return nil, false
		}
		if strings.TrimSpace(decision) == "" || strings.TrimSpace(scope) == "" {
			return nil, false
		}
		feedback := false
		if raw, present := fields["acceptsFeedback"]; present {
			var value *bool
			if json.Unmarshal(raw, &value) != nil || value == nil {
				return nil, false
			}
			feedback = *value
		}
		ids[id] = true
		choices = append(choices, nativeApprovalChoice{ID: id, Decision: decision, Scope: scope, Feedback: feedback})
	}
	return choices, true
}

func approvalReplyChoice(available json.RawMessage, raw []byte, key string) (string, string, error) {
	choices, ok := readApprovalChoices(available)
	if !ok {
		return "", "", errors.New("the native Muse approval choices are invalid")
	}
	envelope, ok := controlJSONObject(raw)
	if !ok {
		return "", "", errors.New(agent.RefusalUnreadableAnswer)
	}
	choice, feedback := "", ""
	if neutral, present := envelope["response"]; present {
		if _, present := envelope["id"]; present {
			return "", "", errors.New(agent.RefusalUnreadableAnswer)
		}
		if _, present := envelope["result"]; present {
			return "", "", errors.New(agent.RefusalUnreadableAnswer)
		}
		outer, ok := controlJSONObject(neutral)
		id, idOK := controlJSONString(outer["request_id"])
		answer, answerOK := controlJSONObject(outer["response"])
		behavior, behaviorOK := controlJSONString(answer["behavior"])
		if !ok || !idOK || !answerOK || !behaviorOK {
			return "", "", errors.New(agent.RefusalUnreadableAnswer)
		}
		if strings.TrimSpace(id) != key {
			return "", "", errors.New(agent.RefusalOtherRequest)
		}
		behavior = strings.TrimSpace(behavior)
		if behavior != agent.ControlBehaviorAllow && behavior != agent.ControlBehaviorDeny {
			return "", "", errors.New(agent.RefusalNoDecision)
		}
		if value, present := answer["message"]; present {
			var ok bool
			feedback, ok = controlJSONString(value)
			if !ok {
				return "", "", errors.New(agent.RefusalUnreadableAnswer)
			}
		}
		if value, present := answer["choice"]; present {
			var ok bool
			choice, ok = controlJSONString(value)
			if !ok || strings.TrimSpace(choice) == "" {
				return "", "", errors.New(agent.RefusalNoDecision)
			}
		}
		if choice == "" {
			decision := contracts.MuseApprovalDecisionApproved
			if behavior == agent.ControlBehaviorDeny {
				decision = ""
			}
			for _, offered := range choices {
				matches := offered.Decision == decision
				if behavior == agent.ControlBehaviorDeny {
					matches = offered.Decision == contracts.MuseApprovalDecisionDenied || offered.Decision == contracts.MuseApprovalDecisionAbort
				}
				if matches && offered.Scope == contracts.MuseChoiceScopeOnce && (feedback == "" || offered.Feedback) {
					choice = offered.ID
					break
				}
			}
		}
	} else {
		answer, err := nativeReplyFields(envelope, key)
		if err != nil {
			return "", "", err
		}
		var ok bool
		choice, ok = controlJSONString(answer["choiceId"])
		if !ok || strings.TrimSpace(choice) == "" {
			return "", "", errors.New(agent.RefusalNoDecision)
		}
		if value, present := answer["feedback"]; present {
			feedback, ok = controlJSONString(value)
			if !ok {
				return "", "", errors.New(agent.RefusalUnreadableAnswer)
			}
		}
	}
	for _, offered := range choices {
		if offered.ID != choice {
			continue
		}
		if feedback != "" && !offered.Feedback {
			return "", "", errors.New("the Muse choice does not accept feedback")
		}
		return choice, feedback, nil
	}
	return "", "", errors.New(agent.RefusalNoDecision)
}

func nativeReplyFields(envelope map[string]json.RawMessage, key string) (map[string]json.RawMessage, error) {
	if _, present := envelope["response"]; present {
		return nil, errors.New(agent.RefusalUnreadableAnswer)
	}
	id, ok := controlJSONString(envelope["id"])
	if !ok {
		return nil, errors.New(agent.RefusalUnreadableAnswer)
	}
	if id != key {
		return nil, errors.New(agent.RefusalOtherRequest)
	}
	result, ok := controlJSONObject(envelope["result"])
	if !ok {
		return nil, errors.New(agent.RefusalUnreadableAnswer)
	}
	return result, nil
}

func controlJSONObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, false
	}
	return fields, true
}

func controlJSONArray(raw json.RawMessage) ([]json.RawMessage, bool) {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, false
	}
	return values, true
}

func controlJSONString(raw json.RawMessage) (string, bool) {
	var value *string
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return "", false
	}
	return *value, true
}

func controlJSONInteger(raw json.RawMessage) (int64, bool) {
	var value *int64
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return 0, false
	}
	return *value, true
}

type nativeQuestionOption struct {
	Label string
}

type nativeQuestion struct {
	ID        string                 `json:"id"`
	Options   []nativeQuestionOption `json:"options"`
	Selection struct {
		Mode string `json:"mode"`
		Min  *int   `json:"minSelections"`
		Max  *int   `json:"maxSelections"`
	} `json:"selection"`
}

func readNativeQuestions(raw json.RawMessage) ([]nativeQuestion, bool) {
	values, ok := controlJSONArray(raw)
	if !ok || len(values) == 0 {
		return nil, false
	}
	questions := make([]nativeQuestion, 0, len(values))
	ids := make(map[string]bool, len(values))
	for _, value := range values {
		fields, ok := controlJSONObject(value)
		if !ok {
			return nil, false
		}
		id, idOK := controlJSONString(fields["id"])
		text, textOK := controlJSONString(fields["question"])
		_, headerOK := controlJSONString(fields["header"])
		selection, selectionOK := controlJSONObject(fields["selection"])
		mode, modeOK := controlJSONString(selection["mode"])
		options, optionsOK := controlJSONArray(fields["options"])
		if !idOK || strings.TrimSpace(id) == "" || ids[id] || !textOK || strings.TrimSpace(text) == "" || !headerOK || !selectionOK || !modeOK || !optionsOK {
			return nil, false
		}
		if mode != contracts.MuseQuestionSelectionModeSingle && mode != contracts.MuseQuestionSelectionModeMultiple {
			return nil, false
		}
		question := nativeQuestion{ID: id}
		question.Selection.Mode = mode
		labels := make(map[string]bool, len(options))
		for _, raw := range options {
			option, ok := controlJSONObject(raw)
			label, labelOK := controlJSONString(option["label"])
			if !ok || !labelOK || strings.TrimSpace(label) == "" || labels[label] {
				return nil, false
			}
			labels[label] = true
			question.Options = append(question.Options, nativeQuestionOption{Label: label})
		}
		var minOK, maxOK bool
		question.Selection.Min, minOK = readSelectionCount(selection, "minSelections")
		question.Selection.Max, maxOK = readSelectionCount(selection, "maxSelections")
		if _, _, valid := questionSelectionLimits(question); !minOK || !maxOK || !valid {
			return nil, false
		}
		ids[id] = true
		questions = append(questions, question)
	}
	return questions, true
}

func readSelectionCount(fields map[string]json.RawMessage, key string) (*int, bool) {
	raw, present := fields[key]
	if !present {
		return nil, true
	}
	var count *int
	if json.Unmarshal(raw, &count) != nil || count == nil {
		return nil, false
	}
	return count, true
}

func questionSelectionLimits(question nativeQuestion) (int, int, bool) {
	minimum, maximum := 0, len(question.Options)
	if question.Selection.Min != nil {
		minimum = *question.Selection.Min
	}
	if question.Selection.Max != nil {
		maximum = *question.Selection.Max
	}
	return minimum, maximum, minimum >= 0 && maximum >= minimum && maximum <= len(question.Options)
}

// validQuestionAnswers requires each exact question and one native answer form.
func validQuestionAnswers(nativeQuestions []nativeQuestion, answers []json.RawMessage) bool {
	if len(nativeQuestions) == 0 || len(nativeQuestions) != len(answers) {
		return false
	}
	questions := make(map[string]nativeQuestion, len(nativeQuestions))
	for _, question := range nativeQuestions {
		if question.ID == "" || question.Selection.Mode != contracts.MuseQuestionSelectionModeSingle && question.Selection.Mode != contracts.MuseQuestionSelectionModeMultiple {
			return false
		}
		if _, exists := questions[question.ID]; exists {
			return false
		}
		questions[question.ID] = question
	}
	for _, raw := range answers {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return false
		}
		var id string
		if json.Unmarshal(fields["questionId"], &id) != nil {
			return false
		}
		question, exists := questions[id]
		if !exists {
			return false
		}
		delete(questions, id)
		forms := 0
		for _, key := range []string{"selectedLabel", "selectedLabels", "freeText"} {
			if _, exists := fields[key]; exists {
				forms++
			}
		}
		if forms != 1 {
			return false
		}
		if note, exists := fields["note"]; exists {
			text, ok := controlJSONString(note)
			if !ok || utf8.RuneCountInString(text) > 500 {
				return false
			}
		}
		if text, exists := fields["freeText"]; exists {
			if !validQuestionText(text) {
				return false
			}
			continue
		}
		labels := make(map[string]bool, len(question.Options))
		for _, option := range question.Options {
			if option.Label == "" || labels[option.Label] {
				return false
			}
			labels[option.Label] = true
		}
		if question.Selection.Mode == contracts.MuseQuestionSelectionModeSingle {
			var label string
			if json.Unmarshal(fields["selectedLabel"], &label) != nil || !labels[label] {
				return false
			}
			continue
		}
		var selected []string
		if json.Unmarshal(fields["selectedLabels"], &selected) != nil || selected == nil {
			return false
		}
		minimum, maximum, valid := questionSelectionLimits(question)
		if !valid || len(selected) < minimum || len(selected) > maximum {
			return false
		}
		for _, label := range selected {
			if !labels[label] {
				return false
			}
			delete(labels, label)
		}
	}
	return len(questions) == 0
}

func validQuestionText(raw json.RawMessage) bool {
	text, ok := controlJSONString(raw)
	return ok && strings.TrimSpace(text) != "" && utf8.RuneCountInString(text) <= 500
}
