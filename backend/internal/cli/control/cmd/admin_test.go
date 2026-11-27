package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/captcha"
	"github.com/leapmux/leapmux/internal/hub/ratelimit"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/settings"
)

// adminRun runs `leapmux admin <args...> --json` against a hub that
// nobody listens on. A verb that passes its own checks then fails at the
// dial with unavailable, so a test reads which check refused it.
func adminRun(t *testing.T, args ...string) cliOutcome {
	t.Helper()
	isolateCLIEnv(t)
	return runControl(t, append(append([]string{"admin"}, args...), "--hub", unreachableHub)...)
}

// TestAdminUserUpdateNoFields pins the no-op refusal verbatim.
func TestAdminUserUpdateNoFields(t *testing.T) {
	f := adminRun(t, "user", "update", "amy").requireFailure(t, cli.CodeUsage)
	assert.Equal(t, "no fields to update (use --display-name, --email, --email-verified, or --clear-pending-email)", f.Message)
}

// TestParseSettingValue pins the scalar coercion the old offline verb
// accepted: JSON documents pass through, and the CLI quotes bare scalars.
func TestParseSettingValue(t *testing.T) {
	const adminReset = "leapmux admin settings reset <key>"
	v, err := parseSettingValue("3600", adminReset)
	require.NoError(t, err)
	assert.JSONEq(t, "3600", string(v))

	v, err = parseSettingValue("true", adminReset)
	require.NoError(t, err)
	assert.JSONEq(t, "true", string(v))

	v, err = parseSettingValue(`{"port":465}`, adminReset)
	require.NoError(t, err)
	assert.JSONEq(t, `{"port":465}`, string(v))

	v, err = parseSettingValue("dark", adminReset)
	require.NoError(t, err)
	assert.JSONEq(t, `"dark"`, string(v))

	// An empty VALUE is refused, but the empty STRING is a legal value for
	// some keys, so the message must state both ways to reach it.
	_, err = parseSettingValue("", adminReset)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `pass '""' to store the empty string`)
	assert.Contains(t, err.Error(), `"leapmux admin settings reset <key>"`)

	_, err = parseSettingValue("", "leapmux settings reset <key>")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"leapmux settings reset <key>"`)
	assert.NotContains(t, err.Error(), "admin settings reset")

	v, err = parseSettingValue("[1,2]", adminReset)
	require.NoError(t, err)
	assert.JSONEq(t, "[1,2]", string(v))

	v, err = parseSettingValue(`"dark"`, adminReset)
	require.NoError(t, err)
	assert.JSONEq(t, `"dark"`, string(v), "an already-quoted JSON string must not be wrapped again")

	// A value that OPENS a document is a document. Treating a malformed one
	// as a scalar string was worse than useless: for an object-valued key
	// the hub answered with a type error that never mentioned the missing
	// brace, and for a string-valued key it accepted the malformed text and
	// STORED it. An operator who really means a literal brace quotes it.
	_, err = parseSettingValue("{not-json", adminReset)
	require.Error(t, err, "an object-looking value that is not valid JSON must be refused here, not sent")
	assert.Contains(t, err.Error(), "not valid JSON")

	_, err = parseSettingValue(`[1,2`, adminReset)
	require.Error(t, err, "an array-looking value that is not valid JSON must be refused too")

	v, err = parseSettingValue(`"{literal-brace}"`, adminReset)
	require.NoError(t, err, "quoting is how an operator asks for a literal brace")
	assert.JSONEq(t, `"{literal-brace}"`, string(v))
}

func TestAdminRateLimitSet_RequiresAtLeastOneTuningFlag(t *testing.T) {
	f := adminRun(t, "rate-limit", "set", "elevation").requireFailure(t, cli.CodeUsage)
	assert.Equal(t, "pass --max-attempts, --window, or both", f.Message)
}

// Tuning one field must not disturb the other, and must not touch the
// on/off switch: `rate-limit enable|disable` owns that, so adjusting a
// window cannot silently re-arm a limiter an operator turned off.
func TestAdminRateLimitSet_AcceptsOneFlagAndNeverWritesEnabled(t *testing.T) {
	for _, args := range [][]string{
		{"elevation", "--max-attempts", "5"},
		{"elevation", "--window", "900"},
	} {
		adminRun(t, append([]string{"rate-limit", "set"}, args...)...).requireFailure(t, cli.CodeUnavailable)
	}
}

func TestAdminRateLimitSet_RefusesUnknownOperation(t *testing.T) {
	f := adminRun(t, "rate-limit", "set", "chnage-password", "--max-attempts", "5").requireFailure(t, cli.CodeUsage)
	assert.Equal(t, `unknown operation "chnage-password"`, f.Message)
	require.Len(t, f.Hints, 1)
	assert.Contains(t, f.Hints[0], "elevation", "the hint must list the known operations")

	f = adminRun(t, "rate-limit", "reset", " ").requireFailure(t, cli.CodeUsage)
	assert.Equal(t, "<operation> is empty", f.Message)
}

// The hub decodes the value as JSON, so a bare `T`/`TRUE`/`False` must be
// normalized to the JSON literal. Passing the token through made the hub
// answer with a decode error that specifies a line the operator never typed.
func TestParseSettingValue_NormalizesEveryBooleanSpelling(t *testing.T) {
	const resetCmd = "leapmux admin settings reset <key>"
	for _, raw := range []string{"true", "TRUE", "True", "t", "T"} {
		v, err := parseSettingValue(raw, resetCmd)
		require.NoError(t, err)
		assert.JSONEq(t, `true`, string(v), "%q must ship the JSON literal", raw)
	}
	for _, raw := range []string{"false", "FALSE", "False", "f", "F"} {
		v, err := parseSettingValue(raw, resetCmd)
		require.NoError(t, err)
		assert.JSONEq(t, `false`, string(v), "%q must ship the JSON literal", raw)
	}
}

// ParseBool also accepts `1` and `0`. The numeric test runs first, so a
// bare digit stays a number — testing bool first turned
// `settings set <int_key> 1` into `true`.
func TestParseSettingValue_DigitsStayNumbers(t *testing.T) {
	const resetCmd = "leapmux admin settings reset <key>"
	for _, raw := range []string{"0", "1", "42", "-7", "0.5"} {
		v, err := parseSettingValue(raw, resetCmd)
		require.NoError(t, err)
		assert.JSONEq(t, raw, string(v), "%q must ship as a number", raw)
	}

	// strconv.ParseFloat accepts far more than JSON does. Each of these
	// parses as a float and is NOT valid JSON, so shipping it verbatim made
	// the hub answer with a decode error that specifies a character the
	// operator typed for a different reason. They belong on the string path.
	for _, raw := range []string{"NaN", "Infinity", "-Inf", "infinity", "0x1p-2", "1_000"} {
		v, err := parseSettingValue(raw, resetCmd)
		require.NoErrorf(t, err, "%q", raw)
		quoted, mErr := json.Marshal(raw)
		require.NoError(t, mErr)
		assert.JSONEqf(t, string(quoted), string(v), "%q is not JSON, so it must ship quoted", raw)
	}
}

func TestFormatTime_FormatsAnOptionalTimestampInUTC(t *testing.T) {
	assert.Empty(t, formatTime(nil))

	// A non-UTC zone must render as UTC, so two rows from two hubs compare.
	zone := time.FixedZone("UTC+9", 9*60*60)
	stamp := timestamppb.New(time.Date(2026, 8, 18, 12, 0, 0, 0, zone))
	assert.Equal(t, "2026-08-18T03:00:00.000Z", formatTime(stamp))
}

func TestAdminUserRowOf_OmitsTheTimestampsThatAreAbsent(t *testing.T) {
	created := timestamppb.New(time.Date(2026, 8, 18, 3, 0, 0, 0, time.UTC))
	row := rowJSONFields(t, adminUserRowOf(&leapmuxv1.AdminUser{
		Id: "u1", Username: "amy", CreatedAt: created,
	}))

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

func TestAdminWorkerRowOf_OmitsAnUnseenWorkersLastSeen(t *testing.T) {
	created := timestamppb.New(time.Date(2026, 8, 18, 3, 0, 0, 0, time.UTC))
	row := rowJSONFields(t, adminWorkerRowOf(&leapmuxv1.AdminWorker{Id: "w1", CreatedAt: created}))
	assert.NotContains(t, row, "last_seen_at", "a worker that never connected has no last-seen time")

	seen := timestamppb.New(time.Date(2026, 8, 18, 4, 30, 0, 0, time.UTC))
	row = rowJSONFields(t, adminWorkerRowOf(&leapmuxv1.AdminWorker{Id: "w1", CreatedAt: created, LastSeenAt: seen}))
	assert.Equal(t, "2026-08-18T04:30:00.000Z", row["last_seen_at"])
}

// The CLI's JSON envelope is a public contract: an operator's script reads
// these keys by name. A rename or a dropped key is a silent break — the
// script sees an absent field, not an error.
func TestAdminOAuthProviderRowOf_CarriesEveryKeyAScriptReads(t *testing.T) {
	row := rowJSONFields(t, adminOAuthProviderRowOf(&leapmuxv1.AdminOAuthProvider{
		Id:           "p1",
		ProviderType: "oidc",
		Name:         "Corp SSO",
		IssuerUrl:    "https://sso.example.com",
		ClientId:     "client-1",
		Scopes:       "openid email",
		TrustEmail:   true,
		Enabled:      false,
		CreatedAt:    timestamppb.New(time.Date(2026, 8, 18, 3, 0, 0, 0, time.UTC)),
	}))

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

func TestAdminSessionRowOf_CarriesEveryKeyAScriptReads(t *testing.T) {
	at := func(h int) *timestamppb.Timestamp {
		return timestamppb.New(time.Date(2026, 8, 18, h, 0, 0, 0, time.UTC))
	}
	row := rowJSONFields(t, adminSessionRowOf(&leapmuxv1.AdminSession{
		Id: "s1", UserId: "u1", Username: "alice", UserDeleted: true,
		CreatedAt: at(3), LastActiveAt: at(4), ExpiresAt: at(5),
		IpAddress: "203.0.113.7", UserAgent: "curl/8",
	}))

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
	for _, limit := range []int64{0, -1, -50, contracts.MaxPageLimit + 1} {
		require.Error(t, validateListLimit(limit), "limit %d must be refused", limit)
	}
	for _, limit := range []int64{1, contracts.DefaultPageLimit, contracts.MaxPageLimit} {
		require.NoError(t, validateListLimit(limit), "limit %d must be accepted", limit)
	}
}

// Every paginated admin verb must hold the limit, not just `user list`.
func TestAdminListVerbs_RefuseANonPositiveLimit(t *testing.T) {
	leaves := adminLeaves(t)
	want := fmt.Sprintf("limit must be between %d and %d", minListLimit, contracts.MaxPageLimit)
	paged := 0
	for _, c := range adminCases() {
		if leaves[c.name()].Flags().Lookup("limit") == nil {
			continue
		}
		paged++
		args := append(append(append([]string{}, c.path...), c.args...), "--limit", "0")
		f := adminRun(t, args...).requireFailure(t, cli.CodeUsage)
		assert.Equal(t, want, f.Message, "%s must refuse --limit 0", c.name())
	}
	assert.GreaterOrEqual(t, paged, 7, "the scan must find the paginated verbs")
}

// `--secret` with no `--provider` used to fall through to the ALTCHA branch
// and overwrite the signing key, invalidating every live challenge.
func TestAdminCaptchaSet_RefusesAProviderForeignFlag(t *testing.T) {
	f := adminRun(t, "captcha", "set", "--provider", "altcha", "--site-key", "abc").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "--site-key applies only to")
	assert.Contains(t, f.Message, "the target provider is altcha")

	f = adminRun(t, "captcha", "set", "--provider", "turnstile", "--cost", "5").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "--cost applies only to altcha")
}

func TestAdminCaptchaSet_RefusesAnEmptySecretAndAnEmptyInvocation(t *testing.T) {
	f := adminRun(t, "captcha", "set", "--provider", "turnstile", "--secret", "").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "--secret must not be empty")

	f = adminRun(t, "captcha", "set").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "nothing to set")
}

func TestAdminCaptchaSet_RefusesAnUnknownProvider(t *testing.T) {
	f := adminRun(t, "captcha", "set", "--provider", "hcaptcha").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "hcaptcha")
}

// TestSettingShapeFromDescriptor_ScalarKindMatchesTheGoSpelling pins the
// CLI's kind names to settings.FieldKind.String().
//
// The CLI used to restate the wire-to-schema table, and the two already
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
	assert.Equal(t, "the active captcha provider alias (prefer `leapmux admin captcha ...`)",
		settingDescriptionText(captcha.CaptchaSelectedKey.Name(), "the active captcha provider alias"))
	assert.Equal(t, "rate limit for elevation (prefer `leapmux admin rate-limit ...`)",
		settingDescriptionText(ratelimit.SettingKeyPrefix+"elevation", "rate limit for elevation"))
	assert.Equal(t, "the public origin", settingDescriptionText("public_url", "the public origin"),
		"a key with no domain verb keeps the hub's own summary")
}

// TestTokenRowsCarryTheOwnerDeletedFlag pins a field the two inline row
// loops both dropped.
//
// `--user id:<id>` on the token listings deliberately resolves through
// GetByIDIncludeDeleted so an operator can enumerate a soft-deleted
// account's still-live tokens and revoke them. The hub populates
// owner_deleted for exactly that, and neither loop printed it — so the
// listing that exists to find a deleted owner's tokens never said the
// owner was deleted. The session row already carried the equivalent flag.
func TestTokenRowsCarryTheOwnerDeletedFlag(t *testing.T) {
	created := timestamppb.New(time.Unix(1_700_000_000, 0))

	api := rowJSONFields(t, adminAPITokenRowOf(&leapmuxv1.AdminAPIToken{
		Id: "t-1", UserId: "u-1", Username: "alice", OwnerDeleted: true,
		ClientId: oauthapp.ControlCLIClientID, InstallationName: "laptop", GrantedScopes: []string{"account:read", "account:write", "agent:read", "agent:write", "file:read", "git:read", "git:write", "terminal:read", "terminal:write", "tunnel:open", "worker:admin", "worker:read", "workspace:read", "workspace:write"}, CreatedAt: created,
	}))
	assert.Equal(t, true, api["owner_deleted"])
	assert.Equal(t, oauthapp.ControlCLIClientID, api["client_id"])
	assert.Equal(t, "laptop", api["installation_name"])
	assert.Contains(t, api["granted_scopes"], "workspace:read",
		"the grant always appears, so a reader can tell a narrow credential from a wide one")
	assert.NotContains(t, api, "revoked_at", "an absent stamp leaves no field")

	del := rowJSONFields(t, adminDelegationTokenRowOf(&leapmuxv1.AdminDelegationToken{
		Id: "d-1", UserId: "u-1", Username: "alice", OwnerDeleted: false,
		WorkerId: "w-1", CreatedAt: created,
		RevokedAt: created,
	}))
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
// The check has to run before requireAdminClient, because an operator's typo
// must not need a hub.
func TestCaptchaSetRefusesAnAlgorithmBeforeTheDial(t *testing.T) {
	f := adminRun(t, "captcha", "set", "--algorithm", "SHA-999").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "unsupported captcha algorithm")
	assert.Contains(t, f.Message, "SCRYPT", "the refusal lists what IS supported")

	// A supported one passes the local check and fails only at the
	// transport.
	adminRun(t, "captcha", "set", "--algorithm", "SCRYPT").requireFailure(t, cli.CodeUnavailable)
}

// TestUserArgumentRefusalsHappenBeforeTheDial pins pre-dial <user> validation
// through control.ParseUserArgument.
//
// Eight verbs address exactly one user, and the hub holds the same rule --
// but it can only answer over a connection the operator did not need. An
// empty <user> must state the argument, not the transport.
func TestUserArgumentRefusalsHappenBeforeTheDial(t *testing.T) {
	cases := []struct {
		path []string
		// rest carries the other REQUIRED flags of the verb, so the last
		// case reaches the transport instead of a second local refusal.
		// `reset-password` prompts for a password it was not given, and a
		// stdin that is not a terminal turns that prompt into its own
		// refusal.
		rest []string
	}{
		{[]string{"user", "get"}, nil},
		{[]string{"user", "update"}, []string{"--display-name", "Amy"}},
		{[]string{"user", "delete"}, nil},
		{[]string{"user", "grant-admin"}, nil},
		{[]string{"user", "revoke-admin"}, nil},
		{[]string{"user", "reset-password"}, []string{"--password", "s3cret12"}},
		{[]string{"session", "revoke-user"}, nil},
		{[]string{"api-token", "issue"}, []string{"--installation-name", "ci"}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.path, " "), func(t *testing.T) {
			run := func(user string) cliOutcome {
				return adminRun(t, append(append(append([]string{}, tc.path...), user), tc.rest...)...)
			}
			f := run(" ").requireFailure(t, cli.CodeUsage)
			assert.Equal(t, "<user> is empty", f.Message)

			f = run("id:").requireFailure(t, cli.CodeUsage)
			assert.Equal(t, `<user> "id:" has no id after the "id:" prefix`, f.Message)

			// A user given either way passes the local check and fails only
			// at the transport.
			run("amy").requireFailure(t, cli.CodeUnavailable)
			run("id:usr_1").requireFailure(t, cli.CodeUnavailable)
		})
	}
}

// TestUserFilterRefusesInvalidUserBeforeTheDial pins the unified --user
// listing filter. An explicit empty or malformed selector fails before the
// dial; omitted, username, "username:<name>", and "id:<id>" pass through.
func TestUserFilterRefusesInvalidUserBeforeTheDial(t *testing.T) {
	for _, path := range [][]string{
		{"api-token", "list"},
		{"delegation-token", "list"},
		{"worker", "list"},
		{"session", "list"},
	} {
		t.Run(strings.Join(path, " "), func(t *testing.T) {
			for _, bad := range []string{"", "id:"} {
				adminRun(t, append(append([]string{}, path...), "--user", bad)...).
					requireFailure(t, cli.CodeUsage)
			}

			for _, args := range [][]string{nil, {"--user", "id:usr_1"}, {"--user", "amy"}, {"--user", "username:amy"}} {
				adminRun(t, append(append([]string{}, path...), args...)...).requireFailure(t, cli.CodeUnavailable)
			}
		})
	}
}

// TestUserFilterFlagsBind pins --user that bind declares. Every admin listing
// that filters by user shares this single flag and help text.
func TestUserFilterFlagsBind(t *testing.T) {
	var f userFilterFlags
	fs := pflag.NewFlagSet("list", pflag.ContinueOnError)
	f.bind(fs)

	require.NoError(t, fs.Parse([]string{"--user", "id:usr_1"}))
	assert.Equal(t, "id:usr_1", f.User)
	require.NotNil(t, fs.Lookup("user"))
	assert.Contains(t, fs.Lookup("user").Usage, "omit for all users")
}

// TestUserFilterFlagsSelector pins the target that parse builds for --user:
// unset when omitted, or the parsed UserSelector otherwise.
func TestUserFilterFlagsSelector(t *testing.T) {
	newInv := func(args ...string) (*userFilterFlags, *cli.Invocation) {
		f := &userFilterFlags{}
		cmd := cli.NewLeaf(cli.Leaf{
			Use:   "probe",
			Short: "Probe user filter",
			Flags: func(fs *pflag.FlagSet) { f.bind(fs) },
		})
		require.NoError(t, cmd.Flags().Parse(args))
		return f, &cli.Invocation{Command: cmd}
	}

	none, inv := newInv()
	got, err := none.parse(inv)
	require.NoError(t, err)
	assert.False(t, got.isSet())
	assert.Nil(t, got.selector)
	assert.Empty(t, got.prefix)

	byID, inv := newInv("--user", "id:usr_1")
	got, err = byID.parse(inv)
	require.NoError(t, err)
	assert.True(t, got.isSet())
	assert.Equal(t, "usr_1", got.selector.GetId())
	assert.Empty(t, got.selector.GetUsername(), "an id filter carries no username")
	assert.Equal(t, "usr_1", got.prefix)

	byName, inv := newInv("--user", "amy")
	got, err = byName.parse(inv)
	require.NoError(t, err)
	assert.True(t, got.isSet())
	assert.Equal(t, "amy", got.selector.GetUsername())
	assert.Empty(t, got.selector.GetId(), "a username filter carries no id")
	assert.Empty(t, got.prefix, "a 3-char handle is below MinPrefixLength so it does not trigger prefix fallback")

	byLongName, inv := newInv("--user", "alice")
	got, err = byLongName.parse(inv)
	require.NoError(t, err)
	assert.True(t, got.isSet())
	assert.Equal(t, "alice", got.selector.GetUsername())
	assert.Empty(t, got.selector.GetId())
	assert.Equal(t, "alice", got.prefix)
}

// TestUserTarget_ReadsAUsernameOrAnID pins the selector that a verb
// addressing ONE user sends: exactly one handle, and the selector carries
// that handle alone.
func TestUserTarget_ReadsAUsernameOrAnID(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		want *leapmuxv1.UserSelector
	}{
		{"amy", service.SelectUserByUsername("amy")},
		{"id:usr_1", service.SelectUserByID("usr_1")},
		{" id: usr_1 ", service.SelectUserByID("usr_1")},
		// A username cannot hold a colon, so only the exact "id:" prefix
		// selects by id.
		{"ID:usr_1", service.SelectUserByUsername("ID:usr_1")},
	} {
		got, err := control.ParseUserArgument(tc.arg)
		require.NoError(t, err)
		assert.True(t, proto.Equal(tc.want, got), "%q selected %v", tc.arg, got)
	}
}

// An empty explicit --user value builds no target, so no listing silently
// widens to all users when a script passed an empty variable.
func TestUserFilterFlags_BuildNoSelectorForEmptyUser(t *testing.T) {
	filter := &userFilterFlags{}
	cmd := cli.NewLeaf(cli.Leaf{
		Use:   "probe",
		Short: "Probe empty user filter",
		Flags: func(fs *pflag.FlagSet) { filter.bind(fs) },
	})
	require.NoError(t, cmd.Flags().Parse([]string{"--user", ""}))
	got, err := filter.parse(&cli.Invocation{Command: cmd})
	require.Error(t, err)
	assert.False(t, got.isSet(), "a refused value produces an unset target")
}

// TestOnlySettingValue pins the count check on a single-key reply. The hub
// answers one value for each listed key. Any other count is a hub fault, and
// reading index 0 of it would print a value for the wrong key or panic.
func TestOnlySettingValue(t *testing.T) {
	one := &leapmuxv1.SettingValue{Key: "theme"}
	got, err := onlySettingValue([]*leapmuxv1.SettingValue{one})
	require.NoError(t, err)
	assert.Same(t, one, got)

	for _, values := range [][]*leapmuxv1.SettingValue{
		nil,
		{},
		{one, {Key: "another_key"}},
	} {
		got, err := onlySettingValue(values)
		require.Errorf(t, err, "%d values must be refused", len(values))
		assert.Nil(t, got)
		assert.Contains(t, err.Error(), fmt.Sprintf("the hub answered %d setting values for one key", len(values)))
	}
}

// TestAdminPageFlagsRequest pins the page that a listing verb sends. The verb
// framework refuses a limit outside [1, contracts.MaxPageLimit] before the
// dial, so the narrowing to int32 must keep the largest legal limit intact.
func TestAdminPageFlagsRequest(t *testing.T) {
	page := (&adminPageFlags{Limit: 7, Cursor: "c1"}).request()
	assert.EqualValues(t, 7, page.GetLimit())
	assert.Equal(t, "c1", page.GetCursor())

	page = (&adminPageFlags{Limit: contracts.MaxPageLimit}).request()
	assert.EqualValues(t, contracts.MaxPageLimit, page.GetLimit(), "the largest legal limit survives the narrowing")
	assert.Empty(t, page.GetCursor(), "no cursor reads the first page")
}

// An empty <user> must answer BEFORE the password prompt, for the same
// reason `user create` checks <username> first: an operator must not type a
// secret into a request that cannot succeed, and with a stdin that is not a
// terminal the prompt's own refusal states --password, which is not the
// missing argument.
func TestAdminUserResetPassword_RefusesAnEmptyUserBeforeItPrompts(t *testing.T) {
	f := adminRun(t, "user", "reset-password", " ").requireFailure(t, cli.CodeUsage)
	assert.Equal(t, "<user> is empty", f.Message, "the <user> check must run before the prompt")

	// A supplied password reaches the same refusal, so the order does not
	// depend on whether a prompt would have happened.
	f = adminRun(t, "user", "reset-password", " ", "--password", "s3cret12").requireFailure(t, cli.CodeUsage)
	assert.Equal(t, "<user> is empty", f.Message)

	// With a user but no password and no terminal, the prompt refusal takes
	// over -- so the verb really does prompt rather than send a blank
	// password to the hub.
	f = adminRun(t, "user", "reset-password", "amy").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "--password is required (stdin is not a terminal)")
}

// TestAdminUserUpdateReportsTheMoreSpecificRefusalFirst pins the ordering:
// an operator who typed no flags at all is better served by "nothing to
// update" than by a notice about the <user> argument.
func TestAdminUserUpdateReportsTheMoreSpecificRefusalFirst(t *testing.T) {
	f := adminRun(t, "user", "update", " ").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "no fields to update")

	// With a field but an empty <user>, the <user> refusal takes over.
	f = adminRun(t, "user", "update", " ", "--display-name", "Amy").requireFailure(t, cli.CodeUsage)
	assert.Equal(t, "<user> is empty", f.Message)
}

// The token LISTINGS are FILTERS, not selectors: no filter means "every
// user". They must never inherit the selector refusal.
func TestTokenListingsAcceptAnEmptySelector(t *testing.T) {
	adminRun(t, "api-token", "list").requireFailure(t, cli.CodeUnavailable)
	adminRun(t, "delegation-token", "list").requireFailure(t, cli.CodeUnavailable)
}

// An empty <username> must answer BEFORE the password prompt. The prompt
// ran first, so an operator typed a secret into a request that could not
// succeed -- and with a stdin that is not a terminal the refusal stated
// --password, which is not what was missing.
func TestAdminUserCreate_RefusesAnEmptyUsernameBeforeItPrompts(t *testing.T) {
	f := adminRun(t, "user", "create", " ").requireFailure(t, cli.CodeUsage)
	assert.Equal(t, "<username> is empty", f.Message, "the username check must run before the prompt")

	// A supplied password reaches the same refusal, so the order does not
	// depend on whether a prompt would have happened.
	f = adminRun(t, "user", "create", " ", "--password", "s3cret").requireFailure(t, cli.CodeUsage)
	assert.Equal(t, "<username> is empty", f.Message)

	// And a username gets past the check, so the guard did not swallow the
	// working invocation.
	adminRun(t, "user", "create", "amy", "--password", "s3cret").requireFailure(t, cli.CodeUnavailable)
}

// `settings set-secret <key> <json>` merges NAMED fields, so only a JSON
// OBJECT can be a legal partial. A validity test alone let every other JSON
// value dial the hub and come back as a decode error that specifies a Go
// type.
func TestAdminSettingsSetSecret_RefusesAnyValueThatIsNotADocument(t *testing.T) {
	for _, value := range []string{`"hunter2"`, "5", "null", "[1,2]", "true", `"{}"`} {
		f := adminRun(t, "settings", "set-secret", "smtp", value).requireFailure(t, cli.CodeUsage)
		assert.Containsf(t, f.Message, "<json> must be a JSON object of the secret fields", "%s", value)
		assert.Containsf(t, f.Message, `{"password":"..."}`, "the refusal shows the shape: %s", value)
	}

	// A document passes the local check and fails only at the transport,
	// and surrounding whitespace does not change that.
	for _, value := range []string{`{"password":"x"}`, "  {\"password\":\"x\"}  "} {
		adminRun(t, "settings", "set-secret", "smtp", value).requireFailure(t, cli.CodeUnavailable)
	}

	// A malformed document is still refused here, not sent.
	f := adminRun(t, "settings", "set-secret", "smtp", `{"password":`).requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "<json> must be a JSON object")
}

// An admin verb with no hub address must identify the flag and the
// variable. Falling through built a client for the empty URL, whose
// credential lookup answered `hub url missing hostname` under the
// not_logged_in code -- a message that states neither.
func TestRequireAdminClient_RefusesAnEmptyHubAddress(t *testing.T) {
	isolateCLIEnv(t)

	f := runControl(t, "admin", "settings", "list").requireFailure(t, cli.CodeNotLoggedIn)
	assert.Equal(t, "no hub selected", f.Message)
	hints := strings.Join(f.Hints, "\n")
	assert.Contains(t, hints, "--hub")
	assert.Contains(t, hints, "LEAPMUX_HUB")
	assert.Contains(t, hints, "auth login", "the hint states the command that stores a credential")
	assert.NotContains(t, f.Message+hints, "missing hostname")

	// The refusal inside a tab still wins, because that operator has an
	// address and a different problem.
	t.Setenv("LEAPMUX_CONTROL_SOCK", workerIPCSock)
	f = runControl(t, "admin", "settings", "list").requireFailure(t, cli.CodeUsage)
	assert.Equal(t, "admin commands do not run inside a LeapMux tab", f.Message)
}

// rowMapper is one row mapper under test: the proto message it renders, the
// call that renders it, and the JSON key each proto field reaches the
// operator under. The admin rows use it, and so do the worker and workspace
// rows that `worker list` and `workspace list` print.
type rowMapper struct {
	name   string
	msg    proto.Message
	render func() any
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

	mappers := []rowMapper{
		{
			name: "adminUserRowOf", msg: user,
			render: func() any { return adminUserRowOf(user) },
			keys: identity("id", "username", "display_name", "email", "email_verified",
				"pending_email", "password_set", "is_admin", "created_at", "updated_at"),
		},
		{
			name: "adminSessionRowOf", msg: session,
			render: func() any { return adminSessionRowOf(session) },
			keys: identity("id", "user_id", "username", "user_deleted", "created_at",
				"last_active_at", "expires_at", "ip_address", "user_agent"),
		},
		{
			name: "adminAPITokenRowOf", msg: apiToken,
			render: func() any { return adminAPITokenRowOf(apiToken) },
			keys:   merge(tokenFields(), identity("client_id", "client_name", "installation_name", "granted_scopes")),
		},
		{
			name: "adminDelegationTokenRowOf", msg: delegation,
			render: func() any { return adminDelegationTokenRowOf(delegation) },
			keys:   merge(tokenFields(), identity("worker_id")),
		},
		{
			name: "adminWorkerRowOf", msg: worker,
			render: func() any { return adminWorkerRowOf(worker) },
			keys: merge(identity("id", "registered_by", "owner_deleted", "status",
				"auto_registered", "created_at", "last_seen_at"),
				map[string]string{"owner_username": "username"}),
		},
		{
			name: "adminRegistrationKeyRowOf", msg: regKey,
			render: func() any { return adminRegistrationKeyRowOf(regKey) },
			keys: merge(identity("id", "created_by", "creator_deleted", "created_at", "expires_at"),
				map[string]string{"creator_username": "username"}),
		},
		{
			name: "adminOAuthProviderRowOf", msg: provider,
			render: func() any { return adminOAuthProviderRowOf(provider) },
			keys: merge(identity("id", "name", "issuer_url", "client_id", "scopes",
				"trust_email", "response_mode", "enabled", "created_at"),
				map[string]string{"provider_type": "type"}),
		},
	}

	for _, mapper := range mappers {
		t.Run(mapper.name, func(t *testing.T) {
			assertRowMapperCarriesEveryField(t, mapper)
		})
	}
}

func rowJSONFields(t *testing.T, row any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(row)
	require.NoError(t, err)
	var fields map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&fields))
	return fields
}

// assertRowMapperCarriesEveryField fails when mapper drops a field of its
// proto message, prints a field under the wrong key, or prints a key that no
// field declares. TestAdminRowMappers_CarryEveryFieldOfTheirProtoMessage
// states why each of the three is a silent break for an operator's script.
func assertRowMapperCarriesEveryField(t *testing.T, mapper rowMapper) {
	t.Helper()
	populateEveryField(t, mapper.msg)
	row := rowJSONFields(t, mapper.render())

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
		expected, err := json.Marshal(expectedPrintedValue(t, mapper.msg, f))
		require.NoError(t, err)
		actual, err := json.Marshal(row[key])
		require.NoError(t, err)
		assert.Equal(t, string(expected), string(actual), "%s must print proto field %q under %q", mapper.name, name, key)
	}
	for key := range row {
		assert.Truef(t, want[key],
			"%s prints %q, which no proto field declares", mapper.name, key)
	}

	assertBoolFieldsAreNotInterchanged(t, mapper)
}

// expectedPrintedValue is the form proto field f must reach the envelope
// in. Three kinds do not print raw, so each one is stated here rather than
// left out: formatTime renders a timestamp as a UTC string (the format itself
// is pinned by TestFormatTime_FormatsAnOptionalTimestampInUTC), an enum
// prints its lowercase token, and a string, boolean, or int64 prints itself.
func expectedPrintedValue(t *testing.T, m proto.Message, f protoreflect.FieldDescriptor) any {
	t.Helper()
	v := m.ProtoReflect().Get(f)
	// A REPEATED string prints as the slice itself, which is what a JSON
	// consumer reads: joining it would make one long value indistinguishable
	// from two short ones.
	if f.IsList() {
		require.Equalf(t, protoreflect.StringKind, f.Kind(),
			"field %s is a repeated %v, which this expectation has no rule for", f.Name(), f.Kind())
		list := v.List()
		out := make([]string, 0, list.Len())
		for i := range list.Len() {
			out = append(out, list.Get(i).String())
		}
		return out
	}
	switch f.Kind() {
	case protoreflect.StringKind:
		return v.String()
	case protoreflect.BoolKind:
		return v.Bool()
	case protoreflect.Int64Kind:
		return v.Int()
	case protoreflect.EnumKind:
		values := f.Enum().Values()
		prefix := strings.TrimSuffix(string(values.ByNumber(0).Name()), "UNSPECIFIED")
		return strings.ToLower(strings.TrimPrefix(string(values.ByNumber(v.Enum()).Name()), prefix))
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
func assertBoolFieldsAreNotInterchanged(t *testing.T, mapper rowMapper) {
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
		row := rowJSONFields(t, mapper.render())
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
		require.Falsef(t, f.IsMap(), "field %s is a map and needs its own rule here", f.Name())
		// A REPEATED string carries two distinct elements, for the same reason
		// every scalar carries a distinct value: one element could be produced
		// by a mapper reading a sibling list, and two cannot.
		if f.IsList() {
			require.Equalf(t, protoreflect.StringKind, f.Kind(),
				"field %s is a repeated %v, which this populator has no rule for", f.Name(), f.Kind())
			list := r.Mutable(f).List()
			list.Append(protoreflect.ValueOfString("v-" + string(f.Name()) + "-1"))
			list.Append(protoreflect.ValueOfString("v-" + string(f.Name()) + "-2"))
			continue
		}
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

// permissionDeniedUserService refuses ListUsers the way the hub refuses an
// administrator whose CLI credential carries no admin scope.
type permissionDeniedUserService struct {
	leapmuxv1connect.UnimplementedAdminUserServiceHandler
	message string
}

func (s *permissionDeniedUserService) ListUsers(
	_ context.Context,
	_ *leapmuxv1.ListUsersRequest,
) (*leapmuxv1.ListUsersResponse, error) {
	return nil, connect.NewError(connect.CodePermissionDenied, s.message)
}

// TestAdminVerb_ReportsTheHubsRefusalVerbatim is why the CLI adds nothing of
// its own to a PermissionDenied.
//
// It used to append a remedy whenever the LOCAL credential file recorded no
// admin scope, and the file cannot decide that. The hub answers two
// different refusals here and each one states its own remedy, so the
// appended line either repeated the hub word for word, or told a
// non-administrator to run a login that the hub refuses.
func TestAdminVerb_ReportsTheHubsRefusalVerbatim(t *testing.T) {
	for name, message := range map[string]string{
		"the account is not an administrator": "administrator privileges are required",
		"the credential carries no scope":     "this app was not granted the admin:read permission",
	} {
		t.Run(name, func(t *testing.T) {
			mux := http.NewServeMux()
			connectServer := connect.NewServer()
			leapmuxv1connect.RegisterAdminUserServiceHandler(connectServer,
				&permissionDeniedUserService{message: message})
			connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))
			srv := hubtransporttest.NewServer(t, mux)
			t.Cleanup(srv.Close)

			isolateCLIEnv(t)
			// A credential file that records NO admin scope, which is what
			// the deleted hint keyed on.
			require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
				HubURL: srv.URL, AccessToken: "lmx_a_at_1",
				ExpiresAt: time.Now().Add(time.Hour)}))

			out := runControl(t, "admin", "user", "list", "--hub", srv.URL).failed(t)

			envErr := envelopeError(t, out)
			assert.Equal(t, "permission_denied", envErr["code"])
			assert.Equal(t, message, envErr["message"],
				"the hub's wording reaches the operator once and unchanged; the CLI adds nothing")
			assert.NotContains(t, envErr, "hints", "the CLI appends no remedy of its own")
		})
	}
}

func TestRequireAdminClient_ClassifiesBadURLAndCorruptCredentials(t *testing.T) {
	for _, bad := range []string{"not-a-url", "ftp://hub.example", "unix:", "http://:8080"} {
		t.Run("bad_url_"+bad, func(t *testing.T) {
			isolateCLIEnv(t)
			runControl(t, "admin", "settings", "list", "--hub", bad).requireFailure(t, cli.CodeUsage)
		})
	}

	t.Run("corrupt credential file surfaces as internal", func(t *testing.T) {
		isolateCLIEnv(t)
		path, err := control.CredentialsPath("https://hub.example")
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte("{corrupt"), 0o600))

		f := runControl(t, "admin", "settings", "list", "--hub", "https://hub.example").requireFailure(t, cli.CodeInternal)
		assert.Contains(t, f.Message, path)
	})
}

func TestAdminNetworkStatusRenderText_NoLeadingBlankLineAndShowsBaseTCP(t *testing.T) {
	t.Run("no bound listeners omits leading blank line", func(t *testing.T) {
		var buf bytes.Buffer
		res := adminNetworkStatusResult{
			BaseTCPAddresses:  []string{"127.0.0.1:4327\n"},
			LocalIPCAddresses: []string{"unix:/tmp/hub.sock"},
			ConfiguredExtra:   []string{"100.64.0.1:4327"},
			Interfaces: []adminNetworkInterfaceRow{
				{Name: "lo0", Up: true, Addresses: []adminNetworkInterfaceAddr{{IP: "127.0.0.1", Loopback: true}}},
			},
		}
		require.NoError(t, res.RenderText(&buf))
		out := buf.String()
		assert.False(t, strings.HasPrefix(out, "\n"), "output must not start with a blank line: %q", out)
		assert.Contains(t, out, "Base TCP addresses: 127.0.0.1:4327\n")
		assert.Contains(t, out, "Local IPC: unix:/tmp/hub.sock\n")
		assert.Contains(t, out, "Extra listen addresses: 100.64.0.1:4327\n")
		assert.Contains(t, out, "\nInterfaces:\n")
	})

	t.Run("interfaces only omits leading blank line", func(t *testing.T) {
		var buf bytes.Buffer
		res := adminNetworkStatusResult{
			Interfaces: []adminNetworkInterfaceRow{
				{Name: "eth0", Up: false, Addresses: []adminNetworkInterfaceAddr{{IP: "10.0.0.2"}}},
			},
		}
		require.NoError(t, res.RenderText(&buf))
		assert.True(t, strings.HasPrefix(buf.String(), "Interfaces:\n"))
	})
}

func TestAdminAppUpdate_RefusesNoFlagsBeforeDial(t *testing.T) {
	f := adminRun(t, "app", "update", "lmx_app_1").requireFailure(t, cli.CodeUsage)
	assert.Equal(t, "nothing to update (pass at least one flag)", f.Message)
}

func TestAdminJSONSlicesMarshalAsEmptyArrayNotNull(t *testing.T) {
	appJSON, err := json.Marshal(appRegistrationRowOf(&leapmuxv1.App{ClientId: "c1"}))
	require.NoError(t, err)
	assert.Contains(t, string(appJSON), `"redirect_uris":[]`)
	assert.Contains(t, string(appJSON), `"scopes":[]`)
	assert.Contains(t, string(appJSON), `"grant_types":[]`)

	tokJSON, err := json.Marshal(adminAPITokenRowOf(&leapmuxv1.AdminAPIToken{Id: "t1"}))
	require.NoError(t, err)
	assert.Contains(t, string(tokJSON), `"granted_scopes":[]`)
}

func TestSplitScopeFlag_HandlesCRLF(t *testing.T) {
	got, err := splitScopeFlag("workspace:read\r\nfile:read\rgit:read")
	require.NoError(t, err)
	assert.Equal(t, []string{"workspace:read", "file:read", "git:read"}, got)

	_, err = splitScopeFlag("\r\n \t ")
	require.Error(t, err)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
}

func TestAdminValidationBeforeDial_FlagsAndRanges(t *testing.T) {
	// IdP validation (TRAPS-9, DESIGN-20, SWEEP-7)
	adminRun(t, "idp", "add", "--type", "github", "--trust-email", "").requireFailure(t, cli.CodeUsage)
	adminRun(t, "idp", "add", "--type", "bogus").requireFailure(t, cli.CodeUsage)
	adminRun(t, "idp", "add", "--type", "github", "--response-mode", "fragment").requireFailure(t, cli.CodeUsage)
	adminRun(t, "idp", "add", "--type", "github", "--response-mode", "").requireFailure(t, cli.CodeUsage)
	adminRun(t, "idp", "add", "--type", "github", "--response-mode", "form_post").requireFailure(t, cli.CodeUsage)
	adminRun(t, "idp", "add", "--type", "oidc", "--response-mode", "form_post", "--trust-email", "true").requireFailure(t, cli.CodeUnavailable)
	f := adminRun(t, "idp", "rotate-secret", "p1", "--client-secret", "   ").requireFailure(t, cli.CodeUsage)
	assert.Equal(t, "--client-secret is empty", f.Message)

	// User update optional boolean flag validation
	adminRun(t, "user", "update", "amy", "--email-verified", "").requireFailure(t, cli.CodeUsage)
	adminRun(t, "user", "update", "amy", "--email-verified", "invalid").requireFailure(t, cli.CodeUsage)

	// Worker status enum validation (DESIGN-20)
	adminRun(t, "worker", "list", "--status", "").requireFailure(t, cli.CodeUsage)
	adminRun(t, "worker", "list", "--status", "bogus").requireFailure(t, cli.CodeUsage)
	adminRun(t, "worker", "list", "--status", "all").requireFailure(t, cli.CodeUnavailable)

	// Captcha reset empty --provider (SCAN-5)
	adminRun(t, "captcha", "reset", "--provider", "").requireFailure(t, cli.CodeUsage)

	// Negative / out-of-range numeric flags (TRAPS-15)
	adminRun(t, "api-token", "issue", "amy", "--installation-name", "ci", "--ttl", "-1").requireFailure(t, cli.CodeUsage)
	adminRun(t, "api-token", "issue", "amy", "--installation-name", "ci", "--ttl", fmt.Sprintf("%d", service.MaxAPITokenTTLSeconds+1)).requireFailure(t, cli.CodeUsage)
	adminRun(t, "captcha", "set", "--cost", "-1").requireFailure(t, cli.CodeUsage)
	adminRun(t, "captcha", "set", "--memory-cost", "-1").requireFailure(t, cli.CodeUsage)
	adminRun(t, "captcha", "set", "--parallelism", "-1").requireFailure(t, cli.CodeUsage)
	adminRun(t, "captcha", "set", "--expires", "-1").requireFailure(t, cli.CodeUsage)
	adminRun(t, "captcha", "set", "--provider", "recaptcha_v3", "--min-score", "-0.1").requireFailure(t, cli.CodeUsage)
	adminRun(t, "captcha", "set", "--provider", "recaptcha_v3", "--min-score", "1.5").requireFailure(t, cli.CodeUsage)
	adminRun(t, "captcha", "set", "--provider", "recaptcha_v3", "--min-score", "NaN").requireFailure(t, cli.CodeUsage)
	adminRun(t, "rate-limit", "set", "elevation", "--max-attempts", "-1").requireFailure(t, cli.CodeUsage)
	adminRun(t, "rate-limit", "set", "elevation", "--max-attempts", "1001").requireFailure(t, cli.CodeUsage)
	adminRun(t, "rate-limit", "set", "elevation", "--max-attempts", "1000").requireFailure(t, cli.CodeUnavailable)
	adminRun(t, "rate-limit", "set", "elevation", "--window", "-1").requireFailure(t, cli.CodeUsage)
	adminRun(t, "rate-limit", "set", "elevation", "--window", "30").requireFailure(t, cli.CodeUsage)
	adminRun(t, "rate-limit", "set", "elevation", "--window", "59").requireFailure(t, cli.CodeUsage)
	adminRun(t, "rate-limit", "set", "elevation", "--window", "60").requireFailure(t, cli.CodeUnavailable)
	adminRun(t, "rate-limit", "set", "elevation", "--window", "86400").requireFailure(t, cli.CodeUnavailable)
	adminRun(t, "rate-limit", "set", "elevation", "--window", "86401").requireFailure(t, cli.CodeUsage)
	adminRun(t, "rate-limit", "set", "elevation", "--window", "90000").requireFailure(t, cli.CodeUsage)
}

func TestAdminUserPasswordCommands_CheckClientBeforePrompting(t *testing.T) {
	for _, args := range [][]string{
		{"admin", "user", "create", "amy"},
		{"admin", "user", "reset-password", "amy"},
	} {
		t.Run("inside tab "+strings.Join(args, " "), func(t *testing.T) {
			isolateCLIEnv(t)
			t.Setenv("LEAPMUX_CONTROL_SOCK", workerIPCSock)
			f := runControl(t, args...).requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, "admin commands do not run inside a LeapMux tab")
		})
		t.Run("no hub "+strings.Join(args, " "), func(t *testing.T) {
			isolateCLIEnv(t)
			f := runControl(t, args...).requireFailure(t, cli.CodeNotLoggedIn)
			assert.Equal(t, "no hub selected", f.Message)
		})
	}
}

func TestResolveAdminTarget_IDOnlyEntitiesDoNotMatchOwnerUsername(t *testing.T) {
	id1 := fullTestID("wrk-aaaa")
	id2 := fullTestID("wrk-aaab")
	list := func(_ context.Context) ([]cli.Candidate, error) {
		return []cli.Candidate{
			{ID: id1, Kind: "worker", Title: "alice"},
			{ID: id2, Kind: "worker", Title: "bob"},
		}, nil
	}

	// Bare username must NOT resolve to a worker owned by alice.
	got, err := resolveAdminTarget(t.Context(), "alice", "worker", false, list)
	require.NoError(t, err)
	assert.Equal(t, "alice", got, "unmatched bare owner username falls through rather than selecting the worker")

	// Explicit name:/title: selectors are rejected for ID-only entities.
	_, err = resolveAdminTarget(t.Context(), "name:alice", "worker", false, list)
	require.Error(t, err)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)

	_, err = resolveAdminTarget(t.Context(), "title:alice", "worker", false, list)
	require.Error(t, err)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)

	// Ambiguous ID prefix still carries Title as display context.
	_, err = resolveAdminTarget(t.Context(), id1[:7], "worker", false, list)
	require.Error(t, err)
	ce := cli.FromError(err)
	assert.Equal(t, cli.CodeAmbiguousTarget, ce.Code)
	require.Len(t, ce.Candidates, 2)
	assert.Equal(t, "alice", ce.Candidates[0].Title)
}

func TestCaptchaTuningSpecs_SortedByFlagName(t *testing.T) {
	require.NotEmpty(t, captchaTuningSpecs)
	for i := 1; i < len(captchaTuningSpecs); i++ {
		assert.Less(t, captchaTuningSpecs[i-1].flagName, captchaTuningSpecs[i].flagName,
			"captchaTuningSpecs must stay strictly sorted by flagName so foreign-flag refusals are deterministic")
	}
}
