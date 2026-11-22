package zcode

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodeCredentialSecret(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "from-env", zcodeCredentialSecret(func(string) string { return "from-env" }, "/home/u", "u"))
	assert.Equal(t, "zcode-credential-fallback:"+zcodeNodeOS(runtime.GOOS)+":/home/u:alice",
		zcodeCredentialSecret(func(string) string { return "" }, "/home/u", "alice"),
		"the fallback spells the platform as Node.js does")
	assert.NotContains(t, zcodeNodeUsername(), `\`, "Node.js reports the user name without its Windows domain")
}

func TestZCodeCredentialStore_ReadsWhatZCodeEncrypts(t *testing.T) {
	t.Parallel()

	store := zcodeTestCredentialStore(t, map[string]string{
		zcodeCredentialZCodeJWT: " jwt-value ",
		"plain":                 "",
	})
	credentials := zcodeTestReadCredentials(t, store)
	jwt, err := credentials.get(zcodeCredentialZCodeJWT)
	require.NoError(t, err)
	assert.Equal(t, "jwt-value", jwt)
	missing, err := credentials.get("absent")
	require.NoError(t, err)
	assert.Empty(t, missing)

	other := store
	other.secretEnv = "another secret"
	wrong := zcodeTestReadCredentials(t, other)
	_, err = wrong.get(zcodeCredentialZCodeJWT)
	require.ErrorContains(t, err, "the key does not match")
	assert.NotContains(t, err.Error(), "jwt-value", "an error never quotes a credential")
}

func TestZCodeDecryptCredential(t *testing.T) {
	t.Parallel()

	key := sha256.Sum256([]byte("k"))
	plain, err := zcodeDecryptCredential(key, "not encrypted")
	require.NoError(t, err)
	assert.Equal(t, "not encrypted", plain, "a value without the cipher prefix is plain, as in ZCode")

	sealed := zcodeTestEncryptCredential(t, key, "secret")
	parts := strings.Split(strings.TrimPrefix(sealed, zcodeCredentialCipherPrefix), ".")
	require.Len(t, parts, 3)
	for name, value := range map[string]string{
		"two parts":     zcodeCredentialCipherPrefix + parts[0] + "." + parts[1],
		"four parts":    sealed + ".x",
		"an empty iv":   zcodeCredentialCipherPrefix + "." + parts[1] + "." + parts[2],
		"a short iv":    zcodeCredentialCipherPrefix + "AAAA." + parts[1] + "." + parts[2],
		"a short tag":   zcodeCredentialCipherPrefix + parts[0] + ".AAAA." + parts[2],
		"bad base64url": zcodeCredentialCipherPrefix + parts[0] + "." + parts[1] + ".%%%",
	} {
		_, err := zcodeDecryptCredential(key, value)
		assert.Error(t, err, name)
	}
	padded := zcodeCredentialCipherPrefix + parts[0] + "=." + parts[1] + "." + parts[2]
	plain, err = zcodeDecryptCredential(key, padded)
	require.NoError(t, err, "base64url padding is accepted, as Node's Buffer accepts it")
	assert.Equal(t, "secret", plain)
}

func TestZCodeCredentialStore_ReadFailures(t *testing.T) {
	t.Parallel()

	credentials, err := newZCodeCredentialStore("", zcodeTestCredentialGetenv).read()
	require.NoError(t, err)
	assert.Empty(t, credentials.values, "no home directory holds no store")

	credentials, err = newZCodeCredentialStore(t.TempDir(), zcodeTestCredentialGetenv).read()
	require.NoError(t, err)
	assert.Empty(t, credentials.values, "a store that does not exist reads as empty")

	store := newZCodeCredentialStore(t.TempDir(), zcodeTestCredentialGetenv)
	require.NoError(t, os.MkdirAll(filepath.Dir(store.path), 0o700))
	require.NoError(t, os.WriteFile(store.path, []byte(`{"zcodejwttoken":`), 0o600))
	_, err = store.read()
	require.ErrorContains(t, err, "read the ZCode credential store")
}

func TestZCodeCredentials_Session(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		values     map[string]string
		family     string
		want       zcodeAccountSession
		wantSigned bool
	}{
		{
			name: "a Z.ai sign-in with a normalized profile",
			values: map[string]string{
				zcodeOAuthAccessTokenKey("zai"): "access",
				zcodeOAuthUserInfoKey("zai"):    `{"id":"user-1","username":"user","displayName":"User"}`,
				zcodeCredentialZCodeJWT:         "jwt",
			},
			family:     zcodeFamilyZAI,
			want:       zcodeAccountSession{accessToken: "access", accountID: "user-1", zcodeJWT: "jwt"},
			wantSigned: true,
		},
		{
			// The desktop stores the raw profile of the Z.ai user API, as a store on this
			// machine holds it.
			name: "a Z.ai sign-in with the raw profile that the desktop stores",
			values: map[string]string{
				zcodeOAuthAccessTokenKey("zai"): "access",
				zcodeOAuthUserInfoKey("zai"):    `{"avatar":"data:image/png;base64,AAAA","email":"u@example.com","name":"User","user_id":"0b7c4e7e-0000-4000-8000-000000000001"}`,
				zcodeCredentialZCodeJWT:         "jwt",
			},
			family:     zcodeFamilyZAI,
			want:       zcodeAccountSession{accessToken: "access", accountID: "0b7c4e7e-0000-4000-8000-000000000001", zcodeJWT: "jwt"},
			wantSigned: true,
		},
		{
			name: "the ZCode token serves BigModel only while BigModel is the active provider",
			values: map[string]string{
				zcodeOAuthAccessTokenKey("bigmodel"): "access", zcodeCredentialZCodeJWT: "jwt", zcodeCredentialActiveProvider: "zai",
			},
			family:     zcodeFamilyBigModel,
			want:       zcodeAccountSession{accessToken: "access"},
			wantSigned: true,
		},
		{
			name: "BigModel as the active provider",
			values: map[string]string{
				zcodeOAuthAccessTokenKey("bigmodel"): "access", zcodeCredentialZCodeJWT: "jwt", zcodeCredentialActiveProvider: "bigmodel",
			},
			family:     zcodeFamilyBigModel,
			want:       zcodeAccountSession{accessToken: "access", zcodeJWT: "jwt"},
			wantSigned: true,
		},
		{
			name:       "a ZCode token alone signs in a Start Plan",
			values:     map[string]string{zcodeCredentialZCodeJWT: "jwt"},
			family:     zcodeFamilyZAI,
			want:       zcodeAccountSession{zcodeJWT: "jwt"},
			wantSigned: true,
		},
		{
			name:       "an unreadable profile leaves the account id empty",
			values:     map[string]string{zcodeOAuthAccessTokenKey("zai"): "access", zcodeOAuthUserInfoKey("zai"): "{"},
			family:     zcodeFamilyZAI,
			want:       zcodeAccountSession{accessToken: "access"},
			wantSigned: true,
		},
		{
			name:   "no sign-in",
			values: map[string]string{},
			family: zcodeFamilyZAI,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			credentials := zcodeTestReadCredentials(t, zcodeTestCredentialStore(t, test.values))
			got, signedIn, err := credentials.session(test.family)
			require.NoError(t, err)
			assert.Equal(t, test.wantSigned, signedIn)
			assert.Equal(t, test.want, got)
		})
	}
}

// The account id comes from the profile as the desktop's loadUserProfileFromKey
// reads it, because the id is part of the desktop's cached location key.
func TestZCodeProfileAccountID(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		family  string
		profile string
		want    string
	}{
		{name: "a normalized profile", family: "bigmodel", profile: `{"id":"u-1","username":"user","displayName":"User"}`, want: "u-1"},
		{name: "a normalized Z.ai profile", family: "zai", profile: `{"id":"u-1","username":"user","displayName":"User","user_id":"raw"}`, want: "u-1"},
		{name: "an id without a username is not a normalized profile", family: "bigmodel", profile: `{"id":"u-1","displayName":"User"}`},
		{name: "a numeric id is not a normalized profile", family: "bigmodel", profile: `{"id":7,"username":"user","displayName":"User"}`},
		{name: "the raw Z.ai profile", family: "zai", profile: `{"user_id":"u-2","name":"User","email":"u@example.com"}`, want: "u-2"},
		{name: "the raw profile serves Z.ai only", family: "bigmodel", profile: `{"user_id":"u-2","name":"User"}`},
		{name: "a raw Z.ai profile without user_id", family: "zai", profile: `{"name":"User"}`, want: "unknown"},
		{name: "a raw Z.ai profile with only an email", family: "zai", profile: `{"email":"u@example.com"}`, want: "unknown"},
		{name: "a raw Z.ai profile without a name, an email or a user_id", family: "zai", profile: `{"name":"  ","avatar":"a"}`},
		{name: "a numeric user_id", family: "zai", profile: `{"user_id":7,"name":"User"}`, want: "unknown"},
		{name: "no profile", family: "zai", profile: ``},
		{name: "a JSON null", family: "zai", profile: `null`},
		{name: "a JSON array", family: "zai", profile: `[1]`},
		{name: "malformed JSON", family: "zai", profile: `{`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, zcodeProfileAccountID(test.family, test.profile))
		})
	}
}

// The key must spell the desktop's entry exactly, or LeapMux could never read it.
func TestZCodeProjectLocationKey(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		"project-key-location:v1:%5B%22https%3A%2F%2Fapi.z.ai%22%2C%22zai%22%2C%22user-1%22%2Cnull%2C1%2C%22non-team%22%5D",
		zcodeProjectLocationKey("https://api.z.ai", "zai", "user-1", nil),
		"the key of an individual plan, as a desktop store on this machine holds it")
	assert.Equal(t,
		"project-key-location:v1:"+zcodeEncodeURIComponent(`["https://bigmodel.cn","bigmodel","user-1",{"organizationId":"org","projectId":"proj"},1,null]`),
		zcodeProjectLocationKey("https://bigmodel.cn", "bigmodel", "user-1", &zcodeTeamProject{organizationID: "org", projectID: "proj", productID: "prod"}),
		"a team plan states its project and no project selection")
}

func TestZCodeEncodeURIComponent(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "A-z_0.9!~*'()", zcodeEncodeURIComponent("A-z_0.9!~*'()"), "JavaScript keeps these")
	assert.Equal(t, "%20%2F%3F%3D%26%2B%5B%5D%22%25", zcodeEncodeURIComponent(` /?=&+[]"%`))
	assert.Equal(t, "%E9%BB%98", zcodeEncodeURIComponent("默"), "each UTF-8 byte is escaped in uppercase hex")
}

func TestZCodeCredentials_ProjectLocation(t *testing.T) {
	t.Parallel()

	credentials := zcodeTestReadCredentials(t, zcodeTestCredentialStore(t, map[string]string{
		"valid":      `{"organizationId":"org","projectId":"proj","apiKeyId":"key"}`,
		"incomplete": `{"organizationId":"org","projectId":" "}`,
		"malformed":  `{`,
	}))
	location, ok := credentials.projectLocation("valid")
	require.True(t, ok)
	assert.Equal(t, zcodeProjectLocation{OrganizationID: "org", ProjectID: "proj", APIKeyID: "key"}, location)
	for _, key := range []string{"incomplete", "malformed", "absent"} {
		_, ok := credentials.projectLocation(key)
		assert.False(t, ok, key)
	}
}

func TestLoadZCodeDeviceMid(t *testing.T) {
	t.Parallel()

	writeState := func(t *testing.T, content string) string {
		t.Helper()
		home := t.TempDir()
		path := filepath.Join(append([]string{home}, zcodeTelemetryStateRelPath...)...)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		return home
	}

	const desktop = "1a2b3c4d-5e6f-4a1b-8c2d-3e4f5a6b7c8d"
	assert.Equal(t, desktop, loadZCodeDeviceMid(writeState(t, `{"deviceMid":" `+desktop+` ","lastDailyActiveDate":"2026-10-25"}`)),
		"the desktop's device id, so the account sees no extra device")
	fallback := zcodeProcessDeviceMid()
	require.Regexp(t, zcodeUUIDPattern, fallback)
	for name, home := range map[string]string{
		"no state file":   t.TempDir(),
		"no home":         "",
		"not a UUID":      writeState(t, `{"deviceMid":"leapmux"}`),
		"not a string":    writeState(t, `{"deviceMid":42}`),
		"malformed state": writeState(t, `{`),
	} {
		assert.Equal(t, fallback, loadZCodeDeviceMid(home), name)
	}
}

func TestZCodeCredentials_PreservesIndependentCredentialMaterial(t *testing.T) {
	t.Parallel()
	for _, broken := range []string{"oauth", "profile", "jwt"} {
		t.Run(broken, func(t *testing.T) {
			t.Parallel()
			values := map[string]string{zcodeOAuthAccessTokenKey("zai"): "login", zcodeOAuthUserInfoKey("zai"): zcodeTestUserInfo("user-1"), zcodeCredentialZCodeJWT: "jwt"}
			key := map[string]string{"oauth": zcodeOAuthAccessTokenKey("zai"), "profile": zcodeOAuthUserInfoKey("zai"), "jwt": zcodeCredentialZCodeJWT}[broken]
			credentials := zcodeTestReadCredentials(t, zcodeTestCredentialStore(t, values))
			credentials.values[key] = "enc:v1:broken"
			session, _, err := credentials.session("zai")
			require.Error(t, err)
			if broken == "jwt" {
				assert.Equal(t, "login", session.accessToken)
				assert.Equal(t, "user-1", session.accountID)
			} else {
				assert.Equal(t, "jwt", session.zcodeJWT)
			}
		})
	}
}

func TestZCodeCredentials_UsesNativeWhitespace(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ value, want string }{
		{value: "\uFEFF \tjwt\uFEFF", want: "jwt"},
		{value: "\u0085jwt\u0085", want: "\u0085jwt\u0085"},
	} {
		credentials := zcodeCredentials{values: map[string]string{"key": test.value}}
		got, err := credentials.get("key")
		require.NoError(t, err)
		assert.Equal(t, test.want, got)
	}
}

func TestZCodeProjectLocationKey_UsesNativeUnicodeEncoding(t *testing.T) {
	t.Parallel()
	team := &zcodeTeamProject{organizationID: "org\u2028one", projectID: "project\u2029two"}
	assert.Equal(t,
		"project-key-location:v1:%5B%22https%3A%2F%2Fapi.z.ai%22%2C%22zai%22%2C%22user%E2%80%A8one%22%2C%7B%22organizationId%22%3A%22org%E2%80%A8one%22%2C%22projectId%22%3A%22project%E2%80%A9two%22%7D%2C1%2Cnull%5D",
		zcodeProjectLocationKey("https://api.z.ai", "zai", "user\u2028one", team))
}

func TestZCodeCredentials_DecryptsV2(t *testing.T) {
	t.Parallel()

	key := sha256.Sum256([]byte(zcodeTestCredentialSecret))
	v2Env := zcodeTestEncryptV2Credential(t, key, zcodeCredentialSourceEnv, "secret-token")

	store := newZCodeCredentialStore(t.TempDir(), zcodeTestCredentialGetenv)
	require.NoError(t, os.MkdirAll(filepath.Dir(store.path), 0o700))
	require.NoError(t, os.WriteFile(store.path, []byte(`{"oauth:zai:access_token":"`+v2Env+`"}`), 0o600))

	credentials, err := store.read()
	require.NoError(t, err)
	got, err := credentials.get(zcodeOAuthAccessTokenKey("zai"))
	require.NoError(t, err)
	assert.Equal(t, "secret-token", got)

	// Unknown enc: prefixes must fail instead of returning raw ciphertext as plaintext.
	credentials.values["unknown"] = "enc:v99:opaque"
	_, err = credentials.get("unknown")
	require.ErrorIs(t, err, errZCodeCredentialDecryptFailed)
}

func zcodeTestRandomOSKey(t *testing.T) ([sha256.Size]byte, string) {
	t.Helper()
	var key [sha256.Size]byte
	_, err := rand.Read(key[:])
	require.NoError(t, err)
	encoded := base64.RawURLEncoding.EncodeToString(key[:])
	require.Len(t, encoded, zcodeOSKeyEncodedLen)
	return key, encoded
}

func TestZCodeCredentials_DecryptsV2OSKeyringAndCaches(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	key, encoded := zcodeTestRandomOSKey(t)
	var calls atomic.Int32
	var seenService, seenAccount string
	var mu sync.Mutex

	store := newZCodeCredentialStore(home, nil)
	store.osKeyCache = &zcodeOSKeyCache{}
	store.osKeyReader = func(_ context.Context, service, account string) (string, bool, error) {
		calls.Add(1)
		mu.Lock()
		seenService, seenAccount = service, account
		mu.Unlock()
		return encoded, true, nil
	}

	require.NoError(t, os.MkdirAll(filepath.Dir(store.path), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(store.path), zcodeCredentialSourceFile), []byte(zcodeCredentialOSSourceMarker), 0o600))
	// A stray credential-secret file must be ignored when credential-source pins OS storage.
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(store.path), zcodeCredentialDefaultSecretFile), []byte(strings.Repeat("x", 48)), 0o600))

	raw := map[string]string{
		zcodeOAuthAccessTokenKey("zai"): zcodeTestEncryptV2Credential(t, key, zcodeCredentialSourceOS, "oauth-token"),
		zcodeOAuthUserInfoKey("zai"):    zcodeTestEncryptV2Credential(t, key, zcodeCredentialSourceOS, zcodeTestUserInfo("user-42")),
		zcodeCredentialZCodeJWT:         zcodeTestEncryptV2Credential(t, key, zcodeCredentialSourceOS, "jwt-42"),
		zcodeCredentialSessionRevision:  zcodeTestEncryptV2Credential(t, key, zcodeCredentialSourceOS, "rev-1"),
	}
	data, err := json.Marshal(raw)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(store.path, data, 0o600))

	creds, err := store.read()
	require.NoError(t, err)
	session, signedIn, err := creds.session(zcodeFamilyZAI)
	require.NoError(t, err)
	assert.True(t, signedIn)
	assert.Equal(t, zcodeAccountSession{
		accessToken:     "oauth-token",
		accountID:       "user-42",
		zcodeJWT:        "jwt-42",
		sessionRevision: "rev-1",
	}, session)
	assert.Equal(t, int32(1), calls.Load())
	absPath, err := filepath.Abs(store.path)
	require.NoError(t, err)
	assert.Equal(t, zcodeOSKeyringService, seenService)
	assert.Equal(t, zcodeOSKeyringAccount(absPath), seenAccount)

	// Second read reuses the cached OS key without hitting the OS keyring again.
	creds2, err := store.read()
	require.NoError(t, err)
	token2, err := creds2.get(zcodeOAuthAccessTokenKey("zai"))
	require.NoError(t, err)
	assert.Equal(t, "oauth-token", token2)
	assert.Equal(t, int32(1), calls.Load())

	// If ZCode rotates the OS key (new fingerprint), the cache refreshes once.
	newKey, newEncoded := zcodeTestRandomOSKey(t)
	store.osKeyReader = func(context.Context, string, string) (string, bool, error) {
		calls.Add(1)
		return newEncoded, true, nil
	}
	raw[zcodeOAuthAccessTokenKey("zai")] = zcodeTestEncryptV2Credential(t, newKey, zcodeCredentialSourceOS, "rotated-token")
	data, err = json.Marshal(raw)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(store.path, data, 0o600))
	creds3, err := store.read()
	require.NoError(t, err)
	rotated, err := creds3.get(zcodeOAuthAccessTokenKey("zai"))
	require.NoError(t, err)
	assert.Equal(t, "rotated-token", rotated)
	assert.Equal(t, int32(2), calls.Load())
}

func TestZCodeCredentials_OSKeyringCachesDefinitiveFailureNotCancellation(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	key, encoded := zcodeTestRandomOSKey(t)
	v2 := zcodeTestEncryptV2Credential(t, key, zcodeCredentialSourceOS, "secret")

	store := newZCodeCredentialStore(home, nil)
	store.osKeyCache = &zcodeOSKeyCache{}
	require.NoError(t, os.MkdirAll(filepath.Dir(store.path), 0o700))
	require.NoError(t, os.WriteFile(store.path, []byte(`{"zcodejwttoken":"`+v2+`"}`), 0o600))

	// First call cancelled by caller context: must NOT poison the cache.
	var calls atomic.Int32
	store.osKeyReader = func(ctx context.Context, _, _ string) (string, bool, error) {
		calls.Add(1)
		<-ctx.Done()
		return "", false, ctx.Err()
	}
	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	creds, err := store.readContext(canceledCtx)
	require.NoError(t, err)
	_, err = creds.get(zcodeCredentialZCodeJWT)
	require.Error(t, err)

	// Next call hits a definitive OS denial: cached so repeated polls do not re-prompt.
	calls.Store(0)
	store.osKeyReader = func(context.Context, string, string) (string, bool, error) {
		calls.Add(1)
		return "", false, errors.New("user denied keychain access")
	}
	for range 3 {
		creds, err := store.read()
		require.NoError(t, err)
		_, err = creds.get(zcodeCredentialZCodeJWT)
		require.ErrorIs(t, err, errZCodeCredentialStoreUnavailable)
	}
	assert.Equal(t, int32(1), calls.Load(), "definitive denial must be cached across polls")

	// A brand-new store with its own cache can still read cleanly.
	fresh := newZCodeCredentialStore(home, nil)
	fresh.osKeyCache = &zcodeOSKeyCache{}
	fresh.osKeyReader = func(context.Context, string, string) (string, bool, error) {
		return encoded, true, nil
	}
	creds, err = fresh.read()
	require.NoError(t, err)
	got, err := creds.get(zcodeCredentialZCodeJWT)
	require.NoError(t, err)
	assert.Equal(t, "secret", got)
}

func TestZCodeCredentials_ManagedSecretFileAndDefaultFile(t *testing.T) {
	t.Parallel()

	secret := strings.Repeat("s", 48)
	key := sha256.Sum256([]byte(secret))
	v2Env := zcodeTestEncryptV2Credential(t, key, zcodeCredentialSourceEnv, "from-file")

	t.Run("explicit ZCODE_CREDENTIAL_SECRET_FILE", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		secretFile := filepath.Join(t.TempDir(), "managed-secret")
		require.NoError(t, os.WriteFile(secretFile, []byte(secret), 0o600))
		store := newZCodeCredentialStore(home, func(name string) string {
			if name == zcodeCredentialSecretFileEnv {
				return secretFile
			}
			return ""
		})
		require.NoError(t, os.MkdirAll(filepath.Dir(store.path), 0o700))
		require.NoError(t, os.WriteFile(store.path, []byte(`{"zcodejwttoken":"`+v2Env+`"}`), 0o600))

		creds, err := store.read()
		require.NoError(t, err)
		got, err := creds.get(zcodeCredentialZCodeJWT)
		require.NoError(t, err)
		assert.Equal(t, "from-file", got)
	})

	t.Run("default private credential-secret file", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		store := newZCodeCredentialStore(home, nil)
		store.osKeyCache = &zcodeOSKeyCache{}
		store.osKeyReader = func(context.Context, string, string) (string, bool, error) {
			t.Fatal("OS keyring must not be queried when private credential-secret serves")
			return "", false, nil
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(store.path), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(store.path), zcodeCredentialDefaultSecretFile), []byte(secret), 0o600))
		require.NoError(t, os.WriteFile(store.path, []byte(`{"zcodejwttoken":"`+v2Env+`"}`), 0o600))

		creds, err := store.read()
		require.NoError(t, err)
		got, err := creds.get(zcodeCredentialZCodeJWT)
		require.NoError(t, err)
		assert.Equal(t, "from-file", got)
	})

	t.Run("conflicting secret env and secret file fail closed", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		secretFile := filepath.Join(t.TempDir(), "managed-secret")
		require.NoError(t, os.WriteFile(secretFile, []byte(secret), 0o600))
		store := newZCodeCredentialStore(home, func(name string) string {
			switch name {
			case zcodeCredentialSecretEnv:
				return secret
			case zcodeCredentialSecretFileEnv:
				return secretFile
			default:
				return ""
			}
		})
		require.NoError(t, os.MkdirAll(filepath.Dir(store.path), 0o700))
		require.NoError(t, os.WriteFile(store.path, []byte(`{"zcodejwttoken":"`+v2Env+`"}`), 0o600))

		creds, err := store.read()
		require.NoError(t, err)
		_, err = creds.get(zcodeCredentialZCodeJWT)
		require.ErrorIs(t, err, errZCodeCredentialStoreUnavailable)
	})

	t.Run("unsafe permissions on default credential-secret rejected on unix", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX mode bits do not apply on Windows")
		}
		t.Parallel()
		home := t.TempDir()
		store := newZCodeCredentialStore(home, nil)
		require.NoError(t, os.MkdirAll(filepath.Dir(store.path), 0o700))
		secretPath := filepath.Join(filepath.Dir(store.path), zcodeCredentialDefaultSecretFile)
		require.NoError(t, os.WriteFile(secretPath, []byte(secret), 0o644))
		require.NoError(t, os.Chmod(secretPath, 0o644))
		require.NoError(t, os.WriteFile(store.path, []byte(`{"zcodejwttoken":"`+v2Env+`"}`), 0o600))

		creds, err := store.read()
		require.NoError(t, err)
		_, err = creds.get(zcodeCredentialZCodeJWT)
		require.ErrorIs(t, err, errZCodeCredentialStoreUnavailable)
	})

	t.Run("v2 rejects source mismatch and wrong fingerprint", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		osKey, _ := zcodeTestRandomOSKey(t)
		osCiphertext := zcodeTestEncryptV2Credential(t, osKey, zcodeCredentialSourceOS, "os-secret")
		wrongKey := sha256.Sum256([]byte("different-secret"))
		wrongFPCiphertext := zcodeTestEncryptV2Credential(t, wrongKey, zcodeCredentialSourceEnv, "env-secret")

		store := newZCodeCredentialStore(home, zcodeTestCredentialGetenv)
		require.NoError(t, os.MkdirAll(filepath.Dir(store.path), 0o700))
		data, err := json.Marshal(map[string]string{
			"from-os":  osCiphertext,
			"wrong-fp": wrongFPCiphertext,
			"bad-v2":   "enc:v2:os:not-24-hex:a.b.c",
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(store.path, data, 0o600))

		creds, err := store.read()
		require.NoError(t, err)
		_, err = creds.get("from-os")
		require.ErrorIs(t, err, errZCodeCredentialStoreUnavailable)
		_, err = creds.get("wrong-fp")
		require.ErrorIs(t, err, errZCodeCredentialStoreUnavailable)
		_, err = creds.get("bad-v2")
		require.ErrorIs(t, err, errZCodeCredentialDecryptFailed)
	})
}

func TestZCodeDecodeWindowsCredentialBlob(t *testing.T) {
	t.Parallel()

	_, encoded := zcodeTestRandomOSKey(t)
	assert.Equal(t, encoded, zcodeDecodeWindowsCredentialBlob([]byte(encoded)), "UTF-8 blob passes through")

	units := utf16.Encode([]rune(encoded))
	utf16Bytes := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(utf16Bytes[i*2:], u)
	}
	assert.Equal(t, encoded, zcodeDecodeWindowsCredentialBlob(utf16Bytes), "UTF-16LE blob without NUL decodes")

	withNul := append(append([]byte(nil), utf16Bytes...), 0, 0)
	assert.Equal(t, encoded, zcodeDecodeWindowsCredentialBlob(withNul), "UTF-16LE blob with trailing NUL decodes")
}

func TestZCodeCredentials_PinnedOSSkipsKeyringForEnvEntriesAndAvoidsMismatchLoop(t *testing.T) {
	t.Parallel()

	t.Run("pinned OS directory refuses env ciphertext without querying OS keyring", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		secret := strings.Repeat("k", 48)
		key := sha256.Sum256([]byte(secret))
		v2Env := zcodeTestEncryptV2Credential(t, key, zcodeCredentialSourceEnv, "env-secret")

		store := newZCodeCredentialStore(home, nil)
		store.osKeyCache = &zcodeOSKeyCache{}
		store.osKeyReader = func(context.Context, string, string) (string, bool, error) {
			t.Fatal("OS keyring must not be queried for env-only entries")
			return "", false, nil
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(store.path), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(store.path), zcodeCredentialSourceFile), []byte(zcodeCredentialOSSourceMarker), 0o600))
		require.NoError(t, os.WriteFile(store.path, []byte(`{"zcodejwttoken":"`+v2Env+`"}`), 0o600))

		creds, err := store.read()
		require.NoError(t, err)
		_, err = creds.get(zcodeCredentialZCodeJWT)
		require.ErrorIs(t, err, errZCodeCredentialStoreUnavailable)
	})

	t.Run("persistent fingerprint mismatch does not re-query OS keyring on every poll", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		realKey, realEncoded := zcodeTestRandomOSKey(t)
		otherKey, _ := zcodeTestRandomOSKey(t)
		var calls atomic.Int32
		store := newZCodeCredentialStore(home, nil)
		store.osKeyCache = &zcodeOSKeyCache{}
		store.osKeyReader = func(context.Context, string, string) (string, bool, error) {
			calls.Add(1)
			return realEncoded, true, nil
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(store.path), 0o700))
		require.NoError(t, os.WriteFile(store.path, []byte(`{"zcodejwttoken":"`+zcodeTestEncryptV2Credential(t, otherKey, zcodeCredentialSourceOS, "stale")+`"}`), 0o600))

		for range 3 {
			creds, err := store.read()
			require.NoError(t, err)
			_, err = creds.get(zcodeCredentialZCodeJWT)
			require.ErrorIs(t, err, errZCodeCredentialStoreUnavailable)
		}
		assert.Equal(t, int32(1), calls.Load(), "mismatched fingerprint must not trigger repeated OS keyring reads")
		assert.NotEqual(t, zcodeKeyFingerprint(realKey), zcodeKeyFingerprint(otherKey))
	})

	t.Run("legacy v1 tries raw, trimmed, and fallback secret candidates", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		rawSecret := "  padded-secret  "
		trimmedKey := sha256.Sum256([]byte("padded-secret"))
		fallbackKey := sha256.Sum256([]byte(zcodeLegacyFallbackSecret(home, zcodeNodeUsername())))
		store := newZCodeCredentialStore(home, func(name string) string {
			if name == zcodeCredentialSecretEnv {
				return rawSecret
			}
			return ""
		})
		require.NoError(t, os.MkdirAll(filepath.Dir(store.path), 0o700))
		data, err := json.Marshal(map[string]string{
			"trimmed":  zcodeTestEncryptCredential(t, trimmedKey, "from-trimmed"),
			"fallback": zcodeTestEncryptCredential(t, fallbackKey, "from-fallback"),
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(store.path, data, 0o600))

		creds, err := store.read()
		require.NoError(t, err)
		gotTrimmed, err := creds.get("trimmed")
		require.NoError(t, err)
		assert.Equal(t, "from-trimmed", gotTrimmed)
		gotFallback, err := creds.get("fallback")
		require.NoError(t, err)
		assert.Equal(t, "from-fallback", gotFallback)
	})
}
