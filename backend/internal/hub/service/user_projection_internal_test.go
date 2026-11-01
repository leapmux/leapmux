package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// failingLinksStore answers every OAuth link read with an error, so a test
// can drive the projection's failure paths.
type failingLinksStore struct {
	store.Store
}

func (s failingLinksStore) OAuthUserLinks() store.OAuthUserLinkStore {
	return failingLinks{OAuthUserLinkStore: s.Store.OAuthUserLinks()}
}

type failingLinks struct {
	store.OAuthUserLinkStore
}

func (failingLinks) ListByUser(context.Context, userid.UserID) ([]store.OAuthUserLink, error) {
	return nil, errors.New("link read failed")
}

// failingCountAndLinksStore answers every passkey count and every OAuth link
// read with an error.
type failingCountAndLinksStore struct {
	failingLinksStore
}

func (s failingCountAndLinksStore) PasskeyCredentials() store.PasskeyCredentialStore {
	return failingPasskeyCount{PasskeyCredentialStore: s.Store.PasskeyCredentials()}
}

type failingPasskeyCount struct {
	store.PasskeyCredentialStore
}

func (failingPasskeyCount) CountByUser(context.Context, string) (int64, error) {
	return 0, errors.New("passkey count failed")
}

func openProjectionStore(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlite.OpenTestable(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// An account with no password and no passkey elevates only through a
// provider, so its link read cannot degrade: the projection fails, and a
// sign-in that already committed reports the session with the user absent.
// The client then reads GetCurrentUser.
func TestSignInReportOmitsTheUserWhenAProviderOnlyAccountCannotReadItsLinks(t *testing.T) {
	t.Parallel()
	st := openProjectionStore(t)
	user := storetest.SeedPasswordlessUser(t, st, "provider-only")

	_, err := completeUserProto(context.Background(), failingLinksStore{st}, user)
	require.Error(t, err, "the provider is this account's only step-up option, so the failure must surface")

	verification := &leapmuxv1.EmailVerificationStatus{VerificationRequired: true}
	elevation := timestamppb.Now()
	report := signInReport(context.Background(), failingLinksStore{st}, user, verification, elevation)
	assert.Nil(t, report.GetUser(), "a projection that failed leaves the user absent")
	assert.Same(t, verification, report.GetEmailVerification(), "the verification outcome still reaches the client")
	assert.Same(t, elevation, report.GetElevationExpiresAt(), "the elevation still reaches the client")
}

// An account that holds a password keeps a factor of its own, so the same link
// failure degrades: the projection answers with no providers and no error.
func TestCompleteUserProtoDegradesTheLinkReadForAnAccountWithAPassword(t *testing.T) {
	t.Parallel()
	st := openProjectionStore(t)
	user := storetest.SeedUser(t, st, "has-password")

	userProto, err := completeUserProto(context.Background(), failingLinksStore{st}, user)
	require.NoError(t, err)
	assert.Equal(t, user.ID, userProto.GetId())
	assert.Empty(t, userProto.GetOauthProviders())
	assert.False(t, userProto.GetMayElevateThroughAProvider())

	report := signInReport(context.Background(), failingLinksStore{st}, user, nil, nil)
	require.NotNil(t, report.GetUser(), "a degraded projection is still a projection")
	assert.Nil(t, report.GetElevationExpiresAt(), "a fresh sign-in carries no elevation")
}

// A passkey count that fails is not a fact about the account, so the step-up
// rule takes no answer from it. The provider-only shape is then never derived,
// and the link failure degrades instead of failing the projection: one failed
// COUNT must not turn a read into a fatal one.
func TestCompleteUserProtoDegradesWhenThePasskeyCountAlsoFails(t *testing.T) {
	t.Parallel()
	st := openProjectionStore(t)
	user := storetest.SeedPasswordlessUser(t, st, "count-fails")
	failing := failingCountAndLinksStore{failingLinksStore{st}}

	userProto, err := completeUserProto(context.Background(), failing, user)
	require.NoError(t, err, "an unread count keeps the account out of the provider-only shape")
	assert.Equal(t, user.ID, userProto.GetId())
	assert.Zero(t, userProto.GetPasskeyCount(), "the displayed count degrades to zero")
	assert.Empty(t, userProto.GetOauthProviders())
	assert.False(t, userProto.GetMayElevateThroughAProvider(),
		"an unread count fails closed and offers no provider option")

	report := signInReport(context.Background(), failing, user, nil, nil)
	assert.Equal(t, user.ID, report.GetUser().GetId(), "the sign-in still reports the degraded account")
}

// A row with a blank id is not an account the projection can address, so it
// refuses the row rather than report an account with no id.
func TestCompleteUserProtoRefusesARowWithABlankID(t *testing.T) {
	t.Parallel()
	st := openProjectionStore(t)

	_, err := completeUserProto(context.Background(), st, &store.User{Username: "blank-id", FirstCredentialExempt: true})
	require.Error(t, err)

	report := signInReport(context.Background(), st, &store.User{Username: "blank-id", FirstCredentialExempt: true}, nil, nil)
	assert.Nil(t, report.GetUser(), "a sign-in leaves the user absent rather than report it with no id")
}

// storedUserProto reads the account back after a mutation. A read that fails
// answers nil, so the response omits the user rather than the RPC failing.
func TestStoredUserProtoAnswersNilWhenTheAccountCannotBeRead(t *testing.T) {
	t.Parallel()
	st := openProjectionStore(t)
	user := storetest.SeedUser(t, st, "readable")

	assert.Equal(t, user.ID, storedUserProto(context.Background(), st, user.ID).GetId())
	assert.Nil(t, storedUserProto(context.Background(), st, "00000000000000000000000000"))
	assert.Nil(t, storedUserProto(context.Background(), failingLinksStore{st}, storetest.SeedPasswordlessUser(t, st, "no-links").ID),
		"a projection that fails answers nil too")
}
