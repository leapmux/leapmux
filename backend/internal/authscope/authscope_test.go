package authscope_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
)

// nonGrantable is the closed set of values no account may grant. Every
// assertion below that says "the three non-grantable values" reads it, so a
// fourth one added to scope.proto has one place to be declared.
var nonGrantable = []leapmuxv1.Scope{
	leapmuxv1.Scope_SCOPE_UNSPECIFIED,
	leapmuxv1.Scope_SCOPE_NEVER,
	leapmuxv1.Scope_SCOPE_ALL,
}

func TestZeroValueAllowsNothing(t *testing.T) {
	var zero authscope.ScopeSet
	assert.True(t, zero.IsEmpty())
	assert.False(t, zero.IsUnscoped())
	assert.Empty(t, zero.Scopes())
	assert.Equal(t, "", zero.String())
	for _, scope := range authscope.Grantable() {
		assert.Falsef(t, zero.Allows(scope), "the zero set must not allow %s", scope)
	}
	for _, scope := range nonGrantable {
		assert.Falsef(t, zero.Allows(scope), "the zero set must not allow %s", scope)
	}
}

// TestScopeTokenBijectionIsTotal walks the generated Scope_name map, so a value
// added to scope.proto with no token fails here rather than being silently
// unusable on every surface.
func TestScopeTokenBijectionIsTotal(t *testing.T) {
	seenTokens := map[string]leapmuxv1.Scope{}
	for number, name := range leapmuxv1.Scope_name {
		scope := leapmuxv1.Scope(number)
		token, ok := authscope.Token(scope)
		if !authscope.IsGrantable(scope) {
			assert.Falsef(t, ok, "non-grantable %s must have no token", name)
			continue
		}
		require.Truef(t, ok, "grantable %s has no wire token", name)
		assert.NotEmptyf(t, token, "grantable %s has an empty token", name)
		if previous, clash := seenTokens[token]; clash {
			t.Fatalf("token %q names both %s and %s", token, previous, scope)
		}
		seenTokens[token] = scope

		back, ok := authscope.ScopeFor(token)
		require.Truef(t, ok, "token %q does not resolve back to a scope", token)
		assert.Equalf(t, scope, back, "token %q round-trips to the wrong scope", token)
	}
	assert.Len(t, seenTokens, len(authscope.Grantable()))
}

// TestNonGrantableValuesAreUnsayable pins the property that makes the three
// reserved values reserved: no request, stored row or consent screen can name
// one, because Parse only produces what a token names.
func TestNonGrantableValuesAreUnsayable(t *testing.T) {
	for _, scope := range nonGrantable {
		assert.Falsef(t, authscope.IsGrantable(scope), "%s must not be grantable", scope)
		_, ok := authscope.Token(scope)
		assert.Falsef(t, ok, "%s must have no wire token", scope)

		_, err := authscope.New(scope)
		assert.Errorf(t, err, "New must refuse %s", scope)

		// The enum's own Go spelling is the closest thing to a token an
		// attacker could try, and Parse must refuse it like any other unknown.
		_, err = authscope.Parse(scope.String())
		assert.Errorf(t, err, "Parse must refuse the spelling of %s", scope)
	}
}

func TestParseRefusesAnUnknownTokenOutright(t *testing.T) {
	// The whole value is refused rather than the unknown token dropped: a set
	// that quietly lost a member would keep authenticating as a narrower app
	// and nobody would notice the vocabulary drifted.
	_, err := authscope.Parse("workspace:read not-a-scope agent:write")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not-a-scope")
}

func TestParseAcceptsEmptyAsTheEmptySet(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t\n"} {
		set, err := authscope.Parse(raw)
		require.NoErrorf(t, err, "Parse(%q)", raw)
		assert.Truef(t, set.IsEmpty(), "Parse(%q) must be the empty set", raw)
		assert.Falsef(t, set.IsUnscoped(), "Parse(%q) must not be unscoped", raw)
	}
}

func TestStringIsCanonicalSortedAndDeduped(t *testing.T) {
	set, err := authscope.Parse("git:read workspace:read git:read account:read workspace:read")
	require.NoError(t, err)
	// Enum order groups the families, and it is the one order every surface
	// uses.
	assert.Equal(t, "account:read workspace:read git:read", set.String())

	round, err := authscope.Parse(set.String())
	require.NoError(t, err)
	assert.Equal(t, set, round)
}

func TestUnscopedGrantDiffersFromEveryGrantableScope(t *testing.T) {
	unscoped := authscope.UnscopedGrant()
	every := authscope.EveryGrantableScope()

	assert.True(t, unscoped.IsUnscoped())
	assert.False(t, every.IsUnscoped())
	assert.NotEqual(t, unscoped, every, "the two widest grants must be distinct values")

	for _, scope := range authscope.Grantable() {
		assert.Truef(t, unscoped.Allows(scope), "unscoped must allow %s", scope)
		assert.Truef(t, every.Allows(scope), "every-grantable must allow %s", scope)
	}
	for _, scope := range nonGrantable {
		assert.Falsef(t, unscoped.Allows(scope), "unscoped must still refuse %s", scope)
	}
	assert.Equal(t, []leapmuxv1.Scope{leapmuxv1.Scope_SCOPE_ALL}, unscoped.Scopes())
}

// TestNarrowToCollapsesAnUnscopedGrant is the bug this whole type exists to
// make impossible: a delegation bearer inherits an unscoped user, and a
// narrowing that left it unscoped would hand a worker-spawned agent the whole
// hub.
func TestNarrowToCollapsesAnUnscopedGrant(t *testing.T) {
	ceiling := authscope.MustNew(
		leapmuxv1.Scope_SCOPE_WORKSPACE_READ,
		leapmuxv1.Scope_SCOPE_WORKER_READ,
	)
	narrowed := authscope.UnscopedGrant().NarrowTo(ceiling)

	assert.False(t, narrowed.IsUnscoped(), "narrowing an unscoped grant must not stay unscoped")
	assert.Equal(t, ceiling, narrowed)
	assert.False(t, narrowed.Allows(leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS))
}

func TestNarrowToIntersectsAndAnUnscopedCeilingLimitsNothing(t *testing.T) {
	granted := authscope.MustNew(
		leapmuxv1.Scope_SCOPE_WORKSPACE_READ,
		leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS,
	)
	ceiling := authscope.MustNew(
		leapmuxv1.Scope_SCOPE_WORKSPACE_READ,
		leapmuxv1.Scope_SCOPE_FILE_READ,
	)
	assert.Equal(t, authscope.MustNew(leapmuxv1.Scope_SCOPE_WORKSPACE_READ), granted.NarrowTo(ceiling))
	assert.Equal(t, granted, granted.NarrowTo(authscope.UnscopedGrant()))
	assert.True(t, granted.NarrowTo(authscope.ScopeSet{}).IsEmpty())
}

func TestCloseAddsWorkerReadToEveryWorkerSurfaceScope(t *testing.T) {
	workerSurface := []leapmuxv1.Scope{
		leapmuxv1.Scope_SCOPE_WORKER_ADMIN,
		leapmuxv1.Scope_SCOPE_AGENT_READ,
		leapmuxv1.Scope_SCOPE_AGENT_WRITE,
		leapmuxv1.Scope_SCOPE_TERMINAL_READ,
		leapmuxv1.Scope_SCOPE_TERMINAL_WRITE,
		leapmuxv1.Scope_SCOPE_FILE_READ,
		leapmuxv1.Scope_SCOPE_GIT_READ,
		leapmuxv1.Scope_SCOPE_GIT_WRITE,
		leapmuxv1.Scope_SCOPE_TUNNEL_OPEN,
	}
	for _, scope := range workerSurface {
		closed := authscope.MustNew(scope).Close()
		assert.Truef(t, closed.Allows(leapmuxv1.Scope_SCOPE_WORKER_READ),
			"%s must imply worker:read, because its channel cannot open without one", scope)
		assert.Truef(t, closed.Allows(scope), "%s must survive its own closure", scope)
	}
}

func TestCloseAddsTheReadHalfOfEachWritePair(t *testing.T) {
	pairs := [][2]leapmuxv1.Scope{
		{leapmuxv1.Scope_SCOPE_ACCOUNT_WRITE, leapmuxv1.Scope_SCOPE_ACCOUNT_READ},
		{leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE, leapmuxv1.Scope_SCOPE_WORKSPACE_READ},
		{leapmuxv1.Scope_SCOPE_AGENT_WRITE, leapmuxv1.Scope_SCOPE_AGENT_READ},
		{leapmuxv1.Scope_SCOPE_TERMINAL_WRITE, leapmuxv1.Scope_SCOPE_TERMINAL_READ},
		{leapmuxv1.Scope_SCOPE_GIT_WRITE, leapmuxv1.Scope_SCOPE_GIT_READ},
		{leapmuxv1.Scope_SCOPE_ADMIN_USERS, leapmuxv1.Scope_SCOPE_ADMIN_READ},
		{leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS, leapmuxv1.Scope_SCOPE_ADMIN_READ},
		{leapmuxv1.Scope_SCOPE_ADMIN_WORKERS, leapmuxv1.Scope_SCOPE_ADMIN_READ},
	}
	for _, pair := range pairs {
		closed := authscope.MustNew(pair[0]).Close()
		assert.Truef(t, closed.Allows(pair[1]), "%s must imply %s", pair[0], pair[1])
	}
}

func TestCloseIsIdempotentAndLeavesUnrelatedScopesAlone(t *testing.T) {
	set := authscope.MustNew(leapmuxv1.Scope_SCOPE_ACCOUNT_READ, leapmuxv1.Scope_SCOPE_WORKSPACE_READ)
	closed := set.Close()
	assert.Equal(t, set, closed, "no worker-surface member, so nothing to add")
	assert.Equal(t, closed, closed.Close())

	worker := authscope.MustNew(leapmuxv1.Scope_SCOPE_FILE_READ).Close()
	assert.Equal(t, worker, worker.Close())
	assert.Equal(t, authscope.UnscopedGrant(), authscope.UnscopedGrant().Close())
}

// TestCloseNeverAddsAdminOrWrite pins the direction of the implication graph:
// it may only add authority a member already had to have, never a wider one.
func TestCloseNeverAddsAdminOrWrite(t *testing.T) {
	forbidden := []leapmuxv1.Scope{
		leapmuxv1.Scope_SCOPE_ACCOUNT_WRITE,
		leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE,
		leapmuxv1.Scope_SCOPE_WORKER_ADMIN,
		leapmuxv1.Scope_SCOPE_AGENT_WRITE,
		leapmuxv1.Scope_SCOPE_TERMINAL_WRITE,
		leapmuxv1.Scope_SCOPE_GIT_WRITE,
		leapmuxv1.Scope_SCOPE_TUNNEL_OPEN,
		leapmuxv1.Scope_SCOPE_ADMIN_USERS,
		leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS,
		leapmuxv1.Scope_SCOPE_ADMIN_WORKERS,
	}
	for _, member := range authscope.Grantable() {
		closed := authscope.MustNew(member).Close()
		for _, wider := range forbidden {
			if wider == member {
				continue
			}
			assert.Falsef(t, closed.Allows(wider),
				"closing %s must not add %s", member, wider)
		}
	}
}

func TestStorableRefusesTheUnscopedGrant(t *testing.T) {
	_, err := authscope.UnscopedGrant().Storable()
	require.ErrorIs(t, err, authscope.ErrUnscopedNotStorable)

	// The empty set IS storable: it is a credential that reaches nothing,
	// which is a real (and fail-closed) state.
	stored, err := authscope.ScopeSet{}.Storable()
	require.NoError(t, err)
	assert.Equal(t, "", stored)

	stored, err = authscope.MustNew(leapmuxv1.Scope_SCOPE_FILE_READ).Storable()
	require.NoError(t, err)
	assert.Equal(t, "file:read", stored)
}

func TestContains(t *testing.T) {
	wide := authscope.MustNew(
		leapmuxv1.Scope_SCOPE_WORKSPACE_READ,
		leapmuxv1.Scope_SCOPE_FILE_READ,
		leapmuxv1.Scope_SCOPE_WORKER_READ,
	)
	narrow := authscope.MustNew(leapmuxv1.Scope_SCOPE_FILE_READ)

	assert.True(t, wide.Contains(narrow))
	assert.False(t, narrow.Contains(wide))
	assert.True(t, authscope.UnscopedGrant().Contains(wide))
	assert.False(t, wide.Contains(authscope.UnscopedGrant()),
		"a finite grant can never contain the unscoped one")
	assert.True(t, wide.Contains(authscope.ScopeSet{}))
}

func TestWithout(t *testing.T) {
	set := authscope.EveryGrantableScope().Without(
		leapmuxv1.Scope_SCOPE_ADMIN_READ,
		leapmuxv1.Scope_SCOPE_ADMIN_USERS,
		leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS,
		leapmuxv1.Scope_SCOPE_ADMIN_WORKERS,
	)
	assert.False(t, set.Allows(leapmuxv1.Scope_SCOPE_ADMIN_READ))
	assert.True(t, set.Allows(leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE))
	assert.NotContains(t, set.String(), "admin:")

	// An unscoped grant has no members to remove, and quietly turning it into
	// a finite set here would hide the narrowing NarrowTo exists to state.
	assert.Equal(t, authscope.UnscopedGrant(),
		authscope.UnscopedGrant().Without(leapmuxv1.Scope_SCOPE_ADMIN_READ))
}

func TestUnion(t *testing.T) {
	a := authscope.MustNew(leapmuxv1.Scope_SCOPE_FILE_READ)
	b := authscope.MustNew(leapmuxv1.Scope_SCOPE_GIT_READ)
	assert.Equal(t, authscope.MustNew(leapmuxv1.Scope_SCOPE_FILE_READ, leapmuxv1.Scope_SCOPE_GIT_READ), a.Union(b))
	assert.Equal(t, authscope.UnscopedGrant(), a.Union(authscope.UnscopedGrant()))
	assert.Equal(t, authscope.UnscopedGrant(), authscope.UnscopedGrant().Union(a))
}

func TestNewRefusesANonGrantableMemberAndKeepsNothing(t *testing.T) {
	set, err := authscope.New(leapmuxv1.Scope_SCOPE_FILE_READ, leapmuxv1.Scope_SCOPE_NEVER)
	require.Error(t, err)
	assert.True(t, set.IsEmpty(), "a refused New must return the zero set, not a partial one")
}

func TestMustNewPanicsOnANonGrantableMember(t *testing.T) {
	assert.Panics(t, func() { authscope.MustNew(leapmuxv1.Scope_SCOPE_ALL) })
}

// TestEveryTokenIsLowerCaseFamilyColonVerb keeps the vocabulary readable on a
// consent screen: an app asks for "file:read", never "FileRead" or "file.read".
func TestEveryTokenIsLowerCaseFamilyColonVerb(t *testing.T) {
	for _, scope := range authscope.Grantable() {
		token, ok := authscope.Token(scope)
		require.True(t, ok)
		assert.Equalf(t, strings.ToLower(token), token, "%s must be lower case", token)
		parts := strings.Split(token, ":")
		require.Lenf(t, parts, 2, "%s must be family:verb", token)
		assert.NotEmpty(t, parts[0])
		assert.NotEmpty(t, parts[1])
	}
}

// TestAdminScopesCoverEveryAdminToken pins the hub-administration family
// against the wire spelling, so a fifth `admin:` scope added to scope.proto
// fails here until AdminScopes lists it.
//
// The prefix is the authority, not the list: four surfaces refuse or default on
// this family, and a scope that carries the prefix without being listed would
// be granted by every one of them by omission.
func TestAdminScopesCoverEveryAdminToken(t *testing.T) {
	listed := map[leapmuxv1.Scope]bool{}
	for _, scope := range authscope.AdminScopes() {
		listed[scope] = true
		token, ok := authscope.Token(scope)
		require.True(t, ok)
		assert.Truef(t, strings.HasPrefix(token, "admin:"), "%s is listed as an admin scope but its token is %q", scope, token)
	}
	for _, scope := range authscope.Grantable() {
		token, _ := authscope.Token(scope)
		if strings.HasPrefix(token, "admin:") {
			assert.Truef(t, listed[scope], "%s carries the admin: prefix but AdminScopes does not list it", scope)
		}
	}
	assert.Len(t, listed, 4)
}

// TestNonAdminGrantIsEverythingExceptAdministration pins the default both mint
// surfaces take, which is the property the deleted api_tokens.admin_scope
// column defended.
func TestNonAdminGrantIsEverythingExceptAdministration(t *testing.T) {
	grant := authscope.NonAdminGrant()
	assert.False(t, grant.IsUnscoped(), "the default must be a FINITE set, so a ceiling can narrow it")
	for _, scope := range authscope.AdminScopes() {
		assert.Falsef(t, grant.Allows(scope), "the default grant must not carry %s", scope)
	}
	for _, scope := range authscope.Grantable() {
		token, _ := authscope.Token(scope)
		if strings.HasPrefix(token, "admin:") {
			continue
		}
		assert.Truef(t, grant.Allows(scope), "the default grant must carry %s", scope)
	}
}

// TestScopeTokenBijectionMatchesEnumNames pins the convention the frontend's
// scope chips rely on: AccountAppRegistrations derives each scope's token by
// splitting the generated TS enum name (SCOPE_WORKSPACE_READ ->
// workspace:read), while the Go side owns an explicit bijection. The two agree
// only while every token follows the FAMILY_ACTION shape its enum name states,
// and this test fails the suite the moment one stops -- which is the earliest
// point the TypeScript mirror can be told about it.
func TestScopeTokenBijectionMatchesEnumNames(t *testing.T) {
	t.Parallel()

	for number, name := range leapmuxv1.Scope_name {
		scope := leapmuxv1.Scope(number)
		token, ok := authscope.Token(scope)
		if !ok {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(name, "SCOPE_"), "_")
		if len(parts) < 2 {
			continue
		}
		derived := strings.ToLower(parts[0]) + ":" + strings.ToLower(strings.Join(parts[1:], "_"))
		assert.Equalf(t, derived, token,
			"%s's token does not follow its enum name's FAMILY_ACTION shape; the frontend chip derivation and the hub's stored value have diverged", name)
	}
}
