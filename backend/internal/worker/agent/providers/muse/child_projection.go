package muse

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const (
	childProjectionTimerTag   = "muse-child-projection"
	maxChildProjectionBytes   = 8 << 20
	childRunStartedKind       = "started"
	childContextUpdatedKind   = "context_block_updated"
	childInputApplicationKind = "subagent_input_application_v1"
	childDeveloperRole        = "developer"
)

type childProjectionFrame struct {
	raw      []byte
	serial   uint64
	deadline time.Time
	metadata *childProjectionMetadata
}

// Metadata derives only from the immutable native frame. Parsing it once avoids
// rescanning a complete item for each delta that the item supersedes.
type childProjectionMetadata struct {
	method     string
	sessionID  string
	itemID     string
	revision   int64
	controlKey string
	hasID      bool
	cursor     *string
	anchor     *nativeSourceRange
	err        error
}

func readChildProjectionMetadata(raw []byte) *childProjectionMetadata {
	line := providerkit.ParseLine(raw)
	m := &childProjectionMetadata{method: line.Method, hasID: line.HasID()}
	var params struct {
		SessionID string  `json:"sessionId"`
		Cursor    *string `json:"viewCursor"`
	}
	if json.Unmarshal(line.Params, &params) != nil {
		m.err = fmt.Errorf("the Muse child view parameters are invalid")
		return m
	}
	m.sessionID, m.cursor = params.SessionID, params.Cursor
	m.anchor, m.err = childProjectionAnchor(raw)
	if m.err != nil {
		return m
	}
	switch line.Method {
	case contracts.MuseMethodItemStarted, contracts.MuseMethodItemUpdated, contracts.MuseMethodItemCompleted:
		var full itemParams
		if json.Unmarshal(line.Params, &full) != nil || full.Item.ID == "" || full.Item.Revision <= 0 || full.Item.Kind == "" {
			m.err = fmt.Errorf("the Muse child item frame is invalid")
			return m
		}
		m.itemID, m.revision = full.Item.ID, full.Item.Revision
	case contracts.MuseMethodItemDelta:
		var delta struct {
			ItemID string `json:"itemId"`
			Field  string `json:"field"`
			Delta  string `json:"delta"`
		}
		if json.Unmarshal(line.Params, &delta) != nil || delta.ItemID == "" {
			m.err = fmt.Errorf("the Muse child delta frame is invalid")
			return m
		}
		m.itemID = delta.ItemID
	}
	if childControlPublication(line.Method) {
		var control controlParams
		if json.Unmarshal(line.Params, &control) != nil {
			m.err = fmt.Errorf("the Muse child control frame is invalid")
			return m
		}
		m.controlKey, _ = controlID(control)
	}
	return m
}

// The caller holds stateMu when the frame belongs to a projection queue.
func (f *childProjectionFrame) identity() *childProjectionMetadata {
	if f.metadata == nil {
		f.metadata = readChildProjectionMetadata(f.raw)
	}
	return f.metadata
}

type childProjectionHead struct {
	items    []json.RawMessage
	cursor   string
	after    uint64
	reached  bool
	deadline time.Time
	bytes    int
}

type childProjection struct {
	frames             []childProjectionFrame
	heads              []*childProjectionHead
	bufferedBytes      int
	enqueued           uint64
	lastConsumedSerial uint64
	lastConsumedCursor *string
	classifiedThrough  nativePosition
	changed            chan struct{}
	failed             error
}

type childInputMarker struct {
	position nativePosition
	runID    string
	inboxID  string
	runStart bool
}

func newChildProjection() *childProjection {
	return &childProjection{changed: make(chan struct{})}
}

// The caller holds stateMu.
func (p *childProjection) signal() {
	close(p.changed)
	p.changed = make(chan struct{})
}

func (a *Agent) childProofTimeout() time.Duration {
	if timeout := a.APITimeout(); timeout > 0 {
		return timeout
	}
	return 30 * time.Second
}

// The caller holds dispatcher.mu. Persistence runs after dispatch unlocks.
func (a *Agent) queueChildProjectionLocked(id string, state *sessionState, raw []byte) {
	a.queueChildProjectionBeforeLocked(id, state, raw, a.Clock().Now().Add(a.childProofTimeout()))
}

func (a *Agent) queueChildProjectionBeforeLocked(id string, state *sessionState, raw []byte, deadline time.Time) {
	a.stateMu.Lock()
	if state.projection == nil {
		state.projection = newChildProjection()
	}
	p := state.projection
	if p.failed != nil || !a.currentSessionState(id, state) {
		writer, frozen := a.sink, slices.Clone(raw)
		a.stateMu.Unlock()
		a.deferOutputAction(func() { persistUnprojectedViewFrames(writer, id, [][]byte{frozen}) })
		return
	}
	if len(raw) > maxChildProjectionBytes-p.bufferedBytes {
		a.stateMu.Unlock()
		a.failChildProjectionLocked(id, state, fmt.Errorf("the Muse child projection exceeds its memory limit"), raw)
		return
	}
	p.enqueued++
	metadata := readChildProjectionMetadata(raw)
	wake := len(p.frames) == 0 || metadata.anchor != nil || metadata.err != nil || childBookkeepingMethod(metadata.method)
	p.frames = append(p.frames, childProjectionFrame{raw: slices.Clone(raw), serial: p.enqueued, deadline: deadline, metadata: metadata})
	p.bufferedBytes += len(raw)
	if wake {
		p.signal()
	}
	a.stateMu.Unlock()
	if wake {
		a.viewWork.Run(id, func() { a.syncView(id) })
	}
}

// A head waits for all preceding frames and its actual coherent cursor.
// The caller holds dispatcher.mu. Empty history supplies its cursor without a frame.
func (a *Agent) queueChildProjectionHeadLocked(id string, state *sessionState, start childViewStart) {
	if len(start.registryItems) == 0 {
		return
	}
	if start.head == nil {
		a.failChildProjectionLocked(id, state, fmt.Errorf("the Muse registry head has no native cursor"), nil)
		return
	}
	size := 0
	for _, raw := range start.registryItems {
		if len(raw) > maxChildProjectionBytes-size {
			a.failChildProjectionLocked(id, state, fmt.Errorf("the Muse registry head exceeds its memory limit"), nil)
			return
		}
		size += len(raw)
	}
	a.stateMu.Lock()
	if state.projection == nil {
		state.projection = newChildProjection()
	}
	p := state.projection
	if p.failed != nil {
		a.stateMu.Unlock()
		return
	}
	if size > maxChildProjectionBytes-p.bufferedBytes {
		a.stateMu.Unlock()
		a.failChildProjectionLocked(id, state, fmt.Errorf("the Muse child projection exceeds its memory limit"), nil)
		return
	}
	items := make([]json.RawMessage, len(start.registryItems))
	for i, raw := range start.registryItems {
		items[i] = slices.Clone(raw)
	}
	deadline := start.deadline
	if deadline.IsZero() {
		deadline = a.Clock().Now().Add(a.childProofTimeout())
	}
	reached := start.empty || state.acceptedViewCursor != nil && *state.acceptedViewCursor == *start.head
	p.heads = append(p.heads, &childProjectionHead{
		items: items, cursor: *start.head, after: p.enqueued, reached: reached,
		deadline: deadline, bytes: size,
	})
	p.bufferedBytes += size
	p.signal()
	a.stateMu.Unlock()
	a.viewWork.Run(id, func() { a.syncView(id) })
}

// The caller holds dispatcher.mu. Only genuine frames become diagnostic rows.
func (a *Agent) failChildProjectionLocked(id string, state *sessionState, reason error, extra []byte) {
	a.stateMu.Lock()
	if state.projection == nil {
		state.projection = newChildProjection()
	}
	p := state.projection
	first := p.failed == nil
	p.failed = reason
	if state.nativeHead != nil {
		state.nativeHead.collecting = false
	}
	frames := make([][]byte, 0, len(p.frames)+1)
	for _, frame := range p.frames {
		frames = append(frames, frame.raw)
	}
	// A proved unfinished item may have superseded its ephemeral frames without
	// obtaining a transcript row. Keep its genuine bytes when projection fails.
	for _, item := range state.items {
		if provedUnfinishedChildText(id, item) && item.retainedRevision < item.params.Item.Revision && !slices.ContainsFunc(frames, func(raw []byte) bool { return slices.Equal(raw, item.raw) }) {
			frames = append(frames, slices.Clone(item.raw))
		}
	}
	if len(extra) > 0 {
		frames = append(frames, slices.Clone(extra))
	}
	p.frames, p.heads = nil, nil
	p.bufferedBytes = 0
	p.signal()
	state.childViewPrefix = childViewPrefixFailed
	state.viewOverlap = nil
	writer := a.sink
	a.stateMu.Unlock()
	a.publishChildNativeStateLocked(id, state)
	var warn func()
	if first {
		warn = a.captureChildViewWarningLocked(id, state, reason)
	}
	a.deferOutputAction(func() {
		persistUnprojectedViewFrames(writer, id, frames)
		if warn != nil {
			warn()
		}
	})
}

func (a *Agent) failChildProjection(id string, state *sessionState, reason error) {
	a.dispatcher.mu.Lock()
	a.failChildProjectionLocked(id, state, reason, nil)
	a.dispatcher.mu.Unlock()
	a.drainDeferredOutputActions()
	a.drainControls()
}

func childProjectionAnchor(raw []byte) (*nativeSourceRange, error) {
	var event struct {
		Params struct {
			SessionID string             `json:"sessionId"`
			Source    *nativeSourceRange `json:"sourceRange"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return nil, fmt.Errorf("the Muse child view frame is invalid")
	}
	source := event.Params.Source
	if source == nil {
		return nil, nil
	}
	if source.Stream != (nativeStream{Kind: contracts.MuseStreamKindSession, ID: event.Params.SessionID}) || source.First.ID == "" || source.Last.ID == "" || source.First.Sequence <= 0 || source.Last.Sequence < source.First.Sequence {
		return nil, fmt.Errorf("the Muse child view frame has an invalid source range")
	}
	return source, nil
}

// The caller holds stateMu. The segment owns its slice of queue entries.
func nextChildProjectionSegment(p *childProjection) ([]childProjectionFrame, *nativeSourceRange, error) {
	for index := range p.frames {
		frame := &p.frames[index]
		metadata := frame.identity()
		anchor := metadata.anchor
		if metadata.err != nil {
			return nil, nil, metadata.err
		}
		// An item's ephemeral frames have no source position. Only that item's
		// authoritative completion can replace them; an unrelated row proves none.
		if anchor != nil {
			return []childProjectionFrame{*frame}, anchor, nil
		}
		if childBookkeepingMethod(metadata.method) {
			return []childProjectionFrame{*frame}, nil, nil
		}
	}
	return nil, nil, nil
}

func childBookkeepingMethod(method string) bool {
	switch method {
	case methodSessionModelChanged, methodSessionEffortChanged, methodSessionApprovalChanged, methodSessionGoalChanged,
		methodSessionStatusChanged, methodSessionStarted, methodSessionClosed, methodSessionBranchChanged, methodSessionNameChanged,
		methodSessionPermissionProfileChanged, methodSessionViewHealthChanged, methodTurnForegroundCompleted,
		methodSkillChanged, methodAccountChanged, methodAccountLoginCompleted, methodMcpOAuthLoginCompleted, methodSessionListChanged:
		return true
	default:
		return false
	}
}

// supersededChildEphemeral accepts only an exact native item or control identity.
// The complete durable item replaces its earlier deltas without replaying them.
func supersededChildEphemeral(before, after *childProjectionMetadata) bool {
	if (after.method == contracts.MuseMethodItemCompleted || after.method == contracts.MuseMethodItemUpdated || after.method == contracts.MuseMethodItemStarted) && (before.method == contracts.MuseMethodItemDelta || before.method == contracts.MuseMethodItemStarted) {
		return after.itemID != "" && after.revision > 0 && before.itemID == after.itemID && before.sessionID == after.sessionID && before.revision <= after.revision
	}
	return before.hasID && childControlPublication(before.method) && childControlPublication(after.method) && before.controlKey != "" && before.controlKey == after.controlKey
}

func childControlPublication(method string) bool {
	switch method {
	case contracts.MuseMethodApprovalRequest, contracts.MuseMethodUserInputRequest, contracts.MuseMethodApprovalRequested, contracts.MuseMethodApprovalUpdated, contracts.MuseMethodUserInputRequested:
		return true
	default:
		return false
	}
}

// The caller holds dispatcher.mu. Publication waits for child input commits.
func (a *Agent) queueProvedChildControlLocked(id string, state *sessionState, raw []byte) error {
	writer := state.sink
	queue := agent.OutputQueueFor(writer)
	if queue == nil {
		return fmt.Errorf("the Muse child control has no output queue")
	}
	diagnostics, frozen := a.sink, slices.Clone(raw)
	a.deferOutputAction(func() {
		queue.Append(agent.OutputWrite{Label: "publish a proved Muse child control", Run: func() error {
			if err := a.dispatcher.checkPublicationLocks(); err != nil {
				return err
			}
			a.dispatcher.mu.Lock()
			a.stateMu.Lock()
			current := a.currentSessionState(id, state)
			a.stateMu.Unlock()
			if current {
				a.publishControl(providerkit.ParseLine(frozen))
			}
			a.dispatcher.mu.Unlock()
			if !current {
				persistUnprojectedViewFrames(diagnostics, id, [][]byte{frozen})
				return nil
			}
			a.drainControls()
			return nil
		}})
		if err := queue.Flush(); err != nil {
			slog.Debug("retain the proved Muse child control", "session", id, "error", err)
		}
	})
	return nil
}

// Metadata cannot suppress genuine preceding revisions.
func (a *Agent) reconcileReadyChildHeads(id string, state *sessionState) error {
	for {
		a.dispatcher.mu.Lock()
		a.stateMu.Lock()
		if !a.currentSessionState(id, state) || state.projection == nil || state.projection.failed != nil {
			a.stateMu.Unlock()
			a.dispatcher.mu.Unlock()
			return agent.ErrChildRouteNotReady
		}
		p := state.projection
		if len(p.heads) == 0 {
			a.stateMu.Unlock()
			a.dispatcher.mu.Unlock()
			return nil
		}
		head := p.heads[0]
		if slices.ContainsFunc(p.frames, func(frame childProjectionFrame) bool { return frame.serial <= head.after }) || !head.reached {
			a.stateMu.Unlock()
			a.dispatcher.mu.Unlock()
			return nil
		}
		a.stateMu.Unlock()
		err := a.installRegistryHeadLocked(id, state, head.items)
		if err != nil {
			a.dispatcher.mu.Unlock()
			a.drainDeferredOutputActions()
			a.drainControls()
			return err
		}
		a.stateMu.Lock()
		if state.projection == p && p.failed == nil && len(p.heads) > 0 && p.heads[0] == head {
			p.heads[0] = nil
			p.heads = p.heads[1:]
			p.bufferedBytes -= head.bytes
			if len(p.heads) == 0 {
				p.heads = nil
			}
			p.signal()
		}
		a.stateMu.Unlock()
		a.dispatcher.mu.Unlock()
		a.drainDeferredOutputActions()
		a.drainControls()
	}
}

func (a *Agent) applyChildProjectionSegment(id string, state *sessionState, segment []childProjectionFrame) error {
	a.dispatcher.mu.Lock()
	a.stateMu.Lock()
	if !a.currentSessionState(id, state) || state.projection == nil || state.projection.failed != nil || len(state.projection.frames) < len(segment) {
		a.stateMu.Unlock()
		a.dispatcher.mu.Unlock()
		return agent.ErrChildRouteNotReady
	}
	p := state.projection
	a.stateMu.Unlock()
	for _, frame := range segment {
		line := providerkit.ParseLine(frame.raw)
		if childControlPublication(line.Method) {
			if err := a.queueProvedChildControlLocked(id, state, frame.raw); err != nil {
				a.dispatcher.mu.Unlock()
				a.drainDeferredOutputActions()
				a.drainControls()
				return err
			}
		} else {
			a.applySessionOutput(line, state)
		}

	}
	a.stateMu.Lock()
	if state.projection == p && p.failed == nil {
		remaining := p.frames[:0]
		revisions := make(map[string]int64)
		for index := range p.frames {
			pending := p.frames[index]
			before := *pending.identity()
			if before.sessionID == id && before.itemID != "" {
				revision, seen := revisions[before.itemID]
				if !seen {
					if item := state.items[before.itemID]; item != nil {
						revision = item.params.Item.Revision
					}
				}
				revision = max(revision, before.revision)
				revisions[before.itemID] = revision
				// A delta belongs to the latest preceding native revision of its item.
				// An older full item cannot replace text from that newer revision.
				if before.method == contracts.MuseMethodItemDelta {
					before.revision = revision
				}
			}
			consumed := slices.ContainsFunc(segment, func(projected childProjectionFrame) bool {
				return pending.serial == projected.serial || pending.serial < projected.serial && supersededChildEphemeral(&before, projected.identity())
			})
			if consumed {
				if pending.metadata.cursor != nil {
					cursor := *pending.metadata.cursor
					if pending.serial > p.lastConsumedSerial {
						p.lastConsumedSerial, p.lastConsumedCursor = pending.serial, &cursor
					}
					for _, head := range p.heads {
						if head.cursor == cursor {
							head.reached = true
						}
					}
				}
				p.bufferedBytes -= len(pending.raw)
			} else {
				remaining = append(remaining, pending)
			}
		}
		clear(p.frames[len(remaining):])
		p.frames = remaining
		if p.lastConsumedCursor != nil && !slices.ContainsFunc(p.frames, func(frame childProjectionFrame) bool { return frame.serial < p.lastConsumedSerial }) {
			cursor := *p.lastConsumedCursor
			state.acceptedViewCursor = &cursor
		}
		if len(p.frames) == 0 {
			p.frames = nil
		}
		p.signal()
	}
	a.stateMu.Unlock()
	a.dispatcher.mu.Unlock()
	a.drainDeferredOutputActions()
	a.drainControls()
	return a.reconcileReadyChildHeads(id, state)
}

// The existing viewWork key owns the only projection consumer for this child.
func (a *Agent) projectChildView(id string, state *sessionState) {
	for {
		a.stateMu.Lock()
		p := state.projection
		if p != nil && p.failed != nil {
			a.stateMu.Unlock()
			return
		}
		if p == nil || len(p.frames) == 0 && len(p.heads) == 0 {
			a.stateMu.Unlock()
			a.dispatcher.mu.Lock()
			a.publishChildNativeStateLocked(id, state)
			a.dispatcher.mu.Unlock()
			a.drainDeferredOutputActions()
			return
		}
		current := a.currentSessionState(id, state)
		deadline := a.Clock().Now().Add(a.childProofTimeout())
		for _, frame := range p.frames {
			if !frame.deadline.IsZero() && frame.deadline.Before(deadline) {
				deadline = frame.deadline
			}
		}
		for _, head := range p.heads {
			if head.deadline.Before(deadline) {
				deadline = head.deadline
			}
		}
		a.stateMu.Unlock()
		if !current {
			a.failChildProjection(id, state, agent.ErrChildRouteNotReady)
			return
		}
		ctx, finish := a.newChildProofContext(deadline)
		err := a.projectNextChildSegment(ctx, id, state)
		finish()
		if err != nil {
			a.failChildProjection(id, state, err)
			return
		}
	}
}

func (a *Agent) projectNextChildSegment(ctx context.Context, id string, state *sessionState) error {
	for {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if err := a.reconcileReadyChildHeads(id, state); err != nil {
			return err
		}
		a.stateMu.Lock()
		if !a.currentSessionState(id, state) || state.projection == nil || state.projection.failed != nil {
			a.stateMu.Unlock()
			return agent.ErrChildRouteNotReady
		}
		p := state.projection
		if len(p.frames) == 0 && len(p.heads) == 0 {
			a.stateMu.Unlock()
			return nil
		}
		segment, anchor, err := nextChildProjectionSegment(p)
		changed := p.changed
		a.stateMu.Unlock()
		if err != nil {
			return err
		}
		if len(segment) == 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("the Muse child projection has no proved durable continuation: %w", context.Cause(ctx))
			case <-changed:
				continue
			}
		}
		if anchor != nil {
			if err := a.proveChildInputsThrough(ctx, id, state, anchor.Last); err != nil {
				return err
			}
			a.stateMu.Lock()
			valid := a.currentSessionState(id, state) && state.log != nil && state.log.sequences[anchor.First.Sequence] == anchor.First.ID
			a.stateMu.Unlock()
			if !valid {
				return fmt.Errorf("the Muse child view source does not match its native prefix")
			}
		}
		return a.applyChildProjectionSegment(id, state, segment)
	}
}
