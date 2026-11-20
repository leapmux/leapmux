package tunnel

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/workerkeys"
)

// WorkerKeyMismatchError reports that the Worker keys the Hub announced are
// not the expected set. The two fingerprints are the human-comparison form of
// the sets (the same four-word fingerprint the Worker logs); they are never
// compared themselves -- the full key sets are. The Hub's announcement is what
// the presented side describes.
type WorkerKeyMismatchError struct {
	ExpectedFingerprint  string
	PresentedFingerprint string
}

func (e *WorkerKeyMismatchError) Error() string {
	return fmt.Sprintf(
		"worker key mismatch: expected %s, the hub announced %s",
		e.ExpectedFingerprint, e.PresentedFingerprint)
}

// ExpectedWorkerKeyPin is a KeyPinStore for one fixed expected Worker key set.
// It is the trust a tunnel request carries in: the caller has already decided
// which keys the Worker must present, so a matching announcement is accepted
// and a different one is refused.
//
// It is a stateless immutable pin policy -- it holds only the expected keys and
// records nothing -- so a matching Resolve returns a nil commit. There is no
// first contact to persist: the expectation came with the request.
type ExpectedWorkerKeyPin struct {
	keys *leapmuxv1.WorkerPublicKeys
}

// KeyPinStore is implemented by *ExpectedWorkerKeyPin.
var _ KeyPinStore = (*ExpectedWorkerKeyPin)(nil)

// NewExpectedWorkerKeyPin decodes one exact composite Worker key set from hex
// and returns the pin that accepts only that set. The composite is the three
// public keys concatenated (x25519 || mlkem || slhdsa), split here at the
// generated key-size constants: any other length is a different encoding, not
// a key set, and is refused rather than sliced into whatever it holds.
//
// The decoded set goes through workerkeys.Validate, so a correctly sized but
// unusable key (a low-order X25519 point, an ML-KEM key that will not parse)
// fails here instead of failing every handshake until the pin is removed.
func NewExpectedWorkerKeyPin(compositeHex string) (*ExpectedWorkerKeyPin, error) {
	raw, err := hex.DecodeString(compositeHex)
	if err != nil {
		return nil, fmt.Errorf("decode the expected worker keys: %w", err)
	}
	total := contracts.X25519PublicKeySize + contracts.MlkemPublicKeySize + contracts.SlhdsaPublicKeySize
	if len(raw) != total {
		return nil, fmt.Errorf("the expected worker keys are %d bytes, want %d", len(raw), total)
	}
	keys := &leapmuxv1.WorkerPublicKeys{
		X25519: raw[:contracts.X25519PublicKeySize],
		Mlkem:  raw[contracts.X25519PublicKeySize : contracts.X25519PublicKeySize+contracts.MlkemPublicKeySize],
		Slhdsa: raw[contracts.X25519PublicKeySize+contracts.MlkemPublicKeySize:],
	}
	if err := workerkeys.Validate(keys); err != nil {
		return nil, fmt.Errorf("the expected worker keys are unusable: %w", err)
	}
	return &ExpectedWorkerKeyPin{keys: keys}, nil
}

// Resolve reports whether the announced keys are the expected set.
//
// OpenChannel validates the announcement before it reaches Resolve, so this
// compares the full sets -- all three keys, never the fingerprints -- and
// returns a WorkerKeyMismatchError naming both fingerprints when they differ.
// A match returns a nil commit: this pin writes nothing.
func (p *ExpectedWorkerKeyPin) Resolve(ctx context.Context, workerID string, publicKey, mlkemPublicKey, slhdsaPublicKey []byte) (func(context.Context) error, error) {
	presented := &leapmuxv1.WorkerPublicKeys{
		X25519: publicKey,
		Mlkem:  mlkemPublicKey,
		Slhdsa: slhdsaPublicKey,
	}
	if workerkeys.Equal(p.keys, presented) {
		return nil, nil
	}
	return nil, &WorkerKeyMismatchError{
		ExpectedFingerprint: noiseutil.CompositeKeyFingerprint(
			p.keys.GetX25519(), p.keys.GetMlkem(), p.keys.GetSlhdsa()),
		PresentedFingerprint: noiseutil.CompositeKeyFingerprint(
			publicKey, mlkemPublicKey, slhdsaPublicKey),
	}
}
