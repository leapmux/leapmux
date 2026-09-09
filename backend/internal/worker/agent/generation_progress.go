package agent

import (
	"math"
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
	ProgressModelComplete
	ProgressOutputComplete
	ProgressModelReset
	ProgressReset
)

// ProgressUpdate carries one provider observation to the Worker sink.
type ProgressUpdate struct {
	Operation ProgressOperation
	ScopeID   string
	Text      string
	Value     int64
	Minimum   bool
	Exact     bool
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

func CompleteModelProgress(scopeID string) ProgressUpdate {
	return ProgressUpdate{Operation: ProgressModelComplete, ScopeID: scopeID}
}

func CompleteOutputProgress(scopeID string) ProgressUpdate {
	return ProgressUpdate{Operation: ProgressOutputComplete, ScopeID: scopeID}
}

func ResetProgress() ProgressUpdate {
	return ProgressUpdate{Operation: ProgressReset}
}

func ResetModelProgress() ProgressUpdate {
	return ProgressUpdate{Operation: ProgressModelReset}
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
	scopes map[string]*progressScope
	last   ProgressSnapshot
}

func (c *ProgressCounter) Apply(update ProgressUpdate) (ProgressSnapshot, bool) {
	if update.Operation == ProgressReset {
		c.scopes = nil
		changed := c.last != (ProgressSnapshot{})
		c.last = ProgressSnapshot{}
		return c.last, changed
	}
	if update.Operation == ProgressModelReset {
		for id, scope := range c.scopes {
			scope.modelActive = false
			scope.modelRetained = 0
			scope.modelChars = 0
			scope.nativeTokens = 0
			if !scope.outputActive && scope.outputRetained == 0 && scope.outputBytes == 0 {
				delete(c.scopes, id)
			}
		}
		next := c.snapshot()
		changed := next != c.last
		c.last = next
		return next, changed
	}
	if update.ScopeID == "" {
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
	switch update.Operation {
	case ProgressModelText:
		if update.Text != "" {
			scope.modelActive = true
			scope.modelChars = saturatingAdd(scope.modelChars, int64(utf8.RuneCountInString(update.Text)))
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
			scope.outputBytes = saturatingAdd(scope.outputBytes, update.Value)
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
			scope.modelRetained = saturatingAdd(scope.modelRetained, scope.currentModelTokens())
			scope.modelChars = 0
			scope.nativeTokens = 0
		}
		scope.modelActive = false
	case ProgressOutputComplete:
		if scope.outputActive {
			scope.outputRetained = saturatingAdd(scope.outputRetained, scope.outputBytes)
			scope.outputRetainedMinimum = scope.outputRetainedMinimum || scope.outputMinimum
			scope.outputBytes = 0
			scope.outputMinimum = false
		}
		scope.outputActive = false
	case ProgressModelReset, ProgressReset:
		return c.last, false
	}
	c.dropFinishedKind(ProgressModelComplete)
	c.dropFinishedKind(ProgressOutputComplete)
	next := c.snapshot()
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
		if !scope.modelActive && !scope.outputActive && scope.modelRetained == 0 && scope.modelChars == 0 &&
			scope.nativeTokens == 0 && scope.outputRetained == 0 && scope.outputBytes == 0 {
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

func (c *ProgressCounter) snapshot() ProgressSnapshot {
	var snapshot ProgressSnapshot
	for _, scope := range c.scopes {
		snapshot.ThinkingTokens = saturatingAdd(snapshot.ThinkingTokens, scope.modelRetained)
		snapshot.ThinkingTokens = saturatingAdd(snapshot.ThinkingTokens, scope.currentModelTokens())
		snapshot.OutputBytes = saturatingAdd(snapshot.OutputBytes, scope.outputRetained)
		snapshot.OutputBytes = saturatingAdd(snapshot.OutputBytes, scope.outputBytes)
		snapshot.OutputBytesMinimum = snapshot.OutputBytesMinimum || scope.outputRetainedMinimum || scope.outputMinimum
	}
	return snapshot
}

func saturatingAdd(left, right int64) int64 {
	if right <= 0 {
		return left
	}
	if left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}

// modelProgressResetSink clears model progress at visible message boundaries.
type modelProgressResetSink struct{ OutputSink }

func newModelProgressResetSink(inner OutputSink) *modelProgressResetSink {
	return &modelProgressResetSink{OutputSink: inner}
}

func (s *modelProgressResetSink) PersistMessage(source leapmuxv1.MessageSource, content []byte, span SpanInfo) error {
	if source == leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT && span.ParentSpanID == "" {
		s.ReportProgress(ResetModelProgress())
	}
	return s.OutputSink.PersistMessage(source, content, span)
}

func (s *modelProgressResetSink) PersistNotification(source leapmuxv1.MessageSource, content []byte) (bool, error) {
	broadcast, err := s.OutputSink.PersistNotification(source, content)
	if err == nil && broadcast && source == leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT {
		s.ReportProgress(ResetModelProgress())
	}
	return broadcast, err
}

func (s *modelProgressResetSink) PersistTurnEnd(content []byte, span SpanInfo) error {
	s.ReportProgress(ResetModelProgress())
	return s.OutputSink.PersistTurnEnd(content, span)
}

func (s *modelProgressResetSink) BroadcastControlRequest(requestID string, payload []byte, claimToken string) {
	s.ReportProgress(ResetModelProgress())
	s.OutputSink.BroadcastControlRequest(requestID, payload, claimToken)
}
