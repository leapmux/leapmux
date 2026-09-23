package id

import (
	"fmt"
	"math/rand/v2"

	gonanoid "github.com/matoous/go-nanoid/v2"
)

// Generate returns a 48-character nanoid using an alphanumeric alphabet (A-Za-z0-9).
func Generate() string {
	id, err := gonanoid.Generate("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789", 48)
	if err != nil {
		panic(fmt.Sprintf("generate nanoid: %v", err))
	}
	return id
}

// shortAlphabet is the character set of Short: lowercase letters and digits,
// which every consumer of a short id can carry without escaping.
const shortAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// shortLength is the length of a Short id.
const shortLength = 13

// Short returns a 13-character id of lowercase letters and digits.
//
// It is for a value that must be unique within the traffic of one process: a
// request id on a child process's stdin, or a delimiter in a child process's
// output. It is NOT for a value that must be unguessable; use Generate for that,
// which reads a cryptographic source.
func Short() string {
	b := make([]byte, shortLength)
	for i := range b {
		b[i] = shortAlphabet[rand.IntN(len(shortAlphabet))]
	}
	return string(b)
}
