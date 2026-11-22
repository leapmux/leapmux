package zcode

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// The ZCode desktop and its app-server share one credential store,
// <home>/.zcode/v2/credentials.json: a JSON object whose values are AES-256-GCM
// ciphertexts. ZCode 3.18+ writes enc:v2:<os|env>:<fingerprint>:<iv>.<tag>.<ct>
// backed by the OS credential store, ZCODE_CREDENTIAL_SECRET,
// ZCODE_CREDENTIAL_SECRET_FILE, or a private credential-secret file; older builds
// wrote enc:v1:<iv>.<tag>.<ct>. LeapMux reads the store and never writes it,
// because a concurrent write could lose an entry that the desktop just saved.
var zcodeCredentialsRelPath = []string{".zcode", "v2", "credentials.json"}

const (
	// zcodeCredentialEncPrefix marks any encrypted value. An unknown enc: format
	// must fail decryption rather than be returned verbatim as plaintext.
	zcodeCredentialEncPrefix = "enc:"
	// zcodeCredentialCipherPrefix marks a legacy v1 encrypted value:
	// "enc:v1:<iv>.<auth tag>.<ciphertext>", each part base64url.
	zcodeCredentialCipherPrefix = "enc:v1:"
	// zcodeCredentialV2Prefix marks a v2 encrypted value:
	// "enc:v2:<os|env>:<24-hex-fingerprint>:<iv>.<auth tag>.<ciphertext>".
	zcodeCredentialV2Prefix = "enc:v2:"
	// zcodeCredentialSecretEnv overrides the secret that the store key derives from.
	zcodeCredentialSecretEnv = "ZCODE_CREDENTIAL_SECRET"
	// zcodeCredentialSecretFileEnv points at a raw 32-4096 byte secret file.
	zcodeCredentialSecretFileEnv = "ZCODE_CREDENTIAL_SECRET_FILE"
	// zcodeCredentialSourceFile pins a credential directory to OS storage ("os\n").
	zcodeCredentialSourceFile = "credential-source"
	// zcodeCredentialDefaultSecretFile is ZCode's private file-backed secret.
	zcodeCredentialDefaultSecretFile = "credential-secret"
	zcodeCredentialOSSourceMarker    = "os\n"
	zcodeCredentialSourceOS          = "os"
	zcodeCredentialSourceEnv         = "env"
	zcodeCredentialIVBytes           = 12
	zcodeCredentialTagBytes          = 16
	zcodeCredentialSecretMinBytes    = 32
	zcodeCredentialSecretMaxBytes    = 4096
	zcodeCredentialFingerprintHexLen = 24
)

var (
	// errZCodeCredentialDecryptFailed reports malformed or unauthenticated ciphertext.
	errZCodeCredentialDecryptFailed = errors.New("invalid encrypted ZCode credential")
	// errZCodeCredentialStoreUnavailable reports a missing, locked, mismatched, or
	// misconfigured key backend.
	errZCodeCredentialStoreUnavailable = errors.New("ZCode credential storage is unavailable")
)

// The store keys that LeapMux reads.
const (
	zcodeCredentialActiveProvider  = "oauth:active_provider"
	zcodeCredentialSessionRevision = "oauth:session_revision"
	zcodeCredentialZCodeJWT        = "zcodejwttoken"
)

func zcodeOAuthAccessTokenKey(family string) string { return "oauth:" + family + ":access_token" }
func zcodeOAuthUserInfoKey(family string) string    { return "oauth:" + family + ":user_info" }

// The provider families of the account plans, as the release's rules state them
// (access.accountType). Each also identifies the family's OAuth provider.
const (
	zcodeFamilyZAI      = "zai"
	zcodeFamilyBigModel = "bigmodel"
)

// zcodeProjectLocationKey is the store key under which the desktop caches the
// project, and the API key in it, that serves a coding plan
// (ProjectAccessTokenClient). The key spells the JSON array exactly as
// JavaScript's JSON.stringify and encodeURIComponent spell it, so it matches the
// desktop's entry. An individual plan uses the desktop's "non-team" project
// selection.
func zcodeProjectLocationKey(origin, family, accountID string, team *zcodeTeamProject) string {
	var teamPart *zcodeTeamProject
	selection := any("non-team")
	if team != nil {
		teamPart = team
		selection = nil
	}
	return "project-key-location:v1:" + zcodeEncodeURIComponent(zcodeJSONStringify(
		[]any{origin, family, accountID, teamPart, zcodeProjectUsageCodingPlan, selection}))
}

// zcodeCredentialStore reads the shared credential store.
type zcodeCredentialStore struct {
	path       string
	homeDir    string
	username   string
	secretEnv  string
	secretFile string
	// osKeyReader overrides the platform OS keyring reader in tests. When nil,
	// readZCodeOSKey and zcodeProcessOSKeys serve production reads.
	osKeyReader zcodeOSKeyReader
	osKeyCache  *zcodeOSKeyCache
	// osKeyProbe overrides the silent OS keyring access probe in tests.
	osKeyProbe zcodeOSKeyAccessProbe
}

// newZCodeCredentialStore opens the store under homeDir with the environment
// overrides that ZCode honors for this machine and user.
func newZCodeCredentialStore(homeDir string, getenv func(string) string) zcodeCredentialStore {
	path := ""
	if homeDir != "" {
		path = filepath.Join(append([]string{homeDir}, zcodeCredentialsRelPath...)...)
	}
	var secretEnv, secretFile string
	if getenv != nil {
		secretEnv = getenv(zcodeCredentialSecretEnv)
		secretFile = getenv(zcodeCredentialSecretFileEnv)
	}
	return zcodeCredentialStore{
		path:       path,
		homeDir:    homeDir,
		username:   zcodeNodeUsername(),
		secretEnv:  secretEnv,
		secretFile: secretFile,
	}
}

func zcodeLegacyFallbackSecret(homeDir, username string) string {
	if username == "" {
		username = "unknown"
	}
	return "zcode-credential-fallback:" + zcodeNodeOS(runtime.GOOS) + ":" + homeDir + ":" + username
}

// zcodeNodeUsername returns the user name as Node's os.userInfo() reports it. On
// Windows Go adds the domain ("DOMAIN\user"), which Node leaves out.
func zcodeNodeUsername() string {
	current, err := user.Current()
	if err != nil || current.Username == "" {
		return "unknown"
	}
	name := current.Username
	if i := strings.LastIndex(name, `\`); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// zcodeCredentialKeyIdentity uniquely identifies the key material of one store
// read without retaining raw secret bytes.
type zcodeCredentialKeyIdentity struct {
	path      string
	v2Source  string
	v2KeyHash [sha256.Size]byte
	v1KeyHash [sha256.Size]byte
}

// zcodeCredentials is one immutable read of the store and its resolved keys.
type zcodeCredentials struct {
	values   map[string]string
	v1Keys   [][sha256.Size]byte
	v1Err    error
	hasV2Key bool
	v2Source string
	v2Key    [sha256.Size]byte
	v2Err    error
	identity zcodeCredentialKeyIdentity
}

func (s zcodeCredentialStore) readRawValues() (map[string]string, error) {
	var values map[string]string
	err := sessionstore.ReadSidecarFile(s.path, sessionstore.MaxSidecarBytes, func(data []byte) error {
		return json.Unmarshal(data, &values)
	})
	if err != nil {
		return nil, err
	}
	if values == nil {
		values = map[string]string{}
	}
	return values, nil
}

// readContext reads the store and resolves only the key material needed to
// decrypt the entries present in it. A store that does not exist reads as empty
// without touching the OS credential store.
func (s zcodeCredentialStore) readContext(ctx context.Context) (zcodeCredentials, error) {
	credentials := zcodeCredentials{values: map[string]string{}}
	if s.path == "" {
		return credentials, nil
	}
	values, err := s.readRawValues()
	if errors.Is(err, os.ErrNotExist) {
		return credentials, nil
	}
	if err != nil {
		return zcodeCredentials{}, fmt.Errorf("read the ZCode credential store %s: %w", s.path, err)
	}
	credentials.values = values

	s.resolveKeys(ctx, &credentials, zcodeScanCredentialValues(credentials.values))
	return credentials, nil
}

type zcodeCredentialRequirements struct {
	hasV1          bool
	hasV2OS        bool
	hasV2Env       bool
	osFingerprints []string
}

func (r zcodeCredentialRequirements) empty() bool {
	return !r.hasV1 && !r.hasV2OS && !r.hasV2Env
}

func zcodeClassifyCiphertext(raw string) (zcodeCredentialRequirements, bool) {
	switch {
	case strings.HasPrefix(raw, zcodeCredentialV2Prefix):
		source, fp, _, parsed := zcodeParseV2Ciphertext(raw)
		if !parsed {
			return zcodeCredentialRequirements{}, false
		}
		if source == zcodeCredentialSourceOS {
			return zcodeCredentialRequirements{hasV2OS: true, osFingerprints: []string{fp}}, true
		}
		return zcodeCredentialRequirements{hasV2Env: true}, true
	case strings.HasPrefix(raw, zcodeCredentialCipherPrefix):
		return zcodeCredentialRequirements{hasV1: true}, true
	default:
		return zcodeCredentialRequirements{}, false
	}
}

func zcodeScanCredentialValues(values map[string]string) zcodeCredentialRequirements {
	var req zcodeCredentialRequirements
	seenOSFingerprints := map[string]struct{}{}
	for _, raw := range values {
		entry, ok := zcodeClassifyCiphertext(raw)
		if !ok {
			continue
		}
		req.hasV1 = req.hasV1 || entry.hasV1
		req.hasV2OS = req.hasV2OS || entry.hasV2OS
		req.hasV2Env = req.hasV2Env || entry.hasV2Env
		for _, fp := range entry.osFingerprints {
			if _, seen := seenOSFingerprints[fp]; !seen {
				seenOSFingerprints[fp] = struct{}{}
				req.osFingerprints = append(req.osFingerprints, fp)
			}
		}
	}
	slices.Sort(req.osFingerprints)
	return req
}

func (s zcodeCredentialStore) resolvedAbsPath() string {
	absPath := s.path
	if absPath != "" {
		if resolved, err := filepath.Abs(absPath); err == nil {
			absPath = resolved
		}
	}
	return absPath
}

func (s zcodeCredentialStore) osKeyReaderAndCache() (zcodeOSKeyReader, *zcodeOSKeyCache) {
	reader := s.osKeyReader
	cache := s.osKeyCache
	if reader == nil {
		return readZCodeOSKey, &zcodeProcessOSKeys
	}
	if cache == nil {
		cache = &zcodeOSKeyCache{}
	}
	return reader, cache
}

func (s zcodeCredentialStore) resolveOSKeyProbe() zcodeOSKeyAccessProbe {
	if s.osKeyProbe != nil {
		return s.osKeyProbe
	}
	if s.osKeyReader == nil {
		return zcodeDefaultOSKeyProbe
	}
	// Tests that inject a fake osKeyReader without an explicit probe must not
	// inspect the host machine's Keychain.
	return func(string, string) zcodeOSKeyAccessState {
		return zcodeOSKeyAccessSilent
	}
}

func zcodeMacOSKeychainStartupPrompt() agent.StartupPrompt {
	return agent.StartupPrompt{
		Title:       "macOS Keychain access required",
		Description: "ZCode stores its encrypted account key in the macOS login Keychain. LeapMux must read that key via Apple's /usr/bin/security tool to sign in to your ZCode account.",
		Steps: []string{
			"Click Proceed to trigger the macOS Keychain permission dialog.",
			"In the macOS dialog, macOS shows \"security\" as the requesting program. Enter your macOS login password and click \"Always Allow\" so macOS does not ask again.",
			"If you click Cancel or dismiss this dialog, ZCode startup stops without accessing Keychain.",
		},
		Note:           "If this Worker runs on another Mac, the macOS Keychain dialog opens on that Mac. For headless setups, set ZCODE_CREDENTIAL_SECRET_FILE instead.",
		ConfirmLabel:   "Proceed",
		CancelLabel:    "Cancel",
		WaitingMessage: "Waiting for macOS Keychain confirmation…",
		ProceedMessage: "Waiting for macOS Keychain authorization…",
	}
}

// prepareOSKeyForStartup silently checks before spawning zcode app-server
// whether reading an OS-backed v2 credential key may prompt the user. When a
// prompt is expected, it asks confirm first and only invokes the OS keyring
// reader after the user clicks Proceed. Degraded personal-provider startup
// paths (missing/corrupt credentials.json, explicit secret, corrupt marker, or
// silent keyring read failures) return nil so readAccountBindings handles them
// without blocking personal API-key providers.
func (s zcodeCredentialStore) prepareOSKeyForStartup(ctx context.Context, confirm agent.StartupPromptConfirmer) error {
	if s.path == "" {
		return nil
	}
	values, err := s.readRawValues()
	if err != nil {
		return nil
	}
	req := zcodeScanCredentialValues(values)
	if !req.hasV2OS {
		return nil
	}
	if _, hasExplicit, explicitErr := s.readExplicitManagedSecret(); hasExplicit || explicitErr != nil {
		return nil
	}
	absPath := s.resolvedAbsPath()
	if _, markerErr := zcodeReadOSSourceMarker(absPath); markerErr != nil {
		return nil
	}
	reader, cache := s.osKeyReaderAndCache()
	if cache.hasValidKey(absPath, req.osFingerprints) {
		return nil
	}
	switch s.resolveOSKeyProbe()(zcodeOSKeyringService, zcodeOSKeyringAccount(absPath)) {
	case zcodeOSKeyAccessNotFound:
		return nil
	case zcodeOSKeyAccessSilent:
		if confirm != nil {
			_, _ = cache.loadFresh(ctx, absPath, req.osFingerprints, reader)
		} else {
			_, _ = cache.load(ctx, absPath, req.osFingerprints, reader)
		}
		return nil
	case zcodeOSKeyAccessMayPrompt:
	}
	if confirm == nil {
		return fmt.Errorf("%w: macOS Keychain access for ZCode credentials requires interactive authorization; open the agent or send a message to authorize, or configure ZCODE_CREDENTIAL_SECRET_FILE", agent.ErrStartupCanceled)
	}
	proceed, err := confirm(ctx, zcodeMacOSKeychainStartupPrompt())
	if err != nil {
		return err
	}
	if !proceed {
		return fmt.Errorf("%w: macOS Keychain access was not authorized", agent.ErrStartupCanceled)
	}
	if _, err := cache.loadFresh(ctx, absPath, req.osFingerprints, reader); err != nil {
		return fmt.Errorf("%w: authorize macOS Keychain access for ZCode credentials: %w", agent.ErrStartupCanceled, err)
	}
	return nil
}

// decryptRaw decrypts one ciphertext value against the store's configured key
// backend without trimming whitespace, for interaction/requestCredentialCipher.
func (s zcodeCredentialStore) decryptRaw(ctx context.Context, value string) (string, error) {
	if !strings.HasPrefix(value, zcodeCredentialEncPrefix) {
		return value, nil
	}
	req, ok := zcodeClassifyCiphertext(value)
	if !ok {
		return "", errZCodeCredentialDecryptFailed
	}
	var credentials zcodeCredentials
	s.resolveKeys(ctx, &credentials, req)
	return credentials.decrypt(value)
}

func (s zcodeCredentialStore) resolveKeys(ctx context.Context, out *zcodeCredentials, req zcodeCredentialRequirements) {
	absPath := s.resolvedAbsPath()
	out.identity.path = absPath

	if req.empty() {
		return
	}

	// Step 1: Explicit environment secret sources (ZCODE_CREDENTIAL_SECRET or
	// ZCODE_CREDENTIAL_SECRET_FILE). Setting both is an error in ZCode.
	explicitSecret, hasExplicit, explicitErr := s.readExplicitManagedSecret()
	if explicitErr != nil {
		out.v1Err = explicitErr
		out.v2Err = explicitErr
		return
	}
	defer s.recordIdentity(out)

	legacySecret := ""
	resolveV2 := func() {
		if hasExplicit {
			legacySecret = explicitSecret
			out.hasV2Key = true
			out.v2Source = zcodeCredentialSourceEnv
			out.v2Key = sha256.Sum256([]byte(explicitSecret))
			return
		}

		// Step 2: Check credential-source pinning and default credential-secret file.
		pinnedOS, markerErr := zcodeReadOSSourceMarker(absPath)
		if markerErr != nil {
			if req.hasV2OS || req.hasV2Env {
				out.v2Err = markerErr
			}
			return
		}

		useOS := pinnedOS || req.hasV2OS
		if !useOS && absPath != "" {
			secretPath := filepath.Join(filepath.Dir(absPath), zcodeCredentialDefaultSecretFile)
			secret, found, err := zcodeReadSecretFile(secretPath, true)
			if err != nil {
				out.v2Err = fmt.Errorf("%w: %w", errZCodeCredentialStoreUnavailable, err)
				return
			}
			if found {
				legacySecret = secret
				out.hasV2Key = true
				out.v2Source = zcodeCredentialSourceEnv
				out.v2Key = sha256.Sum256([]byte(secret))
				return
			}
			if req.hasV2Env {
				out.v2Err = fmt.Errorf("%w: missing %s for env-encrypted credentials", errZCodeCredentialStoreUnavailable, zcodeCredentialDefaultSecretFile)
			}
			return
		}

		// Step 3: OS keyring, queried only when an enc:v2:os: entry needs it.
		// If the directory is pinned to the OS keyring and only enc:v2:env: entries
		// exist, fail closed without triggering an OS keyring prompt.
		if req.hasV2OS {
			reader, cache := s.osKeyReaderAndCache()
			if !cache.hasCachedOutcome(absPath, req.osFingerprints) {
				switch s.resolveOSKeyProbe()(zcodeOSKeyringService, zcodeOSKeyringAccount(absPath)) {
				case zcodeOSKeyAccessMayPrompt:
					out.v2Err = fmt.Errorf("%w: macOS Keychain authorization is required; reopen or restart the ZCode agent to authorize", errZCodeCredentialStoreUnavailable)
					return
				case zcodeOSKeyAccessNotFound:
					out.v2Err = fmt.Errorf("%w: %w", errZCodeCredentialStoreUnavailable, errZCodeOSKeyNotFound)
					return
				case zcodeOSKeyAccessSilent:
				}
			}
			key, err := cache.load(ctx, absPath, req.osFingerprints, reader)
			if err != nil {
				out.v2Err = err
				return
			}
			out.hasV2Key = true
			out.v2Source = zcodeCredentialSourceOS
			out.v2Key = key
			return
		}
		if req.hasV2Env {
			out.v2Err = fmt.Errorf("%w: credential directory is pinned to OS storage", errZCodeCredentialStoreUnavailable)
		}
	}

	resolveV2()
	if req.hasV1 {
		out.v1Keys = s.legacyKeyCandidates(legacySecret)
	}
}

func (s zcodeCredentialStore) recordIdentity(out *zcodeCredentials) {
	if out.hasV2Key {
		out.identity.v2Source = out.v2Source
		out.identity.v2KeyHash = sha256.Sum256(out.v2Key[:])
	}
	if len(out.v1Keys) > 0 {
		h := sha256.New()
		for _, k := range out.v1Keys {
			_, _ = h.Write(k[:])
		}
		copy(out.identity.v1KeyHash[:], h.Sum(nil))
	}
}

func (s zcodeCredentialStore) readExplicitManagedSecret() (string, bool, error) {
	if s.secretEnv != "" && s.secretFile != "" {
		return "", false, fmt.Errorf("%w: both %s and %s are configured",
			errZCodeCredentialStoreUnavailable, zcodeCredentialSecretEnv, zcodeCredentialSecretFileEnv)
	}
	if s.secretEnv != "" {
		return s.secretEnv, true, nil
	}
	if s.secretFile == "" {
		return "", false, nil
	}
	secret, found, err := zcodeReadSecretFile(s.secretFile, false)
	if err != nil {
		return "", false, fmt.Errorf("%w: %w", errZCodeCredentialStoreUnavailable, err)
	}
	if !found {
		return "", false, fmt.Errorf("%w: %s does not exist", errZCodeCredentialStoreUnavailable, zcodeCredentialSecretFileEnv)
	}
	return secret, true, nil
}

// legacyKeyCandidates mirrors ZCode's legacyKeyCandidates: raw managed secret,
// JS-trimmed managed secret, and the legacy machine/user fallback, deduplicated.
func (s zcodeCredentialStore) legacyKeyCandidates(managedSecret string) [][sha256.Size]byte {
	fallback := zcodeLegacyFallbackSecret(s.homeDir, s.username)
	candidates := make([]string, 0, 3)
	add := func(candidate string) {
		for _, existing := range candidates {
			if existing == candidate {
				return
			}
		}
		candidates = append(candidates, candidate)
	}
	if managedSecret != "" {
		add(managedSecret)
		if trimmed := zcodeTrimJSString(managedSecret); trimmed != "" {
			add(trimmed)
		}
	}
	add(fallback)
	keys := make([][sha256.Size]byte, 0, len(candidates))
	for _, candidate := range candidates {
		keys = append(keys, sha256.Sum256([]byte(candidate)))
	}
	return keys
}

func zcodeReadOSSourceMarker(absCredentialsPath string) (bool, error) {
	if absCredentialsPath == "" {
		return false, nil
	}
	sourcePath := filepath.Join(filepath.Dir(absCredentialsPath), zcodeCredentialSourceFile)
	f, err := os.Open(sourcePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: read %s: %w", errZCodeCredentialStoreUnavailable, zcodeCredentialSourceFile, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, int64(len(zcodeCredentialOSSourceMarker)+1)))
	if err != nil {
		return false, fmt.Errorf("%w: read %s: %w", errZCodeCredentialStoreUnavailable, zcodeCredentialSourceFile, err)
	}
	if string(data) != zcodeCredentialOSSourceMarker {
		return false, fmt.Errorf("%w: %s has unexpected content", errZCodeCredentialStoreUnavailable, zcodeCredentialSourceFile)
	}
	return true, nil
}

func zcodeReadSecretFile(path string, requirePrivate bool) (string, bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, errors.New("credential secret path is not a regular file")
	}
	if requirePrivate && runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", false, errors.New("credential secret file has unsafe permissions")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, zcodeCredentialSecretMaxBytes+1))
	if err != nil {
		return "", false, err
	}
	if len(raw) > zcodeCredentialSecretMaxBytes {
		return "", false, fmt.Errorf("credential secret file exceeds %d bytes", zcodeCredentialSecretMaxBytes)
	}
	value := strings.ToValidUTF8(string(raw), "\uFFFD")
	clear(raw)
	if len(value) < zcodeCredentialSecretMinBytes || len(value) > zcodeCredentialSecretMaxBytes {
		return "", false, fmt.Errorf("credential secret file must be %d to %d UTF-8 bytes", zcodeCredentialSecretMinBytes, zcodeCredentialSecretMaxBytes)
	}
	return value, true, nil
}

// get returns the decrypted value of key, trimmed as JavaScript's String.trim()
// trims it, or "" when the store holds none. An error means that the value could
// not be decrypted, either because the ciphertext is damaged or because the key
// backend is unavailable or does not match.
func (c zcodeCredentials) get(key string) (string, error) {
	value, ok := c.values[key]
	if !ok {
		return "", nil
	}
	plain, err := c.decrypt(value)
	if err != nil {
		return "", fmt.Errorf("decrypt the ZCode credential %q: %w", key, err)
	}
	return zcodeTrimJSString(plain), nil
}

func (c zcodeCredentials) decrypt(value string) (string, error) {
	if strings.HasPrefix(value, zcodeCredentialV2Prefix) {
		source, fingerprint, payload, ok := zcodeParseV2Ciphertext(value)
		if !ok {
			return "", errZCodeCredentialDecryptFailed
		}
		if c.v2Err != nil {
			return "", c.v2Err
		}
		if !c.hasV2Key {
			return "", errZCodeCredentialStoreUnavailable
		}
		if c.v2Source != source {
			return "", fmt.Errorf("%w: credential source %q does not match active source %q",
				errZCodeCredentialStoreUnavailable, source, c.v2Source)
		}
		if zcodeKeyFingerprint(c.v2Key) != fingerprint {
			return "", fmt.Errorf("%w: credential key fingerprint does not match",
				errZCodeCredentialStoreUnavailable)
		}
		return zcodeOpenAESGCM(c.v2Key, payload, false)
	}
	if sealed, ok := strings.CutPrefix(value, zcodeCredentialCipherPrefix); ok {
		if c.v1Err != nil {
			return "", c.v1Err
		}
		if len(c.v1Keys) == 0 {
			return "", fmt.Errorf("%w: no key configured for legacy credential", errZCodeCredentialDecryptFailed)
		}
		var lastErr error
		for _, candidate := range c.v1Keys {
			plain, err := zcodeOpenAESGCM(candidate, sealed, true)
			if err == nil {
				return plain, nil
			}
			lastErr = err
		}
		return "", lastErr
	}
	if strings.HasPrefix(value, zcodeCredentialEncPrefix) {
		return "", fmt.Errorf("%w: unsupported encrypted credential format", errZCodeCredentialDecryptFailed)
	}
	return value, nil
}

func zcodeParseV2Ciphertext(value string) (source, fingerprint, payload string, ok bool) {
	rest, hasPrefix := strings.CutPrefix(value, zcodeCredentialV2Prefix)
	if !hasPrefix {
		return "", "", "", false
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	if parts[0] != zcodeCredentialSourceOS && parts[0] != zcodeCredentialSourceEnv {
		return "", "", "", false
	}
	if !zcodeIsHexFingerprint(parts[1]) {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

func zcodeIsHexFingerprint(value string) bool {
	if len(value) != zcodeCredentialFingerprintHexLen {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return false
	}
	return true
}

func zcodeIsBase64URLChars(value string) bool {
	for i := range len(value) {
		c := value[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

func zcodeOpenAESGCM(key [sha256.Size]byte, sealed string, allowPadding bool) (string, error) {
	parts := strings.Split(sealed, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("%w: the ciphertext is malformed", errZCodeCredentialDecryptFailed)
	}
	decode := func(part string) ([]byte, error) {
		if allowPadding {
			part = strings.TrimRight(part, "=")
		}
		if !zcodeIsBase64URLChars(part) {
			return nil, errors.New("invalid base64url")
		}
		return base64.RawURLEncoding.DecodeString(part)
	}
	iv, err := decode(parts[0])
	if err != nil || len(iv) != zcodeCredentialIVBytes {
		return "", fmt.Errorf("%w: the IV is malformed", errZCodeCredentialDecryptFailed)
	}
	tag, err := decode(parts[1])
	if err != nil || len(tag) != zcodeCredentialTagBytes {
		return "", fmt.Errorf("%w: the auth tag is malformed", errZCodeCredentialDecryptFailed)
	}
	ciphertext, err := decode(parts[2])
	if err != nil {
		return "", fmt.Errorf("%w: the ciphertext is malformed", errZCodeCredentialDecryptFailed)
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, iv, append(ciphertext, tag...), nil)
	if err != nil {
		return "", fmt.Errorf("%w: the key does not match, or the ciphertext is damaged", errZCodeCredentialDecryptFailed)
	}
	return string(plain), nil
}

// zcodeAccountSession is a provider family's sign-in state in the store.
type zcodeAccountSession struct {
	// accessToken is the OAuth access token that coding-plan project tokens are
	// minted from.
	accessToken string
	// accountID identifies the signed-in account (the user profile's id).
	accountID string
	// zcodeJWT is the ZCode token that authorizes a Start Plan, or "" when the
	// store holds none for this family.
	zcodeJWT string
	// sessionRevision changes on every OAuth sign-in or sign-out in ZCode 3.18+.
	sessionRevision string
}

// session returns the family's sign-in state. ok is false when the family is not
// signed in. Independently readable material survives a failure in another field.
func (c zcodeCredentials) session(family string) (zcodeAccountSession, bool, error) {
	var session zcodeAccountSession
	var errs []error
	accessToken, err := c.get(zcodeOAuthAccessTokenKey(family))
	if err != nil {
		errs = append(errs, err)
	} else {
		session.accessToken = accessToken
	}
	jwt, err := c.startPlanJWT(family)
	if err != nil {
		errs = append(errs, err)
	} else {
		session.zcodeJWT = jwt
	}
	if revision, err := c.get(zcodeCredentialSessionRevision); err != nil {
		errs = append(errs, err)
	} else {
		session.sessionRevision = revision
	}
	if session.accessToken != "" {
		profile, err := c.get(zcodeOAuthUserInfoKey(family))
		if err != nil {
			errs = append(errs, err)
		} else {
			session.accountID = zcodeTrimJSString(zcodeProfileAccountID(family, profile))
		}
	}
	return session, session.accessToken != "" || session.zcodeJWT != "", errors.Join(errs...)
}

// sameSignIn verifies decoded account identity. An independently unreadable
// field is safe only while its stored value remains unchanged.
func (c zcodeCredentials) sameSignIn(other zcodeCredentials, family string, before zcodeAccountSession) bool {
	if c.identity != other.identity {
		return false
	}
	after, _, err := other.session(family)
	if before != after {
		return false
	}
	if err == nil {
		return true
	}
	keys := []string{zcodeOAuthAccessTokenKey(family), zcodeOAuthUserInfoKey(family), zcodeCredentialZCodeJWT, zcodeCredentialSessionRevision}
	if family == zcodeFamilyBigModel {
		if jwt, err := other.get(zcodeCredentialZCodeJWT); err == nil && jwt != "" {
			keys = append(keys, zcodeCredentialActiveProvider)
		}
	}
	for _, key := range keys {
		if _, err := other.get(key); err != nil && c.values[key] != other.values[key] {
			return false
		}
	}
	return true
}

// zcodeProfileAccountID returns the account id of a stored user profile, as the
// desktop's loadUserProfileFromKey reads it. A normalized profile states id,
// username and displayName as strings, and its id serves. For Z.ai the desktop
// stores the raw profile of the Z.ai user API instead, whose user_id serves
// (toOAuthUserProfileFromRawZaiUser): "unknown" when it is absent, unless the
// profile states no name and no email either. Every other profile has no id, and
// minting then refuses with its own error.
func zcodeProfileAccountID(family, profile string) string {
	var fields map[string]any
	if json.Unmarshal([]byte(profile), &fields) != nil || fields == nil {
		return ""
	}
	id, idIsString := fields["id"].(string)
	_, usernameIsString := fields["username"].(string)
	_, displayNameIsString := fields["displayName"].(string)
	if idIsString && usernameIsString && displayNameIsString {
		return id
	}
	if family != zcodeFamilyZAI {
		return ""
	}
	userID, ok := fields["user_id"].(string)
	if !ok {
		userID = "unknown"
	}
	name, _ := fields["name"].(string)
	email, _ := fields["email"].(string)
	if zcodeTrimJSString(name) == "" && email == "" && userID == "unknown" {
		return ""
	}
	return userID
}

// startPlanJWT returns the ZCode token that authorizes the family's Start Plan, as
// the desktop chooses it (resolveStartPlanAuthorization): the stored token serves
// Z.ai, and serves BigModel only while BigModel is the active OAuth provider.
func (c zcodeCredentials) startPlanJWT(family string) (string, error) {
	jwt, err := c.get(zcodeCredentialZCodeJWT)
	if err != nil || jwt == "" || family == zcodeFamilyZAI {
		return jwt, err
	}
	active, err := c.get(zcodeCredentialActiveProvider)
	if err != nil {
		return "", err
	}
	if active != family {
		return "", nil
	}
	return jwt, nil
}

// zcodeProjectLocation is where a coding plan's project tokens come from.
type zcodeProjectLocation struct {
	OrganizationID string `json:"organizationId"`
	ProjectID      string `json:"projectId"`
	APIKeyID       string `json:"apiKeyId"`
}

func (l zcodeProjectLocation) valid() bool {
	return strings.TrimSpace(l.OrganizationID) != "" && strings.TrimSpace(l.ProjectID) != "" && strings.TrimSpace(l.APIKeyID) != ""
}

// projectLocation returns the location that the desktop cached under key, or ok
// false when the store holds no usable one.
func (c zcodeCredentials) projectLocation(key string) (zcodeProjectLocation, bool) {
	value, err := c.get(key)
	if err != nil || value == "" {
		return zcodeProjectLocation{}, false
	}
	var location zcodeProjectLocation
	if json.Unmarshal([]byte(value), &location) != nil || !location.valid() {
		return zcodeProjectLocation{}, false
	}
	return location, true
}
