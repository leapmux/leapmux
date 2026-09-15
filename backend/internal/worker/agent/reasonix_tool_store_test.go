package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reasonixSupplementRecord decodes the tool record out of one built supplement.
//
// It reads the envelope key from the contract, so the wrapper the worker writes and
// the wrapper the browser opens stay one key.
func reasonixSupplementRecord(t testing.TB, supplement []byte) reasonixToolRecord {
	t.Helper()
	var envelope struct {
		RawOutput map[string]json.RawMessage `json:"rawOutput"`
	}
	require.NoError(t, json.Unmarshal(supplement, &envelope))
	require.Contains(t, envelope.RawOutput, contracts.ReasonixToolRecordEnvelope)
	var record reasonixToolRecord
	require.NoError(t, json.Unmarshal(envelope.RawOutput[contracts.ReasonixToolRecordEnvelope], &record))
	return record
}

// TestReasonixToolRecordTagsMatchTheContract pins each struct tag to its generated
// constant.
//
// A Go struct tag takes a LITERAL, so reasonixToolRecord cannot spell the contract
// constants that the browser plugin reads. Without this test a renamed field fails
// SILENTLY: the plugin reads `raw_content` and then `content`, so a tag that stopped
// matching falls through to the shorter body rather than failing the build.
func TestReasonixToolRecordTagsMatchTheContract(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		record any
		field  string
		want   string
	}{
		{reasonixToolRecordHeader{}, "Role", contracts.ReasonixToolRecordRoleField},
		{reasonixToolRecordHeader{}, "ToolCallID", contracts.ReasonixToolRecordToolCallIDField},
		{reasonixToolRecord{}, "Name", contracts.ReasonixToolRecordNameField},
		{reasonixToolRecord{}, "Content", contracts.ReasonixToolRecordContentField},
		{reasonixToolRecord{}, "RawContent", contracts.ReasonixToolRecordRawContentField},
	} {
		typ := reflect.TypeOf(tt.record)
		field, found := typ.FieldByName(tt.field)
		require.True(t, found, "%s has no field %s", typ.Name(), tt.field)
		assert.Equal(t, tt.want, field.Tag.Get("json"), "%s.%s must carry the contract field name", typ.Name(), tt.field)
	}
}

// The header's two fields must stay promoted, because the full record decodes its
// role and its tool call id through the embedded type.
func TestReasonixToolRecordPromotesItsHeader(t *testing.T) {
	t.Parallel()
	var record reasonixToolRecord
	require.NoError(t, json.Unmarshal([]byte(`{"role":"tool","tool_call_id":"call","content":"text"}`), &record))
	assert.Equal(t, contracts.ReasonixToolRecordToolRole, record.Role)
	assert.Equal(t, "call", record.ToolCallID)
	assert.Equal(t, "text", record.Content)
}

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
				records, err := readReasonixToolRecords(b.Context(), path, pending, &reasonixEventCache{})
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
	records, err := readReasonixToolRecords(t.Context(), path, map[string][]byte{"call": nil}, &reasonixEventCache{})
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
	records, err := readReasonixToolRecords(t.Context(), path, map[string][]byte{"call": nil}, &reasonixEventCache{})
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
		records, err := readReasonixToolRecords(t.Context(), path, map[string][]byte{"call": nil}, &reasonixEventCache{})
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
	records, err := readReasonixToolRecords(t.Context(), path, map[string][]byte{"call": nil}, &reasonixEventCache{})
	require.NoError(t, err)
	assert.Contains(t, string(records["call"]), "current")
	assert.NotContains(t, string(records["call"]), "old")
}

func TestReasonixToolStoreRejectsInvalidEventLogs(t *testing.T) {
	t.Parallel()
	cases := map[string][]map[string]any{
		"unknown schema":    {{"schema_version": 3, "type": "message"}},
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
			records, err := readReasonixToolRecords(t.Context(), path, map[string][]byte{"call": nil}, &reasonixEventCache{})
			require.Error(t, err)
			assert.Empty(t, records)
		})
	}
}

func TestReasonixToolStorePreservesMissingAndDamagedData(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing.jsonl")
	records, err := readReasonixToolRecords(t.Context(), path, map[string][]byte{"call": nil}, &reasonixEventCache{})
	require.NoError(t, err)
	assert.Empty(t, records)
	_, err = os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
	// A line that a terminator ENDED and that does not parse is a real corruption.
	// An unterminated one is a write in progress, which the reader waits for.
	require.NoError(t, os.WriteFile(path, []byte("{\"role\":\"tool\"\n"), 0o600))
	records, err = readReasonixToolRecords(t.Context(), path, map[string][]byte{"call": nil}, &reasonixEventCache{})
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
		records, err := readReasonixToolSupplements(t.Context(), path, map[string]MessageContent{"call": {Original: original}}, &reasonixEventCache{})
		require.NoError(t, err)
		if !tt.want {
			assert.Empty(t, records)
			continue
		}
		require.Contains(t, records, "call")
		assert.Equal(t, full, reasonixSupplementRecord(t, records["call"]).Content)
	}
}

// appendReasonixEvent adds one line to an event log a reader already consumed.
func appendReasonixEvent(t testing.TB, eventPath string, event map[string]any) {
	t.Helper()
	if _, exists := event["schema_version"]; !exists {
		event["schema_version"] = 2
	}
	encoded, err := json.Marshal(event)
	require.NoError(t, err)
	file, err := os.OpenFile(eventPath, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = file.Write(append(encoded, '\n'))
	require.NoError(t, err)
	require.NoError(t, file.Close())
}

func reasonixEventLogPath(path string) string {
	return strings.TrimSuffix(path, ".jsonl") + ".events.jsonl"
}

// A select event states no head for the main branch, so the reader has to read
// the absent name as "main". Stored raw, the empty name matched no head, and the
// reader then fell through to the NEWEST branch -- the one the select had left.
func TestReasonixToolStoreSelectsTheMainBranchWhenTheEventStatesNoHead(t *testing.T) {
	t.Parallel()
	path := reasonixFixtureEvents(t,
		map[string]any{"type": "message", "id": "first", "head": "main", "msgs": []any{reasonixFixtureRecord("selected branch")}},
		map[string]any{"type": "fork", "head": "main", "new_head": "branch", "from": "first"},
		map[string]any{"type": "message", "id": "other", "head": "branch", "parent": "first", "msgs": []any{reasonixFixtureRecord("other branch")}},
		map[string]any{"type": "select"},
	)
	records, err := readReasonixToolRecords(t.Context(), path, map[string][]byte{"call": nil}, &reasonixEventCache{})
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Contains(t, string(records["call"]), "selected branch")
	assert.NotContains(t, string(records["call"]), "other branch")
}

// One event type this build does not know must not cost the reader every tool
// result in the session. A stale branch view is the smaller failure: the reader
// that failed the whole read recorded nothing at all.
func TestReasonixToolStoreSkipsAnUnsupportedEventType(t *testing.T) {
	t.Parallel()
	for name, events := range map[string][]map[string]any{
		"current schema": {
			{"type": "future-event", "head": "main"},
			{"type": "message", "id": "first", "head": "main", "msgs": []any{reasonixFixtureRecord("stored result")}},
		},
		"legacy schema": {
			{"schema_version": 1, "type": "future-event"},
			{"schema_version": 1, "type": "append", "message_index": 0, "messages": []any{reasonixFixtureRecord("stored result")}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := reasonixFixtureEvents(t, events...)
			records, err := readReasonixToolRecords(t.Context(), path, map[string][]byte{"call": nil}, &reasonixEventCache{})
			require.NoError(t, err)
			require.Len(t, records, 1)
			assert.Contains(t, string(records["call"]), "stored result")
		})
	}
}

// Reasonix appends to this log while LeapMux reads it, so a read can land in the
// middle of a write. The torn line waits for the next read, and it must not cost
// the reader the complete lines before it.
func TestReasonixToolStoreKeepsTheRecordsBeforeATornTrailingLine(t *testing.T) {
	t.Parallel()
	path := reasonixFixtureEvents(t,
		map[string]any{"type": "message", "id": "first", "msgs": []any{reasonixFixtureRecord("stored result")}},
	)
	eventPath := reasonixEventLogPath(path)
	file, err := os.OpenFile(eventPath, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = file.WriteString(`{"schema_version":2,"type":"message","id":"seco`)
	require.NoError(t, err)
	require.NoError(t, file.Close())

	cache := &reasonixEventCache{}
	records, err := readReasonixToolRecords(t.Context(), path, map[string][]byte{"call": nil}, cache)
	require.NoError(t, err)
	require.Contains(t, records, "call")
	assert.Contains(t, string(records["call"]), "stored result")

	// The writer finishes the line, and the next read sees the whole of it.
	file, err = os.OpenFile(eventPath, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = file.WriteString(`nd","parent":"first","msgs":[{"role":"tool","tool_call_id":"call","name":"read_file","content":"second result"}]}` + "\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())

	records, err = readReasonixToolRecords(t.Context(), path, map[string][]byte{"call": nil}, cache)
	require.NoError(t, err)
	require.Contains(t, records, "call")
	assert.Contains(t, string(records["call"]), "second result")
}

// The reader keeps the graph it built and applies only the lines a later read
// adds. A replay from the first byte on every agent message outgrew the read's
// own time budget, and a read that ran out of time enriched nothing.
func TestReasonixToolStoreAppliesOnlyTheLinesOneReadAdds(t *testing.T) {
	t.Parallel()
	path := reasonixFixtureEvents(t,
		map[string]any{"type": "message", "id": "first", "msgs": []any{map[string]any{"role": "assistant", "content": "history"}}},
	)
	eventPath := reasonixEventLogPath(path)
	cache := &reasonixEventCache{}
	pending := map[string][]byte{"call": nil}

	records, err := readReasonixToolRecords(t.Context(), path, pending, cache)
	require.NoError(t, err)
	assert.Empty(t, records)
	consumed := cache.offset
	require.Positive(t, consumed)

	// Garbage of the same length where the first line was. A reader that replays
	// from the first byte fails on it; one that resumes never looks at it.
	file, err := os.OpenFile(eventPath, os.O_RDWR, 0o600)
	require.NoError(t, err)
	_, err = file.WriteAt(bytes.Repeat([]byte("x"), int(consumed)-1), 0)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	appendReasonixEvent(t, eventPath, map[string]any{"type": "message", "id": "second", "parent": "first", "msgs": []any{reasonixFixtureRecord("second result")}})

	records, err = readReasonixToolRecords(t.Context(), path, pending, cache)
	require.NoError(t, err)
	require.Contains(t, records, "call")
	assert.Contains(t, string(records["call"]), "second result")
	assert.Greater(t, cache.offset, consumed, "the second read starts where the first one stopped")
}

// A log that SHRANK describes other bytes, so every stored offset is void and the
// reader starts again from the first one.
func TestReasonixToolStoreReplaysAShortenedEventLog(t *testing.T) {
	t.Parallel()
	path := reasonixFixtureEvents(t,
		map[string]any{"type": "message", "id": "first", "msgs": []any{reasonixFixtureRecord("first result")}},
		map[string]any{"type": "message", "id": "second", "parent": "first", "msgs": []any{reasonixFixtureRecord("second result")}},
	)
	eventPath := reasonixEventLogPath(path)
	cache := &reasonixEventCache{}
	pending := map[string][]byte{"call": nil}

	records, err := readReasonixToolRecords(t.Context(), path, pending, cache)
	require.NoError(t, err)
	assert.Contains(t, string(records["call"]), "second result")

	replacement, err := json.Marshal(map[string]any{"schema_version": 2, "type": "message", "id": "only", "msgs": []any{reasonixFixtureRecord("rewritten result")}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(eventPath, append(replacement, '\n'), 0o600))

	records, err = readReasonixToolRecords(t.Context(), path, pending, cache)
	require.NoError(t, err)
	require.Contains(t, records, "call")
	assert.Contains(t, string(records["call"]), "rewritten result")
}

// The home directory comes from the query, not from the process environment. A
// resumed agent carries the home directory of the row it resumed.
func TestReasonixToolStorePathResolvesAgainstTheQueryHome(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	query := StoredSessionQuery{HomeDir: home, Getenv: func(string) string { return "" }}
	root := filepath.Join(home, ".reasonix", "sessions")
	require.NoError(t, os.MkdirAll(root, 0o700))
	transcript := filepath.Join(root, "session-1.jsonl")
	require.NoError(t, os.WriteFile(transcript, []byte("{}\n"), 0o600))

	assert.Equal(t, transcript, reasonixToolStorePath(query, "session-1", filepath.Join(home, "work")))
	assert.Empty(t, reasonixToolStorePath(query, "../escape", filepath.Join(home, "work")))
	// The default path names the same file and reads nothing to do it, which is
	// what lets the transcript ask for a location on every agent message.
	assert.Equal(t, transcript, reasonixDefaultToolStorePath(query, "session-1", filepath.Join(home, "work")))
	assert.Empty(t, reasonixDefaultToolStorePath(query, "..", filepath.Join(home, "work")))
	assert.Equal(t, filepath.Join(root, "absent.jsonl"),
		reasonixDefaultToolStorePath(query, "absent", filepath.Join(home, "work")),
		"a session with no file on disk still names one, because this reads none")
}
