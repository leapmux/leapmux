package keystore

import (
	"bytes"
	"database/sql/driver"
	"errors"
	"fmt"
)

// Ciphertext holds an opaque encrypted value produced by Keystore for a NOT NULL
// database column. Its driver.Valuer and sql.Scanner implementations transport
// raw bytes only; callers must seal and open values through Keystore with
// row-bound AAD.
type Ciphertext struct {
	raw []byte
}

// NullCiphertext holds an opaque encrypted value for a nullable database column.
// When valid is false, Value returns nil (SQL NULL).
type NullCiphertext struct {
	raw   []byte
	valid bool
}

// FromRawCiphertext wraps raw encrypted bytes into a Ciphertext after verifying
// that the slice meets the minimum envelope overhead (version + nonce + tag).
// It clones the input slice.
func FromRawCiphertext(raw []byte) (Ciphertext, error) {
	if len(raw) < overhead {
		return Ciphertext{}, fmt.Errorf("keystore: ciphertext too short: got %d bytes, need at least %d", len(raw), overhead)
	}
	return Ciphertext{raw: bytes.Clone(raw)}, nil
}

// Bytes returns a defensive copy of the underlying ciphertext bytes.
func (c Ciphertext) Bytes() []byte {
	return bytes.Clone(c.raw)
}

// Version returns the 4-byte big-endian key version prefix from the ciphertext.
func (c Ciphertext) Version() (uint32, error) {
	return CiphertextVersion(c.raw)
}

// Value implements driver.Valuer. It rejects an uninitialized zero-value
// Ciphertext so an omitted struct field fails instead of writing empty bytes.
func (c Ciphertext) Value() (driver.Value, error) {
	if len(c.raw) == 0 {
		return nil, errors.New("keystore: refusing to serialize empty Ciphertext")
	}
	return bytes.Clone(c.raw), nil
}

// Scan implements sql.Scanner for non-NULL binary columns.
func (c *Ciphertext) Scan(src any) error {
	if src == nil {
		return errors.New("keystore: cannot scan NULL into non-null Ciphertext")
	}
	b, ok := src.([]byte)
	if !ok {
		return fmt.Errorf("keystore: unsupported scan source %T for Ciphertext", src)
	}
	if len(b) == 0 {
		return errors.New("keystore: cannot scan empty bytes into Ciphertext")
	}
	c.raw = bytes.Clone(b)
	return nil
}

// Valid reports whether the nullable ciphertext holds a non-NULL value.
func (c NullCiphertext) Valid() bool {
	return c.valid
}

// Bytes returns a defensive copy of the underlying ciphertext bytes, or nil
// when the value is SQL NULL.
func (c NullCiphertext) Bytes() []byte {
	if !c.valid {
		return nil
	}
	return bytes.Clone(c.raw)
}

// Version returns the 4-byte big-endian key version prefix from a non-NULL
// ciphertext.
func (c NullCiphertext) Version() (uint32, error) {
	if !c.valid {
		return 0, errors.New("keystore: null ciphertext has no key version")
	}
	return CiphertextVersion(c.raw)
}

// Value implements driver.Valuer. It returns nil for SQL NULL and rejects a
// valid NullCiphertext whose raw slice is empty.
func (c NullCiphertext) Value() (driver.Value, error) {
	if !c.valid {
		return nil, nil
	}
	if len(c.raw) == 0 {
		return nil, errors.New("keystore: refusing to serialize empty valid NullCiphertext")
	}
	return bytes.Clone(c.raw), nil
}

// Scan implements sql.Scanner for nullable binary columns.
func (c *NullCiphertext) Scan(src any) error {
	if src == nil {
		c.raw = nil
		c.valid = false
		return nil
	}
	b, ok := src.([]byte)
	if !ok {
		return fmt.Errorf("keystore: unsupported scan source %T for NullCiphertext", src)
	}
	if len(b) == 0 {
		return errors.New("keystore: cannot scan empty bytes into NullCiphertext")
	}
	c.raw = bytes.Clone(b)
	c.valid = true
	return nil
}

// SealBytes encrypts plain with aad into a non-null Ciphertext.
func (ks *Keystore) SealBytes(plain, aad []byte) (Ciphertext, error) {
	if ks == nil {
		return Ciphertext{}, errors.New("keystore: nil keystore")
	}
	ct, err := ks.Encrypt(plain, aad)
	if err != nil {
		return Ciphertext{}, err
	}
	return Ciphertext{raw: ct}, nil
}

// OpenBytes decrypts ct with aad and returns the plaintext bytes.
// Decryption of an empty plaintext always returns a non-nil empty slice.
func (ks *Keystore) OpenBytes(ct Ciphertext, aad []byte) ([]byte, error) {
	if ks == nil {
		return nil, errors.New("keystore: nil keystore")
	}
	plain, err := ks.Decrypt(ct.raw, aad)
	if err != nil {
		return nil, err
	}
	if plain == nil {
		return []byte{}, nil
	}
	return plain, nil
}

// SealString encrypts plain with aad into a non-null Ciphertext.
func (ks *Keystore) SealString(plain string, aad []byte) (Ciphertext, error) {
	return ks.SealBytes([]byte(plain), aad)
}

// OpenString decrypts ct with aad and returns the plaintext string.
func (ks *Keystore) OpenString(ct Ciphertext, aad []byte) (string, error) {
	plain, err := ks.OpenBytes(ct, aad)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// SealOptionalBytes encrypts plain into a NullCiphertext. A nil slice encodes
// as SQL NULL; a non-nil empty slice ([]byte{}) encrypts as a valid ciphertext.
func (ks *Keystore) SealOptionalBytes(plain, aad []byte) (NullCiphertext, error) {
	if plain == nil {
		return NullCiphertext{}, nil
	}
	ct, err := ks.SealBytes(plain, aad)
	if err != nil {
		return NullCiphertext{}, err
	}
	return NullCiphertext{raw: ct.raw, valid: true}, nil
}

// OpenOptionalBytes decrypts ct with aad. SQL NULL returns (nil, nil).
func (ks *Keystore) OpenOptionalBytes(ct NullCiphertext, aad []byte) ([]byte, error) {
	if !ct.valid {
		return nil, nil
	}
	return ks.OpenBytes(Ciphertext{raw: ct.raw}, aad)
}

// SealOptionalString encrypts plain into a NullCiphertext. A nil pointer
// encodes as SQL NULL; a non-nil pointer (including &"") encrypts as a valid
// ciphertext.
func (ks *Keystore) SealOptionalString(plain *string, aad []byte) (NullCiphertext, error) {
	if plain == nil {
		return NullCiphertext{}, nil
	}
	ct, err := ks.SealString(*plain, aad)
	if err != nil {
		return NullCiphertext{}, err
	}
	return NullCiphertext{raw: ct.raw, valid: true}, nil
}

// OpenOptionalString decrypts ct with aad. SQL NULL returns (nil, nil).
func (ks *Keystore) OpenOptionalString(ct NullCiphertext, aad []byte) (*string, error) {
	if !ct.valid {
		return nil, nil
	}
	s, err := ks.OpenString(Ciphertext{raw: ct.raw}, aad)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// SealNonEmptyString encrypts plain into a NullCiphertext when plain is
// non-empty, or returns SQL NULL when plain is "".
func (ks *Keystore) SealNonEmptyString(plain string, aad []byte) (NullCiphertext, error) {
	if plain == "" {
		return NullCiphertext{}, nil
	}
	return ks.SealOptionalString(&plain, aad)
}

// SealNonEmptyBytes encrypts plain into a NullCiphertext when len(plain) > 0,
// or returns SQL NULL when plain is nil or empty.
func (ks *Keystore) SealNonEmptyBytes(plain, aad []byte) (NullCiphertext, error) {
	if len(plain) == 0 {
		return NullCiphertext{}, nil
	}
	return ks.SealOptionalBytes(plain, aad)
}

// OpenStringOrDefault decrypts ct with aad, returning fallback when ct is SQL NULL.
func (ks *Keystore) OpenStringOrDefault(ct NullCiphertext, aad []byte, fallback string) (string, error) {
	if !ct.valid {
		return fallback, nil
	}
	return ks.OpenString(Ciphertext{raw: ct.raw}, aad)
}
