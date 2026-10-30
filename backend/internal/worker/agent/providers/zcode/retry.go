package zcode

import (
	"bytes"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// zcodeCompactionRetry joins one manual request's native turn and final state.
// Admissions retain this record after completion until a delayed start consumes
// it, so that start cannot produce a second verdict or become an ordinary turn.
type zcodeCompactionRetry struct {
	generation        uint64
	inputID           string
	admissionRevision int64
	receiptState      string
	pendingFinal      *zcodeStatePatch
	owner             *zcodeTurnState
	turnEnded         bool
	stateEnded        bool
	verdictApplied    bool
	superseded        bool
}

func (a *Agent) bindZCodeCompactionOwnerLocked(owner *zcodeTurnState) {
	if owner == nil || owner.background {
		return
	}
	retry := owner.compaction
	if retry == nil && a.compactionRetry != nil && a.compactionRetry.inputID == owner.inputID {
		retry = a.compactionRetry
	}
	if retry == nil {
		return
	}
	owner.compacting = true
	owner.compaction = retry
	retry.owner = owner
	if retry == a.compactionRetry && !retry.stateEnded && !retry.verdictApplied && !retry.superseded {
		// An uncertain receipt released admission, but this matching native start
		// proves the manual work exists and restores its activity lifetime.
		a.compactionSessionID = a.sessionID
	}
}

// claimZCodeCompactionRetryLocked consumes one current operation's verdict.
// The caller holds Mu and dispatchMu, and captures its writer before publishing
// inactivity. A superseding foreground retains its own retry authority.
func (a *Agent) claimZCodeCompactionRetryLocked(retry *zcodeCompactionRetry) bool {
	if retry == nil || retry != a.compactionRetry || !retry.stateEnded || (retry.owner != nil && !retry.turnEnded) {
		return false
	}
	a.compactionRetry = nil
	if retry.verdictApplied {
		return false
	}
	retry.verdictApplied = true
	return !retry.superseded
}

// captureZCodeRetryVerdict retains immutable native evidence. Settlement states
// whether it claimed the current foreground; only that owner may apply retry
// effects. Historical manual owners still retire their native completion record.
func (a *Agent) captureZCodeRetryVerdict(owner *zcodeTurnState, failure providerkit.TurnFailure, payload []byte) func(bool) {
	a.Mu.Lock()
	identity := a.captureZCodeObservationIdentityLocked()
	compacting, compaction := owner.compacting, owner.compaction
	a.Mu.Unlock()
	raw := bytes.Clone(payload)
	return func(foreground bool) {
		a.Mu.Lock()
		if !a.zcodeSessionLifetimeCurrentLocked(identity) || a.ProcessExitedLocked() {
			a.Mu.Unlock()
			return
		}
		if compaction != nil {
			compaction.turnEnded = true
			if !foreground {
				compaction.superseded = true
			}
			apply := a.claimZCodeCompactionRetryLocked(compaction)
			if !foreground || !apply {
				a.Mu.Unlock()
				return
			}
			failure = providerkit.CompactionEnd()
		} else {
			if !foreground {
				a.Mu.Unlock()
				return
			}
			if compacting {
				failure = providerkit.CompactionEnd()
			} else if a.IntentionalStopRequested() {
				// Shutdown can begin while canonical recovery waits. Keep cancellation
				// bookkeeping, but never start another retry during that shutdown.
				failure = providerkit.TurnFailure{}
			}
		}
		a.Mu.Unlock()
		// Capture before queueing the effect. A background publication during
		// canonical recovery is harmless; a later foreground rejects retained work.
		providerkit.ApplyTurnFailure(a.sink.CaptureAutoRetry(), failure, raw)
	}
}

func (a *Agent) finishStoppedZCodeRetry(owner *zcodeTurnState, writer agent.AutoRetryWriter, manual bool) {
	failure := providerkit.TurnFailure{}
	if manual || (owner != nil && owner.compacting) {
		failure = providerkit.CompactionEnd()
	}
	a.Mu.Lock()
	var retry *zcodeCompactionRetry
	if manual {
		retry = a.compactionRetry
	} else if owner != nil {
		retry = owner.compaction
	}
	if retry != nil {
		if retry != a.compactionRetry || retry.verdictApplied {
			a.Mu.Unlock()
			return
		}
		retry.turnEnded, retry.verdictApplied = true, true
		// Local settlement does not prove the native final state arrived. Retain
		// this identity until it does, so an older state cannot finish a new request.
		if retry.stateEnded {
			a.compactionRetry = nil
		}
		if retry.generation == a.compactionGeneration {
			a.compactionSessionID = ""
		}
		if retry.superseded {
			a.Mu.Unlock()
			return
		}
	}
	a.Mu.Unlock()
	providerkit.ApplyTurnFailure(writer, failure, nil)
}
