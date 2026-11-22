package zcode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
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
	// zcodeOSKeyringTimeout limits one OS credential store lookup when the caller
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

// zcodeOSKeyAccessState reports whether reading the OS credential store item
// via the platform helper is expected to complete silently or prompt the user.
type zcodeOSKeyAccessState uint8

const (
	// zcodeOSKeyAccessSilent means reading the item will not prompt the user
	// (the platform has no per-app Keychain prompt, or the ACL already trusts
	// /usr/bin/security).
	zcodeOSKeyAccessSilent zcodeOSKeyAccessState = iota
	// zcodeOSKeyAccessNotFound means the silent probe confirmed no Keychain item
	// exists for (service, account), so spawning the OS helper is unnecessary.
	zcodeOSKeyAccessNotFound
	// zcodeOSKeyAccessMayPrompt means reading the item may show an interactive
	// OS dialog (such as macOS Keychain authorization or unlock).
	zcodeOSKeyAccessMayPrompt
)

// zcodeOSKeyAccessProbe silently inspects whether reading (service, account)
// from the OS credential store may prompt the user.
type zcodeOSKeyAccessProbe func(service, account string) zcodeOSKeyAccessState

// zcodeDefaultOSKeyProbe is the package-level seam for probeZCodeOSKeyAccess,
// analogous to zcodeLocator, so Start tests can control the silent Keychain
// probe without touching the host Keychain.
var zcodeDefaultOSKeyProbe zcodeOSKeyAccessProbe = probeZCodeOSKeyAccess

const (
	// kSecKeychainPromptRequirePassphase matches Apple's SecACL.h constant
	// (SecKeychainPromptSelector bit 0) requiring a password prompt even for a
	// trusted application.
	kSecKeychainPromptRequirePassphase uint16 = 0x0001
	darwinSecurityBinaryPath                  = "/usr/bin/security"
)

// zcodeDarwinACLInfo holds the decoded fields of one macOS SecACLRef needed to
// decide whether /usr/bin/security can decrypt the item without prompting.
type zcodeDarwinACLInfo struct {
	authorizations  []string
	allowAllApps    bool
	trustedAppPaths []string
	promptSelector  uint16
}

func zcodeHasDarwinDecryptAuthorization(auths []string) bool {
	return slices.Contains(auths, "ACLAuthorizationDecrypt") ||
		slices.Contains(auths, "ACLAuthorizationAny")
}

// zcodeEvaluateDarwinKeychainACL returns zcodeOSKeyAccessSilent only when the
// keychain is unlocked and at least one ACL authorizes decryption (or any
// operation) without requiring a passphrase and trusts either all applications
// or /usr/bin/security.
func zcodeEvaluateDarwinKeychainACL(unlocked bool, acls []zcodeDarwinACLInfo) zcodeOSKeyAccessState {
	if !unlocked {
		return zcodeOSKeyAccessMayPrompt
	}
	for _, acl := range acls {
		if !zcodeHasDarwinDecryptAuthorization(acl.authorizations) {
			continue
		}
		if acl.promptSelector&kSecKeychainPromptRequirePassphase != 0 {
			continue
		}
		if acl.allowAllApps || slices.Contains(acl.trustedAppPaths, darwinSecurityBinaryPath) {
			return zcodeOSKeyAccessSilent
		}
	}
	return zcodeOSKeyAccessMayPrompt
}

// zcodeParseDarwinTrustedAppPath decodes the NUL-terminated path returned by
// SecTrustedApplicationCopyData.
func zcodeParseDarwinTrustedAppPath(raw []byte) string {
	if i := bytes.IndexByte(raw, 0); i >= 0 {
		raw = raw[:i]
	}
	return string(raw)
}

// zcodeOSKeyringAccount returns the account identifier that ZCode stores its OS
// encryption key under: the lowercase hex SHA-256 of the resolved credentials.json
// path.
func zcodeOSKeyringAccount(absPath string) string {
	return zcodeSHA256Hex(absPath)
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
	return len(value) == zcodeOSKeyEncodedLen && zcodeIsBase64URLChars(value)
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

func (e *zcodeOSKeyCacheEntry) matchesFingerprints(wantFingerprints []string) bool {
	for _, fp := range wantFingerprints {
		if fp != "" && fp != e.fingerprint {
			return false
		}
	}
	return true
}

func (e *zcodeOSKeyCacheEntry) clearFailure() {
	e.failed = false
	e.failedCheckedKey = ""
	e.failedErr = nil
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

// hasValidKey reports whether c already holds a cached key for absPath whose
// fingerprint matches every non-empty fingerprint in wantFingerprints. It
// ignores entry.failed and entry.checkedKey so callers can decide whether a
// fresh OS keyring lookup is needed.
func (c *zcodeOSKeyCache) hasValidKey(absPath string, wantFingerprints []string) bool {
	if absPath == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[absPath]
	return entry != nil && entry.hasKey && entry.matchesFingerprints(wantFingerprints)
}

// hasCachedOutcome reports whether load would return an already-cached key or
// failure for (absPath, wantFingerprints) without calling the OS keyring reader.
func (c *zcodeOSKeyCache) hasCachedOutcome(absPath string, wantFingerprints []string) bool {
	if absPath == "" {
		return false
	}
	checkedKey := strings.Join(wantFingerprints, ",")
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _, hit := c.cachedLocked(absPath, wantFingerprints, checkedKey)
	return hit
}

// loadFresh clears any cached failure (and any stale checkedKey for a
// mismatched cached key) for absPath before delegating to load, so an explicit
// interactive retry recovers without restarting the Worker.
func (c *zcodeOSKeyCache) loadFresh(ctx context.Context, absPath string, wantFingerprints []string, reader zcodeOSKeyReader) ([sha256.Size]byte, error) {
	if absPath != "" {
		c.mu.Lock()
		if entry := c.entries[absPath]; entry != nil {
			entry.clearFailure()
			if entry.hasKey && !entry.matchesFingerprints(wantFingerprints) {
				entry.checkedKey = ""
			}
		}
		c.mu.Unlock()
	}
	return c.load(ctx, absPath, wantFingerprints, reader)
}

func (c *zcodeOSKeyCache) cachedLocked(absPath string, wantFingerprints []string, checkedKey string) ([sha256.Size]byte, error, bool) {
	entry := c.entries[absPath]
	if entry == nil {
		return [sha256.Size]byte{}, nil, false
	}
	if entry.hasKey && (entry.matchesFingerprints(wantFingerprints) || entry.checkedKey == checkedKey) {
		return entry.key, nil, true
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
			entry.clearFailure()
		})
		if keyErr != nil {
			return [sha256.Size]byte{}, fmt.Errorf("%w: %w", errZCodeCredentialStoreUnavailable, keyErr)
		}
		return key, nil
	})
}
