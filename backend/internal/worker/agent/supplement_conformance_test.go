package agent

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// supplementConformanceCase is one row of a shared corpus: the frame the agent sent,
// the envelope the worker stored beside it, and the object BOTH implementations must
// produce. The browser plugin replays the same file.
type supplementConformanceCase struct {
	Name         string          `json:"name"`
	Original     json.RawMessage `json:"original"`
	Supplemental json.RawMessage `json:"supplemental"`
	Expected     json.RawMessage `json:"expected"`
}

func loadSupplementConformance(t *testing.T, name string) []supplementConformanceCase {
	t.Helper()
	data, err := os.ReadFile("../../../../testdata/" + name)
	require.NoError(t, err)
	var fixture struct {
		Cases []supplementConformanceCase `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)
	return fixture.Cases
}

// runSupplementConformance replays one corpus against one resolver.
//
// Each case is checked three ways. The resolve must produce the shared expectation,
// it must be IDEMPOTENT -- a row the store resolves twice must not grow a second copy
// of the join -- and it must leave both inputs byte for byte as they were, because the
// original bytes are what the Raw JSON view shows the reader.
func runSupplementConformance(t *testing.T, name string, resolve func(MessageContent) []byte) {
	t.Helper()
	for _, tc := range loadSupplementConformance(t, name) {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			original := append([]byte(nil), tc.Original...)
			supplemental := append([]byte(nil), tc.Supplemental...)
			resolved := resolve(MessageContent{Original: tc.Original, Supplemental: tc.Supplemental})
			require.NotEmpty(t, resolved)
			assert.JSONEq(t, string(tc.Expected), string(resolved))
			again := resolve(MessageContent{Original: resolved, Supplemental: tc.Supplemental})
			assert.JSONEq(t, string(tc.Expected), string(again), "the resolve must be idempotent")
			assert.Equal(t, original, []byte(tc.Original), "the original bytes must not change")
			assert.Equal(t, supplemental, []byte(tc.Supplemental), "the supplement bytes must not change")
		})
	}
}

func TestACPMessageContentConformance(t *testing.T) {
	t.Parallel()
	runSupplementConformance(t, "acp_message_content_conformance.json", resolveACPMessageContent)
}

func TestCodexMessageContentConformance(t *testing.T) {
	t.Parallel()
	runSupplementConformance(t, "codex_message_content_conformance.json", codexProvider{}.ResolveProviderData)
}

// storedSupplementCase is one row of a WRITER corpus.
//
// The ZCode and Cursor supplements are never resolved back into the frame: the worker
// writes them and the browser plugin reads them, so there is no shared resolve to
// replay. The STORED BYTES are the contract instead. This half asserts the worker
// writes exactly those bytes; the plugin's own suite asserts it reads the expected
// fields out of the same file.
type storedSupplementCase struct {
	Name       string          `json:"name"`
	Original   json.RawMessage `json:"original"`
	Supplement json.RawMessage `json:"supplement"`
}

func TestZCodeToolSupplementConformance(t *testing.T) {
	t.Parallel()
	var fixture struct {
		Cases []struct {
			storedSupplementCase
			Record struct {
				Native    json.RawMessage   `json:"native"`
				Artifacts map[string]string `json:"artifacts"`
			} `json:"record"`
		} `json:"cases"`
	}
	data, err := os.ReadFile("../../../../testdata/zcode_tool_supplement_conformance.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			var native contracts.ZCodeStoredTool
			require.NoError(t, json.Unmarshal(tc.Record.Native, &native))
			encoded, err := zcodeToolResultSupplement(tc.Original, zcodeToolRecord{native: native, artifacts: tc.Record.Artifacts})
			require.NoError(t, err)
			assert.JSONEq(t, string(tc.Supplement), string(encoded))
		})
	}
}

func TestCursorStoredToolConformance(t *testing.T) {
	t.Parallel()
	var fixture struct {
		Cases []struct {
			storedSupplementCase
			Record struct {
				Content   json.RawMessage            `json:"content"`
				Arguments json.RawMessage            `json:"arguments"`
				Result    map[string]json.RawMessage `json:"result"`
			} `json:"record"`
		} `json:"cases"`
	}
	data, err := os.ReadFile("../../../../testdata/cursor_stored_tool_conformance.json")
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
	data, err := os.ReadFile("../../../../testdata/cursor_extension_conformance.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			resolved := cursorProvider{}.ResolveProviderData(MessageContent{
				Original: tc.Original, Supplemental: tc.Supplement,
			})
			assert.JSONEq(t, string(tc.Resolved), string(resolved))
		})
	}
}
