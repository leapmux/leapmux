package crdt_test

import (
	"reflect"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hasField reports whether struct type T declares a field called name.
func hasField[T any](name string) bool {
	_, ok := reflect.TypeFor[T]().FieldByName(name)
	return ok
}

// TestCommitBatchStatesItsContextOnce pins that a CommitBatch names the
// committing tenant, epoch and principal in exactly ONE place.
//
// It used to carry a whole RecentBatchRecord, which repeats all three. The
// only production producer filled both halves from the same two expressions
// ten lines apart, so the duplication bought nothing but the chance for the
// two copies to disagree -- and the journal adapter had to mint and refuse the
// dedup owner separately to defend a divergence nothing could produce.
// DedupEntry carries only the batch-specific fields; the shared context comes
// off the envelope.
//
// RecentBatchRecord itself deliberately KEEPS those fields: it is
// LookupRecentBatchID's return shape, and a row read back from the DB has no
// enclosing commit to take them from. The controls below pin that asymmetry,
// so this test cannot pass by deleting the fields everywhere.
func TestCommitBatchStatesItsContextOnce(t *testing.T) {
	assert.False(t, hasField[crdt.CommitBatch]("DedupRow"),
		"CommitBatch must not embed a whole RecentBatchRecord; it restates UserID/Epoch/PrincipalID")
	require.True(t, hasField[crdt.CommitBatch]("Dedup"),
		"CommitBatch must still carry the batch-specific dedup fields")

	for _, name := range []string{"UserID", "Epoch", "PrincipalID"} {
		assert.False(t, hasField[crdt.DedupEntry](name),
			"DedupEntry.%s would restate the enclosing CommitBatch's own field", name)
	}
	for _, name := range []string{"BatchID", "BodyHash", "CanonicalFirstHLC", "OpCount", "ExpiresAt"} {
		assert.True(t, hasField[crdt.DedupEntry](name),
			"DedupEntry must carry the batch-specific field %s", name)
	}

	// Control: the row shape read BACK from the DB keeps its own context,
	// because there is no enclosing commit to borrow it from.
	for _, name := range []string{"UserID", "Epoch", "PrincipalID"} {
		assert.True(t, hasField[crdt.RecentBatchRecord](name),
			"RecentBatchRecord.%s is load-bearing: LookupRecentBatchID returns it standalone", name)
	}
}

// TestTabIndexRowCarriesNoOwner pins that a projected index row does not carry
// an owner column of its own.
//
// Every TabIndexWriter stamps the committing tenant instead (see
// service.txTabIndexWriter): the two are the same value by construction, and
// taking it from the transaction makes "a commit only ever writes its own
// user's index rows" structural rather than data-dependent. A UserID on the row
// was therefore written by every producer and read by no consumer -- a field
// whose only possible effect is to disagree with the one that counts.
//
// TabKey KEEPS its owner: a key names an EXISTING row for the DELETE path,
// where the owner is half the primary key and there is no projection to take
// it from. That control is what stops this test from passing on a blanket
// "delete every UserID in the package".
func TestTabIndexRowCarriesNoOwner(t *testing.T) {
	assert.False(t, hasField[crdt.TabIndexRow]("UserID"),
		"TabIndexRow.UserID is written by every producer and read by no consumer; the writer stamps the committing tenant")
	assert.True(t, hasField[crdt.TabKey]("UserID"),
		"TabKey.UserID is load-bearing: it is half the primary key the bulk deletes bind")
}

// TestSubmitInputCarriesNoTenant pins that a submit does not name the tenant it
// is addressed to.
//
// The manager it lands on IS the tenant: Registry.Get keys managers by user id
// and refuses a blank key, and the factory builds NewManager(userID, ...) from
// that same key. Every producer therefore filled UserID with the value it had
// just handed Get -- two of them literally `UserID: m.userID` -- and the only
// consumer was the gate that compared the field back against m.userID. A field
// whose sole function is to feed the check that validates it can only ever
// agree; deleting it makes "a submit landed on the wrong tenant's manager"
// unrepresentable rather than merely detected.
//
// The controls below pin the fields that stay, so this cannot pass by
// emptying the struct: Batches is the payload itself, and PrincipalID names
// WHO is writing (a delegation bearer, or the hub) -- a different axis from
// WHOSE document, and one no receiver can supply.
func TestSubmitInputCarriesNoTenant(t *testing.T) {
	assert.False(t, hasField[crdt.SubmitInput]("UserID"),
		"SubmitInput.UserID restates the registry key the manager was built from; the manager IS the tenant")

	for _, name := range []string{"Batches", "PrincipalID"} {
		assert.True(t, hasField[crdt.SubmitInput](name),
			"SubmitInput must still carry %s", name)
	}
}
