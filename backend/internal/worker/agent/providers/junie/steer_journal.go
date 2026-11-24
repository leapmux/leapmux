package junie

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type junieJournalCursor struct {
	length int
	digest [sha256.Size]byte
}

func junieCompleteJournal(data []byte) []byte {
	return data[:bytes.LastIndexByte(data, '\n')+1]
}

func junieCursor(data []byte) junieJournalCursor {
	complete := junieCompleteJournal(data)
	return junieJournalCursor{length: len(complete), digest: sha256.Sum256(complete)}
}

func (cursor junieJournalCursor) appended(data []byte) ([]byte, bool) {
	complete := junieCompleteJournal(data)
	if cursor.length > len(complete) || sha256.Sum256(complete[:cursor.length]) != cursor.digest {
		return nil, false
	}
	return complete[cursor.length:], true
}

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

// readJunieSteerAdmissions matches the native append order to serialized
// steering RPC acknowledgements. The journal can commit after an acknowledgement.
func readJunieSteerAdmissions(cursor junieJournalCursor, data []byte, inputs []junieSteerInput) (map[string]agent.NativeInputReceipt, bool) {
	appended, valid := cursor.appended(data)
	if !valid {
		return nil, false
	}
	matched := make(map[string]agent.NativeInputReceipt, len(inputs))
	for line := range bytes.SplitSeq(appended, []byte{'\n'}) {
		if !bytes.Contains(line, []byte(`"UserPromptEvent"`)) {
			continue
		}
		var event junieSteerJournalEvent
		if json.Unmarshal(line, &event) != nil {
			return nil, false
		}
		if event.Kind != "UserPromptEvent" {
			continue
		}
		realtime := false
		for _, attachment := range event.Attachments {
			if attachment.Kind == "RealtimeFollowUpAttachment" {
				realtime = true
				break
			}
		}
		if !realtime {
			return nil, false
		}
		i := len(matched)
		if i >= len(inputs) || event.RequestID == "" || matched[event.RequestID].InputID != "" || event.Prompt != inputs[i].text {
			return nil, false
		}
		matched[event.RequestID] = inputs[i].receipt
	}
	return matched, len(matched) == len(inputs)
}

type junieTaskReply struct {
	taskID, stepID, text string
	inputIDs             []string
}

// readJunieTaskReplies reads completed main-agent answers in native task order.
func readJunieTaskReplies(cursor junieJournalCursor, data []byte) ([]junieTaskReply, bool) {
	appended, valid := cursor.appended(data)
	if !valid {
		return nil, false
	}
	var reply junieTaskReply
	var replies []junieTaskReply
	seenTasks := make(map[string]bool)
	answeredInputs := make(map[string]bool)
	for line := range bytes.SplitSeq(appended, []byte{'\n'}) {
		if !bytes.Contains(line, []byte(`"TaskStartedEvent"`)) &&
			!bytes.Contains(line, []byte(`"UserMessagesCommittedToHistory"`)) &&
			!bytes.Contains(line, []byte(`"ResultBlockUpdatedEvent"`)) {
			continue
		}
		var event junieSteerJournalEvent
		if json.Unmarshal(line, &event) != nil {
			return nil, false
		}
		switch event.Kind {
		case "TaskStartedEvent":
			if !junieTaskIDPattern.MatchString(event.TaskID) || seenTasks[event.TaskID] {
				return nil, false
			}
			seenTasks[event.TaskID] = true
			reply = junieTaskReply{taskID: event.TaskID}
		case "UserMessagesCommittedToHistory":
			if reply.stepID == "" {
				reply.inputIDs = event.CommittedInputIDs
			}
		case "SessionA2uxEvent":
			body := event.Event.Payload
			if body.Kind != "ResultBlockUpdatedEvent" || event.Event.State != "COMPLETED" ||
				event.TaskID != reply.taskID || body.Agent.Kind != "MainAgent" || body.Agent.ID != "main" {
				continue
			}
			step, err := uuid.Parse(body.StepID)
			if reply.taskID == "" || body.Cancelled == nil || *body.Cancelled || reply.stepID != "" ||
				err != nil || step.String() != body.StepID || len(reply.inputIDs) == 0 ||
				strings.TrimSpace(body.Result) == "" {
				return nil, false
			}
			for _, id := range reply.inputIDs {
				if id == "" || answeredInputs[id] {
					return nil, false
				}
				answeredInputs[id] = true
			}
			reply.stepID, reply.text = body.StepID, body.Result
			replies = append(replies, reply)
		}
	}
	return replies, true
}
