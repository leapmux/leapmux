package cmd

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/util/version"
)

type fakeUserSettingsHub struct {
	leapmuxv1connect.UnimplementedUserServiceHandler

	mu          sync.Mutex
	descriptors []*leapmuxv1.SettingDescriptor
	values      []*leapmuxv1.SettingValue
	listErr     error
	lastUpdate  *leapmuxv1.UpdateUserSettingRequest
	lastReset   *leapmuxv1.ResetUserSettingRequest
	versionJSON string
}

func (h *fakeUserSettingsHub) ListUserSettings(
	_ context.Context,
	_ *leapmuxv1.ListUserSettingsRequest,
) (*leapmuxv1.ListUserSettingsResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.listErr != nil {
		return nil, h.listErr
	}
	return &leapmuxv1.ListUserSettingsResponse{
		Descriptors: h.descriptors,
		Values:      h.values,
	}, nil
}

func (h *fakeUserSettingsHub) UpdateUserSetting(
	_ context.Context,
	req *leapmuxv1.UpdateUserSettingRequest,
) (*leapmuxv1.UpdateUserSettingResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastUpdate = req
	return &leapmuxv1.UpdateUserSettingResponse{
		Value: &leapmuxv1.SettingValue{
			Key:           req.GetKey(),
			Customized:    true,
			EffectiveJson: req.GetPartialJson(),
			ValueJson:     req.GetPartialJson(),
		},
	}, nil
}

func (h *fakeUserSettingsHub) ResetUserSetting(
	_ context.Context,
	req *leapmuxv1.ResetUserSettingRequest,
) (*leapmuxv1.ResetUserSettingResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastReset = req
	return &leapmuxv1.ResetUserSettingResponse{
		Value: &leapmuxv1.SettingValue{
			Key:           req.GetKey(),
			Customized:    false,
			EffectiveJson: `{"theme":"system"}`,
		},
	}, nil
}

func startUserSettingsHub(t *testing.T, h *fakeUserSettingsHub) string {
	t.Helper()
	isolateCLIEnv(t)

	mux := http.NewServeMux()
	if h.versionJSON != "" {
		mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, h.versionJSON)
		})
	}
	server := connect.NewServer()
	leapmuxv1connect.RegisterUserServiceHandler(server, h)
	connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(0))
	srv := hubtransporttest.NewServer(t, mux)
	t.Cleanup(srv.Close)

	require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
		HubURL:      srv.URL,
		AccessToken: "lmx_settings_token",
		ExpiresAt:   time.Now().Add(time.Hour),
		UserID:      "u-1",
		Username:    "alice",
	}))
	return srv.URL
}

func TestSettingsDirectHubCRUD(t *testing.T) {
	hub := &fakeUserSettingsHub{
		descriptors: []*leapmuxv1.SettingDescriptor{
			{
				Key:     "appearance",
				Summary: "Theme preferences",
				Fields: []*leapmuxv1.SettingField{
					{Name: "theme", Kind: leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_STRING},
				},
			},
		},
		values: []*leapmuxv1.SettingValue{
			{
				Key:           "appearance",
				Customized:    true,
				EffectiveJson: `{"theme":"dark"}`,
				ValueJson:     `{"theme":"dark"}`,
			},
		},
	}
	url := startUserSettingsHub(t, hub)
	// Settings must talk directly to the Hub even when a tab IPC socket is present.
	t.Setenv("LEAPMUX_CONTROL_SOCK", "unix:/nonexistent/tab.sock")
	t.Setenv("LEAPMUX_HUB", url)

	listData := runControl(t, "settings", "list").Data(t)
	settingsList, ok := listData["settings"].([]any)
	require.True(t, ok)
	require.Len(t, settingsList, 1)
	first := settingsList[0].(map[string]any)
	assert.Equal(t, "appearance", first["key"])
	assert.Equal(t, "Theme preferences", first["description"])

	getData := runControl(t, "settings", "get", "appearance").Data(t)
	assert.Equal(t, "appearance", getData["key"])
	assert.Equal(t, `{"theme"}`, getData["value_schema"])

	notFound := runControl(t, "settings", "get", "missing").requireFailure(t, cli.CodeNotFound)
	assert.Contains(t, notFound.Message, `unknown user setting key "missing"`)

	setData := runControl(t, "settings", "set", "appearance", `{"theme":"light"}`).Data(t)
	assert.Equal(t, true, setData["customized"])
	hub.mu.Lock()
	require.NotNil(t, hub.lastUpdate)
	assert.Equal(t, "appearance", hub.lastUpdate.GetKey())
	assert.JSONEq(t, `{"theme":"light"}`, hub.lastUpdate.GetPartialJson())
	hub.mu.Unlock()

	resetData := runControl(t, "settings", "reset", "appearance").Data(t)
	assert.Equal(t, false, resetData["customized"])
	hub.mu.Lock()
	require.NotNil(t, hub.lastReset)
	assert.Equal(t, "appearance", hub.lastReset.GetKey())
	hub.mu.Unlock()
}

func TestSettingsValidationBeforeDial(t *testing.T) {
	isolateCLIEnv(t)

	for _, args := range [][]string{
		{"settings", "get", "   "},
		{"settings", "set", "   ", "dark"},
		{"settings", "reset", "   "},
	} {
		f := runControl(t, append(args, "--hub", unreachableHub)...).requireFailure(t, cli.CodeUsage)
		assert.Equal(t, "<key> is empty", f.Message, strings.Join(args, " "))
	}

	// Empty value must direct the user to `leapmux settings reset <key>`, not `leapmux admin settings reset <key>`.
	f := runControl(t, "settings", "set", "appearance", "", "--hub", unreachableHub).requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, `"leapmux settings reset <key>"`)
	assert.NotContains(t, f.Message, "admin settings reset")
}

func TestCompleteUserSettingsKeys_DirectHubSortedAndSanitized(t *testing.T) {
	hub := &fakeUserSettingsHub{
		descriptors: []*leapmuxv1.SettingDescriptor{
			{Key: "notifications", Summary: "Alert\nsettings\tline"},
			{Key: "appearance", Summary: "Theme preferences"},
			{Key: "agents", Summary: "Agent defaults"},
		},
	}
	url := startUserSettingsHub(t, hub)
	t.Setenv("LEAPMUX_CONTROL_SOCK", "unix:/nonexistent/tab.sock")

	root := NewCommand()
	cmd, _, err := root.Find([]string{"settings", "get"})
	require.NoError(t, err)
	require.NoError(t, root.PersistentFlags().Set("hub", url))
	cmd.SetContext(t.Context())

	got, directive := completeUserSettingsKeys(cmd, cli.ArgSpec{Name: "key"}, "")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
	assert.Equal(t, []string{
		"agents\tAgent defaults",
		"appearance\tTheme preferences",
		"notifications\tAlert settings line",
	}, got)

	filtered, _ := completeUserSettingsKeys(cmd, cli.ArgSpec{Name: "key"}, "ap")
	assert.Equal(t, []string{"appearance\tTheme preferences"}, filtered)

	otherArg, _ := completeUserSettingsKeys(cmd, cli.ArgSpec{Name: "value"}, "")
	assert.Empty(t, otherArg)

	require.NoError(t, root.PersistentFlags().Set("hub", "   "))
	blankHub, _ := completeUserSettingsKeys(cmd, cli.ArgSpec{Name: "key"}, "")
	assert.Empty(t, blankHub)
}

func TestSettingsVersionMismatchReportsDirectHubBuild(t *testing.T) {
	hub := &fakeUserSettingsHub{
		listErr:     connect.NewError(connect.CodeUnimplemented, "ListUserSettings not supported"),
		versionJSON: `{"version":"4.5.6","formatted":"4.5.6 · hub-sha"}`,
	}
	url := startUserSettingsHub(t, hub)

	f := runControl(t, "settings", "list", "--hub", url).requireFailure(t, cli.CodeVersionMismatch)
	hints := strings.Join(f.Hints, "\n")
	assert.Contains(t, hints, "CLI build "+version.Format())
	assert.Contains(t, hints, "Hub build 4.5.6 · hub-sha")
}
