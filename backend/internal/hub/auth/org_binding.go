package auth

import "github.com/leapmux/leapmux/internal/hub/store"

// orgBindingKind discriminates the three -- and only three -- organization
// policies a workspace lookup can carry. Making it one field rather than an
// (orgID string, unbound bool) pair is the point: that pair had four
// combinations for three legal states, so `{orgID: "x", unbound: true}` was
// representable and every predicate had to re-derive the real state from two
// fields. This mirrors DelegationWorkerScope's scopeKind in the same package,
// which exists for exactly this reason.
type orgBindingKind uint8

const (
	// orgDenyAll is the ZERO VALUE, so a caller that forgets to set a binding
	// denies everything rather than silently skipping the org check.
	orgDenyAll orgBindingKind = iota
	orgBound
	orgAny
)

// OrgBinding names whether a workspace lookup is bound to one organization.
// The zero value denies everything, so a caller that forgets to set it fails
// closed; AnyOrg() is the only way to skip the binding, and it is greppable.
//
// Modelled on DelegationWorkerScope: BindOrg("") collapses to the zero value
// (deny-all), so the empty-string sentinel cannot silently mean "skip".
type OrgBinding struct {
	kind  orgBindingKind
	orgID string
}

// BindOrg binds lookups to orgID. BindOrg("") equals the zero value and
// denies every workspace -- there is no "empty means skip" shortcut.
func BindOrg(orgID string) OrgBinding {
	if orgID == "" {
		return OrgBinding{}
	}
	return OrgBinding{kind: orgBound, orgID: orgID}
}

// AnyOrg skips the organization binding. It is the only constructor that
// permits workspaces from any org; use it deliberately (e.g. delegation
// callers whose pinned workspace may live outside the user's home org).
func AnyOrg() OrgBinding {
	return OrgBinding{kind: orgAny}
}

// DenyAllOrg admits no workspace at all.
//
// It equals the zero value -- a caller that forgets to set a binding still
// denies -- but naming it keeps a DELIBERATE deny legible and greppable
// (`rg DenyAllOrg` finds every one), instead of leaving it as an anonymous
// `auth.OrgBinding{}` literal a reader has to decode from the kind iota's
// ordering. Mirrors DenyAllScope in this package, for the same reason.
func DenyAllOrg() OrgBinding {
	return OrgBinding{kind: orgDenyAll}
}

// BoundOrg is an OrgBinding proven to name a concrete organization.
//
// It exists so the CRDT auth path -- which must ALWAYS bind authorization to
// the manager's concrete org -- cannot be handed AnyOrg(). Passing AnyOrg to a
// predicate that gates on "is a concrete org named" denied every workspace,
// silently and indistinguishably from "workspace not found", while AnyOrg's own
// doc promises the opposite. That is a trap in two directions: the caller sees
// an inexplicable total deny, and the obvious "fix" -- widening the gate to
// permits() -- would admit the cross-org CRDT reads the check exists to stop.
// Requiring this type makes the mistake a compile error instead.
// It WRAPS an OrgBinding rather than re-deriving one from a raw org id, so
// "the zero BoundOrg widens to deny-all" is structural: the zero BoundOrg holds
// the zero OrgBinding, which IS the deny-all value. Storing the id and rebuilding
// the binding in Binding() made that property depend on one line continuing to
// call BindOrg -- rewrite it as OrgBinding{kind: orgBound, orgID: b.orgID} and
// the zero BoundOrg becomes the illegal (kind: bound, orgID: "") state the kind
// discriminant exists to rule out, permitting every blank-org workspace.
type BoundOrg struct{ binding OrgBinding }

// NewBoundOrg returns the binding for a concrete organization. ok is false for
// an empty id, which the caller MUST treat as a deny -- the same fail-closed
// answer the empty-orgID prologue used to give, now stated at the call site.
//
// The empty-id rule is BindOrg's, not a second copy of it: an id BindOrg
// refuses is exactly an id that cannot name a concrete org.
func NewBoundOrg(orgID string) (BoundOrg, bool) {
	binding := BindOrg(orgID)
	if binding.DeniesAll() {
		return BoundOrg{}, false
	}
	return BoundOrg{binding: binding}, true
}

// Binding widens a BoundOrg to the general OrgBinding the shared helpers take.
// The zero BoundOrg -- what a caller holds after ignoring NewBoundOrg's ok --
// yields the zero OrgBinding, which denies everything.
func (b BoundOrg) Binding() OrgBinding { return b.binding }

// isBound reports whether this binding names a concrete organization -- true
// only for BindOrg with a non-empty id. Both the deny-all zero value and
// AnyOrg report false, for opposite reasons, so isBound alone must NEVER be
// used to decide whether to allow: see DeniesAll / permits.
func (b OrgBinding) isBound() bool { return b.kind == orgBound }

// DeniesAll reports whether this binding admits no workspace at all -- the
// zero value or BindOrg(""). It exists so a caller can tell "the binding was
// never set" apart from "the caller deliberately wants any org", which isBound
// deliberately cannot: both report false there.
func (b OrgBinding) DeniesAll() bool { return b.kind == orgDenyAll }

// permits reports whether ws satisfies this binding's ORG rule only. A nil
// workspace is never permitted, so a store path that returned (nil, nil) or a
// batch entry that failed to load fails closed here rather than panicking.
// It applies no other policy: soft-delete is filtered by the loaders (the
// store's GetByID / ListByIDs already drop deleted rows), and ownership is
// IsOwner's job -- every caller layers that on top.
func (b OrgBinding) permits(ws *store.Workspace) bool {
	if ws == nil {
		return false
	}
	switch b.kind {
	case orgAny:
		return true
	case orgBound:
		return ws.OrgID == b.orgID
	default: // orgDenyAll
		return false
	}
}

// ListFilterOrgID returns the org id a store list query should filter on, and
// whether this binding can be expressed as such a filter at all. It is NOT the
// authorization policy -- use permits for that.
//
// ok is false for AnyOrg and for the deny-all zero value, and a caller MUST NOT
// substitute "" on !ok. ListAccessibleWorkspaces filters `org_id = ?` as an
// EXACT match; there is no "any org" form of that query, so an empty id returns
// no rows and would read as "you own nothing" rather than "this binding cannot
// be answered here". Handing the store a synthetic no-match sentinel instead is
// no better: a NUL-bearing parameter is rejected outright by Postgres, turning
// a deny into a transport error. Refuse at the caller and keep the store
// filter honest.
func (b OrgBinding) ListFilterOrgID() (string, bool) {
	if !b.isBound() {
		return "", false
	}
	return b.orgID, true
}
