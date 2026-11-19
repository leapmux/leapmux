package dirac

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiracAPIHistoryReplaysNativeToolRecordsAndUserContent(t *testing.T) {
	t.Parallel()
	operations := strings.Join([]string{
		`{"offset":0,"type":"append_message","message":{"role":"assistant","content":[{"type":"tool_use","id":"read-1","name":"read_file","input":{"paths":["a.txt"]}}]}}`,
		`{"offset":1,"type":"append_message","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"read-1","content":"Actual file contents"}]}}`,
		`{"offset":2,"type":"append_user_content","content":{"type":"text","text":"Native supervision"}}`,
	}, "\n") + "\n"
	messages, err := decodeDiracAPIHistory(nil, nil, []byte(operations))
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.JSONEq(t, `{"role":"user","content":[{"type":"tool_result","tool_use_id":"read-1","content":"Actual file contents"},{"type":"text","text":"Native supervision"}]}`, string(messages[1]))
}

func TestDiracAPIHistoryUsesBaselineAndIgnoresOnlyAnUnfinishedTail(t *testing.T) {
	t.Parallel()
	baseline := []byte(`{"type":"baseline","offset":4}` + "\n" + `{"type":"message","message":{"role":"user","content":"Original"}}` + "\n")
	operations := []byte(`{"offset":5,"type":"append_user_content","content":{"type":"text","text":"🙂"}}` + "\n" + `{"offset":6,"type":"reset"`)
	messages, err := decodeDiracAPIHistory([]byte(`invalid superseded snapshot`), baseline, operations)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.JSONEq(t, `{"role":"user","content":[{"type":"text","text":"Original"},{"type":"text","text":"🙂"}]}`, string(messages[0]))
}

func TestDiracAPIHistoryAllowsExplicitBranchesAndEmptyResets(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"reset", "branch"} {
		messages, err := decodeDiracAPIHistory(nil, nil, []byte(`{"offset":20,"type":"`+kind+`","messages":[]}`+"\n"))
		require.NoError(t, err)
		assert.NotNil(t, messages)
		assert.Empty(t, messages)
	}
}

func TestDiracAPIHistoryRejectsInvalidOperations(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{`, `{"type":"append_message"}`, `{"offset":-1,"type":"reset","messages":[]}`,
		`{"offset":2,"type":"append_message","message":{"role":"user","content":"Gap"}}`,
		`{"offset":0,"type":"append_message","message":{"role":"system","content":"Not native history"}}`,
		`{"offset":0,"type":"append_message","message":{"role":"user","content":null}}`,
		`{"offset":0,"type":"append_user_content","content":{"type":"text","text":"No user"}}`,
		`{"offset":0,"type":"reset","messages":null}`, `{"offset":0,"type":"unknown"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			messages, err := decodeDiracAPIHistory(nil, nil, []byte(raw+"\n"))
			require.Error(t, err)
			assert.Empty(t, messages)
		})
	}
}
