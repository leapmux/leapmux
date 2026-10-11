package cline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestProviderSettingsPathFollowsClinesPrecedence(t *testing.T) {
	t.Parallel()
	env := func(vars map[string]string) func(string) string { return agenttest.FixtureEnv(vars) }
	assert.Equal(t, "/explicit.json", providerSettingsPath(env(map[string]string{envProviderSettings: "/explicit.json", envClineDataDir: "/data"}), "/home"))
	assert.Equal(t, filepath.Join("/data", "settings", "providers.json"), providerSettingsPath(env(map[string]string{envClineDataDir: "/data", envClineDir: "/cline"}), "/home"))
	assert.Equal(t, filepath.Join("/cline", "data", "settings", "providers.json"), providerSettingsPath(env(map[string]string{envClineDir: "/cline"}), "/home"))
	assert.Equal(t, filepath.Join("/home", ".cline", "data", "settings", "providers.json"), providerSettingsPath(env(nil), "/home"))
	assert.Empty(t, providerSettingsPath(env(nil), ""), "no home and no variable locate nothing")
}

func writeProviders(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "providers.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestReadProviderSelection(t *testing.T) {
	t.Parallel()
	selection, err := readProviderSelection(filepath.Join(t.TempDir(), "absent.json"))
	require.NoError(t, err)
	assert.Equal(t, providerSelection{Provider: defaultClineProvider}, selection, "an absent file selects Cline's own provider")

	selection, err = readProviderSelection("")
	require.NoError(t, err)
	assert.Equal(t, providerSelection{Provider: defaultClineProvider}, selection)

	selection, err = readProviderSelection(writeProviders(t, `{"version":1,"lastUsedProvider":"anthropic","modes":{},"providers":{"anthropic":{"settings":{"provider":"anthropic","model":"claude-opus-5","apiKey":"secret"},"updatedAt":"2026-09-24T13:46:05.991Z","tokenSource":"manual"},"openai-native":{"settings":{"provider":"openai-native","model":"gpt-5.6","baseUrl":"https://api.openai.com/v1"},"updatedAt":"2026-09-24T13:46Z"}}}`))
	require.NoError(t, err)
	assert.Equal(t, providerSelection{Provider: "anthropic", Model: "claude-opus-5"}, selection)

	selection, err = readProviderSelection(writeProviders(t, `{"version":1,"providers":{"cline":{"settings":{"provider":"cline","model":"anthropic/claude-opus-5"},"updatedAt":"2026-09-24T13:46:05.991Z","tokenSource":"oauth"}}}`))
	require.NoError(t, err)
	assert.Equal(t, providerSelection{Provider: defaultClineProvider, Model: "anthropic/claude-opus-5"}, selection, "no last provider selects Cline's own")

	selection, err = readProviderSelection(writeProviders(t, `{"version":1.0,"lastUsedProvider":"deepseek","providers":{}}`))
	require.NoError(t, err)
	assert.Equal(t, providerSelection{Provider: "deepseek"}, selection, "a provider with no settings states no model")

	selection, err = readProviderSelection(writeProviders(t, `{"version":1,"lastUsedProvider":"x","providers":{"x":{"settings":{"provider":"x","extra":{"a":1}},"updatedAt":"2028-02-29T00:00:00Z","tokenSource":"migration"}}}`))
	require.NoError(t, err)
	assert.Equal(t, providerSelection{Provider: "x"}, selection, "settings that state no model state none; a field the worker does not check is left alone")
}

// Cline validates the envelope of providers.json and reads a file that fails the
// check as no settings at all: its session then runs on the provider's built-in
// endpoint and key. The worker refuses such a file instead of starting a session
// that would reach an endpoint that the user did not configure.
func TestReadProviderSelectionRefusesAFileThatClineIgnores(t *testing.T) {
	t.Parallel()
	const entry = `{"settings":{"provider":"p","model":"m"},"updatedAt":"2026-09-24T13:46:05Z"}`
	cases := map[string]string{
		"a list":                           `[]`,
		"null":                             `null`,
		"no version":                       `{"providers":{"p":` + entry + `}}`,
		"another version":                  `{"version":2,"providers":{"p":` + entry + `}}`,
		"a version that is text":           `{"version":"1","providers":{"p":` + entry + `}}`,
		"a null version":                   `{"version":null,"providers":{"p":` + entry + `}}`,
		"no providers":                     `{"version":1,"lastUsedProvider":"p"}`,
		"null providers":                   `{"version":1,"providers":null}`,
		"providers that are a list":        `{"version":1,"providers":[]}`,
		"an empty last provider":           `{"version":1,"lastUsedProvider":"","providers":{}}`,
		"a null last provider":             `{"version":1,"lastUsedProvider":null,"providers":{}}`,
		"a last provider that is a number": `{"version":1,"lastUsedProvider":5,"providers":{}}`,
		"null modes":                       `{"version":1,"modes":null,"providers":{}}`,
		"an entry that is text":            `{"version":1,"providers":{"p":"x"}}`,
		"an entry with no settings":        `{"version":1,"providers":{"p":{"updatedAt":"2026-09-24T13:46:05Z"}}}`,
		"null settings":                    `{"version":1,"providers":{"p":{"settings":null,"updatedAt":"2026-09-24T13:46:05Z"}}}`,
		"an entry with no update time":     `{"version":1,"lastUsedProvider":"p","providers":{"p":{"settings":{"provider":"p","model":"m"}}}}`,
		"an update time with an offset":    `{"version":1,"providers":{"p":{"settings":{"provider":"p"},"updatedAt":"2026-09-24T13:46:05+09:00"}}}`,
		"an update time that is no time":   `{"version":1,"providers":{"p":{"settings":{"provider":"p"},"updatedAt":"yesterday"}}}`,
		"an update time out of range":      `{"version":1,"providers":{"p":{"settings":{"provider":"p"},"updatedAt":"2026-13-24T13:46:05Z"}}}`,
		"an update time on no real day":    `{"version":1,"providers":{"p":{"settings":{"provider":"p"},"updatedAt":"2026-02-30T13:46:05Z"}}}`,
		"an unknown token source":          `{"version":1,"providers":{"p":{"settings":{"provider":"p"},"updatedAt":"2026-09-24T13:46:05Z","tokenSource":"other"}}}`,
		"a null token source":              `{"version":1,"providers":{"p":{"settings":{"provider":"p"},"updatedAt":"2026-09-24T13:46:05Z","tokenSource":null}}}`,
		"settings with no provider":        `{"version":1,"providers":{"p":{"settings":{"model":"m"},"updatedAt":"2026-09-24T13:46:05Z"}}}`,
		"a provider id with a space":       `{"version":1,"providers":{"p":{"settings":{"provider":"open ai"},"updatedAt":"2026-09-24T13:46:05Z"}}}`,
		"a model that is a number":         `{"version":1,"providers":{"p":{"settings":{"provider":"p","model":7},"updatedAt":"2026-09-24T13:46:05Z"}}}`,
		"a null key":                       `{"version":1,"providers":{"p":{"settings":{"provider":"p","apiKey":null},"updatedAt":"2026-09-24T13:46:05Z"}}}`,
		"a base URL that is no URL":        `{"version":1,"providers":{"p":{"settings":{"provider":"p","baseUrl":"localhost"},"updatedAt":"2026-09-24T13:46:05Z"}}}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := readProviderSelection(writeProviders(t, content))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "do not match Cline's format")
			assert.Contains(t, err.Error(), "cline auth")
		})
	}
}

func TestReadProviderSelectionRefusesAFileItCannotRead(t *testing.T) {
	t.Parallel()
	_, err := readProviderSelection(writeProviders(t, `{"lastUsedProvider":`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid JSON")

	_, err = readProviderSelection(writeProviders(t, `{"x":"`+strings.Repeat("a", maxProviderFileLength)+`"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceed")

	_, err = readProviderSelection(t.TempDir())
	require.Error(t, err, "a directory is not a settings file")
	assert.Contains(t, err.Error(), "not a regular file")
}

// A file of exactly the limit is read; one byte more is refused.
func TestReadProviderSelectionTakesAFileOfTheLimit(t *testing.T) {
	t.Parallel()
	const content = `{"version":1,"lastUsedProvider":"deepseek","providers":{}}`
	padded := content + strings.Repeat(" ", maxProviderFileLength-len(content))
	require.Len(t, padded, maxProviderFileLength)
	selection, err := readProviderSelection(writeProviders(t, padded))
	require.NoError(t, err)
	assert.Equal(t, providerSelection{Provider: "deepseek"}, selection)

	_, err = readProviderSelection(writeProviders(t, padded+" "))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceed")
}

// The selection trims the words that the file states around the provider and
// the model, and finds the model under the provider's own id.
func TestReadProviderSelectionTrimsTheSelection(t *testing.T) {
	t.Parallel()
	selection, err := readProviderSelection(writeProviders(t, `{"version":1,"lastUsedProvider":" anthropic ","providers":{"anthropic":{"settings":{"provider":"anthropic","model":" claude-opus-5 "},"updatedAt":"2026-09-24T13:46:05Z"}}}`))
	require.NoError(t, err)
	assert.Equal(t, providerSelection{Provider: "anthropic", Model: "claude-opus-5"}, selection)
}

func TestSettingsEnvironmentFallsBackWithoutAShell(t *testing.T) {
	t.Parallel()
	getenv := agenttest.FixtureEnv(map[string]string{envClineDir: "/fixture"})
	read, home := settingsEnvironment(context.Background(), filepath.Join(t.TempDir(), "no-such-shell"), false, getenv, "/fixture-home")
	assert.Equal(t, "/fixture-home", home)
	assert.Equal(t, "/fixture", read(envClineDir))
}
