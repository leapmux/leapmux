package keystore

import (
	"bufio"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	// nonceSize is the XChaCha20-Poly1305 nonce size (24 bytes).
	nonceSize = chacha20poly1305.NonceSizeX
	// keySize is the encryption key size (32 bytes). The token pepper uses
	// the same size.
	keySize = chacha20poly1305.KeySize
	// versionSize is the key version prefix size (4 bytes, big-endian uint32).
	versionSize = 4
	// overhead is the minimum ciphertext overhead: version + nonce + Poly1305 tag.
	overhead = versionSize + nonceSize + chacha20poly1305.Overhead
	// pepperTag is the reserved line tag for the dedicated token pepper in
	// the key-ring file. It can never collide with a numeric key version.
	pepperTag = "pepper"
)

// AAD helper functions for building additional authenticated data.
// Using consistent AAD is critical — a mismatch causes decryption failure.

// ProviderAAD returns the AAD for an OAuth provider's client secret.
func ProviderAAD(providerID string) []byte {
	return []byte("oauth_provider:" + providerID)
}

// AccessTokenAAD returns the AAD for a user's OAuth access token.
func AccessTokenAAD(userID, providerID string) []byte {
	return []byte("access_token:" + userID + ":" + providerID)
}

// RefreshTokenAAD returns the AAD for a user's OAuth refresh token.
func RefreshTokenAAD(userID, providerID string) []byte {
	return []byte("refresh_token:" + userID + ":" + providerID)
}

// Keystore manages a versioned key ring for XChaCha20-Poly1305 envelope
// encryption plus a dedicated, stable pepper for bearer-token hashing.
type Keystore struct {
	keys          map[uint32][keySize]byte
	aeads         map[uint32]cipher.AEAD
	activeVersion uint32
	// pepper is a standalone HMAC key used to hash api_token /
	// delegation_token secrets. It is INDEPENDENT of the encryption key
	// ring: rotating encryption keys never changes it, so existing tokens
	// keep validating across rotations. It is regenerated (which
	// invalidates every token) only via RegeneratePepper.
	pepper    [keySize]byte
	hasPepper bool
}

// New creates a Keystore from a key ring map. The highest version becomes the
// active key used for new encryptions. AEAD ciphers are pre-computed for each
// key version to avoid repeated key schedule expansion on every call.
//
// New does not set a pepper; file-based loaders (LoadFromFile / LoadOrGenerate)
// populate it from the key-ring file.
func New(keys map[uint32][keySize]byte) (*Keystore, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("keystore: key ring is empty")
	}
	aeads := make(map[uint32]cipher.AEAD, len(keys))
	var maxVer uint32
	for v, key := range keys {
		aead, err := chacha20poly1305.NewX(key[:])
		if err != nil {
			return nil, fmt.Errorf("keystore: create AEAD for version %d: %w", v, err)
		}
		aeads[v] = aead
		if v > maxVer {
			maxVer = v
		}
	}
	return &Keystore{keys: keys, aeads: aeads, activeVersion: maxVer}, nil
}

// ActiveVersion returns the active (highest) key version.
func (ks *Keystore) ActiveVersion() uint32 { return ks.activeVersion }

// Pepper returns the dedicated HMAC pepper used to hash bearer-token secrets
// (api_token / delegation_token). It is a standalone secret, independent of
// the encryption key ring, so rotating or removing encryption keys never
// changes it. Regenerate it — which invalidates every existing token — via
// RegeneratePepper.
func (ks *Keystore) Pepper() [keySize]byte { return ks.pepper }

// Versions returns all key versions in the ring, sorted ascending.
func (ks *Keystore) Versions() []uint32 {
	return sortedVersions(ks.keys)
}

// sortedVersions returns the map keys sorted ascending.
func sortedVersions(keys map[uint32][keySize]byte) []uint32 {
	vers := make([]uint32, 0, len(keys))
	for v := range keys {
		vers = append(vers, v)
	}
	sort.Slice(vers, func(i, j int) bool { return vers[i] < vers[j] })
	return vers
}

// Encrypt encrypts plaintext with the active key version using XChaCha20-Poly1305.
// The AAD (additional authenticated data) is bound to the ciphertext but not stored in it.
// Returns: [4-byte big-endian version][24-byte nonce][ciphertext + Poly1305 tag].
func (ks *Keystore) Encrypt(plaintext, aad []byte) ([]byte, error) {
	aead := ks.aeads[ks.activeVersion]

	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("keystore: generate nonce: %w", err)
	}

	// Allocate output: version + nonce + ciphertext.
	out := make([]byte, versionSize+nonceSize, versionSize+nonceSize+len(plaintext)+chacha20poly1305.Overhead)
	binary.BigEndian.PutUint32(out[:versionSize], ks.activeVersion)
	copy(out[versionSize:], nonce)
	out = aead.Seal(out, nonce, plaintext, aad)
	return out, nil
}

// CiphertextVersion extracts the key version from a ciphertext blob without decrypting it.
func CiphertextVersion(ciphertext []byte) (uint32, error) {
	if len(ciphertext) < versionSize {
		return 0, fmt.Errorf("keystore: ciphertext too short")
	}
	return binary.BigEndian.Uint32(ciphertext[:versionSize]), nil
}

// Decrypt decrypts a ciphertext blob produced by Encrypt. The same AAD used
// during encryption must be provided.
func (ks *Keystore) Decrypt(ciphertext, aad []byte) ([]byte, error) {
	if len(ciphertext) < overhead {
		return nil, fmt.Errorf("keystore: ciphertext too short")
	}

	version := binary.BigEndian.Uint32(ciphertext[:versionSize])
	aead, ok := ks.aeads[version]
	if !ok {
		return nil, fmt.Errorf("keystore: unknown key version %d", version)
	}

	nonce := ciphertext[versionSize : versionSize+nonceSize]
	ct := ciphertext[versionSize+nonceSize:]
	plaintext, err := aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, fmt.Errorf("keystore: decrypt: %w", err)
	}
	return plaintext, nil
}

// LoadOrGenerate loads a key ring from the file at path, or generates a new
// key ring (a single version-1 key plus a fresh token pepper) if the file does
// not exist. The file is created with mode 0600. An existing key-ring file that
// predates the pepper (no pepper line) is seeded with a fresh pepper in place.
func LoadOrGenerate(path string) (*Keystore, error) {
	// Try to open exclusively to avoid TOCTOU race between Stat and WriteFile.
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("keystore: create directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		// File didn't exist — generate a new key ring with a pepper.
		key, genErr := GenerateKey()
		if genErr != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return nil, genErr
		}
		pepper, pepErr := GenerateKey()
		if pepErr != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return nil, pepErr
		}
		encoded := "1:" + base64.StdEncoding.EncodeToString(key[:]) + "\n" +
			pepperTag + ":" + base64.StdEncoding.EncodeToString(pepper[:]) + "\n"
		if _, writeErr := f.WriteString(encoded); writeErr != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return nil, fmt.Errorf("keystore: write %s: %w", path, writeErr)
		}
		_ = f.Close()
	} else if !os.IsExist(err) {
		return nil, fmt.Errorf("keystore: create %s: %w", path, err)
	}

	ks, err := LoadFromFile(path)
	if err != nil {
		return nil, err
	}
	// Seed a pepper in place for legacy key-ring files written before the
	// dedicated pepper existed, so it is stable from here on.
	if !ks.hasPepper {
		pepper, genErr := GenerateKey()
		if genErr != nil {
			return nil, genErr
		}
		ks.pepper = pepper
		ks.hasPepper = true
		if err := writeKeyRing(path, ks.keys, ks.pepper); err != nil {
			return nil, err
		}
	}
	return ks, nil
}

// LoadFromFile loads a key ring from a file. Each line is either
// "version:base64key" (an encryption key) or "pepper:base64key" (the dedicated
// token pepper). Blank lines and lines beginning with "#" are ignored.
func LoadFromFile(path string) (*Keystore, error) {
	keys, pepper, hasPepper, err := loadRing(path)
	if err != nil {
		return nil, err
	}
	ks, err := New(keys)
	if err != nil {
		return nil, err
	}
	if hasPepper {
		ks.pepper = pepper
		ks.hasPepper = true
	}
	return ks, nil
}

// loadRing parses the key-ring file into the encryption key map plus the
// optional token pepper. hasPepper reports whether a pepper line was present.
func loadRing(path string) (map[uint32][keySize]byte, [keySize]byte, bool, error) {
	var pepper [keySize]byte
	hasPepper := false

	f, err := os.Open(path)
	if err != nil {
		return nil, pepper, false, fmt.Errorf("keystore: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	keys := make(map[uint32][keySize]byte)
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return nil, pepper, false, fmt.Errorf("keystore: %s:%d: expected version:base64key", path, lineNum)
		}

		decoded, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return nil, pepper, false, fmt.Errorf("keystore: %s:%d: invalid base64: %w", path, lineNum, err)
		}
		if len(decoded) != keySize {
			return nil, pepper, false, fmt.Errorf("keystore: %s:%d: key must be %d bytes, got %d", path, lineNum, keySize, len(decoded))
		}

		if parts[0] == pepperTag {
			if hasPepper {
				return nil, pepper, false, fmt.Errorf("keystore: %s:%d: duplicate pepper", path, lineNum)
			}
			copy(pepper[:], decoded)
			if pepper == ([keySize]byte{}) {
				return nil, pepper, false, fmt.Errorf("keystore: %s:%d: pepper must not be all-zero", path, lineNum)
			}
			hasPepper = true
			continue
		}

		ver, err := strconv.ParseUint(parts[0], 10, 32)
		if err != nil || ver == 0 {
			return nil, pepper, false, fmt.Errorf("keystore: %s:%d: invalid version %q (must be 1-%d)", path, lineNum, parts[0], ^uint32(0))
		}
		version := uint32(ver)
		if _, exists := keys[version]; exists {
			return nil, pepper, false, fmt.Errorf("keystore: %s:%d: duplicate version %d", path, lineNum, version)
		}

		var key [keySize]byte
		copy(key[:], decoded)
		keys[version] = key
	}
	if err := scanner.Err(); err != nil {
		return nil, pepper, false, fmt.Errorf("keystore: read %s: %w", path, err)
	}

	return keys, pepper, hasPepper, nil
}

// GenerateKey generates a cryptographically random 256-bit key.
func GenerateKey() ([keySize]byte, error) {
	var key [keySize]byte
	if _, err := rand.Read(key[:]); err != nil {
		return key, fmt.Errorf("keystore: generate key: %w", err)
	}
	return key, nil
}

// RotateKey adds a new key to the ring file at path. Returns the new version
// number. The token pepper is preserved unchanged across rotation.
func RotateKey(path string) (uint32, error) {
	ks, err := LoadFromFile(path)
	if err != nil {
		return 0, err
	}

	newVersion := ks.activeVersion + 1
	if newVersion == 0 {
		return 0, fmt.Errorf("keystore: maximum key version (%d) reached", ^uint32(0))
	}

	newKey, err := GenerateKey()
	if err != nil {
		return 0, err
	}

	ks.keys[newVersion] = newKey
	ks.activeVersion = newVersion

	if err := ks.ensurePepper(); err != nil {
		return 0, err
	}
	if err := writeKeyRing(path, ks.keys, ks.pepper); err != nil {
		return 0, err
	}
	return newVersion, nil
}

// RemoveKey removes a key version from the ring file at path. The token pepper
// is preserved unchanged.
//
// RemoveKey only guards against removing the active version; it has no
// visibility into the database, so callers that can reach the store should
// first verify no ciphertext still depends on the version (see the admin
// encryption-key remove command).
func RemoveKey(path string, version uint32) error {
	ks, err := LoadFromFile(path)
	if err != nil {
		return err
	}

	if version == ks.activeVersion {
		return fmt.Errorf("keystore: cannot remove active key version %d", version)
	}

	if _, exists := ks.keys[version]; !exists {
		return fmt.Errorf("keystore: key version %d not in ring", version)
	}

	delete(ks.keys, version)
	if err := ks.ensurePepper(); err != nil {
		return err
	}
	return writeKeyRing(path, ks.keys, ks.pepper)
}

// RegeneratePepper replaces the token pepper in the ring file at path with a
// fresh random pepper, leaving the encryption key ring untouched. This
// INVALIDATES every existing api_token and delegation_token (their HMAC
// hashes can no longer be reproduced), so all clients must re-authenticate or
// be re-issued. The change takes effect on the next hub start.
func RegeneratePepper(path string) error {
	keys, _, _, err := loadRing(path)
	if err != nil {
		return err
	}
	pepper, err := GenerateKey()
	if err != nil {
		return err
	}
	return writeKeyRing(path, keys, pepper)
}

// ensurePepper generates and sets a pepper if one is not already present.
// Used by RotateKey / RemoveKey so rewriting a legacy ring file (no pepper
// line) seeds one rather than writing an all-zero pepper.
func (ks *Keystore) ensurePepper() error {
	if ks.hasPepper {
		return nil
	}
	pepper, err := GenerateKey()
	if err != nil {
		return err
	}
	ks.pepper = pepper
	ks.hasPepper = true
	return nil
}

// writeKeyRing writes the key ring (encryption versions plus the token pepper)
// to a file with mode 0600.
func writeKeyRing(path string, keys map[uint32][keySize]byte, pepper [keySize]byte) error {
	// Defense in depth: never persist an all-zero pepper. Every caller passes
	// a freshly generated pepper (or one loaded from disk), so this should be
	// unreachable — but enforcing it at the single write choke point means a
	// future ring-mutating helper that forgets to seed the pepper fails loudly
	// instead of silently signing every token with a publicly-known key.
	if pepper == ([keySize]byte{}) {
		return fmt.Errorf("keystore: refusing to write all-zero pepper")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("keystore: create directory: %w", err)
	}

	versions := sortedVersions(keys)

	var sb strings.Builder
	for _, v := range versions {
		key := keys[v]
		sb.WriteString(strconv.FormatUint(uint64(v), 10))
		sb.WriteByte(':')
		sb.WriteString(base64.StdEncoding.EncodeToString(key[:]))
		sb.WriteByte('\n')
	}
	// The dedicated token pepper (api_token / delegation_token HMAC key).
	// Regenerate via: leapmux admin encryption-key rotate-pepper
	sb.WriteString("# api_token/delegation_token HMAC pepper (regenerate via: leapmux admin encryption-key rotate-pepper)\n")
	sb.WriteString(pepperTag)
	sb.WriteByte(':')
	sb.WriteString(base64.StdEncoding.EncodeToString(pepper[:]))
	sb.WriteByte('\n')

	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		return fmt.Errorf("keystore: write %s: %w", path, err)
	}
	return nil
}
