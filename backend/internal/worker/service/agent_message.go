package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

// A message between agents (AGENT_INPUT_KIND_AGENT_MESSAGE) has two halves in
// the Worker, and both live here:
//
//   - Provenance: agentMessageSender reads the sender from the caller, which
//     the request cannot state, so no caller can forge or drop it. The caller
//     states the tab of the control token that made the call, or the tab that
//     a sibling Worker states for a call that it relays (see CallSource).
//   - Delivery: agentMessageText puts one header before the text, which
//     identifies the sender and states that the message is not the operator's
//     instruction. Every provider receives it through resolveControlInput, the
//     one place where a queued input becomes the text that a provider reads.
//
// An agent that sends a message to itself gets an agent message from itself,
// not a user message. Its own words then never come back to it as the
// operator's instruction, and an agent can still hold a note for a later turn,
// such as a reminder that "agent send self --at" schedules.

// agentMessageHeaderFormat is the line that comes before the text of a message
// from another agent. The first verb takes the sender's title, already quoted,
// and the second takes the display name of its provider. The browser never
// sees the line: the transcript row stores the text that the sender wrote, and
// the row's metadata states the sender.
const agentMessageHeaderFormat = "[Message from agent %s (%s), not from your operator]"

// DescribeCallSource states the tab tabID of this Worker as the source of a
// call: the tab, and for an agent tab the agent's provider and its title now.
// The local IPC router asks for it when it relays a call from tabID to a
// sibling Worker, which holds no row for the tab; agentMessageSender asks for
// it when the call stays here. One description serves both, so a message
// reads the same whichever Worker the recipient runs on.
//
// A tab is an agent tab exactly when this Worker holds a root agent with its
// id; a terminal tab states no agent. A child agent has no process and no
// token, so no call comes from one, and it states no agent either.
func (svc *Service) DescribeCallSource(ctx context.Context, tabID string) (*leapmuxv1.CallSource, error) {
	source := &leapmuxv1.CallSource{TabId: tabID}
	row, err := svc.Queries.GetAgentByID(ctx, tabID)
	if errors.Is(err, sql.ErrNoRows) {
		return source, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the agent of the calling tab: %w", err)
	}
	if !row.ParentAgentID.Valid {
		source.Agent = &leapmuxv1.CallSourceAgent{Provider: row.AgentProvider, Title: row.Title}
	}
	return source, nil
}

// agentMessageSender returns the agent tab that made the call, or nil when no
// agent made it: the browser or the CLI outside a tab, a terminal tab, or a
// quake terminal.
//
// A call that a sibling Worker relays carries the agent that the sibling
// stated, and this Worker never looks for the sibling's tab in its own rows:
// a tab id of the sibling proves nothing about a row here. Any other call
// comes from a local token, and this Worker describes the tab itself.
func (svc *Service) agentMessageSender(caller channel.Caller) (*inputqueue.AgentSender, error) {
	if caller.SourceTabID == "" {
		return nil, nil
	}
	agent := caller.SourceAgent
	if caller.RelayWorkerID == "" {
		source, err := svc.DescribeCallSource(bgCtx(), caller.SourceTabID)
		if err != nil {
			return nil, err
		}
		agent = channel.SourceAgent{Provider: source.GetAgent().GetProvider(), Title: source.GetAgent().GetTitle()}
	}
	if agent.IsZero() {
		return nil, nil
	}
	return &inputqueue.AgentSender{TabID: caller.SourceTabID, Provider: agent.Provider, Title: agent.Title}, nil
}

// agentMessageText returns the text that the recipient of an agent message
// reads: the header, a blank line, and the text that the sender wrote. A
// message that holds attachments alone gets the header alone.
//
// The header states the title that the sender had when it sent the message,
// which the queue keeps, as a letter states its sender. The dispatch reads no
// row for it: the sender can live on a sibling Worker, or its row can be
// purged while the message waits. A sender with no title is identified by its
// tab id.
func agentMessageText(item inputqueue.DispatchItem) (string, error) {
	if item.Sender == nil {
		// The schema ties the kind to a sender, so only a broken row gets here.
		return "", errors.New("the agent message states no sender")
	}
	return senderMessageText(*item.Sender, item.Text), nil
}

// senderMessageText returns the text that the recipient of a message from
// sender reads, when the sender wrote text (see agentMessageText).
func senderMessageText(sender inputqueue.AgentSender, text string) string {
	title := sender.TabID
	if strings.TrimSpace(sender.Title) != "" {
		title = sender.Title
	}
	header := agentMessageHeader(title, sender.Provider)
	if text == "" {
		return header
	}
	return header + "\n\n" + text
}

// agentMessageHeader formats the header of a message from the agent titled
// title. The title is quoted with Go's escapes, so a title that holds a quote
// or a line break cannot end the header early and write a line of its own.
func agentMessageHeader(title string, provider leapmuxv1.AgentProvider) string {
	return fmt.Sprintf(agentMessageHeaderFormat, strconv.Quote(title), agentlabels.DisplayName(provider))
}

// agentMessageTitle returns the title that the header of delivered states,
// when delivered is exactly the text that senderMessageText builds for sender
// with that title and for text. It returns "" for any other text, and for a
// header that identifies the sender by its tab id, which is what the header of
// an untitled sender states.
//
// The transcript row of an agent message states the sender's tab and provider,
// but not its title (see inputqueue.SteeredInput), and the Worker reads no
// agent row for a header (see agentMessageText). When a provider hands back a
// steer that it dropped, the text that it holds is then the one record of the
// title that the header stated.
func agentMessageTitle(delivered string, sender inputqueue.AgentSender, text string) string {
	prefix, _, _ := strings.Cut(agentMessageHeaderFormat, "%s")
	rest, ok := strings.CutPrefix(delivered, prefix)
	if !ok {
		return ""
	}
	quoted, err := strconv.QuotedPrefix(rest)
	if err != nil {
		return ""
	}
	title, err := strconv.Unquote(quoted)
	if err != nil || title == sender.TabID {
		return ""
	}
	sender.Title = title
	if senderMessageText(sender, text) != delivered {
		return ""
	}
	return title
}
