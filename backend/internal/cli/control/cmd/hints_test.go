package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/util/version"
)

// runHinted runs a leaf that fails with err under a tree that addHubHints
// decorated, and returns the error of the command.
func runHinted(t *testing.T, err error) error {
	t.Helper()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	t.Setenv("LEAPMUX_HUB", "")
	root := cli.Group("control", "Control",
		cli.NewLeaf(cli.Leaf{
			Use:   "probe",
			Short: "Probe",
			Run:   func(*cli.Invocation) (any, error) { return nil, err },
		}))
	addHubHints(root)
	return cli.Run(context.Background(), root, []string{"probe"}, strings.NewReader(""), io.Discard, io.Discard)
}

func TestAddHubHints(t *testing.T) {
	t.Run("hints the login after a refused credential", func(t *testing.T) {
		got := cli.FromError(runHinted(t, fmt.Errorf("list tabs: %w", connect.NewError(connect.CodeUnauthenticated, "expired"))))
		assert.Equal(t, cli.CodeNotLoggedIn, got.Code)
		assert.Equal(t, "list tabs: expired", got.Message)
		assert.Equal(t, []string{`run "leapmux auth login --hub <url>" to sign in again`}, got.Hints)
	})

	t.Run("hints the version comparison after an unimplemented call", func(t *testing.T) {
		got := cli.FromError(runHinted(t, connect.NewError(connect.CodeUnimplemented, "no such method")))
		assert.Equal(t, cli.CodeVersionMismatch, got.Code)
		require.Len(t, got.Hints, 1)
		assert.Contains(t, got.Hints[0], "leapmux version")
	})

	t.Run("adds nothing to another refusal", func(t *testing.T) {
		got := cli.FromError(runHinted(t, connect.NewError(connect.CodeNotFound, "no such tab")))
		assert.Equal(t, cli.CodeNotFound, got.Code)
		assert.Empty(t, got.Hints)
	})

	t.Run("keeps the hints of an error that the command classified", func(t *testing.T) {
		own := cli.Wrap(cli.CodeNotLoggedIn, connect.NewError(connect.CodeUnauthenticated, "refused"), "the hub refused this credential").
			WithHint("its own hint")
		got := cli.FromError(runHinted(t, own))
		assert.Equal(t, []string{"its own hint"}, got.Hints)
	})

	t.Run("passes an error that is not a refused call", func(t *testing.T) {
		plain := errors.New("disk full")
		err := runHinted(t, plain)
		require.ErrorIs(t, err, plain)
		assert.Empty(t, cli.FromError(err).Hints)
	})

	t.Run("passes a success", func(t *testing.T) {
		require.NoError(t, runHinted(t, nil))
	})
}

// A failed build lookup must preserve the original Hub refusal.
func TestControlVersionMismatchKeepsTheRefusalWhenTheHubBuildIsUnavailable(t *testing.T) {
	url := startAdminHub(t, &fakeAdminHub{})
	f := runControl(t, "admin", "user", "get", "--hub", url, "alice").requireFailure(t, cli.CodeVersionMismatch)
	assert.Contains(t, f.Message, "GetUser")
	assert.Contains(t, f.Hints, "Hub build unavailable")
	assert.Contains(t, f.Hints, "CLI build "+version.Format())
}

func TestPeerVersionDiagnosticHasABoundedRead(t *testing.T) {
	refusal := connect.NewError(connect.CodeUnimplemented, "the primary request failed")
	called := false
	result := withPeerVersion(t.Context(), refusal, "Hub", func(ctx context.Context) string {
		called = true
		_, bounded := ctx.Deadline()
		assert.True(t, bounded)
		return ""
	})
	assert.True(t, called)
	assert.ErrorIs(t, result, refusal)
	assert.Equal(t, cli.CodeVersionMismatch, cli.FromError(result).Code)
}

func TestControlVersionMismatchReportsTheDirectHubBuild(t *testing.T) {
	url := startAdminHub(t, &fakeAdminHub{versionHandler: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"version":"1.2.3","formatted":"1.2.3 · hub-commit"}`)
	}})
	f := runControl(t, "admin", "user", "get", "--hub", url, "alice").requireFailure(t, cli.CodeVersionMismatch)
	hints := strings.Join(f.Hints, "\n")
	assert.Contains(t, hints, "CLI build "+version.Format())
	assert.Contains(t, hints, "Hub build 1.2.3 · hub-commit")
	assert.Contains(t, hints, "upgrade")
}

func TestControlVersionMismatchReportsTheRelayedHubBuild(t *testing.T) {
	hub := &recordingHub{
		listWorkspacesErr: connect.NewError(connect.CodeUnimplemented, "workspace listing is unsupported"),
		systemBuild:       &leapmuxv1.BuildInfo{Version: "3.4.5", CommitHash: "relay-hub"},
	}
	startSpawnIPC(t, hub, nil)
	f := runControl(t, "workspace", "list").requireFailure(t, cli.CodeVersionMismatch)
	assert.Contains(t, f.Message, "workspace listing is unsupported")
	hints := strings.Join(f.Hints, "\n")
	assert.Contains(t, hints, "CLI build "+version.Format())
	assert.Contains(t, hints, "Hub build 3.4.5 · relay-hub")
	assert.Equal(t, []string{leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure, leapmuxv1connect.SystemServiceGetSystemInfoProcedure}, hub.called())
}

type versionMismatchWorker struct{ build *leapmuxv1.BuildInfo }

func TestControlVersionMismatchReportsALocalRefusalFromTheTabSocket(t *testing.T) {
	hub := &recordingHub{}
	startSpawnIPC(t, hub, versionMismatchWorker{build: &leapmuxv1.BuildInfo{Version: "4.5.6"}}, func(router *controlipc.Router) {
		router.Hub = nil
	})
	f := runControl(t, "workspace", "list").requireFailure(t, cli.CodeVersionMismatch)
	assert.Contains(t, f.Message, "hub client not configured")
	assert.Contains(t, f.Hints, "Worker IPC build 4.5.6")
	assert.Empty(t, hub.called(), "the request never reached the Hub")
}

func (w versionMismatchWorker) DispatchWith(_ context.Context, _ channel.Caller, request *leapmuxv1.InnerRpcRequest, writer channel.ResponseWriter) {
	if request.GetMethod() == leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure && w.build != nil {
		writeProtoResponse(writer, &leapmuxv1.GetWorkerSystemInfoResponse{Build: w.build})
		return
	}
	_ = writer.SendError(int32(codes.Unimplemented), "the Worker does not implement this procedure")
}

func TestControlVersionMismatchReportsTheWorkerBuildOrItsAbsence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build *leapmuxv1.BuildInfo
		want  string
	}{
		{name: "build available", build: &leapmuxv1.BuildInfo{Version: "7.8.9", CommitHash: "worker-commit"}, want: "Worker worker-A build 7.8.9 · worker-commit"},
		{name: "build unknown", want: "Worker worker-A build unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tab := terminalTab()
			startSpawnIPC(t, &recordingHub{locateTab: tab}, versionMismatchWorker{build: tc.build})
			f := runControl(t, "terminal", "read", tab.GetTab().GetTabId()).requireFailure(t, cli.CodeVersionMismatch)
			assert.Contains(t, f.Message, "the Worker does not implement this procedure")
			hints := strings.Join(f.Hints, "\n")
			assert.Contains(t, hints, "CLI build "+version.Format())
			assert.Contains(t, hints, tc.want)
			assert.NotContains(t, hints, "Hub build")
		})
	}
}
