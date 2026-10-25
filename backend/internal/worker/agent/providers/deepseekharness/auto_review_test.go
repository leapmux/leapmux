package deepseekharness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestPrepareAutoReviewOverlayInsertsTheBundledLayer(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	overlay, err := prepareAutoReviewOverlay(directory)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(directory, "auto-review.patch.yml"), overlay)
	raw, err := os.ReadFile(overlay)
	require.NoError(t, err)
	// The row of the package's own cordis.patch.yml in dsh 0.2.1-rc.2.
	assert.JSONEq(t, `[{"insert":[{"id":"auto-review","name":"@deepseek-ai/dsh-experimental-auto-review"}]}]`, string(raw))
	if runtime.GOOS != "windows" {
		info, err := os.Stat(overlay)
		require.NoError(t, err)
		assert.Zero(t, info.Mode().Perm()&0o077)
	}
}

func TestPrepareAutoReviewOverlayFailsWithoutItsDirectory(t *testing.T) {
	t.Parallel()
	_, err := prepareAutoReviewOverlay(filepath.Join(t.TempDir(), "absent"))
	require.Error(t, err)
}

func TestNativePermissionCatalogOffersAutoReview(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		// The reply of dsh 0.2.1-rc.2 with the Auto review layer live.
		{name: "a live layer", raw: `{"options":[{"value":"read-only","name":"read-only"},{"value":"workspace-write","name":"workspace-write"},{"value":"danger-full-access","name":"danger-full-access"},{"value":"auto","name":"auto"}],"defaultOptions":[{"value":"read-only","name":"read-only"},{"value":"workspace-write","name":"workspace-write"},{"value":"danger-full-access","name":"danger-full-access"}],"defaultPreset":"workspace-write"}`, want: true},
		{name: "no layer", raw: `{"options":[{"value":"read-only"},{"value":"workspace-write"},{"value":"danger-full-access"}],"defaultPreset":"workspace-write"}`, want: false},
		{name: "Auto among the defaults alone", raw: `{"options":[{"value":"read-only"}],"defaultOptions":[{"value":"auto"}]}`, want: false},
		{name: "a name without its value", raw: `{"options":[{"name":"auto"}]}`, want: false},
		{name: "an empty catalog", raw: `{}`, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var catalog nativePermissionCatalog
			require.NoError(t, json.Unmarshal([]byte(tc.raw), &catalog))
			assert.Equal(t, tc.want, catalog.offersAutoReview())
		})
	}
}

func TestLoadAutoReviewReadsTheNativeCatalog(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		result map[string]any
		want   bool
	}{
		{name: "a catalog with Auto", result: map[string]any{"ok": true, "value": map[string]any{"options": []any{map[string]any{"value": contracts.DeepseekHarnessPermissionPresetWorkspaceWrite}, map[string]any{"value": contracts.DeepseekHarnessPermissionPresetAuto}}}}, want: true},
		{name: "a catalog without Auto", result: map[string]any{"ok": true, "value": map[string]any{"options": []any{map[string]any{"value": contracts.DeepseekHarnessPermissionPresetWorkspaceWrite}}}}, want: false},
		// A release without the catalog method still starts, with the presets that every release offers.
		{name: "a refused read", result: map[string]any{"ok": false, "error": map[string]any{"code": "NOT_FOUND", "message": "no such method"}}, want: false},
		{name: "a reply with no value", result: map[string]any{"ok": true}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newOfflineAgent(t, &agenttest.Sink{})
			serveNativeReplies(t, a, map[string]map[string]any{"permissionPresets/catalog": tc.result})
			assert.Equal(t, tc.want, a.loadAutoReview(context.Background()))
		})
	}
}

func TestStartupPermissionsKeepsAutoOnlyWhileTheNativeCatalogOffersIt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		requested  string
		autoReview bool
		want       string
	}{
		{name: "Auto with the layer", requested: contracts.DeepseekHarnessPermissionPresetAuto, autoReview: true, want: contracts.DeepseekHarnessPermissionPresetAuto},
		{name: "Auto without the layer", requested: contracts.DeepseekHarnessPermissionPresetAuto, autoReview: false, want: contracts.DeepseekHarnessPermissionPresetWorkspaceWrite},
		{name: "Full access without the layer", requested: contracts.DeepseekHarnessPermissionPresetDangerFullAccess, autoReview: false, want: contracts.DeepseekHarnessPermissionPresetDangerFullAccess},
		{name: "Read only with the layer", requested: contracts.DeepseekHarnessPermissionPresetReadOnly, autoReview: true, want: contracts.DeepseekHarnessPermissionPresetReadOnly},
		// The validation of applySessionSettings refuses an unknown preset; the fallback leaves it alone.
		{name: "an unknown preset", requested: "custom", autoReview: false, want: "custom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, startupPermissions("agent", tc.requested, tc.autoReview))
		})
	}
}
