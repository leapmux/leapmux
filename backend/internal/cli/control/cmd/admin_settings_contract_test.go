package cmd

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

func TestAdminSettingResultPreservesExactNumbers(t *testing.T) {
	const key, number = "precision_probe", "9007199254740993"
	hub := &fakeAdminHub{values: []*leapmuxv1.SettingValue{{
		Key: key, ValueJson: number, EffectiveJson: number, Customized: true,
	}}}
	url := startAdminHub(t, hub)
	output := runControl(t, "admin", "settings", "get", key, "--hub", url).ok(t)
	var envelope struct {
		Data struct {
			EffectiveValue json.RawMessage `json:"effective_json"`
			StoredValue    json.RawMessage `json:"value_json"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(output, &envelope))
	assert.Equal(t, number, string(envelope.Data.EffectiveValue))
	assert.Equal(t, number, string(envelope.Data.StoredValue))

	text := runControlText(t, "admin", "settings", "get", key, "--hub", url)
	require.Equal(t, cli.ExitOK, text.Code, text.Stderr)
	assert.Contains(t, text.Stdout, number)
}

func TestAdminSettingsGetReportsAnAbsentKey(t *testing.T) {
	url := startAdminHub(t, &fakeAdminHub{})
	out := runControl(t, "admin", "settings", "get", "absent_probe", "--hub", url)
	failure := out.requireFailure(t, cli.CodeNotFound)
	assert.Contains(t, failure.Message, "absent_probe")
	assert.Contains(t, failure.Hints, "run leapmux admin settings list to list setting keys")
}

func TestAdminSettingsGetKeepsEmptyDescriptorFields(t *testing.T) {
	const key = "empty-descriptor"
	url := startAdminHub(t, &fakeAdminHub{
		values:      []*leapmuxv1.SettingValue{{Key: key}},
		descriptors: []*leapmuxv1.SettingDescriptor{{Key: key}},
	})
	data := envelopeData(t, runControl(t, "admin", "settings", "get", key, "--hub", url).ok(t))
	assert.Equal(t, "", data["description"])
	assert.Equal(t, "", data["value_schema"])
}

func TestAdminSettingsListDistinguishesAbsentAndExplicitValues(t *testing.T) {
	const nested = `{"threshold":9007199254740993,"enabled":false,"retry":0}`
	url := startAdminHub(t, &fakeAdminHub{values: []*leapmuxv1.SettingValue{
		{Key: "unset"},
		{Key: "explicit-null", ValueJson: "null", EffectiveJson: "null", Customized: true},
		{Key: "nested", ValueJson: nested, EffectiveJson: nested, Customized: true},
		{Key: "invalid", ValueJson: "raw text", EffectiveJson: "raw text", Customized: true},
	}})
	output := runControl(t, "admin", "settings", "list", "--hub", url).ok(t)
	var envelope struct {
		Data struct {
			Settings []struct {
				Key       string          `json:"key"`
				Effective json.RawMessage `json:"effective_json"`
				Stored    json.RawMessage `json:"value_json"`
			} `json:"settings"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(output, &envelope))
	require.Len(t, envelope.Data.Settings, 4)
	for i, want := range []struct {
		key, effective, stored string
	}{
		{key: "unset", effective: "null"},
		{key: "explicit-null", effective: "null", stored: "null"},
		{key: "nested", effective: nested, stored: nested},
		{key: "invalid", effective: `"raw text"`, stored: `"raw text"`},
	} {
		got := envelope.Data.Settings[i]
		assert.Equal(t, want.key, got.Key)
		assert.Equal(t, want.effective, string(got.Effective), want.key)
		assert.Equal(t, want.stored, string(got.Stored), want.key)
	}
}

func TestAdminRowsUseEffectiveVerificationAndEnumTokens(t *testing.T) {
	app := appRegistrationRowOf(&leapmuxv1.App{
		ClientId:   "built-in",
		Verified:   true,
		Visibility: leapmuxv1.AppVisibility_APP_VISIBILITY_HUB_WIDE,
		ClientType: leapmuxv1.AppClientType_APP_CLIENT_TYPE_PUBLIC,
	})
	wire, err := json.Marshal(app)
	require.NoError(t, err)
	var exposed struct {
		Verified   bool   `json:"verified"`
		Visibility string `json:"visibility"`
		ClientType string `json:"client_type"`
	}
	require.NoError(t, json.Unmarshal(wire, &exposed))
	assert.True(t, exposed.Verified)
	assert.Equal(t, "hub_wide", exposed.Visibility)
	assert.Equal(t, "public", exposed.ClientType)
	assert.NotContains(t, string(wire), "verified_at", "a built-in has no verification timestamp")
	assert.NotContains(t, string(wire), "client_secret")

	worker := adminWorkerRowOf(&leapmuxv1.AdminWorker{Status: leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED})
	encoded, err := json.Marshal(worker)
	require.NoError(t, err)
	var record struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(encoded, &record))
	assert.Equal(t, "deleted", record.Status)
}
