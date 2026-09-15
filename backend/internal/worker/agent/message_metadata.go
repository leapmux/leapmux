package agent

import (
	"encoding/json"
	"log/slog"
	"strconv"

	"github.com/leapmux/leapmux/generated/contracts"
)

// withToolUseCount records the worker's count without changing native provider fields.
func withToolUseCount(content MessageContent, count int) MessageContent {
	if count < 0 {
		return content
	}
	var fields map[string]json.RawMessage
	if len(content.Metadata) > 0 && json.Unmarshal(content.Metadata, &fields) != nil {
		slog.Warn("decode metadata for tool count")
		return content
	}
	if fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	fields[contracts.MessageMetadataFieldToolUses] = json.RawMessage(strconv.Itoa(count))
	metadata, err := json.Marshal(fields)
	if err != nil {
		slog.Warn("encode tool count metadata", "error", err)
		return content
	}
	content.Metadata = metadata
	return content
}

// mergeMessageMetadata supplies only validated worker metadata to semantic readers.
// The caller validates the provider envelope. Neither source changes.
//
// The two parses run cheap side first, and the order is load-bearing rather than a
// matter of taste. The provider envelope is the whole message and can be hundreds of
// kilobytes; the metadata is a handful of numbers that only a turn-end row or a usage
// row carries. The stdout reader runs this for EVERY persisted message, so a test of
// the metadata first is what keeps a plain assistant row from paying a full scan of
// its own envelope to learn that it has nothing to merge.
func mergeMessageMetadata(content MessageContent) []byte {
	if len(content.Metadata) == 0 {
		return content.Original
	}
	var supplemental map[string]json.RawMessage
	if json.Unmarshal(content.Metadata, &supplemental) != nil || supplemental == nil {
		return content.Original
	}
	var original map[string]json.RawMessage
	if json.Unmarshal(content.Original, &original) != nil || original == nil {
		return content.Original
	}
	changed := false
	for _, key := range []string{contracts.MessageMetadataFieldDurationMs, contracts.MessageMetadataFieldToolUses} {
		var value *int64
		if json.Unmarshal(supplemental[key], &value) == nil && value != nil && *value >= 0 {
			original[key] = supplemental[key]
			changed = true
		}
	}
	var cost *float64
	if json.Unmarshal(supplemental[contracts.SessionInfoKeyTotalCostUsd], &cost) == nil && cost != nil && *cost >= 0 {
		original[contracts.SessionInfoKeyTotalCostUsd] = supplemental[contracts.SessionInfoKeyTotalCostUsd]
		changed = true
	}
	var usage map[string]json.RawMessage
	if json.Unmarshal(supplemental[contracts.SessionInfoKeyContextUsage], &usage) == nil && usage != nil {
		original[contracts.SessionInfoKeyContextUsage] = supplemental[contracts.SessionInfoKeyContextUsage]
		changed = true
	}
	if !changed {
		return content.Original
	}
	merged, err := json.Marshal(original)
	if err != nil {
		return content.Original
	}
	return merged
}

// EncodeMessageSupplement keeps native provider fields outside the worker metadata schema.
func EncodeMessageSupplement(content MessageContent) ([]byte, error) {
	fields := make(map[string]json.RawMessage, 2)
	if len(content.Supplemental) > 0 {
		fields[contracts.MessageSupplementFieldProvider] = content.Supplemental
	}
	if len(content.Metadata) > 0 {
		fields[contracts.MessageSupplementFieldMetadata] = content.Metadata
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return json.Marshal(fields)
}

// DecodeMessageSupplement restores both supplemental sources without changing the original bytes.
func DecodeMessageSupplement(original, supplemental []byte) (MessageContent, error) {
	content := MessageContent{Original: original}
	if len(supplemental) == 0 {
		return content, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(supplemental, &fields); err != nil {
		return content, err
	}
	content.Supplemental = fields[contracts.MessageSupplementFieldProvider]
	content.Metadata = fields[contracts.MessageSupplementFieldMetadata]
	return content, nil
}

// ResolveMessageContent combines provider data and worker metadata through one read path.
func ResolveMessageContent(provider Provider, content MessageContent) []byte {
	resolved := provider.ResolveProviderData(content)
	return mergeMessageMetadata(MessageContent{Original: resolved, Metadata: content.Metadata})
}
