package acp

import (
	"maps"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type acpURLElicitation struct {
	requestID          string
	publication        providerkit.PublishedControlRequest
	pendingPublication bool
}

// RememberURLElicitation associates a native URL flow with its control request.
// Call it before PublishSessionControlRequest, after confirming that the request uses URL mode.
func (b *Base) RememberURLElicitation(matchKey string, line *providerkit.ParsedLine) {
	if matchKey == "" || line == nil || !line.HasID() || !b.ServesSession(controlRequestSessionID(line.Params)) {
		return
	}
	identity, valid := agent.NewControlRequestIdentity(line.ID)
	if !valid {
		return
	}
	b.forgetResolvedURLElicitations()
	b.Mu.Lock()
	if b.urlElicitations == nil {
		b.urlElicitations = make(map[string]acpURLElicitation)
	}
	entry := acpURLElicitation{requestID: identity.Key, pendingPublication: true}
	if previous := b.urlElicitations[matchKey]; previous.requestID == identity.Key {
		entry.publication = previous.publication
	}
	b.urlElicitations[matchKey] = entry
	b.Mu.Unlock()
}

// bindURLElicitation updates only a remembered URL request. Other uses of its wire ID cannot replace its identity.
func (b *Base) bindURLElicitation(line *providerkit.ParsedLine, publication providerkit.PublishedControlRequest) {
	identity, valid := agent.NewControlRequestIdentity(line.ID)
	if !valid {
		return
	}
	b.Mu.Lock()
	for key, entry := range b.urlElicitations {
		if entry.requestID == identity.Key && entry.pendingPublication {
			if publication.RequestID != "" {
				entry.publication = publication
			}
			entry.pendingPublication = false
			b.urlElicitations[key] = entry
		}
	}
	b.Mu.Unlock()
	b.forgetResolvedURLElicitations()
}

// SendRawInput delegates each answer and releases URL records that the transport no longer owns.
func (b *Base) SendRawInput(data []byte, stop agent.StopContext) error {
	err := b.SendRawInputToOutstandingRequest(data, stop)
	b.forgetResolvedURLElicitations()
	return err
}

func (b *Base) forgetResolvedURLElicitations() {
	b.Mu.Lock()
	entries := maps.Clone(b.urlElicitations)
	b.Mu.Unlock()
	for key, entry := range entries {
		if entry.pendingPublication || b.ControlPublicationIsCurrent(entry.publication) {
			continue
		}
		b.Mu.Lock()
		current, found := b.urlElicitations[key]
		if found && !current.pendingPublication && current.publication == entry.publication && current.requestID == entry.requestID {
			delete(b.urlElicitations, key)
		}
		b.Mu.Unlock()
	}
}

// CompleteURLElicitation accepts a completed URL flow only while its original control remains open.
func (b *Base) CompleteURLElicitation(matchKey string, answer any) bool {
	b.Mu.Lock()
	entry, found := b.urlElicitations[matchKey]
	b.Mu.Unlock()
	if !found {
		return false
	}
	return b.AnswerPublishedControlRequest(b.sink, entry.publication, answer, func(error) { b.forgetResolvedURLElicitations() })
}
