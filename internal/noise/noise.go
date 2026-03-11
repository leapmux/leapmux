// Package noise implements a hybrid post-quantum Noise_NK handshake and session
// management for end-to-end encrypted channels between Frontend and Worker.
//
// The protocol combines classical X25519 + ChaCha20-Poly1305 + BLAKE2b with
// post-quantum ML-KEM-1024 (FIPS 203) and SLH-DSA-SHAKE-256f (FIPS 205).
// Security is maintained even if either the classical or PQ algorithm is broken.
package noise

import (
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/mlkem"
	"crypto/rand"
	"fmt"
	"hash"

	"github.com/cloudflare/circl/sign/slhdsa"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	// SoftNonceLimit triggers re-handshake when exceeded.
	SoftNonceLimit = uint64(1<<31 - 1) // 2^31-1
	// HardNonceLimit refuses to encrypt/decrypt when exceeded.
	HardNonceLimit = uint64(1<<32 - 1) // 2^32-1
	// MaxPlaintextSize is the Noise spec transport message limit minus auth tag.
	MaxPlaintextSize = 65535 - 16

	protocolName = "Noise_NK_25519_ChaChaPoly_BLAKE2b"
	hashLen      = 64 // BLAKE2b output length
	dhLen        = 32 // X25519 key length
	keyLen       = 32 // ChaCha20-Poly1305 key length

	// MlkemPublicKeySize is the size of an ML-KEM-1024 encapsulation key.
	MlkemPublicKeySize = 1568
	// MlkemCiphertextSize is the size of an ML-KEM-1024 ciphertext.
	MlkemCiphertextSize = 1568
	// SlhdsaPublicKeySize is the size of an SLH-DSA-SHAKE-256f public key.
	SlhdsaPublicKeySize = 64
	// SlhdsaSignatureSize is the size of an SLH-DSA-SHAKE-256f signature.
	SlhdsaSignatureSize = 49856
)

// ---- Composite Keypair ----

// CompositeKeypair holds X25519, ML-KEM-1024, and SLH-DSA-SHAKE-256f key material.
type CompositeKeypair struct {
	// X25519
	X25519Public  []byte
	X25519Private []byte

	// ML-KEM-1024
	MlkemDecapsulationKey *mlkem.DecapsulationKey1024

	// SLH-DSA-SHAKE-256f
	SlhdsaPublicKey  slhdsa.PublicKey
	SlhdsaPrivateKey slhdsa.PrivateKey
}

// MlkemPublicKeyBytes returns the ML-KEM-1024 encapsulation key bytes.
func (ck *CompositeKeypair) MlkemPublicKeyBytes() []byte {
	return ck.MlkemDecapsulationKey.EncapsulationKey().Bytes()
}

// SlhdsaPublicKeyBytes returns the SLH-DSA public key bytes.
func (ck *CompositeKeypair) SlhdsaPublicKeyBytes() ([]byte, error) {
	return ck.SlhdsaPublicKey.MarshalBinary()
}

// Fingerprint returns a 4-word composite fingerprint of all public keys.
func (ck *CompositeKeypair) Fingerprint() string {
	slhdsaPub, _ := ck.SlhdsaPublicKeyBytes()
	return CompositeKeyFingerprint(ck.X25519Public, ck.MlkemPublicKeyBytes(), slhdsaPub)
}

// GenerateCompositeKeypair generates X25519 + ML-KEM-1024 + SLH-DSA-SHAKE-256f key material.
func GenerateCompositeKeypair() (*CompositeKeypair, error) {
	// X25519
	x25519Priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate X25519 key: %w", err)
	}

	// ML-KEM-1024
	mlkemDK, err := mlkem.GenerateKey1024()
	if err != nil {
		return nil, fmt.Errorf("generate ML-KEM-1024 key: %w", err)
	}

	// SLH-DSA-SHAKE-256f
	slhdsaPub, slhdsaPriv, err := slhdsa.GenerateKey(rand.Reader, slhdsa.SHAKE_256f)
	if err != nil {
		return nil, fmt.Errorf("generate SLH-DSA key: %w", err)
	}

	return &CompositeKeypair{
		X25519Public:          x25519Priv.PublicKey().Bytes(),
		X25519Private:         x25519Priv.Bytes(),
		MlkemDecapsulationKey: mlkemDK,
		SlhdsaPublicKey:       slhdsaPub,
		SlhdsaPrivateKey:      slhdsaPriv,
	}, nil
}

// RestoreCompositeKeypair reconstructs a CompositeKeypair from serialized key bytes.
func RestoreCompositeKeypair(x25519Pub, x25519Priv, mlkemPrivBytes, slhdsaPubBytes, slhdsaPrivBytes []byte) (*CompositeKeypair, error) {
	mlkemDK, err := mlkem.NewDecapsulationKey1024(mlkemPrivBytes)
	if err != nil {
		return nil, fmt.Errorf("parse ML-KEM decapsulation key: %w", err)
	}

	slhdsaPub := slhdsa.PublicKey{ID: slhdsa.SHAKE_256f}
	if err := slhdsaPub.UnmarshalBinary(slhdsaPubBytes); err != nil {
		return nil, fmt.Errorf("parse SLH-DSA public key: %w", err)
	}

	slhdsaPriv := slhdsa.PrivateKey{ID: slhdsa.SHAKE_256f}
	if err := slhdsaPriv.UnmarshalBinary(slhdsaPrivBytes); err != nil {
		return nil, fmt.Errorf("parse SLH-DSA private key: %w", err)
	}

	return &CompositeKeypair{
		X25519Public:          x25519Pub,
		X25519Private:         x25519Priv,
		MlkemDecapsulationKey: mlkemDK,
		SlhdsaPublicKey:       slhdsaPub,
		SlhdsaPrivateKey:      slhdsaPriv,
	}, nil
}

// ---- Symmetric State (Noise protocol core) ----

type symmetricState struct {
	h    [hashLen]byte // handshake hash
	ck   [hashLen]byte // chaining key
	hasK bool
	k    [keyLen]byte // cipher key
	n    uint32       // nonce counter
}

func newSymmetricState() *symmetricState {
	ss := &symmetricState{}
	// Protocol name fits in 64 bytes, so we pad it.
	nameBytes := []byte(protocolName)
	copy(ss.h[:], nameBytes)
	ss.ck = ss.h
	return ss
}

func (ss *symmetricState) mixHash(data []byte) {
	h, _ := blake2b.New512(nil)
	h.Write(ss.h[:])
	h.Write(data)
	copy(ss.h[:], h.Sum(nil))
}

func (ss *symmetricState) mixKey(ikm []byte) {
	ck, tempK := noiseHKDF(ss.ck[:], ikm)
	ss.ck = ck
	copy(ss.k[:], tempK[:keyLen])
	ss.n = 0
	ss.hasK = true
}

func (ss *symmetricState) encryptAndHash(plaintext []byte) ([]byte, error) {
	if !ss.hasK {
		ss.mixHash(plaintext)
		out := make([]byte, len(plaintext))
		copy(out, plaintext)
		return out, nil
	}
	ct, err := aeadEncrypt(ss.k[:], ss.n, ss.h[:], plaintext)
	if err != nil {
		return nil, err
	}
	ss.mixHash(ct)
	ss.n++
	return ct, nil
}

func (ss *symmetricState) decryptAndHash(ciphertext []byte) ([]byte, error) {
	if !ss.hasK {
		ss.mixHash(ciphertext)
		out := make([]byte, len(ciphertext))
		copy(out, ciphertext)
		return out, nil
	}
	pt, err := aeadDecrypt(ss.k[:], ss.n, ss.h[:], ciphertext)
	if err != nil {
		return nil, err
	}
	ss.mixHash(ciphertext)
	ss.n++
	return pt, nil
}

func (ss *symmetricState) split() (*CipherState, *CipherState) {
	tempK1, tempK2 := noiseHKDF(ss.ck[:], nil)
	var k1, k2 [keyLen]byte
	copy(k1[:], tempK1[:keyLen])
	copy(k2[:], tempK2[:keyLen])
	return &CipherState{k: k1}, &CipherState{k: k2}
}

// hybridSplit mixes extra key material (ML-KEM shared secret) into the chaining
// key before deriving the final cipher keys. This binds both classical and PQ
// secrets into the session keys.
func (ss *symmetricState) hybridSplit(extraKeyMaterial []byte) (*CipherState, *CipherState) {
	// Mix ML-KEM shared secret into chaining key.
	ck2, _ := noiseHKDF(ss.ck[:], extraKeyMaterial)
	// Derive cipher keys from the combined chaining key.
	tempK1, tempK2 := noiseHKDF(ck2[:], nil)
	var k1, k2 [keyLen]byte
	copy(k1[:], tempK1[:keyLen])
	copy(k2[:], tempK2[:keyLen])
	return &CipherState{k: k1}, &CipherState{k: k2}
}

// ---- CipherState (post-handshake) ----

// CipherState manages a key and nonce for post-handshake encryption/decryption.
type CipherState struct {
	k           [keyLen]byte
	n           uint64
	passthrough bool // when true, data passes through without encryption
}

// Encrypt encrypts plaintext using the cipher key.
func (cs *CipherState) Encrypt(plaintext []byte) ([]byte, error) {
	if cs.passthrough {
		out := make([]byte, len(plaintext))
		copy(out, plaintext)
		return out, nil
	}
	if len(plaintext) > MaxPlaintextSize {
		return nil, fmt.Errorf("noise: plaintext too large (%d > %d)", len(plaintext), MaxPlaintextSize)
	}
	if cs.n > HardNonceLimit {
		return nil, fmt.Errorf("noise: send nonce exceeded hard limit")
	}
	ct, err := aeadEncrypt(cs.k[:], uint32(cs.n), nil, plaintext)
	if err != nil {
		return nil, err
	}
	cs.n++
	return ct, nil
}

// Decrypt decrypts ciphertext using the cipher key.
func (cs *CipherState) Decrypt(ciphertext []byte) ([]byte, error) {
	if cs.passthrough {
		out := make([]byte, len(ciphertext))
		copy(out, ciphertext)
		return out, nil
	}
	if cs.n > HardNonceLimit {
		return nil, fmt.Errorf("noise: receive nonce exceeded hard limit")
	}
	pt, err := aeadDecrypt(cs.k[:], uint32(cs.n), nil, ciphertext)
	if err != nil {
		return nil, err
	}
	cs.n++
	return pt, nil
}

// Nonce returns the current nonce value.
func (cs *CipherState) Nonce() uint64 {
	return cs.n
}

// NeedsRekey returns true if the nonce has exceeded the soft limit.
func (cs *CipherState) NeedsRekey() bool {
	if cs.passthrough {
		return false
	}
	return cs.n > SoftNonceLimit
}

// ---- Session ----

// Session wraps send/receive CipherState objects for bidirectional
// encrypted communication after a completed handshake.
type Session struct {
	Send    *CipherState
	Receive *CipherState
}

// Encrypt encrypts plaintext using the send cipher.
func (s *Session) Encrypt(plaintext []byte) ([]byte, error) {
	return s.Send.Encrypt(plaintext)
}

// Decrypt decrypts ciphertext using the receive cipher.
func (s *Session) Decrypt(ciphertext []byte) ([]byte, error) {
	return s.Receive.Decrypt(ciphertext)
}

// NeedsRekey returns true if the send nonce has exceeded the soft limit.
func (s *Session) NeedsRekey() bool {
	return s.Send.NeedsRekey()
}

// NewPassthroughSession creates a Session that passes data through without encryption.
// Used when encryption is disabled (solo mode only).
func NewPassthroughSession() *Session {
	return &Session{
		Send:    &CipherState{passthrough: true},
		Receive: &CipherState{passthrough: true},
	}
}

// ---- Handshake State (for two-step initiator) ----

// HandshakeState holds intermediate state between handshake messages.
type HandshakeState struct {
	ss      *symmetricState
	ePriv   *ecdh.PrivateKey
	rs      []byte // remote static public key (X25519)
	mlkemSS []byte // ML-KEM shared secret
	mlkemCT []byte // ML-KEM ciphertext (needed for transcript)
}

// ---- Responder Handshake (Worker) ----

// ResponderHandshake performs the responder (Worker) side of the hybrid Noise_NK handshake.
// It takes the Worker's composite keypair and the initiator's first handshake message
// (which contains noise_msg1 || mlkem_ciphertext), and returns the response message
// (noise_msg2 || slhdsa_signature) and established session.
func ResponderHandshake(compositeKey *CompositeKeypair, message1 []byte) (response []byte, session *Session, err error) {
	// message1 = noise_msg1 (32 + 16 = 48 bytes) || mlkem_ciphertext (1568 bytes)
	noiseMsg1Len := dhLen + chacha20poly1305.Overhead // 32 + 16 = 48
	expectedLen := noiseMsg1Len + MlkemCiphertextSize
	if len(message1) != expectedLen {
		return nil, nil, fmt.Errorf("noise: message1 wrong size: got %d, want %d", len(message1), expectedLen)
	}

	noiseMsg1 := message1[:noiseMsg1Len]
	mlkemCT := message1[noiseMsg1Len:]

	// Initialize symmetric state.
	ss := newSymmetricState()

	// Mix empty prologue.
	ss.mixHash(nil)

	// Pre-message: <- s (mix responder's static public key).
	ss.mixHash(compositeKey.X25519Public)

	// Read message 1: -> e, es
	// Read initiator's ephemeral public key.
	re := noiseMsg1[:dhLen]
	ss.mixHash(re)

	// es: DH(s, re) — responder's static private with initiator's ephemeral.
	reKey, err := ecdh.X25519().NewPublicKey(re)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: invalid initiator ephemeral key: %w", err)
	}
	sPriv, err := ecdh.X25519().NewPrivateKey(compositeKey.X25519Private)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: invalid responder static private key: %w", err)
	}
	dhES, err := sPriv.ECDH(reKey)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: es DH failed: %w", err)
	}
	ss.mixKey(dhES)
	zeroBytes(dhES)

	// Decrypt payload from message 1.
	_, err = ss.decryptAndHash(noiseMsg1[dhLen:])
	if err != nil {
		return nil, nil, fmt.Errorf("noise: decrypt message1 payload: %w", err)
	}

	// Bind ML-KEM ciphertext into the handshake hash so that tampering with
	// the ciphertext causes the message2 AEAD to fail, independent of the
	// SLH-DSA transcript signature.
	ss.mixHash(mlkemCT)

	// Write message 2: <- e, ee
	// Generate responder ephemeral.
	ePriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: generate responder ephemeral: %w", err)
	}
	ePub := ePriv.PublicKey().Bytes()
	ss.mixHash(ePub)

	// ee: DH(e, re).
	dhEE, err := ePriv.ECDH(reKey)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: ee DH failed: %w", err)
	}
	ss.mixKey(dhEE)
	zeroBytes(dhEE)

	// Encrypt empty payload.
	encPayload, err := ss.encryptAndHash(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: encrypt message2 payload: %w", err)
	}

	noiseMsg2 := append(ePub, encPayload...)

	// ML-KEM decapsulation.
	mlkemSS, err := compositeKey.MlkemDecapsulationKey.Decapsulate(mlkemCT)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: ML-KEM decapsulate: %w", err)
	}

	// Compute transcript for SLH-DSA signing.
	transcript := computeTranscript(ss.h[:], mlkemCT, mlkemSS)

	// Sign transcript with SLH-DSA.
	sig, err := compositeKey.SlhdsaPrivateKey.Sign(rand.Reader, transcript, nil)
	if err != nil {
		zeroBytes(mlkemSS)
		return nil, nil, fmt.Errorf("noise: SLH-DSA sign: %w", err)
	}

	// Hybrid split: combine classical ck with ML-KEM shared secret.
	send, recv := ss.hybridSplit(mlkemSS)
	zeroBytes(mlkemSS)
	ss.clear()

	return append(noiseMsg2, sig...), &Session{Send: send, Receive: recv}, nil
}

// ---- Initiator Handshake (Frontend, Go side for testing) ----

// InitiatorHandshake1 creates the first handshake message for the hybrid Noise_NK initiator.
// Returns the handshake state (needed for step 2) and the first message
// (noise_msg1 || mlkem_ciphertext).
func InitiatorHandshake1(remoteX25519Pub, remoteMlkemPub []byte) (*HandshakeState, []byte, error) {
	// Initialize symmetric state.
	ss := newSymmetricState()

	// Mix empty prologue.
	ss.mixHash(nil)

	// Pre-message: <- s (mix responder's static public key).
	ss.mixHash(remoteX25519Pub)

	// -> e, es
	// Generate ephemeral keypair.
	ePriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: generate initiator ephemeral: %w", err)
	}
	ePub := ePriv.PublicKey().Bytes()
	ss.mixHash(ePub)

	// es: DH(e, rs).
	rsKey, err := ecdh.X25519().NewPublicKey(remoteX25519Pub)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: invalid remote static public key: %w", err)
	}
	dhES, err := ePriv.ECDH(rsKey)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: es DH failed: %w", err)
	}
	ss.mixKey(dhES)
	zeroBytes(dhES)

	// Encrypt empty payload.
	encPayload, err := ss.encryptAndHash(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: encrypt message1 payload: %w", err)
	}

	noiseMsg1 := append(ePub, encPayload...)

	// ML-KEM encapsulation.
	mlkemEK, err := mlkem.NewEncapsulationKey1024(remoteMlkemPub)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: parse ML-KEM encapsulation key: %w", err)
	}
	mlkemSS, mlkemCT := mlkemEK.Encapsulate()

	// Bind ML-KEM ciphertext into the handshake hash (matches responder's mixHash).
	ss.mixHash(mlkemCT)

	message1 := append(noiseMsg1, mlkemCT...)

	return &HandshakeState{
		ss:      ss,
		ePriv:   ePriv,
		rs:      remoteX25519Pub,
		mlkemSS: mlkemSS,
		mlkemCT: mlkemCT,
	}, message1, nil
}

// InitiatorHandshake2 completes the initiator side by processing the responder's
// handshake response message (noise_msg2 || slhdsa_signature).
// It verifies the SLH-DSA signature and combines keys via HKDF.
func InitiatorHandshake2(hs *HandshakeState, message2 []byte, remoteSlhdsaPub []byte) (*Session, error) {
	noiseMsg2Len := dhLen + chacha20poly1305.Overhead // 32 + 16 = 48
	expectedLen := noiseMsg2Len + SlhdsaSignatureSize
	if len(message2) != expectedLen {
		return nil, fmt.Errorf("noise: message2 wrong size: got %d, want %d", len(message2), expectedLen)
	}

	noiseMsg2 := message2[:noiseMsg2Len]
	slhdsaSig := message2[noiseMsg2Len:]

	ss := hs.ss

	// <- e, ee
	// Read responder's ephemeral public key.
	re := noiseMsg2[:dhLen]
	ss.mixHash(re)

	// ee: DH(e, re).
	reKey, err := ecdh.X25519().NewPublicKey(re)
	if err != nil {
		return nil, fmt.Errorf("noise: invalid responder ephemeral key: %w", err)
	}
	dhEE, err := hs.ePriv.ECDH(reKey)
	if err != nil {
		return nil, fmt.Errorf("noise: ee DH failed: %w", err)
	}
	ss.mixKey(dhEE)
	zeroBytes(dhEE)

	// Decrypt payload.
	_, err = ss.decryptAndHash(noiseMsg2[dhLen:])
	if err != nil {
		return nil, fmt.Errorf("noise: decrypt message2 payload: %w", err)
	}

	// Verify SLH-DSA signature over transcript.
	transcript := computeTranscript(ss.h[:], hs.mlkemCT, hs.mlkemSS)

	slhdsaPubKey := slhdsa.PublicKey{ID: slhdsa.SHAKE_256f}
	if err := slhdsaPubKey.UnmarshalBinary(remoteSlhdsaPub); err != nil {
		return nil, fmt.Errorf("noise: parse SLH-DSA public key: %w", err)
	}

	if !slhdsa.Verify(&slhdsaPubKey, slhdsa.NewMessage(transcript), slhdsaSig, nil) {
		return nil, fmt.Errorf("noise: SLH-DSA signature verification failed")
	}

	// Hybrid split: combine classical ck with ML-KEM shared secret.
	// Initiator convention: send=cs2, receive=cs1.
	cs1, cs2 := ss.hybridSplit(hs.mlkemSS)
	ss.clear()
	hs.Clear()

	return &Session{Send: cs2, Receive: cs1}, nil
}

// ---- Classical Responder Handshake (Worker, X25519 only) ----

// ClassicalResponderHandshake performs the responder side of the classical Noise_NK handshake.
// message1 = noise_msg1 (48 bytes), response = noise_msg2 (48 bytes).
func ClassicalResponderHandshake(x25519Pub, x25519Priv []byte, message1 []byte) (response []byte, session *Session, err error) {
	noiseMsg1Len := dhLen + chacha20poly1305.Overhead // 48
	if len(message1) != noiseMsg1Len {
		return nil, nil, fmt.Errorf("noise: classical message1 wrong size: got %d, want %d", len(message1), noiseMsg1Len)
	}

	ss := newSymmetricState()
	ss.mixHash(nil)
	ss.mixHash(x25519Pub)

	re := message1[:dhLen]
	ss.mixHash(re)

	reKey, err := ecdh.X25519().NewPublicKey(re)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: invalid initiator ephemeral key: %w", err)
	}
	sPriv, err := ecdh.X25519().NewPrivateKey(x25519Priv)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: invalid responder static private key: %w", err)
	}
	dhES, err := sPriv.ECDH(reKey)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: es DH failed: %w", err)
	}
	ss.mixKey(dhES)
	zeroBytes(dhES)

	if _, err = ss.decryptAndHash(message1[dhLen:]); err != nil {
		return nil, nil, fmt.Errorf("noise: decrypt message1 payload: %w", err)
	}

	ePriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: generate responder ephemeral: %w", err)
	}
	ePub := ePriv.PublicKey().Bytes()
	ss.mixHash(ePub)

	dhEE, err := ePriv.ECDH(reKey)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: ee DH failed: %w", err)
	}
	ss.mixKey(dhEE)
	zeroBytes(dhEE)

	encPayload, err := ss.encryptAndHash(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: encrypt message2 payload: %w", err)
	}

	send, recv := ss.split()
	ss.clear()
	return append(ePub, encPayload...), &Session{Send: send, Receive: recv}, nil
}

// ---- Classical Initiator Handshake (Frontend, Go side for testing) ----

// ClassicalInitiatorHandshake1 creates the first message for the classical Noise_NK initiator.
func ClassicalInitiatorHandshake1(remoteX25519Pub []byte) (*HandshakeState, []byte, error) {
	ss := newSymmetricState()
	ss.mixHash(nil)
	ss.mixHash(remoteX25519Pub)

	ePriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: generate initiator ephemeral: %w", err)
	}
	ePub := ePriv.PublicKey().Bytes()
	ss.mixHash(ePub)

	rsKey, err := ecdh.X25519().NewPublicKey(remoteX25519Pub)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: invalid remote static public key: %w", err)
	}
	dhES, err := ePriv.ECDH(rsKey)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: es DH failed: %w", err)
	}
	ss.mixKey(dhES)
	zeroBytes(dhES)

	encPayload, err := ss.encryptAndHash(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: encrypt message1 payload: %w", err)
	}

	return &HandshakeState{
		ss:    ss,
		ePriv: ePriv,
		rs:    remoteX25519Pub,
	}, append(ePub, encPayload...), nil
}

// ClassicalInitiatorHandshake2 completes the classical Noise_NK initiator handshake.
func ClassicalInitiatorHandshake2(hs *HandshakeState, message2 []byte) (*Session, error) {
	noiseMsg2Len := dhLen + chacha20poly1305.Overhead // 48
	if len(message2) != noiseMsg2Len {
		return nil, fmt.Errorf("noise: classical message2 wrong size: got %d, want %d", len(message2), noiseMsg2Len)
	}

	ss := hs.ss

	re := message2[:dhLen]
	ss.mixHash(re)

	reKey, err := ecdh.X25519().NewPublicKey(re)
	if err != nil {
		return nil, fmt.Errorf("noise: invalid responder ephemeral key: %w", err)
	}
	dhEE, err := hs.ePriv.ECDH(reKey)
	if err != nil {
		return nil, fmt.Errorf("noise: ee DH failed: %w", err)
	}
	ss.mixKey(dhEE)
	zeroBytes(dhEE)

	if _, err = ss.decryptAndHash(message2[dhLen:]); err != nil {
		return nil, fmt.Errorf("noise: decrypt message2 payload: %w", err)
	}

	cs1, cs2 := ss.split()
	ss.clear()
	hs.Clear()
	return &Session{Send: cs2, Receive: cs1}, nil
}

// ---- Key material zeroing ----

// zeroBytes overwrites a byte slice with zeros to limit key material lifetime in memory.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// clear zeroes the symmetric state's sensitive fields after split/hybridSplit.
func (ss *symmetricState) clear() {
	zeroBytes(ss.ck[:])
	zeroBytes(ss.k[:])
	zeroBytes(ss.h[:])
	ss.hasK = false
	ss.n = 0
}

// Clear zeroes all sensitive fields in the HandshakeState.
// Only secrets are zeroed — public keys (rs) are not sensitive.
// Called automatically by InitiatorHandshake2 after completing the handshake.
func (hs *HandshakeState) Clear() {
	if hs.ss != nil {
		hs.ss.clear()
		hs.ss = nil
	}
	hs.ePriv = nil
	zeroBytes(hs.mlkemSS)
	zeroBytes(hs.mlkemCT)
}

// ---- Crypto primitives ----

func blake2bHash() hash.Hash {
	h, _ := blake2b.New512(nil)
	return h
}

// noiseHKDF implements the Noise HKDF using HMAC-BLAKE2b.
func noiseHKDF(chainingKey, inputKeyMaterial []byte) (out1 [hashLen]byte, out2 [hashLen]byte) {
	// Extract
	mac := hmac.New(blake2bHash, chainingKey)
	if inputKeyMaterial != nil {
		mac.Write(inputKeyMaterial)
	}
	tempKey := mac.Sum(nil)

	// Expand 1
	mac = hmac.New(blake2bHash, tempKey)
	mac.Write([]byte{1})
	o1 := mac.Sum(nil)
	copy(out1[:], o1)

	// Expand 2
	mac = hmac.New(blake2bHash, tempKey)
	mac.Write(o1)
	mac.Write([]byte{2})
	copy(out2[:], mac.Sum(nil))

	return
}

// aeadEncrypt encrypts with ChaCha20-Poly1305.
func aeadEncrypt(key []byte, nonce uint32, ad, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	nonceBytes := make([]byte, chacha20poly1305.NonceSize)
	nonceBytes[4] = byte(nonce)
	nonceBytes[5] = byte(nonce >> 8)
	nonceBytes[6] = byte(nonce >> 16)
	nonceBytes[7] = byte(nonce >> 24)
	return aead.Seal(nil, nonceBytes, plaintext, ad), nil
}

// aeadDecrypt decrypts with ChaCha20-Poly1305.
func aeadDecrypt(key []byte, nonce uint32, ad, ciphertext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	nonceBytes := make([]byte, chacha20poly1305.NonceSize)
	nonceBytes[4] = byte(nonce)
	nonceBytes[5] = byte(nonce >> 8)
	nonceBytes[6] = byte(nonce >> 16)
	nonceBytes[7] = byte(nonce >> 24)
	return aead.Open(nil, nonceBytes, ciphertext, ad)
}

// computeTranscript computes BLAKE2b(handshake_hash || mlkem_ct || mlkem_ss).
func computeTranscript(handshakeHash, mlkemCT, mlkemSS []byte) []byte {
	h, _ := blake2b.New512(nil)
	h.Write(handshakeHash)
	h.Write(mlkemCT)
	h.Write(mlkemSS)
	return h.Sum(nil)
}
