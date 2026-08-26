package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// Two surfaces mint a command-line credential, and they mint the SAME one:
// the /auth/cli/* consent legs, and AdminUserService.IssueAPIToken for a
// headless service account. This file is the one place that shape is
// decided.
//
// They were two literals before, and the drift a second copy invites had
// already happened twice: the admin path sent no issuance notice, and it
// wrote an access expiry the refresh leg silently rewrote. One builder means
// a change to the lifetime, the scope, or the row shape reaches both
// surfaces or neither.

// mintAuthority answers "what permits this mint". Exactly one of the two arms
// is present, and there are two because the hub genuinely has two answers.
//
// A mint an ACTOR performs directly -- the admin verb -- is permitted by that
// actor's own credential, on the rule assertElevatedActor states.
//
// A mint the CONSENT flow performs is not. /auth/cli/token carries no session
// at all: it authenticates by presenting the authorization code, and the
// browser consent that produced that code is where the elevated session was
// required. The grant row IS the proof, so consuming one is the authority.
//
// A struct rather than two functions, so a mint site must state which it has
// and a site with neither does not compile into anything that runs.
type mintAuthority struct {
	actor   *auth.UserInfo
	grantID string
}

// mintedByActor permits a mint that a credential performs directly.
func mintedByActor(actor *auth.UserInfo) mintAuthority {
	return mintAuthority{actor: actor}
}

// mintedByConsentGrant permits a mint that redeems a browser consent. grantID
// is the resolved grant row's id: a caller that has not loaded one has nothing
// to pass.
func mintedByConsentGrant(grantID string) mintAuthority {
	return mintAuthority{grantID: grantID}
}

func (m mintAuthority) assert(now time.Time) error {
	if m.grantID != "" {
		return nil
	}
	if m.actor == nil {
		return connect.NewError(connect.CodeInternal,
			errors.New("refusing to mint a credential that states no authority"))
	}
	return assertElevatedActor(m.actor, now)
}

// clamp limits what this authority may mint, and it is what contains the one
// arm that admits a bearer.
//
// A credential a BEARER mints inherits the minter's remaining life and does
// NOT rotate. Both halves are needed and neither works alone: without the
// inherited ceiling the child restarts the one-year lifetime from its own
// created_at, and without dropping the refresh leg the first rotation
// recomputes every window from that same fresh created_at and un-clamps it.
// Together they make each generation strictly shorter than the last, so a
// chain of self-issued credentials terminates at the browser consent that
// started it instead of running for ever.
//
// A bearer's own ceiling is readable without a store round trip:
// UserInfo.AuthenticatedAt carries the row's created_at for an api_tokens
// credential (see auth.validateRow), and auth.AbsoluteTokenLifetime measures
// from exactly that column.
//
// A SESSION actor and a consent grant clamp nothing. Both stand on a factor a
// human proved in a browser, which is the event the ceiling is measured from
// in the first place.
func (m mintAuthority) clamp(spec apiTokenMint, now time.Time) (apiTokenMint, error) {
	if m.actor == nil || m.actor.Credential.SessionID() != "" {
		return spec, nil
	}
	remaining := m.actor.AuthenticatedAt.Add(auth.AbsoluteTokenLifetime).Sub(now)
	if m.actor.AuthenticatedAt.IsZero() {
		// No creation instant to measure from. Failing closed here would
		// refuse every bearer-driven mint on a mapping slip, so the ordinary
		// access window applies and the non-rotating rule still holds.
		remaining = auth.AccessTokenTTL
	}
	if remaining <= 0 {
		return apiTokenMint{}, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this credential reached its maximum lifetime; it cannot issue another"))
	}
	spec.Rotating = false
	if spec.AccessTTL <= 0 || spec.AccessTTL > remaining {
		spec.AccessTTL = remaining
	}
	return spec, nil
}

// apiTokenMint is everything a caller chooses about one new credential.
// Everything it does NOT carry -- the hashes, the two expiries, the bearer
// strings -- is derived, which is the point.
type apiTokenMint struct {
	UserID     userid.UserID
	ClientType string
	ClientName string
	AdminScope bool
	// AccessTTL is how long the access token authenticates. Zero means
	// auth.AccessTokenTTL, the rotating default every consent leg uses.
	AccessTTL time.Duration
	// Rotating says whether the credential carries a refresh leg.
	//
	// A rotating credential holds a short access token and renews itself,
	// and its whole life is capped by auth.AbsoluteTokenLifetime. A
	// NON-rotating one holds exactly the access token it was minted with and
	// has no second secret, so its life is AccessTTL and nothing can move it.
	//
	// The two must not be combined, and that is what the field exists to
	// make unsayable. An admin-issued credential with a year of access AND a
	// refresh leg looked like a year-long service account, and the first
	// rotation rewrote its expiry to one hour (auth.AccessWindowFor clips
	// every rotation to the ordinary window), so the year the operator
	// configured was unrecoverable -- the row records the expiry, never the
	// TTL it was minted from.
	Rotating bool
}

// mintedAPIToken is one credential's secrets plus the row that stores them.
type mintedAPIToken struct {
	TokenID string
	Pair    auth.MintedBearerPair
	Params  store.CreateAPITokenParams
}

// mintAPIToken derives one credential from a spec. It writes nothing: the
// caller owns the store handle, because the consent legs create inside the
// transaction that consumes their grant and the admin verb does not.
//
// It REFUSES a mint that states no authority, and that is the point of taking
// one. Every surface gated this as the first line of its own handler, and
// nothing made it so: the classification tripwire in
// user_procedures_internal_test.go cannot reach an Admin* procedure, and the
// consent legs are mux routes rather than Connect procedures. So the omission
// the gate exists to prevent was possible at exactly the place it mattered,
// and it had already happened once. Here a new mint site cannot compile
// without naming its authority, and cannot name one that is absent.
func mintAPIToken(v *auth.TokenValidator, by mintAuthority, now time.Time, spec apiTokenMint) (mintedAPIToken, error) {
	if err := by.assert(now); err != nil {
		return mintedAPIToken{}, err
	}
	spec, err := by.clamp(spec, now)
	if err != nil {
		return mintedAPIToken{}, err
	}
	tokenID := id.Generate()
	accessTTL := spec.AccessTTL
	if accessTTL <= 0 {
		accessTTL = auth.AccessTokenTTL
	}
	refreshTTL := time.Duration(0)
	if spec.Rotating {
		// A fresh row's created_at is now, so the absolute lifetime is never
		// the binding term at the mint -- RefreshWindowFor is called anyway
		// so the mint and the rotation read the SAME rule rather than two
		// copies of it.
		refreshTTL = auth.RefreshWindowFor(now, now)
	}
	pair := v.MintBearerPair(auth.BearerKindAPI, tokenID, now, accessTTL, refreshTTL)
	params := store.CreateAPITokenParams{
		ID:         tokenID,
		UserID:     spec.UserID,
		ClientType: spec.ClientType,
		ClientName: spec.ClientName,
		SecretHash: pair.AccessHash,
		ExpiresAt:  &pair.AccessExpiresAt,
		AdminScope: spec.AdminScope,
	}
	if spec.Rotating {
		params.RefreshHash = pair.RefreshHash
		params.RefreshExpiresAt = &pair.RefreshExpiresAt
	}
	return mintedAPIToken{TokenID: tokenID, Pair: pair, Params: params}, nil
}

// RefreshBearer returns the refresh secret to hand back, or the empty string
// for a credential that does not rotate.
func (m mintedAPIToken) RefreshBearer() string {
	if m.Params.RefreshHash == nil {
		return ""
	}
	return m.Pair.RefreshBearer
}

// RefreshExpiresIn returns the refresh window to report, or zero for a
// credential that does not rotate.
func (m mintedAPIToken) RefreshExpiresIn(now time.Time) int {
	if m.Params.RefreshHash == nil {
		return 0
	}
	return remainingExpiresIn(m.Pair.RefreshExpiresAt, now)
}

// notifyCredentialIssued emails the account owner that a command-line
// credential was minted. It is how somebody learns that a credential they did
// not create exists -- the one signal that does not depend on them opening
// Preferences.
//
// BOTH mint surfaces send it. The admin verb sent nothing, which left the one
// surface a stolen administrator cookie reaches as the one surface that
// minted a credential in silence.
//
// Best-effort with a logged warning, NEVER an error. The token is already
// committed by the time this runs, so failing here would leave the hub
// holding a live credential the caller never received: the worst of both.
//
// It runs DETACHED, on its own goroutine and its own deadline, and this is
// the only mail send in the hub that does. Every other one IS the request the
// caller made -- a password-reset link, a verification code -- so the caller
// waits for it and reads its error. This one is incidental to a token the
// caller is blocked on, and an SMTP exchange is capped at sendTimeout, so
// leaving it on the response path put up to that long in front of a login a
// human waits for. context.WithoutCancel for the same reason handleRefresh
// uses it: the notice must not die because the client hung up.
//
// Silent when the address is unverified: an unverified address is not known
// to belong to the account, so sending an account notice to it is a delivery
// to a stranger. Silent when the hub has no relay, because ErrEmailDisabled
// is a configuration state and not a failure -- logging it would print a
// warning on every CLI login of every hub without SMTP.
//
// Refresh sends nothing. A rotation is the same credential continuing, and a
// notice per rotation would train the recipient to ignore the one that
// matters.
func notifyCredentialIssued(
	ctx context.Context,
	sender mail.Sender,
	renderer mail.Renderer,
	user *store.User,
	deviceName string,
	adminScope bool,
	// byAdministrator says the OWNER did not perform this. The two mint
	// surfaces differ on exactly that, and the message has to as well: a
	// consent the recipient gave and an administrator acting for them cannot
	// share the sentence "if this was you, nothing more is needed".
	byAdministrator bool,
) {
	if sender == nil || user == nil || user.Email == "" || !user.EmailVerified {
		return
	}
	msg := renderer.CLICredentialIssuedEmail(user.Email, deviceName, adminScope, byAdministrator)
	userID := user.ID
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), CredentialNoticeTimeout)
	go func() {
		defer cancel()
		if err := sender.Send(sendCtx, msg); err != nil {
			if errors.Is(err, mail.ErrEmailDisabled) {
				return
			}
			slog.WarnContext(sendCtx, "could not send the CLI credential notice", "user_id", userID, "err", err)
		}
	}()
}
