package auth

import (
	"context"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrgBinding_ZeroValueDenies(t *testing.T) {
	var zero OrgBinding
	assert.False(t, zero.isBound())
	assert.True(t, zero.DeniesAll(), "the zero value is the deny-all state")
	assert.False(t, zero.permits(&store.Workspace{OrgID: "org-a"}))
	_, ok := zero.ListFilterOrgID()
	assert.False(t, ok, "deny-all cannot be expressed as a single-org store filter")
}

// isBound and DeniesAll must not be treated as each other's negation: AnyOrg is
// false for BOTH, and reading !isBound() as "denies" is exactly what made
// AnyOrg silently deny every workspace on the CRDT path.
func TestOrgBinding_IsBoundAndDeniesAllAreNotComplements(t *testing.T) {
	any := AnyOrg()
	assert.False(t, any.isBound())
	assert.False(t, any.DeniesAll(), "AnyOrg admits everything; it is not deny-all")

	bound := BindOrg("org-a")
	assert.True(t, bound.isBound())
	assert.False(t, bound.DeniesAll())

	assert.True(t, OrgBinding{}.DeniesAll())
	assert.False(t, OrgBinding{}.isBound())
}

// BoundOrg is the type that makes "AnyOrg reached a concrete-org predicate" a
// compile error. Its constructor refuses an empty id, and its zero value still
// denies so an unchecked NewBoundOrg result cannot fail open.
func TestBoundOrg_RefusesEmptyAndZeroValueDenies(t *testing.T) {
	_, ok := NewBoundOrg("")
	assert.False(t, ok, "an empty org id cannot produce a BoundOrg")

	bound, ok := NewBoundOrg("org-a")
	require.True(t, ok)
	assert.True(t, bound.Binding().isBound())
	assert.True(t, bound.Binding().permits(&store.Workspace{OrgID: "org-a"}))
	assert.False(t, bound.Binding().permits(&store.Workspace{OrgID: "org-b"}))

	// The zero BoundOrg is what a caller holds after ignoring !ok.
	assert.True(t, BoundOrg{}.Binding().DeniesAll())
	assert.False(t, BoundOrg{}.Binding().permits(&store.Workspace{OrgID: "org-a"}),
		"a zero BoundOrg must not permit any workspace")
}

func TestOrgBinding_BindOrgEmptyEqualsZero(t *testing.T) {
	assert.Equal(t, OrgBinding{}, BindOrg(""))
	assert.False(t, BindOrg("").permits(&store.Workspace{OrgID: "org-a"}))
}

func TestOrgBinding_BindOrgPermitsOnlyThatOrg(t *testing.T) {
	b := BindOrg("org-a")
	assert.True(t, b.isBound())
	assert.True(t, b.permits(&store.Workspace{OrgID: "org-a"}))
	assert.False(t, b.permits(&store.Workspace{OrgID: "org-b"}))
	assert.False(t, b.permits(nil))
	orgID, ok := b.ListFilterOrgID()
	assert.True(t, ok)
	assert.Equal(t, "org-a", orgID)
}

func TestOrgBinding_AnyOrgPermitsAny(t *testing.T) {
	b := AnyOrg()
	assert.False(t, b.isBound(), "AnyOrg is deliberately unbound")
	assert.True(t, b.permits(&store.Workspace{OrgID: "org-a"}))
	assert.True(t, b.permits(&store.Workspace{OrgID: "org-b"}))
	assert.False(t, b.permits(nil), "nil workspace still denies")
	// ListAccessibleWorkspaces matches org_id exactly and has no "any org"
	// form, so AnyOrg is not expressible as a store filter -- callers must
	// refuse rather than pass "" and read the empty result as "owns nothing".
	_, ok := b.ListFilterOrgID()
	assert.False(t, ok, "AnyOrg cannot be expressed as a single-org store filter")
}

// DenyAllOrg is the named form of the zero value, not a fourth state.
//
// Naming it is the whole point -- a deliberate deny should be greppable rather
// than an anonymous struct literal -- so it must stay INTERCHANGEABLE with the
// zero value, or the "forgot to set it" and "meant to deny" paths would drift
// into two different behaviors.
func TestDenyAllOrgEqualsZeroValue(t *testing.T) {
	assert.Equal(t, OrgBinding{}, DenyAllOrg(),
		"the named deny must be the same value a forgotten binding produces")
	assert.True(t, DenyAllOrg().DeniesAll())
	assert.False(t, DenyAllOrg().isBound())
	_, ok := DenyAllOrg().ListFilterOrgID()
	assert.False(t, ok, "a deny-all binding is not expressible as a single-org store filter")
}

// A deny-all binding must be answered BEFORE the store is touched.
//
// A nil store makes that mechanical: if loadWorkspaceInOrg reaches for the
// workspace row first and only then applies the binding, this panics. The
// ordering is not cosmetic -- deciding after the read turns a permanent,
// error-free deny into a retryable error whenever that read fails transiently,
// which the CRDT callers cannot distinguish from a real lookup fault. It is
// also what WorkspacesReadableByUser's short-circuit comment claims.
func TestLoadWorkspaceInOrgDeniesBeforeReachingTheStore(t *testing.T) {
	ws, ok, err := loadWorkspaceInOrg(context.Background(), nil, BoundOrg{}, "ws-1")
	require.NoError(t, err, "a deny-all binding is a plain deny, never an error")
	assert.False(t, ok)
	assert.Nil(t, ws)
}
