package mail

import (
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/internal/util/verifycode"
)

// footerSeparator is the literal RFC 3676 §4.3 signature delimiter:
// dash, dash, space, newline. The trailing space is intentional and
// editors that trim trailing whitespace will silently break it — the
// strict-byte tests in templates_test.go pin this exact sequence.
const footerSeparator = "-- \n"

// verifyEmailPath is the frontend route that consumes the verification
// code from the deep-link in the verification email. Centralizing the
// constant here keeps the renderer the single owner of "how a
// verification link is built" — callers only know the code, not the URL
// shape.
const verifyEmailPath = "/verify-email?code="

// Renderer builds the email Messages this package sends. It carries
// the hub's public base URL once so render call sites only pass
// per-message data (recipient, code, command). The zero value
// Renderer{} is valid; tests that don't inspect URLs in the rendered
// output use it directly.
type Renderer struct {
	// HubURL is the absolute base URL the hub exposes (cfg.BaseURL()).
	// Used in two places: the absolute /verify-email link in the
	// verification email body, and the auto-message footer in every
	// email's body.
	HubURL string
}

// footer renders the standard auto-message footer naming LeapMux and
// the hub's public URL. Every email this package sends uses this
// footer so recipients can identify the sender and know the mailbox is
// unattended.
func (r Renderer) footer() string {
	return footerSeparator +
		"This is an automated message from your LeapMux hub at " + r.HubURL + ".\n" +
		"Please do not reply.\n"
}

// VerificationEmail builds the email that delivers a verification code
// to confirm a new or changed email address.
//
// Sent when:
//   - Password sign-up with email when EmailVerificationRequired=true.
//   - OAuth sign-up when the provider's email is untrusted.
//   - User requests an email change.
//   - User requests a resend of a previously-issued code.
//
// Inputs: `to` is the recipient's email address; `storedCode` is the
// raw 6-symbol verifycode (this method calls verifycode.Format to
// render the user-facing XXX-XXX form, and reuses the formatted code
// in the deep-link).
//
// Rendered body:
//
//	Use this code to verify your email address:
//
//	    {code}
//
//	Or click the link below:
//
//	    {link}
//
//	The code expires in 30 minutes.
//
//	-- ␠
//	This is an automated message from your LeapMux hub at {hubURL}.
//	Please do not reply.
//
// (The "␠" marker stands in for a literal trailing space on the "-- "
// signature delimiter; see RFC 3676 §4.3.)
func (r Renderer) VerificationEmail(to, storedCode string) Message {
	display := verifycode.Format(storedCode)
	link := r.HubURL + verifyEmailPath + display
	var body strings.Builder
	body.WriteString("Use this code to verify your email address:\n\n    ")
	body.WriteString(display)
	body.WriteString("\n\nOr click the link below:\n\n    ")
	body.WriteString(link)
	body.WriteString("\n\nThe code expires in 30 minutes.\n\n")
	body.WriteString(r.footer())
	return Message{
		To:      to,
		Subject: "[LeapMux] Verify your email address",
		Body:    body.String(),
	}
}

// RegistrationInstructions builds the email a user sends to themselves
// when they want to set up a worker on another machine.
//
// Sent when: the user clicks "Send email" in the worker registration
// dialog (frontend → WorkerManagementService.EmailRegistrationInstructions).
//
// Inputs: `to` is the user's verified email address; `command` is the
// full `leapmux worker --hub … --registration-key …` shell command.
//
// Rendered body:
//
//	Here's the worker registration command you asked LeapMux to send.
//
//	Run it on the machine where the worker should run:
//
//	    {command}
//
//	The registration key only works while the dialog stays open in
//	your browser, so keep that tab open until the command finishes.
//
//	-- ␠
//	This is an automated message from your LeapMux hub at {hubURL}.
//	Please do not reply.
//
// (The "␠" marker stands in for a literal trailing space on the "-- "
// signature delimiter; see RFC 3676 §4.3.)
func (r Renderer) RegistrationInstructions(to, command string) Message {
	body := fmt.Sprintf(
		"Here's the worker registration command you asked LeapMux to send.\n\n"+
			"Run it on the machine where the worker should run:\n\n    %s\n\n"+
			"The registration key only works while the dialog stays open in your browser, "+
			"so keep that tab open until the command finishes.\n\n%s",
		command,
		r.footer(),
	)
	return Message{
		To:      to,
		Subject: "[LeapMux] Your worker registration command",
		Body:    body,
	}
}
