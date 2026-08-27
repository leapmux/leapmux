package hub

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// TestElevationSlideInterceptorIsMounted pins the WIRING, which no test in
// the service package can reach.
//
// Every test of the report itself mounts NewElevationSlideInterceptor in its
// own harness, so all of them keep passing when NewServer stops building with
// it -- and the hub then ships a slide that reports nothing, which leaves every
// client's copy of the deadline up to a whole auth.ElevationWindow early for the
// whole window. The interceptor emits the header alone, so the mounted mux is
// the only place the omission is visible.
//
// It drives the REAL server wiring, through the same mux the binaries serve,
// with a real elevated session and a real restricted write.
func TestElevationSlideInterceptorIsMounted(t *testing.T) {
	ctx := context.Background()
	srv := startTestServer(t, &config.Config{})
	st := srv.Store()

	hash, err := password.Hash("adminpass123")
	require.NoError(t, err)
	admin, err := service.CreateUser(ctx, st, service.CreateUserParams{
		Username: "admin", PasswordHash: hash, DisplayName: "Admin",
		PasswordSet: true, IsAdmin: true,
	})
	require.NoError(t, err)
	token, _, _, err := auth.Login(ctx, st, "admin", "adminpass123", auth.DefaultSessionDuration)
	require.NoError(t, err)

	// A window close to lapsing, so the slide has somewhere to move to. The
	// statement refuses a deadline no later than the stored one, so a fresh
	// full-length elevation would slide nothing and report nothing.
	now := time.Now().UTC()
	n, err := st.Sessions().Elevate(ctx, store.ElevateSessionParams{
		SessionID:          token,
		UserID:             userid.MustNew(admin.ID),
		ElevationProvenAt:  now,
		ElevationExpiresAt: now.Add(time.Minute),
	}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	// A real restricted write: it slides the window that admitted it.
	body := `{"key":"` + settings.KeySignupEnabled.Name() + `","partial_json":"true"}`
	req := httptest.NewRequest(http.MethodPost,
		"/leapmux.v1.AdminSettingsService/UpdateSetting", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", auth.CookieName+"="+token)
	rec := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	reported := rec.Header().Get(service.ElevationExpiresAtHeader)
	require.NotEmpty(t, reported,
		"NewServer must mount the elevation slide interceptor, or no client ever learns the new deadline")

	// The value is the deadline the STORE now holds, which is also what
	// proves the write really slid rather than the header carrying a
	// constant.
	row, err := st.Sessions().GetByID(ctx, token, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, row.ElevationExpiresAt)
	assert.Equal(t, row.ElevationExpiresAt.UTC().Format(time.RFC3339Nano), reported)
	assert.True(t, row.ElevationExpiresAt.After(now.Add(time.Minute)),
		"the write must extend the window it used")
}
