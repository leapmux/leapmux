package mail

import (
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/internal/util/verifycode"
)

// RenderVerificationEmail builds the verification email containing both a
// short hand-typeable code and a one-click link. The same code backs both
// — the link just deep-links to the frontend page that submits it.
func RenderVerificationEmail(to, storedCode, link string) Message {
	display := verifycode.Format(storedCode)
	var body strings.Builder
	body.WriteString("Your verification code:\n\n    ")
	body.WriteString(display)
	body.WriteString("\n\n")
	body.WriteString("Or click this link:\n    ")
	body.WriteString(link)
	body.WriteString("\n\nThis code expires in 30 minutes.\n")
	return Message{
		To:      to,
		Subject: "Verify your email",
		Body:    body.String(),
	}
}

// RenderRegistrationInstructions builds the email a user sends to
// themselves when they want to set up a worker on another machine.
// The body is the exact command line; the recipient pastes it into a
// terminal on the worker host.
func RenderRegistrationInstructions(to, command string) Message {
	body := fmt.Sprintf(
		"Run the command below on the machine where the worker should run:\n\n    %s\n\n"+
			"This registration key is only valid while the registration dialog stays open in your browser.\n",
		command,
	)
	return Message{
		To:      to,
		Subject: "Worker registration command",
		Body:    body,
	}
}
