package revocationwatcher_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/revocationwatcher"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
)

func newBearerAuthHub(
	t *testing.T,
	st store.Store,
	validator *auth.TokenValidator,
) (leapmuxv1connect.AuthServiceClient, *auth.AuthContextRegistry) {
	t.Helper()
	mux := http.NewServeMux()
	interceptor, cache := auth.NewInterceptorWithTokens(st, nil, validator, false, false)
	t.Cleanup(cache.Stop)
	authService := service.NewAuthService(st, &config.Config{}, auth.NewCredentialLifecycleEffects(cache, nil, nil), nil, mail.NewStubSender(), mail.Renderer{})
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authService, connect.WithInterceptors(interceptor))
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL), cache
}

func authenticateAPIBearer(
	ctx context.Context,
	client leapmuxv1connect.AuthServiceClient,
	bearer string,
) error {
	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", "Bearer "+bearer)
	_, err := client.GetCurrentUser(ctx, req)
	return err
}

type fakeCloser struct {
	mu             sync.Mutex
	sessionClosed  []string
	bearerClosed   []string
	bearerKinds    []auth.BearerKind
	userClosed     []string
	userGeneration []int64
}

func (c *fakeCloser) CloseChannelsBySession(sessionID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionClosed = append(c.sessionClosed, sessionID)
	return 1
}

func (c *fakeCloser) CloseChannelsByBearer(ref auth.BearerRef) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bearerKinds = append(c.bearerKinds, ref.Kind())
	c.bearerClosed = append(c.bearerClosed, ref.TokenID())
	return 1
}

func (c *fakeCloser) CloseChannelsByUserRevocation(userID string, userAuthGeneration int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.userClosed = append(c.userClosed, userID)
	c.userGeneration = append(c.userGeneration, userAuthGeneration)
	return 1
}

func (*fakeCloser) RestampSessionGeneration(string, int64) {}

func (c *fakeCloser) sessionSnapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sessionClosed...)
}

func (c *fakeCloser) bearerSnapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bearerClosed...)
}

func (c *fakeCloser) bearerKindSnapshot() []auth.BearerKind {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]auth.BearerKind(nil), c.bearerKinds...)
}

func (c *fakeCloser) userSnapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.userClosed...)
}

func (c *fakeCloser) userGenerationSnapshot() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.userGeneration...)
}

type envT struct {
	st       store.Store
	cache    *auth.AuthContextRegistry
	closer   *fakeCloser
	watcher  *revocationwatcher.Watcher
	userID   string
	orgID    string
	workerID string
	wsID     string
	tabID    string
}

func setup(t *testing.T) *envT {
	return setupWithOptions(t)
}

func TestNewRequiresCredentialLifecycleEffects(t *testing.T) {
	assert.Panics(t, func() { revocationwatcher.New(nil, nil) })
}

func setupWithOptions(t *testing.T, opts ...revocationwatcher.Option) *envT {
	t.Helper()
	env := setupUnseededWithOptions(t, opts...)
	require.NoError(t, env.watcher.SeedCursor(context.Background()))
	t.Cleanup(func() {
		require.NoError(t, env.watcher.Close(context.Background()))
	})
	return env
}

func setupUnseeded(t *testing.T) *envT {
	return setupUnseededWithOptions(t)
}

func setupUnseededWithOptions(t *testing.T, opts ...revocationwatcher.Option) *envT {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)
	_, sc := auth.NewInterceptor(st, nil, false, false)
	t.Cleanup(sc.Stop)

	closer := &fakeCloser{}
	opts = append([]revocationwatcher.Option{revocationwatcher.WithInterval(50 * time.Millisecond)}, opts...)
	w := revocationwatcher.New(st, auth.NewCredentialLifecycleEffects(sc, closer, nil), opts...)

	u, err := st.Users().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)

	workerID := id.Generate()
	require.NoError(t, st.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    u.ID,
		PublicKey:       []byte("test-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("mlkem"),
		SlhdsaPublicKey: []byte("slhdsa"),
	}))

	wsID := id.Generate()
	require.NoError(t, st.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID: wsID, OrgID: u.OrgID, OwnerUserID: u.ID, Title: "ws",
	}))
	tabID := id.Generate()
	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(context.Background(), store.UpsertOwnedTabParams{
		OrgID:       u.OrgID,
		WorkspaceID: wsID,
		WorkerID:    workerID,
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabID:       tabID,
		Position:    "a",
		TileID:      "tile-1",
	}))

	return &envT{
		st: st, cache: sc, closer: closer, watcher: w,
		userID: u.ID, orgID: u.OrgID, workerID: workerID, wsID: wsID, tabID: tabID,
	}
}

func (e *envT) seedAPIToken(t *testing.T) string {
	t.Helper()
	tokenID := id.Generate()
	require.NoError(t, e.st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:         tokenID,
		UserID:     e.userID,
		ClientType: "cli",
		ClientName: "test",
		SecretHash: []byte("hash"),
		Scope:      "remote:*",
	}))
	return tokenID
}

func (e *envT) seedDelegationToken(t *testing.T) string {
	t.Helper()
	tokenID := id.Generate()
	require.NoError(t, e.st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:               tokenID,
		UserID:           e.userID,
		WorkerID:         e.workerID,
		WorkspaceID:      e.wsID,
		IssuedForTabID:   e.tabID,
		IssuedForTabType: int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
		SecretHash:       []byte("hash"),
		ExpiresAt:        time.Now().Add(time.Hour),
	}))
	return tokenID
}

func (e *envT) seedUser(t *testing.T, username string) string {
	t.Helper()
	userID := id.Generate()
	require.NoError(t, e.st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		OrgID:        e.orgID,
		Username:     username,
		PasswordHash: "hash",
		DisplayName:  username,
		PasswordSet:  true,
	}))
	return userID
}

func (e *envT) testHelper(t *testing.T) store.TestHelper {
	t.Helper()
	testable, ok := e.st.(store.TestableStore)
	require.True(t, ok)
	return testable.TestHelper()
}

func TestWatcher_APITokenRotationEvictsRemoteCacheWithoutClosingChannels(t *testing.T) {
	env := setupUnseeded(t)
	validator, err := auth.NewTokenValidator(env.st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	localClient, localCache := newBearerAuthHub(t, env.st, validator)
	remoteClient, remoteCache := newBearerAuthHub(t, env.st, validator)
	env.watcher = revocationwatcher.New(
		env.st, auth.NewCredentialLifecycleEffects(remoteCache, env.closer, nil),
		revocationwatcher.WithInterval(50*time.Millisecond),
	)
	require.NoError(t, env.watcher.SeedCursor(context.Background()))
	t.Cleanup(func() { require.NoError(t, env.watcher.Close(context.Background())) })

	tokenID := id.Generate()
	oldAccessSecret := auth.MintAccessSecret()
	oldRefreshSecret := auth.MintAccessSecret()
	expiresAt := time.Now().Add(time.Hour)
	refreshExpiresAt := time.Now().Add(24 * time.Hour)
	require.NoError(t, env.st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           env.userID,
		ClientType:       "cli",
		ClientName:       "remote-cache-test",
		SecretHash:       validator.HashSecret(oldAccessSecret),
		RefreshHash:      validator.HashSecret(oldRefreshSecret),
		Scope:            "remote:*",
		ExpiresAt:        &expiresAt,
		RefreshExpiresAt: &refreshExpiresAt,
	}))
	oldBearer := auth.FormatBearer(auth.BearerKindAPI, tokenID, oldAccessSecret)
	require.NoError(t, authenticateAPIBearer(context.Background(), localClient, oldBearer))
	require.NoError(t, authenticateAPIBearer(context.Background(), remoteClient, oldBearer))
	leaseCtx, cancelLease := context.WithCancel(context.Background())
	leaseRelease, current := remoteCache.RegisterAuthenticatedLease(context.Background(), &auth.UserInfo{
		ID: env.userID, Credential: auth.APICredential(tokenID),
	}, cancelLease)
	require.True(t, current)
	t.Cleanup(leaseRelease)

	newExpiresAt := time.Now().Add(time.Hour)
	newRefreshExpiresAt := time.Now().Add(24 * time.Hour)
	previousRefreshExpiresAt := time.Now().Add(auth.RefreshReuseGrace)
	rotated, err := env.st.APITokens().RotateRefresh(context.Background(), store.RotateAPITokenRefreshParams{
		ID:                       tokenID,
		NewSecretHash:            validator.HashSecret(auth.MintAccessSecret()),
		NewExpiresAt:             &newExpiresAt,
		NewRefreshHash:           validator.HashSecret(auth.MintAccessSecret()),
		NewRefreshExpiresAt:      &newRefreshExpiresAt,
		PreviousRefreshHash:      validator.HashSecret(oldRefreshSecret),
		PreviousRefreshExpiresAt: &previousRefreshExpiresAt,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rotated)

	localCache.EvictBearer(auth.NewBearerRef(auth.BearerKindAPI, tokenID))
	err = authenticateAPIBearer(context.Background(), localClient, oldBearer)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	require.NoError(t, authenticateAPIBearer(context.Background(), remoteClient, oldBearer),
		"remote Hub should remain stale until its watcher consumes the rotation event")

	require.NoError(t, env.watcher.RunOnce(context.Background()))
	err = authenticateAPIBearer(context.Background(), remoteClient, oldBearer)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.Empty(t, env.closer.bearerSnapshot(), "routine refresh rotation must preserve open channels")
	select {
	case <-leaseCtx.Done():
		t.Fatal("routine refresh rotation canceled an existing authenticated lease")
	default:
	}
}

type publishFailStore struct {
	store.Store
	events store.RevocationEventStore
}

func (s publishFailStore) RevocationEvents() store.RevocationEventStore {
	return s.events
}

type publishFailRevocationEvents struct {
	store.RevocationEventStore
}

func (s publishFailRevocationEvents) PublishPending(context.Context, int32) (int64, error) {
	return 0, errors.New("forced publish failure")
}

type delayedAcquireRevocationEvents struct {
	store.RevocationEventStore
	delay time.Duration
}

func (s delayedAcquireRevocationEvents) AcquireHubRuntimeLease(
	ctx context.Context,
	p store.AcquireHubRuntimeLeaseParams,
) (int64, error) {
	time.Sleep(s.delay)
	return s.RevocationEventStore.AcquireHubRuntimeLease(ctx, p)
}

type cancelablePublishRevocationEvents struct {
	store.RevocationEventStore
	entered chan struct{}
	exited  chan struct{}
}

type cancelableAcquireRevocationEvents struct {
	store.RevocationEventStore
	entered chan struct{}
	exited  chan struct{}
}

func (s *cancelableAcquireRevocationEvents) AcquireHubRuntimeLease(
	ctx context.Context,
	_ store.AcquireHubRuntimeLeaseParams,
) (int64, error) {
	close(s.entered)
	<-ctx.Done()
	close(s.exited)
	return 0, ctx.Err()
}

func (s *cancelablePublishRevocationEvents) PublishPending(ctx context.Context, _ int32) (int64, error) {
	close(s.entered)
	<-ctx.Done()
	close(s.exited)
	return 0, ctx.Err()
}

type releaseDeadlineRevocationEvents struct {
	store.RevocationEventStore
	releaseBudget chan time.Duration
}

func (s *releaseDeadlineRevocationEvents) ReleaseHubRuntimeLease(
	ctx context.Context,
	holderID string,
) (int64, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		s.releaseBudget <- 0
	} else {
		s.releaseBudget <- time.Until(deadline)
	}
	return s.RevocationEventStore.ReleaseHubRuntimeLease(ctx, holderID)
}

func TestWatcher_PublishesGaplessSeqAndAppliesEvents(t *testing.T) {
	env := setup(t)
	apiToken := env.seedAPIToken(t)
	delegationToken := env.seedDelegationToken(t)
	sessionID, _, _, err := auth.Login(context.Background(), env.st, "admin", hubtestutil.TestAdminPassword)
	require.NoError(t, err)

	_, err = env.st.APITokens().Revoke(context.Background(), apiToken)
	require.NoError(t, err)
	_, err = env.st.Users().RevokeUserTokens(context.Background(), env.userID)
	require.NoError(t, err)
	_, err = env.st.DelegationTokens().Revoke(context.Background(), delegationToken)
	require.NoError(t, err)
	_, err = env.st.Sessions().Delete(context.Background(), sessionID)
	require.NoError(t, err)

	published, err := env.st.RevocationEvents().PublishPending(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, int64(4), published)
	events, err := env.st.RevocationEvents().ListPublishedAfter(context.Background(), 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 4)
	var userAuthGeneration int64
	for i, event := range events {
		assert.Equal(t, int64(i+1), event.Seq)
		if event.Event.Kind == store.RevocationEventKindUserTokens {
			userAuthGeneration = event.Event.UserAuthGeneration
		}
	}
	require.Positive(t, userAuthGeneration)

	require.NoError(t, env.watcher.RunOnce(context.Background()))
	assert.Equal(t, []string{sessionID}, env.closer.sessionSnapshot())
	assert.ElementsMatch(t, []string{apiToken, delegationToken}, env.closer.bearerSnapshot())
	assert.ElementsMatch(t, []auth.BearerKind{auth.BearerKindAPI, auth.BearerKindDelegation}, env.closer.bearerKindSnapshot())
	assert.Equal(t, []string{env.userID}, env.closer.userSnapshot())
	assert.Equal(t, []int64{userAuthGeneration}, env.closer.userGenerationSnapshot())
}

func TestWatcher_ConsumesAlreadyPublishedEventsWhenPublishFails(t *testing.T) {
	env := setupUnseeded(t)
	env.watcher = revocationwatcher.New(
		publishFailStore{
			Store:  env.st,
			events: publishFailRevocationEvents{RevocationEventStore: env.st.RevocationEvents()},
		},
		auth.NewCredentialLifecycleEffects(env.cache, env.closer, nil),
	)
	require.NoError(t, env.watcher.SeedCursor(context.Background()))
	t.Cleanup(func() { require.NoError(t, env.watcher.Close(context.Background())) })
	apiToken := env.seedAPIToken(t)
	_, err := env.st.APITokens().Revoke(context.Background(), apiToken)
	require.NoError(t, err)
	published, err := env.st.RevocationEvents().PublishPending(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), published)

	require.NoError(t, env.watcher.RunOnce(context.Background()))

	assert.Equal(t, []string{apiToken}, env.closer.bearerSnapshot())
}

func TestWatcher_OlderRevokedAtPublishedAfterNewerEventIsNotSkipped(t *testing.T) {
	env := setup(t)
	newerUser := env.userID
	olderUser := env.seedUser(t, "older-revoked-at")

	_, err := env.st.Users().RevokeUserTokens(context.Background(), newerUser)
	require.NoError(t, err)
	require.NoError(t, env.watcher.RunOnce(context.Background()))
	require.Equal(t, []string{newerUser}, env.closer.userSnapshot())
	require.Len(t, env.closer.userGenerationSnapshot(), 1)

	_, err = env.st.Users().RevokeUserTokens(context.Background(), olderUser)
	require.NoError(t, err)
	published, err := env.st.RevocationEvents().PublishPending(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), published)
	events, err := env.st.RevocationEvents().ListPublishedAfter(context.Background(), 1, 1)
	require.NoError(t, err)
	require.Len(t, events, 1)

	olderRevokedAt := events[0].Event.RevokedAt.Add(-time.Hour)
	require.NoError(t, env.testHelper(t).SetRevocationEventRevokedAt(
		context.Background(), events[0].Event.ID, olderRevokedAt))

	require.NoError(t, env.watcher.RunOnce(context.Background()))
	assert.Equal(t, []string{newerUser, olderUser}, env.closer.userSnapshot())
	require.Len(t, env.closer.userGenerationSnapshot(), 2)
}

func TestWatcher_LargeBurstIsProcessedInBoundedPages(t *testing.T) {
	env := setupWithOptions(t,
		revocationwatcher.WithPageSize(2),
		revocationwatcher.WithMaxEventsPerRun(5),
	)

	tokens := make([]string, 7)
	for i := range tokens {
		tokens[i] = env.seedAPIToken(t)
		_, err := env.st.APITokens().Revoke(context.Background(), tokens[i])
		require.NoError(t, err)
	}

	require.NoError(t, env.watcher.RunOnce(context.Background()))
	firstRun := env.closer.bearerSnapshot()
	require.Len(t, firstRun, 5)

	require.NoError(t, env.watcher.RunOnce(context.Background()))
	assert.ElementsMatch(t, tokens, env.closer.bearerSnapshot())
}

func TestWatcher_SeedCursorPublishesAndSkipsPreStartEvents(t *testing.T) {
	env := setupUnseeded(t)
	historical := env.seedAPIToken(t)
	_, err := env.st.APITokens().Revoke(context.Background(), historical)
	require.NoError(t, err)

	require.NoError(t, env.watcher.SeedCursor(context.Background()))
	t.Cleanup(func() { require.NoError(t, env.watcher.Close(context.Background())) })
	maxSeq, err := env.st.RevocationEvents().MaxPublishedSeq(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(1), maxSeq)

	require.NoError(t, env.watcher.RunOnce(context.Background()))
	require.Empty(t, env.closer.bearerSnapshot())

	fresh := env.seedAPIToken(t)
	_, err = env.st.APITokens().Revoke(context.Background(), fresh)
	require.NoError(t, err)
	require.NoError(t, env.watcher.RunOnce(context.Background()))
	assert.Equal(t, []string{fresh}, env.closer.bearerSnapshot())
}

func TestWatcher_SeedCursorBoundsPublicationAndPreservesPostFenceEvents(t *testing.T) {
	env := setupUnseededWithOptions(t,
		revocationwatcher.WithPageSize(1),
		revocationwatcher.WithMaxEventsPerRun(2),
	)

	for range 5 {
		tokenID := env.seedAPIToken(t)
		_, err := env.st.APITokens().Revoke(context.Background(), tokenID)
		require.NoError(t, err)
	}

	require.NoError(t, env.watcher.SeedCursor(context.Background()))
	t.Cleanup(func() { require.NoError(t, env.watcher.Close(context.Background())) })
	maxSeq, err := env.st.RevocationEvents().MaxPublishedSeq(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(2), maxSeq)

	fresh := env.seedAPIToken(t)
	_, err = env.st.APITokens().Revoke(context.Background(), fresh)
	require.NoError(t, err)
	for range 3 {
		require.NoError(t, env.watcher.RunOnce(context.Background()))
	}
	assert.Contains(t, env.closer.bearerSnapshot(), fresh)
}

func TestWatcher_SeedCursorRejectsSecondActiveHub(t *testing.T) {
	env := setupUnseeded(t)
	first := revocationwatcher.New(env.st, auth.NewCredentialLifecycleEffects(env.cache, env.closer, nil),
		revocationwatcher.WithLeaseDuration(time.Hour))
	require.NoError(t, first.SeedCursor(context.Background()))
	t.Cleanup(func() { require.NoError(t, first.Close(context.Background())) })

	second := revocationwatcher.New(env.st, auth.NewCredentialLifecycleEffects(nil, nil, nil),
		revocationwatcher.WithLeaseDuration(time.Hour))
	err := second.SeedCursor(context.Background())
	if err == nil {
		t.Cleanup(func() { require.NoError(t, second.Close(context.Background())) })
	}
	require.ErrorIs(t, err, store.ErrHubAlreadyRunning,
		"a live Hub lease must reject a second active Hub")

	require.NoError(t, first.Close(context.Background()))
	require.NoError(t, second.SeedCursor(context.Background()),
		"clean shutdown must allow immediate Hub handoff")
	t.Cleanup(func() { require.NoError(t, second.Close(context.Background())) })
}

func TestWatcher_StartLoopFiresOnSchedule(t *testing.T) {
	env := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env.watcher.StartLoop(ctx)

	tokenID := env.seedAPIToken(t)
	_, err := env.st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		s := env.closer.bearerSnapshot()
		return len(s) == 1 && s[0] == tokenID
	}, 2*time.Second, 25*time.Millisecond)
}

func TestWatcher_StartLoopContinuesAfterSaturatedBatch(t *testing.T) {
	env := setupWithOptions(t,
		revocationwatcher.WithPageSize(1),
		revocationwatcher.WithMaxEventsPerRun(2),
		revocationwatcher.WithInterval(time.Hour),
	)

	tokens := make([]string, 3)
	for i := range tokens {
		tokens[i] = env.seedAPIToken(t)
		_, err := env.st.APITokens().Revoke(context.Background(), tokens[i])
		require.NoError(t, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env.watcher.StartLoop(ctx)

	require.Eventually(t, func() bool {
		return len(env.closer.bearerSnapshot()) == len(tokens)
	}, time.Second, 10*time.Millisecond)
	assert.ElementsMatch(t, tokens, env.closer.bearerSnapshot())
}

func TestWatcher_NoOpsOnEmptyStream(t *testing.T) {
	env := setup(t)
	require.NoError(t, env.watcher.RunOnce(context.Background()))
	assert.Empty(t, env.closer.bearerSnapshot())
	assert.Empty(t, env.closer.userSnapshot())
}

type injectedRevocationStore struct {
	store.Store
	events store.RevocationEventStore
}

func (s injectedRevocationStore) RevocationEvents() store.RevocationEventStore { return s.events }

type unknownKindRevocationEvents struct {
	store.RevocationEventStore
	afterSeqs []int64
}

func (s *unknownKindRevocationEvents) ListPublishedAfter(
	_ context.Context,
	afterSeq int64,
	_ int32,
) ([]store.PublishedRevocationEvent, error) {
	s.afterSeqs = append(s.afterSeqs, afterSeq)
	if afterSeq >= 1 {
		// The lone unknown-kind event at seq 1 has been consumed; stream drained.
		return nil, nil
	}
	return []store.PublishedRevocationEvent{{
		Seq: 1,
		Event: store.RevocationEvent{
			ID: "future-event", Kind: "future_kind",
		},
	}}, nil
}

// RenewHubRuntimeLease always succeeds: this mock fabricates events that don't
// exist in the durable log, so persisting the post-skip cursor advance through
// the real store's renew (which reconciles cursor against the actual log) would
// spuriously report the lease lost. Stubbing it keeps the test focused on the
// unknown-kind SKIP without a fake events store implementing every lease method.
func (s *unknownKindRevocationEvents) RenewHubRuntimeLease(_ context.Context, _ store.RenewHubRuntimeLeaseParams) (bool, error) {
	return true, nil
}

func TestWatcher_SkipsUnknownEventKind(t *testing.T) {
	env := setupUnseeded(t)
	events := &unknownKindRevocationEvents{RevocationEventStore: env.st.RevocationEvents()}
	w := revocationwatcher.New(
		injectedRevocationStore{Store: env.st, events: events},
		auth.NewCredentialLifecycleEffects(nil, nil, nil),
	)
	require.NoError(t, w.SeedCursor(context.Background()))
	t.Cleanup(func() { require.NoError(t, w.Close(context.Background())) })

	// An unrecognized event kind is logged and skipped, NOT fatal: RunOnce
	// succeeds and the cursor advances past the bad row so the sole Hub keeps
	// serving instead of taking a full outage on one unprocessable event. The
	// first RunOnce reads the short (sub-limit) page and stops without a trailing
	// empty fetch (the drain short-circuit), having advanced the cursor to seq 1;
	// the SECOND RunOnce therefore lists from afterSeq == 1 -- proving the cursor
	// moved past the skip and the bad row is not re-processed.
	require.NoError(t, w.RunOnce(context.Background()))
	select {
	case err := <-w.Errors():
		t.Fatalf("unknown event kind must not signal a fatal watcher error, got %v", err)
	default:
	}
	require.NoError(t, w.RunOnce(context.Background()))
	require.Contains(t, events.afterSeqs, int64(1), "cursor must advance past the skipped unknown event")
}

func TestWatcher_TolerantOfNilCacheAndCloser(t *testing.T) {
	env := setupUnseeded(t)
	w := revocationwatcher.New(env.st, auth.NewCredentialLifecycleEffects(nil, nil, nil))
	require.NoError(t, w.SeedCursor(context.Background()))
	t.Cleanup(func() { require.NoError(t, w.Close(context.Background())) })

	tokenID := env.seedAPIToken(t)
	_, err := env.st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)

	require.NotPanics(t, func() {
		require.NoError(t, w.RunOnce(context.Background()))
	})
}

func TestWatcher_AdvancesDurableCursorAfterApplyingPage(t *testing.T) {
	env := setup(t)
	tokenID := env.seedAPIToken(t)
	_, err := env.st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)

	require.NoError(t, env.watcher.RunOnce(context.Background()))
	require.Equal(t, []string{tokenID}, env.closer.bearerSnapshot())

	deleted, err := env.st.Cleanup().CompactPublishedRevocationEvents(
		context.Background(),
		store.CompactRevocationEventsParams{
			Cutoff: time.Now().UTC().Add(time.Hour),
		},
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
}

// leaseLostRevocationEvents simulates another Hub having stolen (or expired) the
// runtime lease: RenewHubRuntimeLease matches no row, so renewal reports the
// lease was not advanced.
type leaseLostRevocationEvents struct {
	store.RevocationEventStore
}

func (leaseLostRevocationEvents) RenewHubRuntimeLease(context.Context, store.RenewHubRuntimeLeaseParams) (bool, error) {
	return false, nil
}

// Losing the lease (a renewal that advances no row) must self-fence the Hub.
// The lease-renewal heartbeat runs independently of event processing, so the
// self-fence fires even when the processing interval is far longer than the
// lease.
func TestWatcher_LeaseLossIsFatal(t *testing.T) {
	env := setupUnseeded(t)
	injected := injectedRevocationStore{
		Store:  env.st,
		events: leaseLostRevocationEvents{RevocationEventStore: env.st.RevocationEvents()},
	}
	w := revocationwatcher.New(injected, auth.NewCredentialLifecycleEffects(env.cache, env.closer, nil),
		revocationwatcher.WithLeaseDuration(20*time.Millisecond),
		revocationwatcher.WithInterval(time.Hour),
	)
	require.NoError(t, w.SeedCursor(context.Background()))
	t.Cleanup(func() { _ = w.Close(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.StartLoop(ctx)

	select {
	case err := <-w.Errors():
		require.ErrorIs(t, err, revocationwatcher.ErrLeaseLost)
	case <-time.After(time.Second):
		t.Fatal("watcher did not self-fence after losing its lease")
	}
}

// blockingCloser blocks the first CloseChannelsByBearer until released, standing
// in for a wedged worker/frontend that stalls a channel teardown.
type blockingCloser struct {
	*fakeCloser
	entered     chan struct{}
	enterOnce   sync.Once
	block       chan struct{}
	releaseOnce sync.Once
}

func newBlockingCloser() *blockingCloser {
	return &blockingCloser{fakeCloser: &fakeCloser{}, entered: make(chan struct{}), block: make(chan struct{})}
}

func (c *blockingCloser) release() { c.releaseOnce.Do(func() { close(c.block) }) }

func (c *blockingCloser) CloseChannelsByBearer(ref auth.BearerRef) int {
	c.enterOnce.Do(func() { close(c.entered) })
	<-c.block
	return c.fakeCloser.CloseChannelsByBearer(ref)
}

// The revocation watcher must not hold its lease hostage to channel teardown: a
// wedged teardown (blocking well past the lease duration) must NOT self-fence
// the Hub, and the event must still complete once teardown unblocks. Before the
// decoupling, runOnce held w.lease.mu across teardown so the lease could not be
// renewed and the Hub self-fenced.
func TestWatcher_SlowTeardownDoesNotSelfFence(t *testing.T) {
	env := setupUnseeded(t)
	blocking := newBlockingCloser()
	w := revocationwatcher.New(env.st, auth.NewCredentialLifecycleEffects(env.cache, blocking, nil),
		revocationwatcher.WithLeaseDuration(40*time.Millisecond),
		revocationwatcher.WithInterval(20*time.Millisecond),
	)
	require.NoError(t, w.SeedCursor(context.Background()))
	t.Cleanup(func() { blocking.release(); _ = w.Close(context.Background()) })

	tokenID := env.seedAPIToken(t)
	_, err := env.st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.StartLoop(ctx)

	select {
	case <-blocking.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("teardown never started")
	}

	// Hold the teardown for several lease durations. A healthy heartbeat keeps
	// the lease alive; a self-fence here would surface on the Errors channel.
	select {
	case err := <-w.Errors():
		t.Fatalf("watcher self-fenced during a slow teardown: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	blocking.release()
	require.Eventually(t, func() bool {
		s := blocking.bearerSnapshot()
		return len(s) == 1 && s[0] == tokenID
	}, 2*time.Second, 10*time.Millisecond, "the revocation must complete once teardown unblocks")
}

func TestWatcher_CloseDuringSlowTeardownPreventsPostCloseRenewal(t *testing.T) {
	env := setupUnseeded(t)
	blocking := newBlockingCloser()
	w := revocationwatcher.New(env.st, auth.NewCredentialLifecycleEffects(env.cache, blocking, nil),
		revocationwatcher.WithLeaseDuration(time.Second),
		revocationwatcher.WithInterval(10*time.Millisecond),
	)
	require.NoError(t, w.SeedCursor(context.Background()))

	tokenID := env.seedAPIToken(t)
	_, err := env.st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)
	w.StartLoop(context.Background())

	select {
	case <-blocking.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("teardown never started")
	}

	closeCtx, cancelClose := context.WithCancel(context.Background())
	cancelClose()
	require.ErrorIs(t, w.Close(closeCtx), context.Canceled)
	blocking.release()

	select {
	case err := <-w.Errors():
		t.Fatalf("watcher touched the released lease after Close returned: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestWatcher_CloseCancelsPublicRunOnceWithoutWaiting(t *testing.T) {
	env := setupUnseeded(t)
	events := &cancelablePublishRevocationEvents{
		RevocationEventStore: env.st.RevocationEvents(),
		entered:              make(chan struct{}),
		exited:               make(chan struct{}),
	}
	w := revocationwatcher.New(
		injectedRevocationStore{Store: env.st, events: events},
		auth.NewCredentialLifecycleEffects(env.cache, env.closer, nil),
	)
	require.NoError(t, w.SeedCursor(context.Background()))

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	runErr := make(chan error, 1)
	go func() { runErr <- w.RunOnce(runCtx) }()
	select {
	case <-events.entered:
	case <-time.After(time.Second):
		t.Fatal("RunOnce never reached the store")
	}

	closeCtx, cancelClose := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancelClose()
	// Close cancels the RunOnce through operationsCtx and returns WITHOUT waiting
	// for the in-flight store mutation: only the owned loop is drained via
	// stopLoop, and a directly-invoked RunOnce (no production caller) unwinds on
	// its own via the cancelled context.
	require.NoError(t, w.Close(closeCtx))

	// The mutation unwinds (its context was cancelled) and RunOnce reports the
	// error -- but only after Close has already returned.
	require.Error(t, <-runErr)
	select {
	case <-events.exited:
	case <-time.After(time.Second):
		t.Fatal("RunOnce store mutation did not unwind after Close cancelled it")
	}
}

func TestWatcher_CloseCancelsAndDrainsConcurrentSeed(t *testing.T) {
	env := setupUnseeded(t)
	events := &cancelableAcquireRevocationEvents{
		RevocationEventStore: env.st.RevocationEvents(),
		entered:              make(chan struct{}),
		exited:               make(chan struct{}),
	}
	w := revocationwatcher.New(
		injectedRevocationStore{Store: env.st, events: events},
		auth.NewCredentialLifecycleEffects(env.cache, env.closer, nil),
	)

	seedErr := make(chan error, 1)
	go func() { seedErr <- w.SeedCursor(context.Background()) }()
	select {
	case <-events.entered:
	case <-time.After(time.Second):
		t.Fatal("SeedCursor never reached the store")
	}

	closeCtx, cancelClose := context.WithTimeout(context.Background(), time.Second)
	defer cancelClose()
	require.NoError(t, w.Close(closeCtx))
	select {
	case <-events.exited:
	default:
		t.Fatal("Close returned while lease acquisition was still running")
	}
	require.Error(t, <-seedErr)
}

func TestWatcher_CloseGivesLeaseReleaseFreshBudgetAfterSweepDrain(t *testing.T) {
	env := setupUnseeded(t)
	blocking := newBlockingCloser()
	events := &releaseDeadlineRevocationEvents{
		RevocationEventStore: env.st.RevocationEvents(),
		releaseBudget:        make(chan time.Duration, 1),
	}
	w := revocationwatcher.New(
		injectedRevocationStore{Store: env.st, events: events},
		auth.NewCredentialLifecycleEffects(env.cache, blocking, nil),
	)
	require.NoError(t, w.SeedCursor(context.Background()))

	tokenID := env.seedAPIToken(t)
	_, err := env.st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)
	runErr := make(chan error, 1)
	go func() { runErr <- w.RunOnce(context.Background()) }()
	select {
	case <-blocking.entered:
	case <-time.After(time.Second):
		t.Fatal("RunOnce never reached the lifecycle effect")
	}

	time.AfterFunc(75*time.Millisecond, blocking.release)
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancelClose()
	require.NoError(t, w.Close(closeCtx))
	require.Error(t, <-runErr)

	remaining := <-events.releaseBudget
	require.Greater(t, remaining, 4*time.Second,
		"lease deletion needs a fresh cleanup budget after waiting for the sweep")
}

func TestWatcher_ClosedWatcherRejectsSeedAndStart(t *testing.T) {
	env := setupUnseeded(t)
	w := revocationwatcher.New(env.st, auth.NewCredentialLifecycleEffects(env.cache, env.closer, nil))
	require.NoError(t, w.Close(context.Background()))

	require.ErrorIs(t, w.SeedCursor(context.Background()), revocationwatcher.ErrClosed)
	require.ErrorIs(t, w.RunOnce(context.Background()), revocationwatcher.ErrClosed)
	w.StartLoop(context.Background())
	select {
	case err := <-w.Errors():
		t.Fatalf("StartLoop on a closed watcher must be a no-op, got: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWatcher_SeedRejectsRegistrationThatExceedsLocalLeaseBudget(t *testing.T) {
	env := setupUnseeded(t)
	delayedStore := publishFailStore{
		Store: env.st,
		events: delayedAcquireRevocationEvents{
			RevocationEventStore: env.st.RevocationEvents(),
			delay:                20 * time.Millisecond,
		},
	}
	w := revocationwatcher.New(delayedStore, auth.NewCredentialLifecycleEffects(nil, nil, nil),
		revocationwatcher.WithLeaseDuration(5*time.Millisecond))

	err := w.SeedCursor(context.Background())
	require.ErrorIs(t, err, revocationwatcher.ErrLeaseLost)

	replacement := revocationwatcher.New(env.st, auth.NewCredentialLifecycleEffects(nil, nil, nil))
	require.NoError(t, replacement.SeedCursor(context.Background()),
		"failed startup must release a lease it acquired before exceeding its local budget")
	t.Cleanup(func() { require.NoError(t, replacement.Close(context.Background())) })
}

func TestWatcher_CloseRemovesDurableConsumer(t *testing.T) {
	env := setupUnseeded(t)
	require.NoError(t, env.watcher.SeedCursor(context.Background()))
	t.Cleanup(func() { require.NoError(t, env.watcher.Close(context.Background())) })

	tokenID := env.seedAPIToken(t)
	_, err := env.st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)
	_, err = env.st.RevocationEvents().PublishPending(context.Background(), 10)
	require.NoError(t, err)
	compact := func() int64 {
		deleted, compactErr := env.st.Cleanup().CompactPublishedRevocationEvents(
			context.Background(),
			store.CompactRevocationEventsParams{Cutoff: time.Now().UTC().Add(time.Hour)},
		)
		require.NoError(t, compactErr)
		return deleted
	}

	require.Zero(t, compact(), "unacknowledged event must remain while the Hub lease is live")
	require.NoError(t, env.watcher.Close(context.Background()))
	require.Equal(t, int64(1), compact(), "graceful close must release the Hub cursor")
}
