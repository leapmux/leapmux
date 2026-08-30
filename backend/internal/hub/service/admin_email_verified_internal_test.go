package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// The email_verified rule of an admin UpdateUser, exercised directly.
//
// Through the RPC each case costs a user creation and two store round
// trips, so the tests only ever covered the rule at the happy paths somebody
// remembered to write. It is a pure function of the row and the request, so
// the whole table fits here and the RPC tests keep proving that the fenced
// verb runs.
func TestResolveEmailVerified(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		user      store.User
		msg       *leapmuxv1.UpdateUserRequest
		wantValue bool
		wantWrite bool
	}{
		"a new address lowers a raised flag": {
			user:      store.User{Email: "old@example.com", EmailVerified: true},
			msg:       &leapmuxv1.UpdateUserRequest{Email: proto.String("new@example.com")},
			wantValue: false,
			wantWrite: true,
		},
		"a new address on an already-unverified account writes nothing": {
			user:      store.User{Email: "old@example.com", EmailVerified: false},
			msg:       &leapmuxv1.UpdateUserRequest{Email: proto.String("new@example.com")},
			wantValue: false,
			wantWrite: false,
		},
		"a first address lowers a raised flag": {
			user:      store.User{Email: "", EmailVerified: true},
			msg:       &leapmuxv1.UpdateUserRequest{Email: proto.String("first@example.com")},
			wantValue: false,
			wantWrite: true,
		},
		"an explicit verification in the same request wins": {
			user:      store.User{Email: "old@example.com", EmailVerified: true},
			msg:       &leapmuxv1.UpdateUserRequest{Email: proto.String("new@example.com"), EmailVerified: proto.Bool(true)},
			wantValue: true,
			wantWrite: true,
		},
		// No administrator exception, and this is the recovery-route fix. A
		// verified address is a valid self-service password-reset target, so
		// carrying the flag onto an address nobody confirmed handed the
		// highest-privilege accounts a live reset route to whatever address
		// the request carried. The exemption that keeps an administrator
		// signed in lives at the login gate; see
		// auth.EmailVerificationFacts.Satisfied.
		"an administrator loses the flag across an address change too": {
			user:      store.User{Email: "old@example.com", EmailVerified: true, IsAdmin: true},
			msg:       &leapmuxv1.UpdateUserRequest{Email: proto.String("new@example.com")},
			wantValue: false,
			wantWrite: true,
		},
		"clearing the address keeps the flag": {
			user:      store.User{Email: "old@example.com", EmailVerified: true},
			msg:       &leapmuxv1.UpdateUserRequest{Email: proto.String("")},
			wantValue: true,
			wantWrite: false,
		},
		"a case-only rewrite is the same address": {
			user:      store.User{Email: "same@example.com", EmailVerified: true},
			msg:       &leapmuxv1.UpdateUserRequest{Email: proto.String("SAME@Example.com")},
			wantValue: true,
			wantWrite: false,
		},
		"an explicit lowering with no address change still writes": {
			user:      store.User{Email: "same@example.com", EmailVerified: true},
			msg:       &leapmuxv1.UpdateUserRequest{EmailVerified: proto.Bool(false)},
			wantValue: false,
			wantWrite: true,
		},
		"a request that touches neither writes nothing": {
			user:      store.User{Email: "same@example.com", EmailVerified: true},
			msg:       &leapmuxv1.UpdateUserRequest{DisplayName: proto.String("New Name")},
			wantValue: true,
			wantWrite: false,
		},
		// The write test is "the value changes, or the request stated it",
		// not "the value drops". A rule that ever RAISES the flag without an
		// explicit request must still reach the verb.
		"an explicit raise on an unverified account writes": {
			user:      store.User{Email: "same@example.com", EmailVerified: false},
			msg:       &leapmuxv1.UpdateUserRequest{EmailVerified: proto.Bool(true)},
			wantValue: true,
			wantWrite: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			user := tc.user
			value, write := resolveEmailVerified(&user, tc.msg)
			assert.Equal(t, tc.wantValue, value, "value")
			assert.Equal(t, tc.wantWrite, write, "write")
		})
	}
}

// TestEmailVerificationFactsSatisfied_SeparatesTheAddressFromThePrivilege is the
// whole shape of the change, stated once.
//
// email_verified answers "did anybody confirm this address". The
// administrator exemption answers "may this account use the hub". They were
// one column, and writing the second into the first made an administrator's
// unconfirmed address a valid self-service password-reset target -- because
// RequestAccountRecovery reads the column and CANNOT take the exemption: the
// question it asks is exactly the first one.
func TestEmailVerificationFactsSatisfied_SeparatesTheAddressFromThePrivilege(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		required, isAdmin, emailVerified, want bool
	}{
		"nothing is required":                {false, false, false, true},
		"a confirmed address passes":         {true, false, true, true},
		"an unconfirmed address does not":    {true, false, false, false},
		"an administrator passes regardless": {true, true, false, true},
		"and still passes when confirmed":    {true, true, true, true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			facts := auth.EmailVerificationFacts{IsAdmin: tc.isAdmin, EmailVerified: tc.emailVerified}
			assert.Equal(t, tc.want, facts.Satisfied(tc.required))

			// The two constructors must read the same two fields. They are
			// what removes the three adjacent booleans a call site could
			// transpose in silence, so a constructor that read the wrong
			// column would put the defect back behind a nicer name.
			assert.Equal(t, facts, auth.EmailVerificationFactsFromUser(&store.User{
				IsAdmin: tc.isAdmin, EmailVerified: tc.emailVerified,
			}))
			assert.Equal(t, facts, (&auth.UserInfo{
				IsAdmin: tc.isAdmin, EmailVerified: tc.emailVerified,
			}).EmailVerificationFacts())
		})
	}
}

// A missing account satisfies nothing. Both constructors are nil-safe, and
// both fail CLOSED: the zero value holds neither the exemption nor the
// confirmation, so a caller that lost its row is refused rather than admitted.
func TestEmailVerificationFacts_NilFailsClosed(t *testing.T) {
	t.Parallel()

	var user *store.User
	var info *auth.UserInfo
	assert.False(t, auth.EmailVerificationFactsFromUser(user).Satisfied(true))
	assert.False(t, info.EmailVerificationFacts().Satisfied(true))
	// And a hub that requires nothing still admits, because the requirement
	// is the hub's fact and not the account's.
	assert.True(t, auth.EmailVerificationFactsFromUser(user).Satisfied(false))
}
