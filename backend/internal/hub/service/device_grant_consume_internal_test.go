package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type deviceGrantConsumeRecorder struct {
	DeviceAuthorizationStore
	consumed bool
	calls    int
	digest   []byte
	now      time.Time
}

func (s *deviceGrantConsumeRecorder) Consume(digest []byte, now time.Time) bool {
	s.calls++
	s.digest = append([]byte(nil), digest...)
	s.now = now
	return s.consumed
}

func TestAnswerElevationPollPassesHandlerTimeAndMapsConsumeResult(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.November, 13, 4, 5, 6, 7, time.UTC)
	for _, tc := range []struct {
		name       string
		consumed   bool
		wantStatus int
		wantBody   string
	}{
		{"consumed", true, http.StatusOK, `{"elevated":true}`},
		{"not consumed", false, http.StatusBadRequest, `{"error":"invalid_grant","error_description":"device_code expired or already consumed"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := hubtestutil.OpenTestStore(t)
			hubtestutil.CreateTestAdmin(t, st)
			admin, err := st.Users().GetByUsername(t.Context(), hubtestutil.TestAdminUsername)
			require.NoError(t, err)
			uid := userid.MustNew(admin.ID)

			tokenID := "tok-elevation-poll"
			exp := now.Add(time.Hour)
			require.NoError(t, st.APITokens().Create(t.Context(), store.CreateAPITokenParams{
				Issuance:         store.APITokenIssuanceDeviceCode,
				ID:               tokenID,
				UserID:           uid,
				ClientID:         oauthapp.ControlCLIClientID,
				InstallationName: "test",
				GrantedScopes:    "workspace:read",
				SecretHash:       []byte("secret-hash-elevation-poll-01234"),
				ExpiresAt:        &exp,
			}))
			n, err := st.APITokens().Elevate(t.Context(), store.ElevateAPITokenParams{
				TokenID:            tokenID,
				UserID:             uid,
				ElevationProvenAt:  now,
				ElevationExpiresAt: now.Add(auth.ElevationWindow),
			}, now)
			require.NoError(t, err)
			require.EqualValues(t, 1, n)

			recorder := &deviceGrantConsumeRecorder{consumed: tc.consumed}
			h := NewOAuthServerHandler(OAuthServerDeps{
				SessionHasher:        hubtestutil.TestSecretHasher(),
				Store:                st,
				Lifecycle:            auth.NewCredentialLifecycleEffects(nil, nil, nil),
				Notices:              NewCredentialNotices(nil, mail.Renderer{}),
				ServingCtx:           context.Background(),
				DeviceAuthorizations: recorder,
			})
			h.Now = func() time.Time { return now }

			digest := []byte{0, 1, 255}
			req := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
			rec := httptest.NewRecorder()
			h.answerElevationPoll(rec, req, &DeviceAuthorization{ElevateTokenID: tokenID}, digest)

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.JSONEq(t, tc.wantBody, rec.Body.String())
			assert.Equal(t, 1, recorder.calls)
			assert.Equal(t, digest, recorder.digest)
			assert.Equal(t, now, recorder.now)
		})
	}
}
