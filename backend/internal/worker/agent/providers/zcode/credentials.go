package zcode

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// The ZCode desktop and its app-server share one credential store,
// <home>/.zcode/v2/credentials.json: a JSON object whose values are AES-256-GCM
// ciphertexts under a key that both derive from one secret. LeapMux reads the
// store and never writes it, because a concurrent write could lose an entry that
// the desktop just saved.
var zcodeCredentialsRelPath = []string{".zcode", "v2", "credentials.json"}

const (
	// zcodeCredentialCipherPrefix marks an encrypted value:
	// "enc:v1:<iv>.<auth tag>.<ciphertext>", each part base64url.
	zcodeCredentialCipherPrefix = "enc:v1:"
	// zcodeCredentialSecretEnv overrides the secret that the store key derives from.
	zcodeCredentialSecretEnv = "ZCODE_CREDENTIAL_SECRET"
	zcodeCredentialIVBytes   = 12
	zcodeCredentialTagBytes  = 16
)

// The store keys that LeapMux reads.
const (
	zcodeCredentialActiveProvider = "oauth:active_provider"
	zcodeCredentialZCodeJWT       = "zcodejwttoken"
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

// zcodeJSONStringify encodes value as JavaScript's JSON.stringify does for the
// strings, numbers, and objects that LeapMux hashes or uses as keys: no spaces, and
// no HTML escaping.
func zcodeJSONStringify(value any) string {
	var out strings.Builder
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		panic(fmt.Sprintf("zcodeJSONStringify: %v", err))
	}
	return strings.TrimSuffix(out.String(), "\n")
}

// zcodeEncodeURIComponent escapes s as JavaScript's encodeURIComponent does: it
// keeps letters, digits, and - _ . ! ~ * ' ( ), and percent-encodes every other
// UTF-8 byte in uppercase hexadecimal.
func zcodeEncodeURIComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') || strings.IndexByte("-_.!~*'()", c) >= 0 {
			out.WriteByte(c)
			continue
		}
		out.WriteByte('%')
		out.WriteByte(hex[c>>4])
		out.WriteByte(hex[c&0x0f])
	}
	return out.String()
}

// zcodeCredentialStore reads the shared credential store.
type zcodeCredentialStore struct {
	path string
	key  [sha256.Size]byte
}

// newZCodeCredentialStore opens the store under homeDir with the key that ZCode
// derives for this machine and user.
func newZCodeCredentialStore(homeDir string, getenv func(string) string) zcodeCredentialStore {
	path := ""
	if homeDir != "" {
		path = filepath.Join(append([]string{homeDir}, zcodeCredentialsRelPath...)...)
	}
	return zcodeCredentialStore{path: path, key: sha256.Sum256([]byte(zcodeCredentialSecret(getenv, homeDir, zcodeNodeUsername())))}
}

// zcodeCredentialSecret returns the secret that ZCode derives the store key from
// (defaultCredentialSecret): ZCODE_CREDENTIAL_SECRET, or a fallback that spells the
// platform, the home directory, and the user name as Node.js reports them.
func zcodeCredentialSecret(getenv func(string) string, homeDir, username string) string {
	if secret := getenv(zcodeCredentialSecretEnv); secret != "" {
		return secret
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

// zcodeCredentials is one read of the store.
type zcodeCredentials struct {
	values map[string]string
	key    [sha256.Size]byte
}

// read reads the store. A store that does not exist reads as empty.
func (s zcodeCredentialStore) read() (zcodeCredentials, error) {
	credentials := zcodeCredentials{values: map[string]string{}, key: s.key}
	if s.path == "" {
		return credentials, nil
	}
	err := sessionstore.ReadSidecarFile(s.path, sessionstore.MaxSidecarBytes, func(data []byte) error {
		return json.Unmarshal(data, &credentials.values)
	})
	if errors.Is(err, os.ErrNotExist) {
		return credentials, nil
	}
	if err != nil {
		return zcodeCredentials{}, fmt.Errorf("read the ZCode credential store %s: %w", s.path, err)
	}
	return credentials, nil
}

// get returns the decrypted value of key, or "" when the store holds none. An
// error means that the value could not be decrypted, usually because the store
// was written under another secret.
func (c zcodeCredentials) get(key string) (string, error) {
	value, ok := c.values[key]
	if !ok {
		return "", nil
	}
	plain, err := zcodeDecryptCredential(c.key, value)
	if err != nil {
		return "", fmt.Errorf("decrypt the ZCode credential %q: %w", key, err)
	}
	return strings.TrimSpace(plain), nil
}

// zcodeDecryptCredential decrypts one store value as ZCode's
// createCredentialCipherProvider does. A value without the cipher prefix is plain.
func zcodeDecryptCredential(key [sha256.Size]byte, value string) (string, error) {
	sealed, encrypted := strings.CutPrefix(value, zcodeCredentialCipherPrefix)
	if !encrypted {
		return value, nil
	}
	parts := strings.Split(sealed, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return "", errors.New("the ciphertext is malformed")
	}
	decode := func(part string) ([]byte, error) {
		return base64.RawURLEncoding.DecodeString(strings.TrimRight(part, "="))
	}
	iv, err := decode(parts[0])
	if err != nil || len(iv) != zcodeCredentialIVBytes {
		return "", errors.New("the IV is malformed")
	}
	tag, err := decode(parts[1])
	if err != nil || len(tag) != zcodeCredentialTagBytes {
		return "", errors.New("the auth tag is malformed")
	}
	ciphertext, err := decode(parts[2])
	if err != nil {
		return "", errors.New("the ciphertext is malformed")
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
		return "", errors.New("the key does not match, or the ciphertext is damaged")
	}
	return string(plain), nil
}

// zcodeAccountSession is a provider family's sign-in state in the store.
type zcodeAccountSession struct {
	family string
	// accessToken is the OAuth access token that coding-plan project tokens are
	// minted from.
	accessToken string
	// accountID identifies the signed-in account (the user profile's id).
	accountID string
	// zcodeJWT is the ZCode token that authorizes a Start Plan, or "" when the
	// store holds none for this family.
	zcodeJWT string
}

// session returns the family's sign-in state. ok is false when the family is not
// signed in.
func (c zcodeCredentials) session(family string) (zcodeAccountSession, bool, error) {
	accessToken, err := c.get(zcodeOAuthAccessTokenKey(family))
	if err != nil {
		return zcodeAccountSession{}, false, err
	}
	jwt, err := c.startPlanJWT(family)
	if err != nil {
		return zcodeAccountSession{}, false, err
	}
	if accessToken == "" {
		// A Start Plan can be signed in without an OAuth session: its ZCode token
		// is enough.
		if jwt == "" {
			return zcodeAccountSession{}, false, nil
		}
		return zcodeAccountSession{family: family, zcodeJWT: jwt}, true, nil
	}
	profile, err := c.get(zcodeOAuthUserInfoKey(family))
	if err != nil {
		return zcodeAccountSession{}, false, err
	}
	return zcodeAccountSession{
		family:      family,
		accessToken: accessToken,
		accountID:   strings.TrimSpace(zcodeProfileAccountID(family, profile)),
		zcodeJWT:    jwt,
	}, true, nil
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
	if strings.TrimSpace(name) == "" && email == "" && userID == "unknown" {
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
