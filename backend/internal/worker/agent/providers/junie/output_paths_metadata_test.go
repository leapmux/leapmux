package junie

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJunieOutputPathResolvesTheExactNativeResult(t *testing.T) {
	t.Parallel()
	f := newJunieOutputPathFixture(t)
	extra, err := readJunieOutputPath(t.Context(), f.home, f.cwd, f.sessionID, f.original)
	require.NoError(t, err)
	resolved := (junieProvider{}).ResolveProviderData(agent.MessageContent{Original: f.original, Supplemental: extra, AgentSessionID: f.sessionID})
	assert.Equal(t, f.original, resolved, "a path receipt must not replace the provider's inline preview")
	assert.NotContains(t, string(extra), `"nativeOutput"`)
	assert.NotContains(t, string(extra), `"output":`)
}

func TestJunieOutputPathRefusesAnAbsentReceiptExitCode(t *testing.T) {
	t.Parallel()
	f := newJunieOutputPathFixture(t)
	for _, exit := range []any{nil, map[string]any{}, map[string]any{"terminal_id": f.callID, "exit_code": nil}} {
		var frame map[string]any
		require.NoError(t, json.Unmarshal(f.original, &frame))
		frame["_meta"] = map[string]any{"terminal_exit": exit}
		original, err := json.Marshal(frame)
		require.NoError(t, err)
		extra, err := readJunieOutputPath(t.Context(), f.home, f.cwd, f.sessionID, original)
		require.NoError(t, err)
		assert.Empty(t, extra)
	}

}

func TestJunieOutputPathRecoversOnlyItsNativeArchiveFile(t *testing.T) {
	t.Parallel()
	fixture := newJunieOutputPathFixture(t)
	extra, err := readJunieOutputPath(t.Context(), fixture.home, fixture.cwd, fixture.sessionID, fixture.original)
	require.NoError(t, err)
	var supplement map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(extra, &supplement))
	var receipt junieOutputFilePath
	require.NoError(t, json.Unmarshal(supplement["outputFilePath"], &receipt))
	assert.Equal(t, fixture.path, receipt.Path)
	assert.Equal(t, fixture.taskID, receipt.TaskID)
	assert.Equal(t, fixture.sessionID, receipt.SessionID)
	require.NoError(t, os.Remove(fixture.path))
	resolved := (junieProvider{}).ResolveProviderData(agent.MessageContent{Original: fixture.original, Supplemental: extra, AgentSessionID: fixture.sessionID})
	var frame junieExecuteFrame
	require.NoError(t, json.Unmarshal(resolved, &frame))
	require.NotNil(t, frame.Output.Text)
	var originalFrame junieExecuteFrame
	require.NoError(t, json.Unmarshal(fixture.original, &originalFrame))
	assert.Equal(t, originalFrame.Output.Text, frame.Output.Text, "the stored original preview survives file deletion")
}

func TestJunieOutputPathPreservesNativeUTF16Previews(t *testing.T) {
	t.Parallel()
	for _, preview := range []string{"", "zero", "尾😀", strings.Repeat("x", 65536), "A" + strings.Repeat("😀", 16383) + "\uFFFD" + "\n[native omitted preview]\n" + "\uFFFD" + strings.Repeat("😀", 16383) + "Z"} {
		t.Run("native preview", func(t *testing.T) {
			f := newJunieOutputPathFixture(t)
			var frame map[string]any
			require.NoError(t, json.Unmarshal(f.original, &frame))
			frame["rawOutput"] = map[string]any{"output": preview}
			original, err := json.Marshal(frame)
			require.NoError(t, err)
			resolved := (junieProvider{}).ResolveProviderData(agent.MessageContent{Original: original})
			assert.Equal(t, original, resolved, "native UTF-16 cut previews must remain exact")
		})
	}
}

func TestJunieOutputPathRejectsForeignAndDuplicateNativeEvents(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(map[string]any, map[string]any)
	}{
		{name: "foreign main agent", change: func(_ map[string]any, terminal map[string]any) {
			terminal["agent"] = map[string]any{"kind": "CustomAgent", "id": "main"}
		}},
		{name: "foreign agent id", change: func(_ map[string]any, terminal map[string]any) {
			terminal["agent"] = map[string]any{"kind": "MainAgent", "id": "another"}
		}},
		{name: "foreign command", change: func(_ map[string]any, terminal map[string]any) { terminal["command"] = "another-command" }},
		{name: "different snapshot", change: func(_ map[string]any, terminal map[string]any) { terminal["output"] = "another snapshot" }},
		{name: "failed exit", change: func(_ map[string]any, terminal map[string]any) { terminal["exitCode"] = 1 }},
		{name: "absent exit", change: func(_ map[string]any, terminal map[string]any) { delete(terminal, "exitCode") }},
		{name: "nested task", change: func(event map[string]any, _ map[string]any) { event["taskId"] = "task-261003-225242-1bq0/nested" }},
		{name: "foreign task", change: func(event map[string]any, _ map[string]any) { event["taskId"] = "task-261003-225243-1bq0" }},
		{name: "relative path", change: func(_ map[string]any, terminal map[string]any) {
			terminal["outputFile"] = map[string]any{"relativePath": "terminal-output-123.txt"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newJunieOutputPathFixture(t)
			event, ok := fixture.event["event"].(map[string]any)
			require.True(t, ok)
			terminal, ok := event["agentEvent"].(map[string]any)
			require.True(t, ok)
			tc.change(fixture.event, terminal)
			writeJunieOutputPathEvents(t, fixture, fixture.event)
			extra, err := readJunieOutputPath(t.Context(), fixture.home, fixture.cwd, fixture.sessionID, fixture.original)
			require.Error(t, err)
			assert.Empty(t, extra)
		})
	}
	t.Run("duplicate completed event", func(t *testing.T) {
		fixture := newJunieOutputPathFixture(t)
		raw, err := json.Marshal(fixture.event)
		require.NoError(t, err)
		var conflicting map[string]any
		require.NoError(t, json.Unmarshal(raw, &conflicting))
		conflicting["taskId"] = "task-261003-225243-1bq0"
		writeJunieOutputPathEvents(t, fixture, fixture.event, conflicting)
		extra, err := readJunieOutputPath(t.Context(), fixture.home, fixture.cwd, fixture.sessionID, fixture.original)
		require.ErrorContains(t, err, "duplicate")
		assert.Empty(t, extra)
	})
}

func TestJunieOutputPathAcceptsRepeatedCompletedEventsWithTheSameNativeOwner(t *testing.T) {
	t.Parallel()
	fixture := newJunieOutputPathFixture(t)
	raw, err := json.Marshal(fixture.event)
	require.NoError(t, err)
	var completedTask map[string]any
	require.NoError(t, json.Unmarshal(raw, &completedTask))
	runningEvent, ok := fixture.event["event"].(map[string]any)
	require.True(t, ok)
	runningEvent["state"] = "IN_PROGRESS"
	fixture.event["timestampMs"] = int64(1791044052953)
	completedEvent, ok := completedTask["event"].(map[string]any)
	require.True(t, ok)
	completedEvent["state"] = "COMPLETED"
	completedTask["timestampMs"] = int64(1791044054254)
	writeJunieOutputPathEvents(t, fixture, fixture.event, completedTask)
	extra, err := readJunieOutputPath(t.Context(), fixture.home, fixture.cwd, fixture.sessionID, fixture.original)
	require.NoError(t, err, "repeated native snapshots with the same completed owner identify one saved result")
	require.NotEmpty(t, extra)
	var supplement map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(extra, &supplement))
	var receipt junieOutputFilePath
	require.NoError(t, json.Unmarshal(supplement["outputFilePath"], &receipt))
	assert.Equal(t, fixture.path, receipt.Path)
}

func TestJunieOutputPathRefusesConflictingCompletedReplays(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(map[string]any, map[string]any)
	}{
		{name: "task", change: func(event map[string]any, _ map[string]any) { event["taskId"] = "task-261003-225243-1bq0" }},
		{name: "path", change: func(_ map[string]any, terminal map[string]any) {
			terminal["outputFile"] = map[string]any{"relativePath": "/foreign/terminal-output-123.txt"}
		}},
		{name: "command", change: func(_ map[string]any, terminal map[string]any) { terminal["command"] = "another-command" }},
		{name: "snapshot", change: func(_ map[string]any, terminal map[string]any) { terminal["output"] = "another snapshot" }},
		{name: "exit", change: func(_ map[string]any, terminal map[string]any) { terminal["exitCode"] = 1 }},
		{name: "agent", change: func(_ map[string]any, terminal map[string]any) {
			terminal["agent"] = map[string]any{"kind": "MainAgent", "id": "another"}
		}},
		{name: "agent kind", change: func(_ map[string]any, terminal map[string]any) {
			terminal["agent"] = map[string]any{"kind": "CustomAgent", "id": "main"}
		}},
		{name: "missing snapshot", change: func(_ map[string]any, terminal map[string]any) { delete(terminal, "output") }},
		{name: "missing exit", change: func(_ map[string]any, terminal map[string]any) { delete(terminal, "exitCode") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newJunieOutputPathFixture(t)
			raw, err := json.Marshal(fixture.event)
			require.NoError(t, err)
			var conflicting map[string]any
			require.NoError(t, json.Unmarshal(raw, &conflicting))
			event, ok := conflicting["event"].(map[string]any)
			require.True(t, ok)
			terminal, ok := event["agentEvent"].(map[string]any)
			require.True(t, ok)
			tc.change(conflicting, terminal)
			writeJunieOutputPathEvents(t, fixture, fixture.event, conflicting)
			extra, err := readJunieOutputPath(t.Context(), fixture.home, fixture.cwd, fixture.sessionID, fixture.original)
			require.Error(t, err)
			assert.Empty(t, extra)
		})
	}
}

func TestJunieOutputPathChecksMetadataWithoutOpeningTheLog(t *testing.T) {
	t.Parallel()
	for _, depth := range []int{1, 2, 3, 4, 5} {
		t.Run(fmt.Sprintf("symlink depth %d", depth), func(t *testing.T) {
			t.Parallel()
			fixture := newJunieOutputPathFixture(t)
			parts := []string{"sessions", fixture.sessionID, fixture.taskID, "terminal-output", "terminal-output-123.txt"}
			path := filepath.Join(append([]string{fixture.home}, parts[:depth]...)...)
			realPath := path + ".real"
			require.NoError(t, os.Rename(path, realPath))
			require.NoError(t, os.Symlink(realPath, path))
			extra, err := readJunieOutputPath(t.Context(), fixture.home, fixture.cwd, fixture.sessionID, fixture.original)
			if depth <= 2 {
				require.ErrorContains(t, err, "symlink")
				assert.Empty(t, extra)
			} else {
				require.NoError(t, err)
				assert.NotEmpty(t, extra, "the output path does not require a log read")
			}
		})
	}
	t.Run("changed saved bytes", func(t *testing.T) {
		fixture := newJunieOutputPathFixture(t)
		require.NoError(t, os.WriteFile(fixture.path, []byte("another call's output"), 0o600))
		extra, err := readJunieOutputPath(t.Context(), fixture.home, fixture.cwd, fixture.sessionID, fixture.original)
		require.NoError(t, err)
		assert.NotEmpty(t, extra, "changed external bytes do not change native path metadata")
	})
}

type junieChangingArchive struct {
	sessionstore.ArchiveRoot
	prefix  string
	fixture junieOutputPathFixture
	mode    string
	stats   *int
}

func (r junieChangingArchive) OpenChild(name string) (sessionstore.ArchiveRoot, error) {
	child, err := r.ArchiveRoot.OpenChild(name)
	if err != nil {
		return nil, err
	}
	r.ArchiveRoot, r.prefix = child, filepath.Join(r.prefix, name)
	return r, nil
}

func (r junieChangingArchive) Lstat(name string) (os.FileInfo, error) {
	if name == "summary.json" {
		*r.stats++
		if r.mode == "during read" && *r.stats == 2 {
			if err := os.WriteFile(filepath.Join(r.fixture.home, "sessions", r.fixture.sessionID, "summary.json"), []byte(`{"sessionId":"`+r.fixture.sessionID+`","projectDir":"`+r.fixture.cwd+`"}`), 0o600); err != nil {
				return nil, err
			}
			if err := os.Chtimes(filepath.Join(r.fixture.home, "sessions", r.fixture.sessionID, "summary.json"), time.Unix(3000, 0), time.Unix(3000, 0)); err != nil {
				return nil, err
			}
		}
	}
	return r.ArchiveRoot.Lstat(name)
}

func (r junieChangingArchive) Open(name string) (*os.File, error) {
	if name == "summary.json" {
		switch r.mode {
		case "before open":
			if err := os.Rename(filepath.Join(r.fixture.home, "sessions", r.fixture.sessionID, "summary.json"), filepath.Join(r.fixture.home, "sessions", r.fixture.sessionID, "summary.json")+".replaced"); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(r.fixture.home, "sessions", r.fixture.sessionID, "summary.json"), []byte(`{"sessionId":"`+r.fixture.sessionID+`","projectDir":"`+r.fixture.cwd+`"}`), 0o600); err != nil {
				return nil, err
			}
		case "read error":
			return os.OpenFile(filepath.Join(r.fixture.home, "sessions", r.fixture.sessionID, "summary.json"), os.O_WRONLY, 0o600)
		case "growth":
			if err := os.WriteFile(filepath.Join(r.fixture.home, "sessions", r.fixture.sessionID, "summary.json"), []byte(strings.Repeat("x", (1<<20)+1)), 0o600); err != nil {
				return nil, err
			}
		}
	}
	return r.ArchiveRoot.Open(name)
}

func (r junieChangingArchive) Close() error {
	err := r.ArchiveRoot.Close()
	if r.mode == "close error" && strings.HasSuffix(r.prefix, r.fixture.sessionID) {
		return errors.Join(err, errors.New("the test archive close failed"))
	}
	return err
}

func TestJunieOutputPathRejectsReadMutationErrorsAndSizeLimits(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"before open", "during read", "read error", "close error", "growth", "size limit", "zero limit", "negative limit", "encoded size limit", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fixture := newJunieOutputPathFixture(t)
			require.NoError(t, os.Chtimes(fixture.path, time.Unix(2000, 0), time.Unix(2000, 0)))
			root, err := sessionstore.OpenArchiveRoot(fixture.home)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })
			stats := 0
			wrapped := junieChangingArchive{ArchiveRoot: root, fixture: fixture, mode: mode, stats: &stats}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			maximum := 1 << 20
			if mode == "size limit" {
				maximum = 1
			}
			if mode == "zero limit" {
				maximum = 0
			}
			if mode == "negative limit" {
				maximum = -1
			}
			if mode == "encoded size limit" {
				maximum = len(fixture.path)
			}
			if mode == "cancelled" {
				cancel()
			}
			extra, err := readJunieOutputPathFromRoot(ctx, wrapped, fixture.home, fixture.cwd, fixture.sessionID, fixture.original, maximum)
			require.Error(t, err)
			assert.Empty(t, extra)
		})
	}
}

func TestJunieOutputPathPreservesEmptyAndUnicodeNativePreviews(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, output string }{
		{name: "empty", output: ""},
		{name: "unicode", output: "尾😀\nzero:false"},
		{name: "unicode cuts", output: "A" + strings.Repeat("😀", 40000) + "Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			output := tc.output
			fixture := newJunieOutputPathFixture(t)
			preview := output
			var frame map[string]any
			require.NoError(t, json.Unmarshal(fixture.original, &frame))
			frame["rawOutput"] = map[string]any{"output": preview}
			var err error
			fixture.original, err = json.Marshal(frame)
			require.NoError(t, err)
			event, ok := fixture.event["event"].(map[string]any)
			require.True(t, ok)
			terminal, ok := event["agentEvent"].(map[string]any)
			require.True(t, ok)
			terminal["output"] = preview
			writeJunieOutputPathEvents(t, fixture, fixture.event)
			require.NoError(t, os.WriteFile(fixture.path, []byte(output), 0o600))
			extra, err := readJunieOutputPath(t.Context(), fixture.home, fixture.cwd, fixture.sessionID, fixture.original)
			require.NoError(t, err)
			var supplement map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(extra, &supplement))
			var receipt junieOutputFilePath
			require.NoError(t, json.Unmarshal(supplement["outputFilePath"], &receipt))
			assert.Equal(t, fixture.path, receipt.Path)
			assert.Equal(t, fixture.original, (junieProvider{}).ResolveProviderData(agent.MessageContent{Original: fixture.original, Supplemental: extra}))
		})
	}
}

func TestJunieOutputPathRejectsWrongSummaryAndNativeExitAuthority(t *testing.T) {
	t.Parallel()
	for _, value := range []map[string]any{
		{"sessionId": "session-foreign", "projectDir": "/foreign"},
		{"sessionId": "session-261003-225241-1cba", "projectDir": "/foreign"},
	} {
		t.Run("foreign summary", func(t *testing.T) {
			fixture := newJunieOutputPathFixture(t)
			raw, err := json.Marshal(value)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(fixture.home, "sessions", fixture.sessionID, "summary.json"), raw, 0o600))
			extra, err := readJunieOutputPath(t.Context(), fixture.home, fixture.cwd, fixture.sessionID, fixture.original)
			require.ErrorContains(t, err, "another session or project")
			assert.Empty(t, extra)
		})
	}
	fixture := newJunieOutputPathFixture(t)
	for _, exit := range []map[string]any{
		{},
		{"terminal_id": fixture.callID, "exit_code": 1},
		{"terminal_id": "another-call", "exit_code": 0},
		{"terminal_id": fixture.callID, "exit_code": 0, "signal": "SIGTERM"},
	} {
		var frame map[string]any
		require.NoError(t, json.Unmarshal(fixture.original, &frame))
		frame["_meta"] = map[string]any{"terminal_exit": exit}
		original, err := json.Marshal(frame)
		require.NoError(t, err)
		extra, err := readJunieOutputPath(t.Context(), fixture.home, fixture.cwd, fixture.sessionID, original)
		require.NoError(t, err)
		assert.Empty(t, extra)
	}
}

func TestJunieOutputPathHandlesMissingMalformedAndUnfinishedRecords(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing", "malformed", "unfinished", "foreign call", "invalid utf8", "partial utf8 tail"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fixture := newJunieOutputPathFixture(t)
			path := filepath.Join(fixture.home, "sessions", fixture.sessionID, "events.jsonl")
			switch mode {
			case "missing":
				require.NoError(t, os.Remove(path))
			case "malformed":
				require.NoError(t, os.WriteFile(path, []byte("{invalid}\n"), 0o600))
			case "unfinished":
				raw, err := json.Marshal(fixture.event)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, raw, 0o600))
			case "foreign call":
				event, ok := fixture.event["event"].(map[string]any)
				require.True(t, ok)
				terminal, ok := event["agentEvent"].(map[string]any)
				require.True(t, ok)
				terminal["stepId"] = "another-call"
				writeJunieOutputPathEvents(t, fixture, fixture.event)
			case "invalid utf8":
				require.NoError(t, os.WriteFile(path, []byte{0xff, '\n'}, 0o600))
			case "partial utf8 tail":
				require.NoError(t, os.WriteFile(path, []byte{0xff}, 0o600))
			}
			extra, err := readJunieOutputPath(t.Context(), fixture.home, fixture.cwd, fixture.sessionID, fixture.original)
			if mode == "unfinished" || mode == "foreign call" || mode == "partial utf8 tail" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			assert.Empty(t, extra)
		})
	}
}

func TestJunieOutputPathIgnoresUnrelatedNativeEventFields(t *testing.T) {
	t.Parallel()
	fixture := newJunieOutputPathFixture(t)
	unrelated := map[string]any{"kind": "SessionA2uxEvent", "taskId": fixture.taskID, "event": map[string]any{"agentEvent": map[string]any{
		"kind": "AnotherNativeEvent", "output": map[string]any{"value": false}, "command": []any{"different shape"},
	}}}
	writeJunieOutputPathEvents(t, fixture, unrelated, fixture.event)
	extra, err := readJunieOutputPath(t.Context(), fixture.home, fixture.cwd, fixture.sessionID, fixture.original)
	require.NoError(t, err, "another event kind must not decode through the terminal fields")
	require.NotEmpty(t, extra)
}

func TestJunieOutputPathRequiredValuesDistinguishZeroAndEmptyFromAbsence(t *testing.T) {
	t.Parallel()
	f := newJunieOutputPathFixture(t)
	var frame map[string]any
	require.NoError(t, json.Unmarshal(f.original, &frame))
	frame["rawOutput"] = map[string]any{"output": "", "exitCode": 0}
	raw, err := json.Marshal(frame)
	require.NoError(t, err)
	parsed, valid := junieCompletedExecute(raw)
	require.True(t, valid)
	require.NotNil(t, parsed.Output.Text)
	assert.Empty(t, *parsed.Output.Text)
	for _, mode := range []string{"missing", "null"} {
		t.Run(mode, func(t *testing.T) {
			frame["rawOutput"] = map[string]any{}
			if mode == "null" {
				frame["rawOutput"] = map[string]any{"output": nil}
			}
			raw, err := json.Marshal(frame)
			require.NoError(t, err)
			_, valid := junieCompletedExecute(raw)
			assert.False(t, valid)
		})
	}
}

func TestJunieOutputPathKeepsCompletedMetadataBeforeAPartialUTF8Tail(t *testing.T) {
	t.Parallel()
	fixture := newJunieOutputPathFixture(t)
	path := filepath.Join(fixture.home, "sessions", fixture.sessionID, "events.jsonl")
	completed, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(completed, 0xff), 0o600))
	extra, err := readJunieOutputPath(t.Context(), fixture.home, fixture.cwd, fixture.sessionID, fixture.original)
	require.NoError(t, err)
	require.NotEmpty(t, extra, "an unfinished tail must not discard an earlier completed native event")
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(extra, &fields))
	var receipt junieOutputFilePath
	require.NoError(t, json.Unmarshal(fields["outputFilePath"], &receipt))
	assert.Equal(t, fixture.path, receipt.Path)
	assert.Equal(t, fixture.callID, receipt.CallID)
}
