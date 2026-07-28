package store

import (
	"log/slog"

	"github.com/leapmux/leapmux/internal/util/userid"
)

// BoundTabIndexKey is a TabIndexKey whose owner has cleared userid.OwnerFilter:
// both
// fields are plain strings, ready to bind into the bulk deletes'
// `WHERE (user_id, tab_id) IN ((?, ?), ...)` predicate.
//
// The fields are unexported, and FilterTabIndexKeys is the only thing that can
// populate them, so an adapter cannot reach a bindable key without passing
// through the guard. That is the same move userid.UserID makes: a rule a
// future edit would have to work at to break beats a rule it can forget.
type BoundTabIndexKey struct {
	owner string
	tabID string
}

// Owner returns the unwrapped, guaranteed-non-blank owner id to bind.
func (k BoundTabIndexKey) Owner() string { return k.owner }

// TabID returns the tab id to bind alongside Owner.
func (k BoundTabIndexKey) TabID() string { return k.tabID }

// FilterTabIndexKeysForTable is FilterTabIndexKeys plus the report every caller
// owes on a drop, so the "skip the key, but say so, naming the table" rule lives
// in one place instead of three verbatim copies (postgres owned + rendered, and
// sqlutil.BulkDeleteTabs for sqlite/mysql).
//
// The call still happens INSIDE each BulkDelete* method rather than being
// hoisted into a shared bulk-delete helper: internal/audit's store-bind rule
// resolves the guard against its lexically enclosing top-level function, so
// moving the call out would make the guard invisible to the net that requires
// it. Only the reporting is shared here, which the rule does not constrain.
func FilterTabIndexKeysForTable(table string, keys []TabIndexKey) []BoundTabIndexKey {
	bound, dropped := FilterTabIndexKeys(keys)
	if dropped > 0 {
		slog.Warn("bulk tab-index delete skipped keys with an unusable owner",
			"table", table, "dropped", dropped, "bound", len(bound))
	}
	return bound
}

// FilterTabIndexKeys is the bulk counterpart of userid.OwnerFilter: it drops
// every key whose owner is zero and returns the survivors in bindable form,
// plus a count of what it dropped. A dropped key is SKIPPED, not fatal -- one unusable key
// must not cancel the deletes queued for its valid neighbours, which is the
// difference between this and the single-row refusal (there, the one key IS the
// whole statement, so refusing it refuses everything).
//
// Skipped is NOT the same as silent, which is what dropped is for. Reaching a
// zero owner means an upstream tenancy invariant broke -- exactly the condition
// service.errBlankTenant raises as an error on the single-row journal paths --
// and a delete that quietly writes fewer rows than it was handed would hide it
// behind a nil error. Every caller logs a non-zero count with the table it was
// deleting from, so the two axes stay independent: "error vs skip" is decided
// by whether one bad key can cancel its neighbours, "silent vs reported" is
// always reported.
//
// This exists because the bulk deletes bind `WHERE (user_id, tab_id) IN
// ((?, ?), ...)` -- a per-key ownership predicate that userid.OwnerFilter alone
// cannot gate, since refusing the whole call on one bad key would silently drop
// the rest of the batch. Every bulk tab-index delete routes through here
// (postgres directly, sqlite/mysql via sqlutil.BulkDeleteTabs) so the three
// dialects cannot disagree about what a blank owner means.
//
// The unwrap is also why the return type changes shape: TabIndexKey.UserID is a
// userid.UserID, and the dialects bind strings, so this is the single place the
// two meet.
//
// The audit in internal/audit recognises this as a shared owner guard, the same
// way it recognises GetOwnedWorker: the refusal happens inside, so callers do
// not repeat it.
func FilterTabIndexKeys(keys []TabIndexKey) (bound []BoundTabIndexKey, dropped int) {
	bound = make([]BoundTabIndexKey, 0, len(keys))
	for _, k := range keys {
		owner, ok := userid.OwnerFilter(k.UserID)
		if !ok {
			dropped++
			continue
		}
		bound = append(bound, BoundTabIndexKey{owner: owner, tabID: k.TabID})
	}
	return bound, dropped
}
