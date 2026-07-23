package mail_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"math/big"
	"mime"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/mail"
)

// fakeSMTPServer is a minimal in-process SMTP server for unit tests. It
// handles 220/EHLO/STARTTLS/AUTH/MAIL/RCPT/DATA/QUIT well enough to
// exercise our SMTPSender code path. It is NOT a production SMTP relay
// and does not validate inputs beyond what the tests rely on.
type fakeSMTPServer struct {
	t *testing.T

	listener net.Listener
	tlsCfg   *tls.Config // server-side cert (used for STARTTLS or implicit-TLS listener)

	// Set by options at construction and never written afterwards, so
	// serve() can read them from its own goroutine without synchronisation.
	advertiseSTARTTLS bool // when false, EHLO response omits STARTTLS
	requireImplicit   bool // when true, the listener wraps every accepted conn in TLS

	mu       sync.Mutex
	received []*receivedMessage
	authBlob string // last AUTH PLAIN payload (base64), empty if none
	authUsed bool
}

type receivedMessage struct {
	from string
	to   []string
	data string
}

// fakeSMTPOption configures the server BEFORE its accept loop starts.
//
// These have to be options rather than fields a test assigns afterwards:
// serve() reads them on every accepted connection from its own goroutine,
// so a post-construction write races the read. The race detector caught
// exactly that on requireImplicit.
type fakeSMTPOption func(*fakeSMTPServer)

// withoutSTARTTLS makes the EHLO response omit STARTTLS.
func withoutSTARTTLS() fakeSMTPOption {
	return func(s *fakeSMTPServer) { s.advertiseSTARTTLS = false }
}

// withImplicitTLS wraps every accepted connection in TLS.
func withImplicitTLS() fakeSMTPOption {
	return func(s *fakeSMTPServer) { s.requireImplicit = true }
}

func newFakeSMTPServer(t *testing.T, opts ...fakeSMTPOption) *fakeSMTPServer {
	t.Helper()
	tlsCfg, _ := newSelfSignedTLS(t, "127.0.0.1")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeSMTPServer{
		t:                 t,
		listener:          ln,
		tlsCfg:            tlsCfg,
		advertiseSTARTTLS: true,
	}
	for _, opt := range opts {
		opt(s)
	}
	// Started only once configuration is complete, so serve() never reads
	// a field a test is still writing.
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeSMTPServer) addr() string { return s.listener.Addr().String() }

func (s *fakeSMTPServer) lastMessage() *receivedMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.received) == 0 {
		return nil
	}
	return s.received[len(s.received)-1]
}

func (s *fakeSMTPServer) lastAuthBlob() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authBlob
}

func (s *fakeSMTPServer) sawAuth() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authUsed
}

func (s *fakeSMTPServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		if s.requireImplicit {
			tlsConn := tls.Server(conn, s.tlsCfg)
			if err := tlsConn.Handshake(); err != nil {
				_ = conn.Close()
				continue
			}
			conn = tlsConn
		}
		go s.handle(conn)
	}
}

func (s *fakeSMTPServer) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	writeLine := func(line string) {
		_, _ = w.WriteString(line + "\r\n")
		_ = w.Flush()
	}

	writeLine("220 fake.example.test ESMTP")

	var (
		envFrom string
		envTo   []string
	)

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(strings.ToUpper(line), "EHLO"):
			writeLine("250-fake.example.test")
			writeLine("250-AUTH PLAIN")
			if s.advertiseSTARTTLS {
				writeLine("250-STARTTLS")
			}
			writeLine("250 8BITMIME")
		case strings.EqualFold(line, "STARTTLS"):
			writeLine("220 Ready to start TLS")
			tlsConn := tls.Server(conn, s.tlsCfg)
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			conn = tlsConn
			r = bufio.NewReader(conn)
			w = bufio.NewWriter(conn)
			writeLine = func(line string) {
				_, _ = w.WriteString(line + "\r\n")
				_ = w.Flush()
			}
		case strings.HasPrefix(strings.ToUpper(line), "AUTH PLAIN"):
			fields := strings.Fields(line)
			s.mu.Lock()
			s.authUsed = true
			if len(fields) > 2 {
				s.authBlob = fields[2]
			}
			s.mu.Unlock()
			writeLine("235 Authentication successful")
		case strings.HasPrefix(strings.ToUpper(line), "MAIL FROM:"):
			envFrom = extractAngleAddr(line)
			writeLine("250 OK")
		case strings.HasPrefix(strings.ToUpper(line), "RCPT TO:"):
			envTo = append(envTo, extractAngleAddr(line))
			writeLine("250 OK")
		case strings.EqualFold(line, "DATA"):
			writeLine("354 End data with <CR><LF>.<CR><LF>")
			var buf strings.Builder
			for {
				dataLine, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if dataLine == ".\r\n" {
					break
				}
				// Undo dot-stuffing.
				if strings.HasPrefix(dataLine, "..") {
					dataLine = dataLine[1:]
				}
				buf.WriteString(dataLine)
			}
			s.mu.Lock()
			s.received = append(s.received, &receivedMessage{
				from: envFrom,
				to:   envTo,
				data: buf.String(),
			})
			s.mu.Unlock()
			writeLine("250 OK queued")
			envFrom, envTo = "", nil
		case strings.EqualFold(line, "QUIT"):
			writeLine("221 Bye")
			return
		case strings.EqualFold(line, "RSET"):
			envFrom, envTo = "", nil
			writeLine("250 OK")
		default:
			writeLine("500 unknown command")
		}
	}
}

// extractAngleAddr pulls the bare email out of "MAIL FROM:<addr>" /
// "RCPT TO:<addr>" lines. Returns "" if no angle-bracket address is
// present, which the tests treat as a failure.
func extractAngleAddr(s string) string {
	lt := strings.IndexByte(s, '<')
	gt := strings.LastIndexByte(s, '>')
	if lt < 0 || gt < 0 || gt <= lt {
		return ""
	}
	return s[lt+1 : gt]
}

// newSelfSignedTLS mints a single-use ECDSA self-signed certificate
// covering the given hostnames/IPs. Returns the server-side
// *tls.Config and a *x509.CertPool the client can trust.
func newSelfSignedTLS(t *testing.T, hosts ...string) (*tls.Config, *x509.CertPool) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "leapmux-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
	pool := x509.NewCertPool()
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	pool.AddCert(parsed)
	return &tls.Config{Certificates: []tls.Certificate{cert}}, pool
}

// dialerToHostPort returns an SMTPConfig.Dialer that ignores the addr
// argument and always connects to the fake server's actual port. We
// still set Host="127.0.0.1" so PlainAuth's localhost rule is happy and
// TLS ServerName matches the cert SAN.
func dialerToHostPort(target string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, target)
	}
}

func TestSMTPSender_None_PlaintextDelivery(t *testing.T) {
	srv := newFakeSMTPServer(t, withoutSTARTTLS())

	sender := mail.NewSMTPSender(mail.SMTPConfig{
		Host:    "127.0.0.1",
		Port:    25,
		From:    "Hub <hub@example.test>",
		TLSMode: "none",
		Dialer:  dialerToHostPort(srv.addr()),
	})
	if err := sender.Send(context.Background(), mail.Message{
		To:      "alice@example.test",
		Subject: "[LeapMux] Verify your email address",
		Body:    "hello\n-- \nfooter\n",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got := srv.lastMessage()
	if got == nil {
		t.Fatal("server received no message")
	}
	if got.from != "hub@example.test" {
		t.Errorf("envelope From = %q, want bare envelope address", got.from)
	}
	if len(got.to) != 1 || got.to[0] != "alice@example.test" {
		t.Errorf("envelope To = %v, want [alice@example.test]", got.to)
	}
	if !strings.Contains(got.data, "From: \"Hub\" <hub@example.test>") {
		t.Errorf("From: header missing display-name form: %q", got.data)
	}
	if !strings.Contains(got.data, "Subject: [LeapMux] Verify your email address") {
		t.Errorf("Subject header missing: %q", got.data)
	}
	if !strings.Contains(got.data, "\nhello\r\n-- \r\nfooter\r\n") {
		t.Errorf("body bytes do not match expected wire form (DotWriter LF→CRLF): %q", got.data)
	}
	if srv.sawAuth() {
		t.Error("AUTH was used despite empty Username")
	}
}

func TestSMTPSender_None_AuthPLAIN(t *testing.T) {
	srv := newFakeSMTPServer(t, withoutSTARTTLS())

	sender := mail.NewSMTPSender(mail.SMTPConfig{
		Host:     "127.0.0.1",
		Port:     25,
		Username: "alice",
		Password: "s3cret",
		From:     "hub@example.test",
		TLSMode:  "none",
		Dialer:   dialerToHostPort(srv.addr()),
	})
	if err := sender.Send(context.Background(), mail.Message{
		To:      "bob@example.test",
		Subject: "x",
		Body:    "y\n",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !srv.sawAuth() {
		t.Fatal("expected AUTH PLAIN, got none")
	}
	// RFC 4616: the SASL PLAIN payload is base64("\0" username "\0" password).
	raw, err := base64.StdEncoding.DecodeString(srv.lastAuthBlob())
	if err != nil {
		t.Fatalf("decode auth blob: %v", err)
	}
	want := "\x00alice\x00s3cret"
	if string(raw) != want {
		t.Errorf("AUTH PLAIN payload = %q, want %q", string(raw), want)
	}
}

func TestSMTPSender_STARTTLS_Upgrade(t *testing.T) {
	srv := newFakeSMTPServer(t)
	// Use the fake server's actual cert for client trust.
	// (newSelfSignedTLS mints a fresh cert each call, so we can't reuse
	// the result of an additional call here.)
	clientPool := poolFromServerCfg(t, srv.tlsCfg)

	sender := mail.NewSMTPSender(mail.SMTPConfig{
		Host:    "127.0.0.1",
		Port:    587,
		From:    "hub@example.test",
		TLSMode: "starttls",
		TLSConfig: &tls.Config{
			RootCAs:    clientPool,
			ServerName: "127.0.0.1",
		},
		Dialer: dialerToHostPort(srv.addr()),
	})
	if err := sender.Send(context.Background(), mail.Message{
		To:      "alice@example.test",
		Subject: "starttls",
		Body:    "body\n",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := srv.lastMessage(); got == nil {
		t.Fatal("no message received after STARTTLS upgrade")
	}
}

func TestSMTPSender_STARTTLS_RequiredButMissing(t *testing.T) {
	srv := newFakeSMTPServer(t, withoutSTARTTLS())

	sender := mail.NewSMTPSender(mail.SMTPConfig{
		Host:    "127.0.0.1",
		Port:    587,
		From:    "hub@example.test",
		TLSMode: "starttls",
		Dialer:  dialerToHostPort(srv.addr()),
	})
	err := sender.Send(context.Background(), mail.Message{
		To: "alice@example.test", Subject: "x", Body: "y\n",
	})
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("expected STARTTLS error, got %v", err)
	}
}

func TestSMTPSender_Implicit_TLSDelivery(t *testing.T) {
	srv := newFakeSMTPServer(t, withImplicitTLS())
	clientPool := poolFromServerCfg(t, srv.tlsCfg)

	sender := mail.NewSMTPSender(mail.SMTPConfig{
		Host:    "127.0.0.1",
		Port:    465,
		From:    "hub@example.test",
		TLSMode: "implicit",
		TLSConfig: &tls.Config{
			RootCAs:    clientPool,
			ServerName: "127.0.0.1",
		},
		Dialer: dialerToHostPort(srv.addr()),
	})
	if err := sender.Send(context.Background(), mail.Message{
		To:      "alice@example.test",
		Subject: "implicit",
		Body:    "body\n",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := srv.lastMessage(); got == nil {
		t.Fatal("no message received over implicit TLS")
	}
}

func TestSMTPSender_RejectsHeaderInjection(t *testing.T) {
	srv := newFakeSMTPServer(t, withoutSTARTTLS())

	sender := mail.NewSMTPSender(mail.SMTPConfig{
		Host:    "127.0.0.1",
		Port:    25,
		From:    "hub@example.test",
		TLSMode: "none",
		Dialer:  dialerToHostPort(srv.addr()),
	})
	cases := []struct {
		name string
		msg  mail.Message
	}{
		{
			name: "CRLF in To",
			msg:  mail.Message{To: "victim@x.test\r\nBcc: leak@y.test", Subject: "ok", Body: "b\n"},
		},
		{
			name: "CRLF in Subject",
			msg:  mail.Message{To: "victim@x.test", Subject: "ok\r\nBcc: leak", Body: "b\n"},
		},
		{
			name: "LF only in To",
			msg:  mail.Message{To: "victim@x.test\nBcc: leak@y.test", Subject: "ok", Body: "b\n"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := sender.Send(context.Background(), tc.msg)
			if err == nil || !strings.Contains(err.Error(), "CR or LF") {
				t.Fatalf("expected header-injection rejection, got %v", err)
			}
		})
	}
	if got := srv.lastMessage(); got != nil {
		t.Errorf("server should not have received any message; got %+v", got)
	}
}

func TestSMTPSender_SubjectEncoding(t *testing.T) {
	srv := newFakeSMTPServer(t, withoutSTARTTLS())

	sender := mail.NewSMTPSender(mail.SMTPConfig{
		Host:    "127.0.0.1",
		Port:    25,
		From:    "hub@example.test",
		TLSMode: "none",
		Dialer:  dialerToHostPort(srv.addr()),
	})

	t.Run("ASCII subject is not encoded", func(t *testing.T) {
		if err := sender.Send(context.Background(), mail.Message{
			To: "alice@example.test", Subject: "Plain ASCII", Body: "b\n",
		}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		got := srv.lastMessage()
		if !strings.Contains(got.data, "\r\nSubject: Plain ASCII\r\n") {
			t.Errorf("ASCII subject must pass through unchanged: %q", got.data)
		}
	})

	t.Run("non-ASCII subject is RFC 2047 encoded-word", func(t *testing.T) {
		const original = "héllo wörld"
		if err := sender.Send(context.Background(), mail.Message{
			To: "alice@example.test", Subject: original, Body: "b\n",
		}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		got := srv.lastMessage()
		// Find the Subject header line.
		var subjectLine string
		for _, line := range strings.Split(got.data, "\r\n") {
			if strings.HasPrefix(line, "Subject: ") {
				subjectLine = strings.TrimPrefix(line, "Subject: ")
				break
			}
		}
		if subjectLine == "" {
			t.Fatalf("no Subject header in body: %q", got.data)
		}
		// Assert encoded-word framing — the Q-vs-q letter case is
		// implementation-defined by stdlib, so don't pin it.
		if !strings.HasPrefix(strings.ToLower(subjectLine), "=?utf-8?") || !strings.HasSuffix(subjectLine, "?=") {
			t.Errorf("expected RFC 2047 encoded-word framing, got %q", subjectLine)
		}
		// Round-trip via mime.WordDecoder: the decoded subject must
		// match the original.
		dec := &mime.WordDecoder{}
		decoded, err := dec.DecodeHeader(subjectLine)
		if err != nil {
			t.Fatalf("decode subject: %v", err)
		}
		if decoded != original {
			t.Errorf("round-trip mismatch: got %q, want %q", decoded, original)
		}
	})
}

func TestSMTPSender_ContextCancelledBeforeDial(t *testing.T) {
	srv := newFakeSMTPServer(t)

	sender := mail.NewSMTPSender(mail.SMTPConfig{
		Host:    "127.0.0.1",
		Port:    25,
		From:    "hub@example.test",
		TLSMode: "none",
		Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Honor the context the sender hands us.
			var d net.Dialer
			return d.DialContext(ctx, network, srv.addr())
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := sender.Send(ctx, mail.Message{
		To: "alice@example.test", Subject: "x", Body: "y\n",
	})
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected wrapped context.Canceled, got %v", err)
	}
}

// poolFromServerCfg lifts the leaf cert from a server-side *tls.Config
// (where it lives as a tls.Certificate) into a *x509.CertPool the
// client can trust. Avoids re-minting cert material per test.
func poolFromServerCfg(t *testing.T, cfg *tls.Config) *x509.CertPool {
	t.Helper()
	if len(cfg.Certificates) == 0 || len(cfg.Certificates[0].Certificate) == 0 {
		t.Fatal("server tls config has no certificates")
	}
	leaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatalf("parse server cert: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return pool
}
