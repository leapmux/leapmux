package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPiGoalSessionFollowsActiveBranch(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "session.jsonl")
	content := `{"type":"session","id":"session","version":3}` + "\n" +
		`{"type":"custom","id":"focus-a","parentId":null,"customType":"pi-goal-focus","data":{"version":1,"focusedGoalId":"goal-a"}}` + "\n" +
		`{"type":"message","id":"branch-a","parentId":"focus-a","message":{"role":"assistant","content":"A"}}` + "\n" +
		`{"type":"custom","id":"focus-b","parentId":"focus-a","customType":"pi-goal-focus","data":{"version":1,"focusedGoalId":"goal-b"}}` + "\n" +
		`{"type":"custom","id":"clear","parentId":"focus-b","customType":"pi-goal-focus","data":{"version":1,"focusedGoalId":null}}` + "\n" +
		`{"type":"message","id":"unfinished"`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	var reader piGoalSessionReader
	session, err := reader.read(t.Context(), path, directory, "session")
	require.NoError(t, err)
	assert.Equal(t, "clear", session.LastID)
	for leaf, expected := range map[string]string{"branch-a": "goal-a", "focus-b": "goal-b", "clear": ""} {
		goal, known := session.focus(leaf, nil)
		assert.True(t, known)
		assert.Equal(t, expected, goal)
	}
	_, known := session.focus("unfinished", nil)
	assert.False(t, known)
	var other piGoalSessionReader
	_, err = other.read(t.Context(), path, directory, "other-session")
	require.Error(t, err)
}

func TestPiGoalSessionRejectsAmbiguousAndMissingParents(t *testing.T) {
	t.Parallel()
	session := piGoalSession{}
	require.NoError(t, session.add([]byte(`{"type":"message","id":"a","parentId":"b"}`)))
	require.NoError(t, session.add([]byte(`{"type":"message","id":"b","parentId":"a"}`)))
	_, known := session.focus("a", nil)
	assert.False(t, known)
	_, known = session.focus("missing", nil)
	assert.False(t, known)
	require.Error(t, session.add([]byte(`{"id":"a"}`)))
	require.Error(t, session.add([]byte(`{}`)))
	require.Error(t, session.add([]byte(`{invalid`)))
}

func TestPiGoalSessionReaderReadsAppendsAndDiscardsReplacements(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "session.jsonl")
	header := `{"type":"session","id":"session","version":3}` + "\n"
	first := `{"type":"message","id":"first","parentId":null}` + "\n"
	partial := `{"type":"message","id":"second"`
	require.NoError(t, os.WriteFile(path, []byte(header+first+partial), 0o600))
	var reader piGoalSessionReader
	session, err := reader.read(t.Context(), path, directory, "session")
	require.NoError(t, err)
	assert.Equal(t, "first", session.LastID)
	assert.Equal(t, int64(len(header+first)), reader.offset)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = file.WriteString(`,"parentId":"first"}` + "\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	session, err = reader.read(t.Context(), path, directory, "session")
	require.NoError(t, err)
	assert.Equal(t, "second", session.LastID)
	assert.Len(t, session.Entries, 2)
	previousOffset := reader.offset
	session, err = reader.read(t.Context(), path, directory, "session")
	require.NoError(t, err)
	assert.Equal(t, previousOffset, reader.offset)
	assert.Len(t, session.Entries, 2)
	newPath := filepath.Join(directory, "replacement.jsonl")
	require.NoError(t, os.WriteFile(newPath, []byte(header+`{"type":"message","id":"replacement","parentId":null}`+"\n"), 0o600))
	require.NoError(t, os.Rename(newPath, path))
	session, err = reader.read(t.Context(), path, directory, "session")
	require.NoError(t, err)
	assert.Equal(t, "replacement", session.LastID)
	assert.Len(t, session.Entries, 1)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = reader.read(ctx, path, directory, "session")
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, reader.session.Entries)
}

func TestParsePiGoalFileUsesNativeObjectiveRules(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, objective string
	}{
		{"structured body", "\n# Goal Prompt\n\nBody **objective**.\nSecond line.\n\n## Progress\n\n- Status: active", "Body **objective**.\nSecond line."},
		{"plain body", "\nPlain body.\n", "Plain body."},
		{"empty body", "\n", "Metadata objective"},
		{"empty prompt", "\n# Goal Prompt\n\n## Progress\nStatus", "Metadata objective"},
		{"progress without a heading", "\n## Progress\n- Status: active", "Metadata objective"},
		{"plain body before progress", "\nPlain body.\n\n## Progress\n- Status: active", "Plain body."},
		{"windows body", "\r\n# Goal Prompt\r\nFirst\r\nSecond\r\n## Progress", "First\nSecond"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			record, err := parsePiGoalFile([]byte(`{"version":3,"id":"goal","objective":"Metadata objective","status":"active"}` + tc.body))
			require.NoError(t, err)
			assert.Equal(t, tc.objective, record.Objective)
		})
	}
}

func TestReadPiGoalFileFindsOnlyTheFocusedCanonicalRecord(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	goals := filepath.Join(directory, ".pi", "goals")
	require.NoError(t, os.MkdirAll(goals, 0o700))
	content := []byte(`{"version":3,"id":"focused","objective":"Metadata","status":"paused"}` + "\n# Goal Prompt\nCurrent objective")
	require.NoError(t, os.WriteFile(filepath.Join(goals, "current.md"), content, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(goals, "unrelated.md"), []byte(`{"version":3,"id":"other","objective":"Other"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(goals, "broken.md"), []byte(`{invalid`), 0o600))
	var reader piGoalFileReader
	record, err := reader.read(t.Context(), directory, "focused")
	require.NoError(t, err)
	require.NotNil(t, record)
	assert.Equal(t, "Current objective", record.Objective)
	assert.Equal(t, "paused", record.Status)
	missing, err := reader.read(t.Context(), directory, "missing")
	require.NoError(t, err)
	assert.Nil(t, missing)
	require.NoError(t, os.WriteFile(filepath.Join(goals, "duplicate.md"), content, 0o600))
	_, err = reader.read(t.Context(), directory, "focused")
	require.ErrorContains(t, err, "multiple")
}

func TestPiGoalFileReaderReusesUnchangedFilesAndSeesEveryEdit(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	goals := filepath.Join(directory, ".pi", "goals")
	require.NoError(t, os.MkdirAll(goals, 0o700))
	path := filepath.Join(goals, "current.md")
	header := `{"version":3,"id":"focused","objective":"Metadata","status":"active"}`
	require.NoError(t, os.WriteFile(path, []byte(header+"\n# Goal Prompt\nFirst objective"), 0o600))
	stat, err := os.Stat(path)
	require.NoError(t, err)
	var reader piGoalFileReader
	first, err := reader.read(t.Context(), directory, "focused")
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, "First objective", first.Objective)

	// A file whose size AND modification time are unchanged keeps its parsed record.
	// A rewrite that preserves both is what proves the reader did not read again.
	require.NoError(t, os.WriteFile(path, []byte(header+"\n# Goal Prompt\nFIRST objective"), 0o600))
	require.NoError(t, os.Chtimes(path, stat.ModTime(), stat.ModTime()))
	again, err := reader.read(t.Context(), directory, "focused")
	require.NoError(t, err)
	require.NotNil(t, again)
	assert.Equal(t, "First objective", again.Objective)
	assert.NotSame(t, first, again, "each caller gets its own copy of the cached record")

	// A file whose size grew must be read again, although the cache holds it.
	require.NoError(t, os.WriteFile(path, []byte(header+"\n# Goal Prompt\nSecond objective, longer"), 0o600))
	grown, err := reader.read(t.Context(), directory, "focused")
	require.NoError(t, err)
	require.NotNil(t, grown)
	assert.Equal(t, "Second objective, longer", grown.Objective)

	// A file of the SAME size with a later modification time must be read again too.
	same := []byte(header + "\n# Goal Prompt\nSecond objective, LONGER")
	require.NoError(t, os.WriteFile(path, same, 0o600))
	stamp := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(path, stamp, stamp))
	edited, err := reader.read(t.Context(), directory, "focused")
	require.NoError(t, err)
	require.NotNil(t, edited)
	assert.Equal(t, "Second objective, LONGER", edited.Objective)

	// A deleted file leaves the cache, so its record cannot return as a duplicate.
	copyPath := filepath.Join(goals, "copy.md")
	require.NoError(t, os.WriteFile(copyPath, same, 0o600))
	_, err = reader.read(t.Context(), directory, "focused")
	require.ErrorContains(t, err, "multiple")
	require.NoError(t, os.Remove(copyPath))
	recovered, err := reader.read(t.Context(), directory, "focused")
	require.NoError(t, err)
	require.NotNil(t, recovered)
	assert.Equal(t, "Second objective, LONGER", recovered.Objective)
}

func BenchmarkReadPiGoalSession(b *testing.B) {
	directory := b.TempDir()
	path := filepath.Join(directory, "session.jsonl")
	var content strings.Builder
	content.WriteString(`{"type":"session","id":"session","version":3}` + "\n")
	for i := 0; i < 10000; i++ {
		fmt.Fprintf(&content, "{\"type\":\"message\",\"id\":\"%d\",\"parentId\":\"%d\",\"message\":{\"content\":\"%s\"}}\n", i, i-1, strings.Repeat("x", 1000))
	}
	require.NoError(b, os.WriteFile(path, []byte(content.String()), 0o600))
	for _, reuse := range []bool{false, true} {
		b.Run(fmt.Sprintf("reuse=%t", reuse), func(b *testing.B) {
			var reader piGoalSessionReader
			_, err := reader.read(context.Background(), path, directory, "session")
			require.NoError(b, err)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if !reuse {
					reader = piGoalSessionReader{}
				}
				_, err := reader.read(context.Background(), path, directory, "session")
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
