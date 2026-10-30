package agenttest

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubagentReportCaptureAndDirectWritesShareOneIdentity(t *testing.T) {
	t.Parallel()
	for _, capturedFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct first", true: "captured first"}[capturedFirst], func(t *testing.T) {
			t.Parallel()
			sink := &Sink{}
			sink.UpdateSessionID("native")
			write := agent.SubagentReportWrite{ReportID: "same", Report: agent.SubagentReport{Text: "First"}}
			persist := func(capture bool) {
				t.Helper()
				if capture {
					captured, present, err := agent.CaptureSubagentReport(sink, write)
					require.NoError(t, err)
					require.True(t, present)
					_, err = captured.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX)
					require.NoError(t, err)
				} else {
					_, err := sink.PersistSubagentReport(write)
					require.NoError(t, err)
				}
			}
			persist(capturedFirst)
			write.Report.Text = "Changed replay"
			persist(!capturedFirst)
			require.Len(t, sink.PersistedNotifications(), 1)
			reports := sink.LeapMuxNotifications()
			require.Len(t, reports, 1)
			assert.Equal(t, "First", reports[0]["text"])
		})
	}
}

func TestSubagentReportIdentityBelongsToItsNativeSession(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	for _, session := range []string{"first", "second"} {
		sink.AdoptNativeSession(session)
		stored, err := sink.PersistSubagentReport(agent.SubagentReportWrite{ReportID: "same", Report: agent.SubagentReport{Text: session}})
		require.NoError(t, err)
		assert.True(t, stored)
	}
	require.Len(t, sink.PersistedNotifications(), 2)
	reports := sink.LeapMuxNotifications()
	require.Len(t, reports, 2)
	assert.Equal(t, "first", reports[0]["text"])
	assert.Equal(t, "second", reports[1]["text"])
}
