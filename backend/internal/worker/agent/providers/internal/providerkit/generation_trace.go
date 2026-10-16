package providerkit

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/phasetrace"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

var generationTracer = phasetrace.New(contracts.GenerationTraceEnv, contracts.GenerationTraceMarker, "generation buffer receipt")

type generationDigest struct {
	hasher hash.Hash
	bytes  int
}

type generationTraceSnapshot struct {
	digest [sha256.Size]byte
	bytes  int
}

// capture hashes only new bytes while the generation buffer holds its lock.
// The text includes any paragraph separator that AppendText inserted.
func (state *generationDigest) capture(text string) generationTraceSnapshot {
	if state.hasher == nil {
		state.hasher = sha256.New()
	}
	_, _ = state.hasher.Write([]byte(text[state.bytes:])) // hash.Hash.Write never returns an error.
	state.bytes = len(text)
	snapshot := generationTraceSnapshot{bytes: state.bytes}
	state.hasher.Sum(snapshot.digest[:0])
	return snapshot
}

// captureGenerationSnapshot allocates no hash state when tracing is disabled.
func captureGenerationSnapshot(segment *generationSegment) generationTraceSnapshot {
	if !generationTracer.Enabled() {
		return generationTraceSnapshot{}
	}
	if segment.trace == nil {
		segment.trace = &generationDigest{}
	}
	return segment.trace.capture(segment.text.String())
}

// traceGenerationSnapshot logs the immutable receipt after the buffer releases its lock.
func traceGenerationSnapshot(scope string, kind agent.AssembledMessageKind, snapshot generationTraceSnapshot) {
	generationTracer.LogLazy(func() []any {
		return []any{
			contracts.GenerationTraceFieldScope, scope,
			contracts.GenerationTraceFieldKind, string(kind),
			contracts.GenerationTraceFieldSHA256, hex.EncodeToString(snapshot.digest[:]),
			contracts.GenerationTraceFieldBytes, snapshot.bytes,
		}
	})
}
