// Package control implements the `leapmux control` CLI's persistent
// state (credentials, key pins, per-hub defaults) and the transport
// layer that connects to a hub or worker IPC socket.
package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/util/atomicfile"
)

// CredentialFile is the per-hub credential payload stored under
// ~/.config/leapmux/control/credentials/<hub-host>.json (mode 0600).
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

// CanonicalHubURL returns the normalized form of raw for storage and display.
// Surrounding whitespace is trimmed. Local IPC URLs (unix:/npipe:) are returned
// verbatim after trimming because socket paths are case- and slash-sensitive.
// Parseable URLs with a host have their scheme and host lowercased and trailing
// slashes stripped from the path; other inputs fall back to stripping trailing
// slashes.
func CanonicalHubURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || locallisten.IsLocal(trimmed) {
		return trimmed
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return strings.TrimRight(trimmed, "/")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	if u.RawPath != "" {
		u.RawPath = strings.TrimRight(u.RawPath, "/")
	}
	return u.String()
}

// SameHubURL reports whether a and b identify the same hub. Remote HTTP(S) URLs
// compare case-insensitively on scheme and host, ignore trailing path slashes,
// and match by HubHost key; local IPC socket URLs compare verbatim after
// trimming whitespace.
func SameHubURL(a, b string) bool {
	ca := CanonicalHubURL(a)
	cb := CanonicalHubURL(b)
	if ca == "" || cb == "" {
		return false
	}
	if ca == cb {
		return true
	}
	if locallisten.IsLocal(ca) || locallisten.IsLocal(cb) {
		return false
	}
	ha, errA := HubHost(ca)
	hb, errB := HubHost(cb)
	return errA == nil && errB == nil && ha == hb
}

// HubHost extracts a collision-free filesystem key for hubURL, preserving
// scheme, hostname, port, and any non-root base path.
func HubHost(hubURL string) (string, error) {
	if locallisten.IsLocal(hubURL) {
		// Local sockets use the URL verbatim, with non-filename chars
		// flattened.
		flat := strings.NewReplacer("/", "_", ":", "_", "\\", "_").Replace(hubURL)
		return flat, nil
	}
	u, err := url.Parse(hubURL)
	if err != nil {
		return "", fmt.Errorf("parse hub url: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("hub url missing hostname")
	}
	// IPv6 literals contain colons, which are illegal in Windows filenames.
	host = strings.ReplaceAll(host, ":", "-")
	scheme := strings.ToLower(u.Scheme)
	if scheme == "" {
		scheme = "https"
	}
	key := scheme + "_" + host
	if u.Port() != "" {
		key = key + "_" + u.Port()
	}
	cleanPath := strings.TrimRight(u.Path, "/")
	if cleanPath != "" {
		sum := sha256.Sum256([]byte(cleanPath))
		key = key + "_" + hex.EncodeToString(sum[:6])
	}
	return key, nil
}

// ConfigDir returns the control CLI configuration directory:
// $LEAPMUX_CONTROL_CONFIG_DIR, else $XDG_CONFIG_HOME/leapmux/control, else
// %APPDATA%\leapmux\control on Windows or ~/.config/leapmux/control on POSIX.
func ConfigDir() (string, error) {
	if env := os.Getenv("LEAPMUX_CONTROL_CONFIG_DIR"); env != "" {
		return env, nil
	}
	if env := os.Getenv("XDG_CONFIG_HOME"); env != "" {
		return filepath.Join(env, "leapmux", "control"), nil
	}
	if runtime.GOOS == "windows" {
		if dir, err := os.UserConfigDir(); err == nil && dir != "" {
			return filepath.Join(dir, "leapmux", "control"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "leapmux", "control"), nil
}

const credentialsDirName = "credentials"

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
	return filepath.Join(dir, credentialsDirName, host+".json"), nil
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
	// One zone on disk: UTC. The writers mint deadlines with time.Now(),
	// whose zone is the writer's, and a file that carries one machine's
	// offset cannot be compared with the hub's own Z-ending timestamps
	// (jq, a shell pipeline) without parsing zones first -- and every
	// future command that prints a file deadline would otherwise have to
	// remember a UTC conversion to keep its output honest.
	creds.ExpiresAt = creds.ExpiresAt.UTC()
	creds.RefreshExpiresAt = creds.RefreshExpiresAt.UTC()
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
	return removeAtomicFile(path)
}

// ListCredentialFiles returns every credential file under
// <ConfigDir>/credentials.
func ListCredentialFiles() ([]CredentialFile, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	credsDir := filepath.Join(dir, credentialsDirName)
	entries, err := os.ReadDir(credsDir)
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
		data, err := os.ReadFile(filepath.Join(credsDir, e.Name()))
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
// the requested hub. It wraps cli.ErrNotLoggedIn, so the error envelope
// reports not_logged_in, with the login command as its hint, wherever the
// error surfaces.
var ErrNotLoggedIn = fmt.Errorf("%w to this hub", cli.ErrNotLoggedIn)

// NoHubError reports that nothing selects a hub: no --hub flag, no tab
// socket, no LEAPMUX_HUB, and no single or chosen default among the stored
// credentials.
type NoHubError struct {
	// Stored is the number of stored credentials. With none, the user is
	// signed in nowhere; with several, the user must choose one.
	Stored int
}

func (e *NoHubError) Error() string {
	if e.Stored > 1 {
		return fmt.Sprintf("no hub selected; this machine stores credentials for %d hubs and none is the default", e.Stored)
	}
	return "no hub selected"
}

// CLIError classifies the error for the envelope.
func (e *NoHubError) CLIError() *cli.Error {
	if e.Stored > 1 {
		return cli.Wrap(cli.CodeUsage, e, "").
			WithHint("pass --hub <url>").
			WithHint(`run "leapmux auth use <hub>" to choose the default hub`)
	}
	return cli.Wrap(cli.CodeNotLoggedIn, e, "").
		WithHint("pass --hub <url> or set LEAPMUX_HUB").
		WithHint(`run "leapmux auth login --hub <url>" to store a credential`)
}

// HubSource identifies how the CLI chose a Hub address.
type HubSource int

const (
	// HubSourceUnspecified means the client was constructed directly without
	// hub-address resolution.
	HubSourceUnspecified HubSource = iota
	// HubSourceFlag means an explicit --hub flag selected the Hub.
	HubSourceFlag
	// HubSourceEnv means LEAPMUX_HUB selected the Hub.
	HubSourceEnv
	// HubSourceContext means context.json selected the Hub.
	HubSourceContext
	// HubSourceSingleCredential means the sole stored credential file selected the Hub.
	HubSourceSingleCredential
	// HubSourceDiscovered means local solo/dev socket discovery found the Hub.
	HubSourceDiscovered
)

// DefaultHub returns the hub that a command addresses when neither --hub
// nor a tab's socket selects one: LEAPMUX_HUB, else context.json's hub, else
// the hub of the only stored credential, else a live local solo/dev Hub IPC
// socket. It returns a *NoHubError when none of them selects a hub.
func DefaultHub() (string, error) {
	hub, _, _, err := resolveDefaultHub(true)
	return hub, err
}

// DefaultHubWithSource is DefaultHub plus the HubSource that selected the hub.
func DefaultHubWithSource() (string, HubSource, error) {
	hub, source, _, err := resolveDefaultHub(true)
	return hub, source, err
}

// ConfiguredDefaultHub is DefaultHub without local solo/dev socket discovery.
// Commands that require a configured hub target (such as `auth login`) use it
// so an auto-discovered local IPC socket does not masquerade as a browser login
// target.
func ConfiguredDefaultHub() (string, error) {
	hub, _, _, err := resolveDefaultHub(false)
	return hub, err
}

// resolveDefaultHub resolves the default hub, its HubSource provenance, and
// the already-loaded CredentialFile when the hub came from the sole stored
// credential file, so callers avoid reading and unmarshaling that file a second
// time.
func resolveDefaultHub(allowDiscover bool) (string, HubSource, *CredentialFile, error) {
	if hub := strings.TrimSpace(os.Getenv("LEAPMUX_HUB")); hub != "" {
		return hub, HubSourceEnv, nil, nil
	}
	ctxCfg, err := LoadContext()
	if err != nil {
		return "", HubSourceUnspecified, nil, err
	}
	if ctxCfg.Hub != "" {
		return ctxCfg.Hub, HubSourceContext, nil, nil
	}
	files, err := ListCredentialFiles()
	if err != nil {
		return "", HubSourceUnspecified, nil, err
	}
	if len(files) == 1 {
		return files[0].HubURL, HubSourceSingleCredential, &files[0], nil
	}
	if allowDiscover && len(files) == 0 {
		if local := DiscoverLocalHub(); local != "" {
			return local, HubSourceDiscovered, nil, nil
		}
	}
	return "", HubSourceUnspecified, nil, &NoHubError{Stored: len(files)}
}

// SetDefaultHub records hub in context.json as the default hub, keeping any
// other saved context defaults. An empty hub clears the saved hub.
func SetDefaultHub(hub string) error {
	cfg, err := LoadContext()
	if err != nil {
		return err
	}
	cfg.Hub = CanonicalHubURL(hub)
	return SaveContext(cfg)
}

// ClearDefaultHubIf clears the saved default hub when it matches hub, keeping
// any other saved context fields intact.
func ClearDefaultHubIf(hub string) error {
	hub = strings.TrimSpace(hub)
	if hub == "" {
		return nil
	}
	cfg, err := LoadContext()
	if err != nil {
		return err
	}
	if SameHubURL(cfg.Hub, hub) {
		cfg.Hub = ""
		return SaveContext(cfg)
	}
	return nil
}
