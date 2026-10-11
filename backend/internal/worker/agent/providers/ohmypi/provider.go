package ohmypi

import (
	"context"
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/jsonfield"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ompProvider is Oh My Pi's stateless plugin for its native protocol.
type ompProvider struct {
	agent.ProviderDefaults
}

// ValidateAttachment accepts text and images. omp's prompt carries text and
// `images`, and it has no field for a PDF or another binary file.
func (ompProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	return providerkit.RejectPDFAndBinaryAttachment("Oh My Pi", attachment)
}

// ResolveResumeHandle accepts a session file path or a session ID.
// providerkit.ResolveSessionFileOrIDHandle also validates Pi's two handle formats.
// The Worker stores the file path because omp can resume a file that does not yet exist.
// sessionHandleLocked and the session picker preserve that native behavior.
func (ompProvider) ResolveResumeHandle(handle, homeDir string) (string, error) {
	return providerkit.ResolveSessionFileOrIDHandle(handle, homeDir)
}

// ListStoredSessions reads omp's own session files; see sessions.go.
func (ompProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return storedSessions(ctx, q)
}

// Classify groups lifecycle notifications instead of adding a row for each update.
// A completed compaction creates a boundary, including the response to the Worker's compact command.
// A current compaction or a refused compact command creates a replaceable status.
// A retry and its completion share one retry notice.
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

// ResolveProviderData restores a matched retained partial result on its native start frame.
// A turn that ends before its tool keeps the start frame and last partial result.
// The resolver checks native call identity before it uses that supplement.
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
	// Return the same bytes when the resolved frame already contains this partial result.
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
