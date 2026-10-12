package gemini

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const geminiNativeChildID = "35e07dff-8c2c-4662-9993-9e7deb6a290b"

func TestGeminiChildRecordsRequireExactRootProjectAndNativeUUID(t *testing.T) {
	t.Parallel()
	query, directory, hash := geminiStoreFixture(t)
	writeGeminiSession(t, geminiFixtureSessionPath(directory, "root"), "root", hash, "main", "root prompt")
	children := filepath.Join(directory, "root")
	require.NoError(t, os.MkdirAll(children, 0o700))
	writeGeminiSession(t, filepath.Join(children, geminiNativeChildID+".jsonl"), geminiNativeChildID, hash, "subagent", "native child prompt")
	writeGeminiSession(t, filepath.Join(children, "fae5552a-3ab6-47f1-83e3-799034a381f8.jsonl"), "another-identity", hash, "subagent", "foreign child")
	writeGeminiSession(t, filepath.Join(children, "83e05575-615e-48c6-a83e-bc06af048deb.jsonl"), "83e05575-615e-48c6-a83e-bc06af048deb", "other-project", "subagent", "foreign project")
	writeGeminiSession(t, filepath.Join(children, "invalid.jsonl"), "invalid", hash, "subagent", "invalid child")
	childrenRecords, err := geminiChildRecords(query, "root")
	require.NoError(t, err)
	require.Len(t, childrenRecords, 1)
	assert.Equal(t, geminiNativeChildID, childrenRecords[0].Session.SessionID)
	_, err = geminiChildRecords(query, "foreign-root")
	assert.Error(t, err)
}

func TestGeminiChildRecordsRejectSymlinkedDescendantDirectories(t *testing.T) {
	t.Parallel()
	query, directory, hash := geminiStoreFixture(t)
	writeGeminiSession(t, geminiFixtureSessionPath(directory, "root"), "root", hash, "main", "root prompt")
	external := t.TempDir()
	writeGeminiSession(t, filepath.Join(external, geminiNativeChildID+".jsonl"), geminiNativeChildID, hash, "subagent", "external child")
	require.NoError(t, os.Symlink(external, filepath.Join(directory, "root")))
	children, err := geminiChildRecords(query, "root")
	assert.Error(t, err)
	assert.Empty(t, children)
}

func TestGeminiChildWritesPreserveCompleteOriginalRecordsAndDistinctNativeKeys(t *testing.T) {
	t.Parallel()
	const message = `{"id":"native-model","type":"gemini","content":"native answer","thoughts":[{"subject":"Inspect","description":"Read the source.","timestamp":"native-time"}],"toolCalls":[{"id":"read_file__native-call","name":"read_file","args":{"file_path":"/native/file"},"status":"success","result":[{"functionResponse":{"response":{"output":"actual file bytes"}}}],"unknownNativeField":false}],"unknownNativeMessageField":0}`
	session, err := decodeGeminiSession([]byte(geminiNativeMetadata + "\n" + message + "\n"))
	require.NoError(t, err)
	writes, err := geminiChildWrites(session)
	require.NoError(t, err)
	require.Len(t, writes, 3)
	assert.Equal(t, []string{"native-model:content", "native-model:thought:0", "read_file__native-call:result"}, []string{writes[0].Key, writes[1].Key, writes[2].Key})
	assert.Equal(t, []byte(message), writes[0].Content.Original)
	assert.Equal(t, []byte(message), writes[1].Content.Original)
	var supplement map[string]any
	require.NoError(t, json.Unmarshal(writes[1].Content.Supplemental, &supplement))
	assert.Equal(t, contracts.GeminiMessagePartThought, supplement[contracts.GeminiSupplementMessagePart])
	assert.Equal(t, float64(0), supplement[contracts.GeminiSupplementMessagePartIndex])
	var tool map[string]any
	require.NoError(t, json.Unmarshal(writes[2].Content.Original, &tool))
	assert.Equal(t, false, tool["unknownNativeField"])
	assert.NotContains(t, tool, "sessionUpdate")
	assert.Equal(t, "read_file__native-call", writes[2].Span.SpanID)
	assert.True(t, writes[2].Span.Closing)
}

func TestGeminiChildWritesKeepUserContentAndRejectMissingNativeRecords(t *testing.T) {
	t.Parallel()
	session, err := decodeGeminiSession([]byte(geminiNativeMetadata + "\n" + `{"id":"native-user","type":"user","content":[{"text":"native prompt"}]}` + "\n"))
	require.NoError(t, err)
	writes, err := geminiChildWrites(session)
	require.NoError(t, err)
	require.Len(t, writes, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, writes[0].Source)
	assert.Empty(t, writes[0].Span.SpanID)
	_, err = geminiChildWrites(geminiSession{Messages: []geminiMessage{{ID: "native"}}})
	assert.Error(t, err)
}
