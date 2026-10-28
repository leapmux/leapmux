package qwen

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQwenRegistryKeyConformance(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(testutil.RepoPath(t, "testdata", "qwen_registry_key_conformance.json"))
	require.NoError(t, err)
	var fixture struct {
		Cases []struct {
			Name        string `json:"name"`
			Source      string `json:"source"`
			SessionID   string `json:"sessionId"`
			ToolCallID  string `json:"toolCallId"`
			DispatchID  string `json:"dispatchId"`
			ExpectedKey string `json:"expectedKey"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))
	require.NotEmpty(t, fixture.Cases)
	for _, vector := range fixture.Cases {
		t.Run(vector.Name, func(t *testing.T) {
			t.Parallel()
			key := qwenRegistryRowKey(vector.Source, vector.SessionID, vector.ToolCallID, vector.DispatchID)
			assert.Equal(t, vector.ExpectedKey, key)
			if key == "" {
				return
			}
			assert.Len(t, key, 81)
			assert.NoError(t, bgtask.ValidateRowKey(key))
			assert.NoError(t, bgtask.ValidateGroupKey(key))
			assert.Equal(t, key, bgtask.NormalizeRowKey(key))
			assert.Equal(t, key, bgtask.NewRowKey(key).String())
		})
	}
}

func TestQwenRegistryKeyRejectsIncompleteOrInvalidIdentities(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, source, session, call, dispatch string
	}{
		{name: "missing owner", source: contracts.QwenToolAgent, call: "call"},
		{name: "missing call", source: contracts.QwenToolAgent, session: "owner"},
		{name: "unknown source", source: "shell", session: "owner", call: "call"},
		{name: "agent dispatch", source: contracts.QwenToolAgent, session: "owner", call: "call", dispatch: "dispatch"},
		{name: "invalid owner", source: contracts.QwenToolAgent, session: string([]byte{0xff}), call: "call"},
		{name: "invalid call", source: contracts.QwenToolAgent, session: "owner", call: string([]byte{0xff})},
		{name: "invalid dispatch", source: contracts.QwenToolWorkflow, session: "owner", call: "call", dispatch: string([]byte{0xff})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Empty(t, qwenRegistryRowKey(tc.source, tc.session, tc.call, tc.dispatch))
		})
	}
	assert.Empty(t, qwenWorkflowDispatchRowKey("owner", "call", ""))
	assert.Equal(t, "qwen-registry-v1:a1177c0911fd2197b36567c453224ef3d6fb6c4b7368399506542fa820c9aae5", qwenAgentRowKey(qwenInitialChildSession, "call_shared"))
	assert.Equal(t, "qwen-registry-v1:ec431696f1d789f6ccb726221e7502121b4aaab7ceb01ab62c91f4d2a6b9c38d", qwenWorkflowRowKey(qwenInitialChildSession, "call_shared"))
	assert.Equal(t, "qwen-registry-v1:efe480f6869b94a40378e0cb3290f9521f28095e627ea9ce09fe63af44e078b4", qwenWorkflowDispatchRowKey(qwenInitialChildSession, "call_shared", "dispatch-1"))
}

func TestQwenRegistryKeysKeepLargeIdentitiesDistinct(t *testing.T) {
	t.Parallel()
	large := strings.Repeat("opaque:", 1<<15)
	first := qwenAgentRowKey(large, large+"first")
	second := qwenAgentRowKey(large, large+"second")
	assert.Len(t, first, 81)
	assert.Len(t, second, 81)
	assert.NotEqual(t, first, second)
	assert.Equal(t, first, qwenAgentRowKey(large, large+"first"))
	assert.NoError(t, bgtask.ValidateGroupKey(first))
	assert.Equal(t, first, bgtask.NormalizeRowKey(first))
}
