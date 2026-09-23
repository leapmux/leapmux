package agenttest

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/require"
)

// sessionInputSender is the part of a provider that
// AssertRejectsMissingAndReplacedSessions drives.
type sessionInputSender interface {
	SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error
}

// AssertRejectsMissingAndReplacedSessions pins that sender validates a
// session-qualified input. It refuses an empty session and a session that is not
// its current one, and the error states the session. The caller sets the
// current session to anything but "previous".
func AssertRejectsMissingAndReplacedSessions(t *testing.T, sender sessionInputSender) {
	t.Helper()
	for _, expected := range []string{"", "previous"} {
		require.ErrorContains(t, sender.SendInputForSession(expected, "Do not send this input.", nil), "session")
	}
}
