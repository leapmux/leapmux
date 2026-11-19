package dirac

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiracPresentationReplaysNativeReceiptPatches(t *testing.T) {
	t.Parallel()
	operations := strings.Join([]string{
		`{"offset":0,"type":"create","message":{"id":"spawn-card","conversationHistoryIndex":0,"content":{"type":"card","card":{"id":"spawn-card","toolName":"subagents","status":"running","taskletReceipt":{"operation":"spawn","outcome":"requested","rows":[]}}}}}`,
		`{"offset":1,"type":"patch_card","id":"spawn-card","patch":{"id":"spawn-card","status":"success","collapsed":false,"endTime":0,"taskletReceipt":{"operation":"spawn","outcome":"accepted","rows":[{"taskletId":"Witten","title":"Inspect the parser","outcome":"accepted"}]}}}`,
	}, "\n") + "\n"
	rows, err := decodeDiracPresentation(nil, nil, []byte(operations))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.JSONEq(t, `{"id":"spawn-card","conversationHistoryIndex":0,"content":{"type":"card","card":{"id":"spawn-card","toolName":"subagents","status":"success","collapsed":false,"endTime":0,"taskletReceipt":{"operation":"spawn","outcome":"accepted","rows":[{"taskletId":"Witten","title":"Inspect the parser","outcome":"accepted"}]}}}}`, string(rows[0]))
}

func TestDiracPresentationUsesBaselineBeforeItsLaterOperations(t *testing.T) {
	t.Parallel()
	baseline := `{"type":"baseline","offset":2}` + "\n" + `{"type":"message","message":{"id":"thought","content":{"type":"markdown","content":"한글 ","isReasoning":true}}}` + "\n"
	operations := strings.Join([]string{
		`{"offset":0,"type":"obsolete-covered-operation"}`,
		`{"offset":3,"type":"append_markdown","id":"thought","text":"🙂"}`,
		`{"offset":4,"type":"append_markdown","id":"thought","text":"🙂"}`,
		`{"offset":5,"type":"patch_markdown","id":"thought","patch":{"isReasoning":false,"agentId":0}}`,
	}, "\n") + "\n"
	rows, err := decodeDiracPresentation([]byte(`not the current baseline`), []byte(baseline), []byte(operations))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.JSONEq(t, `{"id":"thought","content":{"type":"markdown","content":"한글 🙂🙂","isReasoning":false,"agentId":0}}`, string(rows[0]))
}

func TestDiracPresentationPreservesCompleteLinesBeforeAnInterruptedAppend(t *testing.T) {
	t.Parallel()
	snapshot := []byte(`[{"id":"text","content":{"type":"markdown","content":"First"}}]`)
	operations := []byte(`{"offset":0,"type":"append_markdown","id":"text","text":" retained"}` + "\n" + `{"offset":1,"type":"delete","id":"text"`)
	rows, err := decodeDiracPresentation(snapshot, nil, operations)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.JSONEq(t, `{"id":"text","content":{"type":"markdown","content":"First retained"}}`, string(rows[0]))
}

func TestDiracPresentationDeletesResetsAndAppendsToAnEmptyCard(t *testing.T) {
	t.Parallel()
	snapshot := []byte(`[{"id":"old","content":{"type":"markdown","content":"Old"}},{"id":"kept","content":{"type":"markdown","content":"Kept"}}]`)
	operations := strings.Join([]string{
		`{"offset":0,"type":"delete","id":"old"}`,
		`{"offset":1,"type":"patch_message","id":"kept","patch":{"ts":0}}`,
		`{"offset":50,"type":"reset","messages":[{"id":"current","content":{"type":"card","card":{"id":"current"}}}]}`,
		`{"offset":51,"type":"append_card_body","id":"current","text":"Native card"}`,
		`{"offset":52,"type":"patch_card","id":"current","patch":{"status":"success"}}`,
	}, "\n") + "\n"
	rows, err := decodeDiracPresentation(snapshot, nil, []byte(operations))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.JSONEq(t, `{"id":"current","content":{"type":"card","card":{"id":"current","body":"Native card","status":"success"}}}`, string(rows[0]))
}

func TestDiracPresentationPatchesAPIStatusAndRetainsExplicitZero(t *testing.T) {
	t.Parallel()
	snapshot := []byte(`[{"id":"request","content":{"type":"api_status","status":{"id":"request","retryStatus":"waiting","tokensOut":8}}}]`)
	operations := []byte(`{"offset":0,"type":"patch_api_status","id":"request","patch":{"tokensOut":0,"cost":0},"deletions":["retryStatus"]}` + "\n")
	rows, err := decodeDiracPresentation(snapshot, nil, operations)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.JSONEq(t, `{"id":"request","content":{"type":"api_status","status":{"id":"request","tokensOut":0,"cost":0}}}`, string(rows[0]))
}

func TestDiracPresentationRejectsMalformedOrForeignOperations(t *testing.T) {
	t.Parallel()
	snapshot := []byte(`[{"id":"text","content":{"type":"markdown","content":"Original"}}]`)
	for _, raw := range []string{
		`{`,
		`{"type":"delete","id":"text"}`,
		`{"offset":null,"type":"delete","id":"text"}`,
		`{"offset":-1,"type":"delete","id":"text"}`,
		`{"offset":0.5,"type":"delete","id":"text"}`,
		`{"offset":9007199254740992,"type":"delete","id":"text"}`,
		`{"offset":2,"type":"delete","id":"text"}`,
		`{"offset":0,"type":"delete","id":"other"}`,
		`{"offset":0,"type":"create","message":{"id":"text","content":{"type":"markdown","content":"Replacement"}}}`,
		`{"offset":0,"type":"create","message":{"id":"","content":{"type":"markdown","content":"Replacement"}}}`,
		`{"offset":0,"type":"create","message":{"id":"new","content":null}}`,
		`{"offset":0,"type":"patch_message","id":"text","patch":null}`,
		`{"offset":0,"type":"patch_message","id":"text","patch":{"id":"other"}}`,
		`{"offset":0,"type":"patch_card","id":"text","patch":{}}`,
		`{"offset":0,"type":"patch_api_status","id":"text","patch":{}}`,
		`{"offset":0,"type":"patch_markdown","id":"text","patch":{"content":"Not an append"}}`,
		`{"offset":0,"type":"patch_markdown","id":"text","patch":{"type":"card"}}`,
		`{"offset":0,"type":"append_markdown","id":"text"}`,
		`{"offset":0,"type":"append_markdown","id":"text","text":null}`,
		`{"offset":0,"type":"append_card_body","id":"text","text":"Wrong type"}`,
		`{"offset":0,"type":"reset","messages":null}`,
		`{"offset":0,"type":"reset","messages":[{"id":"same","content":{"type":"markdown"}},{"id":"same","content":{"type":"markdown"}}]}`,
		`{"offset":0,"type":"unknown","id":"text","patch":{}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			rows, err := decodeDiracPresentation(snapshot, nil, []byte(raw+"\n"))
			require.Error(t, err)
			assert.Empty(t, rows, "a failed replay publishes no partial document")
		})
	}
}

func TestDiracPresentationRejectsInvalidBaselinesAndSnapshots(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"", `null`, `[]`, `{"type":"baseline"}` + "\n",
		`{"type":"baseline","offset":-2}` + "\n",
		`{"type":"baseline","offset":0.5}` + "\n",
		`{"type":"baseline","offset":0}`, // Atomic baseline publication always includes its final newline.
		`{"type":"baseline","offset":0}` + "\n" + `{"type":"create","message":{}}` + "\n",
	} {
		t.Run("baseline="+raw, func(t *testing.T) {
			t.Parallel()
			rows, err := decodeDiracPresentation(nil, []byte(raw), nil)
			require.Error(t, err)
			assert.Empty(t, rows)
		})
	}
	for _, raw := range []string{"", `null`, `{}`, `[null]`, `[{"content":{"type":"markdown"}}]`} {
		t.Run("snapshot="+raw, func(t *testing.T) {
			t.Parallel()
			_, err := decodeDiracPresentation([]byte(raw), nil, nil)
			require.Error(t, err)
		})
	}
}

func TestDiracPresentationAllowsAnExplicitEmptyDocument(t *testing.T) {
	t.Parallel()
	for _, input := range []struct{ snapshot, baseline, operations []byte }{
		{},
		{snapshot: []byte(`[]`)},
		{baseline: []byte(`{"type":"baseline","offset":-1}` + "\n")},
		{operations: []byte(`{"offset":0,"type":"reset","messages":[]}` + "\n")},
	} {
		rows, err := decodeDiracPresentation(input.snapshot, input.baseline, input.operations)
		require.NoError(t, err)
		assert.NotNil(t, rows)
		assert.Empty(t, rows)
	}
}

func TestDiracPresentationDoesNotMutateItsSourceBytes(t *testing.T) {
	t.Parallel()
	snapshot := []byte(`[{"id":"text","content":{"type":"markdown","content":"Before"}}]`)
	before := string(snapshot)
	rows, err := decodeDiracPresentation(snapshot, nil, []byte(`{"offset":0,"type":"append_markdown","id":"text","text":" after"}`+"\n"))
	require.NoError(t, err)
	assert.Equal(t, before, string(snapshot))
	var message map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rows[0], &message))
	assert.JSONEq(t, `{"type":"markdown","content":"Before after"}`, string(message["content"]))
}
