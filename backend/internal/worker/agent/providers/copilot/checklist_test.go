package copilot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"testing/synctest"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Go worker and the browser plugin both read Copilot's markdown checklist, so
// one corpus is the executable specification that each side replays.
func TestCopilotChecklistConformance(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(testutil.RepoPath(t, "testdata", "copilot_checklist_conformance.json"))
	require.NoError(t, err)
	var corpus struct {
		Cases []struct {
			Name      string `json:"name"`
			Checklist string `json:"checklist"`
			Expected  []struct {
				Content string `json:"content"`
				Status  string `json:"status"`
			} `json:"expected"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &corpus))
	require.NotEmpty(t, corpus.Cases)
	for _, test := range corpus.Cases {
		t.Run(test.Name, func(t *testing.T) {
			t.Parallel()
			items := parseCopilotChecklist(test.Checklist)
			require.Len(t, items, len(test.Expected))
			for index, want := range test.Expected {
				assert.Equal(t, want.Content, items[index].Content, "item %d content", index)
				assert.Equal(t, todoevents.StatusFromProviderWord(want.Status), items[index].Status, "item %d status", index)
				assert.Equal(t, want.Content, items[index].ActiveForm, "item %d active form", index)
			}
		})
	}
}

func TestExtractTodoEventReadsSqlTodos(t *testing.T) {
	t.Parallel()
	event, ok := (copilotProvider{}).ExtractTodoEvent("", []byte(`{"type":"leapmux_copilot_sql_todos","rows":[{"id":"a","title":"First","description":"detail","status":"done"},{"id":"b","title":"Second","status":"in_progress"},{"title":"Third","status":"blocked"},{"description":"Fourth","status":"unknown"}]}`), nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	require.Len(t, event.Snapshot, 4)
	assert.Equal(t, []todoevents.Status{todoevents.StatusCompleted, todoevents.StatusInProgress, todoevents.StatusBlocked, todoevents.StatusPending}, []todoevents.Status{event.Snapshot[0].Status, event.Snapshot[1].Status, event.Snapshot[2].Status, event.Snapshot[3].Status})
	assert.Equal(t, "a", event.Snapshot[0].ID)
	assert.Equal(t, "First", event.Snapshot[0].Content)
	assert.Equal(t, "detail", event.Snapshot[0].Description)
	assert.Equal(t, "Fourth", event.Snapshot[3].Content)
}

func TestExtractTodoEventReadsAnEmptySqlSnapshot(t *testing.T) {
	t.Parallel()
	p := copilotProvider{}
	event, ok := p.ExtractTodoEvent("", []byte(`{"type":"leapmux_copilot_sql_todos","rows":[]}`), nil)
	require.True(t, ok)
	assert.Empty(t, event.Snapshot)
	for _, rows := range []string{`null`, `{}`, `"invalid"`, `[{"title":7}]`, `[null]`} {
		_, ok := p.ExtractTodoEvent("", []byte(`{"type":"leapmux_copilot_sql_todos","rows":`+rows+`}`), nil)
		assert.False(t, ok, rows)
	}
}

type copilotRequestWriter func([]byte) (int, error)

func (w copilotRequestWriter) Write(raw []byte) (int, error) { return w(raw) }

func TestSqlTodosChangeStoresOneSnapshotRow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, sink := newNativeCopilotForEvents(t)
		done := make(chan struct{})
		response := `{"rows":[{"id":"a","title":"First","status":"pending"}]}`
		calls := 0
		writer := copilotRequestWriter(func(raw []byte) (int, error) {
			var request struct {
				ID     int64  `json:"id"`
				Method string `json:"method"`
			}
			if err := json.Unmarshal(raw, &request); err != nil {
				return 0, err
			}
			assert.Equal(t, "session.plan.readSqlTodos", request.Method)
			calls++
			a.Deliver(request.ID, fmt.Appendf(nil, `{"result":%s}`, response))
			return len(raw), nil
		})
		a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: t.Context(), Stdin: agenttest.NopStdin(writer), ProcessDone: done})
		t.Cleanup(func() { close(done); a.Process.Stop() })
		refresh := func() {
			a.HandleOutput(nativeCopilotSessionEvent(t, a.currentNativeSessionID(), "", "session.todos_changed", map[string]any{}))
			synctest.Wait()
		}
		for range 2 {
			refresh()
		}
		assert.Equal(t, 2, calls)
		rows := sink.Messages()
		require.Len(t, rows, 1)
		assert.JSONEq(t, `{"type":"leapmux_copilot_sql_todos","rows":[{"id":"a","title":"First","status":"pending"}]}`, string(rows[0].Content))
		response = `{"rows":[]}`
		a.HandleOutput(nativeCopilotEvent(t, "", "session.todos_changed", map[string]any{}))
		synctest.Wait()
		require.Len(t, sink.Messages(), 2)
		assert.JSONEq(t, `{"type":"leapmux_copilot_sql_todos","rows":[]}`, string(sink.Messages()[1].Content))
		assert.Equal(t, "session-1", sink.Messages()[1].AgentSessionID)
		response = `{"rows":[null]}`
		refresh()
		assert.Len(t, sink.Messages(), 2, "a malformed result must not replace the snapshot")
		response = `{"rows":[{"title":"Retry this write","status":"pending"}]}`
		sink.PersistErr = errors.New("the store is unavailable")
		refresh()
		assert.JSONEq(t, `[]`, string(a.sqlTodos), "a refused write must not enter the duplicate cache")
		sink.PersistErr = nil
		refresh()
		refresh()
		require.Len(t, sink.Messages(), 4, "the sink records one failed attempt and one successful attempt")
		a.sessionMu.Lock()
		a.forgetNativeSessionState("replacement")
		a.sessionMu.Unlock()
		refresh()
		require.Len(t, sink.Messages(), 5, "a replacement session must store its own snapshot")
		assert.Equal(t, "replacement", sink.Messages()[4].AgentSessionID)
	})
}
