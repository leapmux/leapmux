package kimi

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiControlFlushRetainsTheNativeTextPrefixAcrossReset(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		prefix       string
		snapshotText string
		reset        bool
		delta        string
		offset       int
		want         []string
	}{
		{"unchanged prefix", "prefix", "prefix", true, "", 6, []string{"prefix"}},
		{"later append", "same", "same", true, " tail", 4, []string{"same", " tail"}},
		{"recovered unicode suffix", "A🙂", "A🙂 recovered", true, "!", 13, []string{"A🙂", " recovered!"}},
		{"corrected snapshot", "old", "corrected", true, "!", 9, []string{"old", "corrected!"}},
		{"append without reset", "same", "", false, " tail", 4, []string{"same", " tail"}},
		{"empty prefix", "", "", true, "after", 0, []string{"after"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.sink.UpdateSessionID("session_1")
			rig.transcriptOps(t, 1,
				`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`,
				fmt.Sprintf(`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"f1","role":"assistant","text":%q}}`, test.prefix))
			publishNativeKimiApproval(t, rig, "main", "approval_1")
			require.Len(t, rig.sink.PublishedControls(), 1)
			seq := int64(2)
			if test.reset {
				turn := fmt.Sprintf(`{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"},"steps":[{"kind":"step","stepId":"t0.1","state":"running","frames":[{"kind":"text","frameId":"f1","role":"assistant","text":%q}]}]}`, test.snapshotText)
				rig.transcriptReset(t, seq, turn)
				rig.transcriptReset(t, seq, turn)
				seq++
			}
			if test.delta != "" {
				rig.transcriptOps(t, seq, fmt.Sprintf(`{"op":"append","target":{"type":"frame","turnId":"t0","stepId":"t0.1","frameId":"f1"},"offset":%d,"text":%q}`, test.offset, test.delta))
				seq++
			}
			rig.transcriptOps(t, seq, `{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","state":"completed"}}`)
			var text []string
			for _, message := range rig.sink.Messages() {
				_, body := assembledRow(t, message)
				text = append(text, body)
			}
			assert.Equal(t, test.want, text)
		})
	}
}
