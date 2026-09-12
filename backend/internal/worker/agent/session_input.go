package agent

import (
	"errors"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

var ErrInputSessionChanged = errors.New("the input belongs to a different provider session")

// checkInputSession runs in the same critical section that captures the native input target.
// A nil expected value selects the current session. A supplied empty value never authorizes delivery.
func checkInputSession(expected *string, current string) error {
	if expected != nil && (*expected == "" || *expected != current) {
		return ErrInputSessionChanged
	}
	return nil
}

func (a *ClaudeCodeAgent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments, "")
}

func (a *CodexAgent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments)
}

func (a *acpBase) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendPreparedPromptForSession(&sessionID, content, attachments, nil)
}

func (a *copilotAgent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments)
}

func (a *zcodeAgent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments, "")
}

func (a *PiAgent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments, false)
}
