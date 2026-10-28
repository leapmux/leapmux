package muse

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// One deadline covers all reads and evidence waits in this operation.
func (a *Agent) newChildProofContext(deadline time.Time) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(a.Context())
	remaining := deadline.Sub(a.Clock().Now())
	if remaining <= 0 {
		cancel(context.DeadlineExceeded)
		return ctx, func() {}
	}
	timer := a.Clock().NewTimer(remaining, childProjectionTimerTag)
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-timer.C:
			cancel(context.DeadlineExceeded)
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		cancel(context.Canceled)
		timer.Stop(childProjectionTimerTag)
		<-done
	}
}

func (a *Agent) childProofRequest(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("encode the Muse proof request: %w", err)
	}
	// Context cancellation owns the limit. Pages must not extend it.
	return a.SendRequestContext(ctx, method, raw, 0)
}

func (a *Agent) readOwnedNativeTail(ctx context.Context, id string, state *sessionState) (nativePosition, error) {
	raw, err := a.childProofRequest(ctx, methodLogStreams, map[string]string{"sessionId": id})
	if err != nil {
		return nativePosition{}, err
	}
	var reply struct {
		Streams *[]struct {
			Stream nativeStream    `json:"stream"`
			Tail   *nativePosition `json:"tail"`
		} `json:"streams"`
	}
	if json.Unmarshal(raw, &reply) != nil || reply.Streams == nil {
		return nativePosition{}, fmt.Errorf("the Muse proof stream list is invalid")
	}
	matches := 0
	var tail nativePosition
	for _, stream := range *reply.Streams {
		if stream.Stream != (nativeStream{Kind: contracts.MuseStreamKindSession, ID: id}) {
			continue
		}
		if stream.Tail == nil || stream.Tail.ID == "" || stream.Tail.Sequence <= 0 {
			return nativePosition{}, fmt.Errorf("the Muse proof stream has no valid tail")
		}
		matches++
		tail = *stream.Tail
	}
	if matches != 1 {
		return nativePosition{}, fmt.Errorf("the Muse proof stream is not uniquely owned")
	}
	a.stateMu.Lock()
	current := a.currentSessionState(id, state)
	a.stateMu.Unlock()
	if !current {
		return nativePosition{}, agent.ErrChildRouteNotReady
	}
	return tail, nil
}

func (a *Agent) ensureNativePrefix(ctx context.Context, id string, state *sessionState, tail nativePosition) error {
	for {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		a.stateMu.Lock()
		if !a.currentSessionState(id, state) || state.log == nil {
			a.stateMu.Unlock()
			return agent.ErrChildRouteNotReady
		}
		complete, err := state.log.hasNativePrefixThrough(tail)
		from := state.log.contiguousThrough
		a.stateMu.Unlock()
		if err != nil || complete {
			return err
		}
		params := map[string]any{
			"sessionId": id, "stream": nativeStream{Kind: contracts.MuseStreamKindSession, ID: id}, "limit": 1000,
		}
		if from.Sequence > 0 {
			params["from"] = from
		}
		raw, err := a.childProofRequest(ctx, methodLogPage, params)
		if err != nil {
			return err
		}
		var page struct {
			Records *[]json.RawMessage `json:"records"`
			Cursor  json.RawMessage    `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &page) != nil || page.Records == nil || len(*page.Records) == 0 || len(page.Cursor) == 0 {
			return fmt.Errorf("the Muse proof page ended before its required tail")
		}
		var cursor struct {
			Stream nativeStream   `json:"stream"`
			After  nativePosition `json:"after"`
		}
		if json.Unmarshal(page.Cursor, &cursor) != nil || cursor.Stream != (nativeStream{Kind: contracts.MuseStreamKindSession, ID: id}) {
			return fmt.Errorf("the Muse proof page cursor is invalid")
		}
		expected := from.Sequence + 1
		var last nativePosition
		for _, source := range *page.Records {
			record, decodeErr := decodeNativeRecord(source, id)
			if decodeErr != nil || record.Sequence != expected {
				return fmt.Errorf("the Muse proof page contains an invalid record or a sequence gap")
			}
			last = nativePosition{ID: record.ID, Sequence: record.Sequence}
			if record.Sequence == tail.Sequence && record.ID != tail.ID {
				return fmt.Errorf("the Muse proof page conflicts with its required tail")
			}
			expected++
		}
		if cursor.After != last {
			return fmt.Errorf("the Muse proof page cursor differs from its last record")
		}
		a.stateMu.Lock()
		if !a.currentSessionState(id, state) || state.log == nil {
			a.stateMu.Unlock()
			return agent.ErrChildRouteNotReady
		}
		changed := false
		var admissionErr error
		for _, source := range *page.Records {
			record, decodeErr := decodeNativeRecord(source, id)
			if decodeErr != nil {
				admissionErr = decodeErr
				break
			}
			prior, exists := state.log.records[record.ID]
			if !exists || !slices.Equal(prior.raw, source) {
				changed = true
			}
			if addErr := state.log.add(source); addErr != nil {
				admissionErr = addErr
				state.log.unavailable = addErr.Error()
				break
			}
		}
		complete, err = state.log.hasNativePrefixThrough(tail)
		a.stateMu.Unlock()
		if changed {
			a.enrichLogItems(state)
		}
		if admissionErr != nil {
			return admissionErr
		}
		if err != nil || complete {
			return err
		}
		if last.Sequence >= tail.Sequence {
			return fmt.Errorf("the Muse proof page passed an absent required tail")
		}
	}
}

func decodeChildInputMarker(log *nativeLog, stored storedRecord) (*childInputMarker, error) {
	evidence := inputEvidence(stored)
	if evidence.decodeErr != nil {
		return nil, fmt.Errorf("the Muse child proof record is malformed")
	}
	if evidence.envelope.Payload.Kind != contracts.MuseParentInputRun {
		return nil, nil
	}
	var event struct {
		Kind   string `json:"kind"`
		Source string `json:"source"`
		Reason string `json:"reason"`
		Role   string `json:"role"`
	}
	if json.Unmarshal(evidence.envelope.Payload.Event, &event) != nil {
		return nil, fmt.Errorf("the Muse child proof event is malformed")
	}
	marker := &childInputMarker{
		position: nativePosition{ID: stored.record.ID, Sequence: stored.record.Sequence},
		runID:    evidence.envelope.Payload.RunID,
	}
	switch event.Kind {
	case childRunStartedKind:
		marker.runStart = true
	case childContextUpdatedKind:
		if event.Source != contracts.MuseParentInputInputSource {
			return nil, nil
		}
		if event.Reason != childInputApplicationKind || event.Role != childDeveloperRole || evidence.envelope.CausationID == nil || *evidence.envelope.CausationID == "" {
			return nil, fmt.Errorf("the Muse child input application has no exact native identity")
		}
		marker.inboxID = *evidence.envelope.CausationID
	default:
		return nil, nil
	}
	if marker.runID == "" {
		return nil, fmt.Errorf("the Muse child input marker has no run")
	}
	if failure := nativeInputEvidenceFailure(log, evidence); failure != "" {
		return nil, fmt.Errorf("%s", failure)
	}
	return marker, nil
}

// The caller holds stateMu and supplies a complete child prefix.
func firstChildRun(log *nativeLog, through nativePosition) (string, error) {
	records, complete, err := log.nativeRecordsBetween(nativePosition{}, through)
	if err != nil {
		return "", err
	}
	if !complete {
		return "", fmt.Errorf("the Muse child start prefix is incomplete")
	}
	for _, stored := range records {
		if stored.record.Payload.Kind != contracts.MuseParentInputRun {
			continue
		}
		evidence := inputEvidence(stored)
		var event struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(evidence.envelope.Payload.Event, &event) != nil {
			return "", fmt.Errorf("the Muse child start event is malformed")
		}
		if event.Kind == childRunStartedKind {
			if failure := nativeInputEvidenceFailure(log, evidence); failure != "" {
				return "", fmt.Errorf("%s", failure)
			}
			return evidence.envelope.Payload.RunID, nil
		}
	}
	return "", fmt.Errorf("the Muse child prefix has no initial run")
}

func initialChildInputProof(log *nativeLog, target nativeParentInputTarget) (bool, error) {
	spawn, control, count := selectNativeInputControl(log, contracts.MuseParentInputSpawnAccepted, func(value nativeInputControl) bool {
		return value.SubagentID == target.SubagentID
	})
	if count == 0 {
		return false, nil
	}
	if count != 1 || control.ParentSessionID != target.ParentSessionID {
		return false, fmt.Errorf("the Muse initial child spawn is ambiguous")
	}
	binding, _, count := selectNativeInputControl(log, contracts.MuseParentInputChildSessionBound, func(value nativeInputControl) bool {
		return value.SubagentID == target.SubagentID && value.ChildSessionID == target.ChildSessionID
	})
	if count == 0 {
		return false, nil
	}
	if count != 1 {
		return false, fmt.Errorf("the Muse initial child binding is ambiguous")
	}
	attestation, _, count := selectNativeInputControl(log, contracts.MuseParentInputStartAttested, func(value nativeInputControl) bool {
		return value.SubagentID == target.SubagentID && value.SubagentSessionID == target.ChildSessionID && value.RunStream == contracts.MuseParentInputRun+"/"+target.RunID
	})
	if count == 0 {
		return false, nil
	}
	if count != 1 {
		return false, fmt.Errorf("the Muse initial child attestation is ambiguous")
	}
	for _, evidence := range []nativeInputEvidence{spawn, binding, attestation} {
		if failure := nativeInputEvidenceFailure(log, evidence); failure != "" {
			return false, fmt.Errorf("%s", failure)
		}
	}
	return true, nil
}

// A nil match without a pending reason means positive initial or human ownership.
// Generic Ignored never authorizes a child input marker.
func resolveChildInputMarker(parent *nativeLog, target nativeParentInputTarget, marker childInputMarker, initialRun string) (*nativeParentInputMatch, string, error) {
	if marker.runStart && marker.runID == initialRun {
		proved, err := initialChildInputProof(parent, target)
		if err != nil {
			return nil, "", err
		}
		if !proved {
			return nil, "The native initial child input proof is incomplete.", nil
		}
		return nil, "", nil
	}
	var selected storedRecord
	count := 0
	for _, stored := range parent.records {
		if stored.record.Payload.Kind != contracts.MuseParentInputRun || stored.record.Payload.Event.Kind != contracts.MuseLogEventInboxItemDrained {
			continue
		}
		evidence := inputEvidence(stored)
		var drain nativeInputDrain
		if json.Unmarshal(evidence.envelope.Payload.Event, &drain) != nil {
			return nil, "", fmt.Errorf("the Muse parent input delivery is malformed")
		}
		if marker.inboxID != "" {
			if drain.ItemID != marker.inboxID {
				continue
			}
		} else {
			if drain.Target != (nativeStream{Kind: contracts.MuseParentInputRun, ID: target.RunID}) || drain.Snapshot == nil {
				continue
			}
			var source nativeInputSource
			if json.Unmarshal(drain.Snapshot.Source, &source) != nil {
				return nil, "", fmt.Errorf("the Muse parent input source is malformed")
			}
			if source.Source != contracts.MuseParentInputInputSource || source.InputKind != contracts.MuseParentInputFollowupTask {
				continue
			}
		}
		selected = stored
		count++
	}
	if count == 0 {
		return nil, "The native parent input delivery is unavailable.", nil
	}
	if count != 1 {
		return nil, "", fmt.Errorf("the Muse parent input delivery is ambiguous")
	}
	match := matchNativeParentInput(parent, selected, target)
	switch match.Status {
	case nativeParentInputMatched:
		return &match, "", nil
	case nativeParentInputHuman:
		return nil, "", nil
	case nativeParentInputPending:
		return nil, match.Reason, nil
	case nativeParentInputIgnored:
		return nil, "", fmt.Errorf("the native child input marker has no matching parent input")
	default:
		return nil, "", fmt.Errorf("the native child input proof is invalid: %s", match.Reason)
	}
}

// The caller holds stateMu. This position comes from a received raw record.
func highestNativePosition(log *nativeLog) nativePosition {
	var result nativePosition
	for sequence, id := range log.sequences {
		if sequence > result.Sequence {
			result = nativePosition{ID: id, Sequence: sequence}
		}
	}
	return result
}

func (a *Agent) proveChildInputMarker(ctx context.Context, id string, state *sessionState, marker childInputMarker) error {
	a.stateMu.Lock()
	parentID := state.parentSessionID
	parent := a.sessions[parentID]
	subagentID := state.subagentID
	valid := a.currentSessionState(id, state) && a.currentSessionState(parentID, parent) && state.log != nil && parent.log != nil
	var initialRun string
	var err error
	if valid {
		initialRun, err = firstChildRun(state.log, marker.position)
	}
	a.stateMu.Unlock()
	if !valid {
		return agent.ErrChildRouteNotReady
	}
	if err != nil {
		return err
	}
	tail, err := a.readOwnedNativeTail(ctx, parentID, parent)
	if err != nil {
		return err
	}
	if err := a.ensureNativePrefix(ctx, parentID, parent, tail); err != nil {
		return err
	}
	target := nativeParentInputTarget{ParentSessionID: parentID, ChildSessionID: id, SubagentID: subagentID, RunID: marker.runID}
	for {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		a.stateMu.Lock()
		if !a.currentSessionState(id, state) || !a.currentSessionState(parentID, parent) || state.projection == nil || state.projection.failed != nil {
			a.stateMu.Unlock()
			return agent.ErrChildRouteNotReady
		}
		observed := highestNativePosition(parent.log)
		if observed.Sequence > tail.Sequence {
			a.stateMu.Unlock()
			if err := a.ensureNativePrefix(ctx, parentID, parent, observed); err != nil {
				return err
			}
			tail = observed
			continue
		}
		complete, prefixErr := parent.log.hasNativePrefixThrough(tail)
		if prefixErr != nil || !complete {
			a.stateMu.Unlock()
			if prefixErr != nil {
				return prefixErr
			}
			return fmt.Errorf("the Muse parent proof prefix became incomplete")
		}
		// The matcher reads the complete observed prefix while stateMu protects it.
		match, pending, proofErr := resolveChildInputMarker(parent.log, target, marker, initialRun)
		var needed *nativeSourceRange
		if proofErr == nil && match != nil {
			pending, needed = unfinishedChildTextBeforeInput(id, state, marker)
		}
		childChanged := state.log.changed
		parentChanged := parent.log.changed
		projectionChanged := state.projection.changed
		writer := state.sink
		a.stateMu.Unlock()
		if proofErr != nil {
			return proofErr
		}
		if needed != nil {
			if err := a.ensureNativePrefix(ctx, id, state, needed.Last); err != nil {
				return err
			}
			continue
		}
		if pending == "" {
			if match == nil {
				return nil
			}
			supplement, err := json.Marshal(map[string]any{contracts.MuseSupplementFieldNativeRecords: match.Records})
			if err != nil {
				return err
			}
			err = writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{
				Original: slices.Clone(match.Original), Supplemental: supplement,
				AgentSessionID: id, IdempotencyKey: match.IdempotencyKey,
			}, agent.SpanInfo{})
			if !agent.OutputRetained(err) {
				return err
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", pending, context.Cause(ctx))
		case <-parentChanged:
		case <-childChanged:
		case <-projectionChanged:
		}
	}
}

func (a *Agent) proveChildInputsThrough(ctx context.Context, id string, state *sessionState, through nativePosition) error {
	if !a.hasCapability("rawLog") {
		return fmt.Errorf("the Muse host does not grant native log access")
	}
	if err := a.ensureNativePrefix(ctx, id, state, through); err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		a.stateMu.Lock()
		if !a.currentSessionState(id, state) || state.projection == nil || state.projection.failed != nil {
			a.stateMu.Unlock()
			return agent.ErrChildRouteNotReady
		}
		p := state.projection
		complete, err := state.log.hasNativePrefixThrough(through)
		if err != nil || !complete {
			a.stateMu.Unlock()
			if err != nil {
				return err
			}
			return fmt.Errorf("the Muse child proof prefix became incomplete")
		}
		if p.classifiedThrough.Sequence >= through.Sequence {
			a.stateMu.Unlock()
			return nil
		}
		sequence := p.classifiedThrough.Sequence + 1
		stored := state.log.records[state.log.sequences[sequence]]
		marker, err := decodeChildInputMarker(state.log, stored)
		a.stateMu.Unlock()
		if err != nil {
			return err
		}
		if marker != nil {
			if err := a.proveChildInputMarker(ctx, id, state, *marker); err != nil {
				return err
			}
		}
		a.stateMu.Lock()
		if !a.currentSessionState(id, state) || state.projection != p || p.failed != nil {
			a.stateMu.Unlock()
			return agent.ErrChildRouteNotReady
		}
		p.classifiedThrough = nativePosition{ID: stored.record.ID, Sequence: stored.record.Sequence}
		a.stateMu.Unlock()
	}
}
