package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
)

type zcodeToolReference struct {
	Kind               string `json:"kind"`
	ToolCallID         string `json:"toolCallId"`
	ToolName           string `json:"toolName"`
	AssistantMessageID string `json:"assistantMessageId"`
	AgentID            string `json:"agentId"`
	ChildSessionID     string `json:"childSessionId"`
}

func zcodeToolReferenceFrom(raw []byte) (zcodeToolReference, bool) {
	var envelope struct {
		Type    string             `json:"type"`
		Payload zcodeToolReference `json:"payload"`
	}
	err := json.Unmarshal(raw, &envelope)
	return envelope.Payload, err == nil && envelope.Type == contracts.ZCodeEventToolUpdated && envelope.Payload.ToolCallID != ""
}

func (ref zcodeToolReference) lookup(previous zcodeToolLookup) (zcodeToolLookup, bool) {
	if ref.ToolName != "" {
		previous.toolName = ref.ToolName
	}
	if ref.AssistantMessageID != "" {
		previous.messageID = ref.AssistantMessageID
	}
	if ref.ChildSessionID != "" {
		previous.sessionID = ref.ChildSessionID
	}
	if ref.AgentID != "" {
		prefix := contracts.ZCodeToolPrefixSubagent + ref.AgentID + "_"
		if !strings.HasPrefix(ref.ToolCallID, prefix) || len(ref.ToolCallID) == len(prefix) {
			return zcodeToolLookup{}, false
		}
		previous.callID = strings.TrimPrefix(ref.ToolCallID, prefix)
	}
	return previous, true
}

// zcodeToolSource reads ZCode's tool records out of the CLI's session database.
//
// requests holds what each scheduled tool notification stated about a call. The later
// record read needs that to select the right row.
//
// mu guards requests, which is the one field that two goroutines reach. The reader
// goroutine writes it from observeMessage and clears it from resetRecords and
// finishTurn; the supplement worker reads it from readSupplements. mu is held for the
// map operations alone and never across the database read.
type zcodeToolSource struct {
	noopToolSupplementSource
	// store keeps the one database handle of the agent. The agent's transcript and
	// every child transcript share it, and the store's own mutex serializes the reads.
	store *zcodeToolStore
	// resolveLocation reports the agent's database, artifact root and current session.
	// The agent holds its own mutex for that answer, so both goroutines may ask.
	resolveLocation func() zcodeToolStoreLocation

	// artifacts holds the decoded artifact bodies of THIS transcript's turn. Each
	// transcript owns one, so a subagent's turn end empties its own and never the
	// parent's. See zcodeArtifactCache.
	artifacts *zcodeArtifactCache

	mu       sync.Mutex
	requests map[string]zcodeToolLookup
}

func newZCodeToolTranscript(ctx context.Context, services ProviderServices, resolveLocation func() zcodeToolStoreLocation) *toolTranscript {
	store := &zcodeToolStore{}
	// The store holds one database handle for the agent. The agent's context ending
	// is what releases it, because the transcript itself has no teardown call.
	context.AfterFunc(ctx, store.close)
	return newToolTranscript(ctx, services, newZCodeToolSource(store, resolveLocation))
}

func newZCodeToolSource(store *zcodeToolStore, resolveLocation func() zcodeToolStoreLocation) *zcodeToolSource {
	return &zcodeToolSource{
		store:           store,
		resolveLocation: resolveLocation,
		artifacts:       &zcodeArtifactCache{},
		requests:        make(map[string]zcodeToolLookup),
	}
}

func (z *zcodeToolSource) providerName() string { return "ZCode" }

func (z *zcodeToolSource) locate(string) toolTranscriptLocation {
	location := z.resolveLocation()
	return toolTranscriptLocation{sessionKey: location.sessionID, path: location.databasePath, ready: location.databasePath != ""}
}

func (z *zcodeToolSource) toolCallID(original []byte) string {
	ref, valid := zcodeToolReferenceFrom(original)
	if valid && (ref.Kind == contracts.ZCodeToolKindResult || ref.Kind == contracts.ZCodeToolKindError) {
		return ref.ToolCallID
	}
	return ""
}

func (z *zcodeToolSource) observeMessage(content MessageContent, span SpanInfo) {
	ref, valid := zcodeToolReferenceFrom(content.Original)
	if !valid || ref.Kind != contracts.ZCodeToolKindScheduled || ref.ToolCallID != span.SpanID {
		return
	}
	lookup, valid := ref.lookup(zcodeToolLookup{})
	if !valid {
		return
	}
	z.mu.Lock()
	z.requests[ref.ToolCallID] = lookup
	z.mu.Unlock()
}

func (z *zcodeToolSource) resetRecords() { z.clearRequests() }

func (z *zcodeToolSource) finishTurn() { z.clearRequests() }

// clearRequests drops what this source holds for a turn or a session that ended,
// AND the artifact bodies it read for that turn. Only a pass of the same turn can
// read one of those, because the turn end clears the pending set a later pass asks
// about -- so keeping them past this point retains every artifact of the session
// for nothing.
//
// The cache is this source's own, not the store's. Its drop therefore takes no lock
// that a database read holds: this runs inside the transcript's own mutex on the
// goroutine that drains the provider's stdout, and readZCodeToolRecords holds the
// store mutex for a whole multi-statement SQLite read, so a shared cache made every
// turn end wait for whatever another transcript was reading.
func (z *zcodeToolSource) clearRequests() {
	z.mu.Lock()
	clear(z.requests)
	z.mu.Unlock()
	z.artifacts.drop()
}

// newChild builds a source on the SAME store, which is how each child transcript
// shares the agent's one database handle. It gets its OWN artifact cache, because a
// child's turn ends the instant its Agent result lands and the parent's has not.
func (z *zcodeToolSource) newChild() toolSupplementSource {
	return newZCodeToolSource(z.store, z.resolveLocation)
}

// readSupplements asks the agent for the store location again rather than reading what
// locate reported. The transcript calls locate on the reader goroutine, so a field that
// held that answer would be one more value that two goroutines share, and the one this
// read needs is the current one.
func (z *zcodeToolSource) readSupplements(ctx context.Context, _ string, pending map[string]MessageContent, final bool) (map[string][]byte, error) {
	location := z.resolveLocation()
	z.mu.Lock()
	lookups := make(map[string]zcodeToolLookup)
	for id, original := range pending {
		ref, valid := zcodeToolReferenceFrom(original.Original)
		if !valid || ref.ToolCallID != id {
			continue
		}
		if lookup, valid := ref.lookup(z.requests[id]); valid {
			lookups[id] = lookup
		}
	}
	z.mu.Unlock()
	records, readErr := readZCodeToolRecords(ctx, z.store, z.artifacts, location, lookups)
	out := make(map[string][]byte)
	answered := make([]string, 0, len(records))
	for id, record := range records {
		if !record.ready && !final {
			continue
		}
		supplement, err := zcodeToolResultSupplement(pending[id].Original, record)
		if err != nil {
			readErr = errors.Join(readErr, err)
			continue
		}
		out[id] = supplement
		answered = append(answered, id)
	}
	if len(answered) > 0 {
		z.mu.Lock()
		for _, id := range answered {
			delete(z.requests, id)
		}
		z.mu.Unlock()
	}
	return out, readErr
}

func zcodeToolResultSupplement(original []byte, record zcodeToolRecord) ([]byte, error) {
	ref, valid := zcodeToolReferenceFrom(original)
	if !valid {
		return nil, fmt.Errorf("invalid ZCode tool result")
	}
	encoded, err := json.Marshal(contracts.ZCodeToolResultEnvelope{
		Type:       contracts.ZCodeEventToolUpdated,
		Payload:    contracts.ZCodeSupplementRef{Kind: ref.Kind, ToolCallID: ref.ToolCallID},
		NativeTool: record.native,
		Artifacts:  record.artifacts,
	})
	if err != nil {
		return nil, err
	}
	if len(encoded) > liveStdoutMaxTokenSize() {
		return nil, fmt.Errorf("ZCode tool supplement exceeds the message size limit")
	}
	return encoded, nil
}
