package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Each caller of restartAgentPreservingSession states its OWN action, whole.
//
// The parameter used to be a bare sentence FRAGMENT interpolated into four frames.
// It read correctly in some and not in others: "failed to finish input queue a forced
// stop" for both callers, "failed to restart agent after an agent settings restart"
// circularly, and the one notification no frame reached told a reader who pressed Stop
// that the agent had failed to RESTART.
func TestRestartMessagesStateTheirOwnCallersAction(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		messages restartMessages
		// word is what every sentence of this caller must state, so a message
		// copied from the other caller fails here.
		word string
	}{
		{name: "settings", messages: settingsRestartMessages, word: "settings"},
		{name: "forced stop", messages: forcedStopMessages, word: "force"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for label, sentence := range map[string]string{
				"pauseFailedLog":      tc.messages.pauseFailedLog,
				"finishFailedLog":     tc.messages.finishFailedLog,
				"restartFailedLog":    tc.messages.restartFailedLog,
				"pauseFailedNotice":   tc.messages.pauseFailedNotice,
				"restartFailedNotice": tc.messages.restartFailedNotice,
			} {
				assert.NotEmptyf(t, sentence, "%s must be stated", label)
				assert.Containsf(t, strings.ToLower(sentence), tc.word,
					"%s must state this caller's own action, not the other caller's", label)
			}
			// A notice is concatenated with the error text, so it has to end where
			// that text begins.
			for _, notice := range []string{tc.messages.pauseFailedNotice, tc.messages.restartFailedNotice} {
				assert.True(t, strings.HasSuffix(notice, ": "),
					"a notice ends where the error text begins: %q", notice)
			}
		})
	}
	assert.NotEqual(t, settingsRestartMessages, forcedStopMessages)
}
