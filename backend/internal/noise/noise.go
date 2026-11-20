// Package noise implements a hybrid post-quantum Noise_NK handshake and session
// management for end-to-end encrypted channels between Frontend and Worker.
//
// The protocol combines classical X25519 + ChaCha20-Poly1305 + BLAKE2b with
// post-quantum ML-KEM-1024 (FIPS 203) and SLH-DSA-SHAKE-256f (FIPS 205).
// Security is maintained even if either the classical or PQ algorithm is broken.
package noise

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/mlkem"
	"crypto/rand"
	"errors"
	"fmt"
	"hash"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/cloudflare/circl/sign/slhdsa"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	hashLen = 64                            // BLAKE2b output length
	dhLen   = contracts.X25519PublicKeySize // X25519 key length
	keyLen  = 32                            // ChaCha20-Poly1305 key length

	classicMessage1Size     = dhLen + contracts.MlkemCiphertextSize + 2*chacha20poly1305.Overhead
	postQuantumMessage1Size = classicMessage1Size + contracts.MlkemPublicKeySize + chacha20poly1305.Overhead
	classicMessage2Size     = dhLen + chacha20poly1305.Overhead
	postQuantumMessage2Size = classicMessage2Size + contracts.MlkemCiphertextSize + chacha20poly1305.Overhead + contracts.SlhdsaSignatureSize

	// EphemeralPublicKeySize is the size of an X25519 ephemeral public key, as
	// carried by the in-band rekey's dh_pub field. Equals dhLen.
	EphemeralPublicKeySize = dhLen

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

// RestoreCompositeKeypair reconstructs a CompositeKeypair from the PRIVATE
// halves of its key material: the X25519 scalar, the ML-KEM-1024 decapsulation
// seed, and the SLH-DSA private key.
//
// Only the private halves are taken because each public half is derivable from
// them: the X25519 public point from the scalar, the ML-KEM encapsulation key
// from the seed, and the SLH-DSA public key from the private key, which embeds
// it. A stored public half would be a second copy that can drift from the
// private half it belongs to — and once the Hub pins that copy, the drift is
// permanent.
//
// The SLH-DSA public key is RECOMPUTED, not trusted: circl's
// PrivateKey.PublicKey() returns the copy embedded in the private key without
// checking it, so a corrupt secret seed with an intact embedded public key
// would pin a key the Worker cannot sign for. FIPS 205's slh_keygen (Section
// 10.1, Algorithm 21) reads SK.seed, SK.prf and PK.seed in that order, so
// replaying the private key's first three n-byte fields through GenerateKey
// reproduces the keypair the private key was made from; the recomputed public
// key must match the embedded one. This costs one top-tree computation, about a
// twentieth of a signature. It cannot detect a corrupt SK.prf, which is harmless
// because every signature carries its own randomizer R.
func RestoreCompositeKeypair(x25519Priv, mlkemSeed, slhdsaPrivBytes []byte) (*CompositeKeypair, error) {
	x25519Pub, err := X25519PublicFromSeed(x25519Priv)
	if err != nil {
		return nil, fmt.Errorf("restore X25519 public key: %w", err)
	}

	mlkemDK, err := mlkem.NewDecapsulationKey1024(mlkemSeed)
	if err != nil {
		return nil, fmt.Errorf("parse ML-KEM decapsulation key: %w", err)
	}

	slhdsaPriv := slhdsa.PrivateKey{ID: slhdsa.SHAKE_256f}
	if err := slhdsaPriv.UnmarshalBinary(slhdsaPrivBytes); err != nil {
		return nil, fmt.Errorf("parse SLH-DSA private key: %w", err)
	}
	slhdsaPub := slhdsaPriv.PublicKey()

	// n is the byte length of each of the four n-byte fields the private key
	// holds (SK.seed, SK.prf, PK.seed, PK.root), so it is half a public key's
	// PK.seed||PK.root. circl exposes no accessor, and UnmarshalBinary has
	// already refused any length but 4n.
	n := contracts.SlhdsaPublicKeySize / 2
	recomputed, _, err := slhdsa.GenerateKey(bytes.NewReader(slhdsaPrivBytes[:3*n]), slhdsa.SHAKE_256f)
	if err != nil {
		return nil, fmt.Errorf("recompute SLH-DSA public key: %w", err)
	}
	if !recomputed.Equal(slhdsaPub) {
		return nil, errors.New("the SLH-DSA private key does not match its public key")
	}

	return &CompositeKeypair{
		X25519Public:          x25519Pub,
		X25519Private:         x25519Priv,
		MlkemDecapsulationKey: mlkemDK,
		SlhdsaPublicKey:       slhdsaPub,
		SlhdsaPrivateKey:      slhdsaPriv,
	}, nil
}

// ValidateStaticPublicKeys reports whether three static public keys can serve a
// handshake. It checks the size of each key, and the two keys that a size check
// cannot clear:
//
//   - The X25519 key must not be a low-order point, which every DH refuses.
//   - The ML-KEM key must parse, because a key whose coefficients are out of
//     range is one every channel handshake refuses.
//
// The size is the whole check of the SLH-DSA key. Its parser only splits the
// bytes into a seed and a root, so every value of the right size parses.
func ValidateStaticPublicKeys(x25519Pub, mlkemPub, slhdsaPub []byte) error {
	if len(x25519Pub) != contracts.X25519PublicKeySize {
		return fmt.Errorf("worker x25519 public key is %d bytes, want %d", len(x25519Pub), contracts.X25519PublicKeySize)
	}
	if err := refuseLowOrderX25519(x25519Pub); err != nil {
		return err
	}
	if len(mlkemPub) != contracts.MlkemPublicKeySize {
		return fmt.Errorf("worker mlkem public key is %d bytes, want %d", len(mlkemPub), contracts.MlkemPublicKeySize)
	}
	if _, err := mlkem.NewEncapsulationKey1024(mlkemPub); err != nil {
		return fmt.Errorf("worker mlkem public key is not a valid ML-KEM-1024 encapsulation key: %w", err)
	}
	if len(slhdsaPub) != contracts.SlhdsaPublicKeySize {
		return fmt.Errorf("worker slhdsa public key is %d bytes, want %d", len(slhdsaPub), contracts.SlhdsaPublicKeySize)
	}
	return nil
}

// lowOrderProbe returns the private key that refuseLowOrderX25519 multiplies a
// public key with. It is generated once and guards nothing: its DH output is
// discarded.
var lowOrderProbe = sync.OnceValues(func() (*ecdh.PrivateKey, error) {
	return ecdh.X25519().GenerateKey(rand.Reader)
})

// refuseLowOrderX25519 refuses an X25519 public key that is a low-order point.
// A DH with such a key outputs all zeros whatever the private key is, because
// X25519 clamps every private key to a multiple of the cofactor 8. Every Noise
// handshake with the key then fails at its DH, so a pinned low-order key leaves
// the Worker unreachable until it registers again. crypto/ecdh refuses the
// all-zero output, so one trial DH with any private key detects the point.
func refuseLowOrderX25519(pub []byte) error {
	probe, err := lowOrderProbe()
	if err != nil {
		return fmt.Errorf("generate the key that checks the worker x25519 public key: %w", err)
	}
	key, err := ecdh.X25519().NewPublicKey(pub)
	if err != nil {
		return fmt.Errorf("worker x25519 public key is not an X25519 key: %w", err)
	}
	secret, err := probe.ECDH(key)
	clear(secret)
	if err != nil {
		return fmt.Errorf("worker x25519 public key is a low-order point that no handshake can use: %w", err)
	}
	return nil
}

// ---- Symmetric State (Noise protocol core) ----

type symmetricState struct {
	h    [hashLen]byte // handshake hash
	ck   [hashLen]byte // chaining key
	hasK bool
	k    [keyLen]byte // cipher key
	n    uint32       // nonce counter
}

func newSymmetricState(protocolName string) *symmetricState {
	ss := &symmetricState{}
	nameBytes := []byte(protocolName)
	if len(nameBytes) <= hashLen {
		copy(ss.h[:], nameBytes)
	} else {
		ss.h = blake2b.Sum512(nameBytes)
	}
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
	zeroBytes(ck[:])
	zeroBytes(tempK[:])
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
	zeroBytes(tempK1[:])
	zeroBytes(tempK2[:])
	return &CipherState{k: k1}, &CipherState{k: k2}
}

// ---- CipherState (post-handshake) ----

// ErrCipherDestroyed reports use of a cipher after its session ended.
var ErrCipherDestroyed = errors.New("noise: cipher state destroyed")

// CipherState manages one transport direction. Its lock serializes encryption,
// decryption, rotation and destruction, including the previous key's timer.
// The caller still puts frames on the wire in nonce order.
type CipherState struct {
	mu            sync.Mutex
	k             [keyLen]byte
	n             atomic.Uint64
	destroyed     bool
	prev          *CipherState
	prevExpiresAt time.Time
	prevTimer     *time.Timer
}

// Encrypt encrypts plaintext using the cipher key and associated data.
func (cs *CipherState) Encrypt(ad, plaintext []byte) ([]byte, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.destroyed {
		return nil, ErrCipherDestroyed
	}
	if len(plaintext) > contracts.MaxPlaintextPerChunk {
		return nil, fmt.Errorf("noise: plaintext too large (%d > %d)", len(plaintext), contracts.MaxPlaintextPerChunk)
	}
	n := cs.n.Load()
	if n > contracts.HardNonceLimit {
		return nil, fmt.Errorf("noise: send nonce exceeded hard limit")
	}
	ct, err := aeadEncrypt(cs.k[:], uint32(n), ad, plaintext)
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
func (cs *CipherState) Decrypt(ad, ciphertext []byte) ([]byte, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.destroyed {
		return nil, ErrCipherDestroyed
	}
	n := cs.n.Load()
	if n > contracts.HardNonceLimit {
		return nil, fmt.Errorf("noise: receive nonce exceeded hard limit")
	}
	pt, err := aeadDecrypt(cs.k[:], uint32(n), ad, ciphertext)
	if err == nil {
		cs.n.Add(1)
		return pt, nil
	}
	if cs.prev != nil {
		if !time.Now().Before(cs.prevExpiresAt) {
			cs.clearPreviousLocked()
		} else if previousPlaintext, previousErr := cs.prev.tryDecrypt(ad, ciphertext); previousErr == nil {
			return previousPlaintext, nil
		}
	}
	return nil, err
}

// tryDecrypt decrypts without the prev fallback. Used by Decrypt for prev and
// by tests that want to assert the current key alone cannot decrypt a frame.
func (cs *CipherState) tryDecrypt(ad, ciphertext []byte) ([]byte, error) {
	n := cs.n.Load()
	if n > contracts.HardNonceLimit {
		return nil, fmt.Errorf("noise: receive nonce exceeded hard limit")
	}
	pt, err := aeadDecrypt(cs.k[:], uint32(n), ad, ciphertext)
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

// SetNonceForTest sets the cipher nonce. For tests that need contracts.SoftNonceLimit /
// contracts.HardNonceLimit without encrypting billions of times. Do not use in production.
func (cs *CipherState) SetNonceForTest(n uint64) {
	cs.n.Store(n)
}

// NeedsRekey returns true if the nonce has exceeded the soft limit.
func (cs *CipherState) NeedsRekey() bool {
	return cs.n.Load() > contracts.SoftNonceLimit
}

// rekeyWithSecret derives a new cipher key that mixes fresh key-agreement
// entropy into the current key, then swaps it in as the active key.
//
// When retainPrev is true (the receive direction) the previous key is moved
// into cs.prev (zeroing any older retained key first) and marked live for
// RekeyGraceWindow, so in-flight frames encrypted under it can still be
// decrypted. When retainPrev is false (the send direction, which keeps no
// grace window) the previous key is zeroed and dropped instead — structurally,
// not via a follow-up clear step the caller must remember. The nonce resets to 0.
//
// The derivation applies the handshake's two HKDF stages to the transport key:
//
//	ck1 = HKDF(k, dhSecret)      // fresh X25519 ECDH(e_i, e_r)
//	ck2 = HKDF(ck1, pqSecret)    // static || ephemeral ML-KEM secret (nil in classic mode)
//	k'  = ck2[0:32]
//
// Keyed off this CipherState's own k, so the two transport directions stay
// distinct (Send and Receive start from different keys and produce different
// k'). dhSecret must be the raw X25519 shared secret. On a post-quantum channel,
// pqSecret is the 64-byte concatenation of the static and ephemeral ML-KEM
// secrets; it is nil on a classic channel. Both inputs are zeroed here.
//
// Forward secrecy: because k' mixes fresh DH + ML-KEM entropy, compromise of
// the current epoch key does NOT let an attacker derive the next. The Hub still
// never learns keys (E2EE relay); the rekey frames carrying the ephemeral
// pubkeys / ML-KEM ciphertext ride inside the existing AEAD-encrypted channel,
// so they are authenticated by the current cipher and need no SLH-DSA
// signature. See issue #321.
func (cs *CipherState) rekeyWithSecret(dhSecret, pqSecret []byte, retainPrev bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	defer zeroBytes(dhSecret)
	defer zeroBytes(pqSecret)
	if cs.destroyed {
		return
	}

	// Both modes run the same two-stage ratchet. The second stage takes an
	// empty input on classic channels.
	chainingKey, unused := noiseHKDF(cs.k[:], dhSecret)
	zeroBytes(unused[:])
	newKey, unused := noiseHKDF(chainingKey[:], pqSecret)
	zeroBytes(chainingKey[:])
	zeroBytes(unused[:])

	cs.clearPreviousLocked()
	if retainPrev {
		previous := &CipherState{k: cs.k}
		previous.n.Store(cs.n.Load())
		cs.prev = previous
		cs.prevExpiresAt = time.Now().Add(RekeyGraceWindow)
		cs.prevTimer = time.AfterFunc(RekeyGraceWindow, func() {
			cs.mu.Lock()
			defer cs.mu.Unlock()
			if cs.prev == previous {
				cs.clearPreviousLocked()
			}
		})
	}

	copy(cs.k[:], newKey[:keyLen])
	cs.n.Store(0)
	zeroBytes(newKey[:])
}

// clearPreviousLocked wipes the previous key and stops its timer. The caller
// holds the cipher lock.
func (cs *CipherState) clearPreviousLocked() {
	if cs.prevTimer != nil {
		cs.prevTimer.Stop()
		cs.prevTimer = nil
	}
	if cs.prev != nil {
		zeroBytes(cs.prev.k[:])
		cs.prev = nil
	}
	cs.prevExpiresAt = time.Time{}
}

// ---- Session ----

// Session wraps send/receive CipherState objects for bidirectional
// encrypted communication after a completed handshake.
type Session struct {
	Send    *CipherState
	Receive *CipherState
}

// Encrypt encrypts plaintext using the send cipher and associated data.
func (s *Session) Encrypt(ad, plaintext []byte) ([]byte, error) {
	return s.Send.Encrypt(ad, plaintext)
}

// Decrypt decrypts ciphertext using the receive cipher and associated data.
func (s *Session) Decrypt(ad, ciphertext []byte) ([]byte, error) {
	return s.Receive.Decrypt(ad, ciphertext)
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

// RekeyReceiveWithSecret rotates the receive CipherState with fresh DH +
// (optional) ML-KEM entropy, retaining the previous key for RekeyGraceWindow so
// in-flight frames encrypted under it can still be decrypted. Call after
// decrypting a peer rekey frame that commits the switch, under the same lock
// that serializes Decrypt (the recvLoop is single-threaded on Go).
func (s *Session) RekeyReceiveWithSecret(dhSecret, pqSecret []byte) {
	s.Receive.rekeyWithSecret(dhSecret, pqSecret, true)
}

// zero destroys the cipher and wipes both epochs.
func (cs *CipherState) zero() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	zeroBytes(cs.k[:])
	cs.destroyed = true
	cs.clearPreviousLocked()
}

// Zero destroys both ciphers and erases their active and previous keys.
func (s *Session) Zero() {
	s.Send.zero()
	s.Receive.zero()
}

// ---- Handshake State (for two-step initiator) ----

// HandshakeState holds intermediate state between handshake messages.
type HandshakeState struct {
	ss          *symmetricState
	x25519Seed  []byte
	mlkemSeed   []byte
	slhdsaPub   []byte
	postQuantum bool
}

// GenerateEphemeralX25519Seed generates a fresh X25519 ephemeral private key as
// the raw 32-byte seed the CALLER owns and can actually zero. Handshakes and
// in-band rekeys hold this seed because crypto/ecdh's *PrivateKey keeps an
// internal copy of the scalar that the API gives no way to
// wipe — Zero(priv.Bytes()) only zeroes a throwaway copy, leaving the real
// scalar on the heap until GC. Holding the seed directly makes "wipe the
// ephemeral once both directions have rotated" genuinely true instead of
// aspirational, which matters: the fresh-DH entropy is the forward-secrecy
// guarantee of #321. Pair with X25519PublicFromSeed to derive the wire DhPub.
// The caller must zero the returned seed when done.
func GenerateEphemeralX25519Seed() ([]byte, error) {
	_, seed, err := generateEphemeralX25519Key()
	return seed, err
}

func generateEphemeralX25519Key() (*ecdh.PrivateKey, []byte, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return priv, priv.Bytes(), nil
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
	priv, err := ecdh.X25519().NewPrivateKey(localPriv)
	if err != nil {
		return nil, fmt.Errorf("noise: invalid X25519 private key: %w", err)
	}
	return dhWithPrivateKey(priv, remotePub)
}

func dhWithPrivateKey(priv *ecdh.PrivateKey, remotePub []byte) ([]byte, error) {
	pub, err := ecdh.X25519().NewPublicKey(remotePub)
	if err != nil {
		return nil, fmt.Errorf("noise: invalid X25519 public key: %w", err)
	}
	secret, err := priv.ECDH(pub)
	if err != nil {
		return nil, fmt.Errorf("noise: X25519 DH failed: %w", err)
	}
	return secret, nil
}

// RekeyMaterial is the fresh key-agreement output an in-band rekey carries
// between the two peers. It is the bridge between the rekey wire fields and
// CipherState.rekeyWithSecret.
type RekeyMaterial struct {
	// LocalEphemeralPriv is this side's fresh X25519 ephemeral private key.
	// Held only until both directions have rotated, then zeroed.
	LocalEphemeralPriv []byte
	// PeerEphemeralPub is the peer's fresh X25519 ephemeral public key.
	PeerEphemeralPub []byte
	// PQSharedSecret concatenates the static and ephemeral ML-KEM secrets on a
	// post-quantum channel. It is nil on a classic channel.
	PQSharedSecret []byte
}

// DeriveRekeySecrets computes the (dhSecret, pqSecret) pair both peers feed into
// CipherState.rekeyWithSecret. dhSecret is the X25519 ECDH of the two fresh
// ephemerals. pqSecret is the 64-byte static || ephemeral ML-KEM secret on a
// post-quantum channel, or nil on a classic channel. Both inputs come from the
// same RekeyMaterial on each side, so the two directions mix identical entropy —
// direction-distinctness comes from each CipherState's own starting key. The
// caller must zero the returned secrets once both directions have rotated.
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

// RekeyPQOffer is the initiator's post-quantum half of one in-band rekey.
type RekeyPQOffer struct {
	StaticCiphertext   []byte
	EphemeralPublicKey []byte
	staticSharedSecret []byte
	ephemeralSeed      []byte
}

// OfferRekeyPQ creates the static encapsulation and ephemeral key of a
// post-quantum rekey. An empty Worker key returns an empty offer for a classic
// channel.
func OfferRekeyPQ(workerMlkemPub []byte) (*RekeyPQOffer, error) {
	return offerRekeyPQ(systemEntropy, workerMlkemPub)
}

func offerRekeyPQ(rnd entropy, workerMlkemPub []byte) (*RekeyPQOffer, error) {
	if len(workerMlkemPub) == 0 {
		return &RekeyPQOffer{}, nil
	}
	workerKey, err := mlkem.NewEncapsulationKey1024(workerMlkemPub)
	if err != nil {
		return nil, fmt.Errorf("noise: parse worker ML-KEM encapsulation key: %w", err)
	}
	staticSharedSecret, staticCiphertext := rnd.encapsulate(workerKey)
	ephemeralKey, ephemeralSeed, err := rnd.mlkemKey()
	if err != nil {
		zeroBytes(staticSharedSecret)
		zeroBytes(staticCiphertext)
		zeroBytes(ephemeralSeed)
		return nil, fmt.Errorf("noise: generate rekey ML-KEM key: %w", err)
	}
	return &RekeyPQOffer{
		StaticCiphertext:   staticCiphertext,
		EphemeralPublicKey: ephemeralKey.EncapsulationKey().Bytes(),
		staticSharedSecret: staticSharedSecret,
		ephemeralSeed:      ephemeralSeed,
	}, nil
}

// Accept completes an offer with the Worker's ephemeral encapsulation. It
// zeroes the offer on every exit. The caller zeroes the returned secret.
func (o *RekeyPQOffer) Accept(ephemeralCiphertext []byte) (pqSecret []byte, err error) {
	defer o.Zero()
	if o == nil || (len(o.staticSharedSecret) == 0 && len(o.ephemeralSeed) == 0) {
		if len(ephemeralCiphertext) != 0 {
			return nil, fmt.Errorf("noise: classic rekey Ack carries an ML-KEM ciphertext")
		}
		return nil, nil
	}
	if len(o.staticSharedSecret) != keyLen {
		return nil, fmt.Errorf("noise: rekey offer has no valid static ML-KEM secret")
	}
	if len(ephemeralCiphertext) != contracts.MlkemCiphertextSize {
		return nil, fmt.Errorf("noise: rekey ephemeral ML-KEM ciphertext is %d bytes, want %d", len(ephemeralCiphertext), contracts.MlkemCiphertextSize)
	}
	ephemeralKey, err := mlkem.NewDecapsulationKey1024(o.ephemeralSeed)
	if err != nil {
		return nil, fmt.Errorf("noise: restore rekey ML-KEM key: %w", err)
	}
	ephemeralSharedSecret, err := ephemeralKey.Decapsulate(ephemeralCiphertext)
	if err != nil {
		return nil, fmt.Errorf("noise: decapsulate rekey ephemeral ML-KEM secret: %w", err)
	}
	defer zeroBytes(ephemeralSharedSecret)
	pqSecret = make([]byte, 0, 2*keyLen)
	pqSecret = append(pqSecret, o.staticSharedSecret...)
	pqSecret = append(pqSecret, ephemeralSharedSecret...)
	return pqSecret, nil
}

// Zero wipes an offer's secret and wire buffers. It is safe on a nil offer and
// safe to call more than once.
func (o *RekeyPQOffer) Zero() {
	if o == nil {
		return
	}
	zeroBytes(o.StaticCiphertext)
	zeroBytes(o.EphemeralPublicKey)
	zeroBytes(o.staticSharedSecret)
	zeroBytes(o.ephemeralSeed)
}

// AnswerRekeyPQ decapsulates the static secret and encapsulates to the
// initiator's ephemeral key. A nil decapsulation key means a classic channel.
func AnswerRekeyPQ(dk *mlkem.DecapsulationKey1024, staticCiphertext, ephemeralPublicKey []byte) (pqSecret, ephemeralCiphertext []byte, err error) {
	return answerRekeyPQ(systemEntropy, dk, staticCiphertext, ephemeralPublicKey)
}

func answerRekeyPQ(rnd entropy, dk *mlkem.DecapsulationKey1024, staticCiphertext, ephemeralPublicKey []byte) ([]byte, []byte, error) {
	request, err := ParseRekeyPQRequest(dk, staticCiphertext, ephemeralPublicKey)
	if err != nil {
		return nil, nil, err
	}
	return request.answer(rnd)
}

// RekeyPQRequest holds the validated ML-KEM portion of a rekey request.
type RekeyPQRequest struct {
	workerKey        *mlkem.DecapsulationKey1024
	staticCiphertext []byte
	ephemeralKey     *mlkem.EncapsulationKey1024
}

// ParseRekeyPQRequest validates the request's shape before the caller applies
// rekey rate policy. A nil Worker key requires empty ML-KEM fields.
func ParseRekeyPQRequest(dk *mlkem.DecapsulationKey1024, staticCiphertext, ephemeralPublicKey []byte) (RekeyPQRequest, error) {
	if dk == nil {
		if len(staticCiphertext) != 0 || len(ephemeralPublicKey) != 0 {
			return RekeyPQRequest{}, errors.New("noise: classic rekey request carries ML-KEM material")
		}
		return RekeyPQRequest{}, nil
	}
	if len(staticCiphertext) != contracts.MlkemCiphertextSize {
		return RekeyPQRequest{}, fmt.Errorf("noise: rekey static ML-KEM ciphertext is %d bytes, want %d", len(staticCiphertext), contracts.MlkemCiphertextSize)
	}
	if len(ephemeralPublicKey) != contracts.MlkemPublicKeySize {
		return RekeyPQRequest{}, fmt.Errorf("noise: rekey ephemeral ML-KEM key is %d bytes, want %d", len(ephemeralPublicKey), contracts.MlkemPublicKeySize)
	}
	ephemeralKey, err := mlkem.NewEncapsulationKey1024(ephemeralPublicKey)
	if err != nil {
		return RekeyPQRequest{}, fmt.Errorf("noise: parse rekey ephemeral ML-KEM key: %w", err)
	}
	return RekeyPQRequest{workerKey: dk, staticCiphertext: staticCiphertext, ephemeralKey: ephemeralKey}, nil
}

// Answer completes the validated exchange once rekey policy allows it.
func (r RekeyPQRequest) Answer() (pqSecret, ephemeralCiphertext []byte, err error) {
	return r.answer(systemEntropy)
}

func (r RekeyPQRequest) answer(rnd entropy) (pqSecret, ephemeralCiphertext []byte, err error) {
	if r.workerKey == nil {
		return nil, nil, nil
	}
	staticSharedSecret, err := r.workerKey.Decapsulate(r.staticCiphertext)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: decapsulate rekey static ML-KEM secret: %w", err)
	}
	defer zeroBytes(staticSharedSecret)
	ephemeralSharedSecret, ephemeralCiphertext := rnd.encapsulate(r.ephemeralKey)
	defer zeroBytes(ephemeralSharedSecret)
	pqSecret = make([]byte, 0, 2*keyLen)
	pqSecret = append(pqSecret, staticSharedSecret...)
	pqSecret = append(pqSecret, ephemeralSharedSecret...)
	return pqSecret, ephemeralCiphertext, nil
}

// ---- Channel Handshake ----

type entropy struct {
	x25519Key   func() (*ecdh.PrivateKey, []byte, error)
	mlkemKey    func() (*mlkem.DecapsulationKey1024, []byte, error)
	encapsulate func(ek *mlkem.EncapsulationKey1024) (sharedKey, ciphertext []byte)
	signRandom  io.Reader
}

var systemEntropy = entropy{
	x25519Key: generateEphemeralX25519Key,
	mlkemKey: func() (*mlkem.DecapsulationKey1024, []byte, error) {
		key, err := mlkem.GenerateKey1024()
		if err != nil {
			return nil, nil, err
		}
		return key, key.Bytes(), nil
	},
	encapsulate: func(ek *mlkem.EncapsulationKey1024) (sharedKey, ciphertext []byte) {
		return ek.Encapsulate()
	},
	signRandom: rand.Reader,
}

func handshakePrologue(workerID string) ([]byte, error) {
	if workerID == "" {
		return nil, fmt.Errorf("noise: Worker id is required for the channel handshake")
	}
	return []byte(contracts.NoiseHandshakeContextLabel + "\x00" + workerID), nil
}

func mixStaticKeys(ss *symmetricState, x25519Pub, mlkemPub, slhdsaPub []byte) {
	ss.mixHash(x25519Pub)
	ss.mixHash(mlkemPub)
	ss.mixHash(slhdsaPub)
}

func handshakeSignatureInput(handshakeHash []byte) []byte {
	return append([]byte(contracts.NoiseHandshakeSignatureLabel+"\x00"), handshakeHash...)
}

// ResponderHandshake performs the post-quantum responder handshake.
func ResponderHandshake(compositeKey *CompositeKeypair, workerID string, message1 []byte) (response []byte, session *Session, err error) {
	return responderHandshake(systemEntropy, compositeKey, workerID, message1)
}

func responderHandshake(rnd entropy, compositeKey *CompositeKeypair, workerID string, message1 []byte) (response []byte, session *Session, err error) {
	return runResponderHandshake(rnd, true, compositeKey, workerID, message1)
}

// ClassicalResponderHandshake performs the classic responder handshake, which
// authenticates the Worker's static ML-KEM key without an SLH-DSA signature or
// an ephemeral ML-KEM exchange.
func ClassicalResponderHandshake(compositeKey *CompositeKeypair, workerID string, message1 []byte) (response []byte, session *Session, err error) {
	return classicalResponderHandshake(systemEntropy, compositeKey, workerID, message1)
}

func classicalResponderHandshake(rnd entropy, compositeKey *CompositeKeypair, workerID string, message1 []byte) (response []byte, session *Session, err error) {
	return runResponderHandshake(rnd, false, compositeKey, workerID, message1)
}

func runResponderHandshake(rnd entropy, postQuantum bool, compositeKey *CompositeKeypair, workerID string, message1 []byte) (response []byte, session *Session, err error) {
	expectedLen := classicMessage1Size
	responseLen := classicMessage2Size
	protocolName := contracts.NoiseClassicProtocolName
	if postQuantum {
		expectedLen = postQuantumMessage1Size
		responseLen = postQuantumMessage2Size
		protocolName = contracts.NoisePostQuantumProtocolName
	}
	if len(message1) != expectedLen {
		return nil, nil, fmt.Errorf("noise: message1 wrong size: got %d, want %d", len(message1), expectedLen)
	}
	prologue, err := handshakePrologue(workerID)
	if err != nil {
		return nil, nil, err
	}
	slhdsaPub, err := compositeKey.SlhdsaPublicKeyBytes()
	if err != nil {
		return nil, nil, fmt.Errorf("noise: marshal Worker SLH-DSA public key: %w", err)
	}
	ss := newSymmetricState(protocolName)
	defer ss.clear()
	ss.mixHash(prologue)
	mixStaticKeys(ss, compositeKey.X25519Public, compositeKey.MlkemPublicKeyBytes(), slhdsaPub)

	offset := 0
	initiatorEphemeral := message1[offset : offset+dhLen]
	offset += dhLen
	ss.mixHash(initiatorEphemeral)
	dhES, err := DH(compositeKey.X25519Private, initiatorEphemeral)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: es DH(responder static, initiator ephemeral): %w", err)
	}
	ss.mixKey(dhES)
	zeroBytes(dhES)

	var ephemeralKeyBytes []byte
	if postQuantum {
		encryptedKeyLen := contracts.MlkemPublicKeySize + chacha20poly1305.Overhead
		ephemeralKeyBytes, err = ss.decryptAndHash(message1[offset : offset+encryptedKeyLen])
		if err != nil {
			return nil, nil, fmt.Errorf("noise: decrypt message1 ephemeral ML-KEM key: %w", err)
		}
		offset += encryptedKeyLen
	}

	encryptedStaticLen := contracts.MlkemCiphertextSize + chacha20poly1305.Overhead
	staticCiphertext, err := ss.decryptAndHash(message1[offset : offset+encryptedStaticLen])
	if err != nil {
		return nil, nil, fmt.Errorf("noise: decrypt message1 static ML-KEM ciphertext: %w", err)
	}
	offset += encryptedStaticLen
	staticSharedSecret, err := compositeKey.MlkemDecapsulationKey.Decapsulate(staticCiphertext)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: decapsulate message1 static ML-KEM secret: %w", err)
	}
	ss.mixKey(staticSharedSecret)
	zeroBytes(staticSharedSecret)
	if _, err := ss.decryptAndHash(message1[offset:]); err != nil {
		return nil, nil, fmt.Errorf("noise: decrypt message1 payload: %w", err)
	}
	var ephemeralMlkemKey *mlkem.EncapsulationKey1024
	if postQuantum {
		ephemeralMlkemKey, err = mlkem.NewEncapsulationKey1024(ephemeralKeyBytes)
		if err != nil {
			return nil, nil, fmt.Errorf("noise: parse message1 ephemeral ML-KEM key: %w", err)
		}
	}

	responderKey, responderSeed, err := rnd.x25519Key()
	zeroBytes(responderSeed)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: generate responder ephemeral: %w", err)
	}
	responderEphemeral := responderKey.PublicKey().Bytes()
	ss.mixHash(responderEphemeral)
	dhEE, err := dhWithPrivateKey(responderKey, initiatorEphemeral)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: ee DH(responder ephemeral, initiator ephemeral): %w", err)
	}
	ss.mixKey(dhEE)
	zeroBytes(dhEE)

	message2 := append(make([]byte, 0, responseLen), responderEphemeral...)
	if postQuantum {
		ephemeralSharedSecret, ephemeralCiphertext := rnd.encapsulate(ephemeralMlkemKey)
		encryptedCiphertext, err := ss.encryptAndHash(ephemeralCiphertext)
		if err != nil {
			zeroBytes(ephemeralSharedSecret)
			return nil, nil, fmt.Errorf("noise: encrypt message2 ephemeral ML-KEM ciphertext: %w", err)
		}
		ss.mixKey(ephemeralSharedSecret)
		zeroBytes(ephemeralSharedSecret)
		message2 = append(message2, encryptedCiphertext...)
	}
	encryptedPayload, err := ss.encryptAndHash(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: encrypt message2 payload: %w", err)
	}
	message2 = append(message2, encryptedPayload...)
	if postQuantum {
		signature, err := compositeKey.SlhdsaPrivateKey.Sign(rnd.signRandom, handshakeSignatureInput(ss.h[:]), nil)
		if err != nil {
			return nil, nil, fmt.Errorf("noise: SLH-DSA sign: %w", err)
		}
		message2 = append(message2, signature...)
	}
	send, receive := ss.split()
	return message2, &Session{Send: send, Receive: receive}, nil
}

// InitiatorHandshake1 creates the first post-quantum handshake message.
func InitiatorHandshake1(workerID string, remoteX25519Pub, remoteMlkemPub, remoteSlhdsaPub []byte) (*HandshakeState, []byte, error) {
	return initiatorHandshake1(systemEntropy, workerID, remoteX25519Pub, remoteMlkemPub, remoteSlhdsaPub)
}

func initiatorHandshake1(rnd entropy, workerID string, remoteX25519Pub, remoteMlkemPub, remoteSlhdsaPub []byte) (*HandshakeState, []byte, error) {
	return runInitiatorHandshake1(rnd, true, workerID, remoteX25519Pub, remoteMlkemPub, remoteSlhdsaPub)
}

// ClassicalInitiatorHandshake1 creates the first classic handshake message.
func ClassicalInitiatorHandshake1(workerID string, remoteX25519Pub, remoteMlkemPub, remoteSlhdsaPub []byte) (*HandshakeState, []byte, error) {
	return classicalInitiatorHandshake1(systemEntropy, workerID, remoteX25519Pub, remoteMlkemPub, remoteSlhdsaPub)
}

func classicalInitiatorHandshake1(rnd entropy, workerID string, remoteX25519Pub, remoteMlkemPub, remoteSlhdsaPub []byte) (*HandshakeState, []byte, error) {
	return runInitiatorHandshake1(rnd, false, workerID, remoteX25519Pub, remoteMlkemPub, remoteSlhdsaPub)
}

func runInitiatorHandshake1(rnd entropy, postQuantum bool, workerID string, remoteX25519Pub, remoteMlkemPub, remoteSlhdsaPub []byte) (state *HandshakeState, message1 []byte, err error) {
	prologue, err := handshakePrologue(workerID)
	if err != nil {
		return nil, nil, err
	}
	messageLen := classicMessage1Size
	protocolName := contracts.NoiseClassicProtocolName
	if postQuantum {
		messageLen = postQuantumMessage1Size
		protocolName = contracts.NoisePostQuantumProtocolName
	}
	state = &HandshakeState{
		ss:          newSymmetricState(protocolName),
		postQuantum: postQuantum,
	}
	stateToClear := state
	if postQuantum {
		state.slhdsaPub = append([]byte(nil), remoteSlhdsaPub...)
	}
	defer func() {
		if err != nil {
			stateToClear.Clear()
			state = nil
			message1 = nil
		}
	}()
	state.ss.mixHash(prologue)
	mixStaticKeys(state.ss, remoteX25519Pub, remoteMlkemPub, remoteSlhdsaPub)

	var initiatorKey *ecdh.PrivateKey
	initiatorKey, state.x25519Seed, err = rnd.x25519Key()
	if err != nil {
		return nil, nil, fmt.Errorf("noise: generate initiator ephemeral: %w", err)
	}
	initiatorEphemeral := initiatorKey.PublicKey().Bytes()
	state.ss.mixHash(initiatorEphemeral)
	dhES, err := dhWithPrivateKey(initiatorKey, remoteX25519Pub)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: es DH(initiator ephemeral, responder static): %w", err)
	}
	state.ss.mixKey(dhES)
	zeroBytes(dhES)

	message1 = append(make([]byte, 0, messageLen), initiatorEphemeral...)
	if postQuantum {
		var ephemeralKey *mlkem.DecapsulationKey1024
		ephemeralKey, state.mlkemSeed, err = rnd.mlkemKey()
		if err != nil {
			return nil, nil, fmt.Errorf("noise: generate initiator ephemeral ML-KEM key: %w", err)
		}
		encryptedKey, err := state.ss.encryptAndHash(ephemeralKey.EncapsulationKey().Bytes())
		if err != nil {
			return nil, nil, fmt.Errorf("noise: encrypt message1 ephemeral ML-KEM key: %w", err)
		}
		message1 = append(message1, encryptedKey...)
	}

	workerMlkemKey, err := mlkem.NewEncapsulationKey1024(remoteMlkemPub)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: parse Worker ML-KEM encapsulation key: %w", err)
	}
	staticSharedSecret, staticCiphertext := rnd.encapsulate(workerMlkemKey)
	encryptedCiphertext, err := state.ss.encryptAndHash(staticCiphertext)
	if err != nil {
		zeroBytes(staticSharedSecret)
		return nil, nil, fmt.Errorf("noise: encrypt message1 static ML-KEM ciphertext: %w", err)
	}
	state.ss.mixKey(staticSharedSecret)
	zeroBytes(staticSharedSecret)
	message1 = append(message1, encryptedCiphertext...)
	encryptedPayload, err := state.ss.encryptAndHash(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("noise: encrypt message1 payload: %w", err)
	}
	message1 = append(message1, encryptedPayload...)
	return state, message1, nil
}

// Finish processes message 2 in the mode that created the state and returns the
// initiator's session. It clears the state on every exit.
func (hs *HandshakeState) Finish(message2 []byte) (*Session, error) {
	if hs == nil {
		return nil, fmt.Errorf("noise: handshake state is nil")
	}
	defer hs.Clear()
	if hs.ss == nil {
		return nil, fmt.Errorf("noise: handshake state is already cleared")
	}
	expectedLen := classicMessage2Size
	if hs.postQuantum {
		expectedLen = postQuantumMessage2Size
	}
	if len(message2) != expectedLen {
		return nil, fmt.Errorf("noise: message2 wrong size: got %d, want %d", len(message2), expectedLen)
	}

	offset := 0
	responderEphemeral := message2[offset : offset+dhLen]
	offset += dhLen
	hs.ss.mixHash(responderEphemeral)
	dhEE, err := DH(hs.x25519Seed, responderEphemeral)
	if err != nil {
		return nil, fmt.Errorf("noise: ee DH(initiator ephemeral, responder ephemeral): %w", err)
	}
	hs.ss.mixKey(dhEE)
	zeroBytes(dhEE)
	if hs.postQuantum {
		encryptedCiphertextLen := contracts.MlkemCiphertextSize + chacha20poly1305.Overhead
		ephemeralCiphertext, err := hs.ss.decryptAndHash(message2[offset : offset+encryptedCiphertextLen])
		if err != nil {
			return nil, fmt.Errorf("noise: decrypt message2 ephemeral ML-KEM ciphertext: %w", err)
		}
		offset += encryptedCiphertextLen
		ephemeralKey, err := mlkem.NewDecapsulationKey1024(hs.mlkemSeed)
		if err != nil {
			return nil, fmt.Errorf("noise: restore initiator ephemeral ML-KEM key: %w", err)
		}
		ephemeralSharedSecret, err := ephemeralKey.Decapsulate(ephemeralCiphertext)
		if err != nil {
			return nil, fmt.Errorf("noise: decapsulate message2 ephemeral ML-KEM secret: %w", err)
		}
		hs.ss.mixKey(ephemeralSharedSecret)
		zeroBytes(ephemeralSharedSecret)
	}
	encryptedPayloadLen := chacha20poly1305.Overhead
	if _, err := hs.ss.decryptAndHash(message2[offset : offset+encryptedPayloadLen]); err != nil {
		return nil, fmt.Errorf("noise: decrypt message2 payload: %w", err)
	}
	offset += encryptedPayloadLen
	if hs.postQuantum {
		slhdsaPub := slhdsa.PublicKey{ID: slhdsa.SHAKE_256f}
		if err := slhdsaPub.UnmarshalBinary(hs.slhdsaPub); err != nil {
			return nil, fmt.Errorf("noise: parse SLH-DSA public key: %w", err)
		}
		signature := message2[offset:]
		if !slhdsa.Verify(&slhdsaPub, slhdsa.NewMessage(handshakeSignatureInput(hs.ss.h[:])), signature, nil) {
			return nil, fmt.Errorf("noise: SLH-DSA signature verification failed")
		}
	}
	cs1, cs2 := hs.ss.split()
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

// clear zeroes the symmetric state's sensitive fields after the handshake ends.
func (ss *symmetricState) clear() {
	zeroBytes(ss.ck[:])
	zeroBytes(ss.k[:])
	zeroBytes(ss.h[:])
	ss.hasK = false
	ss.n = 0
}

// Clear zeroes all sensitive fields in the HandshakeState. It is idempotent.
func (hs *HandshakeState) Clear() {
	if hs == nil {
		return
	}
	if hs.ss != nil {
		hs.ss.clear()
		hs.ss = nil
	}
	zeroBytes(hs.x25519Seed)
	zeroBytes(hs.mlkemSeed)
}

// ---- Crypto primitives ----

func blake2bHash() hash.Hash {
	h, _ := blake2b.New512(nil)
	return h
}

// noiseHKDF implements the Noise HKDF using HMAC-BLAKE2b.
func noiseHKDF(chainingKey, inputKeyMaterial []byte) (out1 [hashLen]byte, out2 [hashLen]byte) {
	mac := hmac.New(blake2bHash, chainingKey)
	mac.Write(inputKeyMaterial)
	var tempKey [hashLen]byte
	mac.Sum(tempKey[:0])

	mac = hmac.New(blake2bHash, tempKey[:])
	zeroBytes(tempKey[:])
	mac.Write([]byte{1})
	mac.Sum(out1[:0])

	mac.Reset()
	mac.Write(out1[:])
	mac.Write([]byte{2})
	mac.Sum(out2[:0])
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
