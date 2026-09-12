package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func piArtifactTestDirectory(t testing.TB) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "pi-mcp-output-Ab123C")
	require.NoError(t, os.Mkdir(directory, 0o700))
	return directory
}

func piArtifactTestEvent(t testing.TB, details map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type": "tool_execution_end", "toolCallId": "call", "toolName": "mcp", "isError": false,
		"result": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "Preview\n[truncated]"}},
			"details": details,
		},
	})
	require.NoError(t, err)
	return raw
}

func TestPiToolResultRetainsOriginalAndRecoversFullOutput(t *testing.T) {
	t.Parallel()
	directory := piArtifactTestDirectory(t)
	path := filepath.Join(directory, "output-1234abcd.txt")
	complete := "First line\nComplete output: 끝"
	require.NoError(t, os.WriteFile(path, []byte(complete), 0o600))
	raw, err := json.Marshal(map[string]any{
		"type": "tool_execution_end", "toolCallId": "call", "toolName": "mcpScript", "isError": false,
		"result": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "First line\n[truncated]"}, map[string]any{"type": "image", "data": "AAAA", "mimeType": "image/png"}},
			"details": map[string]any{"mode": "script", "outputGuard": map[string]any{"truncated": true, "originalBytes": len(complete), "fullOutputPath": path}},
		},
	})
	require.NoError(t, err)
	raw = append([]byte(" \t"), raw...)
	sink := &testSink{}
	a := newPiAgentWithSink(sink)
	a.handlePiToolExecutionEnd(raw)
	messages := sink.Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, raw, messages[0].Content)
	require.NotEmpty(t, messages[0].SupplementalContent)
	var supplement struct {
		ToolCallID string `json:"toolCallId"`
		ToolName   string `json:"toolName"`
		OutputFile struct {
			Path string `json:"path"`
			Text string `json:"text"`
		} `json:"outputFile"`
	}
	require.NoError(t, json.Unmarshal(messages[0].SupplementalContent, &supplement))
	assert.Equal(t, "call", supplement.ToolCallID)
	assert.Equal(t, "mcpScript", supplement.ToolName)
	assert.Equal(t, path, supplement.OutputFile.Path)
	assert.Equal(t, complete, supplement.OutputFile.Text)
}

func TestReadPiArtifactValidatesFilesAndByteCounts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		content []byte
		bytes   json.RawMessage
		maximum int
		wantErr bool
	}{
		{"exact limit", []byte("끝"), json.RawMessage(`3`), 3, false},
		{"absent byte count", []byte("full"), nil, 8, false},
		{"empty text", []byte{}, json.RawMessage(`0`), 8, false},
		{"size limit", []byte("long"), nil, 3, true},
		{"changed length", []byte("full"), json.RawMessage(`3`), 8, true},
		{"negative count", []byte("full"), json.RawMessage(`-1`), 8, true},
		{"fractional count", []byte("full"), json.RawMessage(`4.5`), 8, true},
		{"null count", []byte("full"), json.RawMessage(`null`), 8, true},
		{"invalid count", []byte("full"), json.RawMessage(`"four"`), 8, true},
		{"invalid UTF-8", []byte{0xff}, nil, 8, true},
		{"zero limit", []byte{}, nil, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(piArtifactTestDirectory(t), "output-1234abcd.txt")
			require.NoError(t, os.WriteFile(path, tc.content, 0o600))
			data, err := readPiToolArtifact(t.Context(), piArtifactReference{path: path, bytes: tc.bytes}, "output", tc.maximum)
			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, data)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.content, data)
			}
		})
	}
}

func TestReadPiArtifactRejectsOtherPathsAndSymlinks(t *testing.T) {
	t.Parallel()
	directory := piArtifactTestDirectory(t)
	valid := filepath.Join(directory, "output-1234abcd.txt")
	require.NoError(t, os.WriteFile(valid, []byte("full"), 0o600))
	link := filepath.Join(directory, "output-1234abce.txt")
	require.NoError(t, os.Symlink(valid, link))
	linkedDirectory := filepath.Join(t.TempDir(), "pi-mcp-output-Xy123Z")
	require.NoError(t, os.Symlink(directory, linkedDirectory))
	ordinary := filepath.Join(t.TempDir(), "output-1234abcd.txt")
	require.NoError(t, os.WriteFile(ordinary, []byte("full"), 0o600))
	for _, path := range []string{
		ordinary, link, filepath.Join(linkedDirectory, "output-1234abcd.txt"),
		filepath.Join(directory, "output-1234abcf.txt"), filepath.Join(directory, "other.txt"),
		"output-1234abcd.txt", filepath.Join(directory, "mcp-result-1234abcd.txt"),
	} {
		_, err := readPiToolArtifact(t.Context(), piArtifactReference{path: path}, "output", 64)
		assert.Error(t, err, path)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := readPiToolArtifact(ctx, piArtifactReference{path: valid}, "output", 64)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestPiArtifactRecoveryKeepsAvailableDataAndSavedSnapshots(t *testing.T) {
	t.Parallel()
	directory := piArtifactTestDirectory(t)
	outputPath := filepath.Join(directory, "output-1234abcd.txt")
	resultPath := filepath.Join(directory, "mcp-result-1234abcd.txt")
	require.NoError(t, os.WriteFile(outputPath, []byte("complete text"), 0o600))
	require.NoError(t, os.WriteFile(resultPath, []byte("invalid JSON"), 0o600))
	raw := piArtifactTestEvent(t, map[string]any{
		"outputGuard": map[string]any{"truncated": true, "fullOutputPath": outputPath},
		"mcpResult":   map[string]any{"omitted": true, "fullResultPath": resultPath},
	})
	first, complete, err := recoverPiToolArtifacts(t.Context(), raw, nil)
	require.Error(t, err)
	assert.False(t, complete)
	require.NotEmpty(t, first)
	assert.Contains(t, string(first), "complete text")
	require.NoError(t, os.Remove(outputPath))
	result := []byte(`{"content":[{"type":"image","mimeType":"image/png","data":"AAAA"}],"structuredContent":{"count":0}}`)
	require.NoError(t, os.WriteFile(resultPath, result, 0o600))
	second, complete, err := recoverPiToolArtifacts(t.Context(), raw, first)
	require.NoError(t, err)
	assert.True(t, complete)
	assert.Contains(t, string(second), "complete text")
	assert.Contains(t, string(second), `"count":0`)
	require.NoError(t, os.Remove(resultPath))
	third, complete, err := recoverPiToolArtifacts(t.Context(), raw, second)
	require.NoError(t, err)
	assert.True(t, complete)
	assert.JSONEq(t, string(second), string(third))
}

func TestPiArtifactRecoveryFollowsSessionIdentityAndFinalBoundaries(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"first identity", "new session", "cancelled context", "next boundary"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(piArtifactTestDirectory(t), "output-1234abcd.txt")
			raw := piArtifactTestEvent(t, map[string]any{"outputGuard": map[string]any{"truncated": true, "fullOutputPath": path}})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			sink := &testSink{}
			transcript := newPiToolTranscript(ctx, sink)
			if scenario == "new session" {
				transcript.UpdateSessionID("first")
			}
			if scenario == "cancelled context" {
				cancel()
			}
			require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: raw}, SpanInfo{SpanID: "call", Closing: true}))
			assert.Empty(t, sink.Messages()[0].SupplementalContent)
			if scenario == "new session" {
				transcript.UpdateSessionID("second")
			}
			require.NoError(t, os.WriteFile(path, []byte("complete text"), 0o600))
			if scenario == "first identity" {
				transcript.UpdateSessionID("first")
			}
			require.NoError(t, transcript.PersistTurnEnd(MessageContent{Original: []byte(`{"type":"agent_end"}`)}, SpanInfo{}))
			stored := sink.Messages()[0]
			assert.Equal(t, raw, stored.Content)
			if scenario == "new session" {
				assert.Empty(t, stored.SupplementalContent)
			} else {
				assert.Contains(t, string(stored.SupplementalContent), "complete text")
			}
		})
	}
}

func TestPiArtifactRecoveryKeepsTheNativeResultWithinTheCombinedLimit(t *testing.T) {
	t.Parallel()
	directory := piArtifactTestDirectory(t)
	outputPath := filepath.Join(directory, "output-1234abcd.txt")
	resultPath := filepath.Join(directory, "mcp-result-1234abcd.txt")
	require.NoError(t, os.WriteFile(outputPath, []byte(strings.Repeat("x", 40000)), 0o600))
	result, err := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": strings.Repeat("y", 20000)}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(resultPath, result, 0o600))
	raw := piArtifactTestEvent(t, map[string]any{
		"outputGuard": map[string]any{"truncated": true, "fullOutputPath": outputPath},
		"mcpResult":   map[string]any{"omitted": true, "fullResultPath": resultPath},
	})
	const remaining = 30000
	padding := liveStdoutMaxTokenSize() - 1024 - remaining - len(raw) - len(`,"padding":""`)
	require.Positive(t, padding)
	raw = []byte(string(raw[:len(raw)-1]) + `,"padding":"` + strings.Repeat("x", padding) + `"}`)
	extra, complete, err := recoverPiToolArtifacts(t.Context(), raw, nil)
	require.Error(t, err)
	assert.False(t, complete)
	require.NotEmpty(t, extra)
	assert.LessOrEqual(t, len(extra), remaining)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(extra, &fields))
	assert.NotEmpty(t, fields["mcpResultFile"])
	assert.Empty(t, fields["outputFile"])
}

func BenchmarkPiToolArtifactRecovery(b *testing.B) {
	path := filepath.Join(piArtifactTestDirectory(b), "output-1234abcd.txt")
	data := []byte(strings.Repeat("output line\n", 10000))
	require.NoError(b, os.WriteFile(path, data, 0o600))
	raw := piArtifactTestEvent(b, map[string]any{"outputGuard": map[string]any{"truncated": true, "fullOutputPath": path, "originalBytes": len(data)}})
	ctx := context.Background()
	saved, complete, err := recoverPiToolArtifacts(ctx, raw, nil)
	require.NoError(b, err)
	require.True(b, complete)
	for _, scenario := range []struct {
		name     string
		original []byte
		saved    []byte
	}{
		{"initial read", raw, nil},
		{"saved snapshot", raw, saved},
		{"no artifact", []byte(`{"type":"tool_execution_end","toolCallId":"call","toolName":"read","result":{"content":[{"type":"text","text":"` + strings.Repeat("x", 51200) + `"}],"details":{}}}`), nil},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _, err := recoverPiToolArtifacts(ctx, scenario.original, scenario.saved)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
