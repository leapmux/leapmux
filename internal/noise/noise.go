// Package noise implements Noise_NK handshake and session management
// for end-to-end encrypted channels between Frontend and Worker.
package noise

import (
	"crypto/rand"
	"fmt"

	"github.com/flynn/noise"
)

// CipherSuite is Noise_NK_25519_ChaChaPoly_BLAKE2b.
var CipherSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2b)

const (
	// SoftNonceLimit triggers re-handshake when exceeded.
	SoftNonceLimit = uint64(1<<31 - 1) // 2^31-1
	// HardNonceLimit refuses to encrypt/decrypt when exceeded.
	HardNonceLimit = uint64(1<<32 - 1) // 2^32-1
	// MaxPlaintextSize is the Noise spec transport message limit minus auth tag.
	MaxPlaintextSize = 65535 - 16
)

// Keypair is an alias for the underlying Noise DHKey type (X25519).
type Keypair = noise.DHKey

// GenerateKeypair generates a new X25519 static key pair for use as
// a Noise responder (Worker) identity.
func GenerateKeypair() (noise.DHKey, error) {
	return CipherSuite.GenerateKeypair(rand.Reader)
}

// Session wraps a pair of Noise CipherState objects for bidirectional
// encrypted communication after a completed handshake.
type Session struct {
	Send    *noise.CipherState
	Receive *noise.CipherState
}

// Encrypt encrypts plaintext using the send cipher. Enforces the Noise
// spec plaintext size limit and nonce hard limit.
func (s *Session) Encrypt(plaintext []byte) ([]byte, error) {
	if len(plaintext) > MaxPlaintextSize {
		return nil, fmt.Errorf("noise: plaintext too large (%d > %d)", len(plaintext), MaxPlaintextSize)
	}
	if s.Send.Nonce() > HardNonceLimit {
		return nil, fmt.Errorf("noise: send nonce exceeded hard limit")
	}
	return s.Send.Encrypt(nil, nil, plaintext)
}

// Decrypt decrypts ciphertext using the receive cipher. Enforces the
// nonce hard limit.
func (s *Session) Decrypt(ciphertext []byte) ([]byte, error) {
	if s.Receive.Nonce() > HardNonceLimit {
		return nil, fmt.Errorf("noise: receive nonce exceeded hard limit")
	}
	return s.Receive.Decrypt(nil, nil, ciphertext)
}

// NeedsRekey returns true if the send nonce has exceeded the soft limit.
func (s *Session) NeedsRekey() bool {
	return s.Send.Nonce() > SoftNonceLimit
}

// ResponderHandshake performs the responder (Worker) side of Noise_NK.
// It takes the Worker's static key pair, the initiator's first handshake
// message, and returns the response message and established session.
func ResponderHandshake(staticKey Keypair, message1 []byte) (response []byte, session *Session, err error) {
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   CipherSuite,
		Pattern:       noise.HandshakeNK,
		Initiator:     false,
		StaticKeypair: staticKey,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create handshake state: %w", err)
	}

	// Process message 1 from initiator
	_, _, _, err = hs.ReadMessage(nil, message1)
	if err != nil {
		return nil, nil, fmt.Errorf("read handshake message 1: %w", err)
	}

	// Write message 2 (response)
	response, send, receive, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("write handshake message 2: %w", err)
	}

	if send == nil || receive == nil {
		return nil, nil, fmt.Errorf("handshake not complete after 2 messages")
	}

	return response, &Session{Send: send, Receive: receive}, nil
}

// InitiatorHandshake1 creates the initiator (Frontend) side of Noise_NK
// and produces the first handshake message. Call InitiatorHandshake2 with
// the responder's reply to complete the handshake.
//
// Returns the handshake state (needed for step 2) and the first message.
func InitiatorHandshake1(remoteStaticPubKey []byte) (hs *noise.HandshakeState, message1 []byte, err error) {
	hs, err = noise.NewHandshakeState(noise.Config{
		CipherSuite: CipherSuite,
		Pattern:     noise.HandshakeNK,
		Initiator:   true,
		PeerStatic:  remoteStaticPubKey,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create handshake state: %w", err)
	}

	message1, _, _, err = hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("write handshake message 1: %w", err)
	}

	return hs, message1, nil
}

// InitiatorHandshake2 completes the initiator side by processing the
// responder's handshake response message.
func InitiatorHandshake2(hs *noise.HandshakeState, message2 []byte) (*Session, error) {
	_, receive, send, err := hs.ReadMessage(nil, message2)
	if err != nil {
		return nil, fmt.Errorf("read handshake message 2: %w", err)
	}

	if send == nil || receive == nil {
		return nil, fmt.Errorf("handshake not complete after 2 messages")
	}

	return &Session{Send: send, Receive: receive}, nil
}
