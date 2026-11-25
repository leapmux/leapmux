package agenttest

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/require"
)

// sessionInputSender is the part of a provider that
// AssertRejectsMissingAndReplacedSessions drives.
type sessionInputSender interface {
	SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) (agent.NativeInputReceipt, error)
}

// AssertRejectsMissingAndReplacedSessions pins that sender validates a
// session-qualified input. It refuses an empty session and a session that is not
// its current one, and the error states the session. The caller sets the
// current session to anything but "previous".
func AssertRejectsMissingAndReplacedSessions(t *testing.T, sender sessionInputSender) {
	t.Helper()
	for _, expected := range []string{"", "previous"} {
		_, err := sender.SendInputForSession(expected, "Do not send this input.", nil)
		require.ErrorContains(t, err, "session")
	}
}

// SendInput keeps error assertions concise in provider transport tests.
func SendInput(provider agent.Agent, content string, attachments []*leapmuxv1.Attachment) error {
	_, err := provider.SendInput(content, attachments)
	return err
}

// SendInputForSession keeps error assertions concise in session-pinned transport tests.
func SendInputForSession(provider agent.Agent, sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	_, err := provider.SendInputForSession(sessionID, content, attachments)
	return err
}

// SteerInput keeps error assertions concise in steering transport tests.
func SteerInput(provider agent.InputSteerer, request agent.SteerRequest) error {
	_, err := provider.SteerInput(request)
	return err
}

// SteerInputForSession keeps error assertions concise in session-pinned steering tests.
func SteerInputForSession(provider agent.SessionInputSteerer, sessionID string, request agent.SteerRequest) error {
	_, err := provider.SteerInputForSession(sessionID, request)
	return err
}
