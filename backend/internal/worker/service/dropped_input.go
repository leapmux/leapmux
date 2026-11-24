package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

// requeueDroppedInput queues again a steer that the provider of agentID took
// into the running turn and dropped before the model read it, and reports
// whether this call added the item. The provider hands back the request that
// it received, and the Worker reads the steered item by the request's input id
// (see inputqueue.Store.SteeredInput). The copy keeps what the item was:
//
//   - A message from another agent comes back as a message from the same
//     sender, with the text that the sender wrote. The dispatch of the copy
//     puts the header before it, as for the original, so the recipient never
//     reads it as its operator's instruction.
//   - Every other input comes back as a user message with the text that the
//     model would have read. For the user's own message, that is the message.
//     A control reply answered a request of the turn that it steered into,
//     and no control response links the id of its copy, so the copy cannot be
//     a reply: an answer without its question fails at dispatch. Its text
//     states what it answers. The queue admits an automatic retry only from
//     the Worker's own schedule, so its copy is a user message too.
//
// The attachments come from the request, because a transcript row keeps no
// attachment data.
//
// An input that the queue still holds, or that the user removed, is not queued
// again. The id of the copy derives from the original's (see droppedInputID),
// so a repeat of one drop adds nothing.
func (svc *Service) requeueDroppedInput(agentID string, dropped agent.SteerRequest) (bool, error) {
	origin, err := svc.InputQueue.SteeredInput(bgCtx(), agentID, dropped.InputID)
	if err != nil {
		return false, fmt.Errorf("requeue dropped input %q: read the steered input: %w", dropped.InputID, err)
	}
	if origin.Held {
		return false, nil
	}
	_, added, err := svc.InputQueue.EnqueueReportingAdded(bgCtx(), droppedInputItem(agentID, dropped, origin))
	return added, err
}

// droppedInputItem returns the queue item that takes back the dropped steer
// whose steered input is origin (see requeueDroppedInput).
//
// A message from another agent enters as a user message with its sender, and
// the queue's admission makes it an agent message again, under the same rules
// as the original.
func droppedInputItem(agentID string, dropped agent.SteerRequest, origin inputqueue.SteeredInput) inputqueue.NewItem {
	item := inputqueue.NewItem{
		ID: droppedInputID(agentID, dropped.InputID), AgentID: agentID,
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: dropped.Content,
		Attachments:      queueAttachments(dropped.Attachments),
		ReclassifyOnEdit: true,
	}
	if origin.Sender == nil {
		return item
	}
	sender := *origin.Sender
	if sender.Title == "" {
		// A transcript row keeps no title. Without the one that the header
		// stated, the copy's header identifies the sender by its tab id.
		sender.Title = agentMessageTitle(dropped.Content, sender, origin.Text)
	}
	item.Sender, item.Text = &sender, origin.Text
	return item
}

// droppedInputID is the queue item id of the copy of an input that a provider
// dropped. It derives from the dropped input, so a repeat of one drop gives the
// same item and the queue adds nothing. A copy has an id of its own, so a drop
// of the copy is another drop. The id also becomes the transcript message id,
// which is unique across the Worker, so the agent id is part of the hash. The
// length prefix keeps each pair of ids apart: no separator is safe, because an
// input id is a client's own text.
func droppedInputID(agentID, inputID string) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%d:%s%s", len(agentID), agentID, inputID))
	return "dropped-" + hex.EncodeToString(sum[:16])
}
