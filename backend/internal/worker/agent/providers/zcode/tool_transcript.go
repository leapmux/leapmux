package zcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/tooltranscript"
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
// goroutine writes it from ObserveMessage and clears it from ResetRecords and
// FinishTurn; the supplement worker reads it from ReadSupplements. mu is held for the
// map operations alone and never across the database read.
type zcodeToolSource struct {
	tooltranscript.SourceDefaults
	// store keeps the one database handle of the agent. The agent's transcript and
	// every child transcript share it, and the store's own mutex serializes the reads.
	store *zcodeToolStore
	// resolveLocation reports the agent's database, output file root and current session.
	// The agent holds its own mutex for that answer, so both goroutines may ask.
	resolveLocation func() zcodeToolStoreLocation

	// output files holds the decoded output file bodies of THIS transcript's turn. Each
	// transcript owns one, so a subagent's turn end empties its own and never the
	// parent's. See zcodeOutputFileCache.
	outputFiles *zcodeOutputFileCache

	mu                sync.Mutex
	requests          map[string]zcodeToolLookup
	requestGeneration uint64
	canFinalize       func() bool
	canFinalizeChild  func(string) bool
}

type zcodeToolTranscriptPolicy struct {
	canFinalizeRoot  func() bool
	canFinalizeChild func(string) bool
}

func newZCodeToolTranscript(ctx context.Context, services agent.ProviderServices, resolveLocation func() zcodeToolStoreLocation, policy *zcodeToolTranscriptPolicy) *tooltranscript.Transcript {
	store := &zcodeToolStore{}
	// The store holds one database handle for the agent. The agent's context ending
	// is what releases it, because the transcript itself has no teardown call.
	context.AfterFunc(ctx, store.retire)
	source := newZCodeToolSource(store, resolveLocation)
	if policy != nil {
		source.canFinalize = policy.canFinalizeRoot
		source.canFinalizeChild = policy.canFinalizeChild
	}
	return tooltranscript.New(ctx, services, source)
}

func (z *zcodeToolSource) AuthorityIsCurrent(owner agent.TranscriptOwner) bool {
	return owner != nil && owner.IsSessionCurrent()
}

func (z *zcodeToolSource) CanFinalizeTurn() bool {
	return z.canFinalize == nil || z.canFinalize()
}

func newZCodeToolSource(store *zcodeToolStore, resolveLocation func() zcodeToolStoreLocation) *zcodeToolSource {
	return &zcodeToolSource{
		store:           store,
		resolveLocation: resolveLocation,
		outputFiles:     &zcodeOutputFileCache{},
		requests:        make(map[string]zcodeToolLookup),
	}
}

func (z *zcodeToolSource) ProviderName() string { return "ZCode" }

func (z *zcodeToolSource) Locate(string) tooltranscript.Location {
	location := z.resolveLocation()
	return tooltranscript.Location{SessionKey: location.sessionID, Path: location.databasePath, OutputFileRoot: location.outputFileRoot, Ready: location.databasePath != ""}
}

func (z *zcodeToolSource) ToolCallID(original []byte) string {
	ref, valid := zcodeToolReferenceFrom(original)
	if valid {
		return ref.ToolCallID
	}
	return ""
}

func (z *zcodeToolSource) ObserveMessage(content agent.MessageContent, span agent.SpanInfo) {
	ref, valid := zcodeToolReferenceFrom(content.Original)
	if !valid || ref.Kind != contracts.ZCodeToolKindScheduled {
		return
	}
	lookup, valid := ref.lookup(zcodeToolLookup{})
	if !valid {
		return
	}
	z.mu.Lock()
	if lookup.callID == "" {
		lookup.callID = ref.ToolCallID
	}
	lookup = zcodeCapturedToolLookup(content, span.SpanID, lookup)
	z.requests[span.SpanID] = lookup
	z.mu.Unlock()
}

func (z *zcodeToolSource) ResetRecords() { z.clearRequests() }

func (z *zcodeToolSource) FinishTurn() { z.clearRequests() }

// clearRequests drops what this source holds for a turn or a session that ended,
// AND the output file bodies it read for that turn. Only a pass of the same turn can
// read one of those, because the turn end clears the pending set a later pass asks
// about -- so keeping them past this point retains every output file of the session
// for nothing.
//
// The cache is this source's own, not the store's. Its drop therefore takes no lock
// that a database read holds: this runs inside the transcript's own mutex on the
// goroutine that drains the provider's stdout, and readZCodeToolRecords holds the
// store mutex for a whole multi-statement SQLite read, so a shared cache made every
// turn end wait for whatever another transcript was reading.
func (z *zcodeToolSource) clearRequests() {
	z.mu.Lock()
	z.requestGeneration++
	clear(z.requests)
	z.mu.Unlock()
	z.outputFiles.drop()
}

// NewChild builds a source on the SAME store, which is how each child transcript
// shares the agent's one database handle. It gets its OWN output file cache, because a
// child's turn ends the instant its Agent result lands and the parent's has not.
func (z *zcodeToolSource) NewChild(childID string, services agent.ProviderServices) tooltranscript.Source {
	resolveParent := z.resolveLocation
	resolve := resolveParent
	if services != nil {
		resolve = func() zcodeToolStoreLocation {
			location := resolveParent()
			location.sessionID = services.MessageSessionID()
			return location
		}
	}
	child := newZCodeToolSource(z.store, resolve)
	child.canFinalizeChild = z.canFinalizeChild
	if canFinalize := z.canFinalizeChild; canFinalize != nil {
		child.canFinalize = func() bool { return canFinalize(childID) }
	}
	return child
}

// ReadSupplements uses the accepted row's complete native lookup location.
// A delayed read never selects the replacement session or artifact directory.
type zcodeToolRead struct {
	original []byte
	lookup   zcodeToolLookup
}

func captureZCodeToolRead(content agent.MessageContent, spanID string, previous zcodeToolLookup) (zcodeToolRead, bool) {
	ref, valid := zcodeToolReferenceFrom(content.Original)
	if !valid {
		return zcodeToolRead{}, false
	}
	previous = zcodeCapturedToolLookup(content, spanID, previous)
	if previous.callID == "" {
		previous.callID = ref.ToolCallID
	}
	lookup, valid := ref.lookup(previous)
	if !valid {
		return zcodeToolRead{}, false
	}
	return zcodeToolRead{original: slices.Clone(content.Original), lookup: lookup}, true
}

func (z *zcodeToolSource) CaptureSupplement(location tooltranscript.Location, content agent.MessageContent, span agent.SpanInfo) tooltranscript.SupplementReader {
	id := span.SpanID
	z.mu.Lock()
	previous := z.requests[id]
	z.mu.Unlock()
	read, valid := captureZCodeToolRead(content, id, previous)
	if !valid {
		return nil
	}
	store := z.store
	outputFiles := &zcodeOutputFileCache{}
	reads := map[string]zcodeToolRead{id: read}
	return func(ctx context.Context, final bool) ([]byte, error) {
		supplements, err := readZCodeToolSupplements(ctx, store, outputFiles, location, reads, final)
		return supplements[id], err
	}
}

func readZCodeToolSupplements(ctx context.Context, store *zcodeToolStore, outputFiles *zcodeOutputFileCache, captured tooltranscript.Location, reads map[string]zcodeToolRead, final bool) (map[string][]byte, error) {
	location := zcodeToolStoreLocation{databasePath: captured.Path, outputFileRoot: captured.OutputFileRoot, sessionID: captured.SessionKey}
	lookups := make(map[string]zcodeToolLookup, len(reads))
	for id, read := range reads {
		lookups[id] = read.lookup
	}
	records, readErr := readZCodeToolRecords(ctx, store, outputFiles, location, lookups)
	out := make(map[string][]byte, len(records))
	for id, record := range records {
		if !record.ready && !final {
			continue
		}
		supplement, err := zcodeToolResultSupplement(reads[id].original, record)
		if err != nil {
			readErr = errors.Join(readErr, err)
			continue
		}
		out[id] = supplement
	}
	return out, readErr
}

func (z *zcodeToolSource) ReadSupplements(ctx context.Context, captured tooltranscript.Location, pending map[string]agent.MessageContent, final bool) (map[string][]byte, error) {
	bindings := make(map[string]zcodeToolLookup, len(pending))
	z.mu.Lock()
	generation := z.requestGeneration
	for id := range pending {
		if previous, exists := z.requests[id]; exists {
			bindings[id] = previous
		}
	}
	z.mu.Unlock()
	reads := make(map[string]zcodeToolRead, len(pending))
	for id, content := range pending {
		if read, valid := captureZCodeToolRead(content, id, bindings[id]); valid {
			reads[id] = read
		}
	}
	out, readErr := readZCodeToolSupplements(ctx, z.store, z.outputFiles, captured, reads, final)
	if len(out) > 0 {
		z.mu.Lock()
		if z.requestGeneration == generation {
			for id := range out {
				previous, capturedBinding := bindings[id]
				current, exists := z.requests[id]
				if capturedBinding && exists && current == previous {
					delete(z.requests, id)
				}
			}
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
		Type:        contracts.ZCodeEventToolUpdated,
		Payload:     contracts.ZCodeSupplementRef{Kind: ref.Kind, ToolCallID: ref.ToolCallID},
		NativeTool:  record.native,
		OutputFiles: record.outputFiles,
	})
	if err != nil {
		return nil, err
	}
	if len(encoded) > agent.LiveMaxMessageSize() {
		return nil, fmt.Errorf("ZCode tool supplement exceeds the message size limit")
	}
	return encoded, nil
}

func zcodeCapturedToolLookup(content agent.MessageContent, spanID string, previous zcodeToolLookup) zcodeToolLookup {
	var fields map[string]json.RawMessage
	var captured contracts.ZCodeInvocationMetadata
	if json.Unmarshal(content.Metadata, &fields) != nil || json.Unmarshal(fields[contracts.ZCodeWorkerMetadataInvocation], &captured) != nil || captured.SpanID != spanID || captured.CallID == "" {
		return previous
	}
	return zcodeToolLookup{callID: captured.CallID, messageID: captured.MessageID, sessionID: captured.SessionID, toolName: captured.ToolName}
}

// canFinalizeZCodeTools keeps native lookup state until all concurrent work ends.
func (a *Agent) canFinalizeZCodeTools() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	for _, owner := range a.turns {
		if !owner.settled || a.hasActiveZCodeToolLocked(owner) {
			return false
		}
	}
	a.children.mu.Lock()
	defer a.children.mu.Unlock()
	for _, child := range a.children.spawns {
		if child.childAgentID != "" {
			return false
		}
	}
	return true
}

func (a *Agent) canFinalizeZCodeChildTools(childID string) bool {
	a.children.mu.Lock()
	defer a.children.mu.Unlock()
	for _, child := range a.children.spawns {
		if child.childAgentID == childID {
			return false
		}
	}
	return true
}

func (a *Agent) finalizeZCodeToolsWhenIdle() {
	if a.toolTranscript == nil || !a.canFinalizeZCodeTools() {
		return
	}
	if err := a.toolTranscript.FinalizePending(); err != nil && !errors.Is(err, agent.ErrOutputInFlight) {
		slog.Warn("zcode final tool enrichment failed", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) reapZCodeSettledTurns() {
	a.Mu.Lock()
	for _, owner := range a.turns {
		a.dropZCodeTurnLocked(owner)
	}
	a.Mu.Unlock()
	a.finalizeZCodeToolsWhenIdle()
}
