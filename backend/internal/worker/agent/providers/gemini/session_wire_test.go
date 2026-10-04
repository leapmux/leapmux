package gemini

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const geminiNativeMetadata = `{"sessionId":"native-session","projectHash":"project-hash","startTime":"2026-10-02T16:00:00Z","lastUpdated":"2026-10-02T16:00:00Z","kind":"main"}`

func geminiNativeMessage(id, text string) string {
	data, _ := json.Marshal(map[string]any{"id": id, "type": "gemini", "content": text, "timestamp": "2026-10-02T16:00:00Z"})
	return string(data)
}

func TestDecodeGeminiSessionPreservesNativeRecordsAndCompletePatches(t *testing.T) {
	t.Parallel()
	tool := `{"id":"shell__call","name":"run_shell_command","args":{"command":"printf value"},"result":[{"functionResponse":{"response":{"output":"old"}}}],"status":"success","resultDisplay":"value"}`
	data := strings.Join([]string{
		geminiNativeMetadata,
		`{"id":"message","type":"gemini","content":"first","toolCalls":[` + tool + `]}`,
		`{"$patch":{"updates":[{"id":"message","content":"final","toolCalls":[{"id":"shell__call","result":[{"functionResponse":{"response":{"output":"complete"}}}]}]}]}}`,
		`{"$set":{"summary":"native summary","lastUpdated":"2026-10-02T17:00:00Z"}}`,
	}, "\n")
	session, err := decodeGeminiSession([]byte(data))
	require.NoError(t, err)
	assert.Equal(t, "native-session", session.SessionID)
	assert.Equal(t, "native summary", session.Summary)
	require.Len(t, session.Messages, 1)
	assert.Equal(t, "final", geminiMessageText(session.Messages[0].Content))
	require.Len(t, session.Messages[0].ToolCalls, 1)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(session.Messages[0].ToolCalls[0], &fields))
	assert.JSONEq(t, `{"command":"printf value"}`, string(fields["args"]))
	assert.JSONEq(t, `"value"`, string(fields["resultDisplay"]))
	assert.JSONEq(t, `[{"functionResponse":{"response":{"output":"complete"}}}]`, string(fields["result"]))
	var original map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(session.Messages[0].Original, &original))
	assert.JSONEq(t, `"final"`, string(original["content"]))
	var tools []json.RawMessage
	require.NoError(t, json.Unmarshal(original["toolCalls"], &tools))
	assert.JSONEq(t, string(session.Messages[0].ToolCalls[0]), string(tools[0]))
}

func TestDecodeGeminiSessionMatchesNativeRewindAndOrdering(t *testing.T) {
	t.Parallel()
	base := []string{geminiNativeMetadata, geminiNativeMessage("a", "A"), geminiNativeMessage("b", "B"), geminiNativeMessage("c", "C")}
	for _, test := range []struct {
		name   string
		record string
		want   []string
	}{
		{name: "rewind removes the specified message", record: `{"$rewindTo":"b"}`, want: []string{"a"}},
		{name: "unknown rewind clears the conversation", record: `{"$rewindTo":"missing"}`, want: []string{}},
		{name: "ordered messages follow the unspecified prefix", record: `{"$patch":{"orderIds":["c","b"]}}`, want: []string{"a", "c", "b"}},
		{name: "removed messages disappear before ordering", record: `{"$patch":{"removeIds":["b"],"orderIds":["c"]}}`, want: []string{"a", "c"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			session, err := decodeGeminiSession([]byte(strings.Join(append(append([]string{}, base...), test.record), "\n")))
			require.NoError(t, err)
			ids := make([]string, 0, len(session.Messages))
			for _, message := range session.Messages {
				ids = append(ids, message.ID)
			}
			assert.Equal(t, test.want, ids)
		})
	}
}

func TestDecodeGeminiSessionReplacesNativeMessageSnapshots(t *testing.T) {
	t.Parallel()
	data := strings.Join([]string{geminiNativeMetadata, geminiNativeMessage("a", "old"), geminiNativeMessage("a", "new"), `{"$set":{"messages":[{"id":"b","type":"user","content":[{"text":"complete snapshot"}]}]}}`}, "\n")
	session, err := decodeGeminiSession([]byte(data))
	require.NoError(t, err)
	require.Len(t, session.Messages, 1)
	assert.Equal(t, "b", session.Messages[0].ID)
	assert.Equal(t, "complete snapshot", geminiMessageText(session.Messages[0].Content))
}

func TestDecodeGeminiSessionRejectsInvalidAndForeignRecords(t *testing.T) {
	t.Parallel()
	for _, record := range []string{
		"",
		`{"id":"a","type":"user","content":"before metadata"}`,
		geminiNativeMetadata + "\n{" + "\n",
		geminiNativeMetadata + "\n" + `{"sessionId":"foreign-session","projectHash":"project-hash"}`,
		geminiNativeMetadata + "\n" + `{"$set":{"sessionId":"foreign-session"}}`,
		geminiNativeMetadata + "\n" + `{"$set":{"projectHash":"foreign-project"}}`,
		geminiNativeMetadata + "\n" + `{"$set":{"lastUpdated":"invalid"}}`,
	} {
		_, err := decodeGeminiSession([]byte(record))
		assert.Error(t, err, "record %q", record)
	}
}

func TestDecodeGeminiSessionIgnoresOnlyTheIncompleteLastRecord(t *testing.T) {
	t.Parallel()
	session, err := decodeGeminiSession([]byte(geminiNativeMetadata + "\n" + geminiNativeMessage("a", "retained") + "\n" + `{"id":"partial"`))
	require.NoError(t, err)
	require.Len(t, session.Messages, 1)
	assert.Equal(t, "retained", geminiMessageText(session.Messages[0].Content))
}

func TestDecodeGeminiSessionReadsNativeCompleteJSON(t *testing.T) {
	t.Parallel()
	session, err := decodeGeminiSession([]byte(`{"sessionId":"legacy-session","projectHash":"native-project","messages":[{"id":"u","type":"user","content":"native context"}]}`))
	require.NoError(t, err)
	assert.Equal(t, "legacy-session", session.SessionID)
	require.Len(t, session.Messages, 1)
	assert.Equal(t, "native context", geminiMessageText(session.Messages[0].Content))
}
