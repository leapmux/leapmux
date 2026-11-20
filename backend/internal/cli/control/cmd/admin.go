package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/ratelimit"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
	"github.com/spf13/cobra"
)

// An admin call reports the hub's refusal VERBATIM, through
// control.EmitErrorWith, and adds nothing of its own.
//
// The CLI used to append a remedy of its own to any PermissionDenied when
// the local credential file recorded no admin scope. The file cannot decide
// that. The hub answers two different refusals here -- "administrator
// privileges are required" for an account that is not an administrator, and
// "this app was not granted the admin:read permission" for an administrator
// whose credential lacks the scope -- and each one already states what is
// wrong. Appending to both told a non-administrator to run a login that the
// hub refuses, and printed the same instruction twice on the refusal that
// was genuine.
//
// The scoped refusal states the PERMISSION rather than a command, because the
// same endpoint answers every registered app and `leapmux control auth login
// --scope admin:read` means nothing to a third-party one. The remedy for the
// CLI's own credential is the login flow's --scope flag, whose help states
// it; a refusal is not the place to repeat it.

// requireAdminClient is newClient plus the admin exclusions: admin
// commands NEVER use the worker-IPC transport. They talk to the hub
// directly, because the worker's IPC bridge is a typing device, not a
// security boundary — anything registered there is callable by any
// worker-spawned agent, so no admin procedure is. Refusing the transport
// here (and again on the client, in case the env var leaked in some other
// way) keeps `control admin ...` out of agent reach entirely.
//
// The refusal holds even with an explicit --hub. That flag would send the
// call with the user's stored credential for that hub, so an agent could
// still act as an administrator.
func requireAdminClient(inv *cli.Invocation) (*control.Client, error) {
	if os.Getenv(controlenv.SocketVariable) != "" {
		return nil, cli.Usagef("admin commands do not run inside a LeapMux tab").
			WithHint("run the command in a shell outside LeapMux, or unset " + controlenv.SocketVariable + " in this shell first")
	}
	// An empty address is not a hub. hubURLFor reports a *NoHubError, whose
	// hints state the flag and the variable that supply one; building a
	// client for the empty URL answered `hub url missing hostname` instead.
	hub, err := hubURLFor(inv.Command)
	if err != nil {
		return nil, err
	}
	// Anonymous fallback supports a solo hub's local IPC socket. A TCP address
	// needs a session. The hub enforces the admin gate, so a credential-less
	// call against any other connection answers unauthenticated.
	c, err := control.NewClientOrAnonymous(hub)
	if err != nil {
		return nil, cli.Wrap(cli.CodeNotLoggedIn, err, "")
	}
	if c.IsWorkerIPC() {
		return nil, cli.Usagef("admin commands talk to the hub directly and cannot run over the worker IPC transport")
	}
	return c, nil
}

// argInto builds an adminVerbSpec.BeforeDial that copies the positional
// argument name into value, and refuses an empty one. A verb body reads the
// variable, as it reads a flag value.
func argInto(value *string, name string) func(adminArgs) error {
	return func(a adminArgs) error {
		*value = strings.TrimSpace(a.Arg(name))
		if *value == "" {
			return cli.Usagef("<%s> is empty", name)
		}
		return nil
	}
}

// minListLimit is the smallest page a verb may ask for. The ceiling is
// contracts.MaxPageLimit — the page-limit contract that the hub caps at, so
// the range this check states is always the hub's own range.
const minListLimit = 1

// validateListLimit refuses a --limit outside the page range.
//
// The hub normalizes too (normalizePageParams in hub/service: a non-positive
// limit takes the default, an oversized one caps), which is what protects every
// other client. This check exists for the ANSWER an operator gets: it runs
// before the dial, so `--limit 0` identifies the flag and its range
// instead of quietly returning a page of a size the operator did not ask
// for.
func validateListLimit(limit int64) error {
	if limit < minListLimit || limit > contracts.MaxPageLimit {
		return cli.Usagef("limit must be between %d and %d", minListLimit, contracts.MaxPageLimit)
	}
	return nil
}

// settingValueJSON renders one SettingValue for the envelope.
func settingValueJSON(v *leapmuxv1.SettingValue) map[string]any {
	out := map[string]any{
		"key":            v.GetKey(),
		"customized":     v.GetCustomized(),
		"effective_json": jsonOrNull(v.GetEffectiveJson()),
	}
	if v.GetValueJson() != "" {
		out["value_json"] = jsonOrNull(v.GetValueJson())
	}
	putTime(out, "updated_at", v.GetUpdatedAt())
	if len(v.GetSecretSet()) > 0 {
		out["secret_set"] = v.GetSecretSet()
	}
	return out
}

func jsonOrNull(s string) any {
	if s == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	return v
}

// propagationName renders a key's propagation class for the operator.
//
// Every verb that reports one key states it, because it is the difference
// between a write that the running hub already applies and one that waits
// for a restart. Reporting it on `list` alone left `set` printing a
// success envelope identical to a hot key's while the hub kept the old
// value.
func propagationName(restart bool) string {
	if restart {
		return "restart"
	}
	return "hot"
}

// addPropagation states, on a write verb's reply, whether the running hub
// already applies the written value. `settings set`, `settings set-secret` and
// `settings reset` all call it, because a reset of a restart-class key also waits
// for a restart.
func addPropagation(out map[string]any, restart bool) {
	out["propagation"] = propagationName(restart)
	if restart {
		out["note_restart"] = "this setting applies after a hub restart"
	} else {
		out["note_hot"] = "the hub that stored this setting applies it at once; a value written to the shared database outside the hub reaches it within its 30-second settings snapshot"
	}
}

// settingDescriptionText appends the domain-verb pointer to the keys whose
// dedicated verb writes several of them in one transaction.
//
// A key that a cross-key rule ties to another key cannot be configured one
// write at a time: the hub answers `captcha.selected=turnstile requires its
// site key and secret to be configured first`, and only `captcha set`
// composes both halves in one UpdateSettings. The rate-limit keys read the
// same way. An operator who runs `settings list` learns the working verb
// there, instead of after a refusal.
func settingDescriptionText(key, summary string) string {
	switch {
	case slices.Contains(captchaSettingKeys(), key):
		return summary + " (prefer `leapmux control admin captcha ...`)"
	case strings.HasPrefix(key, ratelimit.SettingKeyPrefix):
		return summary + " (prefer `leapmux control admin rate-limit ...`)"
	}
	return summary
}

// newAdminSettingsList builds `control admin settings list`.
func newAdminSettingsList() *cobra.Command {
	return adminLeaf(adminVerbSpec{
		Use:   "list",
		Short: "List every setting with its effective value",
		Examples: []cli.Example{
			{Command: "leapmux control admin settings list"},
		},
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminSettingsService().ListSettings(a.Context(), &leapmuxv1.ListSettingsRequest{})
			if err != nil {
				return err
			}
			rows := make([]map[string]any, 0, len(resp.GetValues()))
			byDescr := map[string]*leapmuxv1.SettingDescriptor{}
			for _, d := range resp.GetDescriptors() {
				byDescr[d.GetKey()] = d
			}
			for _, v := range resp.GetValues() {
				row := settingValueJSON(v)
				if d := byDescr[v.GetKey()]; d != nil {
					row["propagation"] = propagationName(d.GetRestart())
					row["description"] = settingDescriptionText(d.GetKey(), d.GetSummary())
				}
				rows = append(rows, row)
			}
			return a.Result(map[string]any{"settings": rows})
		},
	})
}

// parseSettingValue accepts a JSON document or a bare scalar, quoting the
// scalar on the caller's behalf.
//
// A boolean spelling that `strconv.ParseBool` accepts is NORMALIZED to the
// JSON literal: `T`, `t`, `TRUE`, `True`, `False`, and the rest are not
// valid JSON, so passing them through verbatim made the hub answer with a
// decode error that specified a line the operator never typed.
//
// The numeric test runs FIRST, and deliberately so. `ParseBool` also
// accepts `1` and `0`, so testing it first turned `settings set
// <int_key> 1` into the boolean `true`. A bare `1` is far more likely to
// mean the number; an operator who wants the boolean writes `true`.
func parseSettingValue(raw string) (json.RawMessage, error) {
	if raw == "" {
		// The empty string is a legal value for some keys (public_url
		// accepts it), so the refusal states the two ways to reach it
		// rather than reading as "this key takes no empty value".
		return nil, errors.New(
			`<value> is empty; pass '""' to store the empty string, or run "leapmux control admin settings reset <key>" to restore the default`)
	}
	// A value that OPENS a document is a document. Falling through a
	// malformed one quoted it as a string, and the hub then answered with
	// a type error that never mentioned the unbalanced brace the operator
	// typed — or, for a string-valued key, accepted the malformed text and
	// stored it with no error at all.
	if strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[") {
		if !json.Valid([]byte(raw)) {
			return nil, fmt.Errorf("value starts with %q but is not valid JSON; check the brackets and the quoting", raw[:1])
		}
		return json.RawMessage(raw), nil
	}
	// ParseFloat also accepts NaN, Inf, Infinity, a hex float (0x1p-2), and
	// digit separators (1_000). None of those is JSON, so the json.Valid
	// test keeps them on the string path, where json.Marshal quotes them.
	if _, err := strconv.ParseFloat(raw, 64); err == nil && json.Valid([]byte(raw)) {
		return json.RawMessage(raw), nil
	}
	if v, err := strconv.ParseBool(raw); err == nil {
		return json.RawMessage(strconv.FormatBool(v)), nil
	}
	if json.Valid([]byte(raw)) {
		return json.RawMessage(raw), nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// newAdminSettingsGet builds `control admin settings get KEY`.
func newAdminSettingsGet() *cobra.Command {
	return adminLeaf(adminVerbSpec{
		Use:   "get <key>",
		Short: "Show one setting, its value and its shape",
		Examples: []cli.Example{
			{Command: "leapmux control admin settings get session_duration_seconds"},
		},
		Run: func(c *control.Client, a adminArgs) error {
			key := a.Rest[0]
			resp, err := c.AdminSettingsService().ListSettings(a.Context(), &leapmuxv1.ListSettingsRequest{})
			if err != nil {
				return err
			}
			for _, v := range resp.GetValues() {
				if v.GetKey() != key {
					continue
				}
				out := settingValueJSON(v)
				for _, d := range resp.GetDescriptors() {
					if d.GetKey() == key {
						out["propagation"] = propagationName(d.GetRestart())
						out["description"] = settingDescriptionText(d.GetKey(), d.GetSummary())
						out["value_schema"] = settingShapeFromDescriptor(d)
					}
				}
				return a.Result(out)
			}
			return cli.Usagef("unknown setting key %q (see `leapmux control admin settings list`)", key)
		},
	})
}

// settingShapeFromDescriptor derives the JSON shape hint from the
// descriptor's field schema.
//
// A scalar key's kind name comes from service.SettingFieldKindFromProto
// and settings.FieldKind.String, which own the wire-to-schema table and
// the spelling. Restating either already cost a drift: this surface
// printed "boolean" where the Go schema — and the golden account schema
// that pins it — say "bool".
//
// The two halves stay APART. `settings set KEY` merges the public names
// and `settings set-secret KEY` the secret ones, so a secret field listed
// among the public names sends an operator to the verb that refuses it.
func settingShapeFromDescriptor(d *leapmuxv1.SettingDescriptor) string {
	fields := d.GetFields()
	if len(fields) == 1 && fields[0].GetName() == "" {
		kind, ok := service.SettingFieldKindFromProto(fields[0].GetKind())
		if !ok {
			return "unknown"
		}
		return kind.String()
	}
	names := make([]string, 0, len(fields))
	secrets := make([]string, 0)
	for _, f := range fields {
		if f.GetSecret() {
			secrets = append(secrets, `"`+f.GetName()+`"`)
			continue
		}
		names = append(names, `"`+f.GetName()+`"`)
	}
	// A key whose every field is secret has no public half to print.
	shape := ""
	if len(names) > 0 {
		shape = "{" + strings.Join(names, ", ") + "}"
	}
	if len(secrets) > 0 {
		if shape != "" {
			shape += " + "
		}
		shape += "secret {" + strings.Join(secrets, ", ") + "}"
	}
	return shape
}

// newAdminSettingsSet builds `control admin settings set KEY VALUE`.
func newAdminSettingsSet() *cobra.Command {
	var value json.RawMessage
	return adminLeaf(adminVerbSpec{
		Use:   "set <key> <value>",
		Short: "Set the public value of a setting from JSON or a bare value",
		Examples: []cli.Example{
			{Command: "leapmux control admin settings set signup_enabled false"},
			{Comment: "Set two fields of a setting that holds a document.", Command: `leapmux control admin settings set smtp '{"host":"smtp.example.com","port":587}'`},
		},
		Mutates: true,
		BeforeDial: func(a adminArgs) error {
			// A malformed VALUE must answer with the value, not with a
			// connection error, so the coercion runs before the dial.
			parsed, err := parseSettingValue(a.Rest[1])
			if err != nil {
				return cli.Wrap(cli.CodeUsage, err, "")
			}
			value = parsed
			return nil
		},
		Run: func(c *control.Client, a adminArgs) error {
			updated, restart, err := updateOneSetting(a.Context(), c, &leapmuxv1.SettingWrite{
				Key: a.Rest[0], PartialJson: string(value),
			})
			if err != nil {
				return err
			}
			out := settingValueJSON(updated)
			if updated.GetEffectiveJson() != updated.GetValueJson() {
				out["note"] = "the effective value differs from the stored value; see effective_json"
			}
			// The write itself reports the propagation class, so this reply
			// already says whether the running hub applies the new value.
			// Reading it back through a second ListSettings serialized
			// every registered key to learn one boolean.
			addPropagation(out, restart)
			return a.Result(out)
		},
	})
}

// newAdminSettingsSetSecret builds `control admin settings set-secret KEY JSON`.
func newAdminSettingsSetSecret() *cobra.Command {
	return adminLeaf(adminVerbSpec{
		Use:   "set-secret <key> <json>",
		Short: "Set the secret fields of a setting; the hub stores them encrypted",
		Examples: []cli.Example{
			{Command: `leapmux control admin settings set-secret smtp '{"password":"..."}'`},
		},
		Mutates: true,
		BeforeDial: func(a adminArgs) error {
			// A DOCUMENT test BEFORE the validity test. The secret half
			// merges NAMED fields, so a bare scalar, an array, or a lone
			// quoted string can never be a legal partial here — and each of
			// them is valid JSON, so a validity test alone let them dial the
			// hub and come back as a decode error that specifies a Go type. This is
			// why the rule differs from parseSettingValue's, which accepts
			// an array for an array-valued key.
			raw := strings.TrimSpace(a.Rest[1])
			if !strings.HasPrefix(raw, "{") || !json.Valid([]byte(raw)) {
				return cli.Usagef(`<json> must be a JSON object of the secret fields, for example {"password":"..."}`)
			}
			return nil
		},
		Run: func(c *control.Client, a adminArgs) error {
			updated, restart, err := updateOneSetting(a.Context(), c, &leapmuxv1.SettingWrite{
				Key: a.Rest[0], SecretPartialJson: a.Rest[1],
			})
			if err != nil {
				return err
			}
			out := settingValueJSON(updated)
			addPropagation(out, restart)
			return a.Result(out)
		},
	})
}

// newAdminSettingsReset builds `control admin settings reset KEY`.
func newAdminSettingsReset() *cobra.Command {
	return adminLeaf(adminVerbSpec{
		Use:   "reset <key>",
		Short: "Restore the default value of a setting",
		Examples: []cli.Example{
			{Command: "leapmux control admin settings reset session_duration_seconds"},
		},
		Mutates: true,
		Run: func(c *control.Client, a adminArgs) error {
			value, restart, err := resetOneSetting(a.Context(), c, a.Rest[0])
			if err != nil {
				return err
			}
			out := settingValueJSON(value)
			addPropagation(out, restart)
			return a.Result(out)
		},
	})
}

// newAdminCommand builds `control admin`, the online, authenticated surface
// of hub administration. These leaves call the Admin*Service RPCs directly
// (never the worker-IPC bridge); the break-glass offline subset lives in
// `leapmux recover`.
func newAdminCommand() *cobra.Command {
	admin := cli.Group("admin", "Administer the hub; needs an administrator's login (offline repair: \"leapmux recover\")",
		cli.Group("settings", "Read and change the hub's settings: sign-in policy, SMTP, timeouts, limits",
			newAdminSettingsList(),
			newAdminSettingsGet(),
			newAdminSettingsSet(),
			newAdminSettingsSetSecret(),
			newAdminSettingsReset(),
		),
		cli.Group("user", "Manage users",
			newAdminUserList(),
			newAdminUserGet(),
			newAdminUserCreate(),
			newAdminUserUpdate(),
			newAdminUserDelete(),
			newAdminUserSetAdmin(true, "grant-admin <user>", "Make a user an administrator",
				[]cli.Example{{Command: "leapmux control admin user grant-admin alice"}}),
			newAdminUserSetAdmin(false, "revoke-admin <user>", "Take a user's administrator access away",
				[]cli.Example{{Command: "leapmux control admin user revoke-admin alice"}}),
			newAdminUserResetPassword(),
			newAdminUserListSessions(),
		),
		cli.Group("session", "List and revoke sign-in sessions",
			newAdminSessionList(),
			newAdminSessionRevoke(),
			newAdminSessionRevokeUser(),
			newAdminSessionPurgeExpired(),
		),
		cli.Group("worker", "Manage the workers of every user",
			newAdminWorkerList(),
			newAdminWorkerGet(),
			newAdminWorkerDeregister(),
			cli.Group("reg-key", "Manage worker registration keys",
				newAdminWorkerRegKeyList(),
				newAdminWorkerRegKeyRevoke(),
				newAdminWorkerRegKeyPurgeExpired(),
			),
		),
		// "app" is the OUTBOUND direction: a program this hub grants
		// access to. The inbound direction -- an identity provider the
		// hub signs users in WITH -- is `control admin idp`.
		//
		// The RPCs behind these verbs are NOT admin-only: an ordinary
		// user registers an app for themself through the same ones in
		// the preferences dialog, and ownership rather than a role
		// decides what each caller sees. They live here because
		// registering an app is an operator's job on a shared hub.
		cli.Group("app", "Manage app registrations: what a consent screen authorizes",
			newAdminAppList(),
			newAdminAppRegister(),
			newAdminAppUpdate(),
			newAdminAppSetVerified(true, "verify <app>", "Vouch for an app, so that its consent screen stops warning",
				[]cli.Example{{Command: "leapmux control admin app verify lmx_app_x1y2"}}),
			newAdminAppSetVerified(false, "unverify <app>", "Withdraw a vouch for an app",
				[]cli.Example{{Command: "leapmux control admin app unverify lmx_app_x1y2"}}),
			newAdminAppSetElevation(true, "allow-elevation <app>", "Let an app ask a user to prove a factor again",
				[]cli.Example{{Command: "leapmux control admin app allow-elevation lmx_app_x1y2"}}),
			newAdminAppSetElevation(false, "deny-elevation <app>", "Take that permission away; every open elevation ends at the next call",
				[]cli.Example{{Command: "leapmux control admin app deny-elevation lmx_app_x1y2"}}),
			newAdminAppRevoke(),
			newAdminAppDelete(),
		),
		// "idp" is the INBOUND direction: an identity provider the hub
		// signs users in with. The outbound direction -- an app the hub
		// grants access to -- is `control admin app`.
		cli.Group("idp", "Manage the OAuth and OIDC providers that users sign in with",
			newAdminOAuthProviderAdd(),
			newAdminOAuthProviderList(),
			newAdminOAuthProviderRemove(),
			newAdminOAuthProviderSetEnabled(true, "enable <idp>", "Enable a sign-in provider",
				[]cli.Example{{Command: "leapmux control admin idp enable 3hKd9s"}}),
			newAdminOAuthProviderSetEnabled(false, "disable <idp>", "Disable a sign-in provider",
				[]cli.Example{{Command: "leapmux control admin idp disable 3hKd9s"}}),
		),
		cli.Group("captcha", "Manage the bot protection: ALTCHA, reCAPTCHA v3 or Turnstile (a solo hub never asks)",
			newAdminCaptchaShow(),
			newAdminCaptchaSet(),
			newAdminCaptchaSetEnabled(true, "enable", "Turn captcha verification on",
				[]cli.Example{{Command: "leapmux control admin captcha enable"}}),
			newAdminCaptchaSetEnabled(false, "disable", "Turn captcha verification off; the honeypot check stays",
				[]cli.Example{{Command: "leapmux control admin captcha disable"}}),
			newAdminCaptchaReset(),
		),
		cli.Group("rate-limit", "Manage the rate limit of each user operation",
			newAdminRateLimitList(),
			newAdminRateLimitSet(),
			newAdminRateLimitSetEnabled(true, "enable <operation>", "Turn on the rate limit of an operation",
				[]cli.Example{{Command: "leapmux control admin rate-limit enable elevation"}}),
			newAdminRateLimitSetEnabled(false, "disable <operation>", "Turn off the rate limit of an operation",
				[]cli.Example{{Command: "leapmux control admin rate-limit disable elevation"}}),
			newAdminRateLimitReset(),
		),
		cli.Group("api-token", "Manage durable API tokens for scripts and integrations",
			newAdminAPITokenList(),
			newAdminAPITokenIssue(),
			newAdminAPITokenRevoke(),
		),
		cli.Group("delegation-token", "Manage the delegation tokens that workers mint",
			newAdminDelegationTokenList(),
			newAdminDelegationTokenRevoke(),
		),
	)
	cli.DescribeArgs(admin, adminArgHelp())
	return admin
}

// adminArgHelp describes the arguments of the admin verbs. An admin verb
// takes a full id, never a prefix or "self": it acts on the objects of every
// user, so a short form could select an object that the operator did not
// mean. The control tree describes its own targets after this, and a
// description given here wins over that one.
func adminArgHelp() map[string]string {
	operations := make([]string, 0, len(ratelimit.KnownOperations()))
	for _, op := range ratelimit.KnownOperations() {
		operations = append(operations, string(op))
	}
	return map[string]string{
		"user":      control.UserArgumentHelp,
		"worker":    `A worker id, as "leapmux control admin worker list" prints it.`,
		"reg-key":   `A registration key id, as "leapmux control admin worker reg-key list" prints it.`,
		"app":       `An app's client id, as "leapmux control admin app list" prints it.`,
		"idp":       `A sign-in provider id, as "leapmux control admin idp list" prints it.`,
		"session":   `A session id, as "leapmux control admin session list" prints it.`,
		"token":     `A token id, as the list command of this group prints it.`,
		"operation": "One of: " + strings.Join(operations, ", ") + ".",
		"key":       `A setting key, as "leapmux control admin settings list" prints it.`,
		"value":     `A JSON value. A word that is not JSON is stored as a string.`,
		"json":      `A JSON object of the secret fields, such as {"password":"..."}.`,
	}
}
