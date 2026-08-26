package cmd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/captcha"
	"github.com/leapmux/leapmux/internal/hub/ratelimit"
	"github.com/leapmux/leapmux/internal/hub/settings"
)

// TestAdminCommandsRefuseWorkerIPC pins the transport rule every admin
// leaf inherits through requireAdminClient: admin commands talk to the
// hub directly and never ride the worker-IPC bridge. The hubrpc table
// the bridge dispatches through is a typing device, not a security
// boundary — anything registered there is callable by any spawned agent
// — so no admin procedure is registered there and the client refuses the
// transport outright.
func TestAdminCommandsRefuseWorkerIPC(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_SOCK", "unix:/tmp/agent.sock")

	_, err := requireAdminClient("")
	require.Error(t, err)
	assert.True(t, control.IsEmitted(err), "the refusal uses the JSON envelope, like every control error")
}

// TestAdminCommandUsageStrings pins the usage/validation messages that
// carried over verbatim from the old offline verbs, checked before any
// client is built (so no hub or credentials are needed).
func TestAdminCommandUsageStrings(t *testing.T) {
	cases := []struct {
		name string
		run  func(any, []string) error
		want string
	}{
		{"settings get needs KEY", RunAdminSettingsGet, "usage: leapmux control admin settings get KEY"},
		{"settings set needs KEY VALUE", RunAdminSettingsSet, "usage: leapmux control admin settings set KEY VALUE"},
		{"settings set-secret needs KEY JSON", RunAdminSettingsSetSecret, "usage: leapmux control admin settings set-secret KEY JSON"},
		{"settings reset needs KEY", RunAdminSettingsReset, "usage: leapmux control admin settings reset KEY"},
		{"api-token issue needs a user selector", RunAdminAPITokenIssue, "--user-id or --username is required"},
		{"api-token revoke needs id", RunAdminAPITokenRevoke, "--id is required"},
		{"delegation-token revoke needs id", RunAdminDelegationTokenRevoke, "--id is required"},
		{"rate-limit set needs operation", RunAdminRateLimitSet, "--operation is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(nil, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestAdminUserUpdateNoFields pins the no-op refusal verbatim.
func TestAdminUserUpdateNoFields(t *testing.T) {
	err := RunAdminUserUpdate(nil, nil)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "no fields to update (use --display-name, --email, --email-verified, or --clear-pending-email)"))
}

// TestParseSettingValue pins the scalar coercion the old offline verb
// accepted: JSON documents pass through, bare scalars are quoted.
func TestParseSettingValue(t *testing.T) {
	v, err := parseSettingValue("3600")
	require.NoError(t, err)
	assert.JSONEq(t, "3600", string(v))

	v, err = parseSettingValue("true")
	require.NoError(t, err)
	assert.JSONEq(t, "true", string(v))

	v, err = parseSettingValue(`{"port":465}`)
	require.NoError(t, err)
	assert.JSONEq(t, `{"port":465}`, string(v))

	v, err = parseSettingValue("dark")
	require.NoError(t, err)
	assert.JSONEq(t, `"dark"`, string(v))

	// An empty VALUE is refused, but the empty STRING is a legal value for
	// some keys, so the message must name both ways to reach it.
	_, err = parseSettingValue("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `pass '""' to store the empty string`)
	assert.Contains(t, err.Error(), "settings reset KEY")

	v, err = parseSettingValue("[1,2]")
	require.NoError(t, err)
	assert.JSONEq(t, "[1,2]", string(v))

	v, err = parseSettingValue(`"dark"`)
	require.NoError(t, err)
	assert.JSONEq(t, `"dark"`, string(v), "an already-quoted JSON string must not be wrapped again")

	// A value that OPENS a document is a document. Treating a malformed one
	// as a scalar string was worse than useless: for an object-valued key
	// the hub answered with a type error that never mentioned the missing
	// brace, and for a string-valued key it accepted the malformed text and
	// STORED it. An operator who really means a literal brace quotes it.
	_, err = parseSettingValue("{not-json")
	require.Error(t, err, "an object-looking value that is not valid JSON must be refused here, not sent")
	assert.Contains(t, err.Error(), "not valid JSON")

	_, err = parseSettingValue(`[1,2`)
	require.Error(t, err, "an array-looking value that is not valid JSON must be refused too")

	v, err = parseSettingValue(`"{literal-brace}"`)
	require.NoError(t, err, "quoting is how an operator asks for a literal brace")
	assert.JSONEq(t, `"{literal-brace}"`, string(v))
}

func TestAdminRateLimitSet_RequiresAtLeastOneTuningFlag(t *testing.T) {
	err := RunAdminRateLimitSet(nil, []string{"--operation", "elevation"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pass --max-attempts, --window, or both")
}

// Tuning one field must not disturb the other, and must not touch the
// on/off switch: `rate-limit enable|disable` owns that, so adjusting a
// window cannot silently re-arm a limiter an operator turned off.
func TestAdminRateLimitSet_AcceptsOneFlagAndNeverWritesEnabled(t *testing.T) {
	for _, args := range [][]string{
		{"--operation", "elevation", "--max-attempts", "5"},
		{"--operation", "elevation", "--window", "900"},
	} {
		err := RunAdminRateLimitSet(nil, args)
		require.Error(t, err, "no hub is reachable in a unit test")
		assert.NotContains(t, err.Error(), "pass --max-attempts, --window, or both",
			"%v must pass flag validation and fail only at the transport", args)
	}
}

func TestAdminRateLimitSet_RefusesUnknownOperation(t *testing.T) {
	err := RunAdminRateLimitSet(nil, []string{"--operation", "chnage-password", "--max-attempts", "5"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown operation "chnage-password"`)
	assert.Contains(t, err.Error(), "elevation", "the message must list the known operations")
}

// The hub decodes the value as JSON, so a bare `T`/`TRUE`/`False` must be
// normalized to the JSON literal. Passing the token through made the hub
// answer with a decode error naming a line the operator never typed.
func TestParseSettingValue_NormalizesEveryBooleanSpelling(t *testing.T) {
	for _, raw := range []string{"true", "TRUE", "True", "t", "T"} {
		v, err := parseSettingValue(raw)
		require.NoError(t, err)
		assert.JSONEq(t, `true`, string(v), "%q must ship the JSON literal", raw)
	}
	for _, raw := range []string{"false", "FALSE", "False", "f", "F"} {
		v, err := parseSettingValue(raw)
		require.NoError(t, err)
		assert.JSONEq(t, `false`, string(v), "%q must ship the JSON literal", raw)
	}
}

// ParseBool also accepts `1` and `0`. The numeric test runs first, so a
// bare digit stays a number — testing bool first turned
// `settings set <int_key> 1` into `true`.
func TestParseSettingValue_DigitsStayNumbers(t *testing.T) {
	for _, raw := range []string{"0", "1", "42", "-7", "0.5"} {
		v, err := parseSettingValue(raw)
		require.NoError(t, err)
		assert.JSONEq(t, raw, string(v), "%q must ship as a number", raw)
	}

	// strconv.ParseFloat accepts far more than JSON does. Each of these
	// parses as a float and is NOT valid JSON, so shipping it verbatim made
	// the hub answer with a decode error naming a character the operator
	// typed for a different reason. They belong on the string path.
	for _, raw := range []string{"NaN", "Infinity", "-Inf", "infinity", "0x1p-2", "1_000"} {
		v, err := parseSettingValue(raw)
		require.NoErrorf(t, err, "%q", raw)
		quoted, mErr := json.Marshal(raw)
		require.NoError(t, mErr)
		assert.JSONEqf(t, string(quoted), string(v), "%q is not JSON, so it must ship quoted", raw)
	}
}

// Every optional timestamp in an admin row goes through putTime. A row
// that printed a nil stamp would show 1970-01-01, which reads as a real
// event: an API token "last used" before it existed, a worker "last seen"
// at the epoch.
func TestPutTime_OmitsAnAbsentTimestampAndFormatsAPresentOneInUTC(t *testing.T) {
	row := map[string]any{}
	putTime(row, "deleted_at", nil)
	assert.NotContains(t, row, "deleted_at", "an absent stamp leaves no field at all")

	// A non-UTC zone must render as UTC, so two rows from two hubs compare.
	zone := time.FixedZone("UTC+9", 9*60*60)
	putTime(row, "created_at", timestamppb.New(time.Date(2026, 8, 18, 12, 0, 0, 0, zone)))
	assert.Equal(t, "2026-08-18T03:00:00.000Z", row["created_at"])
}

func TestAdminUserJSON_OmitsTheTimestampsThatAreAbsent(t *testing.T) {
	created := timestamppb.New(time.Date(2026, 8, 18, 3, 0, 0, 0, time.UTC))
	row := adminUserJSON(&leapmuxv1.AdminUser{
		Id: "u1", Username: "amy", CreatedAt: created,
	})

	assert.Equal(t, "u1", row["id"])
	assert.Equal(t, "2026-08-18T03:00:00.000Z", row["created_at"])
	// A live user has no deletion, and an untouched one no update.
	assert.NotContains(t, row, "deleted_at")
	assert.NotContains(t, row, "updated_at")
	// A false boolean is still a field: absent and false are different
	// answers to "is this user an admin".
	assert.Equal(t, false, row["is_admin"])
	assert.Equal(t, false, row["password_set"])
}

func TestAdminWorkerJSON_OmitsAnUnseenWorkersLastSeen(t *testing.T) {
	created := timestamppb.New(time.Date(2026, 8, 18, 3, 0, 0, 0, time.UTC))
	row := adminWorkerJSON(&leapmuxv1.AdminWorker{Id: "w1", CreatedAt: created})
	assert.NotContains(t, row, "last_seen_at", "a worker that never connected has no last-seen time")

	seen := timestamppb.New(time.Date(2026, 8, 18, 4, 30, 0, 0, time.UTC))
	row = adminWorkerJSON(&leapmuxv1.AdminWorker{Id: "w1", CreatedAt: created, LastSeenAt: seen})
	assert.Equal(t, "2026-08-18T04:30:00.000Z", row["last_seen_at"])
}

// The CLI's JSON envelope is a public contract: an operator's script reads
// these keys by name. A rename or a dropped key is a silent break — the
// script sees an absent field, not an error. These two mappers had no test,
// so nothing pinned their key set. Every stamp goes through putTime, so a
// hub that omits one leaves the field out rather than printing the epoch.
func TestAdminOAuthProviderJSON_CarriesEveryKeyAScriptReads(t *testing.T) {
	row := adminOAuthProviderJSON(&leapmuxv1.AdminOAuthProvider{
		Id:           "p1",
		ProviderType: "oidc",
		Name:         "Corp SSO",
		IssuerUrl:    "https://sso.example.com",
		ClientId:     "client-1",
		Scopes:       "openid email",
		TrustEmail:   true,
		Enabled:      false,
		CreatedAt:    timestamppb.New(time.Date(2026, 8, 18, 3, 0, 0, 0, time.UTC)),
	})

	assert.Equal(t, map[string]any{
		"id":          "p1",
		"type":        "oidc",
		"name":        "Corp SSO",
		"issuer_url":  "https://sso.example.com",
		"client_id":   "client-1",
		"scopes":      "openid email",
		"trust_email": true,
		"enabled":     false,
		"created_at":  "2026-08-18T03:00:00.000Z",
	}, row)
	// The client SECRET never leaves the hub, and no key here can carry it.
	for k := range row {
		assert.NotContains(t, k, "secret", "a provider row must never expose the client secret")
	}
	// A false flag is still a field: absent and false are different answers
	// to "is this provider enabled".
	assert.Contains(t, row, "enabled")
}

func TestAdminSessionJSON_CarriesEveryKeyAScriptReads(t *testing.T) {
	at := func(h int) *timestamppb.Timestamp {
		return timestamppb.New(time.Date(2026, 8, 18, h, 0, 0, 0, time.UTC))
	}
	row := adminSessionJSON(&leapmuxv1.AdminSession{
		Id: "s1", UserId: "u1", Username: "alice", UserDeleted: true,
		CreatedAt: at(3), LastActiveAt: at(4), ExpiresAt: at(5),
		IpAddress: "203.0.113.7", UserAgent: "curl/8",
	})

	assert.Equal(t, map[string]any{
		"id":             "s1",
		"user_id":        "u1",
		"username":       "alice",
		"user_deleted":   true,
		"created_at":     "2026-08-18T03:00:00.000Z",
		"last_active_at": "2026-08-18T04:00:00.000Z",
		"expires_at":     "2026-08-18T05:00:00.000Z",
		"ip_address":     "203.0.113.7",
		"user_agent":     "curl/8",
	}, row)
}

func TestValidateListLimit_RefusesNonPositiveAndOversized(t *testing.T) {
	// A non-positive limit reads as "return no rows" in the store, which an
	// operator cannot tell apart from a genuinely empty result.
	for _, limit := range []int64{0, -1, -50, 501} {
		require.Error(t, validateListLimit(limit), "limit %d must be refused", limit)
	}
	for _, limit := range []int64{1, 50, 500} {
		require.NoError(t, validateListLimit(limit), "limit %d must be accepted", limit)
	}
}

// Every paginated admin verb must hold the limit, not just `user list`.
func TestAdminListVerbs_RefuseANonPositiveLimit(t *testing.T) {
	verbs := map[string]func(any, []string) error{
		"user list":             RunAdminUserList,
		"user list-sessions":    RunAdminUserListSessions,
		"session list":          RunAdminSessionList,
		"api-token list":        RunAdminAPITokenList,
		"delegation-token list": RunAdminDelegationTokenList,
		"worker list":           RunAdminWorkerList,
		"worker reg-key list":   RunAdminWorkerRegKeyList,
	}
	for name, run := range verbs {
		err := run(nil, []string{"--limit", "0"})
		require.Error(t, err, "%s must refuse --limit 0", name)
		assert.Contains(t, err.Error(), "limit must be between 1 and 500", "%s", name)
	}
}

// `--secret` with no `--provider` used to fall through to the ALTCHA arm
// and overwrite the signing key, invalidating every live challenge.
func TestAdminCaptchaSet_RefusesAProviderForeignFlag(t *testing.T) {
	err := RunAdminCaptchaSet(nil, []string{"--provider", "altcha", "--site-key", "abc"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--site-key applies only to")
	assert.Contains(t, err.Error(), "the target provider is altcha")

	err = RunAdminCaptchaSet(nil, []string{"--provider", "turnstile", "--cost", "5"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--cost applies only to altcha")
}

func TestAdminCaptchaSet_RefusesAnEmptySecretAndAnEmptyInvocation(t *testing.T) {
	err := RunAdminCaptchaSet(nil, []string{"--provider", "turnstile", "--secret", ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--secret must not be empty")

	err = RunAdminCaptchaSet(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nothing to set")
}

func TestAdminCaptchaSet_RefusesAnUnknownProvider(t *testing.T) {
	err := RunAdminCaptchaSet(nil, []string{"--provider", "hcaptcha"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_request")
}

// TestSettingShapeFromDescriptor_ScalarKindMatchesTheGoSpelling pins the
// CLI's kind names to settings.FieldKind.String().
//
// The CLI used to restate the wire-to-schema table, and the two had already
// drifted: `settings get` printed "boolean" where the Go schema — and the
// golden account schema that pins it — say "bool". An operator reading one
// surface must be able to use what it says on the other.
func TestSettingShapeFromDescriptor_ScalarKindMatchesTheGoSpelling(t *testing.T) {
	scalar := func(k leapmuxv1.SettingFieldKind) string {
		return settingShapeFromDescriptor(&leapmuxv1.SettingDescriptor{
			Fields: []*leapmuxv1.SettingField{{Name: "", Kind: k}},
		})
	}
	for protoKind, goKind := range map[leapmuxv1.SettingFieldKind]settings.FieldKind{
		leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_BOOL:        settings.FieldBool,
		leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_INT:         settings.FieldInt,
		leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_FLOAT:       settings.FieldFloat,
		leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_STRING:      settings.FieldString,
		leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_ENUM:        settings.FieldEnum,
		leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_STRING_LIST: settings.FieldStringList,
		leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_BYTES:       settings.FieldBytes,
		leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_CUSTOM:      settings.FieldCustom,
	} {
		assert.Equalf(t, goKind.String(), scalar(protoKind),
			"the CLI and the Go schema must spell %v the same way", protoKind)
	}
	assert.Equal(t, "bool", scalar(leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_BOOL),
		"the golden account schema pins this spelling")
	assert.Equal(t, "unknown", scalar(leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_UNSPECIFIED),
		"a kind this build does not know must not print as a kind that it does")
}

// TestSettingShapeFromDescriptor_KeepsTheSecretFieldsOutOfThePublicHalf
// pins the shape hint an operator reads before writing a document.
//
// `settings set KEY` merges the PUBLIC half and `settings set-secret KEY`
// the secret one. Listing a secret field among the public names sent an
// operator to the verb that refuses it: the shape for smtp advertised
// "password" in the brace list AND again in the secret list.
func TestSettingShapeFromDescriptor_KeepsTheSecretFieldsOutOfThePublicHalf(t *testing.T) {
	shape := settingShapeFromDescriptor(&leapmuxv1.SettingDescriptor{
		Key: "smtp",
		Fields: []*leapmuxv1.SettingField{
			{Name: "host", Kind: leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_STRING},
			{Name: "port", Kind: leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_INT},
			{Name: "password", Kind: leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_STRING, Secret: true},
		},
	})

	assert.Equal(t, `{"host", "port"} + secret {"password"}`, shape)
	public, _, found := strings.Cut(shape, " + secret ")
	require.True(t, found, "the shape states both halves")
	assert.NotContains(t, public, "password", "a secret field must not appear in the public brace list")

	// A key whose every field is secret has no public half to print, and
	// must not answer with an empty brace pair.
	assert.Equal(t, `secret {"token"}`, settingShapeFromDescriptor(&leapmuxv1.SettingDescriptor{
		Key:    "webhook",
		Fields: []*leapmuxv1.SettingField{{Name: "token", Kind: leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_STRING, Secret: true}},
	}))
}

// The captcha and rate-limit keys carry a cross-key rule that one
// `settings set` cannot satisfy, so their description points at the verb
// that writes both halves together. Every other key is described verbatim.
func TestSettingDescriptionText_PointsAtTheVerbThatCanWriteTheKey(t *testing.T) {
	assert.Equal(t, "the active captcha provider alias (prefer `leapmux control admin captcha ...`)",
		settingDescriptionText(captcha.CaptchaSelectedKey.Name(), "the active captcha provider alias"))
	assert.Equal(t, "rate limit for elevation (prefer `leapmux control admin rate-limit ...`)",
		settingDescriptionText(ratelimit.SettingKeyPrefix+"elevation", "rate limit for elevation"))
	assert.Equal(t, "the public origin", settingDescriptionText("public_url", "the public origin"),
		"a key with no domain verb keeps the hub's own summary")
}

// TestTokenRowsCarryTheOwnerDeletedFlag pins a field the two inline row
// loops both dropped.
//
// `--user-id` on the token listings deliberately resolves through
// GetByIDIncludeDeleted so an operator can enumerate a soft-deleted
// account's still-live tokens and revoke them. The hub populates
// owner_deleted for exactly that, and neither loop printed it — so the
// listing that exists to find a deleted owner's tokens never said the
// owner was deleted. The session row already carried the equivalent flag.
func TestTokenRowsCarryTheOwnerDeletedFlag(t *testing.T) {
	created := timestamppb.New(time.Unix(1_700_000_000, 0))

	api := adminAPITokenJSON(&leapmuxv1.AdminAPIToken{
		Id: "t-1", UserId: "u-1", Username: "alice", OwnerDeleted: true,
		ClientType: "cli", ClientName: "laptop", CreatedAt: created,
	})
	assert.Equal(t, true, api["owner_deleted"])
	assert.Equal(t, "cli", api["client_type"])
	assert.NotContains(t, api, "revoked_at", "an absent stamp leaves no field")

	del := adminDelegationTokenJSON(&leapmuxv1.AdminDelegationToken{
		Id: "d-1", UserId: "u-1", Username: "alice", OwnerDeleted: false,
		WorkerId: "w-1", AgentId: "a-1", CreatedAt: created,
		RevokedAt: created,
	})
	assert.Equal(t, false, del["owner_deleted"])
	assert.Equal(t, "w-1", del["worker_id"])
	assert.Contains(t, del, "revoked_at", "a present stamp renders")

	// The two kinds agree on every shared field name, so an operator's
	// filter or script reads one shape.
	for _, name := range []string{"id", "user_id", "username", "owner_deleted", "created_at"} {
		assert.Contains(t, api, name)
		assert.Contains(t, del, name)
	}
}

// TestCaptchaSetRefusesAnAlgorithmBeforeTheDial pins that a bad
// --algorithm answers with the algorithm, not with a connection error.
// The check has to sit in BeforeDial, because the hub is unreachable in a
// unit test and an operator's typo must not need one either.
func TestCaptchaSetRefusesAnAlgorithmBeforeTheDial(t *testing.T) {
	err := RunAdminCaptchaSet(nil, []string{"--algorithm", "SHA-999"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported captcha algorithm")
	assert.Contains(t, err.Error(), "SCRYPT", "the refusal lists what IS supported")

	// A supported one passes the local check and fails only at the
	// transport.
	err = RunAdminCaptchaSet(nil, []string{"--algorithm", "SCRYPT"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "unsupported captcha algorithm")
}

// TestUserSelectorRefusalsHappenBeforeTheDial pins the gap the shared
// selector closes.
//
// Seven verbs address exactly ONE user, and the hub holds the same rule —
// but it can only answer over a connection the operator did not need. A
// missing or ambiguous selector must name the flag, not the transport.
func TestUserSelectorRefusalsHappenBeforeTheDial(t *testing.T) {
	cases := []struct {
		name   string
		run    func(any, []string) error
		idFlag string
		// rest carries the other REQUIRED flags of the verb, so the last
		// case reaches the transport instead of a second local refusal.
		// `reset-password` prompts for a password it was not given, and a
		// non-terminal stdin turns that prompt into its own refusal.
		rest []string
	}{
		{"user get", RunAdminUserGet, "id", nil},
		{"user delete", RunAdminUserDelete, "id", nil},
		{"user grant-admin", func(c any, a []string) error { return RunAdminUserSetAdmin(c, a, true) }, "id", nil},
		{"user revoke-admin", func(c any, a []string) error { return RunAdminUserSetAdmin(c, a, false) }, "id", nil},
		{"user reset-password", RunAdminUserResetPassword, "id", []string{"--password", "s3cret12"}},
		{"user list-sessions", RunAdminUserListSessions, "id", nil},
		{"session revoke-user", RunAdminSessionRevokeUser, "user-id", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// No `rest` on the two refusal cases, on purpose: the selector
			// must answer first even when a later required flag is also
			// missing.
			err := tc.run(nil, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--"+tc.idFlag+" or --username is required")

			err = tc.run(nil, []string{"--" + tc.idFlag, "usr_1", "--username", "amy"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--"+tc.idFlag+" and --username are mutually exclusive")

			// One selector alone passes the local check and fails only at
			// the transport.
			err = tc.run(nil, append([]string{"--username", "amy"}, tc.rest...))
			require.Error(t, err, "no hub is reachable in a unit test")
			assert.NotContains(t, err.Error(), "is required")
			assert.NotContains(t, err.Error(), "mutually exclusive")
		})
	}
}

// A missing selector must answer BEFORE the password prompt, for the same
// reason `user create` checks --username first: an operator must not type a
// secret into a request that cannot succeed, and with a non-terminal stdin
// the prompt's own refusal names --password, which is not the missing flag.
func TestAdminUserResetPassword_RefusesAMissingSelectorBeforeItPrompts(t *testing.T) {
	err := RunAdminUserResetPassword(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id or --username is required")
	assert.NotContains(t, err.Error(), "stdin is not a terminal",
		"the selector check must run before the prompt")

	// A supplied password reaches the same refusal, so the order does not
	// depend on whether a prompt would have happened.
	err = RunAdminUserResetPassword(nil, []string{"--password", "s3cret12"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id or --username is required")

	// With a selector but no password and no terminal, the prompt refusal
	// takes over -- so the verb really does prompt rather than send a blank
	// password to the hub.
	err = RunAdminUserResetPassword(nil, []string{"--username", "amy"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--password is required (stdin is not a terminal)")
}

// TestAdminUserUpdateReportsTheMoreSpecificRefusalFirst pins the ordering:
// an operator who typed no flags at all is better served by "nothing to
// update" than by a missing-selector notice.
func TestAdminUserUpdateReportsTheMoreSpecificRefusalFirst(t *testing.T) {
	err := RunAdminUserUpdate(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no fields to update")

	// With a field but no selector, the selector refusal takes over.
	err = RunAdminUserUpdate(nil, []string{"--display-name", "Amy"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id or --username is required")
}

// The token LISTINGS are FILTERS, not selectors: an empty pair means
// "every user". They must never inherit the selector refusal.
func TestTokenListingsAcceptAnEmptySelector(t *testing.T) {
	for name, run := range map[string]func(any, []string) error{
		"api-token list":        RunAdminAPITokenList,
		"delegation-token list": RunAdminDelegationTokenList,
	} {
		err := run(nil, nil)
		require.Errorf(t, err, "%s: no hub is reachable in a unit test", name)
		assert.NotContainsf(t, err.Error(), "is required",
			"%s lists every user when no filter is given", name)
	}
}

// A missing --username must answer BEFORE the password prompt. The prompt
// ran first, so an operator typed a secret into a request that could not
// succeed -- and with a non-terminal stdin the refusal named --password,
// which is not the flag that was missing.
func TestAdminUserCreate_RefusesAMissingUsernameBeforeItPrompts(t *testing.T) {
	err := RunAdminUserCreate(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--username is required")
	assert.NotContains(t, err.Error(), "stdin is not a terminal",
		"the username check must run before the prompt")

	// A supplied password reaches the same refusal, so the order does not
	// depend on whether a prompt would have happened.
	err = RunAdminUserCreate(nil, []string{"--password", "s3cret"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--username is required")

	// And a username alone gets past the check, so the guard did not
	// swallow the working invocation.
	err = RunAdminUserCreate(nil, []string{"--username", "amy", "--password", "s3cret"})
	require.Error(t, err, "no hub is reachable in a unit test")
	assert.NotContains(t, err.Error(), "--username is required")
}

// `settings set-secret KEY VALUE` merges NAMED fields, so only a JSON
// DOCUMENT can be a legal partial. A validity test alone let every other
// JSON value dial the hub and come back as a decode error naming a Go type.
func TestAdminSettingsSetSecret_RefusesAnyValueThatIsNotADocument(t *testing.T) {
	for _, value := range []string{`"hunter2"`, "5", "null", "[1,2]", "true", `"{}"`} {
		err := RunAdminSettingsSetSecret(nil, []string{"smtp", value})
		require.Errorf(t, err, "%s must be refused", value)
		assert.Containsf(t, err.Error(), "VALUE must be a JSON document naming the secret fields", "%s", value)
		assert.Containsf(t, err.Error(), `{"password":"..."}`, "the refusal shows the shape: %s", value)
	}

	// A document passes the local check and fails only at the transport,
	// and surrounding whitespace does not change that.
	for _, value := range []string{`{"password":"x"}`, "  {\"password\":\"x\"}  "} {
		err := RunAdminSettingsSetSecret(nil, []string{"smtp", value})
		require.Errorf(t, err, "no hub is reachable in a unit test: %s", value)
		assert.NotContainsf(t, err.Error(), "VALUE must be a JSON document", "%s", value)
	}

	// A malformed document is still refused here, not sent.
	err := RunAdminSettingsSetSecret(nil, []string{"smtp", `{"password":`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VALUE must be a JSON document")
}

// An admin verb with no hub address must identify the flag and the
// variable. Falling through built a client for the empty URL, whose
// credential lookup answered `hub url missing hostname` under the
// not_logged_in code -- a message that names neither.
func TestRequireAdminClient_RefusesAnEmptyHubAddress(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_SOCK", "")
	t.Setenv("LEAPMUX_HUB", "")

	_, err := requireAdminClient("")
	require.Error(t, err)
	assert.True(t, control.IsEmitted(err))
	assert.Contains(t, err.Error(), "invalid_request")
	assert.Contains(t, err.Error(), "--hub")
	assert.Contains(t, err.Error(), "LEAPMUX_HUB")
	assert.Contains(t, err.Error(), "auth login", "the message names the verb that stores a credential")
	assert.NotContains(t, err.Error(), "missing hostname")

	// The worker-IPC refusal still wins, because that operator has an
	// address and a different problem.
	t.Setenv("LEAPMUX_CONTROL_SOCK", workerIPCSock)
	_, err = requireAdminClient("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LEAPMUX_CONTROL_SOCK")
}

// adminRowMapper is one admin row mapper under test: the proto message it
// renders, the call that renders it, and the JSON key each proto field
// reaches the operator under.
type adminRowMapper struct {
	name   string
	msg    proto.Message
	render func() map[string]any
	// keys maps every proto field name to its JSON key. The two differ
	// wherever the wire name would read badly in a row an operator greps
	// (owner_username -> username), so the table states the mapping instead
	// of assuming it.
	keys map[string]string
}

// TestAdminRowMappers_CarryEveryFieldOfTheirProtoMessage is the tripwire
// for a whole defect class this review found twice.
//
// The CLI's JSON envelope is a public contract: an operator's script reads
// these keys by name, so a dropped field is a SILENT break -- the script
// sees an absent key, not an error. `owner_deleted` reached the worker row
// on the wire and no loop printed it, and `creator_deleted` did the same on
// the registration-key row. Both mappers looked complete.
//
// The table below fails when the proto grows a field that no mapper prints,
// so a new field cannot ship half-wired. It also fails when a mapper prints
// the WRONG source under a declared key, which is the second half of the
// same defect: an operator reading `created_by` cannot tell a user id from
// a username, so a mapper that reads the sibling getter is as silent as one
// that reads nothing.
func TestAdminRowMappers_CarryEveryFieldOfTheirProtoMessage(t *testing.T) {
	user := &leapmuxv1.AdminUser{}
	session := &leapmuxv1.AdminSession{}
	apiToken := &leapmuxv1.AdminAPIToken{}
	delegation := &leapmuxv1.AdminDelegationToken{}
	worker := &leapmuxv1.AdminWorker{}
	regKey := &leapmuxv1.AdminRegistrationKey{}
	provider := &leapmuxv1.AdminOAuthProvider{}

	identity := func(names ...string) map[string]string {
		out := make(map[string]string, len(names))
		for _, name := range names {
			out[name] = name
		}
		return out
	}
	merge := func(base map[string]string, extra map[string]string) map[string]string {
		for k, v := range extra {
			base[k] = v
		}
		return base
	}

	tokenFields := func() map[string]string {
		return identity("id", "user_id", "username", "owner_deleted",
			"created_at", "last_used_at", "expires_at", "revoked_at")
	}

	mappers := []adminRowMapper{
		{
			name: "adminUserJSON", msg: user,
			render: func() map[string]any { return adminUserJSON(user) },
			keys: identity("id", "username", "display_name", "email", "email_verified",
				"pending_email", "password_set", "is_admin", "created_at", "updated_at"),
		},
		{
			name: "adminSessionJSON", msg: session,
			render: func() map[string]any { return adminSessionJSON(session) },
			keys: identity("id", "user_id", "username", "user_deleted", "created_at",
				"last_active_at", "expires_at", "ip_address", "user_agent"),
		},
		{
			name: "adminAPITokenJSON", msg: apiToken,
			render: func() map[string]any { return adminAPITokenJSON(apiToken) },
			keys:   merge(tokenFields(), identity("client_type", "client_name", "admin_scope")),
		},
		{
			name: "adminDelegationTokenJSON", msg: delegation,
			render: func() map[string]any { return adminDelegationTokenJSON(delegation) },
			keys:   merge(tokenFields(), identity("worker_id", "agent_id")),
		},
		{
			name: "adminWorkerJSON", msg: worker,
			render: func() map[string]any { return adminWorkerJSON(worker) },
			keys: merge(identity("id", "registered_by", "owner_deleted", "status",
				"auto_registered", "created_at", "last_seen_at"),
				map[string]string{"owner_username": "username"}),
		},
		{
			name: "adminRegistrationKeyJSON", msg: regKey,
			render: func() map[string]any { return adminRegistrationKeyJSON(regKey) },
			keys: merge(identity("id", "created_by", "creator_deleted", "created_at", "expires_at"),
				map[string]string{"creator_username": "username"}),
		},
		{
			name: "adminOAuthProviderJSON", msg: provider,
			render: func() map[string]any { return adminOAuthProviderJSON(provider) },
			keys: merge(identity("id", "name", "issuer_url", "client_id", "scopes",
				"trust_email", "enabled", "created_at"),
				map[string]string{"provider_type": "type"}),
		},
	}

	for _, mapper := range mappers {
		t.Run(mapper.name, func(t *testing.T) {
			populateEveryField(t, mapper.msg)
			row := mapper.render()

			want := map[string]bool{}
			fields := mapper.msg.ProtoReflect().Descriptor().Fields()
			for i := range fields.Len() {
				f := fields.Get(i)
				name := string(f.Name())
				key, declared := mapper.keys[name]
				require.Truef(t, declared,
					"proto field %q has no JSON key in this table; add it here AND to %s",
					name, mapper.name)
				want[key] = true
				// assert, not require: one run must report EVERY field the
				// mapper drops, not just the first one.
				if !assert.Containsf(t, row, key,
					"proto field %q reaches no operator: %s prints no %q", name, mapper.name, key) {
					continue
				}
				assert.Equalf(t, expectedPrintedValue(t, mapper.msg, f), row[key],
					"%s prints the wrong source under %q; that key must carry proto field %q",
					mapper.name, key, name)
			}
			for key := range row {
				assert.Truef(t, want[key],
					"%s prints %q, which no proto field declares", mapper.name, key)
			}

			assertBoolFieldsAreNotInterchanged(t, mapper)
		})
	}
}

// expectedPrintedValue is the form proto field f must reach the envelope
// in. Three kinds do not print raw, so each one is stated here rather than
// left out: putTime renders a timestamp as a UTC string (the format itself
// is pinned by TestPutTime_OmitsAnAbsentTimestampAndFormatsAPresentOneInUTC),
// an enum prints its value name, and a string, boolean, or int64 prints
// itself.
func expectedPrintedValue(t *testing.T, m proto.Message, f protoreflect.FieldDescriptor) any {
	t.Helper()
	v := m.ProtoReflect().Get(f)
	switch f.Kind() {
	case protoreflect.StringKind:
		return v.String()
	case protoreflect.BoolKind:
		return v.Bool()
	case protoreflect.Int64Kind:
		return v.Int()
	case protoreflect.EnumKind:
		// The generated String() returns the proto value NAME, which is
		// what the mappers print.
		return string(f.Enum().Values().ByNumber(v.Enum()).Name())
	case protoreflect.MessageKind:
		require.Equalf(t, "google.protobuf.Timestamp", string(f.Message().FullName()),
			"field %s is a message this expectation has no rule for", f.Name())
		ts, ok := v.Message().Interface().(*timestamppb.Timestamp)
		require.Truef(t, ok, "field %s does not hold a Timestamp", f.Name())
		return ts.AsTime().UTC().Format(timeFormat)
	default:
		t.Fatalf("field %s has kind %v, which this expectation does not render", f.Name(), f.Kind())
		return nil
	}
}

// assertBoolFieldsAreNotInterchanged catches a mapper that prints a
// SIBLING boolean under a field's key.
//
// A boolean carries one bit, so the per-field distinct values that separate
// the strings and the stamps cannot separate two booleans: with both set
// true, a worker row that read `auto_registered` under the `owner_deleted`
// key printed the right answer for the wrong reason. Setting exactly one
// boolean at a time is what separates them, and the all-false state that
// runs first catches a mapper that prints a constant.
func assertBoolFieldsAreNotInterchanged(t *testing.T, mapper adminRowMapper) {
	t.Helper()
	r := mapper.msg.ProtoReflect()
	fields := r.Descriptor().Fields()
	var bools []protoreflect.FieldDescriptor
	for i := range fields.Len() {
		if f := fields.Get(i); f.Kind() == protoreflect.BoolKind {
			bools = append(bools, f)
		}
	}
	if len(bools) == 0 {
		return
	}
	// State -1 clears every boolean; state k sets bools[k] alone.
	for state := -1; state < len(bools); state++ {
		for k, f := range bools {
			r.Set(f, protoreflect.ValueOfBool(k == state))
		}
		row := mapper.render()
		for k, f := range bools {
			key := mapper.keys[string(f.Name())]
			assert.Equalf(t, k == state, row[key],
				"%s prints the wrong boolean under %q; only proto field %q may reach it",
				mapper.name, key, f.Name())
		}
	}
}

// populateEveryField sets every field of m to a non-zero value, so a mapper
// that omits one is visible as an absent JSON key rather than as a zero.
//
// Each value is DISTINCT from every sibling of the same kind. A shared
// value cannot separate two fields: a mapper that reads `creator_username`
// under the `created_by` key prints a correct-looking string, and only a
// value that differs per field makes the swap fail. Booleans cannot carry a
// distinct value, so assertBoolFieldsAreNotInterchanged separates those in
// its own pass.
//
// It refuses a field whose kind it has no rule for, which is what keeps the
// table above honest when the proto grows a shape these messages do not
// carry today.
func populateEveryField(t *testing.T, m proto.Message) {
	t.Helper()
	r := m.ProtoReflect()
	fields := r.Descriptor().Fields()
	base := time.Date(2026, 8, 18, 3, 0, 0, 0, time.UTC)
	for i := range fields.Len() {
		f := fields.Get(i)
		require.Falsef(t, f.IsList() || f.IsMap(), "field %s is repeated and needs its own rule here", f.Name())
		switch f.Kind() {
		case protoreflect.StringKind:
			r.Set(f, protoreflect.ValueOfString("v-"+string(f.Name())))
		case protoreflect.BoolKind:
			r.Set(f, protoreflect.ValueOfBool(true))
		case protoreflect.Int64Kind:
			r.Set(f, protoreflect.ValueOfInt64(int64(7+i)))
		case protoreflect.EnumKind:
			values := f.Enum().Values()
			r.Set(f, protoreflect.ValueOfEnum(values.Get(values.Len()-1).Number()))
		case protoreflect.MessageKind:
			require.Equalf(t, "google.protobuf.Timestamp", string(f.Message().FullName()),
				"field %s is a message this populator has no rule for", f.Name())
			// One hour apart, which the millisecond-precision format the
			// rows print keeps distinguishable.
			stamp := timestamppb.New(base.Add(time.Duration(i) * time.Hour))
			r.Set(f, protoreflect.ValueOfMessage(stamp.ProtoReflect()))
		default:
			t.Fatalf("field %s has kind %v, which this populator does not set", f.Name(), f.Kind())
		}
	}
}
