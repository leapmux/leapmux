package deepseekharness

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

type nativeChildDescriptor struct {
	ID     string `json:"childId"`
	Mode   string `json:"mode"`
	Label  string `json:"label"`
	Parent string `json:"-"`
}
type nativeChild struct {
	descriptor nativeChildDescriptor
	agentID    string
	spawnID    string
	active     bool
}

func (a *Agent) beginChild(stream *sessionStream, raw []byte) error {
	var descriptor nativeChildDescriptor
	if err := json.Unmarshal(raw, &descriptor); err != nil || descriptor.ID == "" {
		return fmt.Errorf("DeepSeek Harness child catalog has no identity")
	}
	return a.beginOwnedChild(stream, descriptor)
}

func (a *Agent) beginOwnedChild(stream *sessionStream, descriptor nativeChildDescriptor) error {
	if descriptor.Mode != "continuable" && descriptor.Mode != "one-shot" {
		return fmt.Errorf("DeepSeek Harness child catalog has an invalid mode")
	}
	descriptor.Parent = stream.sessionID
	a.Mu.Lock()
	old, exists := a.childCatalog[descriptor.ID]
	a.Mu.Unlock()
	if exists {
		if old != descriptor {
			return fmt.Errorf("DeepSeek Harness child catalog changes an owned identity")
		}
		return nil
	}
	sink := a.streamSink(stream)
	spawnID := ""
	virtualID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: spawnID, ProviderChildKey: descriptor.ID, Title: descriptor.Label, Options: optionmap.Map{contracts.DeepseekHarnessOptionChildMode: descriptor.Mode}})
	if err != nil {
		return err
	}
	childSink := sink.ChildSink(virtualID)
	childSink.UpdateSessionID(descriptor.ID)
	child := &nativeChild{descriptor: descriptor, agentID: virtualID, spawnID: spawnID}
	if err := sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: descriptor.ID, Kind: bgtask.KindSubagent, ChildAgentID: virtualID, Title: descriptor.Label, Status: bgtask.StatusRunning}); err != nil {
		return err
	}
	if err := a.followSession(sessionAddress{Kind: "subagent", ParentSessionID: descriptor.Parent, ChildSessionID: descriptor.ID, Mode: descriptor.Mode}, virtualID); err != nil {
		return err
	}
	a.Mu.Lock()
	a.childCatalog[descriptor.ID] = descriptor
	a.children[descriptor.ID] = child
	a.Mu.Unlock()
	return nil
}

// A continuable result identifies the exact child created by this exact native call.
func (a *Agent) bindChildResult(stream *sessionStream, callID string, raw []byte) error {
	sink := a.streamSink(stream)
	name := sink.GetSpanType(callID)
	if name != contracts.DeepseekHarnessToolSubagent {
		return nil
	}
	var result struct {
		Message struct {
			CallID  string `json:"toolCallId"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if result.Message.IsError {
		return nil
	}
	if result.Message.CallID != callID {
		return fmt.Errorf("DeepSeek Harness child result changes its call identity")
	}
	if len(result.Message.Content) != 1 || result.Message.Content[0].Type != contracts.DeepseekHarnessContentTypeText {
		return nil
	}
	const prefix = "started subagent "
	text := result.Message.Content[0].Text
	if !strings.HasPrefix(text, prefix) {
		return nil
	}
	childID := strings.TrimPrefix(text, prefix)
	a.Mu.Lock()
	descriptor, known := a.childCatalog[childID]
	child := a.children[childID]
	a.Mu.Unlock()
	if !known || descriptor.Parent != stream.sessionID || descriptor.Mode != "continuable" || child == nil || child.descriptor != descriptor {
		return fmt.Errorf("DeepSeek Harness child result has no matching native catalog entry")
	}
	if child.spawnID != "" && child.spawnID != callID {
		return fmt.Errorf("DeepSeek Harness child result changes its first spawn span")
	}
	virtualID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: callID, ProviderChildKey: childID, Title: descriptor.Label, Options: optionmap.Map{contracts.DeepseekHarnessOptionChildMode: descriptor.Mode}})
	if err != nil {
		return err
	}
	if virtualID != child.agentID {
		return fmt.Errorf("DeepSeek Harness child result changes its stored child identity")
	}
	a.Mu.Lock()
	child.spawnID = callID
	a.Mu.Unlock()
	return nil
}

func (a *Agent) childTurnState(id string, active bool, completion agent.MessageCompletion) error {
	a.Mu.Lock()
	child := a.children[id]
	if child != nil {
		child.active = active
	}
	a.Mu.Unlock()
	if child == nil {
		return nil
	}
	if active {
		if err := a.sink.ReviveBackgroundTask(id); err != nil {
			return err
		}
		return a.sink.UpdateBackgroundTaskStatus(id, bgtask.StatusRunning, "")
	}
	status := bgtask.StatusCompleted
	switch completion {
	case agent.MessageCompletionComplete:
	case agent.MessageCompletionError:
		status = bgtask.StatusFailed
	case agent.MessageCompletionInterrupted:
		status = bgtask.StatusInterrupted
	}
	return a.sink.CloseBackgroundTask(id, status)
}

func (a *Agent) SendChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendChild(childKey, content, attachments, "queue")
}
func (a *Agent) SteerChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendChild(childKey, content, attachments, "steer")
}

func (a *Agent) sendChild(childKey, content string, attachments []*leapmuxv1.Attachment, delivery string) error {
	a.Mu.Lock()
	if a.streamFailure != nil || a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("DeepSeek Harness cannot send input after its native process stops")
	}
	child := a.children[childKey]
	active := child != nil && child.active
	a.Mu.Unlock()
	if child == nil || child.descriptor.Mode != "continuable" {
		return agent.ErrChildOperationUnsupported
	}
	if delivery == "queue" && active {
		return agent.ErrAgentBusy
	}
	if delivery == "steer" && !active {
		return agent.ErrNoActiveTurn
	}
	parts := []map[string]any{{"type": "text", "text": content}}
	for _, attachment := range agent.ClassifyAttachments(attachments) {
		if attachment.Kind != agent.AttachmentKindImage {
			return fmt.Errorf("DeepSeek Harness child input accepts text and images")
		}
		parts = append(parts, map[string]any{"type": "image", "mediaType": attachment.MIMEType, "data": base64.StdEncoding.EncodeToString(attachment.Data), "name": attachment.Filename})
	}
	var receipt struct {
		ID string `json:"messageId"`
	}
	return a.rpc.request(a.Context(), "subagents/prompt", map[string]any{"parentSessionId": child.descriptor.Parent, "childSessionId": childKey, "mode": "continuable", "delivery": delivery, "requestId": uuid.NewString(), "content": parts}, &receipt)
}

func (a *Agent) ActiveChildTurnState(childKey string) agent.TurnState {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	child := a.children[childKey]
	if child == nil {
		return agent.TurnState{}
	}
	return agent.TurnState{Active: child.active, Steerable: child.active && child.descriptor.Mode == "continuable"}
}

func (a *Agent) InterruptChild(childKey string, stop agent.StopContext) error {
	a.Mu.Lock()
	child := a.children[childKey]
	a.Mu.Unlock()
	if child == nil || child.descriptor.Mode != "continuable" {
		return agent.ErrChildOperationUnsupported
	}
	return a.rpc.call(a.Context(), "subagents/interruptByParent", map[string]any{"childSessionId": childKey, "parentSessionId": child.descriptor.Parent, "mode": "continuable"}, nil)
}
