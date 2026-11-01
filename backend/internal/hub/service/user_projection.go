package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// completeUserProto builds the COMPLETE account projection: the stored row,
// the passkey count, the linked OAuth providers, and the step-up option rule.
// Every User the hub returns is this projection, so a client that adopts a
// User from any response reads the same facts GetCurrentUser reports.
//
// It returns an error only when the account elevates only through a provider
// and its link read failed; every other failure degrades, as stated below.
func completeUserProto(ctx context.Context, st store.Store, user *store.User) (*leapmuxv1.User, error) {
	// ONE count, for the number this REPORTS and for the step-up option rule
	// below. Best-effort: this runs at every page load, so a transient count
	// failure must not fail a request whose real work is elsewhere.
	//
	// The two readers take the failure differently, and they must. The
	// reported number degrades to zero, which is a stale count on a screen.
	// The RULE takes no answer at all: a zero that a failed read produced is
	// not a fact about the account, and feeding it to the predicate below
	// reports a passkey-only account as "verify through your provider", which
	// providerMayElevateAccount then re-reads from the store and refuses. The
	// client would have hidden the option that works and offered the one that
	// cannot.
	passkeyCount, counted := countPasskeysBestEffort(ctx, st, user)
	// The step-up option rule, from the ONE predicate that decides it. It
	// reads the two facts already in hand rather than paying a second COUNT.
	//
	// An unread count fails CLOSED, so the screen offers no option rather
	// than only a refused one. The user still reaches every factor the
	// account really holds through the ordinary prompt, and the next page
	// load answers the question properly.
	elevatesOnlyThroughAProvider := counted && accountShapeElevatesOnlyThroughAProvider(user.FirstCredentialExempt, passkeyCount)

	uid, ok := userid.New(user.ID)
	if !ok {
		return nil, errors.New("user row has a blank id")
	}
	// The link read REPORTS its failure only for the account shape that has
	// nothing else to fall back on, and degrades for every other.
	//
	// An empty list is indistinguishable from "this account has no linked
	// provider". For an account that holds neither a password nor a passkey
	// the provider IS its only step-up option, so the empty list becomes
	// "this account has nothing to verify with yet" on the screen the
	// command-line consent leg just bounced the user to -- a false and
	// alarming answer to a transient read.
	//
	// Every other account keeps a factor it can present, so the same failure
	// costs it a missing Linked Accounts section until the next read. It
	// must NOT cost it the whole session: GetCurrentUser runs on every page
	// load, and a non-Unauthenticated failure leaves the client on a
	// bootstrap error with no way into the app -- for a query most accounts
	// never have a row for. An account whose passkey count did not read is in
	// that second group by construction, because the shape above is false for
	// it -- so one failed COUNT cannot turn a read into a fatal one.
	linkedProviders, err := linkedProvidersFor(ctx, st, uid)
	if err != nil {
		if elevatesOnlyThroughAProvider {
			return nil, err
		}
		slog.WarnContext(ctx, "could not list the account's linked OAuth providers",
			"user_id", user.ID, "err", err)
	}

	userProto := userToProto(user, passkeyCount)
	userProto.OauthProviders = linkedProviders
	userProto.MayElevateThroughAProvider = elevatesOnlyThroughAProvider
	return userProto, nil
}

// signInReport builds the SessionReport a SUCCESSFUL sign-in returns. The
// sign-in's main work -- the account, the session, the cookie -- already
// committed, so a failed projection must not fail the RPC: it logs the
// failure and leaves user absent, and the client reads GetCurrentUser.
// elevation is nil for a fresh sign-in, which grants no elevation.
func signInReport(
	ctx context.Context, st store.Store, user *store.User,
	verification *leapmuxv1.EmailVerificationStatus, elevation *timestamppb.Timestamp,
) *leapmuxv1.SessionReport {
	report := &leapmuxv1.SessionReport{EmailVerification: verification, ElevationExpiresAt: elevation}
	userProto, err := completeUserProto(ctx, st, user)
	if err != nil {
		slog.WarnContext(ctx, "the sign-in succeeded, but its account projection could not be read",
			"user_id", user.ID, "err", err)
		return report
	}
	report.User = userProto
	return report
}

// storedUserProto re-reads the account after a committed mutation and returns
// its complete projection. The mutation already committed, so a failed read
// or projection must not fail the RPC: it logs the failure and answers nil,
// the response omits user, and the client reads GetCurrentUser.
func storedUserProto(ctx context.Context, st store.Store, userID string) *leapmuxv1.User {
	user, err := st.Users().GetByID(ctx, userID)
	if err != nil {
		slog.WarnContext(ctx, "the account changed, but it could not be read back", "user_id", userID, "err", err)
		return nil
	}
	userProto, err := completeUserProto(ctx, st, user)
	if err != nil {
		slog.WarnContext(ctx, "the account changed, but its projection could not be read", "user_id", userID, "err", err)
		return nil
	}
	return userProto
}

// linkedProvidersFor lists the account's OAuth links, each with whether an
// administrator currently has that provider enabled.
//
// It REPORTS a failure rather than discarding it, and that is the difference
// from the passkey count beside it. The count's zero costs a wasted round
// trip; an empty provider list is indistinguishable from "this account has no
// linked provider", and for an account that holds neither a password nor a
// passkey that is the ONLY step-up option -- so a transient store failure
// told the user their account had nothing to verify with at all, on the
// screen the command-line consent leg just bounced them to. An error the
// client retries is the honest answer.
//
// A disabled provider's link is INCLUDED. See LinkedOAuthProvider.enabled: the
// owner must still be able to detach a link whose provider an administrator
// turned off, and the verification screen filters on the flag instead.
func linkedProvidersFor(ctx context.Context, st store.Store, userID userid.UserID) ([]*leapmuxv1.LinkedOAuthProvider, error) {
	links, err := st.OAuthUserLinks().ListByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list the account's OAuth links: %w", err)
	}
	if len(links) == 0 {
		return nil, nil
	}
	// ListAll is acceptable here: the number of configured OAuth providers is
	// typically in the single digits, and adding a GetByIDs method to every
	// backend is not worth the complexity.
	providers, err := st.OAuthProviders().ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the configured OAuth providers: %w", err)
	}
	rows := make(map[string]store.OAuthProviderSummary, len(providers))
	for _, p := range providers {
		rows[p.ID] = p
	}
	out := make([]*leapmuxv1.LinkedOAuthProvider, 0, len(links))
	for _, link := range links {
		row, ok := rows[link.ProviderID]
		if !ok {
			// An ABSENT row is different from a disabled one: the provider
			// was removed, so there is no name to render and nothing the
			// link can ever reach again.
			continue
		}
		out = append(out, &leapmuxv1.LinkedOAuthProvider{
			Id:      link.ProviderID,
			Name:    row.Name,
			Enabled: row.Enabled,
		})
	}
	return out, nil
}

// userToProto is the building block of completeUserProto: the stored row and
// a passkey count, with no provider list and no step-up rule.
func userToProto(u *store.User, passkeyCount int64) *leapmuxv1.User {
	return &leapmuxv1.User{
		Id:            u.ID,
		Username:      u.Username,
		DisplayName:   u.DisplayName,
		IsAdmin:       u.IsAdmin,
		Email:         u.Email,
		EmailVerified: u.EmailVerified,
		PendingEmail:  u.PendingEmail,
		// The HASH, not the users.password_set column. The column is a claim
		// the creating flow makes, and the solo bootstrap sets it true with an
		// empty hash -- so the column reports a password on an account that
		// cannot sign in with one. The client renders "Change Password" and
		// "Password changed." from this field, which is exactly the sentence a
		// solo owner must not read while setting their first one.
		//
		// The ADMISSION rules keep reading the column, and that is deliberate:
		// the claim is what routes solo past the first-credential rule that no
		// solo account could ever satisfy. See assertFirstCredentialAuthIsFresh.
		PasswordSet:  u.HasUsablePassword(),
		PasskeyCount: int32(passkeyCount),
	}
}

// countPasskeysBestEffort returns the account's passkey count, and whether
// the query answered at all. A failure logs a warning and reports (0, false).
//
// The second return value is what keeps the two readers in completeUserProto
// honest. A count of zero means one of two things, and only one of them is a
// fact about the account: it holds no passkey, or nobody could ask. A display
// tolerates the first reading of both; an authorization-shaped rule must not,
// so it reads this flag and fails closed.
func countPasskeysBestEffort(ctx context.Context, st store.Store, u *store.User) (int64, bool) {
	count, err := st.PasskeyCredentials().CountByUser(ctx, u.ID)
	if err != nil {
		slog.WarnContext(ctx, "count passkeys for user proto", "user_id", u.ID, "err", err)
		return 0, false
	}
	return count, true
}
