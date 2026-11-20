package main

import (
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/cli/control"
)

func TestMainControlSendsTheExactRequestToTheExplicitHub(t *testing.T) {
	const token = "synthetic-hub-transport-token"
	type request struct {
		method, path, authorization, contentType string
		payload                                  []byte
		err                                      error
	}
	var mu sync.Mutex
	var received []request
	hub := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wire, err := io.ReadAll(r.Body)
		mu.Lock()
		received = append(received, request{
			method: r.Method, path: r.URL.Path, authorization: r.Header.Get("Authorization"),
			contentType: r.Header.Get("Content-Type"), payload: wire, err: err,
		})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/proto")
		w.WriteHeader(http.StatusOK)
	}))
	config := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", config)
	require.NoError(t, control.SaveCredentials(hub.URL, control.CredentialFile{
		HubURL: hub.URL, AccessToken: token, UserID: "test-owner", ExpiresAt: time.Now().Add(time.Hour),
	}))

	code, stdout, stderr := runMain(t, []string{
		"control", "--hub", hub.URL, "--json", "rpc", "WorkspaceService/ListTabs",
		`{"workspace_ids":["first-workspace","second-workspace"]}`,
	}, "LEAPMUX_CONTROL_CLI=", "LEAPMUX_CONTROL_SOCK="+filepath.Join(config, "unused-ipc"), "LEAPMUX_CONTROL_TOKEN=synthetic-tab-token")
	require.Zero(t, code, stderr)
	assert.JSONEq(t, `{"data":{}}`, stdout)

	expected, err := proto.Marshal(&leapmuxv1.ListTabsRequest{WorkspaceIds: []string{"first-workspace", "second-workspace"}})
	require.NoError(t, err)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, received, 1)
	assert.NoError(t, received[0].err)
	assert.Equal(t, http.MethodPost, received[0].method)
	assert.Equal(t, leapmuxv1connect.WorkspaceServiceListTabsProcedure, received[0].path)
	assert.Equal(t, "Bearer "+token, received[0].authorization)
	assert.Equal(t, "application/proto", received[0].contentType)
	assert.Equal(t, expected, received[0].payload)
}
