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
	"google.golang.org/protobuf/types/known/timestamppb"

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
	expires := timestamppb.New(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))
	keyWire, err := proto.Marshal(&leapmuxv1.CreateRegistrationKeyResponse{RegistrationKey: "fixture-registration-key", ExpiresAt: expires})
	require.NoError(t, err)
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
		if r.URL.Path == leapmuxv1connect.WorkerManagementServiceCreateRegistrationKeyProcedure {
			_, _ = w.Write(keyWire)
		}
	}))
	config := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", config)
	require.NoError(t, control.SaveCredentials(hub.URL, control.CredentialFile{
		HubURL: hub.URL, AccessToken: token, UserID: "test-owner", ExpiresAt: time.Now().Add(time.Hour),
	}))

	for _, test := range []struct {
		name, path, output string
		args               []string
		body               proto.Message
	}{
		{
			name: "raw RPC", path: leapmuxv1connect.WorkspaceServiceListTabsProcedure, output: `{"data":{}}`,
			args: []string{"rpc", "WorkspaceService/ListTabs", `{"workspace_ids":["first-workspace","second-workspace"]}`},
			body: &leapmuxv1.ListTabsRequest{WorkspaceIds: []string{"first-workspace", "second-workspace"}},
		},
		{
			name: "worker key creation", path: leapmuxv1connect.WorkerManagementServiceCreateRegistrationKeyProcedure,
			output: `{"data":{"key":"fixture-registration-key","expires_at":"2030-01-02T03:04:05.000Z"}}`,
			args:   []string{"worker", "key", "create"}, body: &leapmuxv1.CreateRegistrationKeyRequest{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"control", "--hub", hub.URL, "--json"}, test.args...)
			code, stdout, stderr := runMain(t, args,
				"LEAPMUX_CONTROL_CLI=", "LEAPMUX_CONTROL_SOCK="+filepath.Join(config, "unused-ipc"), "LEAPMUX_CONTROL_TOKEN=synthetic-tab-token")
			require.Zero(t, code, stderr)
			assert.JSONEq(t, test.output, stdout)
			expected, err := proto.Marshal(test.body)
			require.NoError(t, err)
			mu.Lock()
			defer mu.Unlock()
			defer func() { received = nil }()
			require.Len(t, received, 1)
			assert.NoError(t, received[0].err)
			assert.Equal(t, http.MethodPost, received[0].method)
			assert.Equal(t, test.path, received[0].path)
			assert.Equal(t, "Bearer "+token, received[0].authorization)
			assert.Equal(t, "application/proto", received[0].contentType)
			assert.Equal(t, expected, received[0].payload)
		})
	}
}
