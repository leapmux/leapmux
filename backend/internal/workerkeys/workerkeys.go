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
	"errors"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/noise"
)

// Validate reports whether keys is a complete set of static public keys.
//
// A set is complete or it is nothing: all three keys are the Worker's identity,
// whatever handshake the connection later negotiates, so a partial set is
// always refused. A correctly sized key can still be one that every handshake
// refuses, and a pin can never change afterwards.
// noise.ValidateStaticPublicKeys states what the handshake requires of each key.
func Validate(keys *leapmuxv1.WorkerPublicKeys) error {
	if keys == nil {
		return errors.New("the worker public keys are missing")
	}
	return noise.ValidateStaticPublicKeys(keys.GetX25519(), keys.GetMlkem(), keys.GetSlhdsa())
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
// The key set is the three keys, and an unknown field is not part of it, so
// Equal compares the three fields, not the messages: proto.Equal also compares
// unknown fields.
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
