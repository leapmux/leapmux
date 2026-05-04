package mail

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/hub/config"
)

// SMTPConfig configures an SMTPSender. Production callers fill the first
// six fields from hub config; the last two are test-only injection
// points that let smtp_test.go point the sender at an in-process fake
// without touching the OS's TLS root pool or DNS.
type SMTPConfig struct {
	// Host is the SMTP relay's hostname (e.g. "smtp.example.com"). Used
	// both for the network dial and for TLS ServerName / PlainAuth host
	// pinning.
	Host string
	// Port is the SMTP TCP port. Conventional values: 25 (none), 465
	// (implicit), 587 (starttls).
	Port int
	// Username, when non-empty, triggers SMTP AUTH PLAIN. An empty
	// Username skips authentication — appropriate for trusted local
	// relays.
	Username string
	// Password is the credential paired with Username.
	Password string
	// From is the envelope sender address. Must be a parseable
	// net/mail.ParseAddress value; the bare local@domain form is used
	// for SMTP MAIL FROM and the full canonical form for the From:
	// header.
	From string
	// TLSMode selects between starttls / implicit / none. Use the
	// SmtpTLSMode* constants from internal/hub/config.
	TLSMode string

	// TLSConfig, when non-nil, overrides the default TLS configuration.
	// Tests use this to inject a self-signed-cert RootCAs pool. The
	// SMTPSender always clones this value per Send, so callers can
	// safely share a single config across goroutines.
	TLSConfig *tls.Config
	// Dialer, when non-nil, overrides the default
	// (&net.Dialer{}).DialContext for the initial TCP connection. Tests
	// use this to point at a loopback fake without depending on DNS or
	// OS routing. The hook always produces a raw TCP connection — the
	// SMTPSender wraps it in TLS as needed.
	Dialer func(ctx context.Context, network, addr string) (net.Conn, error)
}

// SMTPSender delivers Messages over real SMTP. Safe for concurrent use:
// each Send opens a fresh connection.
type SMTPSender struct {
	cfg SMTPConfig
}

// NewSMTPSender returns a Sender that delivers via the configured SMTP
// relay. The config is used as-is on each Send, so callers should fully
// populate it before construction; mid-flight mutation is unsupported.
func NewSMTPSender(cfg SMTPConfig) *SMTPSender {
	return &SMTPSender{cfg: cfg}
}

// Send delivers msg via SMTP. Returns nil on success.
//
// The flow:
//  1. Reject CRLF in addresses and subject (header-injection defense).
//  2. Parse From / To via net/mail.ParseAddress and remember both the
//     bare envelope address (for MAIL FROM / RCPT TO) and the canonical
//     header form (for the From: / To: headers).
//  3. Clone the TLS config and default ServerName to cfg.Host so
//     concurrent Send calls and tests can't accidentally disable
//     hostname verification on a shared config.
//  4. Dial. For implicit mode, wrap the raw connection with tls.Client
//     and HandshakeContext(ctx); for STARTTLS, dial in the clear and
//     issue STARTTLS after EHLO; for none, dial in the clear.
//  5. Spawn a watchdog goroutine that closes the connection on
//     ctx.Done(). A local done channel + defer close(done) ensures the
//     goroutine exits even when ctx is context.Background() (whose
//     Done() never fires) — without it, every Send would leak a
//     goroutine.
//  6. AUTH PLAIN if Username is set.
//  7. Build RFC 5322 headers + body and write via Data(). The textproto
//     DotWriter handles dot-stuffing AND \n→\r\n translation, so the
//     body is written with plain LF line endings.
func (s *SMTPSender) Send(ctx context.Context, msg Message) error {
	cfg := s.cfg
	if err := validateNoCRLF("To", msg.To); err != nil {
		return err
	}
	if err := validateNoCRLF("Subject", msg.Subject); err != nil {
		return err
	}
	if err := validateNoCRLF("From", cfg.From); err != nil {
		return err
	}

	toAddr, err := mail.ParseAddress(msg.To)
	if err != nil {
		return fmt.Errorf("parse to address: %w", err)
	}
	fromAddr, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return fmt.Errorf("parse from address: %w", err)
	}

	// Clone TLSConfig per Send; default ServerName when unset. Cloning
	// shields concurrent Sends — and any test that injects a shared
	// *tls.Config — from accidental mutation, and the ServerName
	// default closes the door on hostname-verification misconfiguration.
	var tlsCfg *tls.Config
	if cfg.TLSConfig != nil {
		tlsCfg = cfg.TLSConfig.Clone()
	} else {
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if tlsCfg.ServerName == "" {
		tlsCfg.ServerName = cfg.Host
	}

	dial := cfg.Dialer
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial smtp: %w", err)
	}
	// Watchdog: close the connection on ctx cancellation so EHLO/AUTH/
	// DATA — none of which are ctx-aware in stdlib — can be interrupted.
	// done lets the watchdog exit on success even when ctx is Background
	// (Done never fires, so without done the goroutine would leak).
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	// If ctx has a deadline, propagate it as an I/O deadline for the
	// rest of the SMTP exchange. Stdlib's smtp.Client doesn't accept a
	// context, but it does honor net.Conn deadlines.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	switch cfg.TLSMode {
	case config.SmtpTLSModeImplicit:
		// Wrap-after-dial keeps the Dialer hook usable across all three
		// TLS modes; tls.Dialer{NetDialer: …} only accepts *net.Dialer,
		// not an arbitrary dial function.
		tlsConn := tls.Client(conn, tlsCfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return fmt.Errorf("tls handshake: %w", err)
		}
		conn = tlsConn
	case config.SmtpTLSModeSTARTTLS, config.SmtpTLSModeNone, "":
		// Stay plaintext for now; STARTTLS upgrade happens after EHLO.
	default:
		_ = conn.Close()
		return fmt.Errorf("unsupported smtp tls mode: %q", cfg.TLSMode)
	}

	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp greet: %w", err)
	}
	defer func() { _ = c.Close() }()

	if cfg.TLSMode == config.SmtpTLSModeSTARTTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("smtp: server did not advertise STARTTLS")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}

	if cfg.Username != "" {
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	// Envelope vs header forms: c.Mail / c.Rcpt expect a bare
	// `local@domain`; the From: / To: headers want the full quoted form
	// (which includes any display name). Reusing one variable for both
	// would silently send malformed envelopes when callers add a
	// display name to From.
	if err := c.Mail(fromAddr.Address); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	if err := c.Rcpt(toAddr.Address); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if err := writeMessage(w, fromAddr, toAddr, msg, cfg.Host); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp data close: %w", err)
	}
	if err := c.Quit(); err != nil {
		return fmt.Errorf("smtp quit: %w", err)
	}
	return nil
}

// validateNoCRLF rejects any \r or \n in field. Header injection (e.g.
// To = "victim@x\r\nBcc: leak@y") would otherwise let an attacker who
// controls one header line inject arbitrary additional headers.
func validateNoCRLF(name, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("invalid %s: contains CR or LF", name)
	}
	return nil
}

// writeMessage emits an RFC 5322 message: headers, blank line, body.
// The DotWriter passed in handles \n→\r\n translation and dot-stuffing,
// so we write with plain LF line endings.
func writeMessage(w interface {
	Write([]byte) (int, error)
}, from, to *mail.Address, msg Message, host string) error {
	subject := msg.Subject
	if !isASCII(subject) {
		// Q-encode non-ASCII subjects per RFC 2047. The output is
		// "=?utf-8?Q?…?=" or "=?utf-8?q?…?=" depending on stdlib
		// implementation — both are valid encoded-word framings.
		subject = mime.QEncoding.Encode("utf-8", subject)
	}

	headers := []string{
		"From: " + from.String(),
		"To: " + to.String(),
		"Subject: " + subject,
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
		"Message-ID: " + newMessageID(host),
		"MIME-Version: 1.0",
		// We deliberately do NOT advertise format=flowed: that profile
		// would only matter if we implemented flowed-text reflowing,
		// and the byte-level output already preserves the RFC 3676
		// signature delimiter on its own.
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: 8bit",
	}
	header := strings.Join(headers, "\n") + "\n\n"
	if _, err := w.Write([]byte(header)); err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg.Body)); err != nil {
		return err
	}
	return nil
}

// isASCII reports whether s contains only ASCII characters.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7f {
			return false
		}
	}
	return true
}

// newMessageID returns a fresh RFC 5322 Message-ID. The local part is
// 16 bytes of crypto/rand entropy hex-encoded; the domain is the SMTP
// host so receiving MTAs see a recognizable origin.
func newMessageID(host string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	if host == "" {
		host = "leapmux.invalid"
	}
	return "<" + hex.EncodeToString(b[:]) + "@" + host + ">"
}
