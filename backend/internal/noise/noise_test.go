package noise

import (
	"bytes"
	"crypto/mlkem"
	"crypto/mlkem/mlkemtest"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudflare/circl/sign/slhdsa"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"

	"github.com/leapmux/leapmux/generated/contracts"
)

const testWorkerID = "worker-test"

var updateVectors = flag.Bool("update-vectors", false, "write the Noise conformance vectors")

func slhdsaPublicKey(t *testing.T, key *CompositeKeypair) []byte {
	t.Helper()
	publicKey, err := key.SlhdsaPublicKeyBytes()
	require.NoError(t, err)
	return publicKey
}

// The contract states these sizes for both languages, and the handshake runs on
// these libraries. A size that differed from theirs would refuse every valid
// worker or admit a value that the handshake cannot use.
func TestSizeContractsMatchTheCryptographyLibraries(t *testing.T) {
	assert.Equal(t, curve25519.PointSize, contracts.X25519PublicKeySize)
	assert.Equal(t, mlkem.EncapsulationKeySize1024, contracts.MlkemPublicKeySize)
	assert.Equal(t, mlkem.CiphertextSize1024, contracts.MlkemCiphertextSize)
	assert.Equal(t, slhdsa.SHAKE_256f.Scheme().PublicKeySize(), contracts.SlhdsaPublicKeySize)
	assert.Equal(t, slhdsa.SHAKE_256f.Scheme().SignatureSize(), contracts.SlhdsaSignatureSize)
	assert.Equal(t, chacha20poly1305.Overhead, contracts.NoiseAEADAuthTagSize)
}

// The sizes of a real keypair, as the second opinion on each library constant.
func TestGeneratedKeysHaveTheContractSizes(t *testing.T) {
	key, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	slhdsaPub, err := key.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	assert.Len(t, key.X25519Public, contracts.X25519PublicKeySize)
	assert.Len(t, key.MlkemPublicKeyBytes(), contracts.MlkemPublicKeySize)
	assert.Len(t, slhdsaPub, contracts.SlhdsaPublicKeySize)
}

func TestHandshakeRoundtrip(t *testing.T) {
	// Worker generates composite keypair.
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	// Initiator (Frontend) starts handshake.
	hs, msg1, err := InitiatorHandshake1(testWorkerID, workerKey.X25519Public, workerKey.MlkemPublicKeyBytes(), slhdsaPublicKey(t, workerKey))
	require.NoError(t, err)
	assert.NotEmpty(t, msg1)

	// Worker (Responder) processes msg1 and returns msg2.
	msg2, workerSession, err := ResponderHandshake(workerKey, testWorkerID, msg1)
	require.NoError(t, err)
	assert.NotEmpty(t, msg2)
	assert.NotNil(t, workerSession)

	// Initiator completes handshake.
	initiatorSession, err := hs.Finish(msg2)
	require.NoError(t, err)
	assert.NotNil(t, initiatorSession)

	// Test bidirectional encryption.
	t.Run("initiator_to_responder", func(t *testing.T) {
		plaintext := []byte("hello from initiator")
		ciphertext, err := initiatorSession.Encrypt(nil, plaintext)
		require.NoError(t, err)
		assert.NotEqual(t, plaintext, ciphertext)

		decrypted, err := workerSession.Decrypt(nil, ciphertext)
		require.NoError(t, err)
		assert.Equal(t, plaintext, decrypted)
	})

	t.Run("responder_to_initiator", func(t *testing.T) {
		plaintext := []byte("hello from responder")
		ciphertext, err := workerSession.Encrypt(nil, plaintext)
		require.NoError(t, err)
		assert.NotEqual(t, plaintext, ciphertext)

		decrypted, err := initiatorSession.Decrypt(nil, ciphertext)
		require.NoError(t, err)
		assert.Equal(t, plaintext, decrypted)
	})
}

func TestMultipleMessages(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	hs, msg1, err := InitiatorHandshake1(testWorkerID, workerKey.X25519Public, workerKey.MlkemPublicKeyBytes(), slhdsaPublicKey(t, workerKey))
	require.NoError(t, err)

	msg2, workerSession, err := ResponderHandshake(workerKey, testWorkerID, msg1)
	require.NoError(t, err)

	initiatorSession, err := hs.Finish(msg2)
	require.NoError(t, err)

	for i := 0; i < 100; i++ {
		msg := []byte(fmt.Sprintf("message %d from initiator", i))
		ct, err := initiatorSession.Encrypt(nil, msg)
		require.NoError(t, err)
		pt, err := workerSession.Decrypt(nil, ct)
		require.NoError(t, err)
		assert.Equal(t, msg, pt)

		msg = []byte(fmt.Sprintf("message %d from responder", i))
		ct, err = workerSession.Encrypt(nil, msg)
		require.NoError(t, err)
		pt, err = initiatorSession.Decrypt(nil, ct)
		require.NoError(t, err)
		assert.Equal(t, msg, pt)
	}
}

func TestWrongMlkemKey(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	wrongKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	hs, msg1, err := InitiatorHandshake1(testWorkerID, workerKey.X25519Public, wrongKey.MlkemPublicKeyBytes(), slhdsaPublicKey(t, workerKey))
	require.NoError(t, err)
	defer hs.Clear()

	msg2, session, err := ResponderHandshake(workerKey, testWorkerID, msg1)
	require.Error(t, err, "the pre-message hash must reject a different ML-KEM key")
	assert.Nil(t, msg2)
	assert.Nil(t, session)
}

func TestInvalidSlhdsaSignature(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	t.Run("a wrong public key fails at message 1", func(t *testing.T) {
		wrongKey, err := GenerateCompositeKeypair()
		require.NoError(t, err)
		hs, msg1, err := InitiatorHandshake1(testWorkerID, workerKey.X25519Public, workerKey.MlkemPublicKeyBytes(), slhdsaPublicKey(t, wrongKey))
		require.NoError(t, err)
		defer hs.Clear()
		_, _, err = ResponderHandshake(workerKey, testWorkerID, msg1)
		require.Error(t, err)
	})

	t.Run("a forged signature fails verification", func(t *testing.T) {
		hs, msg1, err := InitiatorHandshake1(testWorkerID, workerKey.X25519Public, workerKey.MlkemPublicKeyBytes(), slhdsaPublicKey(t, workerKey))
		require.NoError(t, err)
		msg2, _, err := ResponderHandshake(workerKey, testWorkerID, msg1)
		require.NoError(t, err)
		msg2[len(msg2)-1] ^= 0xFF
		_, err = hs.Finish(msg2)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SLH-DSA signature verification failed")
	})
}

func TestEmptyMessage(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	hs, msg1, err := InitiatorHandshake1(testWorkerID, workerKey.X25519Public, workerKey.MlkemPublicKeyBytes(), slhdsaPublicKey(t, workerKey))
	require.NoError(t, err)

	msg2, workerSession, err := ResponderHandshake(workerKey, testWorkerID, msg1)
	require.NoError(t, err)

	initiatorSession, err := hs.Finish(msg2)
	require.NoError(t, err)

	ct, err := initiatorSession.Encrypt(nil, []byte{})
	require.NoError(t, err)
	assert.NotEmpty(t, ct) // Ciphertext includes auth tag.

	pt, err := workerSession.Decrypt(nil, ct)
	require.NoError(t, err)
	assert.Empty(t, pt)
}

func TestPlaintextSizeLimit(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	hs, msg1, err := InitiatorHandshake1(testWorkerID, workerKey.X25519Public, workerKey.MlkemPublicKeyBytes(), slhdsaPublicKey(t, workerKey))
	require.NoError(t, err)

	msg2, _, err := ResponderHandshake(workerKey, testWorkerID, msg1)
	require.NoError(t, err)

	session, err := hs.Finish(msg2)
	require.NoError(t, err)

	// Exactly at limit should succeed.
	atLimit := make([]byte, contracts.MaxPlaintextPerChunk)
	_, err = session.Encrypt(nil, atLimit)
	require.NoError(t, err)

	// One byte over limit should fail.
	overLimit := make([]byte, contracts.MaxPlaintextPerChunk+1)
	_, err = session.Encrypt(nil, overLimit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plaintext too large")
}

func TestNeedsRekey(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	hs, msg1, err := InitiatorHandshake1(testWorkerID, workerKey.X25519Public, workerKey.MlkemPublicKeyBytes(), slhdsaPublicKey(t, workerKey))
	require.NoError(t, err)

	msg2, _, err := ResponderHandshake(workerKey, testWorkerID, msg1)
	require.NoError(t, err)

	session, err := hs.Finish(msg2)
	require.NoError(t, err)

	assert.False(t, session.Send.NeedsRekey(), "NeedsRekey should be false at nonce 0")
}

func TestCipherStateRekey(t *testing.T) {
	key := [keyLen]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}

	// Two paired CipherStates that start from the same key: cs is the receiver
	// (this side) and peer is the sender (the other end). Both ends of one
	// direction track the same nonce counter, so advance them in lockstep.
	cs := &CipherState{k: key}
	peer := &CipherState{k: key}

	// Two frames round-trip to bring both ends to nonce 2.
	for _, p := range [][]byte{[]byte("before-1"), []byte("before-2")} {
		ct, encErr := peer.Encrypt(nil, p)
		require.NoError(t, encErr)
		_, decErr := cs.Decrypt(nil, ct)
		require.NoError(t, decErr)
	}
	assert.Equal(t, uint64(2), cs.Nonce())
	assert.Equal(t, uint64(2), peer.Nonce())

	// A fresh 32-byte DH secret shared by both sides for this rekey. Copy it for
	// each side: rekeyWithSecret zeroes its input, so the second side needs its
	// own untouched bytes.
	dhSecret := [dhLen]byte{0xa0, 0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7, 0xa8, 0xa9, 0xaa, 0xab, 0xac, 0xad, 0xae, 0xaf, 0xb0, 0xb1, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6, 0xb7, 0xb8, 0xb9, 0xba, 0xbb, 0xbc, 0xbd, 0xbe, 0xbf}
	peerDH := dhSecret

	// peer keeps an "old key" view so we can simulate a straddling frame the
	// peer encrypted under the old key AFTER the rekey request was sent but
	// BEFORE it observed the swap — the exact case the grace window serves.
	oldKey := peer // peer still holds the pre-rekey key at nonce 2

	cs.rekeyWithSecret(dhSecret[:], nil, true)
	assert.Equal(t, uint64(0), cs.Nonce(), "rekey must reset nonce")
	assert.NotEqual(t, key, cs.k, "rekey must change the key")

	// Forward secrecy: HKDF(oldKey, nil) — what an attacker holding the old key
	// could compute — must NOT equal the new key. The fresh DH entropy is what
	// denies that.
	attackerDerived, _ := noiseHKDF(key[:], nil)
	var attackerK [keyLen]byte
	copy(attackerK[:], attackerDerived[:keyLen])
	assert.NotEqual(t, attackerK, cs.k, "new key must not be derivable from the old key alone (#321)")

	// Straddling frame: peer encrypts one more under the OLD key (nonce 2),
	// which cs must still decrypt via the retained previous key.
	ctStraddle, err := oldKey.Encrypt(nil, []byte("straddle"))
	require.NoError(t, err)
	pt, err := cs.Decrypt(nil, ctStraddle)
	require.NoError(t, err, "in-flight old-key frame must decrypt via the grace window")
	assert.Equal(t, []byte("straddle"), pt)

	// New-epoch traffic round-trips across the pair once both have rotated.
	peer.rekeyWithSecret(peerDH[:], nil, true)
	assert.Equal(t, cs.k, peer.k, "both sides must derive the same new key from shared DH entropy")
	ctNew, err := cs.Encrypt(nil, []byte("after"))
	require.NoError(t, err)
	assert.Equal(t, uint64(1), cs.Nonce())
	pt, err = peer.Decrypt(nil, ctNew)
	require.NoError(t, err)
	assert.Equal(t, []byte("after"), pt)
}

func TestCipherStateRekeyGraceWindowExpiry(t *testing.T) {
	key := [keyLen]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	cs := &CipherState{k: key}
	oldKey := &CipherState{k: key}
	dhSecret := [dhLen]byte{0xa0, 0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7, 0xa8, 0xa9, 0xaa, 0xab, 0xac, 0xad, 0xae, 0xaf, 0xb0, 0xb1, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6, 0xb7, 0xb8, 0xb9, 0xba, 0xbb, 0xbc, 0xbd, 0xbe, 0xbf}

	// Both at nonce 0; rotate cs, then the straddling frame is at nonce 0.
	cs.rekeyWithSecret(dhSecret[:], nil, true)
	ctStraddle, err := oldKey.Encrypt(nil, []byte("straddle"))
	require.NoError(t, err)

	// Within the window, the old-key frame decrypts via prev.
	pt, err := cs.Decrypt(nil, ctStraddle)
	require.NoError(t, err)
	assert.Equal(t, []byte("straddle"), pt)

	// Force expiry: push prevExpiresAt into the past. A fresh old-key frame
	// (nonce 1, since prev already consumed 0) must now fail closed.
	cs.prevExpiresAt = time.Now().Add(-time.Second)
	ctStraddle2, err := oldKey.Encrypt(nil, []byte("straddle2"))
	require.NoError(t, err)
	_, err = cs.Decrypt(nil, ctStraddle2)
	require.Error(t, err, "after the grace window an old-key frame must fail closed")

	// SCAN-3: the expired decrypt must RETIRE prev (zero + drop the reference),
	// not merely skip it — otherwise an idle channel leaves the still-valid
	// previous-epoch key in the heap until the next rekey or close, widening
	// the forward-secrecy surface the grace window introduced.
	assert.Nil(t, cs.prev, "expired prev must be zeroed and dropped on the next decrypt")
	assert.True(t, cs.prevExpiresAt.IsZero(), "prevExpiresAt must be cleared when prev is retired")
}

func TestCipherStateRekeySendDirectionKeepsNoPrev(t *testing.T) {
	// SCAN-7: the send direction keeps no grace window. rekeyWithSecret with
	// retainPrev=false must zero the replaced key and NOT retain a prev —
	// structurally, so there is no follow-up clear step for a caller to forget
	// and leave a live previous key on the Send CipherState (which no Decrypt
	// ever reads but which lingers until close).
	key := [keyLen]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	cs := &CipherState{k: key}
	oldKey := &CipherState{k: key}
	dhSecret := [dhLen]byte{0xa0, 0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7, 0xa8, 0xa9, 0xaa, 0xab, 0xac, 0xad, 0xae, 0xaf, 0xb0, 0xb1, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6, 0xb7, 0xb8, 0xb9, 0xba, 0xbb, 0xbc, 0xbd, 0xbe, 0xbf}

	cs.rekeyWithSecret(dhSecret[:], nil, false)
	assert.Nil(t, cs.prev, "send direction (retainPrev=false) must not retain a previous key")
	assert.True(t, cs.prevExpiresAt.IsZero(), "send direction must not arm a grace window")

	// An old-key straddler must fail immediately (no prev to fall back to).
	ctStraddle, err := oldKey.Encrypt(nil, []byte("straddle"))
	require.NoError(t, err)
	_, err = cs.Decrypt(nil, ctStraddle)
	require.Error(t, err, "send direction must not decrypt old-key frames (no grace window)")
}

func TestSessionNeedsRekeyEither(t *testing.T) {
	send := &CipherState{k: [keyLen]byte{1}}
	recv := &CipherState{k: [keyLen]byte{2}}
	session := &Session{Send: send, Receive: recv}

	assert.False(t, session.NeedsRekeyEither())

	send.SetNonceForTest(contracts.SoftNonceLimit + 1)
	assert.True(t, session.NeedsRekeyEither(), "send soft nonce must trip either")

	send.SetNonceForTest(0)
	recv.SetNonceForTest(contracts.SoftNonceLimit + 1)
	assert.True(t, session.NeedsRekeyEither(), "receive soft nonce must trip either")
}

type rekeyRatchetFixture struct {
	Readme  string `json:"_readme"`
	Vectors []struct {
		Name                  string `json:"name"`
		KeyHex                string `json:"keyHex"`
		DhSecretHex           string `json:"dhSecretHex"`
		PQSecretHex           string `json:"pqSecretHex,omitempty"`
		WarmupPlaintext       string `json:"warmupPlaintext"`
		Plaintext             string `json:"plaintext"`
		ExpectedCiphertextHex string `json:"expectedCiphertextHex"`
	} `json:"vectors"`
}

func TestCipherStateRekeyMatchesHKDF(t *testing.T) {
	const path = "../../../testdata/noise_rekey_vectors.json"
	data, err := os.ReadFile(path)
	require.NoError(t, err, "the rekey vectors are shared with frontend/src/lib/noise.test.ts")
	var fixture rekeyRatchetFixture
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Vectors)

	for i := range fixture.Vectors {
		v := &fixture.Vectors[i]
		t.Run(v.Name, func(t *testing.T) {
			keyBytes, err := hex.DecodeString(v.KeyHex)
			require.NoError(t, err)
			require.Len(t, keyBytes, keyLen)
			dhBytes, err := hex.DecodeString(v.DhSecretHex)
			require.NoError(t, err)
			require.Len(t, dhBytes, dhLen)
			var pqSecret []byte
			if v.PQSecretHex != "" {
				pqSecret, err = hex.DecodeString(v.PQSecretHex)
				require.NoError(t, err)
				require.Len(t, pqSecret, 2*keyLen)
			}
			var key [keyLen]byte
			copy(key[:], keyBytes)
			var dhSecret [dhLen]byte
			copy(dhSecret[:], dhBytes)

			ck1, _ := noiseHKDF(key[:], dhSecret[:])
			expectedK, _ := noiseHKDF(ck1[:], pqSecret)
			var want [keyLen]byte
			copy(want[:], expectedK[:keyLen])

			cs := &CipherState{k: key}
			_, err = cs.Encrypt(nil, []byte(v.WarmupPlaintext))
			require.NoError(t, err)
			cs.rekeyWithSecret(dhSecret[:], pqSecret, true)
			assert.Equal(t, want, cs.k)

			ct, err := cs.Encrypt(nil, []byte(v.Plaintext))
			require.NoError(t, err)
			if *updateVectors {
				v.ExpectedCiphertextHex = hex.EncodeToString(ct)
			}
			assert.Equal(t, v.ExpectedCiphertextHex, hex.EncodeToString(ct),
				"post-rekey ciphertext must match the TypeScript interop vector")

			peer := &CipherState{k: want}
			pt, err := peer.Decrypt(nil, ct)
			require.NoError(t, err)
			assert.Equal(t, []byte(v.Plaintext), pt)
		})
	}
	if *updateVectors {
		writeNoiseVectorFile(t, path, fixture)
	}
}

func TestTamperedMlkemCiphertextCausesAEADFailure(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	hs, msg1, err := InitiatorHandshake1(testWorkerID, workerKey.X25519Public, workerKey.MlkemPublicKeyBytes(), slhdsaPublicKey(t, workerKey))
	require.NoError(t, err)
	defer hs.Clear()

	const staticCiphertextOffset = dhLen + contracts.MlkemPublicKeySize + chacha20poly1305.Overhead
	msg1[staticCiphertextOffset] ^= 0xFF
	msg2, session, err := ResponderHandshake(workerKey, testWorkerID, msg1)
	require.Error(t, err, "the encrypted static ciphertext must authenticate at message 1")
	assert.Nil(t, msg2)
	assert.Nil(t, session)
}

func TestClassicalHandshakeRoundtrip(t *testing.T) {
	// A classic handshake authenticates the Worker's static ML-KEM key.
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	// Initiator starts classical handshake.
	hs, msg1, err := ClassicalInitiatorHandshake1(testWorkerID, workerKey.X25519Public, workerKey.MlkemPublicKeyBytes(), slhdsaPublicKey(t, workerKey))
	require.NoError(t, err)
	assert.Equal(t, 1632, len(msg1), "classical message1 should be 1632 bytes")

	// Worker processes msg1 and returns msg2.
	msg2, workerSession, err := ClassicalResponderHandshake(workerKey, testWorkerID, msg1)
	require.NoError(t, err)
	assert.Equal(t, 48, len(msg2), "classical message2 should be 48 bytes")
	assert.NotNil(t, workerSession)

	// Initiator completes handshake.
	initiatorSession, err := hs.Finish(msg2)
	require.NoError(t, err)
	assert.NotNil(t, initiatorSession)

	// Test bidirectional encryption.
	t.Run("initiator_to_responder", func(t *testing.T) {
		plaintext := []byte("hello from initiator (classical)")
		ciphertext, err := initiatorSession.Encrypt(nil, plaintext)
		require.NoError(t, err)
		assert.NotEqual(t, plaintext, ciphertext)

		decrypted, err := workerSession.Decrypt(nil, ciphertext)
		require.NoError(t, err)
		assert.Equal(t, plaintext, decrypted)
	})

	t.Run("responder_to_initiator", func(t *testing.T) {
		plaintext := []byte("hello from responder (classical)")
		ciphertext, err := workerSession.Encrypt(nil, plaintext)
		require.NoError(t, err)
		assert.NotEqual(t, plaintext, ciphertext)

		decrypted, err := initiatorSession.Decrypt(nil, ciphertext)
		require.NoError(t, err)
		assert.Equal(t, plaintext, decrypted)
	})
}

func TestClassicalMultipleMessages(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	hs, msg1, err := ClassicalInitiatorHandshake1(testWorkerID, workerKey.X25519Public, workerKey.MlkemPublicKeyBytes(), slhdsaPublicKey(t, workerKey))
	require.NoError(t, err)

	msg2, workerSession, err := ClassicalResponderHandshake(workerKey, testWorkerID, msg1)
	require.NoError(t, err)

	initiatorSession, err := hs.Finish(msg2)
	require.NoError(t, err)

	for i := 0; i < 100; i++ {
		msg := []byte(fmt.Sprintf("classical message %d from initiator", i))
		ct, err := initiatorSession.Encrypt(nil, msg)
		require.NoError(t, err)
		pt, err := workerSession.Decrypt(nil, ct)
		require.NoError(t, err)
		assert.Equal(t, msg, pt)

		msg = []byte(fmt.Sprintf("classical message %d from responder", i))
		ct, err = workerSession.Encrypt(nil, msg)
		require.NoError(t, err)
		pt, err = initiatorSession.Decrypt(nil, ct)
		require.NoError(t, err)
		assert.Equal(t, msg, pt)
	}
}

func TestClassicalWrongKey(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	wrongKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	// Initiator targets workerKey, but responder uses wrongKey's private key.
	// The DH will produce different shared secrets, so decrypt fails.
	hs, msg1, err := ClassicalInitiatorHandshake1(testWorkerID, workerKey.X25519Public, workerKey.MlkemPublicKeyBytes(), slhdsaPublicKey(t, workerKey))
	require.NoError(t, err)

	msg2, _, err := ClassicalResponderHandshake(wrongKey, testWorkerID, msg1)
	// Responder decryption of message1 fails because DH(wrongPriv, initiatorEphemeral)
	// produces a different key than DH(correctPriv, initiatorEphemeral).
	require.Error(t, err)
	_ = msg2
	_ = hs
}

func TestNonceLimitsConst(t *testing.T) {
	// Pin the literal nonce limits and plaintext size so a regression in the
	// constant expression (e.g. shifting the exponent or misplacing a `-1`)
	// is caught here rather than only by downstream behavior.
	assert.Equal(t, uint64(2147483647), contracts.SoftNonceLimit, "SoftNonceLimit should be 2^31-1")
	assert.Equal(t, uint64(4294967295), contracts.HardNonceLimit, "HardNonceLimit should be 2^32-1")
	// The pin keeps the frozen derivation (65535 ciphertext cap - 16 tag bytes) red if
	// wire.json retunes either primitive without this suite noticing.
	assert.Equal(t, 65519, contracts.MaxPlaintextPerChunk, "MaxPlaintextPerChunk should be 65535-16")
}

func TestMessageSizes(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	hs, msg1, err := InitiatorHandshake1(testWorkerID, workerKey.X25519Public, workerKey.MlkemPublicKeyBytes(), slhdsaPublicKey(t, workerKey))
	require.NoError(t, err)
	assert.Equal(t, 3216, len(msg1), "post-quantum message1 size")
	msg2, _, err := ResponderHandshake(workerKey, testWorkerID, msg1)
	require.NoError(t, err)
	assert.Equal(t, 51488, len(msg2), "post-quantum message2 size")
	hs.Clear()

	classicState, msg1, err := ClassicalInitiatorHandshake1(testWorkerID, workerKey.X25519Public, workerKey.MlkemPublicKeyBytes(), slhdsaPublicKey(t, workerKey))
	require.NoError(t, err)
	assert.Equal(t, 1632, len(msg1), "classic message1 size")
	msg2, _, err = ClassicalResponderHandshake(workerKey, testWorkerID, msg1)
	require.NoError(t, err)
	assert.Equal(t, 48, len(msg2), "classic message2 size")
	classicState.Clear()
}

func TestHandshakeStateZeroedAfterHybrid(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	hs, msg1, err := InitiatorHandshake1(testWorkerID, workerKey.X25519Public, workerKey.MlkemPublicKeyBytes(), slhdsaPublicKey(t, workerKey))
	require.NoError(t, err)
	assert.NotNil(t, hs.ss)
	assert.NotEmpty(t, hs.x25519Seed)
	assert.NotEmpty(t, hs.mlkemSeed)

	msg2, _, err := ResponderHandshake(workerKey, testWorkerID, msg1)
	require.NoError(t, err)
	_, err = hs.Finish(msg2)
	require.NoError(t, err)

	assert.Nil(t, hs.ss)
	assert.Equal(t, make([]byte, dhLen), hs.x25519Seed)
	assert.Equal(t, make([]byte, 64), hs.mlkemSeed)
}

func TestHandshakeStateZeroedAfterClassical(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	hs, msg1, err := ClassicalInitiatorHandshake1(testWorkerID, workerKey.X25519Public, workerKey.MlkemPublicKeyBytes(), slhdsaPublicKey(t, workerKey))
	require.NoError(t, err)
	assert.NotNil(t, hs.ss)
	assert.Empty(t, hs.mlkemSeed)

	msg2, _, err := ClassicalResponderHandshake(workerKey, testWorkerID, msg1)
	require.NoError(t, err)
	_, err = hs.Finish(msg2)
	require.NoError(t, err)

	assert.Nil(t, hs.ss)
	assert.Equal(t, make([]byte, dhLen), hs.x25519Seed)
}

// TestGenerateEphemeralX25519SeedRoundTrip pins the seed-based ephemeral API
// the in-band rekey introduced so the fresh X25519 secret can be held as a
// caller-owned []byte (and genuinely zeroed) rather than an opaque
// *ecdh.PrivateKey whose internal scalar crypto/ecdh gives no way to wipe.
// Two paired (seed → pub) sides must derive the same shared secret via
// DeriveRekeySecrets — the core property both rekey peers rely on.
func TestGenerateEphemeralX25519SeedRoundTrip(t *testing.T) {
	initSeed, err := GenerateEphemeralX25519Seed()
	require.NoError(t, err)
	require.Len(t, initSeed, dhLen, "ephemeral seed must be 32 bytes")
	respSeed, err := GenerateEphemeralX25519Seed()
	require.NoError(t, err)

	initPub, err := X25519PublicFromSeed(initSeed)
	require.NoError(t, err)
	require.Len(t, initPub, dhLen, "ephemeral public key must be 32 bytes")
	respPub, err := X25519PublicFromSeed(respSeed)
	require.NoError(t, err)

	// Each side derives the shared DH secret from its own seed + the peer's pub.
	initDH, initPQ, err := DeriveRekeySecrets(RekeyMaterial{
		LocalEphemeralPriv: initSeed,
		PeerEphemeralPub:   respPub,
	})
	require.NoError(t, err)
	require.Nil(t, initPQ, "classic rekey material yields a nil PQ secret")
	respDH, _, err := DeriveRekeySecrets(RekeyMaterial{
		LocalEphemeralPriv: respSeed,
		PeerEphemeralPub:   initPub,
	})
	require.NoError(t, err)

	assert.Equal(t, initDH, respDH, "both sides must derive the same X25519 shared secret")
	assert.Len(t, initDH, dhLen, "shared secret must be 32 bytes")
	// The shared secret is not all-zero (X25519 of two random keys is non-trivial).
	nonZero := false
	for _, b := range initDH {
		if b != 0 {
			nonZero = true
			break
		}
	}
	assert.True(t, nonZero, "shared secret must be non-zero")
}

// TestGenerateEphemeralX25519SeedRejectsBadSeed pins that X25519PublicFromSeed
// surfaces a parse error (rather than silently deriving a key from garbage) —
// the rekey paths rely on this to fail closed on a malformed seed.
func TestX25519PublicFromSeedRejectsBadSeed(t *testing.T) {
	_, err := X25519PublicFromSeed([]byte("too-short"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "X25519 seed")
}

// RestoreCompositeKeypair takes only the private halves and derives each public
// one, so a keypair that survived a save/restore round trip is the one that was
// generated. If it were not, the Worker would pin keys it cannot use.
func TestRestoreCompositeKeypairRoundTripsAPrivateOnlyTriple(t *testing.T) {
	generated, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	slhdsaPriv, err := generated.SlhdsaPrivateKey.MarshalBinary()
	require.NoError(t, err)

	restored, err := RestoreCompositeKeypair(
		generated.X25519Private,
		generated.MlkemDecapsulationKey.Bytes(),
		slhdsaPriv,
	)
	require.NoError(t, err)

	assert.Equal(t, generated.X25519Public, restored.X25519Public, "the public point is derived from the scalar")
	assert.Equal(t, generated.X25519Private, restored.X25519Private)
	assert.Equal(t, generated.MlkemPublicKeyBytes(), restored.MlkemPublicKeyBytes())
	generatedSlhdsa, err := generated.SlhdsaPublicKeyBytes()
	require.NoError(t, err)
	restoredSlhdsa, err := restored.SlhdsaPublicKeyBytes()
	require.NoError(t, err)
	assert.Equal(t, generatedSlhdsa, restoredSlhdsa)
	assert.Equal(t, generated.Fingerprint(), restored.Fingerprint())
}

func TestRestoreCompositeKeypairRefusesBadKeyMaterial(t *testing.T) {
	generated, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	mlkemSeed := generated.MlkemDecapsulationKey.Bytes()
	slhdsaPriv, err := generated.SlhdsaPrivateKey.MarshalBinary()
	require.NoError(t, err)

	// FIPS 205 slh_keygen reads SK.seed, SK.prf and PK.seed, so the private
	// key is four n-byte fields in that order.
	n := contracts.SlhdsaPublicKeySize / 2
	require.Len(t, slhdsaPriv, 4*n)

	alteredSeed := append([]byte(nil), slhdsaPriv...)
	alteredSeed[0] ^= 0xFF
	alteredRoot := append([]byte(nil), slhdsaPriv...)
	alteredRoot[3*n] ^= 0xFF

	for name, tc := range map[string]struct {
		x25519Priv []byte
		mlkemSeed  []byte
		slhdsaPriv []byte
		want       string
	}{
		"a short X25519 private key": {
			x25519Priv: generated.X25519Private[:31], mlkemSeed: mlkemSeed, slhdsaPriv: slhdsaPriv,
			want: "X25519",
		},
		"an ML-KEM seed of 63 bytes": {
			x25519Priv: generated.X25519Private, mlkemSeed: mlkemSeed[:63], slhdsaPriv: slhdsaPriv,
			want: "ML-KEM",
		},
		"an SLH-DSA private key of 127 bytes": {
			x25519Priv: generated.X25519Private, mlkemSeed: mlkemSeed, slhdsaPriv: slhdsaPriv[:127],
			want: "SLH-DSA",
		},
		"an SLH-DSA secret seed that does not match its embedded public key": {
			x25519Priv: generated.X25519Private, mlkemSeed: mlkemSeed, slhdsaPriv: alteredSeed,
			want: "does not match its public key",
		},
		"an SLH-DSA PK.root that does not match the seeds": {
			x25519Priv: generated.X25519Private, mlkemSeed: mlkemSeed, slhdsaPriv: alteredRoot,
			want: "does not match its public key",
		},
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			ck, err := RestoreCompositeKeypair(tc.x25519Priv, tc.mlkemSeed, tc.slhdsaPriv)
			require.Error(t, err)
			assert.Nil(t, ck)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// staticPublicKeys returns the three static public keys of a fresh keypair.
func staticPublicKeys(t *testing.T) (x25519Pub, mlkemPub, slhdsaPub []byte) {
	t.Helper()
	ck, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	slhdsaPub, err = ck.SlhdsaPublicKeyBytes()
	require.NoError(t, err)
	return ck.X25519Public, ck.MlkemPublicKeyBytes(), slhdsaPub
}

// A key of the right size can still be one that every handshake refuses, and a
// pin can never change afterwards. Each of the two checks past the size check
// has its own case, because a size check alone passes both.
func TestValidateStaticPublicKeys(t *testing.T) {
	x25519Pub, mlkemPub, slhdsaPub := staticPublicKeys(t)

	t.Run("accepts a generated set", func(t *testing.T) {
		require.NoError(t, ValidateStaticPublicKeys(x25519Pub, mlkemPub, slhdsaPub))
	})

	// orderEightPoint is a point of order 8 on Curve25519, encoded as an X25519
	// public key: every X25519 private key multiplies it to the all-zero output.
	orderEightPoint, err := hex.DecodeString("e0eb7a7c3b41b8ae1656e3faf19fc46ada098deb9c32b1fd866205165f49b800")
	require.NoError(t, err)
	// Every 12-bit coefficient of this key is 0xFFF, above the 3329 modulus.
	outOfRangeMlkem := bytes.Repeat([]byte{0xFF}, contracts.MlkemPublicKeySize)

	for _, tc := range []struct {
		name    string
		x25519  []byte
		mlkem   []byte
		slhdsa  []byte
		wantErr string
	}{
		{"an absent x25519 key", nil, mlkemPub, slhdsaPub, "worker x25519 public key is 0 bytes"},
		{"a short x25519 key", x25519Pub[:31], mlkemPub, slhdsaPub, "worker x25519 public key is 31 bytes"},
		{"a long x25519 key", append(slices.Clone(x25519Pub), 0), mlkemPub, slhdsaPub, "worker x25519 public key is 33 bytes"},
		{"an absent mlkem key", x25519Pub, nil, slhdsaPub, "worker mlkem public key is 0 bytes"},
		{"a short mlkem key", x25519Pub, mlkemPub[:1567], slhdsaPub, "worker mlkem public key is 1567 bytes"},
		{"a long mlkem key", x25519Pub, append(slices.Clone(mlkemPub), 0), slhdsaPub, "worker mlkem public key is 1569 bytes"},
		{"an absent slhdsa key", x25519Pub, mlkemPub, nil, "worker slhdsa public key is 0 bytes"},
		{"a short slhdsa key", x25519Pub, mlkemPub, slhdsaPub[:63], "worker slhdsa public key is 63 bytes"},
		{"a long slhdsa key", x25519Pub, mlkemPub, append(slices.Clone(slhdsaPub), 0), "worker slhdsa public key is 65 bytes"},
		{"32 zero bytes as the x25519 key", make([]byte, contracts.X25519PublicKeySize), mlkemPub, slhdsaPub, "low-order point"},
		{"an x25519 key of order 8", orderEightPoint, mlkemPub, slhdsaPub, "low-order point"},
		{"mlkem coefficients out of range", x25519Pub, outOfRangeMlkem, slhdsaPub, "not a valid ML-KEM-1024 encapsulation key"},
	} {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			err := ValidateStaticPublicKeys(tc.x25519, tc.mlkem, tc.slhdsa)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// ValidateStaticPublicKeys checks only the size of the SLH-DSA key, because the
// parser refuses no value of that size. If a new version of the library starts to
// refuse one, this test fails, and the validation needs the parse.
func TestSlhdsaParserAcceptsEveryKeyOfTheContractSize(t *testing.T) {
	t.Parallel()

	for _, fill := range []byte{0x00, 0xFF} {
		key := slhdsa.PublicKey{ID: slhdsa.SHAKE_256f}
		require.NoError(t, key.UnmarshalBinary(bytes.Repeat([]byte{fill}, contracts.SlhdsaPublicKeySize)))
	}
}

// The keys that the check accepts are the keys that the handshake uses, so an
// accepted set completes a handshake.
func TestValidateStaticPublicKeysAcceptsWhatTheHandshakeParses(t *testing.T) {
	worker, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	slhdsaPub, err := worker.SlhdsaPublicKeyBytes()
	require.NoError(t, err)
	require.NoError(t, ValidateStaticPublicKeys(worker.X25519Public, worker.MlkemPublicKeyBytes(), slhdsaPub))

	hs, msg1, err := InitiatorHandshake1(testWorkerID, worker.X25519Public, worker.MlkemPublicKeyBytes(), slhdsaPublicKey(t, worker))
	require.NoError(t, err)
	msg2, _, err := ResponderHandshake(worker, testWorkerID, msg1)
	require.NoError(t, err)
	_, err = hs.Finish(msg2)
	require.NoError(t, err)
}

func TestStaticKeyHolderCannotReplayARecordedSession(t *testing.T) {
	worker, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	responderSeed, err := GenerateEphemeralX25519Seed()
	require.NoError(t, err)
	rnd := systemEntropy
	rnd.x25519Seed = func() ([]byte, error) { return slices.Clone(responderSeed), nil }

	hs, message1, err := InitiatorHandshake1(testWorkerID, worker.X25519Public, worker.MlkemPublicKeyBytes(), slhdsaPublicKey(t, worker))
	require.NoError(t, err)
	message2, _, err := responderHandshake(rnd, worker, testWorkerID, message1)
	require.NoError(t, err)
	recordedSession, err := hs.Finish(message2)
	require.NoError(t, err)
	ciphertext, err := recordedSession.Encrypt(nil, []byte("recorded message"))
	require.NoError(t, err)

	_, replayedSession, err := responderHandshake(rnd, worker, testWorkerID, message1)
	require.NoError(t, err)
	_, err = replayedSession.Decrypt(nil, ciphertext)
	require.Error(t, err, "fresh post-quantum entropy must make a replayed responder session differ")
}

func TestStaticKeyHolderCannotDeriveARekeySecret(t *testing.T) {
	worker, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	offer, err := OfferRekeyPQ(worker.MlkemPublicKeyBytes())
	require.NoError(t, err)
	staticCiphertext := slices.Clone(offer.StaticCiphertext)
	ephemeralPublicKey := slices.Clone(offer.EphemeralPublicKey)
	responderSecret, ephemeralCiphertext, err := AnswerRekeyPQ(worker.MlkemDecapsulationKey, staticCiphertext, ephemeralPublicKey)
	require.NoError(t, err)
	defer Zero(responderSecret)
	initiatorSecret, err := offer.Accept(ephemeralCiphertext)
	require.NoError(t, err)
	defer Zero(initiatorSecret)
	require.Equal(t, responderSecret, initiatorSecret)
	require.Len(t, initiatorSecret, 2*keyLen)

	staticSecret, err := worker.MlkemDecapsulationKey.Decapsulate(staticCiphertext)
	require.NoError(t, err)
	defer Zero(staticSecret)
	assert.Equal(t, staticSecret, initiatorSecret[:keyLen])
	secondResponderSecret, _, err := AnswerRekeyPQ(worker.MlkemDecapsulationKey, staticCiphertext, ephemeralPublicKey)
	require.NoError(t, err)
	defer Zero(secondResponderSecret)
	assert.NotEqual(t, responderSecret[keyLen:], secondResponderSecret[keyLen:], "the static key alone must not determine the ephemeral half")
}

func TestStaticX25519KeyAloneCannotAnswerAClassicHandshake(t *testing.T) {
	worker, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	answer := func(t *testing.T, message1 []byte, useMlkemKey bool) []byte {
		t.Helper()
		ss := newSymmetricState(contracts.NoiseClassicProtocolName)
		defer ss.clear()
		prologue, err := handshakePrologue(testWorkerID)
		require.NoError(t, err)
		ss.mixHash(prologue)
		mixStaticKeys(ss, worker.X25519Public, worker.MlkemPublicKeyBytes(), slhdsaPublicKey(t, worker))
		initiatorEphemeral := message1[:dhLen]
		ss.mixHash(initiatorEphemeral)
		dhES, err := DH(worker.X25519Private, initiatorEphemeral)
		require.NoError(t, err)
		ss.mixKey(dhES)
		zeroBytes(dhES)

		encryptedStaticLen := contracts.MlkemCiphertextSize + chacha20poly1305.Overhead
		staticCiphertext, err := ss.decryptAndHash(message1[dhLen : dhLen+encryptedStaticLen])
		require.NoError(t, err)
		staticSecret := make([]byte, keyLen)
		if useMlkemKey {
			staticSecret, err = worker.MlkemDecapsulationKey.Decapsulate(staticCiphertext)
			require.NoError(t, err)
		}
		defer zeroBytes(staticSecret)
		ss.mixKey(staticSecret)
		ss.mixHash(message1[dhLen+encryptedStaticLen:])

		responderSeed, err := GenerateEphemeralX25519Seed()
		require.NoError(t, err)
		defer zeroBytes(responderSeed)
		responderEphemeral, err := X25519PublicFromSeed(responderSeed)
		require.NoError(t, err)
		ss.mixHash(responderEphemeral)
		dhEE, err := DH(responderSeed, initiatorEphemeral)
		require.NoError(t, err)
		ss.mixKey(dhEE)
		zeroBytes(dhEE)
		encryptedPayload, err := ss.encryptAndHash(nil)
		require.NoError(t, err)
		return append(responderEphemeral, encryptedPayload...)
	}

	for _, tc := range []struct {
		name        string
		useMlkemKey bool
		wantError   bool
	}{
		{name: "control answer uses the static ML-KEM key", useMlkemKey: true},
		{name: "X25519 alone cannot construct the payload tag", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hs, message1, err := ClassicalInitiatorHandshake1(testWorkerID, worker.X25519Public, worker.MlkemPublicKeyBytes(), slhdsaPublicKey(t, worker))
			require.NoError(t, err)
			message2 := answer(t, message1, tc.useMlkemKey)
			session, err := hs.Finish(message2)
			if tc.wantError {
				require.Error(t, err, "the static ML-KEM key must also authenticate the classic responder")
				assert.Nil(t, session)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, session)
		})
	}
}

func startHandshakeForMode(t *testing.T, key *CompositeKeypair, workerID string, postQuantum bool) (*HandshakeState, []byte) {
	t.Helper()
	if postQuantum {
		hs, message1, err := InitiatorHandshake1(workerID, key.X25519Public, key.MlkemPublicKeyBytes(), slhdsaPublicKey(t, key))
		require.NoError(t, err)
		return hs, message1
	}
	hs, message1, err := ClassicalInitiatorHandshake1(workerID, key.X25519Public, key.MlkemPublicKeyBytes(), slhdsaPublicKey(t, key))
	require.NoError(t, err)
	return hs, message1
}

func answerHandshakeForMode(t *testing.T, key *CompositeKeypair, workerID string, message1 []byte, postQuantum bool) ([]byte, *Session) {
	t.Helper()
	if postQuantum {
		message2, session, err := ResponderHandshake(key, workerID, message1)
		require.NoError(t, err)
		return message2, session
	}
	message2, session, err := ClassicalResponderHandshake(key, workerID, message1)
	require.NoError(t, err)
	return message2, session
}

func TestHandshakeBindsTheWorkerID(t *testing.T) {
	worker, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	for _, postQuantum := range []bool{false, true} {
		hs, message1 := startHandshakeForMode(t, worker, "worker-a", postQuantum)
		var session *Session
		if postQuantum {
			_, session, err = ResponderHandshake(worker, "worker-b", message1)
		} else {
			_, session, err = ClassicalResponderHandshake(worker, "worker-b", message1)
		}
		require.Error(t, err)
		assert.Nil(t, session)
		hs.Clear()
	}
}

func TestHandshakeBindsEveryStaticKey(t *testing.T) {
	worker, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	other, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	for _, postQuantum := range []bool{false, true} {
		for _, keyIndex := range []int{0, 1, 2} {
			x25519Pub := worker.X25519Public
			mlkemPub := worker.MlkemPublicKeyBytes()
			slhdsaPub := slhdsaPublicKey(t, worker)
			switch keyIndex {
			case 0:
				x25519Pub = other.X25519Public
			case 1:
				mlkemPub = other.MlkemPublicKeyBytes()
			case 2:
				slhdsaPub = slhdsaPublicKey(t, other)
			}
			var hs *HandshakeState
			var message1 []byte
			if postQuantum {
				hs, message1, err = InitiatorHandshake1(testWorkerID, x25519Pub, mlkemPub, slhdsaPub)
			} else {
				hs, message1, err = ClassicalInitiatorHandshake1(testWorkerID, x25519Pub, mlkemPub, slhdsaPub)
			}
			require.NoError(t, err)
			var session *Session
			if postQuantum {
				_, session, err = ResponderHandshake(worker, testWorkerID, message1)
			} else {
				_, session, err = ClassicalResponderHandshake(worker, testWorkerID, message1)
			}
			require.Error(t, err, "static key %d must be bound in mode %t", keyIndex, postQuantum)
			assert.Nil(t, session)
			hs.Clear()
		}
	}
}

func TestHandshakeRefusesAnEmptyWorkerID(t *testing.T) {
	worker, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	for _, postQuantum := range []bool{false, true} {
		var hs *HandshakeState
		var message1 []byte
		if postQuantum {
			hs, message1, err = InitiatorHandshake1("", worker.X25519Public, worker.MlkemPublicKeyBytes(), slhdsaPublicKey(t, worker))
		} else {
			hs, message1, err = ClassicalInitiatorHandshake1("", worker.X25519Public, worker.MlkemPublicKeyBytes(), slhdsaPublicKey(t, worker))
		}
		require.Error(t, err)
		assert.Nil(t, hs)
		assert.Nil(t, message1)

		hs, message1 = startHandshakeForMode(t, worker, testWorkerID, postQuantum)
		if postQuantum {
			_, _, err = ResponderHandshake(worker, "", message1)
		} else {
			_, _, err = ClassicalResponderHandshake(worker, "", message1)
		}
		require.Error(t, err)
		hs.Clear()
	}
}

func TestHandshakeRefusesAMessageOfTheOtherMode(t *testing.T) {
	worker, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	pqState, pqMessage1 := startHandshakeForMode(t, worker, testWorkerID, true)
	classicState, classicMessage1 := startHandshakeForMode(t, worker, testWorkerID, false)
	_, session, err := ResponderHandshake(worker, testWorkerID, classicMessage1)
	require.Error(t, err)
	assert.Nil(t, session)
	_, session, err = ClassicalResponderHandshake(worker, testWorkerID, pqMessage1)
	require.Error(t, err)
	assert.Nil(t, session)

	pqMessage2, _ := answerHandshakeForMode(t, worker, testWorkerID, pqMessage1, true)
	classicMessage2, _ := answerHandshakeForMode(t, worker, testWorkerID, classicMessage1, false)
	_, err = pqState.Finish(classicMessage2)
	require.Error(t, err)
	_, err = classicState.Finish(pqMessage2)
	require.Error(t, err)
}

func TestProtocolNameStartsTheHandshakeHash(t *testing.T) {
	name64 := strings.Repeat("a", 64)
	name65 := strings.Repeat("b", 65)
	assert.Equal(t, []byte(name64), newSymmetricState(name64).h[:])
	hashed := blake2b.Sum512([]byte(name65))
	assert.Equal(t, hashed[:], newSymmetricState(name65).h[:])
	classicHash := newSymmetricState(contracts.NoiseClassicProtocolName).h
	postQuantumHash := newSymmetricState(contracts.NoisePostQuantumProtocolName).h
	assert.NotEqual(t, classicHash, postQuantumHash)
}

func TestResponderRefusesAnInvalidEphemeralMlkemKey(t *testing.T) {
	worker, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	ss := newSymmetricState(contracts.NoisePostQuantumProtocolName)
	defer ss.clear()
	prologue, err := handshakePrologue(testWorkerID)
	require.NoError(t, err)
	ss.mixHash(prologue)
	mixStaticKeys(ss, worker.X25519Public, worker.MlkemPublicKeyBytes(), slhdsaPublicKey(t, worker))
	seed, err := GenerateEphemeralX25519Seed()
	require.NoError(t, err)
	defer zeroBytes(seed)
	ephemeral, err := X25519PublicFromSeed(seed)
	require.NoError(t, err)
	ss.mixHash(ephemeral)
	dhES, err := DH(seed, worker.X25519Public)
	require.NoError(t, err)
	ss.mixKey(dhES)
	zeroBytes(dhES)
	encryptedKey, err := ss.encryptAndHash(bytes.Repeat([]byte{0xFF}, contracts.MlkemPublicKeySize))
	require.NoError(t, err)
	workerKey, err := mlkem.NewEncapsulationKey1024(worker.MlkemPublicKeyBytes())
	require.NoError(t, err)
	staticSecret, staticCiphertext := workerKey.Encapsulate()
	encryptedCiphertext, err := ss.encryptAndHash(staticCiphertext)
	require.NoError(t, err)
	ss.mixKey(staticSecret)
	zeroBytes(staticSecret)
	payload, err := ss.encryptAndHash(nil)
	require.NoError(t, err)
	message1 := bytes.Join([][]byte{ephemeral, encryptedKey, encryptedCiphertext, payload}, nil)

	for _, tc := range []struct {
		name   string
		offset int
		want   string
	}{
		{name: "key parse follows all authentication", offset: -1, want: "parse message1 ephemeral ML-KEM key"},
		{name: "static ciphertext authentication precedes key parse", offset: dhLen + contracts.MlkemPublicKeySize + chacha20poly1305.Overhead, want: "decrypt message1 static ML-KEM ciphertext"},
		{name: "payload authentication precedes key parse", offset: len(message1) - 1, want: "decrypt message1 payload"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			corrupted := slices.Clone(message1)
			if tc.offset >= 0 {
				corrupted[tc.offset] ^= 0xFF
			}
			_, session, err := ResponderHandshake(worker, testWorkerID, corrupted)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Nil(t, session)
		})
	}
}

func TestFinishClearsTheStateOnEveryExit(t *testing.T) {
	worker, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	for _, postQuantum := range []bool{false, true} {
		cases := []struct {
			name    string
			mutate  func([]byte) []byte
			wantErr bool
		}{
			{name: "short message", mutate: func(msg []byte) []byte { return slices.Clone(msg[:16]) }, wantErr: true},
			{name: "forged payload tag", mutate: func(msg []byte) []byte {
				msg = slices.Clone(msg)
				offset := dhLen
				if postQuantum {
					offset += contracts.MlkemCiphertextSize + chacha20poly1305.Overhead
				}
				msg[offset] ^= 0xFF
				return msg
			}, wantErr: true},
			{name: "low-order responder ephemeral", mutate: func(msg []byte) []byte {
				msg = slices.Clone(msg)
				clear(msg[:dhLen])
				return msg
			}, wantErr: true},
			{name: "success"},
		}
		if postQuantum {
			cases = append(cases, struct {
				name    string
				mutate  func([]byte) []byte
				wantErr bool
			}{name: "forged signature", mutate: func(msg []byte) []byte {
				msg = slices.Clone(msg)
				msg[len(msg)-1] ^= 0xFF
				return msg
			}, wantErr: true})
		}
		for _, tc := range cases {
			t.Run(fmt.Sprintf("postQuantum=%t/%s", postQuantum, tc.name), func(t *testing.T) {
				hs, message1 := startHandshakeForMode(t, worker, testWorkerID, postQuantum)
				message2, _ := answerHandshakeForMode(t, worker, testWorkerID, message1, postQuantum)
				if tc.mutate != nil {
					message2 = tc.mutate(message2)
				}
				session, err := hs.Finish(message2)
				if tc.wantErr {
					require.Error(t, err)
					assert.Nil(t, session)
				} else {
					require.NoError(t, err)
					assert.NotNil(t, session)
				}
				assert.Nil(t, hs.ss)
				assert.Equal(t, make([]byte, dhLen), hs.x25519Seed)
				if postQuantum {
					assert.Equal(t, make([]byte, 64), hs.mlkemSeed)
				}
				_, err = hs.Finish(message2)
				require.Error(t, err)
			})
		}
	}
}

func TestHandshake1ClearsItsSecretsOnAFailure(t *testing.T) {
	worker, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	for _, postQuantum := range []bool{false, true} {
		for _, invalidKey := range []string{"mlkem", "x25519"} {
			var kept [][]byte
			rnd := systemEntropy
			rnd.x25519Seed = func() ([]byte, error) {
				seed := bytes.Repeat([]byte{1}, dhLen)
				kept = append(kept, seed)
				return seed, nil
			}
			rnd.mlkemSeed = func() ([]byte, error) {
				seed := bytes.Repeat([]byte{2}, 64)
				kept = append(kept, seed)
				return seed, nil
			}
			x25519Pub := worker.X25519Public
			mlkemPub := worker.MlkemPublicKeyBytes()
			if invalidKey == "x25519" {
				x25519Pub = make([]byte, dhLen)
			} else {
				mlkemPub = bytes.Repeat([]byte{0xFF}, contracts.MlkemPublicKeySize)
			}
			var hs *HandshakeState
			var message1 []byte
			if postQuantum {
				hs, message1, err = initiatorHandshake1(rnd, testWorkerID, x25519Pub, mlkemPub, slhdsaPublicKey(t, worker))
			} else {
				hs, message1, err = classicalInitiatorHandshake1(rnd, testWorkerID, x25519Pub, mlkemPub, slhdsaPublicKey(t, worker))
			}
			require.Error(t, err)
			assert.Nil(t, hs)
			assert.Nil(t, message1)
			require.NotEmpty(t, kept)
			for _, secret := range kept {
				assert.Equal(t, make([]byte, len(secret)), secret)
			}
		}
	}
}

func TestOfferRekeyPQClearsItsSecretsOnAFailure(t *testing.T) {
	worker, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	rnd := systemEntropy
	var sharedSecret []byte
	rnd.encapsulate = func(ek *mlkem.EncapsulationKey1024) ([]byte, []byte) {
		var ciphertext []byte
		sharedSecret, ciphertext = ek.Encapsulate()
		return sharedSecret, ciphertext
	}
	rnd.mlkemSeed = func() ([]byte, error) { return nil, errors.New("key generation failed") }
	offer, err := offerRekeyPQ(rnd, worker.MlkemPublicKeyBytes())
	require.Error(t, err)
	assert.Nil(t, offer)
	require.NotEmpty(t, sharedSecret)
	assert.Equal(t, make([]byte, keyLen), sharedSecret)
}

func TestCipherStateAuthenticatesItsAssociatedData(t *testing.T) {
	key := [keyLen]byte{1}
	receiver := &CipherState{k: key}
	sender := &CipherState{k: key}
	ad := []byte("channel-envelope")
	ciphertext, err := sender.Encrypt(ad, []byte("payload"))
	require.NoError(t, err)
	_, err = receiver.Decrypt([]byte("other"), ciphertext)
	require.Error(t, err)
	_, err = receiver.Decrypt(nil, ciphertext)
	require.Error(t, err)
	plaintext, err := receiver.Decrypt(ad, ciphertext)
	require.NoError(t, err)
	assert.Equal(t, []byte("payload"), plaintext)

	previousKey := [keyLen]byte{2}
	receiver = &CipherState{k: previousKey}
	oldSender := &CipherState{k: previousKey}
	dhSecret := bytes.Repeat([]byte{3}, dhLen)
	receiver.rekeyWithSecret(dhSecret, nil, true)
	ciphertext, err = oldSender.Encrypt(ad, []byte("old epoch"))
	require.NoError(t, err)
	_, err = receiver.Decrypt([]byte("other"), ciphertext)
	require.Error(t, err)
	_, err = receiver.Decrypt(nil, ciphertext)
	require.Error(t, err)
	plaintext, err = receiver.Decrypt(ad, ciphertext)
	require.NoError(t, err, "a failed attempt must not advance the previous key's nonce")
	assert.Equal(t, []byte("old epoch"), plaintext)
}

func TestRekeyPQRefusesMalformedInput(t *testing.T) {
	worker, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	offer, err := OfferRekeyPQ(worker.MlkemPublicKeyBytes())
	require.NoError(t, err)
	staticCiphertext := slices.Clone(offer.StaticCiphertext)
	ephemeralPublicKey := slices.Clone(offer.EphemeralPublicKey)
	offer.Zero()

	_, _, err = AnswerRekeyPQ(worker.MlkemDecapsulationKey, staticCiphertext[:1567], ephemeralPublicKey)
	require.Error(t, err)
	_, _, err = AnswerRekeyPQ(worker.MlkemDecapsulationKey, staticCiphertext, ephemeralPublicKey[:1567])
	require.Error(t, err)
	_, _, err = AnswerRekeyPQ(worker.MlkemDecapsulationKey, staticCiphertext, bytes.Repeat([]byte{0xFF}, contracts.MlkemPublicKeySize))
	require.Error(t, err)
	_, _, err = AnswerRekeyPQ(nil, staticCiphertext, nil)
	require.Error(t, err)
	_, _, err = AnswerRekeyPQ(nil, nil, ephemeralPublicKey)
	require.Error(t, err)

	realOffer, err := OfferRekeyPQ(worker.MlkemPublicKeyBytes())
	require.NoError(t, err)
	_, err = realOffer.Accept(make([]byte, contracts.MlkemCiphertextSize-1))
	require.Error(t, err)
	var nilOffer *RekeyPQOffer
	_, err = nilOffer.Accept([]byte{1})
	require.Error(t, err)
	emptyOffer, err := OfferRekeyPQ(nil)
	require.NoError(t, err)
	require.NotNil(t, emptyOffer)
	_, err = emptyOffer.Accept([]byte{1})
	require.Error(t, err)

	pqSecret, ciphertext, err := AnswerRekeyPQ(nil, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, pqSecret)
	assert.Nil(t, ciphertext)
	pqSecret, err = nilOffer.Accept(nil)
	require.NoError(t, err)
	assert.Nil(t, pqSecret)
	pqSecret, err = emptyOffer.Accept(nil)
	require.NoError(t, err)
	assert.Nil(t, pqSecret)
	nilOffer.Zero()
	emptyOffer.Zero()
	emptyOffer.Zero()
}

type noiseKeyPairVector struct {
	X25519Hex string `json:"x25519Hex"`
	MlkemHex  string `json:"mlkemHex"`
	SlhdsaHex string `json:"slhdsaHex"`
}

type noisePrivateVector struct {
	X25519Hex    string `json:"x25519Hex"`
	MlkemSeedHex string `json:"mlkemSeedHex"`
	SlhdsaHex    string `json:"slhdsaHex"`
}

type noiseEntropyVector struct {
	X25519SeedHex             string `json:"x25519SeedHex,omitempty"`
	MlkemKeygenSeedHex        string `json:"mlkemKeygenSeedHex,omitempty"`
	MlkemEncapsulationSeedHex string `json:"mlkemEncapsulationSeedHex,omitempty"`
	SignRandomHex             string `json:"signRandomHex,omitempty"`
}

type noiseFrameVector struct {
	CorrelationID string `json:"correlationId"`
	Flags         uint32 `json:"flags"`
	Plaintext     string `json:"plaintext"`
	CiphertextHex string `json:"ciphertextHex"`
}

type noiseHandshakeVector struct {
	Name             string             `json:"name"`
	Mode             string             `json:"mode"`
	WorkerID         string             `json:"workerId"`
	ChannelID        string             `json:"channelId"`
	ResponderPrivate noisePrivateVector `json:"responderPrivate"`
	ResponderPublic  noiseKeyPairVector `json:"responderPublic"`
	InitiatorEntropy noiseEntropyVector `json:"initiatorEntropy"`
	ResponderEntropy noiseEntropyVector `json:"responderEntropy"`
	Message1Hex      string             `json:"message1Hex"`
	Message2Hex      string             `json:"message2Hex"`
	InitiatorFrame   noiseFrameVector   `json:"initiatorFrame"`
	ResponderFrame   noiseFrameVector   `json:"responderFrame"`
}

type noiseHandshakeFixture struct {
	Readme  string                 `json:"_readme"`
	Vectors []noiseHandshakeVector `json:"vectors"`
}

func patternedBytes(size int, offset byte) []byte {
	out := make([]byte, size)
	for i := range out {
		out[i] = byte(i) + offset
	}
	return out
}

func deterministicPrivateVector(t *testing.T, offset byte) noisePrivateVector {
	t.Helper()
	_, privateKey, err := slhdsa.GenerateKey(bytes.NewReader(patternedBytes(96, offset+2)), slhdsa.SHAKE_256f)
	require.NoError(t, err)
	privateBytes, err := privateKey.MarshalBinary()
	require.NoError(t, err)
	return noisePrivateVector{
		X25519Hex:    hex.EncodeToString(patternedBytes(dhLen, offset)),
		MlkemSeedHex: hex.EncodeToString(patternedBytes(64, offset+1)),
		SlhdsaHex:    hex.EncodeToString(privateBytes),
	}
}

func sourceHandshakeVectors(t *testing.T) noiseHandshakeFixture {
	t.Helper()
	fixture := noiseHandshakeFixture{Readme: "Cross-language Noise channel handshake and transport-frame vectors. Go owns these files; the TypeScript suite replays the same private keys, entropy, messages, and frame associated data."}
	for _, mode := range []string{"classic", "post_quantum"} {
		offset := byte(1)
		if mode == "post_quantum" {
			offset = 31
		}
		v := noiseHandshakeVector{
			Name:             mode,
			Mode:             mode,
			WorkerID:         "worker-" + mode,
			ChannelID:        "channel-" + mode,
			ResponderPrivate: deterministicPrivateVector(t, offset),
			InitiatorEntropy: noiseEntropyVector{
				X25519SeedHex:             hex.EncodeToString(patternedBytes(dhLen, offset+3)),
				MlkemEncapsulationSeedHex: hex.EncodeToString(patternedBytes(32, offset+4)),
			},
			ResponderEntropy: noiseEntropyVector{
				X25519SeedHex: hex.EncodeToString(patternedBytes(dhLen, offset+5)),
			},
			InitiatorFrame: noiseFrameVector{
				CorrelationID: "9007199254740993",
				Flags:         1,
				Plaintext:     "from the initiator",
			},
			ResponderFrame: noiseFrameVector{
				CorrelationID: "5",
				Flags:         0,
				Plaintext:     "from the responder",
			},
		}
		if mode == "post_quantum" {
			v.InitiatorEntropy.MlkemKeygenSeedHex = hex.EncodeToString(patternedBytes(64, offset+6))
			v.ResponderEntropy.MlkemEncapsulationSeedHex = hex.EncodeToString(patternedBytes(32, offset+7))
			v.ResponderEntropy.SignRandomHex = hex.EncodeToString(patternedBytes(32, offset+8))
		}
		fixture.Vectors = append(fixture.Vectors, v)
	}
	return fixture
}

func decodeVectorHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	require.NoError(t, err)
	return decoded
}

func vectorEntropy(t *testing.T, v noiseEntropyVector) entropy {
	t.Helper()
	x25519Seed := decodeVectorHex(t, v.X25519SeedHex)
	mlkemSeed := decodeVectorHex(t, v.MlkemKeygenSeedHex)
	encapsulationSeed := decodeVectorHex(t, v.MlkemEncapsulationSeedHex)
	signRandom := decodeVectorHex(t, v.SignRandomHex)
	return entropy{
		x25519Seed: func() ([]byte, error) { return slices.Clone(x25519Seed), nil },
		mlkemSeed:  func() ([]byte, error) { return slices.Clone(mlkemSeed), nil },
		encapsulate: func(ek *mlkem.EncapsulationKey1024) ([]byte, []byte) {
			sharedSecret, ciphertext, err := mlkemtest.Encapsulate1024(ek, encapsulationSeed)
			require.NoError(t, err)
			return sharedSecret, ciphertext
		},
		signRandom: bytes.NewReader(signRandom),
	}
}

func vectorFrameAssociatedData(t *testing.T, channelID string, frame noiseFrameVector) []byte {
	t.Helper()
	correlationID, err := strconv.ParseUint(frame.CorrelationID, 10, 64)
	require.NoError(t, err)
	ad := make([]byte, 12+len(channelID))
	binary.BigEndian.PutUint64(ad[:8], correlationID)
	binary.BigEndian.PutUint32(ad[8:12], frame.Flags)
	copy(ad[12:], channelID)
	return ad
}

func replayHandshakeVector(t *testing.T, v noiseHandshakeVector) (publicKeys noiseKeyPairVector, message1, message2, initiatorCiphertext, responderCiphertext []byte) {
	t.Helper()
	worker, err := RestoreCompositeKeypair(
		decodeVectorHex(t, v.ResponderPrivate.X25519Hex),
		decodeVectorHex(t, v.ResponderPrivate.MlkemSeedHex),
		decodeVectorHex(t, v.ResponderPrivate.SlhdsaHex),
	)
	require.NoError(t, err)
	publicKeys = noiseKeyPairVector{
		X25519Hex: hex.EncodeToString(worker.X25519Public),
		MlkemHex:  hex.EncodeToString(worker.MlkemPublicKeyBytes()),
		SlhdsaHex: hex.EncodeToString(slhdsaPublicKey(t, worker)),
	}
	var hs *HandshakeState
	if v.Mode == "post_quantum" {
		hs, message1, err = initiatorHandshake1(vectorEntropy(t, v.InitiatorEntropy), v.WorkerID, worker.X25519Public, worker.MlkemPublicKeyBytes(), slhdsaPublicKey(t, worker))
		require.NoError(t, err)
		message2, responderSession, err := responderHandshake(vectorEntropy(t, v.ResponderEntropy), worker, v.WorkerID, message1)
		require.NoError(t, err)
		defer responderSession.Zero()
		initiatorSession, err := hs.Finish(message2)
		require.NoError(t, err)
		defer initiatorSession.Zero()
		initiatorCiphertext, err = initiatorSession.Encrypt(vectorFrameAssociatedData(t, v.ChannelID, v.InitiatorFrame), []byte(v.InitiatorFrame.Plaintext))
		require.NoError(t, err)
		responderCiphertext, err = responderSession.Encrypt(vectorFrameAssociatedData(t, v.ChannelID, v.ResponderFrame), []byte(v.ResponderFrame.Plaintext))
		require.NoError(t, err)
		return publicKeys, message1, message2, initiatorCiphertext, responderCiphertext
	}
	require.Equal(t, "classic", v.Mode)
	hs, message1, err = classicalInitiatorHandshake1(vectorEntropy(t, v.InitiatorEntropy), v.WorkerID, worker.X25519Public, worker.MlkemPublicKeyBytes(), slhdsaPublicKey(t, worker))
	require.NoError(t, err)
	message2, responderSession, err := classicalResponderHandshake(vectorEntropy(t, v.ResponderEntropy), worker, v.WorkerID, message1)
	require.NoError(t, err)
	defer responderSession.Zero()
	initiatorSession, err := hs.Finish(message2)
	require.NoError(t, err)
	defer initiatorSession.Zero()
	initiatorCiphertext, err = initiatorSession.Encrypt(vectorFrameAssociatedData(t, v.ChannelID, v.InitiatorFrame), []byte(v.InitiatorFrame.Plaintext))
	require.NoError(t, err)
	responderCiphertext, err = responderSession.Encrypt(vectorFrameAssociatedData(t, v.ChannelID, v.ResponderFrame), []byte(v.ResponderFrame.Plaintext))
	require.NoError(t, err)
	return publicKeys, message1, message2, initiatorCiphertext, responderCiphertext
}

func TestHandshakeVectors(t *testing.T) {
	const path = "../../../testdata/noise_handshake_vectors.json"
	if *updateVectors {
		fixture := sourceHandshakeVectors(t)
		for i := range fixture.Vectors {
			v := &fixture.Vectors[i]
			publicKeys, message1, message2, initiatorCiphertext, responderCiphertext := replayHandshakeVector(t, *v)
			v.ResponderPublic = publicKeys
			v.Message1Hex = hex.EncodeToString(message1)
			v.Message2Hex = hex.EncodeToString(message2)
			v.InitiatorFrame.CiphertextHex = hex.EncodeToString(initiatorCiphertext)
			v.ResponderFrame.CiphertextHex = hex.EncodeToString(responderCiphertext)
		}
		writeNoiseVectorFile(t, path, fixture)
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var fixture noiseHandshakeFixture
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.Len(t, fixture.Vectors, 2)
	for _, v := range fixture.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			publicKeys, message1, message2, initiatorCiphertext, responderCiphertext := replayHandshakeVector(t, v)
			assert.Equal(t, v.ResponderPublic, publicKeys)
			assert.Equal(t, v.Message1Hex, hex.EncodeToString(message1))
			assert.Equal(t, v.Message2Hex, hex.EncodeToString(message2))
			assert.Equal(t, v.InitiatorFrame.CiphertextHex, hex.EncodeToString(initiatorCiphertext))
			assert.Equal(t, v.ResponderFrame.CiphertextHex, hex.EncodeToString(responderCiphertext))
		})
	}
}

type noiseRekeyPQVector struct {
	Name                   string             `json:"name"`
	WorkerMlkemSeedHex     string             `json:"workerMlkemSeedHex"`
	InitiatorEntropy       noiseEntropyVector `json:"initiatorEntropy"`
	ResponderEntropy       noiseEntropyVector `json:"responderEntropy"`
	StaticCiphertextHex    string             `json:"staticCiphertextHex"`
	EphemeralPublicKeyHex  string             `json:"ephemeralPublicKeyHex"`
	EphemeralCiphertextHex string             `json:"ephemeralCiphertextHex"`
	PQSecretHex            string             `json:"pqSecretHex"`
}

type noiseRekeyPQFixture struct {
	Readme    string               `json:"_readme"`
	Exchanges []noiseRekeyPQVector `json:"exchanges"`
}

func sourceRekeyPQVectors() noiseRekeyPQFixture {
	return noiseRekeyPQFixture{
		Readme: "Cross-language post-quantum rekey exchange vectors. Each 64-byte secret is the static ML-KEM shared secret followed by the ephemeral ML-KEM shared secret.",
		Exchanges: []noiseRekeyPQVector{{
			Name:               "static-and-ephemeral-mlkem",
			WorkerMlkemSeedHex: hex.EncodeToString(patternedBytes(64, 71)),
			InitiatorEntropy: noiseEntropyVector{
				MlkemKeygenSeedHex:        hex.EncodeToString(patternedBytes(64, 72)),
				MlkemEncapsulationSeedHex: hex.EncodeToString(patternedBytes(32, 73)),
			},
			ResponderEntropy: noiseEntropyVector{
				MlkemEncapsulationSeedHex: hex.EncodeToString(patternedBytes(32, 74)),
			},
		}},
	}
}

func replayRekeyPQVector(t *testing.T, v noiseRekeyPQVector) (staticCiphertext, ephemeralPublicKey, ephemeralCiphertext, responderSecret, initiatorSecret []byte) {
	t.Helper()
	workerKey, err := mlkem.NewDecapsulationKey1024(decodeVectorHex(t, v.WorkerMlkemSeedHex))
	require.NoError(t, err)
	offer, err := offerRekeyPQ(vectorEntropy(t, v.InitiatorEntropy), workerKey.EncapsulationKey().Bytes())
	require.NoError(t, err)
	staticCiphertext = slices.Clone(offer.StaticCiphertext)
	ephemeralPublicKey = slices.Clone(offer.EphemeralPublicKey)
	responderSecret, ephemeralCiphertext, err = answerRekeyPQ(vectorEntropy(t, v.ResponderEntropy), workerKey, staticCiphertext, ephemeralPublicKey)
	require.NoError(t, err)
	initiatorSecret, err = offer.Accept(ephemeralCiphertext)
	require.NoError(t, err)
	return staticCiphertext, ephemeralPublicKey, ephemeralCiphertext, responderSecret, initiatorSecret
}

func TestRekeyPQVectors(t *testing.T) {
	const path = "../../../testdata/noise_rekey_pq_vectors.json"
	if *updateVectors {
		fixture := sourceRekeyPQVectors()
		for i := range fixture.Exchanges {
			v := &fixture.Exchanges[i]
			staticCiphertext, ephemeralPublicKey, ephemeralCiphertext, responderSecret, initiatorSecret := replayRekeyPQVector(t, *v)
			require.Equal(t, responderSecret, initiatorSecret)
			v.StaticCiphertextHex = hex.EncodeToString(staticCiphertext)
			v.EphemeralPublicKeyHex = hex.EncodeToString(ephemeralPublicKey)
			v.EphemeralCiphertextHex = hex.EncodeToString(ephemeralCiphertext)
			v.PQSecretHex = hex.EncodeToString(responderSecret)
			Zero(responderSecret)
			Zero(initiatorSecret)
		}
		writeNoiseVectorFile(t, path, fixture)
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var fixture noiseRekeyPQFixture
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Exchanges)
	for _, v := range fixture.Exchanges {
		t.Run(v.Name, func(t *testing.T) {
			staticCiphertext, ephemeralPublicKey, ephemeralCiphertext, responderSecret, initiatorSecret := replayRekeyPQVector(t, v)
			defer Zero(responderSecret)
			defer Zero(initiatorSecret)
			assert.Equal(t, responderSecret, initiatorSecret)
			assert.Equal(t, v.StaticCiphertextHex, hex.EncodeToString(staticCiphertext))
			assert.Equal(t, v.EphemeralPublicKeyHex, hex.EncodeToString(ephemeralPublicKey))
			assert.Equal(t, v.EphemeralCiphertextHex, hex.EncodeToString(ephemeralCiphertext))
			assert.Equal(t, v.PQSecretHex, hex.EncodeToString(responderSecret))
		})
	}
}

func writeNoiseVectorFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	require.NoError(t, err)
	data = append(data, '\n')
	require.NoError(t, os.WriteFile(path, data, 0o644))
}
