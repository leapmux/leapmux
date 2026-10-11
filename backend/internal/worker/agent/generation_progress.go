package agent

import (
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const progressCharsPerToken = 4

// ProgressOperation specifies one provider observation for a live counter.
type ProgressOperation uint8

const (
	ProgressModelText ProgressOperation = iota + 1
	ProgressNativeTokens
	ProgressOutputDelta
	ProgressOutputTotal
	ProgressOutputTail
	ProgressModelComplete
	ProgressOutputComplete
	ProgressOutputReset
	ProgressModelReset
	ProgressReset
)

// ProgressUpdate carries one provider observation to the Worker sink.
type ProgressUpdate struct {
	Operation ProgressOperation
	ScopeID   string
	// PreserveModelScopes applies only to a global model reset. ScopeID takes priority.
	PreserveModelScopes []string
	Text                string
	Value               int64
	Minimum             bool
	Exact               bool
	// Truncated states that the observation LOST earlier output. It belongs to
	// ProgressOutputTail alone: a tail says what the tool printed last, and the
	// reader must know when there was more before it.
	Truncated bool
}

func ModelTextProgress(scopeID, text string) ProgressUpdate {
	return ProgressUpdate{Operation: ProgressModelText, ScopeID: scopeID, Text: text}
}

func NativeTokenProgress(scopeID string, tokens int64) ProgressUpdate {
	return ProgressUpdate{Operation: ProgressNativeTokens, ScopeID: scopeID, Value: tokens}
}

func OutputDeltaProgress(scopeID string, bytes int64) ProgressUpdate {
	return ProgressUpdate{Operation: ProgressOutputDelta, ScopeID: scopeID, Value: bytes}
}

func OutputTotalProgress(scopeID string, bytes int64, minimum bool) ProgressUpdate {
	return ProgressUpdate{Operation: ProgressOutputTotal, ScopeID: scopeID, Value: bytes, Minimum: minimum}
}

func OutputExactTotalProgress(scopeID string, bytes int64) ProgressUpdate {
	return ProgressUpdate{Operation: ProgressOutputTotal, ScopeID: scopeID, Value: bytes, Exact: true}
}

// OutputTailProgress carries the last text a running tool printed, so the card
// can show the call's current output while it runs.
//
// Counters produce one aggregate for the agent. This operation carries text for one span.
// scopeID identifies the tool span. The tail belongs only to that span.
//
// The worker does not store live tails in the messages table.
// The final native frame supplies the whole output for the finished row.
func OutputTailProgress(scopeID, tail string, truncated bool) ProgressUpdate {
	return ProgressUpdate{Operation: ProgressOutputTail, ScopeID: scopeID, Text: tail, Truncated: truncated}
}

func CompleteModelProgress(scopeID string) ProgressUpdate {
	return ProgressUpdate{Operation: ProgressModelComplete, ScopeID: scopeID}
}

func CompleteOutputProgress(scopeID string) ProgressUpdate {
	return ProgressUpdate{Operation: ProgressOutputComplete, ScopeID: scopeID}
}

func ResetOutputProgress(scopeID string) ProgressUpdate {
	return ProgressUpdate{Operation: ProgressOutputReset, ScopeID: scopeID}
}

func ResetProgress() ProgressUpdate {
	return ProgressUpdate{Operation: ProgressReset}
}

func ResetModelProgress() ProgressUpdate {
	return ProgressUpdate{Operation: ProgressModelReset}
}

// ResetModelProgressPreservingScopes keeps the supplied scopes during a global model reset.
// The update owns its selector so later caller mutation cannot change it.
func ResetModelProgressPreservingScopes(scopeIDs []string) ProgressUpdate {
	return ProgressUpdate{Operation: ProgressModelReset, PreserveModelScopes: slices.Clone(scopeIDs)}
}

// ResetModelScopeProgress clears one model scope. An invalid target changes nothing.
func ResetModelScopeProgress(scopeID string) ProgressUpdate {
	if strings.TrimSpace(scopeID) == "" || strings.ContainsRune(scopeID, '\x00') {
		return ProgressUpdate{}
	}
	return ProgressUpdate{Operation: ProgressModelReset, ScopeID: scopeID}
}

type progressScope struct {
	modelActive           bool
	modelRetained         int64
	modelChars            int64
	nativeTokens          int64
	outputActive          bool
	outputRetained        int64
	outputBytes           int64
	outputMinimum         bool
	outputRetainedMinimum bool
}

// ProgressSnapshot is the aggregate live counter state for one agent.
type ProgressSnapshot struct {
	ThinkingTokens     int64
	OutputBytes        int64
	OutputBytesMinimum bool
}

// ProgressCounter applies provider observations and produces monotonic counters.
type ProgressCounter struct {
	scopes              map[string]*progressScope
	last                ProgressSnapshot
	outputMinimumScopes int
}

func (c *ProgressCounter) Apply(update ProgressUpdate) (ProgressSnapshot, bool) {
	if update.Operation == ProgressReset {
		c.scopes = nil
		c.outputMinimumScopes = 0
		changed := c.last != (ProgressSnapshot{})
		c.last = ProgressSnapshot{}
		return c.last, changed
	}
	if update.Operation == ProgressModelReset {
		var preserved map[string]struct{}
		if update.ScopeID == "" && len(update.PreserveModelScopes) > 0 {
			preserved = make(map[string]struct{}, len(update.PreserveModelScopes))
			for _, scopeID := range update.PreserveModelScopes {
				preserved[scopeID] = struct{}{}
			}
		}
		for id, scope := range c.scopes {
			if update.ScopeID != "" && update.ScopeID != id {
				continue
			}
			if _, keep := preserved[id]; keep {
				continue
			}
			scope.modelActive = false
			scope.modelRetained = 0
			scope.modelChars = 0
			scope.nativeTokens = 0
			if !scope.hasOutputState() {
				delete(c.scopes, id)
			}
		}
		next := c.snapshot()
		changed := next != c.last
		c.last = next
		return next, changed
	}
	// A tail changes no counter. The publisher reads its text before aggregate calculation.
	// See OutputTailProgress.
	if update.Operation == ProgressOutputTail || update.ScopeID == "" {
		return c.last, false
	}
	if c.scopes == nil {
		c.scopes = make(map[string]*progressScope)
	}
	scope := c.scopes[update.ScopeID]
	if scope == nil {
		scope = &progressScope{}
		c.scopes[update.ScopeID] = scope
	}
	oldModel, oldOutput := scope.currentModelTotal(), scope.currentOutputTotal()
	oldMinimum := scope.hasOutputMinimum()
	switch update.Operation {
	case ProgressModelText:
		if update.Text != "" {
			scope.modelActive = true
			scope.modelChars = SaturatingAdd(scope.modelChars, int64(utf8.RuneCountInString(update.Text)))
		}
	case ProgressNativeTokens:
		if update.Value >= 0 {
			scope.modelActive = true
			if update.Value > scope.nativeTokens {
				scope.nativeTokens = update.Value
			}
		}
	case ProgressOutputDelta:
		if update.Value > 0 {
			scope.outputActive = true
			scope.outputBytes = SaturatingAdd(scope.outputBytes, update.Value)
		}
	case ProgressOutputTotal:
		if update.Value >= 0 {
			scope.outputActive = true
			if update.Exact && update.Value >= scope.outputBytes {
				scope.outputBytes = update.Value
				scope.outputMinimum = false
			} else if update.Exact {
				scope.outputMinimum = true
			} else if update.Value > scope.outputBytes {
				scope.outputBytes = update.Value
			}
			if !update.Exact {
				scope.outputMinimum = scope.outputMinimum || update.Minimum
			}
		}
	case ProgressModelComplete:
		if scope.modelActive {
			scope.modelRetained = SaturatingAdd(scope.modelRetained, scope.currentModelTokens())
			scope.modelChars = 0
			scope.nativeTokens = 0
		}
		scope.modelActive = false
	case ProgressOutputComplete:
		if scope.outputActive {
			scope.outputRetained = SaturatingAdd(scope.outputRetained, scope.outputBytes)
			scope.outputRetainedMinimum = scope.outputRetainedMinimum || scope.outputMinimum
			scope.outputBytes = 0
			scope.outputMinimum = false
		}
		scope.outputActive = false
	case ProgressOutputReset:
		scope.outputActive = false
		scope.outputRetained = 0
		scope.outputBytes = 0
		scope.outputMinimum = false
		scope.outputRetainedMinimum = false
	case ProgressModelReset, ProgressReset:
		return c.last, false
	case ProgressOutputTail:
		// The earlier guard returns before a tail changes any scope.
		// This case keeps the switch exhaustive over ProgressOperation.
		return c.last, false
	}
	var next ProgressSnapshot
	switch update.Operation {
	case ProgressModelComplete:
		c.dropFinishedKind(ProgressModelComplete)
		next = c.snapshot()
	case ProgressOutputComplete, ProgressOutputReset:
		c.dropFinishedKind(ProgressOutputComplete)
		next = c.snapshot()
	default:
		next = c.last
		next.ThinkingTokens = replaceAggregate(next.ThinkingTokens, oldModel, scope.currentModelTotal())
		next.OutputBytes = replaceAggregate(next.OutputBytes, oldOutput, scope.currentOutputTotal())
		newMinimum := scope.hasOutputMinimum()
		if oldMinimum != newMinimum {
			if newMinimum {
				c.outputMinimumScopes++
			} else {
				c.outputMinimumScopes--
			}
		}
		next.OutputBytesMinimum = c.outputMinimumScopes > 0
	}
	changed := next != c.last
	c.last = next
	return next, changed
}

func (c *ProgressCounter) Snapshot() ProgressSnapshot {
	return c.last
}

func (c *ProgressCounter) dropFinishedKind(kind ProgressOperation) {
	active := false
	for _, scope := range c.scopes {
		if kind == ProgressModelComplete && scope.modelActive || kind == ProgressOutputComplete && scope.outputActive {
			active = true
			break
		}
	}
	if active {
		return
	}
	for id, scope := range c.scopes {
		if kind == ProgressModelComplete {
			scope.modelRetained = 0
			scope.modelChars = 0
			scope.nativeTokens = 0
		} else {
			scope.outputRetained = 0
			scope.outputBytes = 0
			scope.outputMinimum = false
			scope.outputRetainedMinimum = false
		}
		if !scope.modelActive && scope.modelRetained == 0 && scope.modelChars == 0 && scope.nativeTokens == 0 && !scope.hasOutputState() {
			delete(c.scopes, id)
		}
	}
}

func (s *progressScope) currentModelTokens() int64 {
	tokens := s.modelChars / progressCharsPerToken
	if s.nativeTokens > tokens {
		return s.nativeTokens
	}
	return tokens
}

func (s *progressScope) currentModelTotal() int64 {
	return SaturatingAdd(s.modelRetained, s.currentModelTokens())
}

func (s *progressScope) currentOutputTotal() int64 {
	return SaturatingAdd(s.outputRetained, s.outputBytes)
}

func (s *progressScope) hasOutputMinimum() bool {
	return s.outputRetainedMinimum || s.outputMinimum
}

func (s *progressScope) hasOutputState() bool {
	return s.outputActive || s.outputRetained != 0 || s.outputBytes != 0 || s.hasOutputMinimum()
}

func replaceAggregate(total, oldValue, newValue int64) int64 {
	if oldValue >= total {
		return newValue
	}
	return SaturatingAdd(total-oldValue, newValue)
}

func (c *ProgressCounter) snapshot() ProgressSnapshot {
	var snapshot ProgressSnapshot
	minimumScopes := 0
	for _, scope := range c.scopes {
		snapshot.ThinkingTokens = SaturatingAdd(snapshot.ThinkingTokens, scope.currentModelTotal())
		snapshot.OutputBytes = SaturatingAdd(snapshot.OutputBytes, scope.currentOutputTotal())
		if scope.hasOutputMinimum() {
			minimumScopes++
		}
	}
	c.outputMinimumScopes = minimumScopes
	snapshot.OutputBytesMinimum = minimumScopes > 0
	return snapshot
}

func SaturatingAdd(left, right int64) int64 {
	if right <= 0 {
		return left
	}
	if left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}

// modelProgressResetTranscript clears model progress at transcript boundaries.
type modelProgressResetTranscript struct {
	TranscriptServices
	progress ProgressServices
}

func (s modelProgressResetTranscript) PersistMessage(source leapmuxv1.MessageSource, content MessageContent, span SpanInfo) error {
	if content.WriteReceipt == nil {
		content.WriteReceipt = NewTranscriptWriteReceipt()
	}
	content = s.CaptureMessage(content, span)
	err := s.TranscriptServices.PersistMessage(source, content, span)
	if err == nil && source == leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT && span.ParentSpanID == "" {
		s.resetModelProgress(content)
	}
	return err
}

func (s modelProgressResetTranscript) PersistNotification(source leapmuxv1.MessageSource, content MessageContent) (bool, error) {
	if content.WriteReceipt == nil {
		content.WriteReceipt = NewTranscriptWriteReceipt()
	}
	content = s.CaptureMessage(content, SpanInfo{})
	broadcast, err := s.TranscriptServices.PersistNotification(source, content)
	if err == nil && broadcast && source == leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT {
		s.resetModelProgress(content)
	}
	return broadcast, err
}

func (s modelProgressResetTranscript) PersistTurnEnd(content MessageContent, span SpanInfo) error {
	if content.WriteReceipt == nil {
		content.WriteReceipt = NewTranscriptWriteReceipt()
	}
	content = s.CaptureMessage(content, span)
	err := s.TranscriptServices.PersistTurnEnd(content, span)
	if err == nil {
		s.resetModelProgress(content)
	}
	return err
}

func (s modelProgressResetTranscript) resetModelProgress(content MessageContent) {
	if content.WriteReceipt != nil && !content.WriteReceipt.ClaimModelReset() {
		return
	}
	if content.Publication != nil {
		content.Publication.ReportProgress(ResetModelProgress())
		return
	}
	s.progress.ReportProgress(ResetModelProgress())
}

type modelProgressResetControl struct {
	ControlServices
	progress ProgressServices
}

func (s modelProgressResetControl) PublishControlRequest(request ControlRequest) error {
	s.progress.ReportProgress(ResetModelProgress())
	return s.ControlServices.PublishControlRequest(request)
}

type modelProgressResetChildren struct{ ChildServices }

func (s modelProgressResetChildren) ChildSink(childAgentID string) ProviderServices {
	return NewModelProgressResetSink(s.ChildServices.ChildSink(childAgentID))
}

// Only the AGENT-source child writes are overridden. See the ChildServices doc on
// PersistChildMessage for why PersistChildPrompt and PersistChildUserMessage are
// not, and what a future USER-source interception would have to change.
func (s modelProgressResetChildren) PersistChildMessage(
	childAgentID string,
	source leapmuxv1.MessageSource,
	content []byte,
	span SpanInfo,
) error {
	return s.ChildSink(childAgentID).PersistMessage(source, MessageContent{Original: content}, span)
}

func (s modelProgressResetChildren) PersistChildTurnEnd(childAgentID string, content MessageContent, span SpanInfo) error {
	return s.ChildSink(childAgentID).PersistTurnEnd(content, span)
}

func NewModelProgressResetSink(inner ProviderServices) ProviderServices {
	return providerServices{
		TranscriptServices: modelProgressResetTranscript{
			TranscriptServices: inner,
			progress:           inner,
		},
		TurnServices:     inner,
		SpanServices:     inner,
		ProgressServices: inner,
		ControlServices: modelProgressResetControl{
			ControlServices: inner,
			progress:        inner,
		},
		SessionServices:        inner,
		PlanServices:           inner,
		GoalServices:           inner,
		AutoContinueServices:   inner,
		ChildServices:          modelProgressResetChildren{ChildServices: inner},
		BackgroundTaskServices: inner,
	}
}
