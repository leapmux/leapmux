package qwen

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"
)

// qwenRegistryRowKey scopes model-supplied IDs to the native root session.
// The browser replays the same four length-prefixed UTF-8 components. Shared
// vectors in testdata/qwen_registry_key_conformance.json specify their bytes.
func qwenRegistryRowKey(source, sessionID, nativeToolCallID, nativeDispatchID string) string {
	if sessionID == "" || nativeToolCallID == "" ||
		(source != contracts.QwenToolAgent && source != contracts.QwenToolWorkflow) ||
		(source == contracts.QwenToolAgent && nativeDispatchID != "") {
		return ""
	}
	digest := sha256.New()
	var length [8]byte
	for _, component := range []string{source, sessionID, nativeToolCallID, nativeDispatchID} {
		if !utf8.ValidString(component) {
			return ""
		}
		binary.BigEndian.PutUint64(length[:], uint64(len(component)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write([]byte(component))
	}
	return contracts.QwenRegistryKeyPrefix + hex.EncodeToString(digest.Sum(nil))
}

func qwenAgentRowKey(sessionID, nativeToolCallID string) string {
	return qwenRegistryRowKey(contracts.QwenToolAgent, sessionID, nativeToolCallID, "")
}

func qwenWorkflowRowKey(sessionID, nativeToolCallID string) string {
	return qwenRegistryRowKey(contracts.QwenToolWorkflow, sessionID, nativeToolCallID, "")
}

func qwenWorkflowDispatchRowKey(sessionID, nativeToolCallID, nativeDispatchID string) string {
	if nativeDispatchID == "" {
		return ""
	}
	return qwenRegistryRowKey(contracts.QwenToolWorkflow, sessionID, nativeToolCallID, nativeDispatchID)
}
