package providerkit

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/phasetrace"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

var generationTracer = phasetrace.New(contracts.GenerationTraceEnv, contracts.GenerationTraceMarker, "generation buffer receipt")

// traceGenerationSnapshot records a receipt after buffering native text, without exposing the text itself.
func traceGenerationSnapshot(scope string, kind agent.AssembledMessageKind, text string) {
	generationTracer.LogLazy(func() []any {
		digest := sha256.Sum256([]byte(text))
		return []any{
			contracts.GenerationTraceFieldScope, scope,
			contracts.GenerationTraceFieldKind, string(kind),
			contracts.GenerationTraceFieldSHA256, hex.EncodeToString(digest[:]),
			contracts.GenerationTraceFieldBytes, len(text),
		}
	})
}
