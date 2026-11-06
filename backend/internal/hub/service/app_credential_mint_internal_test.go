package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// The gate is a property of the MINT, not of what each handler remembered to
// type. Two surfaces mint the same command-line credential, the classification
// tripwire in user_procedures_internal_test.go cannot reach an Admin*
// procedure, and the consent legs are mux routes rather than Connect
// procedures -- so the omission the gate exists to prevent was possible at
// exactly the place it mattered, and it already happened once.
func TestMintAuthority(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	uid := userid.MustNew("6PwpqVF2YPRs9izaHencG20nazPAhW0aXNAKLBdAnBui")
	session := func(elevatedUntil *time.Time) *auth.UserInfo {
		return &auth.UserInfo{
			ID:         uid,
			Credential: auth.SessionCredential("s-1"),
			Elevation:  auth.NewElevation(&now, elevatedUntil),
		}
	}

	t.Run("a consumed consent grant permits the mint", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, mintedByConsentGrant("code-1").assert(now))
	})

	// /oauth/token carries no session at all, so the grant row IS the
	// proof -- but only a caller that actually loaded one can pass it.
	t.Run("an absent grant with no actor is refused", func(t *testing.T) {
		t.Parallel()
		err := mintedByConsentGrant("").assert(now)
		require.Error(t, err)
		assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	})

	t.Run("an elevated session permits the mint", func(t *testing.T) {
		t.Parallel()
		until := now.Add(time.Hour)
		require.NoError(t, mintedByActor(session(&until)).assert(now))
	})

	t.Run("an un-elevated session is refused with the prompt marker", func(t *testing.T) {
		t.Parallel()
		err := mintedByActor(session(nil)).assert(now)
		require.Error(t, err)
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		// The prompt marker rides the response header, keyed on the sentinel
		// (NewElevationResponseInterceptor): a refusal that wraps it is the one
		// a step-up prompt can clear.
		assert.True(t, errors.Is(err, errElevationRequired),
			"the refusal must be the one a step-up prompt can clear")
	})

	t.Run("a lapsed elevation is refused", func(t *testing.T) {
		t.Parallel()
		lapsed := now.Add(-time.Minute)
		require.Error(t, mintedByActor(session(&lapsed)).assert(now))
	})

	// A COMMAND-LINE credential takes the same rule as a session: it has a
	// row of its own to stamp now, and it proves the factor in a browser
	// through /oauth/step-up. Admitting it unelevated made
	// possession of the credential file the whole of the check for the mint.
	t.Run("an elevated command-line credential is admitted", func(t *testing.T) {
		t.Parallel()
		until := now.Add(time.Hour)
		bearer := &auth.UserInfo{
			ID:         uid,
			Credential: auth.APICredential("t-1"),
			Elevation:  auth.Elevation{ExpiresAt: until},
		}
		require.NoError(t, mintedByActor(bearer).assert(now))
	})

	t.Run("an unelevated command-line credential is refused", func(t *testing.T) {
		t.Parallel()
		bearer := &auth.UserInfo{ID: uid, Credential: auth.APICredential("t-1")}
		require.Error(t, mintedByActor(bearer).assert(now))
	})

	// A DELEGATION bearer can carry no elevation at all -- a worker mints it
	// for an agent that reads untrusted input -- so it is refused whatever
	// the row says.
	t.Run("a delegation bearer is refused", func(t *testing.T) {
		t.Parallel()
		until := now.Add(time.Hour)
		delegated := &auth.UserInfo{
			ID:         uid,
			Credential: auth.DelegationCredential("d-1", "w-1"),
			Elevation:  auth.Elevation{ExpiresAt: until},
		}
		require.Error(t, mintedByActor(delegated).assert(now))
	})

	t.Run("no authority at all is refused", func(t *testing.T) {
		t.Parallel()
		require.Error(t, mintAuthorityZeroForTest().assert(now))
	})
}

// mintAuthorityZeroForTest is the shape a new mint site would produce by
// forgetting to state an authority at all.
func mintAuthorityZeroForTest() mintAuthority { return mintAuthority{} }

// TestMintAuthorityClampTreatsSoloAsProven pins the solo classification: the
// synthetic user carries no credential row, so a session-id test alone sorted
// it onto the BEARER side of the clamp and a solo hub's `admin api-token issue`
// minted a non-rotating credential measured from process start -- the
// year-long shape the clamp exists to prevent, on the one hub whose user
// proved everything by reaching the port.
func TestMintAuthorityClampTreatsSoloAsProven(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	uid := userid.MustNew("6PwpqVF2YPRs9izaHencG20nazPAhW0aXNAKLBdAnBui")
	solo := &auth.UserInfo{ID: uid, Solo: true, IsAdmin: true}

	spec := apiTokenMint{
		UserID:        uid,
		ClientID:      "leapmux-service-account",
		GrantedScopes: "account:read",
		AccessTTL:     0, // the rotating default
		Rotating:      true,
	}
	clamped, err := mintedByActor(solo).clamp(spec, now)
	require.NoError(t, err)
	assert.True(t, clamped.Rotating, "a solo actor stands on the port, so nothing clamps the mint")
	assert.Equal(t, time.Duration(0), clamped.AccessTTL, "the ordinary access window applies")
}

// The kind() classification is what assert and clamp both read; pin each
// actor class to its side of the clamp so a new class cannot land on the
// wrong one silently.
func TestAuthorityKindClassifiesEveryActor(t *testing.T) {
	require.Equal(t, consentGrantAuthority, mintedByConsentGrant("g1").kind())
	require.Equal(t, missingAuthority, mintAuthority{}.kind())
	require.Equal(t, sessionActorAuthority, mintedByActor(&auth.UserInfo{
		Solo:       true,
		Credential: auth.APICredential("t1"),
	}).kind())
	sessionCred := auth.UserInfo{Credential: auth.SessionCredential("s1")}
	require.Equal(t, sessionActorAuthority, mintedByActor(&sessionCred).kind())
	bearer := auth.UserInfo{Credential: auth.APICredential("t2")}
	require.Equal(t, bearerActorAuthority, mintedByActor(&bearer).kind())
}

// parkedSender holds a send open until the test releases it.
type parkedSender struct {
	entered chan struct{}
	release chan struct{}
}

func newParkedSender() parkedSender {
	return parkedSender{entered: make(chan struct{}), release: make(chan struct{})}
}

func (s parkedSender) Send(context.Context, mail.Message) error {
	close(s.entered)
	<-s.release
	return nil
}

func (s parkedSender) awaitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-s.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the notice never reached the sender")
	}
}

func noticeRecipient() *store.User {
	return &store.User{
		ID:            "6PwpqVF2YPRs9izaHencG20nazPAhW0aXNAKLBdAnBui",
		Email:         "owner@example.com",
		EmailVerified: true,
	}
}

// The Hub's shutdown joins a notice through this counter, so a send must stay
// counted until it ends. Otherwise an exit during the send loses the email.
func TestNotifyCredentialIssuedCountsTheSendUntilItEnds(t *testing.T) {
	t.Parallel()
	sender := newParkedSender()
	release := sync.OnceFunc(func() { close(sender.release) })
	defer release()
	notices := NewCredentialNotices(sender, mail.Renderer{})

	notices.Notify(context.Background(), noticeRecipient(), credentialNotice{AppName: "app"})

	sender.awaitEntered(t)
	assert.False(t, notices.Wait(0, ""), "a running send must be counted")
	release()
	assert.True(t, notices.Wait(30*time.Second, ""), "an ended send must leave the count")
}

// A notice that never starts must leave nothing for the shutdown to wait for.
func TestNotifyCredentialIssuedCountsNothingWhenItSendsNothing(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"nil sender", "nil user", "empty address", "unverified address"} {
		t.Run(mode, func(t *testing.T) {
			sender := newParkedSender()
			user := noticeRecipient()
			var delivery mail.Sender = sender
			switch mode {
			case "nil sender":
				delivery = nil
			case "nil user":
				user = nil
			case "empty address":
				user.Email = ""
			case "unverified address":
				user.EmailVerified = false
			}
			notices := NewCredentialNotices(delivery, mail.Renderer{})
			notices.Notify(context.Background(), user, credentialNotice{AppName: "app"})
			assert.True(t, notices.Wait(0, ""))
			select {
			case <-sender.entered:
				t.Fatal("ineligible recipient received a notice")
			default:
			}
		})
	}
}

type credentialNoticeSenderFunc func(context.Context, mail.Message) error

func (f credentialNoticeSenderFunc) Send(ctx context.Context, msg mail.Message) error {
	return f(ctx, msg)
}

func TestCredentialNoticesDetachAndCompleteFailedSends(t *testing.T) {
	t.Parallel()
	for _, sendErr := range []error{nil, mail.ErrEmailDisabled, errors.New("delivery failed")} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		observed := make(chan error, 1)
		notices := NewCredentialNotices(credentialNoticeSenderFunc(func(sendCtx context.Context, _ mail.Message) error {
			observed <- sendCtx.Err()
			_, hasDeadline := sendCtx.Deadline()
			assert.True(t, hasDeadline, "detached delivery requires a deadline")
			return sendErr
		}), mail.Renderer{})
		notices.Notify(ctx, noticeRecipient(), credentialNotice{AppName: "app"})
		assert.True(t, notices.Wait(30*time.Second, ""))
		assert.NoError(t, <-observed, "request cancellation must not cancel the notice")
	}
}
