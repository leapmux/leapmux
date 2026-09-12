package agent

import (
	"context"
	"errors"
	"os"
)

func newPiToolTranscript(ctx context.Context, services ProviderServices) *toolTranscript {
	return &toolTranscript{
		ProviderServices: services,
		ctx:              ctx,
		providerName:     "Pi",
		locate: func(sessionID string) toolTranscriptLocation {
			return toolTranscriptLocation{sessionKey: sessionID, path: os.TempDir()}
		},
		initialSupplement: func(ctx context.Context, _ string, original []byte, span SpanInfo) ([]byte, error) {
			if !span.Closing {
				return nil, nil
			}
			extra, _, err := recoverPiToolArtifacts(ctx, original, nil)
			return extra, err
		},
		toolCallID: func(raw []byte) string {
			source := parsePiToolArtifactSource(raw)
			if source != nil && (source.outputReference().path != "" || source.mcpResultReference().path != "") {
				return source.ToolCallID
			}
			return ""
		},
		readSupplements: func(ctx context.Context, _ string, pending map[string]MessageContent, final bool) (map[string][]byte, error) {
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
		},
		newChild: func(child ProviderServices) *toolTranscript {
			return newPiToolTranscript(ctx, child)
		},
	}
}
