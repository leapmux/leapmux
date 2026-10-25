package zcode

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
	other.key = sha256.Sum256([]byte("another secret"))
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
			want:       zcodeAccountSession{family: "zai", accessToken: "access", accountID: "user-1", zcodeJWT: "jwt"},
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
			want:       zcodeAccountSession{family: "zai", accessToken: "access", accountID: "0b7c4e7e-0000-4000-8000-000000000001", zcodeJWT: "jwt"},
			wantSigned: true,
		},
		{
			name: "the ZCode token serves BigModel only while BigModel is the active provider",
			values: map[string]string{
				zcodeOAuthAccessTokenKey("bigmodel"): "access", zcodeCredentialZCodeJWT: "jwt", zcodeCredentialActiveProvider: "zai",
			},
			family:     zcodeFamilyBigModel,
			want:       zcodeAccountSession{family: "bigmodel", accessToken: "access"},
			wantSigned: true,
		},
		{
			name: "BigModel as the active provider",
			values: map[string]string{
				zcodeOAuthAccessTokenKey("bigmodel"): "access", zcodeCredentialZCodeJWT: "jwt", zcodeCredentialActiveProvider: "bigmodel",
			},
			family:     zcodeFamilyBigModel,
			want:       zcodeAccountSession{family: "bigmodel", accessToken: "access", zcodeJWT: "jwt"},
			wantSigned: true,
		},
		{
			name:       "a ZCode token alone signs in a Start Plan",
			values:     map[string]string{zcodeCredentialZCodeJWT: "jwt"},
			family:     zcodeFamilyZAI,
			want:       zcodeAccountSession{family: "zai", zcodeJWT: "jwt"},
			wantSigned: true,
		},
		{
			name:       "an unreadable profile leaves the account id empty",
			values:     map[string]string{zcodeOAuthAccessTokenKey("zai"): "access", zcodeOAuthUserInfoKey("zai"): "{"},
			family:     zcodeFamilyZAI,
			want:       zcodeAccountSession{family: "zai", accessToken: "access"},
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
