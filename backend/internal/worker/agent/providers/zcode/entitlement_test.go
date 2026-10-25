package zcode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchStartPlanEntitlementModels_ReadsActivePlanEntitlements(t *testing.T) {
	t.Parallel()

	var gotAuth, gotMid, gotLang, gotVer, gotUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotMid = r.Header.Get("X-Device-Mid")
		gotLang = r.Header.Get("X-Client-Language")
		gotVer = r.Header.Get("X-ZCode-App-Version")
		gotUA = r.Header.Get("User-Agent")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"plans": []map[string]any{{
					"status": "active",
					"entitlements": []map[string]any{
						{"show_name": "GLM-5.3-Flash", "meter": "model_usage"},
						{"show_name": "GLM-5.3", "meter": "model_usage"},
					},
				}},
			},
		})
	}))
	defer server.Close()

	models, err := fetchStartPlanEntitlementModels(context.Background(), server.URL+"/api/v1/zcode-plan/anthropic", "test-jwt", "3.16.3")
	require.NoError(t, err)
	assert.Equal(t, []string{"GLM-5.3-Flash", "GLM-5.3"}, models)
	assert.Equal(t, "Bearer test-jwt", gotAuth)
	assert.Equal(t, "ZCode/3.16.3", gotUA)
	assert.Equal(t, "3.16.3", gotVer)
	assert.NotEmpty(t, gotMid)
	assert.NotEmpty(t, gotLang)
}

func TestFetchStartPlanEntitlementModels_IgnoresInactivePlansAndNonModelEntitlements(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"plans": []map[string]any{
					{
						"status": "expired",
						"entitlements": []map[string]any{
							{"show_name": "GLM-5.2", "meter": "model_usage"},
						},
					},
					{
						"status": "active",
						"entitlements": []map[string]any{
							{"show_name": "search-prime", "meter": "tool_usage"},
							{"show_name": "GLM-5.3-Flash", "meter": "model_usage"},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	models, err := fetchStartPlanEntitlementModels(context.Background(), server.URL+"/anthropic", "key", "3.16.3")
	require.NoError(t, err)
	assert.Equal(t, []string{"GLM-5.3-Flash"}, models)
}

func TestFetchStartPlanEntitlementModels_NonZeroCodeReturnsError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 3001, "msg": "parameter error"})
	}))
	defer server.Close()

	_, err := fetchStartPlanEntitlementModels(context.Background(), server.URL+"/anthropic", "key", "3.16.3")
	require.Error(t, err)
}

func TestFetchStartPlanEntitlementModels_HTTPErrorReturnsError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := fetchStartPlanEntitlementModels(context.Background(), server.URL+"/anthropic", "key", "3.16.3")
	require.Error(t, err)
}

func TestFetchStartPlanEntitlementModels_EmptyBaseURLReturnsError(t *testing.T) {
	t.Parallel()

	_, err := fetchStartPlanEntitlementModels(context.Background(), "", "key", "3.16.3")
	require.Error(t, err)
}

func TestZcodeStartPlanBalanceURL(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		"https://zcode.z.ai/api/v1/zcode-plan/billing/balance",
		zcodeStartPlanBalanceURL("https://zcode.z.ai/api/v1/zcode-plan/anthropic"))
	assert.Equal(t,
		"https://zcode.z.ai/api/v1/zcode-plan/billing/balance",
		zcodeStartPlanBalanceURL("https://zcode.z.ai/api/v1/zcode-plan/anthropic/"))
	assert.Equal(t, "", zcodeStartPlanBalanceURL(""))
	assert.Equal(t, "https://example.com/billing/balance", zcodeStartPlanBalanceURL("https://example.com/anthropic"))
}

func TestZCodeCatalog_AccountProviderPayloadStartPlanUsesEntitlementModels(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, `{
      "provider": {
        "builtin:zai-start-plan": {
          "kind": "anthropic", "enabled": false,
          "options": {"apiKey": "start-key"},
          "models": {"GLM-5.3-Flash": {}}
        }
      }
    }`)
	catalog.startPlanModels = map[string][]string{
		"builtin:zai-start-plan": {"GLM-5.3-Flash", "GLM-5.3"},
	}
	path := filepath.Join(t.TempDir(), "zcode-builtin.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
      "revision":30,
      "config":{"providerConfigRules":{"providerRules":[
        {"providerId":"account:zai-start-plan","config":{"access":{"type":"zhipu-account","accountType":"zai","mode":"start-plan"}}}
      ]}}
    }`), 0o600))

	payload, err := catalog.accountProviderPayload(path, "leapmux-agent")
	require.NoError(t, err)
	provider, ok := payload.Providers["account:zai-start-plan"]
	require.True(t, ok)
	assert.Equal(t, []string{"GLM-5.3-Flash", "GLM-5.3"}, provider.BuiltinModelIDs,
		"the entitlement model list replaces the static builtin list")
}

func TestZCodeCatalog_AccountProviderPayloadStartPlanFallsBackToBuiltinList(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, `{
      "provider": {
        "builtin:zai-start-plan": {
          "kind": "anthropic", "enabled": false,
          "options": {"apiKey": "start-key"},
          "models": {"GLM-5.3-Flash": {}}
        }
      }
    }`)
	path := filepath.Join(t.TempDir(), "zcode-builtin.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
      "revision":30,
      "config":{"providerConfigRules":{"providerRules":[
        {"providerId":"account:zai-start-plan","config":{"access":{"type":"zhipu-account","accountType":"zai","mode":"start-plan"}}}
      ]}}
    }`), 0o600))

	payload, err := catalog.accountProviderPayload(path, "leapmux-agent")
	require.NoError(t, err)
	provider, ok := payload.Providers["account:zai-start-plan"]
	require.True(t, ok)
	assert.Empty(t, provider.BuiltinModelIDs,
		"with no entitlement fetch the app-server keeps its own builtinModelIds")
}

func TestZcodeAppVersion_ReadsInfoPlist(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "Contents", "Resources", "glm"), 0o755))
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleShortVersionString</key><string>3.16.3</string>
</dict></plist>`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Contents", "Info.plist"), []byte(plist), 0o644))
	script := filepath.Join(dir, "Contents", "Resources", "glm", "zcode.cjs")
	assert.Equal(t, "3.16.3", zcodeAppVersion(script))
}

func TestZcodeAppVersion_ReadsPackageJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "resources", "glm"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"@zcode/desktop","version":"2.5.0"}`), 0o644))
	script := filepath.Join(dir, "resources", "glm", "zcode.cjs")
	assert.Equal(t, "2.5.0", zcodeAppVersion(script))
}

func TestZcodeAppVersion_EmptyScriptPathReturnsUnknown(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "unknown", zcodeAppVersion(""))
	assert.Equal(t, "unknown", zcodeAppVersion("/nonexistent/zcode.cjs"))
}

func TestFetchStartPlanEntitlementModels_EmptyAPIKeyReturnsError(t *testing.T) {
	t.Parallel()

	_, err := fetchStartPlanEntitlementModels(context.Background(), "https://zcode.z.ai/api/v1/zcode-plan/anthropic", "", "3.16.3")
	require.Error(t, err)
}

func TestFetchStartPlanEntitlementModels_NullDataReturnsError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": nil})
	}))
	defer server.Close()

	_, err := fetchStartPlanEntitlementModels(context.Background(), server.URL+"/anthropic", "key", "3.16.3")
	require.Error(t, err)
}

func TestZCodeCatalog_AccountProviderPayloadCodingPlanIgnoresStartPlanModels(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, `{
      "provider": {
        "builtin:zai-coding-plan": {
          "kind": "anthropic", "enabled": true,
          "options": {"apiKey": "plan-key"},
          "models": {"GLM-5.3": {}}
        }
      }
    }`)
	catalog.startPlanModels = map[string][]string{
		"builtin:zai-coding-plan": {"SHOULD-NOT-APPEAR"},
	}
	path := filepath.Join(t.TempDir(), "zcode-builtin.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
      "revision":30,
      "config":{"providerConfigRules":{"providerRules":[
        {"providerId":"account:zai-individual-coding-plan","config":{"access":{"type":"zhipu-account","accountType":"zai","mode":"individual-coding-plan"}}}
      ]}}
    }`), 0o600))

	payload, err := catalog.accountProviderPayload(path, "leapmux-agent")
	require.NoError(t, err)
	provider, ok := payload.Providers["account:zai-individual-coding-plan"]
	require.True(t, ok)
	assert.Empty(t, provider.BuiltinModelIDs,
		"a coding-plan provider must not receive start-plan entitlement models")
}

func TestZCodeCatalog_AccountProviderPayloadCopiesEntitlementModels(t *testing.T) {
	t.Parallel()

	catalog := zcodeTestCatalog(t, `{
      "provider": {
        "builtin:zai-start-plan": {
          "kind": "anthropic", "enabled": false,
          "options": {"apiKey": "start-key"},
          "models": {"GLM-5.3-Flash": {}}
        }
      }
    }`)
	original := []string{"GLM-5.3-Flash", "GLM-5.3"}
	catalog.startPlanModels = map[string][]string{
		"builtin:zai-start-plan": original,
	}
	path := filepath.Join(t.TempDir(), "zcode-builtin.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
      "revision":30,
      "config":{"providerConfigRules":{"providerRules":[
        {"providerId":"account:zai-start-plan","config":{"access":{"type":"zhipu-account","accountType":"zai","mode":"start-plan"}}}
      ]}}
    }`), 0o600))

	payload, err := catalog.accountProviderPayload(path, "leapmux-agent")
	require.NoError(t, err)
	provider := payload.Providers["account:zai-start-plan"]
	require.Len(t, provider.BuiltinModelIDs, 2)
	// Mutate the payload slice; the catalog cache must not see the change.
	provider.BuiltinModelIDs[0] = "MUTATED"
	assert.Equal(t, "GLM-5.3-Flash", original[0],
		"the payload must hold a copy, not alias the catalog cache")
}

func TestZcodeAppVersion_InfoPlistWithoutKeyReturnsUnknown(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "Contents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Contents", "Info.plist"),
		[]byte(`<plist><dict><key>CFBundleName</key><string>ZCode</string></dict></plist>`), 0o644))
	script := filepath.Join(dir, "Contents", "Resources", "glm", "zcode.cjs")
	assert.Equal(t, "unknown", zcodeAppVersion(script))
}

func TestZcodeAppVersion_PackageJSONWithoutVersionReturnsUnknown(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "resources", "glm"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"app"}`), 0o644))
	script := filepath.Join(dir, "resources", "glm", "zcode.cjs")
	assert.Equal(t, "unknown", zcodeAppVersion(script))
}
