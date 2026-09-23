package pi

import (
	"context"
	"errors"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/tooltranscript"
)

// piToolSource recovers Pi's tool artifacts.
//
// It holds no state: Pi's supplements come out of the message bytes and the files that
// those bytes point at, so every method reads its arguments alone.
type piToolSource struct {
	tooltranscript.SourceDefaults
}

func newPiToolTranscript(ctx context.Context, services agent.ProviderServices) *tooltranscript.Transcript {
	return tooltranscript.New(ctx, services, piToolSource{})
}

func (piToolSource) ProviderName() string { return "Pi" }

func (piToolSource) Locate(sessionID string) tooltranscript.Location {
	// Pi's supplements come out of the message bytes, so this transcript reads no file
	// and states no path.
	return tooltranscript.Location{SessionKey: sessionID, Ready: true}
}

func (piToolSource) InitialSupplement(ctx context.Context, _ string, original []byte, span agent.SpanInfo) ([]byte, error) {
	if !span.Closing {
		return nil, nil
	}
	extra, _, err := recoverPiToolArtifacts(ctx, original, nil)
	return extra, err
}

func (piToolSource) ToolCallID(original []byte) string {
	reference := parsePiToolArtifactSource(original)
	if reference != nil && (reference.outputReference().path != "" || reference.mcpResultReference().path != "") {
		return reference.ToolCallID
	}
	return ""
}

func (piToolSource) ReadSupplements(ctx context.Context, _ string, pending map[string]agent.MessageContent, final bool) (map[string][]byte, error) {
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

func (piToolSource) NewChild() tooltranscript.Source { return piToolSource{} }
