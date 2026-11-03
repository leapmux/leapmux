// Package workerkeys is the one definition of a Worker's wire public keys: the
// set a Worker registers with, repeats in every WorkerHello, and the Hub pins.
//
// A leaf package on purpose. It imports only the generated protobuf types and
// internal/noise, so the store, the Hub service, the solo launcher and the
// Worker client can all share one definition of the set without pulling one
// another's dependencies in.
package workerkeys

import (
	"bytes"
	"crypto/mlkem"
	"errors"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/noise"
)

// Validate reports whether keys is a complete set of static public keys.
//
// A set is complete or it is nothing: all three keys are the Worker's identity,
// whatever handshake the connection later negotiates, so a partial set is
// always refused. The ML-KEM key is also parsed here, because a correctly sized
// key whose coefficients are out of range is one every post-quantum handshake
// will refuse — and a pin can never change afterwards.
func Validate(keys *leapmuxv1.WorkerPublicKeys) error {
	if keys == nil {
		return errors.New("the worker public keys are missing")
	}
	x25519 := keys.GetX25519()
	if len(x25519) != noise.EphemeralPublicKeySize {
		return fmt.Errorf("worker x25519 public key is %d bytes, want %d", len(x25519), noise.EphemeralPublicKeySize)
	}
	mlkemKey := keys.GetMlkem()
	if len(mlkemKey) != noise.MlkemPublicKeySize {
		return fmt.Errorf("worker mlkem public key is %d bytes, want %d", len(mlkemKey), noise.MlkemPublicKeySize)
	}
	if _, err := mlkem.NewEncapsulationKey1024(mlkemKey); err != nil {
		return fmt.Errorf("worker mlkem public key is not a valid ML-KEM-1024 encapsulation key: %w", err)
	}
	slhdsa := keys.GetSlhdsa()
	if len(slhdsa) != noise.SlhdsaPublicKeySize {
		return fmt.Errorf("worker slhdsa public key is %d bytes, want %d", len(slhdsa), noise.SlhdsaPublicKeySize)
	}
	return nil
}

// Of returns the wire public keys of a composite keypair.
//
// It validates its own result, so its error is real: circl's PublicKey.MarshalBinary
// never fails, and a zero-value key marshals to empty bytes with no error at all.
func Of(ck *noise.CompositeKeypair) (*leapmuxv1.WorkerPublicKeys, error) {
	if ck == nil {
		return nil, errors.New("the worker's composite keypair is missing")
	}
	if ck.MlkemDecapsulationKey == nil {
		return nil, errors.New("the worker's composite keypair has no ML-KEM key")
	}
	slhdsa, err := ck.SlhdsaPublicKeyBytes()
	if err != nil {
		return nil, fmt.Errorf("marshal the worker's slhdsa public key: %w", err)
	}
	keys := &leapmuxv1.WorkerPublicKeys{
		X25519: ck.X25519Public,
		Mlkem:  ck.MlkemPublicKeyBytes(),
		Slhdsa: slhdsa,
	}
	if err := Validate(keys); err != nil {
		return nil, err
	}
	return keys, nil
}

// Equal reports whether two sets hold the same three keys.
//
// It compares the fields, not the messages: proto.Equal also compares unknown
// fields, which would refuse a newer Worker for ever while only three columns
// are stored.
func Equal(a, b *leapmuxv1.WorkerPublicKeys) bool {
	return bytes.Equal(a.GetX25519(), b.GetX25519()) &&
		bytes.Equal(a.GetMlkem(), b.GetMlkem()) &&
		bytes.Equal(a.GetSlhdsa(), b.GetSlhdsa())
}

// Fingerprint returns the four-word fingerprint of the set, the same one the
// Worker logs when it starts.
func Fingerprint(keys *leapmuxv1.WorkerPublicKeys) string {
	return noise.CompositeKeyFingerprint(keys.GetX25519(), keys.GetMlkem(), keys.GetSlhdsa())
}
