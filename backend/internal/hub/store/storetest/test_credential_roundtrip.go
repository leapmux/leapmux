package storetest

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// testCredentialRoundTrip is the tripwire for a column a dialect adapter
// forgets.
//
// Each adapter copies the params struct into the generated struct field by
// field. An omitted field is not an error in Go: the generated field keeps its
// ZERO value, the INSERT binds it, and the write succeeds. So a column silently
// stores "" in every dialect at once, and every test that does not read that
// exact column back passes.
//
// delegation_tokens.granted_scopes did precisely this. All three Create
// adapters and all three row mappers dropped it, so every delegation bearer
// authenticated with an EMPTY grant. The scope rung is fail-closed, so the
// result was a refusal rather than a bypass -- but it made the delegation
// surface unreachable, and no test named the column.
//
// The check is REFLECTIVE rather than a list of assertions, so a column added
// later is covered without anybody remembering this file. It writes a distinct
// non-zero value into every field of the params struct, reads the row back, and
// compares each field the two structs share by name.
func (s *Suite) testCredentialRoundTrip(t *testing.T) {
	ctx := context.Background()

	t.Run("delegation token", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "roundtrip-delegation")
		worker := SeedWorker(t, st, user.ID)

		expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
		refreshExpires := expires.Add(time.Hour)
		params := store.CreateDelegationTokenParams{
			ID:               id.Generate(),
			UserID:           userid.MustNew(user.ID),
			WorkerID:         worker.ID,
			AgentID:          "agent-roundtrip",
			TerminalID:       "terminal-roundtrip",
			IssuedForTabID:   "tab-roundtrip",
			IssuedForTabType: int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
			GrantedScopes:    "workspace:read workspace:write worker:read",
			SecretHash:       []byte("secret-hash-roundtrip"),
			RefreshHash:      []byte("refresh-hash-roundtrip"),
			ExpiresAt:        expires,
			RefreshExpiresAt: &refreshExpires,
		}
		requireEveryFieldSet(t, params)
		require.NoError(t, st.DelegationTokens().Create(ctx, params))

		row, err := st.DelegationTokens().GetByID(ctx, params.ID)
		require.NoError(t, err)
		assertSharedFieldsSurvived(t, params, *row)
	})

	t.Run("device authorization", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "roundtrip-device")

		tokenID := id.Generate()
		require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
			ID: tokenID, UserID: userid.MustNew(user.ID), ClientID: oauthapp.ControlCLIClientID,
			InstallationName: "laptop", GrantedScopes: "workspace:read", SecretHash: []byte("hash"),
		}))

		params := store.CreateDeviceAuthorizationParams{
			DeviceCode:      id.Generate(),
			UserCode:        "ABC-DEF",
			DeviceName:      "roundtrip-laptop",
			ClientID:        oauthapp.ControlCLIClientID,
			RequestedScopes: "workspace:read file:read",
			IntervalSeconds: 5,
			ExpiresAt:       time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second),
			ElevateTokenID:  tokenID,
		}
		requireEveryFieldSet(t, params)
		require.NoError(t, st.DeviceAuthorizations().Create(ctx, params))

		row, err := st.DeviceAuthorizations().Get(ctx, params.DeviceCode)
		require.NoError(t, err)
		assertSharedFieldsSurvived(t, params, *row)
	})

	t.Run("api token", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "roundtrip-api")

		expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
		refreshExpires := expires.Add(time.Hour)
		params := store.CreateAPITokenParams{
			ID:               id.Generate(),
			UserID:           userid.MustNew(user.ID),
			ClientID:         oauthapp.ControlCLIClientID,
			InstallationName: "roundtrip-laptop",
			GrantedScopes:    "workspace:read worker:read",
			SecretHash:       []byte("secret-hash-roundtrip"),
			RefreshHash:      []byte("refresh-hash-roundtrip"),
			ExpiresAt:        &expires,
			RefreshExpiresAt: &refreshExpires,
		}
		requireEveryFieldSet(t, params)
		require.NoError(t, st.APITokens().Create(ctx, params))

		row, err := st.APITokens().GetByID(ctx, params.ID)
		require.NoError(t, err)
		assertSharedFieldsSurvived(t, params, *row)
	})
}

// requireEveryFieldSet fails when the caller left a params field at its zero
// value. Without it, a NEW field added to the params struct would be zero on
// both sides and assertSharedFieldsSurvived would compare "" to "" and pass --
// the exact silence this test exists to break.
func requireEveryFieldSet(t *testing.T, params any) {
	t.Helper()
	v := reflect.ValueOf(params)
	for i := range v.NumField() {
		field := v.Type().Field(i)
		if !field.IsExported() {
			continue
		}
		assert.Falsef(t, v.Field(i).IsZero(),
			"%s.%s is at its zero value; give it a distinct value so the round trip can prove it survives",
			v.Type().Name(), field.Name)
	}
}

// assertSharedFieldsSurvived compares every field the two structs share by
// name. A field the row does not carry is skipped: a write-only param (a
// secret the row exposes under another name, a flag consumed by the query) is
// legitimate, and this test is about the columns that DO come back.
func assertSharedFieldsSurvived(t *testing.T, params, row any) {
	t.Helper()
	pv := reflect.ValueOf(params)
	rv := reflect.ValueOf(row)
	compared := 0
	for i := range pv.NumField() {
		field := pv.Type().Field(i)
		if !field.IsExported() {
			continue
		}
		stored := rv.FieldByName(field.Name)
		if !stored.IsValid() {
			continue
		}
		want := pv.Field(i).Interface()
		// UserID is a userid.UserID in the params and a plain string in the
		// row, which is the one deliberate type change across this boundary.
		if id, ok := want.(userid.UserID); ok && stored.Kind() == reflect.String {
			want = id.String()
		}
		compared++
		assert.EqualValuesf(t, derefTime(want), derefTime(stored.Interface()),
			"%s did not survive the write; the dialect adapter probably drops the column",
			field.Name)
	}
	require.NotZero(t, compared, "the two structs shared no field; this proved nothing")
}

// derefTime normalizes a time to a plain UTC time.Time, on BOTH sides of the
// comparison. A column is optional in the params and required in the row (or
// the reverse), so the two structs differ by a pointer where the stored value
// is the same. Comparing the shapes would report every timestamp as a dropped
// column and bury the one real miss.
func derefTime(v any) any {
	switch t := v.(type) {
	case *time.Time:
		if t == nil {
			return nil
		}
		return t.UTC()
	case time.Time:
		return t.UTC()
	}
	return v
}
