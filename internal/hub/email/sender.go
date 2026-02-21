package email

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
)

// Sender sends emails via SMTP.
type Sender struct {
	host        string
	port        int
	username    string
	password    string
	fromAddress string
	useTLS      bool
}

// NewSender creates a new Sender with the given SMTP configuration.
func NewSender(host string, port int, username, password, fromAddress string, useTLS bool) *Sender {
	return &Sender{
		host:        host,
		port:        port,
		username:    username,
		password:    password,
		fromAddress: fromAddress,
		useTLS:      useTLS,
	}
}

// SendVerificationEmail sends a verification email containing a link with the given token.
func (s *Sender) SendVerificationEmail(to, token string) error {
	subject := "Verify your email"
	body := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"></head>
<body>
  <h2>Email Verification</h2>
  <p>Please click the link below to verify your email address:</p>
  <p><a href="https://florence.example.com/verify/%s">Verify Email</a></p>
  <p>If you did not request this, you can safely ignore this email.</p>
</body>
</html>`, token)

	return s.SendEmail(to, subject, body)
}

// SendEmail sends an email with the given subject and HTML body to the specified recipient.
func (s *Sender) SendEmail(to, subject, body string) error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	auth := smtp.PlainAuth("", s.username, s.password, s.host)

	headers := []string{
		fmt.Sprintf("From: %s", s.fromAddress),
		fmt.Sprintf("To: %s", to),
		fmt.Sprintf("Subject: %s", subject),
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=UTF-8",
	}
	msg := []byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + body)

	if s.useTLS {
		return s.sendWithTLS(addr, auth, to, msg)
	}

	return smtp.SendMail(addr, auth, s.fromAddress, []string{to}, msg)
}

// sendWithTLS sends an email over an explicit TLS connection.
func (s *Sender) sendWithTLS(addr string, auth smtp.Auth, to string, msg []byte) error {
	tlsConfig := &tls.Config{
		ServerName: s.host,
	}

	conn, err := tls.Dial("tcp", addr, tlsConfig)
	if err != nil {
		return fmt.Errorf("tls dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("split host port: %w", err)
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("new smtp client: %w", err)
	}
	defer func() { _ = client.Close() }()

	if err = client.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}

	if err = client.Mail(s.fromAddress); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}

	if err = client.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}

	if _, err = w.Write(msg); err != nil {
		return fmt.Errorf("write message: %w", err)
	}

	if err = w.Close(); err != nil {
		return fmt.Errorf("close data writer: %w", err)
	}

	return client.Quit()
}
