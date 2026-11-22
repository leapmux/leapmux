package zcode

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

const (
	// zcodeOSKeyringService is the OS credential store service name that ZCode's
	// systemCredentialCipher registers with @napi-rs/keyring.
	zcodeOSKeyringService = "ZCode Credential Encryption"
	// zcodeOSKeyEncodedLen is the length of a 32-byte key encoded as unpadded
	// base64url, matching ZCode's ENCODED_KEY_PATTERN (/^[A-Za-z0-9_-]{43}$/).
	zcodeOSKeyEncodedLen = 43
	// zcodeOSKeyringTimeout bounds one OS credential store lookup when the caller
	// supplies no shorter deadline, leaving time for an interactive authorization
	// prompt (such as macOS Keychain).
	zcodeOSKeyringTimeout = 60 * time.Second
)

// errZCodeOSKeyNotFound reports that the OS credential store holds no entry for
// the ZCode credential file path.
var errZCodeOSKeyNotFound = errors.New("the OS credential store holds no ZCode encryption key")

// zcodeOSKeyReader reads one secret from the OS credential store.
// found is false when the entry is definitively absent.
type zcodeOSKeyReader func(ctx context.Context, service, account string) (value string, found bool, err error)

// zcodeOSKeyringAccount returns the account identifier that ZCode stores its OS
// encryption key under: the lowercase hex SHA-256 of the resolved credentials.json
// path.
func zcodeOSKeyringAccount(absPath string) string {
	sum := sha256.Sum256([]byte(absPath))
	return hex.EncodeToString(sum[:])
}

// zcodeKeyFingerprint returns the 24-character lowercase hex prefix of SHA-256(key),
// matching ZCode's keyFingerprint().
func zcodeKeyFingerprint(key [sha256.Size]byte) string {
	sum := sha256.Sum256(key[:])
	return hex.EncodeToString(sum[:12])
}

// zcodeDecodeOSKey validates and decodes a base64url-encoded 32-byte OS keyring
// key, matching ZCode's ENCODED_KEY_PATTERN and Buffer.from(encoded, "base64url").
func zcodeDecodeOSKey(encoded string) ([sha256.Size]byte, error) {
	var key [sha256.Size]byte
	if !zcodeIsValidEncodedOSKey(encoded) {
		return key, errors.New("the ZCode OS encryption key is malformed")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != sha256.Size {
		return key, errors.New("the ZCode OS encryption key is malformed")
	}
	copy(key[:], raw)
	clear(raw)
	return key, nil
}

func zcodeIsValidEncodedOSKey(value string) bool {
	if len(value) != zcodeOSKeyEncodedLen {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

// zcodeDecodeWindowsCredentialBlob decodes a Windows Credential Manager blob
// written by @napi-rs/keyring / keyring-rs. A ZCode key is always 43 ASCII
// base64url bytes: UTF-8 blobs carry those 43 bytes directly, while UTF-16LE
// blobs carry 43 little-endian uint16 code units (with an optional trailing NUL).
func zcodeDecodeWindowsCredentialBlob(blob []byte) string {
	if zcodeIsValidEncodedOSKey(string(blob)) {
		return string(blob)
	}
	if len(blob) == 0 || len(blob)%2 != 0 {
		return string(blob)
	}
	units := make([]uint16, len(blob)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(blob[i*2 : i*2+2])
	}
	if len(units) > 0 && units[len(units)-1] == 0 {
		units = units[:len(units)-1]
	}
	return string(utf16.Decode(units))
}

type zcodeOSKeyCacheEntry struct {
	hasKey           bool
	key              [sha256.Size]byte
	fingerprint      string
	checkedKey       string
	failed           bool
	failedCheckedKey string
	failedErr        error
}

// zcodeOSKeyCache caches OS keyring lookups per normalized credentials.json path
// and coalesces concurrent requests. A definitive failure (such as a denied
// Keychain prompt or locked keyring) is cached for the same fingerprint set so
// background 30-second polls do not re-prompt the user.
type zcodeOSKeyCache struct {
	mu      sync.Mutex
	entries map[string]*zcodeOSKeyCacheEntry
	calls   zcodeSharedCalls[zcodeOSKeyCallKey, [sha256.Size]byte]
}

type zcodeOSKeyCallKey struct {
	absPath    string
	checkedKey string
}

// zcodeProcessOSKeys is the process-wide OS keyring cache shared by all ZCode
// agents using the default OS keyring reader.
var zcodeProcessOSKeys zcodeOSKeyCache

func (c *zcodeOSKeyCache) cachedLocked(absPath string, wantFingerprints []string, checkedKey string) ([sha256.Size]byte, error, bool) {
	entry := c.entries[absPath]
	if entry == nil {
		return [sha256.Size]byte{}, nil, false
	}
	if entry.hasKey {
		allMatch := true
		for _, fp := range wantFingerprints {
			if fp != "" && fp != entry.fingerprint {
				allMatch = false
				break
			}
		}
		if allMatch || entry.checkedKey == checkedKey {
			return entry.key, nil, true
		}
	}
	if entry.failed && entry.failedCheckedKey == checkedKey {
		return [sha256.Size]byte{}, entry.failedErr, true
	}
	return [sha256.Size]byte{}, nil, false
}

func (c *zcodeOSKeyCache) load(ctx context.Context, absPath string, wantFingerprints []string, reader zcodeOSKeyReader) ([sha256.Size]byte, error) {
	if absPath == "" {
		return [sha256.Size]byte{}, errors.New("no credential path for OS keyring lookup")
	}
	checkedKey := strings.Join(wantFingerprints, ",")
	c.mu.Lock()
	if key, err, hit := c.cachedLocked(absPath, wantFingerprints, checkedKey); hit {
		c.mu.Unlock()
		return key, err
	}
	c.mu.Unlock()

	callKey := zcodeOSKeyCallKey{absPath: absPath, checkedKey: checkedKey}
	return c.calls.do(ctx, callKey, zcodeOSKeyringTimeout, func(callCtx context.Context) ([sha256.Size]byte, error) {
		c.mu.Lock()
		if key, err, hit := c.cachedLocked(absPath, wantFingerprints, checkedKey); hit {
			c.mu.Unlock()
			return key, err
		}
		c.mu.Unlock()

		encoded, found, err := reader(callCtx, zcodeOSKeyringService, zcodeOSKeyringAccount(absPath))
		if callCtx.Err() != nil {
			return [sha256.Size]byte{}, callCtx.Err()
		}
		var key [sha256.Size]byte
		var keyErr error
		switch {
		case err != nil:
			keyErr = err
		case !found:
			keyErr = errZCodeOSKeyNotFound
		default:
			key, keyErr = zcodeDecodeOSKey(encoded)
		}

		c.calls.publish(callCtx, callKey, func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.entries == nil {
				c.entries = map[string]*zcodeOSKeyCacheEntry{}
			}
			entry := c.entries[absPath]
			if entry == nil {
				entry = &zcodeOSKeyCacheEntry{}
				c.entries[absPath] = entry
			}
			if keyErr != nil {
				// Do not cache transient cancellation errors from the caller.
				if !errors.Is(keyErr, context.Canceled) && !errors.Is(keyErr, context.DeadlineExceeded) {
					entry.failed = true
					entry.failedCheckedKey = checkedKey
					entry.failedErr = fmt.Errorf("%w: %w", errZCodeCredentialStoreUnavailable, keyErr)
				}
				return
			}
			entry.hasKey = true
			entry.key = key
			entry.fingerprint = zcodeKeyFingerprint(key)
			entry.checkedKey = checkedKey
			entry.failed = false
			entry.failedCheckedKey = ""
			entry.failedErr = nil
		})
		if keyErr != nil {
			return [sha256.Size]byte{}, fmt.Errorf("%w: %w", errZCodeCredentialStoreUnavailable, keyErr)
		}
		return key, nil
	})
}
