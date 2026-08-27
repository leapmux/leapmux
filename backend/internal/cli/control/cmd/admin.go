package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/ratelimit"
	"github.com/leapmux/leapmux/internal/hub/service"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// An admin call reports the hub's refusal VERBATIM, through
// control.EmitErrorWith, and adds nothing of its own.
//
// The CLI used to append a remedy of its own to any PermissionDenied when
// the local credential file recorded no admin scope. The file cannot decide
// that. The hub answers two different refusals here -- "administrator
// privileges are required" for an account that is not an administrator, and
// "this CLI credential was not granted hub administration; run `leapmux
// control auth login --admin` to mint one that was" for an administrator
// whose credential lacks the scope -- and each one already states its own
// remedy. Appending to both told a non-administrator to run a login that the
// hub refuses, and printed the same instruction twice on the refusal that
// was genuine.

// requireAdminClient is requireClient plus the admin exclusions: admin
// commands NEVER use the worker-IPC transport. They talk to the hub
// directly, because the worker's IPC bridge is a typing device, not a
// security boundary — anything registered there is callable by any
// worker-spawned agent, so no admin procedure is. Refusing the transport
// here (and again on the client, in case the env var leaked in some other
// way) keeps `control admin ...` out of agent reach entirely.
func requireAdminClient(hubFlag string) (*control.Client, error) {
	if os.Getenv("LEAPMUX_CONTROL_SOCK") != "" {
		return nil, control.EmitError("invalid_request",
			"admin commands talk to the hub directly; unset LEAPMUX_CONTROL_SOCK (or pass --hub) to run them")
	}
	// An empty address is not a hub. Without this the credential lookup
	// answers `hub url missing hostname` under the not_logged_in code,
	// which states neither the flag nor the variable that supplies one.
	if hubFlag == "" {
		return nil, control.EmitError("invalid_request",
			"no hub address; pass --hub <url> or set LEAPMUX_HUB. For a hub that needs a credential, run `leapmux control auth login --hub <url>` first.")
	}
	// Anonymous fallback: a solo hub authenticates every request as the
	// local user, and solo cannot complete a login flow at all — the hub
	// enforces the admin gate, so a credential-less call against a
	// non-solo hub simply answers unauthenticated.
	c, err := control.NewClientOrAnonymous(hubFlag)
	if err != nil {
		return nil, control.EmitErrorWith("not_logged_in", err)
	}
	if c.IsWorkerIPC() {
		return nil, control.EmitError("invalid_request",
			"admin commands talk to the hub directly and cannot run over the worker IPC transport")
	}
	return c, nil
}

// requireFlag builds an adminVerbSpec.BeforeDial that refuses an empty
// required flag, in one wording for every verb. It takes the address of
// the flag variable, because the value arrives only at parse time.
func requireFlag(value *string, name string) func(adminArgs) error {
	return func(adminArgs) error {
		if *value == "" {
			return control.EmitError("invalid_request", "--"+name+" is required")
		}
		return nil
	}
}

// minListLimit is the smallest page a verb may ask for. The ceiling is
// service.MaxPageLimit — the same constant the hub caps at, so the
// range this check states is always the hub's own range.
const minListLimit = 1

// validateListLimit refuses a --limit outside the page range.
//
// The hub normalizes too (service.NormalizePageParams: a non-positive limit
// takes the default, an oversized one caps), which is what protects every
// other client. This check exists for the ANSWER an operator gets: it runs
// before the dial, so `--limit 0` identifies the flag and its range
// instead of quietly returning a page of a size the operator did not ask
// for.
func validateListLimit(limit int64) error {
	if limit < minListLimit || limit > service.MaxPageLimit {
		return control.EmitError("invalid_request",
			fmt.Sprintf("limit must be between %d and %d", minListLimit, service.MaxPageLimit))
	}
	return nil
}

// putTime writes one optional timestamp into an output row, omitting the
// field when the hub sent none.
func putTime(row map[string]any, key string, ts *timestamppb.Timestamp) {
	if ts == nil {
		return
	}
	row[key] = ts.AsTime().UTC().Format(timeFormat)
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

const timeFormat = "2006-01-02T15:04:05.000Z07:00"

// RunAdminSettingsList implements `control admin settings list`.
func RunAdminSettingsList(rawCtx any, args []string) error {
	return adminVerb(rawCtx, args, adminVerbSpec{
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminSettingsService().ListSettings(context.Background(), connect.NewRequest(&leapmuxv1.ListSettingsRequest{}))
			if err != nil {
				return control.EmitErrorWith("rpc_failed", err)
			}
			rows := make([]map[string]any, 0, len(resp.Msg.GetValues()))
			byDescr := map[string]*leapmuxv1.SettingDescriptor{}
			for _, d := range resp.Msg.GetDescriptors() {
				byDescr[d.GetKey()] = d
			}
			for _, v := range resp.Msg.GetValues() {
				row := settingValueJSON(v)
				if d := byDescr[v.GetKey()]; d != nil {
					row["propagation"] = propagationName(d.GetRestart())
					row["description"] = settingDescriptionText(d.GetKey(), d.GetSummary())
				}
				rows = append(rows, row)
			}
			return control.EmitData(rows)
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
			"value is required; pass '\"\"' to store the empty string, or run `settings reset KEY` to restore the default")
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

// RunAdminSettingsGet implements `control admin settings get KEY`.
func RunAdminSettingsGet(rawCtx any, args []string) error {
	return adminVerb(rawCtx, args, adminVerbSpec{
		Positionals: 1,
		Usage:       "usage: leapmux control admin settings get KEY",
		Run: func(c *control.Client, a adminArgs) error {
			key := a.Rest[0]
			resp, err := c.AdminSettingsService().ListSettings(context.Background(), connect.NewRequest(&leapmuxv1.ListSettingsRequest{}))
			if err != nil {
				return control.EmitErrorWith("rpc_failed", err)
			}
			for _, v := range resp.Msg.GetValues() {
				if v.GetKey() != key {
					continue
				}
				out := settingValueJSON(v)
				for _, d := range resp.Msg.GetDescriptors() {
					if d.GetKey() == key {
						out["propagation"] = propagationName(d.GetRestart())
						out["description"] = settingDescriptionText(d.GetKey(), d.GetSummary())
						out["value_schema"] = settingShapeFromDescriptor(d)
					}
				}
				return control.EmitData(out)
			}
			return control.EmitError("invalid_request", fmt.Sprintf("unknown setting key %q (see `leapmux control admin settings list`)", key))
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

// RunAdminSettingsSet implements `control admin settings set KEY VALUE`.
func RunAdminSettingsSet(rawCtx any, args []string) error {
	var value json.RawMessage
	return adminVerb(rawCtx, args, adminVerbSpec{
		Positionals: 2,
		Usage:       "usage: leapmux control admin settings set KEY VALUE",
		BeforeDial: func(a adminArgs) error {
			// A malformed VALUE must answer with the value, not with a
			// connection error, so the coercion runs before the dial.
			parsed, err := parseSettingValue(a.Rest[1])
			if err != nil {
				return control.EmitErrorWith("invalid_request", err)
			}
			value = parsed
			return nil
		},
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminSettingsService().UpdateSetting(context.Background(), connect.NewRequest(&leapmuxv1.UpdateSettingRequest{
				Key: a.Rest[0], PartialJson: string(value),
			}))
			if err != nil {
				return control.EmitErrorWith("update_failed", err)
			}
			out := settingValueJSON(resp.Msg.GetValue())
			if resp.Msg.GetValue().GetEffectiveJson() != resp.Msg.GetValue().GetValueJson() {
				out["note"] = "the effective value differs from the stored value; see effective_json"
			}
			// The write itself reports the propagation class, so this reply
			// already says whether the running hub applies the new value.
			// Reading it back through a second ListSettings serialized
			// every registered key to learn one boolean.
			out["propagation"] = propagationName(resp.Msg.GetRestart())
			if resp.Msg.GetRestart() {
				out["note_restart"] = "this setting applies after a hub restart"
			} else {
				out["note_hot"] = "the hub that stored this setting applies it at once; other hub instances that share the database apply it within 30 seconds"
			}
			return control.EmitData(out)
		},
	})
}

// RunAdminSettingsSetSecret implements `control admin settings set-secret KEY JSON`.
func RunAdminSettingsSetSecret(rawCtx any, args []string) error {
	return adminVerb(rawCtx, args, adminVerbSpec{
		Positionals: 2,
		Usage:       "usage: leapmux control admin settings set-secret KEY JSON",
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
				return control.EmitError("invalid_request",
					`VALUE must be a JSON document that specifies the secret fields, for example {"password":"..."}`)
			}
			return nil
		},
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminSettingsService().UpdateSettingSecret(context.Background(), connect.NewRequest(&leapmuxv1.UpdateSettingSecretRequest{
				Key: a.Rest[0], PartialJson: a.Rest[1],
			}))
			if err != nil {
				return control.EmitErrorWith("update_failed", err)
			}
			return control.EmitData(settingValueJSON(resp.Msg.GetValue()))
		},
	})
}

// RunAdminSettingsReset implements `control admin settings reset KEY`.
func RunAdminSettingsReset(rawCtx any, args []string) error {
	return adminVerb(rawCtx, args, adminVerbSpec{
		Positionals: 1,
		Usage:       "usage: leapmux control admin settings reset KEY",
		Run: func(c *control.Client, a adminArgs) error {
			resp, err := c.AdminSettingsService().ResetSetting(context.Background(), connect.NewRequest(&leapmuxv1.ResetSettingRequest{
				Key: a.Rest[0],
			}))
			if err != nil {
				return control.EmitErrorWith("reset_failed", err)
			}
			return control.EmitData(settingValueJSON(resp.Msg.GetValue()))
		},
	})
}
