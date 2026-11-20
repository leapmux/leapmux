package claude

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// claudeTranscriptKey normalizes a wire message UUID into the exact transcript
// key of a persisted envelope. Claude Code's two native eviction signals (a
// fallback system frame's retracted_message_uuids and a replacement assistant
// frame's supersedes) name the same wire UUIDs, so both the persisted key and
// the signal normalize through this function to match exactly. An empty or
// whitespace-only UUID yields no key, which keeps the write unkeyed.
func claudeTranscriptKey(uuid string) string {
	return strings.ToLower(strings.TrimSpace(uuid))
}

// claudeRevocableKey returns the transcript key of a revocable envelope: an
// assistant or tool-result frame, the only shapes a native eviction names. Any
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

// claudeEvictionKeys returns the normalized target keys one frame evicts: the
// fallback system frame's retracted_message_uuids, or a replacement assistant
// frame's supersedes. The carrying frame's own UUID drops out, because a frame
// never evicts itself -- its fallback notice and model update must survive.
// Empty values, repeats and malformed entries drop out as well.
func claudeEvictionKeys(content []byte, msgType string, self string) []string {
	var targets []string
	switch msgType {
	case claudeMsgTypeSystem:
		var frame struct {
			Subtype               string          `json:"subtype"`
			RetractedMessageUUIDs json.RawMessage `json:"retracted_message_uuids"`
		}
		if json.Unmarshal(content, &frame) != nil || frame.Subtype != contracts.ClaudeSystemSubtypeModelRefusalFallback {
			return nil
		}
		// The plural field is a list on the wire. A bare value is malformed
		// and drops out.
		var many []json.RawMessage
		if json.Unmarshal(frame.RetractedMessageUUIDs, &many) != nil {
			return nil
		}
		targets = claudeStringListEntries(many)
	case claudeMsgTypeAssistant:
		var frame struct {
			Supersedes json.RawMessage `json:"supersedes"`
		}
		if json.Unmarshal(content, &frame) != nil {
			return nil
		}
		targets = claudeStringList(frame.Supersedes)
	default:
		return nil
	}
	keys := make([]string, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, value := range targets {
		key := claudeTranscriptKey(value)
		if key == "" || key == self {
			continue
		}
		if _, repeat := seen[key]; repeat {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
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
	sink    agent.ProviderServices
	capture func(agent.MessageContent, agent.SpanInfo) agent.MessageContent
	keys    []string
}

type claudeRetractionTarget struct {
	destination *claudeTranscriptDestination
	authority   agent.MessageContent
	keys        []string
}

type claudeRetractionOwner struct {
	authority agent.MessageContent
	admission *claudeOutputDecision
}

// The reader records provisional owners. Task events can replace a child
// lease from a queue callback after its sender's transcript settles.
type claudeTranscriptIndex struct {
	mu       sync.Mutex
	root     *claudeTranscriptDestination
	children map[string]*claudeTranscriptDestination
	byKey    map[string]map[*claudeTranscriptDestination]claudeRetractionOwner
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
	for _, key := range previous.keys {
		owners := a.transcripts.byKey[key]
		delete(owners, previous)
		if len(owners) == 0 {
			delete(a.transcripts.byKey, key)
		}
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
	if a.transcripts.byKey == nil {
		a.transcripts.byKey = make(map[string]map[*claudeTranscriptDestination]claudeRetractionOwner)
	}
	owners := a.transcripts.byKey[content.IdempotencyKey]
	if owners == nil {
		owners = make(map[*claudeTranscriptDestination]claudeRetractionOwner)
		a.transcripts.byKey[content.IdempotencyKey] = owners
	}
	if _, known := owners[destination]; !known {
		owners[destination] = claudeRetractionOwner{
			authority: agent.MessageContent{Publication: content.Publication, AgentSessionID: content.AgentSessionID},
			admission: frame.admission,
		}
		if destination != a.transcripts.root {
			destination.keys = append(destination.keys, content.IdempotencyKey)
		}
	}
}

type claudeRetractionCandidate struct {
	key    string
	owners map[*claudeTranscriptDestination]claudeRetractionOwner
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
		var eligible map[*claudeTranscriptDestination]claudeRetractionOwner
		for dest, owner := range a.transcripts.byKey[key] {
			if (destination == nil || dest == destination) && agent.TranscriptFrameIsCurrent(owner.authority) {
				if eligible == nil {
					eligible = make(map[*claudeTranscriptDestination]claudeRetractionOwner)
				}
				eligible[dest] = owner
			}
		}
		if len(eligible) > 0 {
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
		count := 0
		for destination, owner := range candidate.owners {
			accepted, settled := owner.admission.result()
			if !settled {
				return nil, false
			}
			if accepted {
				count++
				selected, captured = destination, owner.authority
			}
		}
		if count != 1 {
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

func (target claudeRetractionTarget) apply() error {
	return agent.DirectTranscript(target.destination.sink).RetractMessagesByIdempotencyKey(target.authority, target.keys)
}
