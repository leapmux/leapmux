// Package control implements the `leapmux control` CLI's persistent
// state (credentials, key pins, per-hub defaults) and the transport
// layer that connects to a hub or worker IPC socket.
package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
)

// CredentialFile is the per-hub credential payload stored under
// ~/.config/leapmux/control/<hub-host>.json (mode 0600).
//
// TokenID, ClientID and Scope are ADVISORY: they let the CLI say which row it
// holds, which app owns it and what it was granted, without a round trip. The
// hub decides all three -- a hand-edited Scope grants nothing, because the
// grant lives in the api_tokens row the bearer specifies.
type CredentialFile struct {
	HubURL      string `json:"hub_url"`
	AccessToken string `json:"access_token"`
	// ClientID is the app this credential was issued to. The hub binds the
	// refresh, revoke and step-up stages to the credential's own app, so the
	// CLI must present this id on them; a file written before the field
	// existed belongs to the control CLI's built-in registration, which is
	// what the empty value falls back to (ClientIDOrBuiltIn).
	ClientID     string    `json:"client_id,omitzero"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	// RefreshExpiresAt is when the credential stops being able to renew
	// itself, so `auth status` can say when this device must sign in again
	// rather than only when the hour-long access token lapses. Zero on a
	// credential written before the hub reported it.
	RefreshExpiresAt time.Time `json:"refresh_expires_at,omitzero"`
	UserID           string    `json:"user_id"`
	Username         string    `json:"username"`
	TokenID          string    `json:"token_id,omitempty"`
	// Scope is the canonical RFC 6749 section 3.3 grant the hub reported, so
	// `auth status` can print what this credential may do.
	Scope string `json:"scope,omitempty"`
}

// ClientIDOrBuiltIn answers which app this credential belongs to: the recorded
// id, or the control CLI's built-in registration for a file written before the
// field existed. Every stage that must identify the app reads this, so a credential
// minted to another registration presents the right id instead of the CLI's
// own -- the hub answers "this grant was issued to a different app" otherwise,
// and the refresh path reads that refusal as a revoked credential.
func (c *CredentialFile) ClientIDOrBuiltIn() string {
	if c == nil || c.ClientID == "" {
		return ControlCLIClientID
	}
	return c.ClientID
}

// HubHost extracts the hostname (or socket path) used for the on-disk
// credential filename.
func HubHost(hubURL string) (string, error) {
	if strings.HasPrefix(hubURL, "unix:") || strings.HasPrefix(hubURL, "npipe:") {
		// Local sockets use the URL verbatim, with non-filename chars
		// flattened.
		flat := strings.NewReplacer("/", "_", ":", "_", "\\", "_").Replace(hubURL)
		return flat, nil
	}
	u, err := url.Parse(hubURL)
	if err != nil {
		return "", fmt.Errorf("parse hub url: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("hub url missing hostname")
	}
	if u.Port() != "" {
		host = host + "_" + u.Port()
	}
	return host, nil
}

// ConfigDir returns ~/.config/leapmux/control (XDG-style on POSIX,
// %APPDATA%\leapmux\control on Windows).
func ConfigDir() (string, error) {
	if env := os.Getenv("LEAPMUX_CONTROL_CONFIG_DIR"); env != "" {
		return env, nil
	}
	if env := os.Getenv("XDG_CONFIG_HOME"); env != "" {
		return filepath.Join(env, "leapmux", "control"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "leapmux", "control"), nil
}

// CredentialsPath returns the full path of the credential file for hubURL.
func CredentialsPath(hubURL string) (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	host, err := HubHost(hubURL)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, host+".json"), nil
}

// SaveCredentials writes the credentials for hubURL to disk with 0600
// permissions. It creates the directory when the directory is missing.
func SaveCredentials(hubURL string, creds CredentialFile) error {
	path, err := CredentialsPath(hubURL)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, data, 0o600)
}

// LoadCredentials reads the credentials for hubURL from disk. Returns
// ErrNotLoggedIn if the file doesn't exist.
func LoadCredentials(hubURL string) (*CredentialFile, error) {
	path, err := CredentialsPath(hubURL)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotLoggedIn
		}
		return nil, err
	}
	var c CredentialFile
	if err := json.Unmarshal(data, &c); err != nil {
		// State the file path. A caller that reports this message has no other
		// way to say WHICH credential file the operator must repair or
		// delete, because the path derives from the hub URL.
		return nil, fmt.Errorf("parse credentials %s: %w", path, err)
	}
	return &c, nil
}

// DeleteCredentials removes the credentials file for hubURL. Idempotent.
//
// It removes the temporary files of an interrupted write as well, and that
// is not housekeeping: each one holds a complete access and refresh pair at
// mode 0600, and nothing else ever shows it. ListCredentialFiles reads only
// the ".json" suffix, so `auth list` cannot report one; and a logout that
// left one behind told the user the credential was gone from this machine
// while its secret stayed on the disk for the rest of its window.
func DeleteCredentials(hubURL string) error {
	path, err := CredentialsPath(hubURL)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return atomicfile.RemoveTempFiles(path)
}

// ListCredentialFiles returns every credential file in ConfigDir.
func ListCredentialFiles() ([]CredentialFile, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []CredentialFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var c CredentialFile
		if json.Unmarshal(data, &c) == nil && c.HubURL != "" {
			out = append(out, c)
		}
	}
	return out, nil
}

// LoadCredentials returns ErrNotLoggedIn when no credential file exists for
// the requested hub.
var ErrNotLoggedIn = errors.New("not logged in to this hub; run `leapmux control auth login`")
