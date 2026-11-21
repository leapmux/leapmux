package opencode

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type openCodeDurablePosition struct {
	AggregateID string `json:"aggregateID"`
	Seq         int64  `json:"seq"`
	Version     int64  `json:"version"`
}

func (p *openCodeDurablePosition) UnmarshalJSON(data []byte) error {
	var wire struct {
		AggregateID string `json:"aggregateID"`
		Seq         *int64 `json:"seq"`
		Version     int64  `json:"version"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Seq == nil {
		return errors.New("the OpenCode durable position requires a sequence")
	}
	*p = openCodeDurablePosition{AggregateID: wire.AggregateID, Seq: *wire.Seq, Version: wire.Version}
	return nil
}

// openCodeEvent retains the exact native envelope while validating its header.
type openCodeEvent struct {
	ID        string                   `json:"id"`
	Type      string                   `json:"type"`
	Created   *float64                 `json:"created"`
	Location  *openCodeLocation        `json:"location,omitempty"`
	Data      json.RawMessage          `json:"data"`
	Durable   *openCodeDurablePosition `json:"durable,omitempty"`
	Raw       json.RawMessage          `json:"-"`
	sessionID string
}

func parseOpenCodeEvent(raw []byte) (openCodeEvent, error) {
	var event openCodeEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return event, fmt.Errorf("decode the OpenCode event envelope: %w", err)
	}
	if event.ID == "" || event.Type == "" || event.Created == nil {
		return event, errors.New("the OpenCode event requires an ID, type, and creation time")
	}
	// Unix nanoseconds must remain representable when the native millisecond value is converted.
	if math.IsNaN(*event.Created) || math.IsInf(*event.Created, 0) || math.Abs(*event.Created) >= float64(math.MaxInt64)/1e6 {
		return event, errors.New("the OpenCode event creation time is outside the supported range")
	}
	var scope struct {
		SessionID string `json:"sessionID"`
		Form      *struct {
			SessionID string `json:"sessionID"`
		} `json:"form"`
	}
	if !providerkit.RawPresent(event.Data) {
		return event, errors.New("the OpenCode event has no data object")
	}
	if err := json.Unmarshal(event.Data, &scope); err != nil {
		return event, fmt.Errorf("decode the OpenCode event scope: %w", err)
	}
	event.sessionID = scope.SessionID
	if event.Type == contracts.OpenCodeNativeEventFormCreated {
		if scope.Form == nil {
			return event, errors.New("the OpenCode form event has no form")
		}
		event.sessionID = scope.Form.SessionID
	}
	if strings.HasPrefix(event.Type, "session.") || strings.HasPrefix(event.Type, "permission.") || strings.HasPrefix(event.Type, "form.") {
		if event.sessionID == "" {
			return event, errors.New("the OpenCode event has no session owner")
		}
	}
	if event.Durable != nil {
		position := event.Durable
		if position.AggregateID == "" || position.Seq < 0 || position.Version <= 0 {
			return event, errors.New("the OpenCode event has an invalid durable position")
		}
		if strings.HasPrefix(event.Type, "session.") && position.AggregateID != event.sessionID {
			return event, errors.New("the OpenCode durable position belongs to another session")
		}
	}
	event.Raw = append(json.RawMessage(nil), raw...)
	return event, nil
}

func (e openCodeEvent) createdAt() time.Time {
	return time.Unix(0, int64(*e.Created*1e6))
}

func (e openCodeEvent) decodeData(out any) error {
	if err := json.Unmarshal(e.Data, out); err != nil {
		return fmt.Errorf("decode OpenCode %s data: %w", e.Type, err)
	}
	return nil
}

func (s *openCodeTextStreams) observe(event openCodeEvent) (bool, error) {
	switch event.Type {
	case contracts.OpenCodeNativeEventStepEnded, "session.retry.scheduled":
		var data struct {
			MessageID string `json:"assistantMessageID"`
		}
		if err := event.decodeData(&data); err != nil {
			return true, err
		}
		if data.MessageID == "" || event.sessionID != s.sessionID {
			return true, errors.New("the OpenCode text settlement has no matching message owner")
		}
		if event.Type == "session.retry.scheduled" {
			s.discardMessage(data.MessageID)
		} else if err := s.flushEnded(data.MessageID); err != nil {
			return true, err
		}
		// Usage and retry notifications still pass through the session dispatcher.
		return false, nil
	case "session.inbox.delivered":
		var data struct {
			ID string `json:"inboxID"`
		}
		if err := event.decodeData(&data); err != nil {
			return true, err
		}
		if data.ID == "" || event.sessionID != s.sessionID {
			return true, errors.New("the OpenCode input delivery has no matching input owner")
		}
		if err := s.flushEnded(""); err != nil {
			return true, err
		}
		return false, nil
	case contracts.OpenCodeNativeEventToolInputStarted:
		if event.sessionID != s.sessionID {
			return true, errors.New("the OpenCode content boundary belongs to another session")
		}
		var data struct {
			MessageID string `json:"assistantMessageID"`
			ID        string `json:"id"`
			Name      string `json:"name"`
		}
		if err := event.decodeData(&data); err != nil {
			return true, err
		}
		if data.MessageID == "" || data.ID == "" || data.Name == "" {
			return true, errors.New("the OpenCode tool boundary has no message, call ID, or name")
		}
		if err := s.flushEnded(""); err != nil {
			return true, err
		}
	}
	kind := agent.AssembledMessageKindText
	prefix := "session.text."
	if strings.HasPrefix(event.Type, "session.reasoning.") {
		kind = agent.AssembledMessageKindReasoning
		prefix = "session.reasoning."
	}
	if !strings.HasPrefix(event.Type, prefix) {
		return false, nil
	}
	if event.sessionID != s.sessionID {
		return true, errors.New("OpenCode text belongs to another session")
	}
	var data struct {
		MessageID string  `json:"assistantMessageID"`
		Ordinal   *int    `json:"ordinal"`
		Delta     *string `json:"delta"`
		Text      *string `json:"text"`
	}
	if err := event.decodeData(&data); err != nil {
		return true, err
	}
	if data.Ordinal == nil {
		return true, errors.New("OpenCode text has no ordinal")
	}
	operation := strings.TrimPrefix(event.Type, prefix)
	if operation != "started" && operation != "delta" && operation != "ended" {
		return true, fmt.Errorf("unknown OpenCode text event %q", event.Type)
	}
	if operation == "delta" && data.Delta == nil || operation == "ended" && data.Text == nil {
		return true, errors.New("OpenCode text event has no text value")
	}
	if operation == "started" && data.MessageID != "" && *data.Ordinal >= 0 {
		if err := s.flushEnded(""); err != nil {
			return true, err
		}
	}
	segment, err := s.segment(data.MessageID, *data.Ordinal, kind)
	if err != nil {
		return true, err
	}
	if segment.finished {
		return true, segment.buffer.OutputQueue().Flush()
	}
	switch operation {
	case "delta":
		s.append(segment, *data.Delta)
	case "ended":
		s.replace(segment, *data.Text)
		// Cancellation and failed attempts flush native fragments before their step settles.
		segment.ended = true
	}
	return true, nil
}

// observeInbox resolves only IDs this runtime submitted. A delivered steer is
// reported after preceding output because the dispatcher preserves native order.
func (s *openCodeSessionRuntime) observeInbox(event openCodeEvent) (bool, error) {
	switch event.Type {
	case "session.inbox.enqueued", "session.inbox.delivered", "session.inbox.cancelled":
	default:
		return false, nil
	}
	var data struct {
		InboxID string `json:"inboxID"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return true, err
	}
	if data.InboxID == "" {
		return true, errors.New("the OpenCode inbox event has no input ID")
	}
	s.mu.Lock()
	if event.sessionID != s.id {
		s.mu.Unlock()
		return true, errors.New("the OpenCode inbox event belongs to another session")
	}
	if admission := s.admissions[data.InboxID]; admission != nil {
		admission.admitted = true
	}
	receipt := s.replyInputs[data.InboxID]
	s.mu.Unlock()
	switch event.Type {
	case "session.inbox.delivered":
		s.steers.EchoID(data.InboxID)
	case "session.inbox.cancelled":
		s.steers.DropID(data.InboxID)
	}
	s.steers.Settle(s.sink)
	if event.Type == "session.inbox.cancelled" && receipt.InputID != "" {
		return true, s.retireInputReply(receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED)
	}
	return true, nil
}

func (s *openCodeToolStreams) observe(event openCodeEvent) (bool, error) {
	if !strings.HasPrefix(event.Type, "session.tool.") {
		return false, nil
	}
	if event.sessionID != s.sessionID {
		return true, errors.New("the OpenCode tool event belongs to another session")
	}
	var data struct {
		MessageID string             `json:"assistantMessageID"`
		ID        *string            `json:"id"`
		Name      *string            `json:"name"`
		Delta     *string            `json:"delta"`
		Text      *string            `json:"text"`
		Executed  *bool              `json:"executed"`
		Content   *[]json.RawMessage `json:"content"`
		Error     json.RawMessage    `json:"error"`
		Input     json.RawMessage    `json:"input"`
		Metadata  json.RawMessage    `json:"metadata"`
	}
	if err := event.decodeData(&data); err != nil {
		return true, err
	}
	switch event.Type {
	case contracts.OpenCodeNativeEventToolInputStarted:
		if data.Name == nil {
			return true, errors.New("the OpenCode tool start has no name")
		}
	case "session.tool.input.delta":
		if data.Delta == nil {
			return true, errors.New("the OpenCode tool input delta has no text")
		}
	case "session.tool.input.ended":
		if data.Text == nil {
			return true, errors.New("the OpenCode tool input ending has no text")
		}
	case contracts.OpenCodeNativeEventToolCalled:
		if err := validateOpenCodeToolObject(data.Input, "input"); err != nil {
			return true, err
		}
		if data.Executed == nil {
			return true, errors.New("the OpenCode tool call has no execution flag")
		}
	case contracts.OpenCodeNativeEventToolProgress:
		if err := validateOpenCodeToolObject(data.Metadata, "progress metadata"); err != nil {
			return true, err
		}
	case contracts.OpenCodeNativeEventToolSuccess, contracts.OpenCodeNativeEventToolFailed:
		if data.Executed == nil {
			return true, errors.New("the OpenCode tool result has no execution flag")
		}
		if event.Type == contracts.OpenCodeNativeEventToolSuccess && (data.Content == nil || len(*data.Content) == 0) {
			return true, errors.New("the OpenCode tool success requires nonempty content")
		}
		if data.Content != nil && len(*data.Content) == 0 {
			return true, errors.New("the OpenCode tool result content cannot be empty")
		}
		if event.Type == contracts.OpenCodeNativeEventToolFailed {
			if err := validateOpenCodeToolObject(data.Error, "error"); err != nil {
				return true, err
			}
		}
	default:
		return true, fmt.Errorf("unsupported OpenCode tool event %q", event.Type)
	}
	tool, err := s.tool(data.MessageID, data.ID)
	if err != nil {
		return true, err
	}
	if tool.closed {
		return true, s.sink.OutputQueue().Flush()
	}
	if event.Type == contracts.OpenCodeNativeEventToolInputStarted && tool.nameKnown && tool.name != *data.Name {
		return true, errors.New("OpenCode changed the name of an existing tool call")
	}
	tool.latest = event.Raw
	switch event.Type {
	case contracts.OpenCodeNativeEventToolInputStarted:
		tool.name, tool.nameKnown, tool.start = *data.Name, true, event.Raw
	case "session.tool.input.delta":
		s.sink.ReportProgress(agent.ModelTextProgress(tool.spanID+":input", *data.Delta))
	case "session.tool.input.ended":
	case contracts.OpenCodeNativeEventToolCalled:
		tool.input, tool.called = data.Input, event.Raw
		if err := s.open(tool, event.Raw); err != nil {
			return true, err
		}
	case contracts.OpenCodeNativeEventToolProgress:
		tool.progress = event.Raw
		if err := s.enrichOpening(tool); err != nil {
			return true, err
		}
	case contracts.OpenCodeNativeEventToolSuccess, contracts.OpenCodeNativeEventToolFailed:
		completion := agent.MessageCompletionComplete
		if event.Type == contracts.OpenCodeNativeEventToolFailed {
			completion = agent.MessageCompletionError
		}
		if s.metadata != nil && len(data.Metadata) > 0 && string(data.Metadata) != "null" {
			if err := s.metadata(tool, data.Metadata); err != nil {
				return true, err
			}
		}
		return true, s.closeTool(tool, event.Raw, completion)
	default:
		return true, fmt.Errorf("unsupported OpenCode tool event %q", event.Type)
	}
	if s.metadata != nil && len(data.Metadata) > 0 && string(data.Metadata) != "null" {
		return true, s.metadata(tool, data.Metadata)
	}
	return true, nil
}

func validateOpenCodeToolObject(raw json.RawMessage, field string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return fmt.Errorf("the OpenCode tool %s requires an object: %w", field, err)
	}
	if object == nil {
		return fmt.Errorf("the OpenCode tool %s requires an object", field)
	}
	return nil
}
