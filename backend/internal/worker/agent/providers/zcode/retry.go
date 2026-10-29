package zcode

import (
	"bytes"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// zcodeCompactionRetry belongs to one explicit compaction generation. Its
// owner may finish before the final state reason; the later event applies the
// captured verdict once, before activity is released.
type zcodeCompactionRetry struct {
	generation uint64
	owner      *zcodeTurnState
	turnEnded  bool
	apply      func()
}

func (a *Agent) bindZCodeCompactionOwnerLocked(owner *zcodeTurnState) {
	if owner == nil || owner.background {
		return
	}
	if retry := a.compactionRetry; retry != nil && retry.generation == a.compactionGeneration && a.compactionSessionID != "" && retry.owner == nil {
		owner.compacting = true
		retry.owner = owner
	}
}

func (a *Agent) captureZCodeRetryVerdict(owner *zcodeTurnState, failure providerkit.TurnFailure, payload []byte) func() {
	writer := a.sink.CaptureAutoRetry()
	raw := bytes.Clone(payload)
	return func() {
		if !owner.compacting {
			providerkit.ApplyTurnFailure(writer, failure, raw)
			return
		}
		apply := func() { providerkit.ApplyTurnFailure(writer, providerkit.CompactionEnd(), nil) }
		a.Mu.Lock()
		if retry := a.compactionRetry; retry != nil && retry.owner == owner && retry.generation == a.compactionGeneration {
			retry.apply = apply
			retry.turnEnded = true
			if a.compactionSessionID != "" {
				a.Mu.Unlock()
				return
			}
			a.compactionRetry = nil
		}
		a.Mu.Unlock()
		apply()
	}
}

func (a *Agent) finishStoppedZCodeRetry(owner *zcodeTurnState, writer agent.AutoRetryWriter, manual bool) {
	failure := providerkit.TurnFailure{}
	if manual || (owner != nil && owner.compacting) {
		failure = providerkit.CompactionEnd()
	}
	a.Mu.Lock()
	if retry := a.compactionRetry; retry != nil && (manual || retry.owner == owner) {
		a.compactionRetry = nil
		a.compactionSessionID = ""
	}
	a.Mu.Unlock()
	providerkit.ApplyTurnFailure(writer, failure, nil)
}
