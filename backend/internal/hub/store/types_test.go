package store

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// UpdateUserProfileParams.Validate is the store-level guard that keeps a username
// (mirrored into the personal-org name and the /o/{slug} URL space) a routable slug.
// It must reject anything the service layer's validate.SanitizeSlug would, not merely
// an empty-after-lowercase value: a whitespace-only or non-slug username passes a bare
// NormalizeUsername yet corrupts orgs.name.
func TestUpdateUserProfileParams_Validate(t *testing.T) {
	// Mixed case is accepted: the store lowercases it (NormalizeUsername), so the
	// stored value is a clean slug.
	valid := []string{"alice", "bob-1", "a", "user-name-2", "Alice"}
	for _, name := range valid {
		require.NoError(t, UpdateUserProfileParams{ID: "u1", Username: name}.Validate(),
			"a well-formed slug %q must validate", name)
	}

	invalid := map[string]string{
		"empty":               "",
		"whitespace only":     "   ",
		"leading/trailing ws": "  alice  ",
		"space inside":        "bad name",
		"punctuation":         "Bad Name!",
		"leading hyphen":      "-alice",
		"consecutive hyphen":  "a--b",
	}
	for label, name := range invalid {
		err := UpdateUserProfileParams{ID: "u1", Username: name}.Validate()
		require.ErrorIs(t, err, ErrInvalidArgument, "%s (%q) must be rejected as an invalid slug", label, name)
	}

	// The mirror check that motivates the fix: NormalizeUsername alone (lowercase only)
	// would let these through, so a non-empty check is not enough.
	assert.NotEqual(t, "", NormalizeUsername("   "), "sanity: whitespace does not normalize to empty")
	assert.NotEqual(t, "", NormalizeUsername("Bad Name!"), "sanity: a non-slug does not normalize to empty")
}

// TestCreateUserParams_Validate mirrors the UpdateUserProfileParams.Validate
// coverage on the CREATE path: a user's username is created in the same
// transaction as its personal org and mirrored into orgs.name, so the store must
// refuse a non-slug username on create the same way it does on rename, closing the
// asymmetry where only the rename path guarded the /o/{slug} mirror.
func TestCreateUserParams_Validate(t *testing.T) {
	// Mixed case is accepted (the store lowercases it); the rest are routable slugs.
	valid := []string{"alice", "bob-1", "a", "user-name-2", "MixedCase", "solo"}
	for _, name := range valid {
		require.NoError(t, CreateUserParams{ID: "u1", OrgID: "o1", Username: name}.Validate(),
			"a well-formed slug %q must validate on create", name)
	}

	invalid := map[string]string{
		"empty":               "",
		"whitespace only":     "   ",
		"leading/trailing ws": "  alice  ",
		"space inside":        "bad name",
		"punctuation":         "Bad Name!",
		"leading hyphen":      "-alice",
		"consecutive hyphen":  "a--b",
	}
	for label, name := range invalid {
		err := CreateUserParams{ID: "u1", OrgID: "o1", Username: name}.Validate()
		require.ErrorIs(t, err, ErrInvalidArgument, "%s (%q) must be rejected as an invalid slug on create", label, name)
	}
}

// TestSearchLikePattern pins the one site that builds the admin-search LIKE
// pattern: it Unicode-lowercases a term so it matches the pre-folded
// display_name_folded column, backslash-escapes the LIKE metacharacters so an
// operator's literal '%'/'_' cannot act as a wildcard (paired with the queries'
// ESCAPE '\'), appends the prefix-match '%', and preserves nil (which
// SearchUsers reads as "no filter -> all rows").
func TestSearchLikePattern(t *testing.T) {
	assert.Nil(t, SearchLikePattern(nil), "a nil query stays nil (no filter), not an empty-string match")

	pattern := func(s string) string {
		p := SearchLikePattern(&s)
		require.NotNil(t, p)
		return *p
	}
	assert.Equal(t, "%", pattern(""), "an empty query becomes the match-all prefix pattern")
	assert.Equal(t, "ölaf%", pattern("ÖLaf"), "a non-ASCII mixed-case term folds to lowercase")
	// The direct folder agrees, so the write path and the query fold identically.
	assert.Equal(t, "ölaf", FoldSearchText("ÖLaf"))

	// LIKE metacharacters in the operator's term are escaped, so they match
	// literally instead of widening the search.
	assert.Equal(t, `\%%`, pattern("%"), "a literal %-search cannot dump every user")
	assert.Equal(t, `a\_b%`, pattern("a_b"), "a literal _ (legal in email local parts) is not a one-char wildcard")
	assert.Equal(t, `a\\b%`, pattern(`a\b`), "a backslash is escaped before the metachars it could mask")
}

// TestGetOwnedWorker_EmptyUserIDDenied pins the empty-identity fail-close on the
// shared cross-dialect owner helper: an empty caller UserID must be refused up
// front rather than matching a blank-registrant row, keeping the store-side rule
// symmetric with auth.WorkerCanUse / auth.IsOwner. The getByID stub returns a
// worker whose RegisteredBy is also empty, so without the guard an empty UserID
// would fail OPEN (the != comparison would be false and the worker returned).
func TestGetOwnedWorker_EmptyUserIDDenied(t *testing.T) {
	blankRegistrant := func(_ context.Context, id string) (*Worker, error) {
		return &Worker{ID: id, RegisteredBy: ""}, nil
	}
	_, err := GetOwnedWorker(context.Background(), GetOwnedWorkerParams{WorkerID: "w1", UserID: ""}, blankRegistrant)
	require.ErrorIs(t, err, ErrNotFound, "an empty caller UserID must be denied, not matched to a blank registrant")

	// The registrant path still works for a real, matching id.
	ownedByAlice := func(_ context.Context, id string) (*Worker, error) {
		return &Worker{ID: id, RegisteredBy: "alice"}, nil
	}
	w, err := GetOwnedWorker(context.Background(), GetOwnedWorkerParams{WorkerID: "w1", UserID: "alice"}, ownedByAlice)
	require.NoError(t, err)
	assert.Equal(t, "w1", w.ID)

	// A non-registrant is still ErrNotFound (probe protection).
	_, err = GetOwnedWorker(context.Background(), GetOwnedWorkerParams{WorkerID: "w1", UserID: "mallory"}, ownedByAlice)
	require.ErrorIs(t, err, ErrNotFound, "a non-registrant must be denied")
}

// TestClampListLimit pins the store-boundary limit normalization that keeps the
// Postgres/MySQL int32 LIMIT cast from silently wrapping a caller's int64 limit.
func TestClampListLimit(t *testing.T) {
	assert.Equal(t, int64(50), ClampListLimit(50), "an ordinary limit passes through")
	assert.Equal(t, int64(0), ClampListLimit(0), "zero is preserved (paginated queries treat it as no rows)")
	assert.Equal(t, int64(0), ClampListLimit(-1), "a negative limit floors at 0 rather than wrapping negative")
	// The ceiling is MaxInt32-1, not MaxInt32: FetchLimit adds a probe row, and
	// the +1 must still fit the dialects' int32 LIMIT casts -- so HasMore stays
	// exact even at the largest permitted limit instead of silently degrading.
	assert.Equal(t, int64(math.MaxInt32-1), ClampListLimit(math.MaxInt32), "the int32 max caps at the probe-safe ceiling")
	assert.Equal(t, int64(math.MaxInt32-1), ClampListLimit(math.MaxInt32+1), "a value past int32 caps at the ceiling, not wraps")
	// The two concrete wrap cases the fix targets: 4294967297 would truncate to 1
	// (a silent under-fetch) and 3000000000 to a negative int32 (a DB error).
	assert.Equal(t, int64(math.MaxInt32-1), ClampListLimit(4294967297), "2^32+1 caps instead of truncating to 1")
	assert.Equal(t, int64(math.MaxInt32-1), ClampListLimit(3000000000), "3e9 caps instead of wrapping negative on int32")
	// The clamped value +1 (the probe row) is always a safe int32 conversion.
	assert.LessOrEqual(t, ClampListLimit(math.MaxInt64)+1, int64(math.MaxInt32))
	assert.GreaterOrEqual(t, ClampListLimit(math.MinInt64), int64(0))
}
