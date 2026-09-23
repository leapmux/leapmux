package cursor

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorStoredToolConformance(t *testing.T) {
	t.Parallel()
	var fixture struct {
		Cases []struct {
			agenttest.StoredSupplementCase
			Record struct {
				Content   json.RawMessage            `json:"content"`
				Arguments json.RawMessage            `json:"arguments"`
				Result    map[string]json.RawMessage `json:"result"`
			} `json:"record"`
		} `json:"cases"`
	}
	data, err := os.ReadFile(testutil.RepoPath(t, "testdata", "cursor_stored_tool_conformance.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			arguments := tc.Record.Arguments
			if string(arguments) == "null" {
				arguments = nil
			}
			encoded, err := cursorToolSupplement(tc.Original, cursorToolRecord{
				content: tc.Record.Content, arguments: arguments, result: tc.Record.Result,
			})
			require.NoError(t, err)
			assert.JSONEq(t, string(tc.Supplement), string(encoded))
		})
	}
}

// TestCursorExtensionConformance replays the Cursor extension resolve.
//
// A `cursor/*` frame arrives one row after the call it describes, so the worker stores
// it beside that row rather than inside it -- and BOTH sides then check that the
// envelope identifies the row before they read it. The browser's half of this file lives in
// providers/cursor/extensionConformance.test.ts.
func TestCursorExtensionConformance(t *testing.T) {
	t.Parallel()
	var fixture struct {
		Cases []struct {
			Name       string          `json:"name"`
			Original   json.RawMessage `json:"original"`
			Supplement json.RawMessage `json:"supplement"`
			Resolved   json.RawMessage `json:"resolved"`
		} `json:"cases"`
	}
	data, err := os.ReadFile(testutil.RepoPath(t, "testdata", "cursor_extension_conformance.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			resolved := cursorProvider{}.ResolveProviderData(agent.MessageContent{
				Original: tc.Original, Supplemental: tc.Supplement,
			})
			assert.JSONEq(t, string(tc.Resolved), string(resolved))
		})
	}
}
