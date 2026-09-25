package ohmypi

import (
	"context"
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/jsonfield"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ompProvider is Oh My Pi's stateless wire-format plugin.
type ompProvider struct {
	agent.ProviderDefaults
}

// ValidateAttachment accepts text and images. omp's prompt carries text and
// `images`, and it has no field for a PDF or another binary file.
func (ompProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	return providerkit.RejectPDFAndBinaryAttachment("Oh My Pi", attachment)
}

// ResolveResumeHandle takes EITHER a session file PATH or a session id.
//
// `omp --resume` resolves both shapes, with the same test Pi uses; see
// providerkit.ResolveSessionFileOrIDHandle. The worker stores the FILE, because
// `--resume <path>` also resumes a session whose file omp has not written yet
// (see sessionHandleLocked), and the session picker lists files for the same
// reason.
func (ompProvider) ResolveResumeHandle(handle, homeDir string) (string, error) {
	return providerkit.ResolveSessionFileOrIDHandle(handle, homeDir)
}

// ListStoredSessions reads omp's own session files; see sessions.go.
func (ompProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return storedSessions(ctx, q)
}

// Classify groups omp's lifecycle notifications, so a long session does not
// collect one row for every retry and every compaction.
//
// A finished compaction is the boundary -- automatic, or the response to the
// worker's own `compact` command. A compaction in progress, and a `compact`
// command omp refused ("Nothing to compact"), are a status, which the next one
// replaces. A retry and its end are one retry notice.
func (ompProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	var env struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Success bool   `json:"success"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return agent.NotificationClassification{}
	}
	switch env.Type {
	case contracts.OhMyPiEventAutoCompactionEnd:
		return agent.NotificationClassification{Kind: agent.NotificationKindCompactionBoundary, Key: "omp:" + env.Type}
	case contracts.OhMyPiEventAutoCompactionStart:
		return agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: "omp:" + env.Type}
	case contracts.OhMyPiEventAutoRetryStart, contracts.OhMyPiEventAutoRetryEnd:
		return agent.NotificationClassification{Kind: agent.NotificationKindAPIRetry, Key: "omp:" + env.Type}
	case contracts.OhMyPiEventResponse:
		if env.Command != contracts.OhMyPiCommandCompact {
			return agent.NotificationClassification{}
		}
		if env.Success {
			return agent.NotificationClassification{Kind: agent.NotificationKindCompactionBoundary, Key: "omp:" + contracts.OhMyPiCommandCompact}
		}
		return agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: "omp:" + contracts.OhMyPiCommandCompact}
	default:
		return agent.NotificationClassification{}
	}
}

// IsInterrupt recognizes omp's `abort` command, the frame Interrupt writes.
func (ompProvider) IsInterrupt(content string) bool {
	var frame struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(content), &frame); err != nil {
		return false
	}
	return frame.Type == CommandAbort
}

// ResolveProviderData puts a retained call's partial result on its start frame.
//
// A turn that ended while a call ran stores the call's START frame as its closing
// row, with the partial result omp reported beside it
// (contracts.OhMyPiIncompleteToolSupplement). The identity keys are checked
// first, so a supplement that states another call, or another tool, cannot reach
// the row.
func (ompProvider) ResolveProviderData(content agent.MessageContent) []byte {
	if len(content.Supplemental) == 0 {
		return content.Original
	}
	var extra contracts.OhMyPiIncompleteToolSupplement
	if json.Unmarshal(content.Supplemental, &extra) != nil || extra.ToolCallID == "" || len(extra.PartialResult) == 0 {
		return content.Original
	}
	var original map[string]json.RawMessage
	if json.Unmarshal(content.Original, &original) != nil || original == nil {
		return content.Original
	}
	var frameType, toolCallID, toolName string
	if json.Unmarshal(original["type"], &frameType) != nil || frameType != contracts.OhMyPiEventToolExecutionStart {
		return content.Original
	}
	if json.Unmarshal(original[contracts.OhMyPiFrameFieldToolCallID], &toolCallID) != nil || toolCallID != extra.ToolCallID {
		return content.Original
	}
	if json.Unmarshal(original[contracts.OhMyPiFrameFieldToolName], &toolName) != nil || toolName != extra.ToolName {
		return content.Original
	}
	// Resolving an already-resolved frame returns the SAME bytes, so a caller
	// that resolves twice allocates no second copy of the row.
	if jsonfield.Equal(original[contracts.OhMyPiFrameFieldResult], extra.PartialResult) {
		return content.Original
	}
	original[contracts.OhMyPiFrameFieldResult] = extra.PartialResult
	resolved, err := json.Marshal(original)
	if err != nil {
		return content.Original
	}
	return resolved
}
