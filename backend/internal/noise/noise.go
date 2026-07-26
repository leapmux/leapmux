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
	"sync/atomic"
	"time"

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

	// EphemeralPublicKeySize is the size of an X25519 ephemeral public key, as
	// carried by the in-band rekey's dh_pub field. Equals dhLen.
	EphemeralPublicKeySize = dhLen

	// MlkemPublicKeySize is the size of an ML-KEM-1024 encapsulation key.
	MlkemPublicKeySize = 1568
	// MlkemCiphertextSize is the size of an ML-KEM-1024 ciphertext.
	MlkemCiphertextSize = 1568
	// SlhdsaPublicKeySize is the size of an SLH-DSA-SHAKE-256f public key.
	SlhdsaPublicKeySize = 64
	// SlhdsaSignatureSize is the size of an SLH-DSA-SHAKE-256f signature.
	SlhdsaSignatureSize = 49856

	// RekeyGraceWindow is how long a previous receive CipherState stays live
	// for decryption after a successful in-band rekey. This unblocks traffic
	// during the rekey round trip: the initiator keeps encrypting under the old
	// send key until the Ack arrives, and frames it emitted just before the
	// swap can still be in flight on the wire (the Hub relay is FIFO per
	// channel, but the two directions are independent TCP streams). The
	// receiver tries the current key, then the previous one within this window.
	// Each retained key keeps its own nonce counter, so replayed old-key
	// frames are still rejected; after the window the previous key is zeroed
	// and an old-key frame fails closed. 10s accommodates a slow or contended
	// link where straddling frames may queue well beyond the bare RTT. See
	// issue #321.
	RekeyGraceWindow = 10 * time.Second
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
//
// n is atomic so soft-limit peeks (NeedsRekey / NeedsRekeyEither) can run from a
// send/idle goroutine while recvLoop Decrypt increments the receive nonce —
// without an unsynchronized data race on the counter.
//
// prev / prevExpiresAt support the rekey grace window: after a successful
// in-band rekey the previous receive key stays live for RekeyGraceWindow so
// frames the peer encrypted just before the swap (still in flight on the wire)
// can still be decrypted. Decrypt tries the current key, then the previous one
// if it has not expired. Only one previous key is retained; it is zeroed on
// expiry or on the next rekey.
//
// Concurrency: prev / prevExpiresAt are NOT guarded by a mutex. They are safe
// ONLY because of a load-bearing single-goroutine ownership rule: on BOTH peers,
// every read of these fields (Decrypt's try-current-then-prev) and every write
// (rekeyWithSecret via RekeyReceiveWithSecret on Ack/Request, and CipherState.zero
// via Session.Zero on close) runs on the channel's SINGLE recvLoop goroutine.
// That makes the receive swap and the prev read serial by construction — no
// mutex is needed on the read path. (The initiator's handleRekeyAck additionally
// takes rekeyMu, but that serializes the SEND rotation against concurrent Encrypt
// in sendInnerRaw, NOT the receive Decrypt against the receive swap.) The nonce
// counter `n` is the exception: it is atomic because NeedsRekey/Nonce peeks run
// from OTHER goroutines (send/idle).
//
// DO NOT move Decrypt, RekeyReceiveWithSecret, or Session.Zero off the recvLoop,
// and do not invoke them from a worker pool or a second goroutine — doing so
// races prev / prevExpiresAt with no compile-time guard, yielding a torn
// pointer read or use-after-zero (decrypt under an already-wiped key). If such
// a refactor is unavoidable, add a mutex around the prev swap here first.
type CipherState struct {
	k             [keyLen]byte
	n             atomic.Uint64
	prev          *CipherState // previous-epoch key retained during the grace window (receive only)
	prevExpiresAt time.Time    // wall time after which prev must be zeroed; zero time ⇒ none
}

// Encrypt encrypts plaintext using the cipher key.
func (cs *CipherState) Encrypt(plaintext []byte) ([]byte, error) {
	if len(plaintext) > MaxPlaintextSize {
		return nil, fmt.Errorf("noise: plaintext too large (%d > %d)", len(plaintext), MaxPlaintextSize)
	}
	n := cs.n.Load()
	if n > HardNonceLimit {
		return nil, fmt.Errorf("noise: send nonce exceeded hard limit")
	}
	ct, err := aeadEncrypt(cs.k[:], uint32(n), nil, plaintext)
	if err != nil {
		return nil, err
	}
	cs.n.Add(1)
	return ct, nil
}

// Decrypt decrypts ciphertext using the cipher key, falling back to the
// previous-epoch key if it is still within the grace window. This keeps traffic
// flowing across the rekey round trip: the peer keeps encrypting under the old
// send key until it observes the rotation, so a frame it emitted just before
// the swap may still arrive after we rotated. Each retained key keeps its own
// nonce counter, so a replayed old-key frame is still rejected.
func (cs *CipherState) Decrypt(ciphertext []byte) ([]byte, error) {
	n := cs.n.Load()
	if n > HardNonceLimit {
		return nil, fmt.Errorf("noise: receive nonce exceeded hard limit")
	}
	pt, err := aeadDecrypt(cs.k[:], uint32(n), nil, ciphertext)
	if err == nil {
		cs.n.Add(1)
		return pt, nil
	}
	// Current key failed. Try the previous-epoch key if it is still live. Safe
	// because Decrypt and the receive rekeyWithSecret both run on the channel's
	// single recvLoop goroutine (see the CipherState concurrency note); no
	// concurrent swap can free prev mid-decrypt.
	prev := cs.prev
	if prev != nil && !cs.prevExpiresAt.IsZero() {
		if time.Now().Before(cs.prevExpiresAt) {
			if pt2, perr := prev.tryDecrypt(ciphertext); perr == nil {
				return pt2, nil
			}
		} else {
			// The grace window elapsed: retire prev now rather than leave the
			// still-cryptographically-valid previous-epoch key in the heap. The
			// window's forward-secrecy promise ("after the window the previous
			// key is zeroed") is delivered here, not deferred to the next rekey
			// or to Session.Zero on close — an idle channel that never decrypts
			// again would otherwise keep prev alive indefinitely.
			zeroBytes(prev.k[:])
			cs.prev = nil
			cs.prevExpiresAt = time.Time{}
		}
	}
	return nil, err
}

// tryDecrypt decrypts without the prev fallback. Used by Decrypt for prev and
// by tests that want to assert the current key alone cannot decrypt a frame.
func (cs *CipherState) tryDecrypt(ciphertext []byte) ([]byte, error) {
	n := cs.n.Load()
	if n > HardNonceLimit {
		return nil, fmt.Errorf("noise: receive nonce exceeded hard limit")
	}
	pt, err := aeadDecrypt(cs.k[:], uint32(n), nil, ciphertext)
	if err != nil {
		return nil, err
	}
	cs.n.Add(1)
	return pt, nil
}

// Nonce returns the current nonce value.
func (cs *CipherState) Nonce() uint64 {
	return cs.n.Load()
}

// SetNonceForTest sets the cipher nonce. For tests that need SoftNonceLimit /
// HardNonceLimit without encrypting billions of times. Do not use in production.
func (cs *CipherState) SetNonceForTest(n uint64) {
	cs.n.Store(n)
}

// NeedsRekey returns true if the nonce has exceeded the soft limit.
func (cs *CipherState) NeedsRekey() bool {
	return cs.n.Load() > SoftNonceLimit
}

// rekeyWithSecret derives a new cipher key that mixes fresh key-agreement
// entropy into the current key, then swaps it in as the active key.
//
// When retainPrev is true (the receive direction) the previous key is moved
// into cs.prev (zeroing any older retained key first) and marked live for
// RekeyGraceWindow, so in-flight frames encrypted under it can still be
// decrypted. When retainPrev is false (the send direction, which keeps no
// grace window) the previous key is zeroed and dropped instead — structurally,
// not via a follow-up clearPrev the caller must remember. The nonce resets to 0.
//
// The derivation mirrors the Noise handshake's mixKey → hybridSplit ratchet
// (noise.go mixKey / hybridSplit), collapsed onto the transport key:
//
//	ck1 = HKDF(k, dhSecret)      // fresh X25519 ECDH(e_i, e_r)
//	ck2 = HKDF(ck1, pqSecret)    // fresh ML-KEM-1024 shared secret (nil in classic mode)
//	k'  = ck2[0:32]
//
// Keyed off this CipherState's own k, so the two transport directions stay
// distinct (Send and Receive start from different keys and produce different
// k'). dhSecret must be the raw X25519 shared secret; pqSecret is the raw
// ML-KEM shared secret, or nil for a classical-only channel. Both inputs are
// zeroed here.
//
// Forward secrecy: because k' mixes fresh DH + ML-KEM entropy, compromise of
// the current epoch key does NOT let an attacker derive the next. The Hub still
// never learns keys (E2EE relay); the rekey frames carrying the ephemeral
// pubkeys / ML-KEM ciphertext ride inside the existing AEAD-encrypted channel,
// so they are authenticated by the current cipher and need no SLH-DSA
// signature. See issue #321.
func (cs *CipherState) rekeyWithSecret(dhSecret, pqSecret []byte, retainPrev bool) {
	// Stage 1: mix the fresh X25519 ECDH secret into the current key.
	ck1, _ := noiseHKDF(cs.k[:], dhSecret)
	// Stage 2: mix the fresh ML-KEM shared secret. pqSecret is nil on a
	// classical channel, in which case noiseHKDF still runs a second
	// (degenerate, IKM-less) stage keyed only off ck1 — NOT skipped — so the
	// derivation stays a uniform two-stage ratchet on both channel kinds. Both
	// peers call this identically, so classic and PQ both agree on the key.
	newK, _ := noiseHKDF(ck1[:], pqSecret)

	// Retire any older retained previous key before deciding what to do with
	// the current one.
	if cs.prev != nil {
		zeroBytes(cs.prev.k[:])
		cs.prev = nil
		cs.prevExpiresAt = time.Time{}
	}

	if retainPrev {
		// Receive direction: promote the current key to prev for the grace
		// window, then install k'.
		cs.prev = &CipherState{k: cs.k, n: atomic.Uint64{}}
		cs.prev.n.Store(cs.n.Load())
		cs.prevExpiresAt = time.Now().Add(RekeyGraceWindow)
	} else {
		// Send direction: it keeps no grace window, so the key we are about to
		// replace is zeroed rather than retained. Doing this here (instead of
		// promoting to prev and relying on the caller to clearPrev) makes
		// "send never holds a previous key" a structural property — a caller
		// cannot forget the clearPrev and leave a live prev on Send.
		zeroBytes(cs.k[:])
	}

	copy(cs.k[:], newK[:keyLen])
	cs.n.Store(0)

	zeroBytes(ck1[:])
	zeroBytes(dhSecret)
	zeroBytes(pqSecret)
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

// NeedsRekeyEither returns true if either direction's nonce has exceeded the
// soft limit. Used by in-band rekey policy so a one-sided traffic flood can
// still force rotation.
func (s *Session) NeedsRekeyEither() bool {
	return s.Send.NeedsRekey() || s.Receive.NeedsRekey()
}

// RekeySendWithSecret rotates the send CipherState with fresh DH + (optional)
// ML-KEM entropy. Call after the peer has accepted a rekey and the local send
// barrier (rekeyMu / SendGate frame permit) is ready to swap so no concurrent
// Encrypt observes a half-installed key. The send direction does not retain a
// previous key — only the receiver needs the grace window — so rekeyWithSecret
// is called with retainPrev=false, which zeroes the replaced key rather than
// promoting it to prev.
func (s *Session) RekeySendWithSecret(dhSecret, pqSecret []byte) {
	s.Send.rekeyWithSecret(dhSecret, pqSecret, false)
}

// clearPrev zeroes and drops any retained previous-epoch key. Exported for
// tests and for any future send-direction path that needs to drop a retained
// prev explicitly; the production send rotation goes through RekeySendWithSecret
// (retainPrev=false) and never sets prev in the first place.
func (cs *CipherState) clearPrev() {
	if cs.prev != nil {
		zeroBytes(cs.prev.k[:])
		cs.prev = nil
	}
	cs.prevExpiresAt = time.Time{}
}

// RekeyReceiveWithSecret rotates the receive CipherState with fresh DH +
// (optional) ML-KEM entropy, retaining the previous key for RekeyGraceWindow so
// in-flight frames encrypted under it can still be decrypted. Call after
// decrypting a peer rekey frame that commits the switch, under the same lock
// that serializes Decrypt (the recvLoop is single-threaded on Go).
func (s *Session) RekeyReceiveWithSecret(dhSecret, pqSecret []byte) {
	s.Receive.rekeyWithSecret(dhSecret, pqSecret, true)
}

// zero wipes the active key and any retained grace-window previous key from
// this CipherState. Used by Session.Zero on close so transport keys do not
// linger in the heap after a channel ends — including a channel closed mid
// grace-window, which would otherwise leave the still-valid previous-epoch key
// sitting in memory until GC (a forward-secrecy gap the grace window widens).
func (cs *CipherState) zero() {
	zeroBytes(cs.k[:])
	if cs.prev != nil {
		zeroBytes(cs.prev.k[:])
		cs.prev = nil
	}
	cs.prevExpiresAt = time.Time{}
}

// Zero wipes both transport directions' active and retained previous-epoch keys
// so they do not linger in the heap after the channel closes. The grace window
// (#321) retains a previous receive key for up to RekeyGraceWindow after each
// rekey; closing the channel in that window must retire it explicitly rather
// than wait for GC, or a process memory dump in the window recovers a still-
// cryptographically-valid transport key.
//
// Locking contract: the caller MUST serialize Zero against any concurrent
// Encrypt/Decrypt/rekeyWithSecret on either direction. The tunnel initiator
// satisfies this from its recvLoop's close defer under rekeyMu (sendInnerRaw
// holds the same lock around Encrypt). The worker responder does NOT call Zero
// on close: its dispatcher runs handlers on independent goroutines that Encrypt
// with no shared lock, so zeroing from HandleClose would race them — the worker
// relies on GC for its send-direction keys instead.
func (s *Session) Zero() {
	s.Send.zero()
	s.Receive.zero()
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

// GenerateEphemeralX25519 generates a fresh X25519 ephemeral keypair, the
// shared generator used by both the Noise handshake and the in-band rekey. The
// caller must zero the returned private key's bytes when done (Clear / zeroBytes).
func GenerateEphemeralX25519() (*ecdh.PrivateKey, error) {
	return ecdh.X25519().GenerateKey(rand.Reader)
}

// GenerateEphemeralX25519Seed generates a fresh X25519 ephemeral private key as
// the raw 32-byte seed the CALLER owns and can actually zero. The in-band rekey
// paths use this (not GenerateEphemeralX25519) because crypto/ecdh's
// *PrivateKey keeps an internal copy of the scalar that the API gives no way to
// wipe — Zero(priv.Bytes()) only zeroes a throwaway copy, leaving the real
// scalar on the heap until GC. Holding the seed directly makes "wipe the
// ephemeral once both directions have rotated" genuinely true instead of
// aspirational, which matters: the fresh-DH entropy is the forward-secrecy
// guarantee of #321. Pair with X25519PublicFromSeed to derive the wire DhPub.
// The caller must zero the returned seed when done.
func GenerateEphemeralX25519Seed() ([]byte, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return priv.Bytes(), nil
}

// X25519PublicFromSeed reconstructs the public point from a raw 32-byte seed
// transiently, returning just the encoded public key. The reconstructed
// *ecdh.PrivateKey is discarded on return; the caller still owns (and must
// zero) the seed. Used by the rekey path to fill the RekeyRequest/RekeyAck
// dh_pub field from a seed held as a caller-owned slice.
func X25519PublicFromSeed(seed []byte) ([]byte, error) {
	priv, err := ecdh.X25519().NewPrivateKey(seed)
	if err != nil {
		return nil, fmt.Errorf("noise: invalid X25519 seed: %w", err)
	}
	return priv.PublicKey().Bytes(), nil
}

// DH performs an X25519 scalar multiplication between a local private key (raw
// 32-byte seed) and a remote public key (raw 32-byte point), returning the
// shared secret. It reconstructs the crypto/ecdh key objects from raw bytes,
// the same shape every handshake and rekey site uses, so there is one place to
// get the error handling and key parsing right. The caller must zero the
// returned secret when done.
func DH(localPriv, remotePub []byte) ([]byte, error) {
	pub, err := ecdh.X25519().NewPublicKey(remotePub)
	if err != nil {
		return nil, fmt.Errorf("noise: invalid X25519 public key: %w", err)
	}
	priv, err := ecdh.X25519().NewPrivateKey(localPriv)
	if err != nil {
		return nil, fmt.Errorf("noise: invalid X25519 private key: %w", err)
	}
	secret, err := priv.ECDH(pub)
	if err != nil {
		return nil, fmt.Errorf("noise: X25519 DH failed: %w", err)
	}
	return secret, nil
}

// RekeyMaterial is the fresh key-agreement output an in-band rekey carries
// between the two peers. It is the bridge between the wire (the dh_pub /
// mlkem_ct fields on RekeyRequest / RekeyAck) and CipherState.rekeyWithSecret.
type RekeyMaterial struct {
	// LocalEphemeralPriv is this side's fresh X25519 ephemeral private key.
	// Held only until both directions have rotated, then zeroed.
	LocalEphemeralPriv []byte
	// PeerEphemeralPub is the peer's fresh X25519 ephemeral public key.
	PeerEphemeralPub []byte
	// PQSharedSecret is the fresh ML-KEM-1024 shared secret (nil in classic mode).
	PQSharedSecret []byte
}

// DeriveRekeySecrets computes the (dhSecret, pqSecret) pair both peers feed into
// CipherState.rekeyWithSecret. dhSecret is the X25519 ECDH of the two fresh
// ephemerals; pqSecret is the ML-KEM shared secret established out-of-band
// (encapsulated by the initiator, decapsulated by the responder), or nil for a
// classical channel. Both inputs come from the same RekeyMaterial on each side,
// so the two directions mix identical entropy — direction-distinctness comes
// from each CipherState's own starting key. The caller must zero the returned
// secrets once both directions have rotated.
func DeriveRekeySecrets(m RekeyMaterial) (dhSecret, pqSecret []byte, err error) {
	if len(m.LocalEphemeralPriv) != dhLen {
		return nil, nil, fmt.Errorf("noise: rekey ephemeral private key wrong size: got %d, want %d", len(m.LocalEphemeralPriv), dhLen)
	}
	if len(m.PeerEphemeralPub) != dhLen {
		return nil, nil, fmt.Errorf("noise: rekey peer ephemeral public key wrong size: got %d, want %d", len(m.PeerEphemeralPub), dhLen)
	}
	dhSecret, err = DH(m.LocalEphemeralPriv, m.PeerEphemeralPub)
	if err != nil {
		return nil, nil, err
	}
	return dhSecret, m.PQSharedSecret, nil
}

// EncapsulateRekeyPQ encapsulates a fresh ML-KEM-1024 shared secret under the
// worker's static encapsulation key, returning (sharedSecret, ciphertext). The
// initiator sends the ciphertext in the RekeyRequest; the responder decapsulates
// it to recover the same shared secret. Empty mlkemPub returns (nil, nil) so a
// classical channel skips PQ entropy uniformly. The caller must zero
// sharedSecret once both directions have rotated.
func EncapsulateRekeyPQ(mlkemPub []byte) (sharedSecret, ciphertext []byte, err error) {
	if len(mlkemPub) == 0 {
		return nil, nil, nil
	}
	ek, err := mlkem.NewEncapsulationKey1024(mlkemPub)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: parse ML-KEM encapsulation key: %w", err)
	}
	ss, ct := ek.Encapsulate()
	return ss, ct, nil
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
	dhES, err := DH(compositeKey.X25519Private, re)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: es DH(responder static, initiator ephemeral): %w", err)
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
	ePriv, err := GenerateEphemeralX25519()
	if err != nil {
		return nil, nil, fmt.Errorf("noise: generate responder ephemeral: %w", err)
	}
	ePub := ePriv.PublicKey().Bytes()
	ss.mixHash(ePub)

	// ee: DH(e, re).
	dhEE, err := DH(ePriv.Bytes(), re)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: ee DH(responder ephemeral, initiator ephemeral): %w", err)
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
	ePriv, err := GenerateEphemeralX25519()
	if err != nil {
		return nil, nil, fmt.Errorf("noise: generate initiator ephemeral: %w", err)
	}
	ePub := ePriv.PublicKey().Bytes()
	ss.mixHash(ePub)

	// es: DH(e, rs).
	dhES, err := DH(ePriv.Bytes(), remoteX25519Pub)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: es DH(initiator ephemeral, responder static): %w", err)
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
	dhEE, err := DH(hs.ePriv.Bytes(), re)
	if err != nil {
		return nil, fmt.Errorf("noise: ee DH(initiator ephemeral, responder ephemeral): %w", err)
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

	dhES, err := DH(x25519Priv, re)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: es DH(responder static, initiator ephemeral): %w", err)
	}
	ss.mixKey(dhES)
	zeroBytes(dhES)

	if _, err = ss.decryptAndHash(message1[dhLen:]); err != nil {
		return nil, nil, fmt.Errorf("noise: decrypt message1 payload: %w", err)
	}

	ePriv, err := GenerateEphemeralX25519()
	if err != nil {
		return nil, nil, fmt.Errorf("noise: generate responder ephemeral: %w", err)
	}
	ePub := ePriv.PublicKey().Bytes()
	ss.mixHash(ePub)

	dhEE, err := DH(ePriv.Bytes(), re)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: ee DH(responder ephemeral, initiator ephemeral): %w", err)
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

	ePriv, err := GenerateEphemeralX25519()
	if err != nil {
		return nil, nil, fmt.Errorf("noise: generate initiator ephemeral: %w", err)
	}
	ePub := ePriv.PublicKey().Bytes()
	ss.mixHash(ePub)

	dhES, err := DH(ePriv.Bytes(), remoteX25519Pub)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: es DH(initiator ephemeral, responder static): %w", err)
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

	dhEE, err := DH(hs.ePriv.Bytes(), re)
	if err != nil {
		return nil, fmt.Errorf("noise: ee DH(initiator ephemeral, responder ephemeral): %w", err)
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

// Zero overwrites a byte slice with zeros. It is the exported form of zeroBytes
// for callers outside the package (channel / tunnel rekey orchestrators) that
// must wipe fresh DH / ML-KEM secrets once both directions have rotated.
func Zero(b []byte) {
	zeroBytes(b)
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
