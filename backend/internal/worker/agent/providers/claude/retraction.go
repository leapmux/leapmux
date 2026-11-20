package claude

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// claudeTranscriptKey normalizes a wire message UUID into the exact transcript
// key of a persisted envelope. Claude Code's two native eviction signals (a
// fallback system frame's retracted_message_uuids and a replacement assistant
// frame's supersedes) identify the same wire UUIDs, so both the persisted key and
// the signal normalize through this function to match exactly. An empty or
// whitespace-only UUID yields no key, which keeps the write unkeyed.
func claudeTranscriptKey(uuid string) string {
	return strings.ToLower(strings.TrimSpace(uuid))
}

// claudeRevocableKey returns the transcript key of a revocable envelope: an
// assistant or tool-result frame, the only shapes that native eviction can remove. Any
// other envelope keeps no key and follows the ordinary unkeyed write.
func claudeRevocableKey(msgType string, env *messageEnvelope) string {
	switch msgType {
	case claudeMsgTypeAssistant:
		return claudeTranscriptKey(env.UUID)
	case claudeMsgTypeUser:
		if hasToolResultBlock(env) {
			return claudeTranscriptKey(env.UUID)
		}
	}
	return ""
}

// claudeStringList reads one wire value that can be a single string or an array
// of strings. A malformed value yields no entries, and a malformed entry drops
// only itself.
func claudeStringList(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var many []json.RawMessage
	if json.Unmarshal(raw, &many) != nil {
		return nil
	}
	return claudeStringListEntries(many)
}

// claudeStringListEntries keeps only the entries that are strings.
func claudeStringListEntries(many []json.RawMessage) []string {
	values := make([]string, 0, len(many))
	for _, entry := range many {
		var value string
		if json.Unmarshal(entry, &value) == nil {
			values = append(values, value)
		}
	}
	return values
}

type claudeTranscriptDestination struct {
	sink     agent.ProviderServices
	capture  func(agent.MessageContent, agent.SpanInfo) agent.MessageContent
	keys     map[string]struct{}
	apiRetry providerkit.RetryResolution
}

type claudeRetractionTarget struct {
	destination *claudeTranscriptDestination
	authority   agent.MessageContent
	keys        []string
}

type claudeRetractionOwner struct {
	destination *claudeTranscriptDestination
	publication agent.TranscriptPublication
	sessionID   string
	admission   *claudeOutputDecision
}

func (owner claudeRetractionOwner) authority() agent.MessageContent {
	return agent.MessageContent{Publication: owner.publication, AgentSessionID: owner.sessionID}
}

// claudeRetractionOwners stores the ordinary single destination inline.
type claudeRetractionOwners struct {
	first claudeRetractionOwner
	extra []claudeRetractionOwner
}

func (owners claudeRetractionOwners) all(yield func(claudeRetractionOwner) bool) {
	if owners.first.destination == nil || !yield(owners.first) {
		return
	}
	for _, owner := range owners.extra {
		if !yield(owner) {
			return
		}
	}
}

func (owners *claudeRetractionOwners) add(owner claudeRetractionOwner) {
	if owners.first.destination == nil {
		owners.first = owner
		return
	}
	owners.extra = append(owners.extra, owner)
}

// The reader records provisional owners. Task events can replace a child
// lease from a queue callback after its sender's transcript settles.
type claudeTranscriptIndex struct {
	mu       sync.Mutex
	root     *claudeTranscriptDestination
	children map[string]*claudeTranscriptDestination
	byKey    map[string]claudeRetractionOwners
}

func (a *Agent) resetTranscriptIndex() {
	a.transcripts.mu.Lock()
	defer a.transcripts.mu.Unlock()
	a.transcripts.root = nil
	a.transcripts.children = nil
	a.transcripts.byKey = nil
}

func (a *Agent) rootTranscript() *claudeTranscriptDestination {
	a.transcripts.mu.Lock()
	defer a.transcripts.mu.Unlock()
	if a.transcripts.root == nil {
		a.transcripts.root = &claudeTranscriptDestination{sink: a.sink, capture: a.sink.CaptureMessage}
	}
	return a.transcripts.root
}

func (a *Agent) childTranscript(childID string) *claudeTranscriptDestination {
	a.transcripts.mu.Lock()
	defer a.transcripts.mu.Unlock()
	return a.childTranscriptLocked(childID)
}

func (a *Agent) childTranscriptLocked(childID string) *claudeTranscriptDestination {
	if destination := a.transcripts.children[childID]; destination != nil {
		return destination
	}
	lease := a.sink.AcquireChildStateSink(childID)
	if lease.Sink == nil || lease.CaptureMessage == nil {
		return nil
	}
	if a.transcripts.children == nil {
		a.transcripts.children = make(map[string]*claudeTranscriptDestination)
	}
	if lease.RetainTranscriptSpans != nil && !lease.RetainTranscriptSpans() {
		return nil
	}
	destination := &claudeTranscriptDestination{sink: agent.NewModelProgressResetSink(lease.Sink), capture: lease.CaptureMessage}
	a.transcripts.children[childID] = destination
	return destination
}

func (a *Agent) replaceChildTranscript(childID string) {
	a.transcripts.mu.Lock()
	defer a.transcripts.mu.Unlock()
	previous := a.transcripts.children[childID]
	if previous == nil {
		return
	}
	delete(a.transcripts.children, childID)
	for key := range previous.keys {
		var retained claudeRetractionOwners
		for owner := range a.transcripts.byKey[key].all {
			if owner.destination != previous {
				retained.add(owner)
			}
		}
		a.transcripts.keepOwnersLocked(key, retained)
	}
	previous.keys = nil
	a.childTranscriptLocked(childID)
}

func (a *Agent) nativeTranscriptSession() string {
	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()
	if sessionID == "" {
		return a.sink.MessageSessionID()
	}
	return sessionID
}

func (a *Agent) rememberRevocableFrame(frame *claudeConversationFrame, wireSession string) {
	content, destination := frame.content, frame.destination
	if content.IdempotencyKey == "" || wireSession == "" || wireSession != a.nativeTranscriptSession() {
		return
	}
	a.transcripts.mu.Lock()
	defer a.transcripts.mu.Unlock()
	key := content.IdempotencyKey
	owners := a.transcripts.byKey[key]
	for owner := range owners.all {
		if owner.destination == destination && owner.admission == nil {
			return
		}
	}
	owners.add(claudeRetractionOwner{
		destination: destination, publication: content.Publication,
		sessionID: content.AgentSessionID, admission: frame.admission,
	})
	if a.transcripts.byKey == nil {
		a.transcripts.byKey = make(map[string]claudeRetractionOwners)
	}
	a.transcripts.byKey[key] = owners
	if destination != a.transcripts.root {
		if destination.keys == nil {
			destination.keys = make(map[string]struct{})
		}
		destination.keys[key] = struct{}{}
	}
}

func (index *claudeTranscriptIndex) keepOwnersLocked(key string, owners claudeRetractionOwners) {
	if owners.first.destination == nil {
		delete(index.byKey, key)
		return
	}
	index.byKey[key] = owners
}

// resolveClaudeRetractionOwner compacts a settled owner in the current index.
// Candidates already frozen by a later frame keep the original decision.
func (a *Agent) resolveClaudeRetractionOwner(frame *claudeConversationFrame, accepted bool) {
	decision := frame.admission
	if decision == nil {
		return
	}
	frame.admission = nil
	key, destination := frame.content.IdempotencyKey, frame.destination
	a.transcripts.mu.Lock()
	var retained claudeRetractionOwners
	destinationStillKnown := false
	acceptedDestination := false
	for owner := range a.transcripts.byKey[key].all {
		if owner.admission == decision {
			if !accepted || acceptedDestination {
				continue
			}
			owner.admission = nil
		}
		if owner.destination == destination {
			destinationStillKnown = true
			acceptedDestination = acceptedDestination || owner.admission == nil
		}
		retained.add(owner)
	}
	a.transcripts.keepOwnersLocked(key, retained)
	if !destinationStillKnown {
		delete(destination.keys, key)
	}
	a.transcripts.mu.Unlock()
	decision.resolve(accepted)
}

type claudeRetractionCandidate struct {
	key    string
	owners claudeRetractionOwners
}

// claudeRetractionCandidates freezes the preceding destinations at the
// signal's arrival. Receipt admission can finish later on their own queues.
func (a *Agent) claudeRetractionCandidates(keys []string, wireSession string, destination *claudeTranscriptDestination) []claudeRetractionCandidate {
	if len(keys) == 0 || wireSession == "" || wireSession != a.nativeTranscriptSession() {
		return nil
	}
	a.transcripts.mu.Lock()
	defer a.transcripts.mu.Unlock()
	var candidates []claudeRetractionCandidate
	for _, key := range keys {
		var eligible claudeRetractionOwners
		for owner := range a.transcripts.byKey[key].all {
			if (destination == nil || owner.destination == destination) && agent.TranscriptFrameIsCurrent(owner.authority()) {
				eligible.add(owner)
			}
		}
		if eligible.first.destination != nil {
			candidates = append(candidates, claudeRetractionCandidate{key: key, owners: eligible})
		}
	}
	return candidates
}

// selectClaudeRetractionTargets counts only accepted receipts. A queued
// redelivery that a receipt rejects supplies no routing authority.
func selectClaudeRetractionTargets(candidates []claudeRetractionCandidate) ([]claudeRetractionTarget, bool) {
	type destinationSession struct {
		destination *claudeTranscriptDestination
		sessionID   string
	}
	positions := make(map[destinationSession]int)
	var targets []claudeRetractionTarget
	for _, candidate := range candidates {
		var selected *claudeTranscriptDestination
		var captured agent.MessageContent
		ambiguous := false
		for owner := range candidate.owners.all {
			if owner.admission != nil {
				accepted, settled := owner.admission.result()
				if !settled {
					return nil, false
				}
				if !accepted {
					continue
				}
			}
			if selected == nil {
				selected, captured = owner.destination, owner.authority()
			} else if selected != owner.destination {
				ambiguous = true
			}
		}
		if selected == nil || ambiguous {
			continue
		}
		group := destinationSession{destination: selected, sessionID: captured.AgentSessionID}
		if position, found := positions[group]; found {
			targets[position].keys = append(targets[position].keys, candidate.key)
		} else {
			positions[group] = len(targets)
			targets = append(targets, claudeRetractionTarget{destination: selected, authority: captured, keys: []string{candidate.key}})
		}
	}
	return targets, true
}

func (target claudeRetractionTarget) apply(sequence agent.TranscriptSequence) error {
	return sequence.Retract(target.destination.sink, target.authority, target.keys)
}
