package mail

import (
	"fmt"
	"strings"
	"time"

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

// accountRecoveryPath is the frontend route for self-service account
// recovery completion.
const accountRecoveryPath = "/recover-account/complete?token="

// Renderer builds the email Messages this package sends. It carries a
// base-URL closure so render call sites only pass per-message data
// (recipient, code, command) while an admin's public_url change applies
// to the next rendered email without a restart. The zero value
// Renderer{} is valid (empty base URL); tests that don't inspect URLs in
// the rendered output use it directly.
type Renderer struct {
	// BaseURL returns the absolute base URL the hub exposes. The renderer
	// uses it in two places: the absolute /verify-email link in the
	// verification email body, and the auto-message footer in every email's
	// body.
	BaseURL func() string
}

// hubURL resolves the renderer's base URL once per render.
func (r Renderer) hubURL() string {
	if r.BaseURL == nil {
		return ""
	}
	return r.BaseURL()
}

// footer renders the standard auto-message footer that identifies
// LeapMux and the hub's public URL. Every email this package sends uses this
// footer so recipients can identify the sender and know the mailbox is
// unattended.
func (r Renderer) footer() string {
	return footerSeparator +
		"This is an automated message from your LeapMux hub at " + r.hubURL() + ".\n" +
		"Please do not reply.\n"
}

// expiresIn renders a token lifetime as the prose the email bodies use.
// The two production lifetimes keep their pinned English; any other
// duration falls back to whole minutes so a future lifetime cannot render
// empty. Callers pass the same constant that governs the real expiry, so
// the sentence and the token cannot promise different lifetimes.
func expiresIn(ttl time.Duration) string {
	switch ttl {
	case time.Hour:
		return "one hour"
	case 30 * time.Minute:
		return "30 minutes"
	default:
		return fmt.Sprintf("%d minutes", int(ttl.Minutes()))
	}
}

// VerificationEmail builds the email that delivers a verification code
// to confirm a new or changed email address.
//
// Sent when:
//   - Password sign-up with email when SMTP verification is effective.
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
//	The code expires in {ttl}.
//
//	-- ␠
//	This is an automated message from your LeapMux hub at {hubURL}.
//	Please do not reply.
//
// (The "␠" marker stands in for a literal trailing space on the "-- "
// signature delimiter; see RFC 3676 §4.3.)
func (r Renderer) VerificationEmail(to, storedCode string, ttl time.Duration) Message {
	display := verifycode.Format(storedCode)
	link := r.hubURL() + verifyEmailPath + display
	var body strings.Builder
	body.WriteString("Use this code to verify your email address:\n\n    ")
	body.WriteString(display)
	body.WriteString("\n\nOr click the link below:\n\n    ")
	body.WriteString(link)
	body.WriteString("\n\nThe code expires in " + expiresIn(ttl) + ".\n\n")
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

// AccountRecoveryEmail builds the self-service account-recovery email.
//
// The body is identical for every account regardless of which sign-in
// methods it holds: naming the method the user lost would tell a mailbox
// reader which mechanisms the account uses. One link, one sentence, one
// action.
func (r Renderer) AccountRecoveryEmail(to, token string, ttl time.Duration) Message {
	link := r.hubURL() + accountRecoveryPath + token
	// expiresIn is an ARGUMENT, never spliced into the format string: a
	// concatenated value carries its own text into the verb list, so one
	// stray %% in a future duration string would shift every verb after it
	// and render "%!o(string=F)f an hour" into the delivered mail.
	body := fmt.Sprintf(
		"You asked to recover your LeapMux account.\n\n"+
			"Click the link below to set a new password and sign back in:\n\n    %s\n\n"+
			"The link expires in %s. If you did not request this, you can ignore this email.\n\n%s",
		link,
		expiresIn(ttl),
		r.footer(),
	)
	return Message{
		To:      to,
		Subject: "[LeapMux] Recover your account",
		Body:    body,
	}
}

// AppCredentialIssuedEmail tells the account owner that an app was authorized
// on their account and now holds a credential.
//
// It is the only signal that does not require the user to open Preferences and
// look, which is what makes it worth sending: a credential minted from a
// replayed browser session is otherwise silent until somebody uses it. The
// message lists the app, the installation the app reported, and every
// permission the account granted -- and says what to do about it.
//
// PERMISSIONS are listed in full rather than summarized. The scope set is
// exactly the permissions that the message tells the recipient they granted, so a message that
// said "some permissions" would leave them no way to tell an ordinary
// authorization from one that also administers the hub.
//
// byAdministrator changes it for a more important reason. TWO surfaces mint
// this credential: a browser consent the recipient performed, and an
// administrator's `api-token issue` for somebody else's account. "If this was
// you, nothing more is needed" is correct for the first surface and misleading
// for the second -- the recipient did nothing, the installation label is
// administrator-chosen, and the sentence invites them to conclude they
// authorized it. The notice exists because this is the surface a stolen
// administrator cookie reaches, so it must not read as a receipt.
func (r Renderer) AppCredentialIssuedEmail(to, appName, installationName string, scopes []string, byAdministrator bool) Message {
	if appName == "" {
		appName = "an unnamed app"
	}
	if installationName == "" {
		installationName = "an unnamed installation"
	}
	permissionLines := "    (none)\n"
	if len(scopes) > 0 {
		var b strings.Builder
		for _, scope := range scopes {
			b.WriteString("    - ")
			b.WriteString(scope)
			b.WriteString("\n")
		}
		permissionLines = b.String()
	}
	administersHub := false
	for _, scope := range scopes {
		if strings.HasPrefix(scope, "admin:") {
			administersHub = true
			break
		}
	}
	opening := "An app was authorized on your LeapMux account.\n"
	action := "If this was you, nothing more is needed. If it was not, sign in and\n" +
		"disconnect it under Preferences › Apps › Connected apps:\n"
	subject := "[LeapMux] An app was authorized on your account"
	if byAdministrator {
		opening = "An ADMINISTRATOR issued an app credential for your LeapMux account.\n" +
			"You did not authorize this yourself.\n"
		action = "If you did not expect it, revoke it and ask your administrator. Sign in\n" +
			"and open Preferences › Apps › Connected apps:\n"
		subject = "[LeapMux] An administrator issued an app credential for you"
	}
	if administersHub {
		subject = "[LeapMux] An app that ADMINISTERS THE HUB was authorized"
		if byAdministrator {
			subject = "[LeapMux] An administrator issued an app credential that ADMINISTERS THE HUB"
		}
	}
	body := fmt.Sprintf(
		"%s\n    App: %s\n    Installation: %s\n\nIt was granted these permissions:\n\n%s\n%s\n    %s\n\n%s",
		opening,
		appName,
		installationName,
		permissionLines,
		action,
		r.hubURL(),
		r.footer(),
	)
	return Message{
		To:      to,
		Subject: subject,
		Body:    body,
	}
}
