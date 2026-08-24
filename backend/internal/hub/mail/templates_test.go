package mail_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/mail"
)

// expectedVerificationBody and expectedRegistrationBody build the
// strict-byte expected output of the two render methods. They are
// constructed via string concatenation rather than a backtick raw-string
// literal so that editor save-on-format hooks cannot strip the trailing
// space on the "-- " (dash-dash-space) RFC 3676 §4.3 signature
// delimiter. If you need to update the template wording, also update
// these expectations.
const verificationFooterTrailingSpace = "-- " // intentional trailing space

func expectedVerificationBody(formattedCode, link, hubURL string) string {
	var b strings.Builder
	b.WriteString("Use this code to verify your email address:\n\n    ")
	b.WriteString(formattedCode)
	b.WriteString("\n\nOr click the link below:\n\n    ")
	b.WriteString(link)
	b.WriteString("\n\nThe code expires in 30 minutes.\n\n")
	b.WriteString(verificationFooterTrailingSpace + "\n")
	b.WriteString("This is an automated message from your LeapMux hub at " + hubURL + ".\n")
	b.WriteString("Please do not reply.\n")
	return b.String()
}

func expectedRegistrationBody(command, hubURL string) string {
	var b strings.Builder
	b.WriteString("Here's the worker registration command you asked LeapMux to send.\n\n")
	b.WriteString("Run it on the machine where the worker should run:\n\n    ")
	b.WriteString(command)
	b.WriteString("\n\n")
	b.WriteString("The registration key only works while the dialog stays open in your browser, ")
	b.WriteString("so keep that tab open until the command finishes.\n\n")
	b.WriteString(verificationFooterTrailingSpace + "\n")
	b.WriteString("This is an automated message from your LeapMux hub at " + hubURL + ".\n")
	b.WriteString("Please do not reply.\n")
	return b.String()
}

func expectedPasswordResetBody(link, hubURL string) string {
	var b strings.Builder
	b.WriteString("You requested a password reset for your LeapMux account.\n\n")
	b.WriteString("Click the link below to choose a new password:\n\n    ")
	b.WriteString(link)
	b.WriteString("\n\nThe link expires in one hour. If you did not request this, you can ignore this email.\n\n")
	b.WriteString(verificationFooterTrailingSpace + "\n")
	b.WriteString("This is an automated message from your LeapMux hub at " + hubURL + ".\n")
	b.WriteString("Please do not reply.\n")
	return b.String()
}

// TestRenderer_VerificationEmail_Bytes is the exactness oracle for the
// verification email's body. It pins every byte including the trailing
// space on "-- ", the blank line before the link, and the {hubURL}
// substitution in the footer.
func TestRenderer_VerificationEmail_Bytes(t *testing.T) {
	const storedCode = "ABC234"
	const formatted = "ABC-234"
	const hubURL = "https://hub.example.com"
	link := hubURL + "/verify-email?code=" + formatted

	r := mail.Renderer{BaseURL: func() string { return hubURL }}
	msg := r.VerificationEmail("alice@example.test", storedCode, 30*time.Minute)

	if msg.To != "alice@example.test" {
		t.Errorf("To = %q, want %q", msg.To, "alice@example.test")
	}
	if msg.Subject != "[LeapMux] Verify your email address" {
		t.Errorf("Subject = %q, want %q", msg.Subject, "[LeapMux] Verify your email address")
	}

	want := expectedVerificationBody(formatted, link, hubURL)
	if msg.Body != want {
		t.Errorf("Body bytes mismatch.\n got: %q\nwant: %q", msg.Body, want)
	}
	if !strings.Contains(msg.Body, "\n-- \n") {
		t.Error("body must contain the literal RFC 3676 §4.3 signature delimiter \"\\n-- \\n\" (dash-dash-space-newline)")
	}
}

// TestRenderer_PasswordResetEmail_Bytes pins the password reset email body.
func TestRenderer_PasswordResetEmail_Bytes(t *testing.T) {
	const token = "abc123"
	const hubURL = "https://hub.example.com"
	link := hubURL + "/reset-password?token=" + token

	r := mail.Renderer{BaseURL: func() string { return hubURL }}
	msg := r.PasswordResetEmail("alice@example.test", token, time.Hour)

	if msg.To != "alice@example.test" {
		t.Errorf("To = %q, want %q", msg.To, "alice@example.test")
	}
	if msg.Subject != "[LeapMux] Reset your password" {
		t.Errorf("Subject = %q, want %q", msg.Subject, "[LeapMux] Reset your password")
	}

	want := expectedPasswordResetBody(link, hubURL)
	if msg.Body != want {
		t.Errorf("Body bytes mismatch.\n got: %q\nwant: %q", msg.Body, want)
	}
}

// TestRenderer_RegistrationInstructions_Bytes pins the worker
// registration email's body byte-for-byte.
func TestRenderer_RegistrationInstructions_Bytes(t *testing.T) {
	const command = "leapmux worker --hub https://hub.example.com --registration-key abc123"
	const hubURL = "https://hub.example.com"

	r := mail.Renderer{BaseURL: func() string { return hubURL }}
	msg := r.RegistrationInstructions("bob@example.test", command)

	if msg.To != "bob@example.test" {
		t.Errorf("To = %q, want %q", msg.To, "bob@example.test")
	}
	if msg.Subject != "[LeapMux] Your worker registration command" {
		t.Errorf("Subject = %q, want %q", msg.Subject, "[LeapMux] Your worker registration command")
	}

	want := expectedRegistrationBody(command, hubURL)
	if msg.Body != want {
		t.Errorf("Body bytes mismatch.\n got: %q\nwant: %q", msg.Body, want)
	}
	if !strings.Contains(msg.Body, "\n-- \n") {
		t.Error("body must contain the literal RFC 3676 §4.3 signature delimiter \"\\n-- \\n\" (dash-dash-space-newline)")
	}
}

// printForExample renders the message for a testable Example. The
// trailing space on the signature delimiter "-- " is rewritten as
// "-- ␠" (open-box marker) so the // Output: block survives editor
// trailing-whitespace stripping. The strict-byte tests above are the
// real source of truth for wire bytes.
func printForExample(msg mail.Message) string {
	visible := strings.ReplaceAll(msg.Body, verificationFooterTrailingSpace+"\n", "-- ␠\n")
	return msg.Subject + "\n\n" + visible
}

func ExampleRenderer_VerificationEmail() {
	r := mail.Renderer{BaseURL: func() string { return "https://hub.example.com" }}
	msg := r.VerificationEmail("alice@example.test", "ABC234", 30*time.Minute)
	fmt.Print(printForExample(msg))
	// Output:
	// [LeapMux] Verify your email address
	//
	// Use this code to verify your email address:
	//
	//     ABC-234
	//
	// Or click the link below:
	//
	//     https://hub.example.com/verify-email?code=ABC-234
	//
	// The code expires in 30 minutes.
	//
	// -- ␠
	// This is an automated message from your LeapMux hub at https://hub.example.com.
	// Please do not reply.
}

func ExampleRenderer_RegistrationInstructions() {
	r := mail.Renderer{BaseURL: func() string { return "https://hub.example.com" }}
	msg := r.RegistrationInstructions(
		"bob@example.test",
		"leapmux worker --hub https://hub.example.com --registration-key abc123",
	)
	fmt.Print(printForExample(msg))
	// Output:
	// [LeapMux] Your worker registration command
	//
	// Here's the worker registration command you asked LeapMux to send.
	//
	// Run it on the machine where the worker should run:
	//
	//     leapmux worker --hub https://hub.example.com --registration-key abc123
	//
	// The registration key only works while the dialog stays open in your browser, so keep that tab open until the command finishes.
	//
	// -- ␠
	// This is an automated message from your LeapMux hub at https://hub.example.com.
	// Please do not reply.
}

// A renderer must reflect the TTL argument it was given: the sentence and
// the token lifetime come from one constant, so they cannot drift.
func TestRenderersReflectTTLArgument(t *testing.T) {
	v := mail.Renderer{}.VerificationEmail("alice@example.test", "ABC234", 2*time.Hour)
	if !strings.Contains(v.Body, "The code expires in 120 minutes.") {
		t.Errorf("verification body does not reflect the TTL: %q", v.Body)
	}
	p := mail.Renderer{}.PasswordResetEmail("alice@example.test", "tok", 2*time.Hour)
	if !strings.Contains(p.Body, "The link expires in 120 minutes.") {
		t.Errorf("reset body does not reflect the TTL: %q", p.Body)
	}
}
