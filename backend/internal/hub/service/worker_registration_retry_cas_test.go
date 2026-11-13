package service_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	workerhub "github.com/leapmux/leapmux/internal/worker/hub"
)

type recoveryRetryBarrier struct {
	entered     chan struct{}
	release     chan struct{}
	completed   chan struct{}
	releaseOnce sync.Once
	calls       atomic.Int32
	contextLive atomic.Bool
}

func (b *recoveryRetryBarrier) unblock() { b.releaseOnce.Do(func() { close(b.release) }) }

type recoveryRetryStore struct {
	store.Store
	barrier *recoveryRetryBarrier
}
type recoveryRetryWorkers struct {
	store.WorkerStore
	barrier *recoveryRetryBarrier
}

func (s recoveryRetryStore) Workers() store.WorkerStore {
	return recoveryRetryWorkers{s.Store.Workers(), s.barrier}
}
func (w recoveryRetryWorkers) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.Worker, error) {
	row, err := w.WorkerStore.GetByIDIncludeDeleted(ctx, id)
	if err != nil {
		return nil, err
	}
	if w.barrier.calls.Add(1) == 1 {
		defer close(w.barrier.completed)
		close(w.barrier.entered)
		select {
		case <-w.barrier.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		w.barrier.contextLive.Store(ctx.Err() == nil)
	}
	return row, nil
}

func TestRegistrationRetryKeepsItsReturnedTokenAfterAnOlderRecoveryEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	barrier := &recoveryRetryBarrier{entered: make(chan struct{}), release: make(chan struct{}), completed: make(chan struct{})}
	env := newRegKeyEnv(t, testConfig(), nil, regKeyEnvOptions{connectorStore: func(st store.Store) store.Store { return recoveryRetryStore{st, barrier} }})
	fixture := newRecoveryFixture(t, env)
	upstreamURL, err := url.Parse(env.server.URL)
	require.NoError(t, err)
	var recoveryCalls atomic.Int32
	upstreamDone := make(chan error, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read proxy request", 500)
			return
		}
		upstream := r.Clone(r.Context())
		upstream.URL.Scheme = upstreamURL.Scheme
		upstream.URL.Host = upstreamURL.Host
		upstream.Host = upstreamURL.Host
		upstream.RequestURI = ""
		upstream.Body = io.NopCloser(bytes.NewReader(body))
		if strings.HasSuffix(r.URL.Path, "/RecoverRegistration") && recoveryCalls.Add(1) == 1 {
			// A proxy can retain an upstream operation after finishing its downstream response.
			upstream = upstream.Clone(ctx)
			go func() {
				response, err := http.DefaultTransport.RoundTrip(upstream)
				if err == nil {
					_, err = io.Copy(io.Discard, response.Body)
					err = errors.Join(err, response.Body.Close())
				}
				upstreamDone <- err
			}()
			select {
			case <-barrier.entered:
				http.Error(w, "upstream response unavailable", http.StatusServiceUnavailable)
			case <-ctx.Done():
				http.Error(w, "upstream deadline", http.StatusGatewayTimeout)
			}
			return
		}
		response, err := http.DefaultTransport.RoundTrip(upstream)
		if err != nil {
			http.Error(w, "upstream transport failed", http.StatusBadGateway)
			return
		}
		defer func() { _ = response.Body.Close() }()
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	t.Cleanup(proxy.Close)
	// Release a held handler before the proxy and upstream servers close on any failure.
	t.Cleanup(barrier.unblock)
	endpoint, err := hubtransport.New(proxy.URL)
	require.NoError(t, err)
	t.Cleanup(endpoint.CloseIdleConnections)
	opts, err := workerhub.RegistrationOptionsFromKeypair(fixture.attempt.attemptID, fixture.key, fixture.attempt.composite)
	require.NoError(t, err)
	result, err := workerhub.Register(ctx, endpoint, opts)
	require.NoError(t, err)
	require.Equal(t, fixture.original.GetWorkerId(), result.WorkerID)
	_, err = auth.AuthenticateWorkerBearer(ctx, env.store, hubtestutil.TestSecretHasher(), "Bearer "+result.AuthToken)
	require.NoError(t, err)
	barrier.unblock()
	select {
	case err = <-upstreamDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-barrier.completed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.True(t, barrier.contextLive.Load(), "the first handler must still have a live request context")
	require.Equal(t, int32(2), recoveryCalls.Load())
	_, err = auth.AuthenticateWorkerBearer(ctx, env.store, hubtestutil.TestSecretHasher(), "Bearer "+result.AuthToken)
	require.NoError(t, err, "an older recovery must not invalidate the token returned by the sequential retry client")
}

type recoveryPairBarrier struct {
	entered [2]chan struct{}
	release [2]chan struct{}
	once    [2]sync.Once
	calls   atomic.Int32
}

func (b *recoveryPairBarrier) unblock(index int) {
	b.once[index].Do(func() { close(b.release[index]) })
}

type recoveryPairStore struct {
	store.Store
	barrier *recoveryPairBarrier
}
type recoveryPairWorkers struct {
	store.WorkerStore
	barrier *recoveryPairBarrier
}

func (s recoveryPairStore) Workers() store.WorkerStore {
	return recoveryPairWorkers{s.Store.Workers(), s.barrier}
}
func (w recoveryPairWorkers) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.Worker, error) {
	row, err := w.WorkerStore.GetByIDIncludeDeleted(ctx, id)
	if err != nil {
		return nil, err
	}
	index := int(w.barrier.calls.Add(1)) - 1
	if index < 2 {
		close(w.barrier.entered[index])
		select {
		case <-w.barrier.release[index]:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return row, nil
}

func TestRecoveryChallengesFromOneDigestHaveOnlyOneWinner(t *testing.T) {
	for _, winner := range []int{0, 1} {
		t.Run([]string{"first proof wins", "second proof wins"}[winner], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			barrier := &recoveryPairBarrier{entered: [2]chan struct{}{make(chan struct{}), make(chan struct{})}, release: [2]chan struct{}{make(chan struct{}), make(chan struct{})}}
			env := newRegKeyEnv(t, testConfig(), nil, regKeyEnvOptions{connectorStore: func(st store.Store) store.Store { return recoveryPairStore{st, barrier} }})
			t.Cleanup(func() { barrier.unblock(0); barrier.unblock(1) })
			fixture := newRecoveryFixture(t, env)
			type result struct {
				response *leapmuxv1.RegisterResponse
				err      error
			}
			results := [2]chan result{make(chan result, 1), make(chan result, 1)}
			for index := range 2 {
				issueCtx, issueInfo := connect.NewClientContext(ctx)
				issueInfo.RequestHeader().Set("Authorization", "Bearer "+fixture.key)
				challenge, err := env.connectorClient.GetRegistrationChallenge(issueCtx, &leapmuxv1.GetRegistrationChallengeRequest{AttemptId: fixture.attempt.attemptID})
				require.NoError(t, err)
				message := recoveryMessageBytes(fixture.attempt.attemptID, challenge.GetChallengeId(), challenge.GetNonce(), fixture.key)
				signature, err := fixture.attempt.composite.SlhdsaPrivateKey.Sign(rand.Reader, message, nil)
				require.NoError(t, err)
				callCtx, callInfo := connect.NewClientContext(ctx)
				callInfo.RequestHeader().Set("Authorization", "Bearer "+fixture.key)
				request := &leapmuxv1.RecoverRegistrationRequest{AttemptId: fixture.attempt.attemptID, ChallengeId: challenge.GetChallengeId(), Signature: signature}
				go func() {
					response, err := env.connectorClient.RecoverRegistration(callCtx, request)
					results[index] <- result{response, err}
				}()
				select {
				case <-barrier.entered[index]:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			receive := func(index int) result {
				select {
				case got := <-results[index]:
					return got
				case <-ctx.Done():
					t.Fatal(ctx.Err())
					return result{}
				}
			}
			barrier.unblock(winner)
			won := receive(winner)
			require.NoError(t, won.err)
			require.NotNil(t, won.response)
			loser := 1 - winner
			barrier.unblock(loser)
			lost := receive(loser)
			require.Error(t, lost.err)
			assert.Equal(t, connect.CodeAborted, connect.CodeOf(lost.err))
			assert.Nil(t, lost.response)
			hubtestutil.RequireWorkerToken(t, env.store, fixture.original.GetWorkerId(), won.response.GetAuthToken())
			fresh := recoverThroughChallenge(t, ctx, env, fixture.key, fixture.attempt)
			hubtestutil.RequireWorkerToken(t, env.store, fixture.original.GetWorkerId(), fresh.GetAuthToken())
			require.NotEqual(t, won.response.GetAuthToken(), fresh.GetAuthToken())
		})
	}
}
