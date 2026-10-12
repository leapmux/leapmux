package fastagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

func writeFastagentChildArchive(t *testing.T, home, parentID, childID, toolCallID, historyFile string) {
	t.Helper()
	dir := filepath.Join(home, "sessions", parentID, "children", childID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	snapshot := map[string]any{
		"session_id": childID,
		"metadata": map[string]any{"extras": map[string]string{
			"subagent_label": "Count files", "subagent_task_preview": "Count files",
		}},
		"execution": map[string]any{
			"resumable":  false,
			"child_link": map[string]string{"parent_session_id": parentID, "parent_tool_call_id": toolCallID},
		},
		"continuation": map[string]any{
			"active_agent": "agent",
			"agents":       map[string]any{"agent": map[string]string{"history_file": historyFile}},
		},
	}
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "session.json"), raw, 0o644))
	history := `{"messages":[{"role":"user","content":[{"type":"text","text":"Count files"}]},{"role":"assistant","content":[{"type":"text","text":"FAST_CHILD_ARCHIVED_TEXT"}]}]}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "history_agent.json"), []byte(history), 0o644))
}

func setFastagentChildOrdinal(t *testing.T, home, parentID, childID string, ordinal int) {
	t.Helper()
	path := filepath.Join(home, "sessions", parentID, "children", childID, "session.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var snapshot map[string]any
	require.NoError(t, json.Unmarshal(raw, &snapshot))
	metadata := snapshot["metadata"].(map[string]any)
	extras := metadata["extras"].(map[string]any)
	extras["subagent_ordinal"] = ordinal
	raw, err = json.Marshal(snapshot)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o644))
}

func setFastagentChildLabel(t *testing.T, home, parentID, childID, label string) {
	t.Helper()
	path := filepath.Join(home, "sessions", parentID, "children", childID, "session.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var snapshot map[string]any
	require.NoError(t, json.Unmarshal(raw, &snapshot))
	metadata := snapshot["metadata"].(map[string]any)
	extras := metadata["extras"].(map[string]any)
	extras["subagent_label"] = label
	raw, err = json.Marshal(snapshot)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o644))
}

var fastagentArchiveChild = &fastagentChildState{prompt: "Count files", requestedLabel: "Count files"}

type fastagentSwapReadRoot struct {
	*os.Root
	beforeOpen func()
}

type fastagentCloseFaultRoot struct {
	sessionstore.ArchiveRoot
	closeErr error
}

func (r fastagentCloseFaultRoot) Close() error {
	return errors.Join(r.ArchiveRoot.Close(), r.closeErr)
}

func TestReadFastagentChildArchiveReportsReadAndCloseFaults(t *testing.T) {
	root, err := sessionstore.OpenArchiveRoot(t.TempDir())
	require.NoError(t, err)
	closeErr := errors.New("injected fastagent root close failure")
	archive, err := readFastagentChildArchiveFromRoot(
		fastagentCloseFaultRoot{ArchiveRoot: root, closeErr: closeErr}, "parent", fastagentArchiveChild)
	assert.Nil(t, archive)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.ErrorIs(t, err, closeErr)
}

func TestReadFastagentChildArchiveClearsDataAfterCloseFault(t *testing.T) {
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-1", "model-call-1", "history_agent.json")
	root, err := sessionstore.OpenArchiveRoot(home)
	require.NoError(t, err)
	closeErr := errors.New("injected fastagent root close failure")
	archive, err := readFastagentChildArchiveFromRoot(
		fastagentCloseFaultRoot{ArchiveRoot: root, closeErr: closeErr}, "parent", fastagentArchiveChild)
	assert.Nil(t, archive)
	require.ErrorIs(t, err, closeErr)
}

func (root *fastagentSwapReadRoot) Open(path string) (*os.File, error) {
	if root.beforeOpen != nil {
		beforeOpen := root.beforeOpen
		root.beforeOpen = nil
		beforeOpen()
	}
	return root.Root.Open(path)
}

type fastagentParentResultFixture struct {
	modelCallID        string
	childID            string
	text               string
	requestedLabel     string
	requestedLabelNull bool
	resolvedLabel      string
}

func writeFastagentParentResultHistory(t *testing.T, home, parentID string, results ...fastagentParentResultFixture) {
	t.Helper()
	parentDir := filepath.Join(home, "sessions", parentID)
	require.NoError(t, os.MkdirAll(parentDir, 0o755))
	snapshot := map[string]any{
		"session_id": parentID,
		"continuation": map[string]any{
			"active_agent": "agent",
			"agents":       map[string]any{"agent": map[string]string{"history_file": "history_agent.json"}},
		},
	}
	toolResults := make(map[string]any, len(results))
	for _, result := range results {
		var requestedLabel any = result.requestedLabel
		if result.requestedLabelNull {
			requestedLabel = nil
		} else if requestedLabel == "" {
			requestedLabel = "Count files"
		}
		resolvedLabel := result.resolvedLabel
		if resolvedLabel == "" {
			resolvedLabel = "Count files"
		}
		toolResults[result.modelCallID] = map[string]any{
			"content": []any{map[string]string{"type": "text", "text": result.text}},
			"_meta": map[string]any{"fast-agent-subagent": map[string]any{
				"child_session_id": result.childID, "parent_tool_call_id": result.modelCallID,
				"requested_label": requestedLabel, "label": resolvedLabel,
			}},
		}
	}
	history := map[string]any{"messages": []any{map[string]any{"role": "user", "tool_results": toolResults}}}
	for filename, content := range map[string]any{"session.json": snapshot, "history_agent.json": history} {
		raw, err := json.Marshal(content)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(parentDir, filename), raw, 0o644))
	}
}

func rewriteFastagentParentLink(t *testing.T, home, parentID, childID, linkedParentID string) {
	t.Helper()
	path := filepath.Join(home, "sessions", parentID, "children", childID, "session.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var snapshot map[string]any
	require.NoError(t, json.Unmarshal(raw, &snapshot))
	execution := snapshot["execution"].(map[string]any)
	link := execution["child_link"].(map[string]any)
	link["parent_session_id"] = linkedParentID
	raw, err = json.Marshal(snapshot)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o644))
}

func TestReadFastagentChildArchiveMatchesItsParentAndPrompt(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-1", "model-call-1", "history_agent.json")
	writeFastagentChildArchive(t, home, "parent", "child-2", "model-call-2", "history_agent.json")
	rewriteFastagentParentLink(t, home, "parent", "child-2", "another-parent")

	// The ACP call id is separate from the native model call id. Only the
	// native child backlink to this parent can select the archive.
	archive, err := readFastagentChildArchive(home, "parent", fastagentArchiveChild)
	require.NoError(t, err)
	require.Len(t, archive.messages, 2)
	assert.Equal(t, "user", archive.messages[0].Role)
	assert.Equal(t, "assistant", archive.messages[1].Role)
	assert.Contains(t, string(archive.messages[1].Content[0]), "FAST_CHILD_ARCHIVED_TEXT")
}

func TestReadFastagentChildArchiveRejectsCollidingBacklinks(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-1", "call-1", "history_agent.json")
	writeFastagentChildArchive(t, home, "parent", "child-2", "call-2", "history_agent.json")

	_, err := readFastagentChildArchive(home, "parent", fastagentArchiveChild)
	require.ErrorContains(t, err, "multiple fastagent children match")
}

func TestReadFastagentChildArchiveSkipsUnreadableUnrelatedChildForNativeResult(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-first", "model-first", "history_agent.json")
	writeFastagentChildArchive(t, home, "parent", "child-second", "model-second", "history_agent.json")
	require.NoError(t, os.Remove(filepath.Join(home, "sessions", "parent", "children", "child-first", "history_agent.json")))
	writeFastagentParentResultHistory(t, home, "parent",
		fastagentParentResultFixture{modelCallID: "model-first", childID: "child-first", text: "FIRST_CHILD_RESULT"},
		fastagentParentResultFixture{modelCallID: "model-second", childID: "child-second", text: "SECOND_CHILD_RESULT"},
	)
	child := &fastagentChildState{prompt: "Count files", requestedLabel: "Count files", resultText: "SECOND_CHILD_RESULT", resultSeen: true}
	archive, err := readFastagentChildArchive(home, "parent", child)
	require.NoError(t, err)
	require.Len(t, archive.messages, 2)
	assert.Contains(t, string(archive.messages[1].Content[0]), "FAST_CHILD_ARCHIVED_TEXT")
}

func TestReadFastagentChildArchiveWaitsForItsNativeResultWhenEarlierChildMatches(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-first", "model-first", "history_agent.json")
	writeFastagentParentResultHistory(t, home, "parent",
		fastagentParentResultFixture{modelCallID: "model-first", childID: "child-first", text: "FIRST_CHILD_RESULT"},
		fastagentParentResultFixture{modelCallID: "model-second", childID: "child-second", text: "SECOND_CHILD_RESULT"},
	)
	child := &fastagentChildState{prompt: "Count files", requestedLabel: "Count files", resultText: "SECOND_CHILD_RESULT", resultSeen: true}
	_, err := readFastagentChildArchive(home, "parent", child)
	require.Error(t, err, "the only visible archive belongs to an earlier native call")

	writeFastagentChildArchive(t, home, "parent", "child-second", "model-second", "history_agent.json")
	secondHistory := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": "Count files"}}},
		map[string]any{"role": "assistant", "content": []any{map[string]string{"type": "text", "text": "SECOND_ARCHIVE_TEXT"}}},
	}}
	raw, err := json.Marshal(secondHistory)
	require.NoError(t, err)
	path := filepath.Join(home, "sessions", "parent", "children", "child-second", "history_agent.json")
	require.NoError(t, os.WriteFile(path, raw, 0o644))
	archive, err := readFastagentChildArchive(home, "parent", child)
	require.NoError(t, err)
	require.Len(t, archive.messages, 2)
	assert.Contains(t, string(archive.messages[1].Content[0]), "SECOND_ARCHIVE_TEXT")
	assert.NotContains(t, string(archive.messages[1].Content[0]), "FAST_CHILD_ARCHIVED_TEXT")
}

func TestReadFastagentChildArchiveRejectsDuplicateNativeResults(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-first", "model-first", "history_agent.json")
	writeFastagentChildArchive(t, home, "parent", "child-second", "model-second", "history_agent.json")
	writeFastagentParentResultHistory(t, home, "parent",
		fastagentParentResultFixture{modelCallID: "model-first", childID: "child-first", text: "SAME_RESULT"},
		fastagentParentResultFixture{modelCallID: "model-second", childID: "child-second", text: "SAME_RESULT"},
	)
	child := &fastagentChildState{prompt: "Count files", requestedLabel: "Count files", resultText: "SAME_RESULT", resultSeen: true}
	_, err := readFastagentChildArchive(home, "parent", child)
	require.ErrorContains(t, err, "multiple fastagent parent results match")
}

func TestReadFastagentChildArchiveRejectsParentResultBacklinkMismatch(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-first", "model-first", "history_agent.json")
	writeFastagentParentResultHistory(t, home, "parent", fastagentParentResultFixture{
		modelCallID: "model-second", childID: "child-first", text: "FIRST_CHILD_RESULT",
	})
	child := &fastagentChildState{prompt: "Count files", requestedLabel: "Count files", resultText: "FIRST_CHILD_RESULT", resultSeen: true}
	_, err := readFastagentChildArchive(home, "parent", child)
	require.ErrorContains(t, err, "does not match a child archive backlink")
}

func TestReadFastagentChildArchiveReadsNativeResultMetadataAlias(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-first", "model-first", "history_agent.json")
	writeFastagentChildArchive(t, home, "parent", "child-second", "model-second", "history_agent.json")
	writeFastagentParentResultHistory(t, home, "parent",
		fastagentParentResultFixture{modelCallID: "model-first", childID: "child-first", text: "FIRST_CHILD_RESULT"},
		fastagentParentResultFixture{modelCallID: "model-second", childID: "child-second", text: "SECOND_CHILD_RESULT"},
	)
	historyPath := filepath.Join(home, "sessions", "parent", "history_agent.json")
	history, err := os.ReadFile(historyPath)
	require.NoError(t, err)
	require.Contains(t, string(history), `"_meta":`)
	child := &fastagentChildState{prompt: "Count files", requestedLabel: "Count files", resultText: "SECOND_CHILD_RESULT", resultSeen: true}
	archive, err := readFastagentChildArchive(home, "parent", child)
	require.NoError(t, err, "native history serializes CallToolResult.meta as _meta")
	require.Len(t, archive.messages, 2)
}

func TestReadFastagentChildArchiveSelectsResolvedDuplicateLabel(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-first", "model-first", "history_agent.json")
	writeFastagentChildArchive(t, home, "parent", "child-second", "model-second", "history_agent.json")
	setFastagentChildLabel(t, home, "parent", "child-second", "Count files-2")
	setFastagentChildOrdinal(t, home, "parent", "child-first", 1)
	setFastagentChildOrdinal(t, home, "parent", "child-second", 2)
	writeFastagentParentResultHistory(t, home, "parent",
		fastagentParentResultFixture{modelCallID: "model-first", childID: "child-first", text: "FIRST_RESULT"},
		fastagentParentResultFixture{
			modelCallID: "model-second", childID: "child-second", text: "SECOND_RESULT",
			requestedLabel: "Count files", resolvedLabel: "Count files-2",
		},
	)
	child := &fastagentChildState{prompt: "Count files", requestedLabel: "Count files", resultText: "SECOND_RESULT", resultSeen: true}
	archive, err := readFastagentChildArchive(home, "parent", child)
	require.NoError(t, err)
	require.Equal(t, "child-second", archive.childID)
}

func TestReadFastagentChildArchiveRejectsParentRequestedLabelMismatch(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-one", "model-one", "history_agent.json")
	writeFastagentParentResultHistory(t, home, "parent", fastagentParentResultFixture{
		modelCallID: "model-one", childID: "child-one", text: "ONE_RESULT",
		requestedLabel: "Another label", resolvedLabel: "Count files",
	})
	child := &fastagentChildState{prompt: "Count files", requestedLabel: "Count files", resultText: "ONE_RESULT", resultSeen: true}
	_, err := readFastagentChildArchive(home, "parent", child)
	require.ErrorContains(t, err, "requested label")
}

func TestReadFastagentChildArchiveRejectsParentResolvedLabelMismatch(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-one", "model-one", "history_agent.json")
	writeFastagentParentResultHistory(t, home, "parent", fastagentParentResultFixture{
		modelCallID: "model-one", childID: "child-one", text: "ONE_RESULT",
		requestedLabel: "Count files", resolvedLabel: "Different resolved label",
	})
	child := &fastagentChildState{prompt: "Count files", requestedLabel: "Count files", resultText: "ONE_RESULT", resultSeen: true}
	_, err := readFastagentChildArchive(home, "parent", child)
	require.ErrorContains(t, err, "resolved label")
}

func TestReadFastagentChildArchiveAcceptsGeneratedLabelWithoutRequest(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-one", "model-one", "history_agent.json")
	setFastagentChildLabel(t, home, "parent", "child-one", "Generated label")
	writeFastagentParentResultHistory(t, home, "parent", fastagentParentResultFixture{
		modelCallID: "model-one", childID: "child-one", text: "ONE_RESULT",
		requestedLabelNull: true, resolvedLabel: "Generated label",
	})
	child := &fastagentChildState{prompt: "Count files", resultText: "ONE_RESULT", resultSeen: true}
	archive, err := readFastagentChildArchive(home, "parent", child)
	require.NoError(t, err)
	require.Equal(t, "child-one", archive.childID)
}

func TestReadFastagentChildArchiveRejectsGeneratedLabelMismatch(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-one", "model-one", "history_agent.json")
	setFastagentChildLabel(t, home, "parent", "child-one", "Generated label")
	writeFastagentParentResultHistory(t, home, "parent", fastagentParentResultFixture{
		modelCallID: "model-one", childID: "child-one", text: "ONE_RESULT",
		requestedLabelNull: true, resolvedLabel: "Different generated label",
	})
	child := &fastagentChildState{prompt: "Count files", resultText: "ONE_RESULT", resultSeen: true}
	_, err := readFastagentChildArchive(home, "parent", child)
	require.ErrorContains(t, err, "resolved label")
}

func TestReadFastagentChildArchiveRejectsAnOldIdenticalResultCheckpoint(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-old", "model-old", "history_agent.json")
	writeFastagentChildArchive(t, home, "parent", "child-new", "model-new", "history_agent.json")
	writeFastagentParentResultHistory(t, home, "parent", fastagentParentResultFixture{
		modelCallID: "model-old", childID: "child-old", text: "SAME_RESULT",
	})
	child := &fastagentChildState{prompt: "Count files", requestedLabel: "Count files", resultText: "SAME_RESULT", resultSeen: true}
	_, err := readFastagentChildArchive(home, "parent", child)
	require.Error(t, err, "the new result has no parent history entry yet")
	writeFastagentParentResultHistory(t, home, "parent",
		fastagentParentResultFixture{modelCallID: "model-old", childID: "child-old", text: "SAME_RESULT"},
		fastagentParentResultFixture{modelCallID: "model-new", childID: "child-new", text: "SAME_RESULT"},
	)
	_, err = readFastagentChildArchive(home, "parent", child)
	require.ErrorContains(t, err, "multiple fastagent parent results match")
}

func TestReadFastagentChildArchiveReadsNewResultAfterParentCompaction(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-old", "model-old", "history_agent.json")
	writeFastagentChildArchive(t, home, "parent", "child-new", "model-new", "history_agent.json")
	setFastagentChildOrdinal(t, home, "parent", "child-old", 1)
	setFastagentChildOrdinal(t, home, "parent", "child-new", 2)
	// Parent compaction removes the old tool result, but Fast Agent keeps its
	// completed child session under children/.
	writeFastagentParentResultHistory(t, home, "parent", fastagentParentResultFixture{
		modelCallID: "model-new", childID: "child-new", text: "NEW_RESULT",
	})
	child := &fastagentChildState{prompt: "Count files", requestedLabel: "Count files", resultText: "NEW_RESULT", resultSeen: true}
	archive, err := readFastagentChildArchive(home, "parent", child)
	require.NoError(t, err)
	require.Equal(t, "child-new", archive.childID)
	require.Len(t, archive.messages, 2)
}

func TestReadFastagentChildArchiveRequiresOrderedNativeOrdinalsForMissingResults(t *testing.T) {
	for _, tc := range []struct {
		name            string
		oldOrdinal      int
		setOld          bool
		selectedOrdinal int
		setSelected     bool
	}{
		{name: "missing old ordinal", selectedOrdinal: 2, setSelected: true},
		{name: "zero old ordinal", oldOrdinal: 0, setOld: true, selectedOrdinal: 2, setSelected: true},
		{name: "duplicate ordinal", oldOrdinal: 2, setOld: true, selectedOrdinal: 2, setSelected: true},
		{name: "later old ordinal", oldOrdinal: 3, setOld: true, selectedOrdinal: 2, setSelected: true},
		{name: "missing selected ordinal", oldOrdinal: 1, setOld: true},
		{name: "zero selected ordinal", oldOrdinal: 1, setOld: true, selectedOrdinal: 0, setSelected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			writeFastagentChildArchive(t, home, "parent", "child-old", "model-old", "history_agent.json")
			writeFastagentChildArchive(t, home, "parent", "child-new", "model-new", "history_agent.json")
			if tc.setOld {
				setFastagentChildOrdinal(t, home, "parent", "child-old", tc.oldOrdinal)
			}
			if tc.setSelected {
				setFastagentChildOrdinal(t, home, "parent", "child-new", tc.selectedOrdinal)
			}
			writeFastagentParentResultHistory(t, home, "parent", fastagentParentResultFixture{
				modelCallID: "model-new", childID: "child-new", text: "NEW_RESULT",
			})
			child := &fastagentChildState{prompt: "Count files", requestedLabel: "Count files", resultText: "NEW_RESULT", resultSeen: true}
			_, err := readFastagentChildArchive(home, "parent", child)
			require.ErrorContains(t, err, "lacks a result for a matching child archive")
		})
	}
}

func TestReadFastagentChildArchiveRejectsASwappedSessionDirectory(t *testing.T) {
	for _, withResult := range []bool{false, true} {
		name := "child archive"
		if withResult {
			name = "parent result"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			outsideHome := t.TempDir()
			writeFastagentChildArchive(t, outsideHome, "parent", "child-outside", "model-outside", "history_agent.json")
			if withResult {
				writeFastagentParentResultHistory(t, outsideHome, "parent", fastagentParentResultFixture{
					modelCallID: "model-outside", childID: "child-outside", text: "OUTSIDE_RESULT",
				})
			}
			home := t.TempDir()
			parentDir := filepath.Join(home, "sessions", "parent")
			require.NoError(t, os.MkdirAll(parentDir, 0o755))
			require.NoError(t, os.Rename(parentDir, parentDir+"-prior"))
			if err := os.Symlink(filepath.Join(outsideHome, "sessions", "parent"), parentDir); err != nil {
				t.Skipf("symlinks are unavailable: %v", err)
			}
			child := &fastagentChildState{prompt: "Count files", requestedLabel: "Count files"}
			if withResult {
				child.resultText = "OUTSIDE_RESULT"
				child.resultSeen = true
			}
			_, err := readFastagentChildArchive(home, "parent", child)
			require.Error(t, err, "the reader must stay inside its configured Fast Agent home")
		})
	}
}

func TestReadFastagentChildArchiveRejectsInRootAncestor(t *testing.T) {
	for _, tc := range []struct {
		name     string
		linkName string
	}{
		{name: "parent session", linkName: "parent"},
		{name: "children listing", linkName: "children"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			writeFastagentChildArchive(t, home, "parent", "child-1", "model-call-1", "history_agent.json")
			parent := filepath.Join(home, "sessions", "parent")
			link := parent
			if tc.linkName == "children" {
				link = filepath.Join(parent, "children")
			}
			alternate := link + "-alternate"
			require.NoError(t, os.Rename(link, alternate))
			history := filepath.Join(alternate, "children", "child-1", "history_agent.json")
			if tc.linkName == "children" {
				history = filepath.Join(alternate, "child-1", "history_agent.json")
			}
			raw, err := os.ReadFile(history)
			require.NoError(t, err)
			substitute := bytes.Replace(raw, []byte("FAST_CHILD_ARCHIVED_TEXT"), []byte("SUBSTITUTE_CHILD_TEXT"), 1)
			require.NotEqual(t, raw, substitute)
			require.NoError(t, os.WriteFile(history, substitute, 0o644))
			if linkErr := os.Symlink(filepath.Base(alternate), link); linkErr != nil {
				t.Skipf("symlinks are unavailable: %v", linkErr)
			}

			archive, err := readFastagentChildArchive(home, "parent", fastagentArchiveChild)
			if archive != nil {
				require.Len(t, archive.messages, 2)
				assert.NotContains(t, string(archive.messages[1].Content[0]), "SUBSTITUTE_CHILD_TEXT")
			}
			require.Error(t, err, "an in-root ancestor link cannot supply a child archive")
			assert.Nil(t, archive)
		})
	}
}

func TestReadFastagentRegularFileRejectsSwapBetweenStatAndOpen(t *testing.T) {
	for _, tc := range []struct {
		name    string
		symlink bool
	}{
		{name: "external symlink", symlink: true},
		{name: "different regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			path := filepath.Join(home, "session.json")
			require.NoError(t, os.WriteFile(path, []byte(`{"safe":true}`), 0o644))
			root, err := os.OpenRoot(home)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })
			swapped := false
			reader := &fastagentSwapReadRoot{Root: root, beforeOpen: func() {
				swapped = true
				require.NoError(t, os.Rename(path, filepath.Join(home, "session-old.json")))
				if tc.symlink {
					outside := filepath.Join(t.TempDir(), "outside.json")
					require.NoError(t, os.WriteFile(outside, []byte(`{"outside":true}`), 0o644))
					if err := os.Symlink(outside, path); err != nil {
						t.Skipf("symlinks are unavailable: %v", err)
					}
					return
				}
				require.NoError(t, os.WriteFile(path, []byte(`{"replacement":true}`), 0o644))
			}}
			_, err = readFastagentRegularFile(reader, "session.json", fastagentChildSnapshotLimit)
			assert.True(t, swapped, "the test swaps the file after Lstat")
			if tc.symlink {
				require.Error(t, err, "the root must refuse an external symlink")
			} else {
				require.ErrorContains(t, err, "changed before it opened")
			}
		})
	}
}

func TestReadFastagentChildArchiveRejectsWrongBacklink(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-1", "model-call-1", "history_agent.json")
	rewriteFastagentParentLink(t, home, "parent", "child-1", "another-parent")

	_, err := readFastagentChildArchive(home, "parent", fastagentArchiveChild)
	require.ErrorContains(t, err, "no fastagent child matches")
}

func TestReadFastagentChildArchiveRejectsHistoryTraversal(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-1", "call-1", "../outside.json")

	_, err := readFastagentChildArchive(home, "parent", fastagentArchiveChild)
	require.ErrorContains(t, err, "history path is invalid")
}

func TestReadFastagentChildArchiveRejectsWrongPrompt(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-1", "model-call-1", "history_agent.json")
	child := &fastagentChildState{prompt: "Count another directory", requestedLabel: "Count files"}

	_, err := readFastagentChildArchive(home, "parent", child)
	require.ErrorContains(t, err, "no fastagent child matches")
}

func TestReadFastagentChildArchiveRejectsSymlinkedChildDirectory(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-1", "model-call-1", "history_agent.json")
	childrenDir := filepath.Join(home, "sessions", "parent", "children")
	childDir := filepath.Join(childrenDir, "child-1")
	outside := filepath.Join(home, "outside-child")
	require.NoError(t, os.Rename(childDir, outside))
	if err := os.Symlink(outside, childDir); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	_, err := readFastagentChildArchive(home, "parent", fastagentArchiveChild)
	require.ErrorContains(t, err, "no fastagent child matches")
}

func TestReadFastagentChildArchiveRejectsSymlinkedHistory(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-1", "model-call-1", "history_agent.json")
	childDir := filepath.Join(home, "sessions", "parent", "children", "child-1")
	historyPath := filepath.Join(childDir, "history_agent.json")
	outside := filepath.Join(home, "outside-history.json")
	require.NoError(t, os.Rename(historyPath, outside))
	if err := os.Symlink(outside, historyPath); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	_, err := readFastagentChildArchive(home, "parent", fastagentArchiveChild)
	require.ErrorContains(t, err, "not a regular file within")
}

func TestReadFastagentChildArchiveRejectsOversizedSnapshot(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-1", "model-call-1", "history_agent.json")
	path := filepath.Join(home, "sessions", "parent", "children", "child-1", "session.json")
	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte("x"), fastagentChildSnapshotLimit+1), 0o644))

	_, err := readFastagentChildArchive(home, "parent", fastagentArchiveChild)
	require.ErrorContains(t, err, "no fastagent child matches")
}

func TestReadFastagentChildArchiveRejectsOversizedHistory(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentChildArchive(t, home, "parent", "child-1", "model-call-1", "history_agent.json")
	path := filepath.Join(home, "sessions", "parent", "children", "child-1", "history_agent.json")
	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte("x"), fastagentChildHistoryLimit+1), 0o644))

	_, err := readFastagentChildArchive(home, "parent", fastagentArchiveChild)
	require.ErrorContains(t, err, "not a regular file within")
}
