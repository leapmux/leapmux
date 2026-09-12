package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reasonixFixtureRecord(text string) map[string]any {
	return map[string]any{"role": "tool", "tool_call_id": "call", "name": "read_file", "content": text}
}

func reasonixFixtureEvents(t testing.TB, events ...map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	var content []byte
	for _, event := range events {
		if _, exists := event["schema_version"]; !exists {
			event["schema_version"] = 2
		}
		encoded, err := json.Marshal(event)
		require.NoError(t, err)
		content = append(content, encoded...)
		content = append(content, '\n')
	}
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(path), "session.events.jsonl"), content, 0o600))
	return path
}

func BenchmarkReasonixToolRecords(b *testing.B) {
	for _, count := range []int{200, 2000, 20000} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			events := make([]map[string]any, 0, count)
			parent := ""
			for index := range count {
				id := strconv.Itoa(index)
				message := map[string]any{"role": "assistant", "content": strings.Repeat("history ", 64)}
				if index == count-1 {
					message = reasonixFixtureRecord("requested result")
				}
				events = append(events, map[string]any{"type": "message", "id": id, "parent": parent, "msgs": []any{message}})
				parent = id
			}
			path := reasonixFixtureEvents(b, events...)
			pending := map[string][]byte{"call": nil}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				records, err := readReasonixToolRecords(b.Context(), path, pending)
				if err != nil || len(records) != 1 {
					b.Fatalf("Read tool records: count=%d error=%v", len(records), err)
				}
			}
		})
	}
}

func TestReasonixToolStoreSelectsTheActiveBranch(t *testing.T) {
	t.Parallel()
	first := reasonixFixtureRecord("selected branch")
	other := reasonixFixtureRecord("other branch")
	path := reasonixFixtureEvents(t,
		map[string]any{"type": "message", "id": "first", "head": "main", "msgs": []any{first}},
		map[string]any{"type": "fork", "head": "main", "new_head": "branch", "from": "first"},
		map[string]any{"type": "message", "id": "other", "head": "branch", "parent": "first", "msgs": []any{other}},
		map[string]any{"type": "select", "head": "main"},
	)
	records, err := readReasonixToolRecords(t.Context(), path, map[string][]byte{"call": nil})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Contains(t, string(records["call"]), "selected branch")
	assert.NotContains(t, string(records["call"]), "other branch")
}

func TestReasonixToolStoreAppliesRewindsAndRetirements(t *testing.T) {
	t.Parallel()
	path := reasonixFixtureEvents(t,
		map[string]any{"type": "message", "id": "first", "head": "main", "msgs": []any{reasonixFixtureRecord("first")}},
		map[string]any{"type": "message", "id": "second", "head": "main", "parent": "first", "msgs": []any{reasonixFixtureRecord("second")}},
		map[string]any{"type": "fork", "head": "main", "new_head": "branch", "from": "second"},
		map[string]any{"type": "select", "head": "branch"},
		map[string]any{"type": "retire", "head": "branch"},
		map[string]any{"type": "rewind", "head": "main", "to": "first"},
	)
	records, err := readReasonixToolRecords(t.Context(), path, map[string][]byte{"call": nil})
	require.NoError(t, err)
	assert.Contains(t, string(records["call"]), `"first"`)
	assert.NotContains(t, string(records["call"]), `"second"`)
}

func TestReasonixToolStoreAppliesPatchesAndRedactions(t *testing.T) {
	t.Parallel()
	for _, redacted := range []bool{false, true} {
		events := []map[string]any{
			{"type": "message", "id": "first", "msgs": []any{reasonixFixtureRecord("original")}},
			{"type": "patch", "target": "first", "msgs": []any{reasonixFixtureRecord("patched")}},
		}
		if redacted {
			events = append(events, map[string]any{"type": "redact", "targets": map[string]any{"first": []any{map[string]any{"role": "assistant", "content": "removed"}}}})
		}
		path := reasonixFixtureEvents(t, events...)
		records, err := readReasonixToolRecords(t.Context(), path, map[string][]byte{"call": nil})
		require.NoError(t, err)
		if redacted {
			assert.Empty(t, records)
		} else {
			assert.Contains(t, string(records["call"]), "patched")
		}
	}
}

func TestReasonixToolStoreReadsLegacyEventLogs(t *testing.T) {
	t.Parallel()
	path := reasonixFixtureEvents(t,
		map[string]any{"schema_version": 1, "type": "replace", "messages": []any{reasonixFixtureRecord("old")}},
		map[string]any{"schema_version": 1, "type": "replace", "messages": []any{}},
		map[string]any{"schema_version": 1, "type": "append", "message_index": 0, "messages": []any{reasonixFixtureRecord("current")}},
	)
	records, err := readReasonixToolRecords(t.Context(), path, map[string][]byte{"call": nil})
	require.NoError(t, err)
	assert.Contains(t, string(records["call"]), "current")
	assert.NotContains(t, string(records["call"]), "old")
}

func TestReasonixToolStoreRejectsInvalidEventLogs(t *testing.T) {
	t.Parallel()
	cases := map[string][]map[string]any{
		"unknown schema":    {{"schema_version": 3, "type": "message"}},
		"unknown event":     {{"type": "future-event"}},
		"missing identity":  {{"type": "message", "msgs": []any{reasonixFixtureRecord("bad")}}},
		"multiple messages": {{"type": "message", "id": "first", "msgs": []any{reasonixFixtureRecord("first"), reasonixFixtureRecord("second")}}},
		"append gap":        {{"schema_version": 1, "type": "append", "message_index": 5}},
		"cycle": {
			{"type": "message", "id": "one", "parent": "two", "msgs": []any{reasonixFixtureRecord("one")}},
			{"type": "message", "id": "two", "parent": "one", "msgs": []any{reasonixFixtureRecord("two")}},
		},
	}
	for name, events := range cases {
		t.Run(name, func(t *testing.T) {
			path := reasonixFixtureEvents(t, events...)
			records, err := readReasonixToolRecords(t.Context(), path, map[string][]byte{"call": nil})
			require.Error(t, err)
			assert.Empty(t, records)
		})
	}
}

func TestReasonixToolStorePreservesMissingAndDamagedData(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing.jsonl")
	records, err := readReasonixToolRecords(t.Context(), path, map[string][]byte{"call": nil})
	require.NoError(t, err)
	assert.Empty(t, records)
	_, err = os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, os.WriteFile(path, []byte(`{"role":"tool"`), 0o600))
	records, err = readReasonixToolRecords(t.Context(), path, map[string][]byte{"call": nil})
	require.Error(t, err)
	assert.Empty(t, records)
}

func TestReasonixToolResultMatchesTheProtocolClip(t *testing.T) {
	t.Parallel()
	assert.True(t, reasonixResultMatches("complete", "complete"))
	assert.True(t, reasonixResultMatches("界\n…(3 more chars truncated)", "界界"))
	assert.False(t, reasonixResultMatches("界\n…(2 more chars truncated)", "界界"))
	assert.False(t, reasonixResultMatches("one\n…(4 more chars truncated)", "twofour"))
	assert.False(t, reasonixResultMatches("one\n…(999999999999999999999999999999 more chars truncated)", "one"))
	assert.False(t, reasonixResultMatches("one\n…(0 more chars truncated)", "one"))
}

func TestReasonixToolStoreRecoversTheFullErrorBehindTheACPHeadline(t *testing.T) {
	t.Parallel()
	full := "error: context canceled\nSubagent outcome: status=cancelled retryable=false\n\nFinal answer:\nPartial report"
	path := reasonixFixtureEvents(t, map[string]any{"type": "message", "id": "result", "msgs": []any{reasonixFixtureRecord(full)}})
	for _, tt := range []struct {
		status, text string
		want         bool
	}{
		{"failed", "context canceled", true},
		{"failed", "different failure", false},
		{"completed", "context canceled", false},
	} {
		original, err := json.Marshal(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "call", "status": tt.status, "content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": tt.text}}}})
		require.NoError(t, err)
		records, err := readReasonixToolSupplements(t.Context(), path, map[string]MessageContent{"call": {Original: original}})
		require.NoError(t, err)
		if !tt.want {
			assert.Empty(t, records)
			continue
		}
		require.Contains(t, records, "call")
		var supplement struct {
			RawOutput struct {
				Reasonix reasonixToolRecord `json:"reasonix"`
			} `json:"rawOutput"`
		}
		require.NoError(t, json.Unmarshal(records["call"], &supplement))
		assert.Equal(t, full, supplement.RawOutput.Reasonix.Content)
	}
}
