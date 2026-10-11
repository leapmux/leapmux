package cline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// The user's Cline settings.
//
// A session runs on the provider and the model that the user's Cline settings
// select, as a session of Cline's own CLI does: the provider that
// `providers.json` last used, and the model its settings state. The hub does not
// state them -- each Cline client reads the file itself and states both at
// `session.create` -- so the worker reads the file the same way.
//
// The daemon runs inside the user's login shell, and a profile can export the
// variables that move the file. So the worker reads those variables in the same
// shell (launch.ShellEnv), and falls back to its own environment when the probe
// establishes nothing.

// dataEnvKeys are the variables that locate Cline's configuration and data.
var dataEnvKeys = []string{
	"CLINE_DIR",
	"CLINE_DATA_DIR",
	"CLINE_DB_DATA_DIR",
	"CLINE_SESSION_DATA_DIR",
	"CLINE_PROVIDER_SETTINGS_PATH",
	"CLINE_GLOBAL_SETTINGS_PATH",
}

// Where Cline keeps its settings, relative to the data directory. These follow
// resolveClineDir, resolveClineDataDir and resolveProviderSettingsPath in
// Cline's sdk/packages/shared/src/storage/paths.ts.
const (
	clineDirName          = ".cline"
	dataDirName           = "data"
	settingsDirName       = "settings"
	providersFileName     = "providers.json"
	envProviderSettings   = "CLINE_PROVIDER_SETTINGS_PATH"
	envClineDir           = "CLINE_DIR"
	envClineDataDir       = "CLINE_DATA_DIR"
	defaultClineProvider  = "cline"
	maxProviderFileLength = 4 << 20
)

// providerSettingsPath resolves providers.json the way Cline does: the explicit
// path, else the settings directory of the data directory, whose default lies
// under the Cline directory in the home directory.
func providerSettingsPath(getenv func(string) string, home string) string {
	if path := strings.TrimSpace(getenv(envProviderSettings)); path != "" {
		return path
	}
	dataDir := strings.TrimSpace(getenv(envClineDataDir))
	if dataDir == "" {
		clineDir := strings.TrimSpace(getenv(envClineDir))
		if clineDir == "" {
			if home == "" {
				return ""
			}
			clineDir = filepath.Join(home, clineDirName)
		}
		dataDir = filepath.Join(clineDir, dataDirName)
	}
	return filepath.Join(dataDir, settingsDirName, providersFileName)
}

// providerSelection is the provider and the model that the user's Cline
// settings select.
type providerSelection struct {
	Provider string
	// Model is the model that the provider's settings state, or "" when they
	// state none.
	Model string
}

// readProviderSelection reads the selection from providers.json. An absent file
// selects Cline's own default provider with no model, as Cline's CLI does, and
// the first turn then states what Cline needs.
//
// A file that cannot be read, that is not JSON, or that fails the check of
// Cline's own schema is an error. Cline reads a file that fails its schema as no
// settings at all, so a session that states the file's provider would run on
// that provider's built-in endpoint and key, which the user did not choose.
//
// A path that is not a regular file is an error too, and the check comes before
// the open: a profile can point CLINE_PROVIDER_SETTINGS_PATH at a FIFO, and the
// open of a FIFO waits for a writer with no deadline.
func readProviderSelection(path string) (providerSelection, error) {
	if path == "" {
		return providerSelection{Provider: defaultClineProvider}, nil
	}
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return providerSelection{Provider: defaultClineProvider}, nil
	}
	if err != nil {
		return providerSelection{}, fmt.Errorf("read the Cline provider settings: %w", err)
	}
	if !info.Mode().IsRegular() {
		return providerSelection{}, fmt.Errorf("the Cline provider settings %s are not a regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return providerSelection{}, fmt.Errorf("read the Cline provider settings: %w", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxProviderFileLength+1))
	if err != nil {
		return providerSelection{}, fmt.Errorf("read the Cline provider settings: %w", err)
	}
	if len(data) > maxProviderFileLength {
		return providerSelection{}, fmt.Errorf("the Cline provider settings %s exceed %d bytes", path, maxProviderFileLength)
	}
	var syntax any
	if err := json.Unmarshal(data, &syntax); err != nil {
		return providerSelection{}, fmt.Errorf("the Cline provider settings %s are not valid JSON: %w", path, err)
	}
	stored, err := decodeProviderSettings(data)
	if err != nil {
		return providerSelection{}, fmt.Errorf("the Cline provider settings %s do not match Cline's format, and Cline ignores such a file: %w; run `cline auth` to write the file again", path, err)
	}
	provider := strings.TrimSpace(stored.lastUsedProvider)
	if provider == "" {
		provider = defaultClineProvider
	}
	return providerSelection{
		Provider: provider,
		Model:    strings.TrimSpace(stored.models[provider]),
	}, nil
}

// storedProviderSettings holds what the worker reads of providers.json: the last
// used provider, and the model of each provider. The keys and the tokens stay
// unread.
type storedProviderSettings struct {
	lastUsedProvider string
	models           map[string]string
}

// The patterns of Cline's settings schema (the provider settings manager of
// Cline 3.0.64, `sdk/packages/shared/src/storage/provider-settings*`).
var (
	// clineProviderID is the form of a provider id in a provider's settings.
	clineProviderID = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9-]*$`)
	// clineDateTime is the form of zod's `string().datetime()`, which the schema
	// takes for `updatedAt`: UTC with a `Z`, and no offset.
	clineDateTime = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})T([01]\d|2[0-3]):[0-5]\d(?::[0-5]\d(?:\.\d+)?)?Z$`)
	// clineTokenSources are the values of an entry's `tokenSource`.
	clineTokenSources = map[string]bool{"manual": true, "oauth": true, "migration": true}
)

// decodeProviderSettings checks providers.json as Cline's schema checks it, and
// reads the selection out of it. The check covers the whole envelope and the
// fields of a provider's settings that locate its endpoint: `provider`, `model`,
// `apiKey` and `baseUrl`. Cline checks the other settings fields too, and the
// worker does not repeat those checks.
func decodeProviderSettings(data []byte) (storedProviderSettings, error) {
	envelope, ok := jsonObject(data)
	if !ok {
		return storedProviderSettings{}, errors.New("the file holds no JSON object")
	}
	// A JSON number, as JavaScript compares it: `1.0` is 1 too.
	var version float64
	if raw, present := envelope["version"]; !present || json.Unmarshal(raw, &version) != nil || version != 1 {
		return storedProviderSettings{}, errors.New("`version` is not 1")
	}
	stored := storedProviderSettings{models: make(map[string]string)}
	if raw, present := envelope["lastUsedProvider"]; present {
		provider, ok := jsonString(raw)
		if !ok || provider == "" {
			return storedProviderSettings{}, errors.New("`lastUsedProvider` is not a non-empty string")
		}
		stored.lastUsedProvider = provider
	}
	if raw, present := envelope["modes"]; present {
		if _, ok := jsonObject(raw); !ok {
			return storedProviderSettings{}, errors.New("`modes` is not an object")
		}
	}
	raw, present := envelope["providers"]
	if !present {
		return storedProviderSettings{}, errors.New("`providers` is missing")
	}
	providers, ok := jsonObject(raw)
	if !ok {
		return storedProviderSettings{}, errors.New("`providers` is not an object")
	}
	// In the order of the ids, so a file with several broken entries always
	// reports the same one.
	for _, id := range slices.Sorted(maps.Keys(providers)) {
		model, err := decodeProviderEntry(providers[id])
		if err != nil {
			return storedProviderSettings{}, fmt.Errorf("the entry of provider %q: %w", id, err)
		}
		stored.models[id] = model
	}
	return stored, nil
}

// decodeProviderEntry checks one entry of `providers` and returns the model of
// its settings, or "" when they state none.
func decodeProviderEntry(raw json.RawMessage) (string, error) {
	entry, ok := jsonObject(raw)
	if !ok {
		return "", errors.New("it is not an object")
	}
	if updatedAt, ok := jsonString(entry["updatedAt"]); !ok || !isClineDateTime(updatedAt) {
		return "", errors.New("`updatedAt` is not a UTC date and time")
	}
	if rawSource, present := entry["tokenSource"]; present {
		if source, ok := jsonString(rawSource); !ok || !clineTokenSources[source] {
			return "", errors.New("`tokenSource` is not manual, oauth or migration")
		}
	}
	settings, ok := jsonObject(entry["settings"])
	if !ok {
		return "", errors.New("`settings` is not an object")
	}
	if provider, ok := jsonString(settings["provider"]); !ok || !clineProviderID.MatchString(provider) {
		return "", errors.New("`settings.provider` is not a provider id")
	}
	fields := map[string]string{}
	for _, name := range []string{"model", "apiKey", "baseUrl"} {
		value, present := settings[name]
		if !present {
			continue
		}
		text, ok := jsonString(value)
		if !ok {
			return "", fmt.Errorf("`settings.%s` is not a string", name)
		}
		fields[name] = text
	}
	if baseURL, present := fields["baseUrl"]; present {
		if parsed, err := url.Parse(baseURL); err != nil || parsed.Scheme == "" {
			return "", errors.New("`settings.baseUrl` is not a URL")
		}
	}
	return fields["model"], nil
}

// jsonObject decodes raw as a JSON object. It reports false for anything else,
// null included.
func jsonObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}
	return object, true
}

// jsonString decodes raw as a JSON string. It reports false for anything else,
// null included.
func jsonString(raw json.RawMessage) (string, bool) {
	var text *string
	if len(raw) == 0 || json.Unmarshal(raw, &text) != nil || text == nil {
		return "", false
	}
	return *text, true
}

// isClineDateTime reports whether value has the form of clineDateTime and states
// a real date.
func isClineDateTime(value string) bool {
	match := clineDateTime.FindStringSubmatch(value)
	if match == nil {
		return false
	}
	_, err := time.Parse(time.DateOnly, match[1])
	return err == nil
}

// settingsEnvironment returns a reader of the variables that locate Cline's
// data as the user's shell sets them, and the home directory. It probes the
// shell once; a probe that establishes nothing falls back to getenv and home.
func settingsEnvironment(ctx context.Context, shell string, loginShell bool, getenv func(string) string, home string) (func(string) string, string) {
	names := append([]string{homeEnvName()}, dataEnvKeys...)
	values, result := launch.ShellEnv(ctx, shell, loginShell, names)
	if result != launch.ProbeYes {
		return getenv, home
	}
	if shellHome := values[homeEnvName()]; shellHome != "" {
		home = shellHome
	}
	return func(name string) string { return values[name] }, home
}
