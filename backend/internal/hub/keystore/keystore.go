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
	// keySize is the encryption key size (32 bytes).
	keySize = chacha20poly1305.KeySize
	// versionSize is the key version prefix size (4 bytes, big-endian uint32).
	versionSize = 4
	// overhead is the minimum ciphertext overhead: version + nonce + Poly1305 tag.
	overhead = versionSize + nonceSize + chacha20poly1305.Overhead
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

// Keystore manages a versioned key ring for XChaCha20-Poly1305 envelope encryption.
type Keystore struct {
	keys          map[uint32][keySize]byte
	aeads         map[uint32]cipher.AEAD
	activeVersion uint32
}

// New creates a Keystore from a key ring map. The highest version becomes the
// active key used for new encryptions. AEAD ciphers are pre-computed for each
// key version to avoid repeated key schedule expansion on every call.
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

// Versions returns all key versions in the ring, sorted ascending.
func (ks *Keystore) Versions() []uint32 {
	vers := make([]uint32, 0, len(ks.keys))
	for v := range ks.keys {
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
// key ring with a single version-1 key if the file does not exist. The file
// is created with mode 0600.
func LoadOrGenerate(path string) (*Keystore, error) {
	// Try to open exclusively to avoid TOCTOU race between Stat and WriteFile.
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("keystore: create directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		// File didn't exist — generate a new key ring.
		key, genErr := GenerateKey()
		if genErr != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return nil, genErr
		}
		encoded := "1:" + base64.StdEncoding.EncodeToString(key[:]) + "\n"
		if _, writeErr := f.WriteString(encoded); writeErr != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return nil, fmt.Errorf("keystore: write %s: %w", path, writeErr)
		}
		_ = f.Close()
	} else if !os.IsExist(err) {
		return nil, fmt.Errorf("keystore: create %s: %w", path, err)
	}
	return LoadFromFile(path)
}

// LoadFromFile loads a key ring from a file. Each line must be "version:base64key".
func LoadFromFile(path string) (*Keystore, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("keystore: open %s: %w", path, err)
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
			return nil, fmt.Errorf("keystore: %s:%d: expected version:base64key", path, lineNum)
		}

		ver, err := strconv.ParseUint(parts[0], 10, 32)
		if err != nil || ver == 0 {
			return nil, fmt.Errorf("keystore: %s:%d: invalid version %q (must be 1-%d)", path, lineNum, parts[0], ^uint32(0))
		}
		version := uint32(ver)

		if _, exists := keys[version]; exists {
			return nil, fmt.Errorf("keystore: %s:%d: duplicate version %d", path, lineNum, version)
		}

		decoded, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return nil, fmt.Errorf("keystore: %s:%d: invalid base64: %w", path, lineNum, err)
		}
		if len(decoded) != keySize {
			return nil, fmt.Errorf("keystore: %s:%d: key must be %d bytes, got %d", path, lineNum, keySize, len(decoded))
		}

		var key [keySize]byte
		copy(key[:], decoded)
		keys[version] = key
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("keystore: read %s: %w", path, err)
	}

	return New(keys)
}

// GenerateKey generates a cryptographically random 256-bit key.
func GenerateKey() ([keySize]byte, error) {
	var key [keySize]byte
	if _, err := rand.Read(key[:]); err != nil {
		return key, fmt.Errorf("keystore: generate key: %w", err)
	}
	return key, nil
}

// RotateKey adds a new key to the ring file at path. Returns the new version number.
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

	if err := writeKeyRingFile(path, ks.keys); err != nil {
		return 0, err
	}
	return newVersion, nil
}

// RemoveKey removes a key version from the ring file at path.
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
	return writeKeyRingFile(path, ks.keys)
}

// writeKeyRingFile writes the key ring to a file with mode 0600.
func writeKeyRingFile(path string, keys map[uint32][keySize]byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("keystore: create directory: %w", err)
	}

	// Sort versions for deterministic output.
	versions := make([]uint32, 0, len(keys))
	for v := range keys {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })

	var sb strings.Builder
	for _, v := range versions {
		key := keys[v]
		sb.WriteString(strconv.FormatUint(uint64(v), 10))
		sb.WriteByte(':')
		sb.WriteString(base64.StdEncoding.EncodeToString(key[:]))
		sb.WriteByte('\n')
	}

	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		return fmt.Errorf("keystore: write %s: %w", path, err)
	}
	return nil
}
