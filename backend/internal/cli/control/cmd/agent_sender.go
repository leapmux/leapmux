package cmd

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
)

// messageSender is the agent tab that sent an agent message, as "agent queue
// list" and "agent messages" print it. The Worker stamps it from the control
// token of the sending agent's tab, so it states which agent wrote the text.
type messageSender struct {
	TabID string `json:"tab_id"`
	// Provider is the canonical CLI alias of the sending agent's provider.
	Provider string `json:"provider,omitempty"`
}

// TextValue prints the sender in one cell: "agent 87a19d5f (claude-code)".
func (s messageSender) TextValue() string {
	if s.Provider == "" {
		return "agent " + cli.ShortID(s.TabID)
	}
	return fmt.Sprintf("agent %s (%s)", cli.ShortID(s.TabID), s.Provider)
}

// messageSenderOf converts the Worker's sender, or returns nil for an input
// that no agent sent.
func messageSenderOf(sender *leapmuxv1.AgentMessageSender) *messageSender {
	if sender.GetTabId() == "" {
		return nil
	}
	return &messageSender{TabID: sender.GetTabId(), Provider: agentlabels.CLIAlias(sender.GetProvider())}
}

// messageSenderOfRow reads the sender of a transcript row from the row's
// Worker metadata, where the Worker records it on the user row of an agent
// message. It returns nil for any other row, and for a supplement that does
// not decode: the sender is a detail of the row, and the row still prints.
func messageSenderOfRow(m *leapmuxv1.AgentChatMessage) *messageSender {
	raw := m.GetSupplementalContent()
	if len(raw) == 0 {
		return nil
	}
	decoded, err := msgcodec.Decompress(raw, m.GetSupplementalContentCompression())
	if err != nil {
		return nil
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(decoded, &envelope) != nil {
		return nil
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal(envelope[contracts.MessageSupplementFieldMetadata], &metadata) != nil {
		return nil
	}
	encoded, ok := metadata[contracts.MessageMetadataFieldAgentMessageSender]
	if !ok {
		return nil
	}
	sender := &leapmuxv1.AgentMessageSender{}
	// A Worker of a later version may state more of the sender.
	if (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(encoded, sender) != nil {
		return nil
	}
	return messageSenderOf(sender)
}
