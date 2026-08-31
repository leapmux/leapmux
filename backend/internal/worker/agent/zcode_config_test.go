package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodeConfigPath(t *testing.T) {
	t.Parallel()

	assert.Equal(t, filepath.Join("/home/u", ".zcode", "v2", "config.json"), zcodeConfigPath("/home/u"))
	assert.Equal(t, "", zcodeConfigPath(""), "no home directory means no path to read")
}

func TestBuildZCodeCatalog_OnlyProvidersWithAKeyAndAModel(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, `{
      "provider": {
        "haskey":    {"kind": "openai", "options": {"apiKey": "k"}, "models": {"m": {}}},
        "nokey":     {"kind": "openai", "options": {"apiKey": ""},  "models": {"m": {}}},
        "blankkey":  {"kind": "openai", "options": {"apiKey": "   "}, "models": {"m": {}}},
        "nomodels":  {"kind": "openai", "options": {"apiKey": "k"}, "models": {}},
        "badkind":   {"kind": "gemini", "options": {"apiKey": "k"}, "models": {"m": {}}}
      }
    }`)

	require.Len(t, catalog.Providers, 1)
	assert.Equal(t, "haskey", catalog.Providers[0].ProviderID)
	require.Len(t, catalog.Models, 1)
	assert.Equal(t, "haskey/m", catalog.Models[0].GetId())
}

// The app-server takes the FIRST registry entry as a session's default, so the order
// must be deterministic and must land on a provider the user enabled.
func TestBuildZCodeCatalog_EnabledProvidersComeFirstThenByID(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, `{
      "provider": {
        "zulu":  {"kind": "openai", "enabled": true,  "options": {"apiKey": "k"}, "models": {"m": {}}},
        "alpha": {"kind": "openai", "enabled": false, "options": {"apiKey": "k"}, "models": {"m": {}}},
        "bravo": {"kind": "openai", "enabled": true,  "options": {"apiKey": "k"}, "models": {"m": {}}},
        "charlie": {"kind": "openai",                 "options": {"apiKey": "k"}, "models": {"m": {}}}
      }
    }`)

	ids := make([]string, 0, len(catalog.Providers))
	for _, p := range catalog.Providers {
		ids = append(ids, p.ProviderID)
	}
	assert.Equal(t, []string{"bravo", "zulu", "alpha", "charlie"}, ids)
}

// A disabled provider that holds a key is still offered. ZCode disables a provider
// for reasons of its own, and dropping it would leave a user whose every provider is
// disabled with no models at all.
func TestBuildZCodeCatalog_DisabledProviderWithAKeyIsStillOffered(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, `{
      "provider": {
        "only": {"kind": "openai", "enabled": false, "options": {"apiKey": "k"}, "models": {"m": {}}}
      }
    }`)

	require.Len(t, catalog.Providers, 1)
	assert.True(t, catalog.hasInlineAPIKey("only"))
}

func TestBuildZCodeCatalog_ModelsAreASortedNonNilArray(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, `{
      "provider": {
        "p": {"kind": "openai", "options": {"apiKey": "k"},
              "models": {"zed": {}, "abe": {}, "mid": {}}}
      }
    }`)

	require.Len(t, catalog.Providers, 1)
	ids := []string{}
	for _, m := range catalog.Providers[0].Models {
		ids = append(ids, m.ModelID)
	}
	assert.Equal(t, []string{"abe", "mid", "zed"}, ids)

	// A nil slice marshals as `null`, which the app-server's validator refuses for
	// the WHOLE request -- so the field must always be an array.
	encoded, err := json.Marshal(catalog.Providers[0])
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"models":[`)
}

func TestBuildZCodeCatalog_EmptyModelsMarshalsAsAnArrayNotNull(t *testing.T) {
	t.Parallel()

	// A provider with no model is skipped, so the empty-array guarantee is checked on
	// the struct directly: it is what a future skip-less path would rely on.
	encoded, err := json.Marshal(zcodeRegistryProvider{ProviderID: "p", Kind: zcodeKindOpenAI, Models: []zcodeRegistryModel{}})
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"models":[]`)
	assert.NotContains(t, string(encoded), `"models":null`)
}

func TestBuildZCodeCatalog_APIKeyIsTheInlineUnionMember(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, `{
      "provider": {"p": {"kind": "openai", "options": {"apiKey": "secret"}, "models": {"m": {}}}}
    }`)

	require.Len(t, catalog.Providers, 1)
	key := catalog.Providers[0].APIKey
	require.NotNil(t, key)
	assert.Equal(t, zcodeAPIKeySourceInline, key.Source)
	assert.Equal(t, "secret", key.Value)
}

func TestZCodeProviderKind_OnlyTheAppServersEnumeration(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		"anthropic":         zcodeKindAnthropic,
		"Anthropic":         zcodeKindAnthropic,
		"  openai  ":        zcodeKindOpenAI,
		"openai-compatible": zcodeKindOpenAICompatible,
	} {
		got, ok := zcodeProviderKind(input)
		assert.Truef(t, ok, "kind %q must be accepted", input)
		assert.Equal(t, want, got)
	}
	for _, input := range []string{"", "gemini", "azure", "openai_compatible"} {
		_, ok := zcodeProviderKind(input)
		assert.Falsef(t, ok, "kind %q must be refused", input)
	}
}

func TestZCodeRegistrySource_FallsBackToCustom(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "builtin", zcodeRegistrySource("Builtin"))
	assert.Equal(t, "models-dev", zcodeRegistrySource("models-dev"))
	assert.Equal(t, "custom", zcodeRegistrySource("something-else"))
	assert.Equal(t, "custom", zcodeRegistrySource(""))
}

func TestZCodeAPIFormat(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "anthropic-messages", zcodeAPIFormat("anthropic"))
	assert.Equal(t, "openai-chat-completions", zcodeAPIFormat("openai"))
	assert.Equal(t, "openai-chat-completions", zcodeAPIFormat("openai-compatible"))
	assert.Equal(t, "", zcodeAPIFormat("gemini"), "an unknown kind omits the field rather than guessing")
}

func TestZCodeRegistryModelFor_ReasoningAndModalityFlags(t *testing.T) {
	t.Parallel()

	var model zcodeConfigModel
	require.NoError(t, json.Unmarshal([]byte(`{
      "name": "GLM-5.3",
      "reasoning": {"enabled": true, "variants": ["low", "high"], "defaultVariant": "high"},
      "limit": {"context": 200000, "output": 32000},
      "modalities": {"input": ["text", "Image", "pdf", "video"]}
    }`), &model))

	entry := zcodeRegistryModelFor("GLM-5.3", model)
	assert.Equal(t, "GLM-5.3", entry.ModelID)
	assert.Equal(t, "GLM-5.3", entry.Label)
	assert.Equal(t, int64(200000), entry.ContextWindow)
	assert.Equal(t, int64(32000), entry.MaxOutputTokens)
	require.NotNil(t, entry.Reasoning)
	assert.True(t, entry.Reasoning.Enabled)
	assert.Equal(t, "high", entry.Reasoning.DefaultLevel)
	assert.Equal(t, []zcodeReasoningLevel{{Value: "low", Label: "low"}, {Value: "high", Label: "high"}}, entry.Reasoning.Levels)
	assert.True(t, entry.SupportsImages, "a mixed-case modality must still be recognized")
	assert.True(t, entry.SupportsPdf)
	assert.True(t, entry.SupportsVideo)
}

func TestZCodeRegistryModelFor_ReasoningDisabledOrEmptyIsOmitted(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]string{
		"absent":       `{}`,
		"disabled":     `{"reasoning": {"enabled": false, "variants": ["low"]}}`,
		"no variants":  `{"reasoning": {"enabled": true, "variants": []}}`,
		"null variant": `{"reasoning": {"enabled": true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var model zcodeConfigModel
			require.NoError(t, json.Unmarshal([]byte(raw), &model))
			assert.Nil(t, zcodeRegistryModelFor("m", model).Reasoning)
		})
	}
}

func TestZCodeModelInfo_EffortsComeFromTheModelsOwnVariants(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, zcodeTwoProviderConfig)

	byID := map[string]*ModelInfo{}
	for _, m := range catalog.Models {
		byID[m.GetId()] = m
	}

	reasoning := byID["builtin:zai/GLM-5.3"]
	require.NotNil(t, reasoning)
	assert.Equal(t, "GLM-5.3", reasoning.DisplayName)
	assert.Equal(t, "Z.ai", reasoning.Description,
		"the provider's configured display name distinguishes two providers offering the same model id")
	assert.Equal(t, int64(200000), reasoning.GetContextWindow())
	assert.Equal(t, "high", reasoning.DefaultEffort)
	efforts := []string{}
	for _, e := range reasoning.SupportedEfforts {
		efforts = append(efforts, e.GetId())
	}
	assert.Equal(t, []string{EffortAuto, "low", "high", "max"}, efforts,
		"auto leads, then the model's own variants in their configured order")

	// A model with no reasoning block declares no effort axis at all, rather than a
	// hardcoded low/medium/high the app-server would refuse.
	plain := byID["builtin:zai/GLM-5.3-Flash"]
	require.NotNil(t, plain)
	assert.Empty(t, plain.SupportedEfforts)
	assert.Equal(t, "GLM-5.3 Flash", plain.DisplayName)
}

func TestZCodeModelInfo_DefaultEffortFallsBackToAuto(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, `{
      "provider": {"p": {"kind": "openai", "options": {"apiKey": "k"},
        "models": {"m": {"reasoning": {"enabled": true, "variants": ["low"]}}}}}
    }`)
	require.Len(t, catalog.Models, 1)
	assert.Equal(t, EffortAuto, catalog.Models[0].DefaultEffort,
		"a variant list with no declared default must not pin one")
}

func TestMarkZCodeDefaultModel_FlagsTheFirstEntryOnly(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, zcodeTwoProviderConfig)
	require.Greater(t, len(catalog.Models), 1)
	assert.True(t, catalog.Models[0].IsDefault)
	for _, m := range catalog.Models[1:] {
		assert.False(t, m.IsDefault)
	}

	// Nil-tolerant: a nil entry must be skipped rather than dereferenced.
	models := []*ModelInfo{nil, {Id: "second"}}
	markZCodeDefaultModel(models)
	assert.True(t, models[1].IsDefault)
	markZCodeDefaultModel(nil)
	markZCodeDefaultModel([]*ModelInfo{})
}

func TestZCodeModelIDHelpers(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "p/m", zcodeModelID("p", "m"))
	assert.Equal(t, "m", zcodeModelID("", "m"), "a provider-less id stays bare")

	provider, model, ok := splitZCodeModelID("builtin:zai/GLM-5.3")
	assert.True(t, ok)
	assert.Equal(t, "builtin:zai", provider)
	assert.Equal(t, "GLM-5.3", model)

	// A model id that itself contains a separator splits on the FIRST one, so a
	// provider id is never mistaken for part of the model.
	provider, model, ok = splitZCodeModelID("openrouter/anthropic/claude")
	assert.True(t, ok)
	assert.Equal(t, "openrouter", provider)
	assert.Equal(t, "anthropic/claude", model)

	for _, bare := range []string{"GLM-5.3", "", "/m", "p/"} {
		_, _, ok := splitZCodeModelID(bare)
		assert.Falsef(t, ok, "%q is not a composite id", bare)
	}
}

func TestNormalizeZCodeModelID(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "p/m", normalizeZCodeModelID(`p\m`), "a backslash spelling must not read as a model switch")
	assert.Equal(t, "p/m", normalizeZCodeModelID("  p/m  "))
	assert.Equal(t, "", normalizeZCodeModelID("   "))
	assert.Equal(t, "GLM-5.3", normalizeZCodeModelID("GLM-5.3"), "a bare id passes through unchanged")
}

func TestZCodeCatalog_ResolveModelID(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, zcodeTwoProviderConfig)

	resolved, ok := catalog.resolveModelID("builtin:zai/GLM-5.3")
	assert.True(t, ok)
	assert.Equal(t, "builtin:zai/GLM-5.3", resolved)

	resolved, ok = catalog.resolveModelID(`builtin:zai\GLM-5.3`)
	assert.True(t, ok, "the backslash spelling resolves to the same model")
	assert.Equal(t, "builtin:zai/GLM-5.3", resolved)

	resolved, ok = catalog.resolveModelID("GLM-5.3")
	assert.True(t, ok, "a bare model id resolves against the catalog")
	assert.Equal(t, "builtin:zai/GLM-5.3", resolved)

	for _, unknown := range []string{"", "   ", "nope", "builtin:zai/nope", "other/GLM-5.3"} {
		_, ok := catalog.resolveModelID(unknown)
		assert.Falsef(t, ok, "%q must not resolve", unknown)
	}
}

// A bare id resolves to the first provider that offers it, which is the enabled one
// by the catalog's ordering -- so a user typing `--model m` lands on the provider
// they chose in ZCode.
func TestZCodeCatalog_ResolveBareModelIDPrefersTheEnabledProvider(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, `{
      "provider": {
        "aaa": {"kind": "openai", "enabled": false, "options": {"apiKey": "k"}, "models": {"shared": {}}},
        "zzz": {"kind": "openai", "enabled": true,  "options": {"apiKey": "k"}, "models": {"shared": {}}}
      }
    }`)

	resolved, ok := catalog.resolveModelID("shared")
	assert.True(t, ok)
	assert.Equal(t, "zzz/shared", resolved)
}

func TestZCodeCatalog_AcceptsInputModality(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, zcodeTwoProviderConfig)

	assert.True(t, catalog.acceptsInputModality("builtin:zai/GLM-5.3-Flash", zcodeModalityImage))
	assert.False(t, catalog.acceptsInputModality("builtin:zai/GLM-5.3", zcodeModalityImage))
	assert.True(t, catalog.acceptsInputModality("builtin:zai/GLM-5.3", zcodeModalityText))
	assert.True(t, catalog.acceptsInputModality(`builtin:zai\GLM-5.3-Flash`, zcodeModalityImage),
		"the gate normalizes the id, so a re-spelled separator does not lose the capability")

	// A model that declares no modality at all is text-only, which is what the
	// app-server assumes for it too.
	assert.True(t, catalog.acceptsInputModality("acme/acme-1", zcodeModalityText))
	assert.False(t, catalog.acceptsInputModality("acme/acme-1", zcodeModalityImage))
	assert.False(t, catalog.acceptsInputModality("unknown/model", zcodeModalityImage))
}

func TestZCodeCatalog_RegistryPayloadNestsTheRegistryObject(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, zcodeTwoProviderConfig)
	payload := catalog.registryPayload(zcodeWorkspaceFor("/w"), "rev-1", 1700)

	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	var got struct {
		Workspace zcodeWorkspace `json:"workspace"`
		Registry  struct {
			Providers   []zcodeRegistryProvider `json:"providers"`
			GeneratedAt int64                   `json:"generatedAt"`
			Revision    string                  `json:"revision"`
		} `json:"registry"`
	}
	require.NoError(t, json.Unmarshal(encoded, &got))

	assert.Equal(t, "/w", got.Workspace.WorkspacePath)
	assert.Equal(t, "/w", got.Workspace.WorkspaceKey)
	assert.Equal(t, "rev-1", got.Registry.Revision)
	assert.Equal(t, int64(1700), got.Registry.GeneratedAt)
	assert.Len(t, got.Registry.Providers, 2)

	// The three fields must NOT also appear at the top level: the app-server refuses
	// the whole request with an "unrecognized keys" report when they do.
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &top))
	for _, key := range []string{"providers", "generatedAt", "revision"} {
		assert.NotContains(t, top, key)
	}
}

func TestZCodeCatalog_RuntimeModelForCarriesTheProvidersKey(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, zcodeTwoProviderConfig)

	overlay, ok := catalog.runtimeModelFor("builtin:zai/GLM-5.3", "rev-2", 99)
	require.True(t, ok)
	assert.Equal(t, "rev-2", overlay.Revision)
	assert.Equal(t, int64(99), overlay.GeneratedAt)
	assert.Equal(t, zcodeModelRef{ProviderID: "builtin:zai", ModelID: "GLM-5.3"}, overlay.Model)
	require.NotNil(t, overlay.Provider.APIKey)
	assert.Equal(t, "zai-key", overlay.Provider.APIKey.Value,
		"the overlay must carry the credential, so a model switch does not race the registry push")
	assert.Equal(t, "", overlay.ThoughtLevel, "no level is implied by the overlay itself")

	_, ok = catalog.runtimeModelFor("nope/nope", "rev", 1)
	assert.False(t, ok)
}

func TestZCodeCatalog_HasInlineAPIKey(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, zcodeTwoProviderConfig)

	assert.True(t, catalog.hasInlineAPIKey("builtin:zai"))
	assert.True(t, catalog.hasInlineAPIKey(""), "an empty provider id asks about any provider")
	assert.False(t, catalog.hasInlineAPIKey("unknown"))
	assert.False(t, zcodeCatalog{}.hasInlineAPIKey(""))
}

func TestLoadZCodeCatalog_MissingMalformedAndKeylessConfigurations(t *testing.T) {
	t.Parallel()

	t.Run("no home directory", func(t *testing.T) {
		t.Parallel()
		_, err := loadZCodeCatalog("")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no home directory")
	})

	t.Run("file does not exist", func(t *testing.T) {
		t.Parallel()
		_, err := loadZCodeCatalog(t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ZCode is not configured")
		assert.Contains(t, err.Error(), "config.json", "the message must name the file the user has to create")
	})

	t.Run("malformed json", func(t *testing.T) {
		t.Parallel()
		home := writeZCodeConfig(t, `{"provider":`)
		_, err := loadZCodeCatalog(home)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse")
	})

	// The three skips have three different remedies -- add a key, add a model, use a
	// supported kind -- so each error must state its own. One message that always said
	// "carries no API key" sent the user to re-enter a credential that was correct.
	t.Run("no provider carries a key", func(t *testing.T) {
		t.Parallel()
		home := writeZCodeConfig(t, `{"provider":{"p":{"kind":"openai","options":{},"models":{"m":{}}}}}`)
		_, err := loadZCodeCatalog(home)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"p" carries no API key`)
	})

	t.Run("the only provider lists no model", func(t *testing.T) {
		t.Parallel()
		home := writeZCodeConfig(t, `{"provider":{"p":{"kind":"openai","options":{"apiKey":"k"},"models":{}}}}`)
		_, err := loadZCodeCatalog(home)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"p" lists no model`)
		assert.NotContains(t, err.Error(), "API key", "the key is present; saying otherwise sends the user to the wrong remedy")
	})

	t.Run("the only provider uses an unsupported kind", func(t *testing.T) {
		t.Parallel()
		home := writeZCodeConfig(t, `{"provider":{"p":{"kind":"gemini","options":{"apiKey":"k"},"models":{"m":{}}}}}`)
		_, err := loadZCodeCatalog(home)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"p" uses the unsupported kind "gemini"`)
		assert.NotContains(t, err.Error(), "API key")
	})

	t.Run("every skip reason is reported, in a stable order", func(t *testing.T) {
		t.Parallel()
		home := writeZCodeConfig(t, `{"provider":{
			"c-kind":{"kind":"gemini","options":{"apiKey":"k"},"models":{"m":{}}},
			"a-key":{"kind":"openai","options":{},"models":{"m":{}}},
			"b-models":{"kind":"openai","options":{"apiKey":"k"},"models":{}}
		}}`)
		_, err := loadZCodeCatalog(home)
		require.Error(t, err)
		// Sorted by provider id: a Go map yields its entries in a different order on
		// every run, and these reach the user inside an error string.
		assert.Contains(t, err.Error(),
			`"a-key" carries no API key; "b-models" lists no model; "c-kind" uses the unsupported kind "gemini"`)
	})

	t.Run("empty object", func(t *testing.T) {
		t.Parallel()
		home := writeZCodeConfig(t, `{}`)
		_, err := loadZCodeCatalog(home)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "it lists no model provider",
			"a configuration with no provider at all has no skip to explain")
	})

	t.Run("a usable configuration loads", func(t *testing.T) {
		t.Parallel()
		home := writeZCodeConfig(t, zcodeTwoProviderConfig)
		catalog, err := loadZCodeCatalog(home)
		require.NoError(t, err)
		assert.Len(t, catalog.Providers, 2)
		assert.Len(t, catalog.Models, 3)
	})
}

func writeZCodeConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".zcode", "v2")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600))
	return home
}

// --- model order ---
//
// The order of a provider's models is load-bearing twice: it is the order of the
// registry's `models` array, and the app-server starts a session on the FIRST model
// of the FIRST provider. ZCode states that order itself, as `zcode.priority`.

const zcodePriorityConfig = `{
  "provider": {
    "builtin:zai": {
      "name": "Z.ai",
      "kind": "anthropic",
      "enabled": true,
      "options": {"apiKey": "zai-key"},
      "models": {
        "GLM-5-Turbo": {"zcode": {"priority": 101}},
        "GLM-5.3": {"zcode": {"priority": 99}},
        "GLM-5.3-Flash": {"zcode": {"priority": 100}},
        "b-unranked": {},
        "a-unranked": {}
      }
    }
  }
}`

func TestZCodeModelOrder_RanksByPriorityThenID(t *testing.T) {
	t.Parallel()

	var cfg zcodeConfigFile
	require.NoError(t, json.Unmarshal([]byte(zcodePriorityConfig), &cfg))

	assert.Equal(t,
		[]string{"GLM-5.3", "GLM-5.3-Flash", "GLM-5-Turbo", "a-unranked", "b-unranked"},
		zcodeModelOrder(cfg.Provider["builtin:zai"].Models),
		"lower priority first; a model that states none sorts last, by id")

	assert.Empty(t, zcodeModelOrder(map[string]zcodeConfigModel{}))
}

// The alphabetical order this replaced put GLM-5-Turbo first ('-' sorts before
// '.'), so every ZCode agent opened on the weakest of the three shipped models.
func TestZCodeCatalog_DefaultsToTheHighestPriorityModel(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, zcodePriorityConfig)

	require.NotEmpty(t, catalog.Models)
	assert.Equal(t, "builtin:zai/GLM-5.3", catalog.Models[0].GetId())
	assert.True(t, catalog.Models[0].IsDefault)
	for _, m := range catalog.Models[1:] {
		assert.False(t, m.IsDefault, "exactly one model is the default")
	}

	require.Len(t, catalog.Providers, 1)
	ids := []string{}
	for _, m := range catalog.Providers[0].Models {
		ids = append(ids, m.ModelID)
	}
	assert.Equal(t, []string{"GLM-5.3", "GLM-5.3-Flash", "GLM-5-Turbo", "a-unranked", "b-unranked"}, ids,
		"the pushed registry carries the same order, which is what the app-server's own default reads")
}

// A priority of zero is a RANK, not an absent one.
func TestZCodeModelOrder_ZeroIsARank(t *testing.T) {
	t.Parallel()

	var cfg zcodeConfigFile
	require.NoError(t, json.Unmarshal([]byte(`{"provider":{"p":{"options":{"apiKey":"k"},"models":{
	  "ranked-last": {"zcode": {"priority": 5}},
	  "ranked-first": {"zcode": {"priority": 0}},
	  "unranked": {"zcode": {}}
	}}}}`), &cfg))

	assert.Equal(t, []string{"ranked-first", "ranked-last", "unranked"},
		zcodeModelOrder(cfg.Provider["p"].Models))
}

func TestZCodeCatalog_DefaultThoughtLevel(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, zcodeTwoProviderConfig)

	assert.Equal(t, "high", catalog.defaultThoughtLevel("builtin:zai/GLM-5.3"),
		"the model's own defaultVariant")
	assert.Equal(t, "high", catalog.defaultThoughtLevel(`builtin:zai\GLM-5.3`),
		"the backslash spelling resolves to the same model")
	assert.Empty(t, catalog.defaultThoughtLevel("builtin:zai/GLM-5.3-Flash"),
		"a model with no reasoning block declares no default")
	assert.Empty(t, catalog.defaultThoughtLevel("no-such-model"))
	assert.Empty(t, catalog.defaultThoughtLevel(""))
}

// The emptiness test and the pushed credential must be the SAME string. A key the test
// accepted only after trimming, but that reached the registry with its padding, puts a
// newline in an Authorization header and fails every turn upstream.
func TestBuildZCodeCatalog_TheAPIKeyIsTrimmedBeforeItIsPushed(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, `{"provider":{"p":{
	  "kind":"openai","options":{"apiKey":" sk-abc\n","baseURL":"  https://x  "},
	  "models":{"m":{}}}}}`)

	require.Len(t, catalog.Providers, 1)
	require.NotNil(t, catalog.Providers[0].APIKey)
	assert.Equal(t, "sk-abc", catalog.Providers[0].APIKey.Value)
	assert.Equal(t, "https://x", catalog.Providers[0].BaseURL)
}

// A key that is only whitespace is still absent, and the provider is skipped whole.
func TestBuildZCodeCatalog_AWhitespaceOnlyAPIKeyIsAbsent(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t,
		`{"provider":{"p":{"kind":"openai","options":{"apiKey":"   "},"models":{"m":{}}}}}`)

	assert.Empty(t, catalog.Providers)
}
