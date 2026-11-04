// Package workerkeystest is the shared WorkerPublicKeys fixture for tests. It
// ends in "test" so TestShippedBinaryLinksNoTestSupport keeps it out of the
// shipped binary, and it lives beside workerkeys so workermgrtest does not have
// to import storetest's dependencies.
package workerkeystest

import (
	"bytes"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/workerkeys"
)

// SeededWorkerFill is the fill byte most fixtures use. It packs the alternating
// ML-KEM coefficients 0x242 and 0x424, both below the 3329 modulus, so the set
// is valid.
const SeededWorkerFill = 0x42

// Keys returns a complete, correctly sized key set whose bytes all equal fill.
//
// It panics when the set fails workerkeys.Validate: a fill byte with a nibble
// above 0xC packs an ML-KEM coefficient above the modulus 3329, and a clear
// panic at the fixture beats a confusing refusal in some later assertion.
func Keys(fill byte) *leapmuxv1.WorkerPublicKeys {
	keys := &leapmuxv1.WorkerPublicKeys{
		X25519: bytes.Repeat([]byte{fill}, contracts.X25519PublicKeySize),
		Mlkem:  bytes.Repeat([]byte{fill}, contracts.MlkemPublicKeySize),
		Slhdsa: bytes.Repeat([]byte{fill}, contracts.SlhdsaPublicKeySize),
	}
	if err := workerkeys.Validate(keys); err != nil {
		panic(fmt.Sprintf("workerkeystest: fill byte %#x does not make a valid key set: %v", fill, err))
	}
	return keys
}
