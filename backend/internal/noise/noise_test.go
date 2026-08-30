package noise

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandshakeRoundtrip(t *testing.T) {
	// Worker generates composite keypair.
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	slhdsaPubBytes, err := workerKey.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	// Initiator (Frontend) starts handshake.
	hs, msg1, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)
	assert.NotEmpty(t, msg1)

	// Worker (Responder) processes msg1 and returns msg2.
	msg2, workerSession, err := ResponderHandshake(workerKey, msg1)
	require.NoError(t, err)
	assert.NotEmpty(t, msg2)
	assert.NotNil(t, workerSession)

	// Initiator completes handshake.
	initiatorSession, err := InitiatorHandshake2(hs, msg2, slhdsaPubBytes)
	require.NoError(t, err)
	assert.NotNil(t, initiatorSession)

	// Test bidirectional encryption.
	t.Run("initiator_to_responder", func(t *testing.T) {
		plaintext := []byte("hello from initiator")
		ciphertext, err := initiatorSession.Encrypt(plaintext)
		require.NoError(t, err)
		assert.NotEqual(t, plaintext, ciphertext)

		decrypted, err := workerSession.Decrypt(ciphertext)
		require.NoError(t, err)
		assert.Equal(t, plaintext, decrypted)
	})

	t.Run("responder_to_initiator", func(t *testing.T) {
		plaintext := []byte("hello from responder")
		ciphertext, err := workerSession.Encrypt(plaintext)
		require.NoError(t, err)
		assert.NotEqual(t, plaintext, ciphertext)

		decrypted, err := initiatorSession.Decrypt(ciphertext)
		require.NoError(t, err)
		assert.Equal(t, plaintext, decrypted)
	})
}

func TestMultipleMessages(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	slhdsaPubBytes, err := workerKey.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	hs, msg1, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	msg2, workerSession, err := ResponderHandshake(workerKey, msg1)
	require.NoError(t, err)

	initiatorSession, err := InitiatorHandshake2(hs, msg2, slhdsaPubBytes)
	require.NoError(t, err)

	for i := 0; i < 100; i++ {
		msg := []byte(fmt.Sprintf("message %d from initiator", i))
		ct, err := initiatorSession.Encrypt(msg)
		require.NoError(t, err)
		pt, err := workerSession.Decrypt(ct)
		require.NoError(t, err)
		assert.Equal(t, msg, pt)

		msg = []byte(fmt.Sprintf("message %d from responder", i))
		ct, err = workerSession.Encrypt(msg)
		require.NoError(t, err)
		pt, err = initiatorSession.Decrypt(ct)
		require.NoError(t, err)
		assert.Equal(t, msg, pt)
	}
}

func TestWrongMlkemKey(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	slhdsaPubBytes, err := workerKey.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	// Generate a different ML-KEM key.
	wrongKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	// Initiator uses wrong ML-KEM public key — encapsulates to wrong key.
	hs, msg1, err := InitiatorHandshake1(workerKey.X25519Public, wrongKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	// Responder decapsulates with its own key. ML-KEM implicit rejection
	// produces a random shared secret rather than an error, so the
	// responder handshake itself succeeds.
	msg2, _, err := ResponderHandshake(workerKey, msg1)
	require.NoError(t, err)

	// Initiator verification fails because the ML-KEM shared secrets differ,
	// producing different transcripts and thus an SLH-DSA signature mismatch.
	_, err = InitiatorHandshake2(hs, msg2, slhdsaPubBytes)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SLH-DSA")
}

func TestInvalidSlhdsaSignature(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	// Generate a different SLH-DSA key — use its public key for verification.
	wrongKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)
	wrongSlhdsaPubBytes, err := wrongKey.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	hs, msg1, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	msg2, _, err := ResponderHandshake(workerKey, msg1)
	require.NoError(t, err)

	// Initiator uses wrong SLH-DSA public key — signature verification should fail.
	_, err = InitiatorHandshake2(hs, msg2, wrongSlhdsaPubBytes)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SLH-DSA")
}

func TestEmptyMessage(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	slhdsaPubBytes, err := workerKey.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	hs, msg1, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	msg2, workerSession, err := ResponderHandshake(workerKey, msg1)
	require.NoError(t, err)

	initiatorSession, err := InitiatorHandshake2(hs, msg2, slhdsaPubBytes)
	require.NoError(t, err)

	ct, err := initiatorSession.Encrypt([]byte{})
	require.NoError(t, err)
	assert.NotEmpty(t, ct) // Ciphertext includes auth tag.

	pt, err := workerSession.Decrypt(ct)
	require.NoError(t, err)
	assert.Empty(t, pt)
}

func TestPlaintextSizeLimit(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	slhdsaPubBytes, err := workerKey.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	hs, msg1, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	msg2, _, err := ResponderHandshake(workerKey, msg1)
	require.NoError(t, err)

	session, err := InitiatorHandshake2(hs, msg2, slhdsaPubBytes)
	require.NoError(t, err)

	// Exactly at limit should succeed.
	atLimit := make([]byte, MaxPlaintextSize)
	_, err = session.Encrypt(atLimit)
	require.NoError(t, err)

	// One byte over limit should fail.
	overLimit := make([]byte, MaxPlaintextSize+1)
	_, err = session.Encrypt(overLimit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plaintext too large")
}

func TestNeedsRekey(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	slhdsaPubBytes, err := workerKey.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	hs, msg1, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	msg2, _, err := ResponderHandshake(workerKey, msg1)
	require.NoError(t, err)

	session, err := InitiatorHandshake2(hs, msg2, slhdsaPubBytes)
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
		ct, encErr := peer.Encrypt(p)
		require.NoError(t, encErr)
		_, decErr := cs.Decrypt(ct)
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
	ctStraddle, err := oldKey.Encrypt([]byte("straddle"))
	require.NoError(t, err)
	pt, err := cs.Decrypt(ctStraddle)
	require.NoError(t, err, "in-flight old-key frame must decrypt via the grace window")
	assert.Equal(t, []byte("straddle"), pt)

	// New-epoch traffic round-trips across the pair once both have rotated.
	peer.rekeyWithSecret(peerDH[:], nil, true)
	assert.Equal(t, cs.k, peer.k, "both sides must derive the same new key from shared DH entropy")
	ctNew, err := cs.Encrypt([]byte("after"))
	require.NoError(t, err)
	assert.Equal(t, uint64(1), cs.Nonce())
	pt, err = peer.Decrypt(ctNew)
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
	ctStraddle, err := oldKey.Encrypt([]byte("straddle"))
	require.NoError(t, err)

	// Within the window, the old-key frame decrypts via prev.
	pt, err := cs.Decrypt(ctStraddle)
	require.NoError(t, err)
	assert.Equal(t, []byte("straddle"), pt)

	// Force expiry: push prevExpiresAt into the past. A fresh old-key frame
	// (nonce 1, since prev already consumed 0) must now fail closed.
	cs.prevExpiresAt = time.Now().Add(-time.Second)
	ctStraddle2, err := oldKey.Encrypt([]byte("straddle2"))
	require.NoError(t, err)
	_, err = cs.Decrypt(ctStraddle2)
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
	ctStraddle, err := oldKey.Encrypt([]byte("straddle"))
	require.NoError(t, err)
	_, err = cs.Decrypt(ctStraddle)
	require.Error(t, err, "send direction must not decrypt old-key frames (no grace window)")
}

func TestSessionNeedsRekeyEither(t *testing.T) {
	send := &CipherState{k: [keyLen]byte{1}}
	recv := &CipherState{k: [keyLen]byte{2}}
	session := &Session{Send: send, Receive: recv}

	assert.False(t, session.NeedsRekeyEither())

	send.SetNonceForTest(SoftNonceLimit + 1)
	assert.True(t, session.NeedsRekeyEither(), "send soft nonce must trip either")

	send.SetNonceForTest(0)
	recv.SetNonceForTest(SoftNonceLimit + 1)
	assert.True(t, session.NeedsRekeyEither(), "receive soft nonce must trip either")
}

func TestCipherStateRekeyMatchesHKDF(t *testing.T) {
	// Fixed vectors from testdata/noise_rekey_vectors.json, shared with
	// frontend/src/lib/noise.test.ts so Go and TS stay on the same fresh-DH
	// rekey derivation. The rekey mixes a fresh X25519 DH secret (and
	// optional ML-KEM secret) into the current key:
	//
	//	ck1 = HKDF(k, dhSecret)
	//	k'  = HKDF(ck1, pqSecret)[0:32]   (pqSecret == nil here ⇒ degenerate 2nd stage)
	data, err := os.ReadFile("../../../testdata/noise_rekey_vectors.json")
	require.NoError(t, err, "the rekey vectors are shared with frontend/src/lib/noise.test.ts")
	var fixture struct {
		Vectors []struct {
			Name                  string `json:"name"`
			KeyHex                string `json:"keyHex"`
			DhSecretHex           string `json:"dhSecretHex"`
			WarmupPlaintext       string `json:"warmupPlaintext"`
			Plaintext             string `json:"plaintext"`
			ExpectedCiphertextHex string `json:"expectedCiphertextHex"`
		} `json:"vectors"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Vectors)

	for _, v := range fixture.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			keyBytes, err := hex.DecodeString(v.KeyHex)
			require.NoError(t, err)
			require.Len(t, keyBytes, keyLen)
			dhBytes, err := hex.DecodeString(v.DhSecretHex)
			require.NoError(t, err)
			require.Len(t, dhBytes, dhLen)
			var key [keyLen]byte
			copy(key[:], keyBytes)
			var dhSecret [dhLen]byte
			copy(dhSecret[:], dhBytes)

			// Compute the expected derived key from a separate copy: rekeyWithSecret
			// zeroes its dhSecret input, so deriving afterwards would use all-zeros.
			ck1, _ := noiseHKDF(key[:], dhSecret[:])
			expectedK, _ := noiseHKDF(ck1[:], nil)
			var want [keyLen]byte
			copy(want[:], expectedK[:keyLen])

			cs := &CipherState{k: key}
			_, err = cs.Encrypt([]byte(v.WarmupPlaintext)) // nonce advances, but rekey resets it
			require.NoError(t, err)
			cs.rekeyWithSecret(dhSecret[:], nil, true)
			assert.Equal(t, want, cs.k)

			ct, err := cs.Encrypt([]byte(v.Plaintext))
			require.NoError(t, err)
			assert.Equal(t, v.ExpectedCiphertextHex, hex.EncodeToString(ct),
				"post-rekey ciphertext must match the TypeScript interop vector")

			peer := &CipherState{k: want}
			pt, err := peer.Decrypt(ct)
			require.NoError(t, err)
			assert.Equal(t, []byte(v.Plaintext), pt)
		})
	}
}

func TestTamperedMlkemCiphertextCausesAEADFailure(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	_, msg1, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	// Tamper with the ML-KEM ciphertext portion of message1.
	// The classical Noise part (first 48 bytes) is untouched.
	tampered := make([]byte, len(msg1))
	copy(tampered, msg1)
	tampered[48] ^= 0xFF // flip one byte in the ML-KEM ciphertext

	// Responder should fail because the tampered mlkemCT changes the handshake
	// hash (via mixHash), causing the message2 AEAD to use a different AD than
	// what the initiator expects. This proves mlkemCT is bound into the
	// Noise state, not just the SLH-DSA transcript.
	// Note: ML-KEM implicit rejection means Decapsulate doesn't error on
	// tampered ciphertext — it produces a random shared secret instead.
	// The failure comes from the divergent handshake hash.
	_, _, err = ResponderHandshake(workerKey, tampered)
	// The responder handshake itself succeeds (ML-KEM implicit rejection),
	// but when the initiator tries to verify, the handshake hashes differ.
	// Let's verify from the initiator's perspective instead:
	require.NoError(t, err) // Responder doesn't detect the tampering

	// Re-do the handshake properly and verify the initiator would fail.
	hs, msg1Good, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	// Tamper with message1 before sending to responder.
	tamperedMsg1 := make([]byte, len(msg1Good))
	copy(tamperedMsg1, msg1Good)
	tamperedMsg1[48] ^= 0xFF

	slhdsaPubBytes, err := workerKey.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	msg2, _, err := ResponderHandshake(workerKey, tamperedMsg1)
	require.NoError(t, err) // Responder succeeds due to ML-KEM implicit rejection

	// Initiator fails because handshake hashes diverged (mixHash(mlkemCT) differs).
	_, err = InitiatorHandshake2(hs, msg2, slhdsaPubBytes)
	require.Error(t, err, "initiator should detect tampered mlkemCT via handshake hash divergence")
}

func TestClassicalHandshakeRoundtrip(t *testing.T) {
	// Worker generates composite keypair (only X25519 used for classical).
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	// Initiator starts classical handshake.
	hs, msg1, err := ClassicalInitiatorHandshake1(workerKey.X25519Public)
	require.NoError(t, err)
	assert.Equal(t, 48, len(msg1), "classical message1 should be 48 bytes")

	// Worker processes msg1 and returns msg2.
	msg2, workerSession, err := ClassicalResponderHandshake(workerKey.X25519Public, workerKey.X25519Private, msg1)
	require.NoError(t, err)
	assert.Equal(t, 48, len(msg2), "classical message2 should be 48 bytes")
	assert.NotNil(t, workerSession)

	// Initiator completes handshake.
	initiatorSession, err := ClassicalInitiatorHandshake2(hs, msg2)
	require.NoError(t, err)
	assert.NotNil(t, initiatorSession)

	// Test bidirectional encryption.
	t.Run("initiator_to_responder", func(t *testing.T) {
		plaintext := []byte("hello from initiator (classical)")
		ciphertext, err := initiatorSession.Encrypt(plaintext)
		require.NoError(t, err)
		assert.NotEqual(t, plaintext, ciphertext)

		decrypted, err := workerSession.Decrypt(ciphertext)
		require.NoError(t, err)
		assert.Equal(t, plaintext, decrypted)
	})

	t.Run("responder_to_initiator", func(t *testing.T) {
		plaintext := []byte("hello from responder (classical)")
		ciphertext, err := workerSession.Encrypt(plaintext)
		require.NoError(t, err)
		assert.NotEqual(t, plaintext, ciphertext)

		decrypted, err := initiatorSession.Decrypt(ciphertext)
		require.NoError(t, err)
		assert.Equal(t, plaintext, decrypted)
	})
}

func TestClassicalMultipleMessages(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	hs, msg1, err := ClassicalInitiatorHandshake1(workerKey.X25519Public)
	require.NoError(t, err)

	msg2, workerSession, err := ClassicalResponderHandshake(workerKey.X25519Public, workerKey.X25519Private, msg1)
	require.NoError(t, err)

	initiatorSession, err := ClassicalInitiatorHandshake2(hs, msg2)
	require.NoError(t, err)

	for i := 0; i < 100; i++ {
		msg := []byte(fmt.Sprintf("classical message %d from initiator", i))
		ct, err := initiatorSession.Encrypt(msg)
		require.NoError(t, err)
		pt, err := workerSession.Decrypt(ct)
		require.NoError(t, err)
		assert.Equal(t, msg, pt)

		msg = []byte(fmt.Sprintf("classical message %d from responder", i))
		ct, err = workerSession.Encrypt(msg)
		require.NoError(t, err)
		pt, err = initiatorSession.Decrypt(ct)
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
	hs, msg1, err := ClassicalInitiatorHandshake1(workerKey.X25519Public)
	require.NoError(t, err)

	msg2, _, err := ClassicalResponderHandshake(wrongKey.X25519Public, wrongKey.X25519Private, msg1)
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
	assert.Equal(t, uint64(2147483647), SoftNonceLimit, "SoftNonceLimit should be 2^31-1")
	assert.Equal(t, uint64(4294967295), HardNonceLimit, "HardNonceLimit should be 2^32-1")
	// MaxPlaintextSize now aliases contracts.MaxPlaintextPerChunk; the pin
	// keeps the frozen derivation (65535 ciphertext cap - 16 tag bytes) red if
	// wire.json retunes either primitive without this suite noticing.
	assert.Equal(t, 65519, MaxPlaintextSize, "MaxPlaintextSize should be contracts.MaxPlaintextPerChunk (65535-16)")
}

func TestMessageSizes(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	hs, msg1, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	// message1 = noise_msg1 (48) + mlkem_ciphertext (1568) = 1616
	assert.Equal(t, 1616, len(msg1), "message1 size")

	msg2, _, err := ResponderHandshake(workerKey, msg1)
	require.NoError(t, err)

	// message2 = noise_msg2 (48) + slhdsa_signature (49856) = 49904
	assert.Equal(t, 49904, len(msg2), "message2 size")
	_ = hs
}

func TestHandshakeStateZeroedAfterHybrid(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	slhdsaPubBytes, err := workerKey.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	hs, msg1, err := InitiatorHandshake1(workerKey.X25519Public, workerKey.MlkemPublicKeyBytes())
	require.NoError(t, err)

	// Verify sensitive material exists before completing handshake.
	assert.NotNil(t, hs.ss, "symmetric state should exist before handshake2")
	assert.NotEmpty(t, hs.mlkemSS, "mlkemSS should be set before handshake2")
	assert.NotEmpty(t, hs.mlkemCT, "mlkemCT should be set before handshake2")

	msg2, _, err := ResponderHandshake(workerKey, msg1)
	require.NoError(t, err)

	_, err = InitiatorHandshake2(hs, msg2, slhdsaPubBytes)
	require.NoError(t, err)

	// After handshake2 completes, HandshakeState should be zeroed.
	assert.Nil(t, hs.ss, "symmetric state should be nil after handshake2")
	assert.Nil(t, hs.ePriv, "ephemeral private key should be nil after handshake2")
	assert.Equal(t, make([]byte, len(hs.mlkemSS)), hs.mlkemSS, "mlkemSS should be zeroed after handshake2")
	assert.Equal(t, make([]byte, len(hs.mlkemCT)), hs.mlkemCT, "mlkemCT should be zeroed after handshake2")
}

func TestHandshakeStateZeroedAfterClassical(t *testing.T) {
	workerKey, err := GenerateCompositeKeypair()
	require.NoError(t, err)

	hs, msg1, err := ClassicalInitiatorHandshake1(workerKey.X25519Public)
	require.NoError(t, err)

	assert.NotNil(t, hs.ss, "symmetric state should exist before handshake2")

	msg2, _, err := ClassicalResponderHandshake(workerKey.X25519Public, workerKey.X25519Private, msg1)
	require.NoError(t, err)

	_, err = ClassicalInitiatorHandshake2(hs, msg2)
	require.NoError(t, err)

	assert.Nil(t, hs.ss, "symmetric state should be nil after classical handshake2")
	assert.Nil(t, hs.ePriv, "ephemeral private key should be nil after classical handshake2")
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
