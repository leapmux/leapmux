package lifecycle

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
	"github.com/stretchr/testify/require"
)

func TestStopWaitsForStartingControlEndpoint(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	runtime, err := StartRuntime(RuntimeConfig{
		Mode: RoleWorker, DataDir: t.TempDir(), Execution: ExecutionForeground,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = runtime.Finish(nil) })
	record := runtime.Record()
	record.ControlURL = locallistentest.UniqueListenURL(t, "lmx-delayed-control")
	require.NoError(t, WriteRuntimeRecord(runtime.Paths().RuntimeRecord, record))
	firstAttempt := make(chan error, 1)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	stopped := make(chan error, 1)
	go func() {
		first := true
		_, err := stopWithRequester(ctx, record.Mode, record.DataDir, func(ctx context.Context, record RuntimeRecord) (StopResponse, error) {
			response, err := RequestStop(ctx, record)
			if first {
				first = false
				firstAttempt <- err
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			return response, err
		})
		stopped <- err
	}()
	select {
	case err := <-firstAttempt:
		require.Error(t, err, "the initial request must reach the not-yet-published endpoint")
	case <-ctx.Done():
		t.Fatal("stop never attempted the starting control endpoint")
	}
	listener, err := locallisten.Listen(record.ControlURL)
	require.NoError(t, err)
	token, err := ReadControlToken(runtime.Paths().ControlToken)
	require.NoError(t, err)
	finished := make(chan error, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body StopRequest
		if request.Header.Get("Authorization") != "Bearer "+token || json.NewDecoder(request.Body).Decode(&body) != nil ||
			body.InstanceID != record.InstanceID || body.AttemptID != record.AttemptID {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		runtime.RequestStop()
		if err := json.NewEncoder(w).Encode(StopResponse{Accepted: true, AttemptID: record.AttemptID}); err != nil {
			t.Errorf("write stop response: %v", err)
		}
		finished <- runtime.Finish(nil)
	})}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		require.ErrorIs(t, <-served, http.ErrServerClosed)
	})
	close(release)
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("stop did not finish after the control endpoint became ready")
	}
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("the controlled runtime did not finish")
	}
}
