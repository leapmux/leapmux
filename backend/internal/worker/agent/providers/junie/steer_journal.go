package junie

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

type junieSteerJournalEvent struct {
	Kind              string   `json:"kind"`
	RequestID         string   `json:"requestId"`
	Prompt            string   `json:"prompt"`
	TaskID            string   `json:"taskId"`
	CommittedInputIDs []string `json:"userMessageIds"`
	Attachments       []struct {
		Kind string `json:"kind"`
	} `json:"customAttachments"`
	Event struct {
		State   string `json:"state"`
		Payload struct {
			Kind      string               `json:"kind"`
			StepID    string               `json:"stepId"`
			Status    string               `json:"status"`
			Result    string               `json:"result"`
			Cancelled *bool                `json:"cancelled"`
			Request   junieJournalQuestion `json:"request"`
			Agent     struct {
				Kind string `json:"kind"`
				ID   string `json:"id"`
			} `json:"agent"`
		} `json:"agentEvent"`
	} `json:"event"`
}

// decodeJunieEventKind reads the top-level event kind before full decoding so
// unrelated lines whose text happens to mention a target kind cannot abort
// scanning due to colliding field shapes.
func decodeJunieEventKind(line []byte) (string, bool) {
	var header struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(line, &header) != nil {
		return "", false
	}
	return header.Kind, true
}

type junieAdmissionParser struct {
	inputs  []junieSteerInput
	matched map[string]agent.NativeInputReceipt
}

func newJunieAdmissionParser(inputs []junieSteerInput) junieAdmissionParser {
	return junieAdmissionParser{
		inputs:  inputs,
		matched: make(map[string]agent.NativeInputReceipt, len(inputs)),
	}
}

func (p *junieAdmissionParser) observe(event *junieSteerJournalEvent) bool {
	realtime := false
	for _, attachment := range event.Attachments {
		if attachment.Kind == "RealtimeFollowUpAttachment" {
			realtime = true
			break
		}
	}
	if !realtime {
		return false
	}
	i := len(p.matched)
	if i >= len(p.inputs) || event.RequestID == "" || p.matched[event.RequestID].InputID != "" || event.Prompt != p.inputs[i].text {
		return false
	}
	p.matched[event.RequestID] = p.inputs[i].receipt
	return true
}

type junieTaskReplyParser struct {
	reply          junieTaskReply
	replies        []junieTaskReply
	seenTasks      map[string]bool
	answeredInputs map[string]bool
}

func newJunieTaskReplyParser() junieTaskReplyParser {
	return junieTaskReplyParser{
		seenTasks:      make(map[string]bool),
		answeredInputs: make(map[string]bool),
	}
}

func (p *junieTaskReplyParser) observe(event *junieSteerJournalEvent) bool {
	switch event.Kind {
	case "TaskStartedEvent":
		if !junieTaskIDPattern.MatchString(event.TaskID) || p.seenTasks[event.TaskID] {
			return false
		}
		p.seenTasks[event.TaskID] = true
		p.reply = junieTaskReply{taskID: event.TaskID}
	case "UserMessagesCommittedToHistory":
		if p.reply.stepID == "" {
			p.reply.inputIDs = event.CommittedInputIDs
		}
	case "SessionA2uxEvent":
		body := event.Event.Payload
		if body.Kind != "ResultBlockUpdatedEvent" || event.Event.State != "COMPLETED" ||
			event.TaskID != p.reply.taskID || body.Agent.Kind != "MainAgent" || body.Agent.ID != "main" {
			return true
		}
		if p.reply.taskID == "" || body.Cancelled == nil || *body.Cancelled || p.reply.stepID != "" ||
			!validJunieStepID(body.StepID) || len(p.reply.inputIDs) == 0 ||
			strings.TrimSpace(body.Result) == "" {
			return false
		}
		for _, id := range p.reply.inputIDs {
			if id == "" || p.answeredInputs[id] {
				return false
			}
			p.answeredInputs[id] = true
		}
		p.reply.stepID, p.reply.text = body.StepID, body.Result
		p.replies = append(p.replies, p.reply)
	}
	return true
}

// readJunieSteerProofEvents verifies the journal prefix once and extracts both
// steer admissions and completed main-agent replies in a single pass.
func readJunieSteerProofEvents(cursor sessionstore.JournalCursor, data []byte, inputs []junieSteerInput) (map[string]agent.NativeInputReceipt, []junieTaskReply, bool) {
	appended, valid := cursor.AppendedBytes(data)
	if !valid {
		return nil, nil, false
	}
	admissions := newJunieAdmissionParser(inputs)
	replies := newJunieTaskReplyParser()
	for line := range bytes.SplitSeq(appended, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		if !bytes.Contains(line, []byte(`"UserPromptEvent"`)) &&
			!bytes.Contains(line, []byte(`"TaskStartedEvent"`)) &&
			!bytes.Contains(line, []byte(`"UserMessagesCommittedToHistory"`)) &&
			!bytes.Contains(line, []byte(`"ResultBlockUpdatedEvent"`)) {
			continue
		}
		kind, ok := decodeJunieEventKind(line)
		if !ok {
			return nil, nil, false
		}
		switch kind {
		case "UserPromptEvent", "TaskStartedEvent", "UserMessagesCommittedToHistory", "SessionA2uxEvent":
		default:
			continue
		}
		var event junieSteerJournalEvent
		if json.Unmarshal(line, &event) != nil {
			return nil, nil, false
		}
		if kind == "UserPromptEvent" {
			if !admissions.observe(&event) {
				return nil, nil, false
			}
			continue
		}
		if !replies.observe(&event) {
			return nil, nil, false
		}
	}
	if len(admissions.matched) != len(inputs) {
		return nil, nil, false
	}
	return admissions.matched, replies.replies, true
}

// readJunieSteerAdmissions matches the native append order to serialized
// steering RPC acknowledgements. The journal can commit after an acknowledgement.
func readJunieSteerAdmissions(cursor sessionstore.JournalCursor, data []byte, inputs []junieSteerInput) (map[string]agent.NativeInputReceipt, bool) {
	appended, valid := cursor.AppendedBytes(data)
	if !valid {
		return nil, false
	}
	admissions := newJunieAdmissionParser(inputs)
	for line := range bytes.SplitSeq(appended, []byte{'\n'}) {
		if len(line) == 0 || !bytes.Contains(line, []byte(`"UserPromptEvent"`)) {
			continue
		}
		kind, ok := decodeJunieEventKind(line)
		if !ok {
			return nil, false
		}
		if kind != "UserPromptEvent" {
			continue
		}
		var event junieSteerJournalEvent
		if json.Unmarshal(line, &event) != nil {
			return nil, false
		}
		if !admissions.observe(&event) {
			return nil, false
		}
	}
	return admissions.matched, len(admissions.matched) == len(inputs)
}

type junieTaskReply struct {
	taskID, stepID, text string
	inputIDs             []string
}

// readJunieTaskReplies reads completed main-agent answers in native task order.
func readJunieTaskReplies(cursor sessionstore.JournalCursor, data []byte) ([]junieTaskReply, bool) {
	appended, valid := cursor.AppendedBytes(data)
	if !valid {
		return nil, false
	}
	replies := newJunieTaskReplyParser()
	for line := range bytes.SplitSeq(appended, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		if !bytes.Contains(line, []byte(`"TaskStartedEvent"`)) &&
			!bytes.Contains(line, []byte(`"UserMessagesCommittedToHistory"`)) &&
			!bytes.Contains(line, []byte(`"ResultBlockUpdatedEvent"`)) {
			continue
		}
		kind, ok := decodeJunieEventKind(line)
		if !ok {
			return nil, false
		}
		switch kind {
		case "TaskStartedEvent", "UserMessagesCommittedToHistory", "SessionA2uxEvent":
		default:
			continue
		}
		var event junieSteerJournalEvent
		if json.Unmarshal(line, &event) != nil {
			return nil, false
		}
		if !replies.observe(&event) {
			return nil, false
		}
	}
	return replies.replies, true
}
