package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/settingsregistry"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCredentialConstructors_RequireHashersOnlyForCredentialCapableStores(t *testing.T) {
	t.Parallel()
	st := hubtestutil.OpenTestStore(t)
	lifecycle := auth.NewCredentialLifecycleEffects(nil, nil, nil)
	for _, constructor := range []struct {
		name, panicText string
		build           func(store.Store, auth.SecretHasher)
	}{
		{"auth service", "auth service requires a session secret hasher when a store is configured", func(st store.Store, hasher auth.SecretHasher) {
			NewAuthService(AuthServiceDeps{Store: st, SessionHasher: hasher, Lifecycle: lifecycle})
		}},
		{"identity provider", "oauth handler requires a session secret hasher when a store is configured", func(st store.Store, hasher auth.SecretHasher) { NewIdPHandler(st, hasher, nil, nil, lifecycle, nil) }},
		{"OAuth server", "OAuth server requires a session secret hasher when a store is configured", func(st store.Store, hasher auth.SecretHasher) {
			NewOAuthServerHandler(OAuthServerDeps{Store: st, SessionHasher: hasher, Lifecycle: lifecycle, ServingCtx: context.Background(), Notices: NewCredentialNotices(nil, mail.Renderer{})})
		}},
		{"worker connector", "service: NewWorkerConnectorService requires a secret hasher when a store is configured", func(st store.Store, hasher auth.SecretHasher) {
			NewWorkerConnectorService(WorkerConnectorServiceDeps{Store: st, Hasher: hasher, QueuePool: sendq.NewMaxBytesPoolForTest(), Clock: quartz.NewReal(), DeregisterEffects: NewWorkerDeregisterEffects(nil, nil, nil)})
		}},
	} {
		t.Run(constructor.name, func(t *testing.T) {
			assert.PanicsWithValue(t, constructor.panicText, func() { constructor.build(st, auth.SecretHasher{}) })
			assert.NotPanics(t, func() { constructor.build(st, hubtestutil.TestSecretHasher()) })
			assert.NotPanics(t, func() { constructor.build(nil, auth.SecretHasher{}) })
		})
	}
}

func TestWorkerManagementConstructor_RequiresSecretHasher(t *testing.T) {
	t.Parallel()
	st := hubtestutil.OpenTestStore(t)
	manager := workermgr.New(workermgr.DenyAllReach())
	settings := settingsregistry.NewManager(st, nil)
	sender := mail.NewDisabledSender()
	assert.PanicsWithValue(t, "service: NewWorkerManagementService requires a secret hasher", func() {
		NewWorkerManagementService(st, auth.SecretHasher{}, manager, nil, sender, mail.Renderer{}, settings)
	})
	assert.NotPanics(t, func() {
		NewWorkerManagementService(st, hubtestutil.TestSecretHasher(), manager, nil, sender, mail.Renderer{}, settings)
	})
}

func TestWorkerConnector_StorelessChannelCleanupNeedsNoHasher(t *testing.T) {
	t.Parallel()
	channels := channelmgr.New(0)
	channels.RegisterWithAuthInfo("owned", "worker", "user", channelmgr.AuthInfo{}, nil)
	channels.RegisterWithAuthInfo("other", "another-worker", "user", channelmgr.AuthInfo{}, nil)
	connector := NewWorkerConnectorService(WorkerConnectorServiceDeps{ChannelManager: channels, QueuePool: sendq.NewMaxBytesPoolForTest(), Clock: quartz.NewReal(), DeregisterEffects: NewWorkerDeregisterEffects(nil, nil, nil)})
	connector.closeWorkerChannels("worker")
	assert.False(t, channels.Exists("owned"))
	assert.True(t, channels.Exists("other"))
}

func TestOAuthServer_MetadataNeedsNeitherStoreNorHasher(t *testing.T) {
	t.Parallel()
	handler := NewOAuthServerHandler(OAuthServerDeps{Lifecycle: auth.NewCredentialLifecycleEffects(nil, nil, nil), ServingCtx: context.Background(), Notices: NewCredentialNotices(nil, mail.Renderer{}), HubURL: func() string { return "https://hub.example.test" }})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-protected-resource"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, recorder.Code)
		assert.Contains(t, recorder.Body.String(), "https://hub.example.test")
	}
}

func TestBrowserWebSocketConstructors_RequireSessionHasherWithOrWithoutStore(t *testing.T) {
	t.Parallel()
	st := hubtestutil.OpenTestStore(t)
	for _, storeCase := range []struct {
		name  string
		store store.Store
	}{{"store", st}, {"storeless", nil}} {
		for _, endpoint := range []string{"channel relay", "user events"} {
			t.Run(storeCase.name+"/"+endpoint, func(t *testing.T) {
				build := func(hasher auth.SecretHasher) http.Handler {
					if endpoint == "channel relay" {
						return NewChannelRelayHandler(storeCase.store, hasher, newTestRegistry(), nil, newTestAuthContexts(t), nil, insecureCookies, sendq.NewMaxBytesPoolForTest())
					}
					return NewUserEventsHandler(storeCase.store, hasher, nil, newTestAuthContexts(t), nil, insecureCookies, sendq.NewMaxBytesPoolForTest())
				}
				assert.PanicsWithValue(t, "service: WebSocket handler requires a session secret hasher", func() { build(auth.SecretHasher{}) })
				var handler http.Handler
				assert.NotPanics(t, func() { handler = build(hubtestutil.TestSecretHasher()) })
				require.NotNil(t, handler)
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://example.test/", nil))
				assert.Equal(t, http.StatusUnauthorized, recorder.Code)
			})
		}
	}
}

func TestWebSocketConstructor_PreservesRegistryDiagnosticBeforeHasherDiagnostic(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "service: WebSocket handler requires an auth context registry", func() {
		NewUserEventsHandler(nil, auth.SecretHasher{}, nil, nil, nil, insecureCookies, sendq.NewMaxBytesPoolForTest())
	})
}
