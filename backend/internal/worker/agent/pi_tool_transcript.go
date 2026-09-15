package agent

import (
	"context"
	"errors"
)

// piToolSource recovers Pi's tool artifacts.
//
// It holds no state: Pi's supplements come out of the message bytes and the files that
// those bytes point at, so every method reads its arguments alone.
type piToolSource struct {
	noopToolSupplementSource
}

func newPiToolTranscript(ctx context.Context, services ProviderServices) *toolTranscript {
	return newToolTranscript(ctx, services, piToolSource{})
}

func (piToolSource) providerName() string { return "Pi" }

func (piToolSource) locate(sessionID string) toolTranscriptLocation {
	// Pi's supplements come out of the message bytes, so this transcript reads no file
	// and states no path.
	return toolTranscriptLocation{sessionKey: sessionID, ready: true}
}

func (piToolSource) readsInitialSupplement() bool { return true }

func (piToolSource) initialSupplement(ctx context.Context, _ string, original []byte, span SpanInfo) ([]byte, error) {
	if !span.Closing {
		return nil, nil
	}
	extra, _, err := recoverPiToolArtifacts(ctx, original, nil)
	return extra, err
}

func (piToolSource) toolCallID(original []byte) string {
	reference := parsePiToolArtifactSource(original)
	if reference != nil && (reference.outputReference().path != "" || reference.mcpResultReference().path != "") {
		return reference.ToolCallID
	}
	return ""
}

func (piToolSource) readSupplements(ctx context.Context, _ string, pending map[string]MessageContent, final bool) (map[string][]byte, error) {
	out := make(map[string][]byte)
	var failures error
	for id, content := range pending {
		extra, complete, err := recoverPiToolArtifacts(ctx, content.Original, content.Supplemental)
		if len(extra) > 0 && (complete || final) {
			out[id] = extra
		}
		failures = errors.Join(failures, err)
	}
	return out, failures
}

func (piToolSource) newChild() toolSupplementSource { return piToolSource{} }
