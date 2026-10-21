package providerkit

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// DiscardGenerationScopes removes the selected scopes and resets their live progress.
func DiscardGenerationScopes(buffer *GenerationBuffer, progress agent.ProgressServices, scopeIDs []string) {
	for _, scopeID := range scopeIDs {
		buffer.Discard(scopeID)
		progress.ReportProgress(agent.ResetModelScopeProgress(scopeID))
	}
}

// PersistGenerationScopes writes the selected scopes in order and completes their progress.
// Each selected scope becomes an immutable pending write before the sink receives any row.
func PersistGenerationScopes(buffer *GenerationBuffer, sink GenerationServices, scopeIDs []string, completion agent.MessageCompletion, discarding bool) error {
	if discarding {
		DiscardGenerationScopes(buffer, sink, scopeIDs)
		return nil
	}
	buffer.bindOutput(sink)
	destination := generationDestination{writer: agent.DirectTranscript(sink)}
	defer func() {
		for _, scopeID := range scopeIDs {
			update := agent.CompleteModelProgress(scopeID)
			if destination.captured && destination.template.Publication != nil {
				destination.template.Publication.ReportProgress(update)
			} else {
				sink.ReportProgress(update)
			}
		}
	}()
	_, err := buffer.persistScopes(scopeIDs, completion, destination.prepare)
	return err
}

// FlushGeneration persists or discards the current scopes in first-seen order.
func FlushGeneration(buffer *GenerationBuffer, sink GenerationServices, completion agent.MessageCompletion, discarding bool) error {
	return PersistGenerationScopes(buffer, sink, buffer.ScopeIDs(), completion, discarding)
}
