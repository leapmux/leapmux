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
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/util/version"
)

func TestDecoratePeerError(t *testing.T) {
	t.Run("hints the login after a refused credential", func(t *testing.T) {
		got := cli.FromError(decoratePeerError(t.Context(), nil, fmt.Errorf("list tabs: %w", connect.NewError(connect.CodeUnauthenticated, "expired"))))
		assert.Equal(t, cli.CodeNotLoggedIn, got.Code)
		assert.Equal(t, "list tabs: expired", got.Message)
		assert.Equal(t, []string{`run "leapmux auth login --hub <url>" to sign in again`}, got.Hints)
	})

	t.Run("hints the version comparison after an unimplemented call on nil peer", func(t *testing.T) {
		got := cli.FromError(decoratePeerError(t.Context(), nil, connect.NewError(connect.CodeUnimplemented, "no such method")))
		assert.Equal(t, cli.CodeVersionMismatch, got.Code)
		require.Len(t, got.Hints, 1)
		assert.Contains(t, got.Hints[0], "leapmux version")
	})

	t.Run("adds nothing to another refusal", func(t *testing.T) {
		got := cli.FromError(decoratePeerError(t.Context(), nil, connect.NewError(connect.CodeNotFound, "no such tab")))
		assert.Equal(t, cli.CodeNotFound, got.Code)
		assert.Empty(t, got.Hints)
	})

	t.Run("keeps the hints of an error that the command classified", func(t *testing.T) {
		own := cli.Wrap(cli.CodeNotLoggedIn, connect.NewError(connect.CodeUnauthenticated, "refused"), "the hub refused this credential").
			WithHint("its own hint")
		got := cli.FromError(decoratePeerError(t.Context(), nil, own))
		assert.Equal(t, []string{"its own hint"}, got.Hints)
	})

	t.Run("passes an error that is not a refused call", func(t *testing.T) {
		plain := errors.New("disk full")
		err := decoratePeerError(t.Context(), nil, plain)
		require.ErrorIs(t, err, plain)
		assert.Empty(t, cli.FromError(err).Hints)
	})

	t.Run("passes a success", func(t *testing.T) {
		require.NoError(t, decoratePeerError(t.Context(), nil, nil))
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

func TestPeerVersionDiagnosticUsesReadDeadline(t *testing.T) {
	refusal := connect.NewError(connect.CodeUnimplemented, "the primary request failed")
	called := false
	result := withPeerVersion(t.Context(), refusal, "Hub", func(ctx context.Context) string {
		called = true
		_, hasDeadline := ctx.Deadline()
		assert.True(t, hasDeadline)
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
	called := hub.called()
	require.NotEmpty(t, called)
	assert.Equal(t, leapmuxv1connect.SystemServiceGetSystemInfoProcedure, called[len(called)-1])
	assert.Contains(t, called, leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure)
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

// Hints must never suggest `leapmux auth login --hub unix:...` or `npipe:...`,
// and direct-hub commands (including worker key subcommands) receive Hub
// version diagnostics directly from the HubClient transport decorator.
func TestDecoratePeerError_OmitsLocalSocketURLAndCoversWorkerDirectHub(t *testing.T) {
	t.Run("local unix hub peer omits socket URL from auth login hint", func(t *testing.T) {
		peer := &control.HubClient{HubURL: "unix:/tmp/solo.sock"}
		err := decoratePeerError(t.Context(), peer, connect.NewError(connect.CodeUnauthenticated, "expired"))
		got := cli.FromError(err)
		assert.Equal(t, cli.CodeNotLoggedIn, got.Code)
		assert.Equal(t, []string{`run "leapmux auth login --hub <url>" to sign in again`}, got.Hints)
	})

	t.Run("worker IPC peer omits socket URL from auth login hint", func(t *testing.T) {
		peer := &control.WorkerIPCClient{SocketURL: "unix:/tmp/tab.sock"}
		err := decoratePeerError(t.Context(), peer, connect.NewError(connect.CodeUnauthenticated, "expired"))
		got := cli.FromError(err)
		assert.Equal(t, cli.CodeNotLoggedIn, got.Code)
		assert.Equal(t, []string{`run "leapmux auth login --hub <url>" to sign in again`}, got.Hints)
	})

	t.Run("worker direct-hub command reports Hub build on unimplemented", func(t *testing.T) {
		url := startAdminHub(t, &fakeAdminHub{versionHandler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"version":"2.3.4","formatted":"2.3.4 · hub-commit"}`)
		}})
		liveCredentialFor(t, url)
		f := runControl(t, "worker", "key", "create", "--hub", url).requireFailure(t, cli.CodeVersionMismatch)
		hints := strings.Join(f.Hints, "\n")
		assert.Contains(t, hints, "CLI build "+version.Format())
		assert.Contains(t, hints, "Hub build 2.3.4 · hub-commit")
	})

	t.Run("worker IPC peer omits --hub from version mismatch hint", func(t *testing.T) {
		peer := &control.WorkerIPCClient{SocketURL: "unix:/tmp/tab.sock"}
		err := withHubHintForPeer(peer, connect.NewError(connect.CodeUnimplemented, "no such method"))
		got := cli.FromError(err)
		assert.Equal(t, cli.CodeVersionMismatch, got.Code)
		assert.Equal(t, []string{`run "leapmux version" to compare the CLI with the hub, and upgrade the older one`}, got.Hints)
	})

	t.Run("withHubVersion handles unsupported peer without panicking", func(t *testing.T) {
		refusal := connect.NewError(connect.CodeUnimplemented, "unsupported")
		got := withHubVersion(t.Context(), nil, refusal)
		ce := cli.FromError(got)
		assert.Equal(t, cli.CodeVersionMismatch, ce.Code)
		assert.Contains(t, ce.Hints, "Hub build unavailable")
	})
}

func TestWithHubHint_PreservesWrappersAndAvoidsDuplicateHints(t *testing.T) {
	t.Run("default path returns codedRPCError unchanged", func(t *testing.T) {
		orig := &codedRPCError{
			Code:  "rpc_error",
			Cause: connect.NewError(connect.CodeNotFound, "no such tab"),
		}
		got := withHubHintForPeer(nil, orig)
		assert.Same(t, orig, got)
		var coded *codedRPCError
		require.ErrorAs(t, got, &coded)
		assert.Equal(t, "rpc_error", coded.Code)
	})

	t.Run("default path does not duplicate HintProvider hints", func(t *testing.T) {
		inner := &stubHintedErr{
			err:   connect.NewError(connect.CodeUnavailable, "worker unreachable"),
			hints: []string{"check worker connectivity"},
		}
		got := withHubHintForPeer(nil, inner)
		assert.Same(t, inner, got)
		ce := cli.FromError(got)
		assert.Equal(t, cli.CodeUnavailable, ce.Code)
		assert.Equal(t, 1, countStrings(ce.Hints, "check worker connectivity"))
	})

	t.Run("auth refusal attaches hub hint without duplicating inner HintProvider hints", func(t *testing.T) {
		inner := &stubHintedErr{
			err: &codedRPCError{
				Code:  "rpc_error",
				Cause: connect.NewError(connect.CodeUnauthenticated, "expired"),
			},
			hints: []string{"refresh session token"},
		}
		got := withHubHintForPeer(nil, inner)
		var coded *codedRPCError
		require.ErrorAs(t, got, &coded)

		ce := cli.FromError(got)
		assert.Equal(t, cli.CodeNotLoggedIn, ce.Code)
		assert.Equal(t, 1, countStrings(ce.Hints, "refresh session token"))
		assert.Equal(t, 1, countStrings(ce.Hints, `run "leapmux auth login --hub <url>" to sign in again`))
	})

	t.Run("unimplemented refusal attaches version hint without duplicating inner HintProvider hints", func(t *testing.T) {
		inner := &stubHintedErr{
			err:   connect.NewError(connect.CodeUnimplemented, "missing method"),
			hints: []string{"inner compatibility note"},
		}
		got := withHubHintForPeer(nil, inner)
		ce := cli.FromError(got)
		assert.Equal(t, cli.CodeVersionMismatch, ce.Code)
		assert.Equal(t, 1, countStrings(ce.Hints, "inner compatibility note"))
		assert.Equal(t, 1, countStrings(ce.Hints, `run "leapmux version --hub <url>" to compare the CLI with the hub, and upgrade the older one`))
	})

	t.Run("withPeerVersion preserves wrappers and avoids duplicating hints", func(t *testing.T) {
		inner := &stubHintedErr{
			err: &codedRPCError{
				Code:  "rpc_error",
				Cause: connect.NewError(connect.CodeUnimplemented, "missing worker method"),
			},
			hints: []string{"inner worker hint"},
		}
		got := withPeerVersion(t.Context(), inner, "Worker w-1", func(context.Context) string {
			return "1.2.3"
		})
		var coded *codedRPCError
		require.ErrorAs(t, got, &coded)
		assert.False(t, needsVersionDiagnostic(got), "already-diagnosed error must not trigger a second version probe")
		assert.Same(t, got, withHubHintForPeer(nil, got), "withHubHintForPeer must not append a duplicate hub version hint")

		ce := cli.FromError(got)
		assert.Equal(t, cli.CodeVersionMismatch, ce.Code)
		assert.Equal(t, 1, countStrings(ce.Hints, "inner worker hint"))
		assert.Equal(t, 1, countStrings(ce.Hints, "CLI build "+version.Format()))
		assert.Equal(t, 1, countStrings(ce.Hints, "Worker w-1 build 1.2.3"))
		assert.Equal(t, 1, countStrings(ce.Hints, "upgrade the CLI or the Worker w-1 to a build that supports this command"))
	})
}

func TestWithHubHint_DirectHubAndWorkerIPCPeersSubstituteCorrectHint(t *testing.T) {
	hubPeer := &control.HubClient{HubURL: "https://hub.example"}
	gotHub := decoratePeerError(t.Context(), hubPeer, connect.NewError(connect.CodeUnauthenticated, "expired"))
	assert.Equal(t,
		[]string{`run "leapmux auth login --hub https://hub.example" to sign in again`},
		cli.FromError(gotHub).Hints,
	)

	ipcPeer := &control.WorkerIPCClient{SocketURL: "unix:/tmp/tab.sock"}
	gotIPC := decoratePeerError(t.Context(), ipcPeer, connect.NewError(connect.CodeUnauthenticated, "expired"))
	assert.Equal(t,
		[]string{`run "leapmux auth login --hub <url>" to sign in again`},
		cli.FromError(gotIPC).Hints,
	)

	// In-band Hub auth refusal relayed over local IPC still receives the local login hint.
	gotRelayed := withHubVersion(t.Context(), ipcPeer, connect.NewError(connect.CodeUnauthenticated, "expired"))
	assert.Equal(t,
		[]string{`run "leapmux auth login --hub <url>" to sign in again`},
		cli.FromError(gotRelayed).Hints,
	)
}

func TestStripHubHintError_UnwrapsDirectAndWrappedHubHints(t *testing.T) {
	cause := connect.NewError(connect.CodeUnauthenticated, "rejected")
	hinted := &hubHintError{cause: cause, hints: []string{"generic login hint"}}

	assert.Nil(t, stripHubHintError(nil))
	assert.Same(t, cause, stripHubHintError(cause))
	assert.Same(t, cause, stripHubHintError(hinted))

	// Connect client streams can wrap interceptor errors inside another wrapper.
	wrapped := fmt.Errorf("connect wrapper: %w", hinted)
	stripped := stripHubHintError(wrapped)
	assert.Same(t, cause, stripped)
	ce := cli.Wrap(cli.CodeNotLoggedIn, stripped, "refused").WithHint("custom hint")
	assert.Equal(t, []string{"custom hint"}, cli.FromError(ce).Hints)
}
