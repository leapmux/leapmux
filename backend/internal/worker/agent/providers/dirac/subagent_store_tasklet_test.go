package dirac

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiracTaskletMetadataKeepsNativeOwnershipAndOutcome(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeDiracTaskletStore(t, root)
	metadata, err := readDiracTaskletMetadata(root, "dirac-session")
	require.NoError(t, err)
	require.Len(t, metadata.Tasklets, 1)
	assert.Equal(t, "native-root", metadata.RootTaskID)
	child := metadata.Tasklets[0]
	assert.Equal(t, "native-spawn-card", child.SpawnCardID)
	assert.Equal(t, "Witten", child.Record.ID)
	assert.Equal(t, "01M7P10WRK68MMQJW753C9KJ2T", child.Record.ConversationID)
	assert.Equal(t, "Inspect the parser", *child.Record.Title)
	assert.Equal(t, "Inspect the parser and report.", *child.Record.Prompt)
	assert.EqualValues(t, 0, *child.Record.CreatedAt)
	assert.Equal(t, bgtask.StatusSucceeded, child.Record.Outcome.status())
	assert.Equal(t, "The parser is correct.", *child.Record.Outcome.Response)
}

func TestDiracTaskletDiscoveryIgnoresUnrelatedRuntimeSettings(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeDiracTaskletStore(t, root)
	runtime := filepath.Join(root, "data", "acp-session-runtime-configs", "dirac-session.json")
	require.NoError(t, os.WriteFile(runtime, []byte(`{"taskId":"native-root","settings":{"mode":42}}`), 0o600))

	metadata, err := readDiracTaskletMetadata(root, "dirac-session")
	require.NoError(t, err)
	assert.Equal(t, "native-root", metadata.RootTaskID)
	require.Len(t, metadata.Tasklets, 1)
	assert.Equal(t, "Witten", metadata.Tasklets[0].Record.ID)
}

func TestDiracTaskletAcceptedReceiptDoesNotFinishTheChild(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeDiracTaskletStore(t, root)
	mutateDiracTaskletRecord(t, root, func(record map[string]any) { delete(record, "outcome") })
	metadata, err := readDiracTaskletMetadata(root, "dirac-session")
	require.NoError(t, err)
	require.Len(t, metadata.Tasklets, 1)
	assert.Nil(t, metadata.Tasklets[0].Record.Outcome)
	assert.Equal(t, bgtask.StatusRunning, metadata.Tasklets[0].Record.Outcome.status())
}

func TestDiracTaskletMetadataRefusesSharedNativeConversations(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeDiracTaskletStore(t, root)
	appendDiracTasklet(t, root, "Bea", "01M7P10WRK68MMQJW753C9KJ2T")
	metadata, err := readDiracTaskletMetadata(root, "dirac-session")
	require.ErrorContains(t, err, "conversation")
	assert.Nil(t, metadata, "two tasklet identities cannot publish one shared child conversation")
}

func TestDiracSpawnBindingsRefuseDuplicateNativeReceipts(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"id":"spawn","ts":1,"conversationHistoryIndex":1,"content":{"type":"card","card":{"id":"spawn","toolName":"subagents","taskletReceipt":{"operation":"spawn","outcome":"accepted","rows":[{"operation":"spawn","taskletId":"Witten","title":"Inspect the parser","role":"default","message":"Inspect the parser and report.","outcome":"accepted"},{"operation":"spawn","taskletId":"Witten","title":"Inspect the parser","role":"default","message":"Inspect the parser and report.","outcome":"accepted"}]}}}}`)
	bindings, order, err := diracSpawnBindings([]json.RawMessage{raw})
	require.ErrorContains(t, err, "multiple Dirac spawn receipt rows identify one tasklet")
	assert.Nil(t, bindings)
	assert.Nil(t, order)
}

func TestDiracTaskletMetadataWaitsForTheDurableOutcomeEvent(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		events string
		mutate func(map[string]any)
	}{
		"accepted without outcome": {
			events: `[{"id":"accepted","taskletId":"Witten","createdAt":0,"communication":{"type":"lifecycle","state":"accepted"}}]`,
		},
		"different outcome": {
			events: `[{"id":"failed","taskletId":"Witten","createdAt":1,"communication":{"type":"outcome","outcome":{"kind":"failed","error":{"name":"Error","message":"Another outcome"},"failedAt":1}}}]`,
		},
		"post-resume torn write after resume": {
			events: `[{"id":"completed","taskletId":"Witten","createdAt":1,"communication":{"type":"outcome","outcome":{"kind":"completed","response":"A different prior result.","completedAt":1}}},{"id":"resumed","taskletId":"Witten","createdAt":2,"communication":{"type":"lifecycle","state":"resumed","assignment":"Continue."}}]`,
			mutate: func(record map[string]any) {
				record["outcome"] = map[string]any{"kind": "completed", "response": "The parser is correct.", "completedAt": 3}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeDiracTaskletStore(t, root)
			if tc.mutate != nil {
				mutateDiracTaskletRecord(t, root, tc.mutate)
			}
			require.NoError(t, os.WriteFile(filepath.Join(root, "data", "tasks", "native-root", "tasklets", "Witten", "tasklet_events.json"), []byte(tc.events), 0o600))
			metadata, err := readDiracTaskletMetadata(root, "dirac-session")
			require.NoError(t, err)
			require.Len(t, metadata.Tasklets, 1)
			record := metadata.Tasklets[0].Record
			assert.Nil(t, record.Outcome, "an immediate read defers settlement until the journal catches up or the grace window elapses")
			require.NotNil(t, record.unmatchedOutcome)
			assert.Equal(t, bgtask.StatusSucceeded, record.unmatchedOutcome.status())
		})
	}
}

func TestDiracTaskletMetadataIgnoresStalePriorOutcomeAfterResume(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		events string
		mutate func(map[string]any)
	}{
		"matches pre-resume outcome at same timestamp": {
			events: `[{"id":"accepted","taskletId":"Witten","createdAt":0,"communication":{"type":"lifecycle","state":"accepted"}},{"id":"outcome-1","taskletId":"Witten","createdAt":1,"communication":{"type":"outcome","outcome":{"kind":"completed","response":"The parser is correct.","completedAt":1}}},{"id":"resumed","taskletId":"Witten","createdAt":1,"communication":{"type":"lifecycle","state":"resumed","assignment":"Continue."}}]`,
		},
		"outcome completedAt older than resume": {
			events: `[{"id":"accepted","taskletId":"Witten","createdAt":0,"communication":{"type":"lifecycle","state":"accepted"}},{"id":"resumed","taskletId":"Witten","createdAt":2,"communication":{"type":"lifecycle","state":"resumed","assignment":"Continue."}}]`,
		},
		"record updatedAt older than resume": {
			events: `[{"id":"accepted","taskletId":"Witten","createdAt":0,"communication":{"type":"lifecycle","state":"accepted"}},{"id":"resumed","taskletId":"Witten","createdAt":2,"communication":{"type":"lifecycle","state":"resumed","assignment":"Continue."}}]`,
			mutate: func(record map[string]any) {
				record["updatedAt"] = 1
				record["outcome"] = map[string]any{"kind": "completed", "response": "Job 1 result.", "completedAt": 2}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeDiracTaskletStore(t, root)
			if tc.mutate != nil {
				mutateDiracTaskletRecord(t, root, tc.mutate)
			}
			require.NoError(t, os.WriteFile(filepath.Join(root, "data", "tasks", "native-root", "tasklets", "Witten", "tasklet_events.json"), []byte(tc.events), 0o600))
			metadata, err := readDiracTaskletMetadata(root, "dirac-session")
			require.NoError(t, err)
			require.Len(t, metadata.Tasklets, 1)
			record := metadata.Tasklets[0].Record
			assert.Nil(t, record.Outcome, "a stale pre-resume outcome must leave the resumed job running")
			assert.Nil(t, record.unmatchedOutcome, "a stale pre-resume outcome must not start torn-write settlement")
		})
	}
}

func TestDiracTaskletMetadataRefusesForeignOrMalformedRecords(t *testing.T) {
	t.Parallel()
	for field, value := range map[string]any{
		"id": "another-child", "rootTaskId": "another-root", "conversationUlid": "",
		"title": "Another assignment", "prompt": "Another prompt", "createdAt": -1,
		"outcome": nil,
	} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeDiracTaskletStore(t, root)
			mutateDiracTaskletRecord(t, root, func(record map[string]any) { record[field] = value })
			metadata, err := readDiracTaskletMetadata(root, "dirac-session")
			require.Error(t, err)
			assert.Nil(t, metadata, "a rejected native owner cannot publish partial children")
		})
	}
	for _, field := range []string{"title", "prompt", "createdAt"} {
		t.Run("missing="+field, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeDiracTaskletStore(t, root)
			mutateDiracTaskletRecord(t, root, func(record map[string]any) { delete(record, field) })
			_, err := readDiracTaskletMetadata(root, "dirac-session")
			require.Error(t, err)
		})
	}
}

func TestDiracTaskletOutcomesUseTheirExactNativeVariants(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		raw    string
		status bgtask.Status
	}{
		{`{"kind":"completed","response":"","completedAt":0}`, bgtask.StatusSucceeded},
		{`{"kind":"failed","error":{"name":"Error","message":"Failed"},"failedAt":0}`, bgtask.StatusFailed},
		{`{"kind":"cancelled","cancelledAt":0}`, bgtask.StatusStopped},
		{`{"kind":"interrupted","reason":"User request","interruptedAt":0}`, bgtask.StatusStopped},
	} {
		t.Run(test.raw, func(t *testing.T) {
			t.Parallel()
			var outcome diracTaskletOutcome
			require.NoError(t, json.Unmarshal([]byte(test.raw), &outcome))
			assert.True(t, outcome.valid())
			assert.Equal(t, test.status, outcome.status())
		})
	}
	for _, raw := range []string{
		`{}`, `{"kind":"completed","completedAt":0}`, `{"kind":"completed","response":"Done"}`,
		`{"kind":"failed","failedAt":0,"error":{"message":"Failed"}}`,
		`{"kind":"cancelled","cancelledAt":-1}`, `{"kind":"interrupted","interruptedAt":0}`,
		`{"kind":"unknown"}`,
	} {
		t.Run("invalid="+raw, func(t *testing.T) {
			t.Parallel()
			var outcome diracTaskletOutcome
			require.NoError(t, json.Unmarshal([]byte(raw), &outcome))
			assert.False(t, outcome.valid())
		})
	}
}

func TestDiracTaskletIDsRefuseUnsafePathComponents(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"Witten", "한국어", "Ada2", "a.b"} {
		assert.True(t, validDiracTaskletID(id), id)
	}
	for _, id := range []string{"", ".", "..", "../child", `child\other`, "/child", " x", "x ", "x.", "x:y", "x\x00y", "CON", "nul.txt", "COM1", "lpt9.log"} {
		assert.False(t, validDiracTaskletID(id), id)
	}
}

func mutateDiracTaskletRecord(t *testing.T, root string, mutate func(map[string]any)) {
	t.Helper()
	path := filepath.Join(root, "data", "tasks", "native-root", "tasklets", "Witten", "tasklet.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var record map[string]any
	require.NoError(t, json.Unmarshal(raw, &record))
	mutate(record)
	raw, err = json.Marshal(record)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o600))
}
