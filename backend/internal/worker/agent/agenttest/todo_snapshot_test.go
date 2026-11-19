package agenttest

import (
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCapturedTodoSnapshotRejectsInvalidStatus(t *testing.T) {
	for _, status := range []todoevents.Status{-1, 999} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			sink := &Sink{}
			services := agent.NewProviderServices(sink)
			services.UpdateSessionID("native")
			captured := agent.CaptureTranscript(services, agent.MessageContent{}, agent.SpanInfo{})
			baseline := []todoevents.Item{{Content: "Existing", Status: todoevents.StatusPending}}
			prepared, err := services.PrepareTodoSnapshot(captured, baseline)
			require.NoError(t, err)
			applied, err := prepared.Persist()
			require.NoError(t, err)
			require.True(t, applied)
			prepared, err = services.PrepareTodoSnapshot(captured, []todoevents.Item{{Content: "Invalid", Status: status}})
			if err == nil {
				_, err = prepared.Persist()
			}
			require.Error(t, err, "the fake must reject statuses that the real database cannot store")
			assert.Equal(t, baseline, sink.TodoSnapshot())
		})
	}
}
