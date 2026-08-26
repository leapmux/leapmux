package cmd

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"slices"
	"strings"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/captcha"
	"github.com/leapmux/leapmux/internal/hub/ratelimit"
)

// The captcha and rate-limit verbs are client-side sugar over
// AdminSettingsService: each composes the partial JSON documents for one
// or more settings keys. There are no dedicated server RPCs — the settings
// surface IS the API, and these verbs exist so operators do not have to
// hand-write the documents.
//
// A verb that touches SEVERAL keys uses UpdateSettings, which applies them
// in one transaction with the cross-key rules run once. A sequence of
// single-key writes cannot express that: each is validated against the
// state the previous one left, so the order has to keep every intermediate
// state legal, and a failure part-way leaves stored state the command
// reported it never reached.

// updateSettingJSON is the shared body: marshal a value and merge it onto
// one settings key.
//
// The two callers below stay separate one-line entry points on purpose.
// Collapsing them into a single `any` parameter would erase the
// compile-time difference between a partial DOCUMENT (a map of field
// names) and a whole SCALAR value — and that mistake does not fail at the
// call site, it travels to the hub and comes back as a merge error.
func updateSettingJSON(c *control.Client, key string, value any) (*leapmuxv1.SettingValue, error) {
	doc, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	resp, err := c.AdminSettingsService().UpdateSetting(context.Background(), connect.NewRequest(&leapmuxv1.UpdateSettingRequest{
		Key: key, PartialJson: string(doc),
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetValue(), nil
}

// adminUpdateSetting merges one partial document onto one settings key.
func adminUpdateSetting(c *control.Client, key string, partial map[string]any) (*leapmuxv1.SettingValue, error) {
	return updateSettingJSON(c, key, partial)
}

// adminUpdateSettingScalar writes a scalar key's whole value. The type
// parameter is what makes the split above real: a map does not satisfy it,
// so the merge error the comment describes cannot be written here.
func adminUpdateSettingScalar[T bool | int64 | float64 | string](c *control.Client, key string, value T) (*leapmuxv1.SettingValue, error) {
	return updateSettingJSON(c, key, value)
}

// No single-key secret helper lives here. The one verb that writes a
// secret (`captcha set`) also writes the row it belongs to, and both
// halves travel in ONE UpdateSettings write so they land together or not
// at all. `settings set-secret` calls UpdateSettingSecret directly.

// defaultIfZero substitutes a family default for a value the operator
// passed as 0.
//
// An explicit 0 means "restore the default" on every captcha and
// rate-limit tuning flag, because it is the one spelling an operator can
// type without first looking the number up. Storing the 0 verbatim
// answered with the key's own range check -- `captcha cost (PBKDF2
// iterations) must be between 10000 and 1000000 (got 0)` -- which reads as
// a refusal of the flag rather than of the value.
func defaultIfZero[T int64 | float64](passed, def T) T {
	if passed == 0 {
		return def
	}
	return passed
}

// captchaSettingKeys names every captcha settings key, taken from the same
// descriptor list that the hub registers. `captcha show` and `captcha
// reset` both need the whole set, and deriving it here means a new captcha
// key reaches both verbs without a second list to keep in step.
func captchaSettingKeys() []string {
	descriptors := captcha.SettingsDescriptors()
	keys := make([]string, 0, len(descriptors))
	for _, d := range descriptors {
		keys = append(keys, d.Name())
	}
	return keys
}

func RunAdminCaptchaShow(rawCtx any, args []string) error {
	return adminVerb(rawCtx, args, adminVerbSpec{
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminSettingsService().ListSettings(context.Background(), connect.NewRequest(&leapmuxv1.ListSettingsRequest{}))
			if err != nil {
				return adminRPCError(c, "rpc_failed", err)
			}
			wanted := captchaSettingKeys()
			values := map[string]any{}
			for _, v := range resp.Msg.GetValues() {
				if slices.Contains(wanted, v.GetKey()) {
					values[v.GetKey()] = settingValueJSON(v)
				}
			}
			return control.EmitData(values)
		},
	})
}

// captchaFlagProviders restricts each tuning flag to the providers that
// own it. A flag whose owner is not the target is refused, never applied
// to a different key and never dropped: a cost meant for ALTCHA must not
// reach Turnstile, and a site key meant for Turnstile must not vanish
// under ALTCHA.
var captchaFlagProviders = map[string][]string{
	"algorithm":   {captcha.ProviderAlias(captcha.ProviderAltcha)},
	"cost":        {captcha.ProviderAlias(captcha.ProviderAltcha)},
	"memory-cost": {captcha.ProviderAlias(captcha.ProviderAltcha)},
	"parallelism": {captcha.ProviderAlias(captcha.ProviderAltcha)},
	"expires":     {captcha.ProviderAlias(captcha.ProviderAltcha)},
	"site-key":    {captcha.ProviderAlias(captcha.ProviderRecaptchaV3), captcha.ProviderAlias(captcha.ProviderTurnstile)},
	"min-score":   {captcha.ProviderAlias(captcha.ProviderRecaptchaV3)},
	"secret":      {captcha.ProviderAlias(captcha.ProviderRecaptchaV3), captcha.ProviderAlias(captcha.ProviderTurnstile)},
}

// refuseProviderForeignFlags rejects each passed flag whose owning
// provider is not the target. Refusing beats ignoring: a cost meant for
// ALTCHA silently dropped under Turnstile, and a secret meant for
// Turnstile silently REWROTE the ALTCHA signing key, which fails every
// challenge already issued to a browser.
func refuseProviderForeignFlags(a adminArgs, target captcha.Provider) error {
	alias := captcha.ProviderAlias(target)
	for _, name := range slices.Sorted(maps.Keys(captchaFlagProviders)) {
		if !a.Passed(name) || slices.Contains(captchaFlagProviders[name], alias) {
			continue
		}
		return control.EmitError("invalid_request", fmt.Sprintf(
			"--%s applies only to %s; the target provider is %s",
			name, strings.Join(captchaFlagProviders[name], " and "), alias))
	}
	return nil
}

// captchaState is one read of the hub's captcha configuration: which
// provider is selected, which of the external providers already hold a
// complete key pair, and which ALTCHA algorithm family is stored.
//
// They travel together because `captcha set` needs all three before it
// writes anything — see its own refusal, and the zero-restores-the-default
// substitution — and one ListSettings answers every question.
type captchaState struct {
	selected captcha.Provider
	// siteKeySet and secretSet report, per provider key, whether the
	// stored row already carries that half.
	siteKeySet map[string]bool
	secretSet  map[string]bool
	// altchaAlgorithm is the stored ALTCHA family. A tuning flag passed as
	// 0 restores the default, and the defaults belong to the family.
	altchaAlgorithm string
}

func readCaptchaState(c *control.Client) (captchaState, error) {
	out := captchaState{
		selected:        captcha.ProviderAltcha,
		siteKeySet:      map[string]bool{},
		secretSet:       map[string]bool{},
		altchaAlgorithm: captcha.DefaultAltchaSettings().Algorithm,
	}
	resp, err := c.AdminSettingsService().ListSettings(context.Background(), connect.NewRequest(&leapmuxv1.ListSettingsRequest{}))
	if err != nil {
		return out, err
	}
	found := false
	for _, v := range resp.Msg.GetValues() {
		if v.GetKey() == captcha.CaptchaSelectedKey.Name() {
			var alias string
			if err := json.Unmarshal([]byte(v.GetEffectiveJson()), &alias); err != nil {
				continue
			}
			p, err := captcha.ParseProvider(alias)
			if err != nil {
				continue
			}
			out.selected = p
			found = true
			continue
		}
		// ANY stored secret field counts, because each provider names its
		// own: ALTCHA signs with `hmac_key` and the external providers
		// verify with `secret_key`. Asking for one field name reported
		// ALTCHA as unconfigured whatever it held.
		for _, set := range v.GetSecretSet() {
			if set {
				out.secretSet[v.GetKey()] = true
				break
			}
		}
		// A scalar key (captcha.enabled) decodes into neither field, so a
		// decode failure is expected and simply leaves the row unreported.
		var row struct {
			SiteKey   string `json:"site_key"`
			Algorithm string `json:"algorithm"`
		}
		if err := json.Unmarshal([]byte(v.GetEffectiveJson()), &row); err != nil {
			continue
		}
		out.siteKeySet[v.GetKey()] = row.SiteKey != ""
		if v.GetKey() == captcha.AltchaKey.Name() && row.Algorithm != "" {
			out.altchaAlgorithm = row.Algorithm
		}
	}
	if !found {
		// A hub that does not report the key at all (solo mode omits every
		// captcha key) has no selection to tune.
		return out, fmt.Errorf("this hub reports no captcha configuration")
	}
	return out, nil
}

// RunAdminCaptchaSet implements `control admin captcha set`. The
// provider selection and the enable switch ride the public half; each
// provider's secret rides the secret half.
//
// The provider's row, its secret, and the selection travel in ONE
// UpdateSettings. The hub merges every key first and runs the cross-key
// rule (SelectedConfigured) ONCE over the whole result, so the order of the
// writes below carries no meaning; UpdateMany sorts them by key name to
// take the row locks in one canonical order.
func RunAdminCaptchaSet(rawCtx any, args []string) error {
	var provider, algorithm, siteKey, secret string
	var minScore float64
	var cost, memoryCost, parallelism, expires int64
	// An explicit --provider settles the target without asking the hub, so
	// BeforeDial can check every provider-owned flag: a typo answers with
	// the typo, not with a connection error.
	var explicitTarget *captcha.Provider
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&provider, "provider", "", "provider alias (altcha, recaptcha_v3, turnstile); omit to tune the active provider")
			fs.StringVar(&algorithm, "algorithm", "", "ALTCHA algorithm")
			fs.Int64Var(&cost, "cost", 0, "algorithm cost parameter; 0 restores the default")
			fs.Int64Var(&memoryCost, "memory-cost", 0, "algorithm memory cost; 0 restores the default")
			fs.Int64Var(&parallelism, "parallelism", 0, "algorithm parallelism; 0 restores the default")
			fs.Int64Var(&expires, "expires", 0, "challenge expiry seconds; 0 restores the default")
			fs.StringVar(&siteKey, "site-key", "", "provider site key")
			fs.StringVar(&secret, "secret", "", "provider secret (stored encrypted)")
			fs.Float64Var(&minScore, "min-score", 0, "recaptcha_v3 minimum score (0 < v <= 1); 0 restores the default")
		},
		BeforeDial: func(a adminArgs) error {
			// --hub selects the hub; it changes nothing, so an invocation
			// that carries it alone is still an empty one.
			if !a.AnyPassed() {
				return control.EmitError("invalid_request", "nothing to set (pass --provider or a tuning flag)")
			}
			// Neither half of a key pair may be passed empty. An empty
			// secret fails every verification, and an empty site key both
			// fails verification and defeats the completeness check below —
			// which asks whether the flag was PASSED, so `--site-key ""`
			// would satisfy it and still store nothing usable.
			if a.Passed("secret") && secret == "" {
				return control.EmitError("invalid_request", "--secret must not be empty; an empty secret fails every verification")
			}
			if a.Passed("site-key") && siteKey == "" {
				return control.EmitError("invalid_request", "--site-key must not be empty; an empty site key fails every verification")
			}
			if a.Passed("algorithm") {
				if err := captcha.ValidateAltchaAlgorithm(algorithm); err != nil {
					return control.EmitErrorWith("invalid_request", err)
				}
			}
			if !a.Passed("provider") {
				return nil
			}
			p, err := captcha.ParseProvider(provider)
			if err != nil {
				return control.EmitErrorWith("invalid_request", err)
			}
			if err := refuseProviderForeignFlags(a, p); err != nil {
				return err
			}
			explicitTarget = &p
			return nil
		},
		Run: func(c *control.Client, a adminArgs) error {
			state, err := readCaptchaState(c)
			if err != nil {
				return adminRPCError(c, "rpc_failed", err)
			}
			current := state.selected
			target := current
			if explicitTarget != nil {
				target = *explicitTarget
			}
			// ONE statement of the rule, over the settled target. The
			// pre-dial call in BeforeDial is the early answer for an
			// explicit --provider; this one covers the target the hub
			// resolved, and the check is a pure predicate, so running it
			// twice costs nothing and changes nothing.
			if err := refuseProviderForeignFlags(a, target); err != nil {
				return err
			}
			switching := target != current

			// The stored row must already hold every half this invocation
			// does not pass. The hub refuses the same state, but it answers
			// with the KEY -- not with the flags the operator has to add --
			// and only after the write has travelled. ALTCHA is exempt: its
			// row self-provisions on first use.
			if target != captcha.ProviderAltcha {
				row := captcha.DescriptorFor(target).Name()
				var missing, flags []string
				if !a.Passed("site-key") && !state.siteKeySet[row] {
					missing, flags = append(missing, "site key"), append(flags, "--site-key")
				}
				if !a.Passed("secret") && !state.secretSet[row] {
					missing, flags = append(missing, "secret"), append(flags, "--secret")
				}
				if len(missing) > 0 {
					return control.EmitError("invalid_request", fmt.Sprintf(
						"%s has no stored %s; pass %s in this invocation",
						captcha.ProviderAlias(target),
						strings.Join(missing, " and "), strings.Join(flags, " and ")))
				}
			}

			// A tuning flag passed as 0 restores the default of the family
			// this invocation LEAVES IN PLACE: the one --algorithm names
			// when it is passed, and the stored one otherwise.
			alg := state.altchaAlgorithm
			if a.Passed("algorithm") {
				alg = algorithm
			}
			family, err := captcha.DefaultAltchaSettingsFor(alg)
			if err != nil {
				// Only a STORED algorithm can fail here, because BeforeDial
				// already refused a passed one this build cannot derive
				// with. Reporting the stored value is the hub's job, so fall
				// back to the shared defaults and let a passed 0 restore a
				// legal value.
				family = captcha.DefaultAltchaSettings()
			}

			// ONE atomic write. Every key this invocation touches travels
			// in a single UpdateSettings, so the hub merges them all,
			// validates the cross-key rules ONCE over the whole result,
			// and stores them in one transaction.
			//
			// A sequence of single-key writes could not do this. Each write
			// was validated against the state the previous one left, so the
			// order had to be chosen to keep every intermediate state legal
			// — and a failure part-way left the hub in a state the command
			// reported it never reached. Re-keying the SELECTED provider
			// was the worst of it: a new site key could go live beside the
			// old secret, failing every verification.
			writes := []*leapmuxv1.SettingWrite{}
			// addWrite takes already-marshalled halves, because the two
			// shapes are not interchangeable: an OBJECT-shaped key takes a
			// map of field names, and a SCALAR key takes the bare value.
			// Passing a map for a scalar produces `{"": v}`, which the
			// key's typed decode refuses.
			addWrite := func(key string, public, secret json.RawMessage) {
				if len(public) == 0 && len(secret) == 0 {
					return
				}
				writes = append(writes, &leapmuxv1.SettingWrite{
					Key:               key,
					PartialJson:       string(public),
					SecretPartialJson: string(secret),
				})
			}
			marshal := func(v any) (json.RawMessage, error) { return json.Marshal(v) }

			// The target provider's row: both halves in ONE write, so a
			// site key and its secret land together or not at all.
			doc := map[string]any{}
			if a.Passed("algorithm") {
				// Only the algorithm travels. The key's own reconciler
				// (captcha.normalizeAltchaFamily, run inside the partial
				// merge) resets the family-specific parameters, so every
				// client gets the same behaviour and an explicit flag below
				// still wins.
				doc["algorithm"] = algorithm
			}
			if a.Passed("cost") {
				doc["cost"] = defaultIfZero(cost, family.Cost)
			}
			if a.Passed("memory-cost") {
				// The PBKDF2 and SHA families really do default to 0 here,
				// so this substitution stores 0 for them.
				doc["memory_cost"] = defaultIfZero(memoryCost, family.MemoryCost)
			}
			if a.Passed("parallelism") {
				doc["parallelism"] = defaultIfZero(parallelism, family.Parallelism)
			}
			if a.Passed("expires") {
				doc["challenge_expiry_seconds"] = defaultIfZero(expires, captcha.DefaultAltchaSettings().ChallengeExpirySeconds)
			}
			if a.Passed("site-key") {
				doc["site_key"] = siteKey
			}
			if a.Passed("min-score") {
				doc["min_score"] = defaultIfZero(minScore, captcha.DefaultRecaptchaV3Settings().MinScore)
			}
			secretDoc := map[string]any{}
			if a.Passed("secret") {
				secretField := "secret_key"
				if target == captcha.ProviderAltcha {
					secretField = "hmac_key"
				}
				secretDoc[secretField] = secret
			}
			var publicJSON, secretJSON json.RawMessage
			if len(doc) > 0 {
				if publicJSON, err = marshal(doc); err != nil {
					return adminRPCError(c, "captcha_set_failed", err)
				}
			}
			if len(secretDoc) > 0 {
				if secretJSON, err = marshal(secretDoc); err != nil {
					return adminRPCError(c, "captcha_set_failed", err)
				}
			}
			addWrite(captcha.DescriptorFor(target).Name(), publicJSON, secretJSON)

			// The selection, and with it the enable switch. Choosing a
			// provider means running it: a switch re-enables verification,
			// so a hub disabled for debugging does not silently stay
			// undefended through a provider change. In-place tuning leaves
			// the switch alone.
			if switching {
				alias, err := marshal(captcha.ProviderAlias(target))
				if err != nil {
					return adminRPCError(c, "captcha_set_failed", err)
				}
				enabled, err := marshal(true)
				if err != nil {
					return adminRPCError(c, "captcha_set_failed", err)
				}
				addWrite(captcha.CaptchaSelectedKey.Name(), alias, nil)
				addWrite(captcha.CaptchaEnabledKey.Name(), enabled, nil)
			}
			if len(writes) == 0 {
				return control.EmitError("invalid_request", "nothing to set (pass --provider or a tuning flag)")
			}
			if _, err := c.AdminSettingsService().UpdateSettings(context.Background(),
				connect.NewRequest(&leapmuxv1.UpdateSettingsRequest{Writes: writes})); err != nil {
				return adminRPCError(c, "captcha_set_failed", err)
			}
			return control.EmitData(map[string]any{
				"updated":   true,
				"provider":  captcha.ProviderAlias(target),
				"activated": switching,
			})
		},
	})
}

// RunAdminCaptchaSetEnabled implements `control admin captcha enable|disable`.
func RunAdminCaptchaSetEnabled(rawCtx any, args []string, enabled bool) error {
	return adminVerb(rawCtx, args, adminVerbSpec{
		Run: func(c *control.Client, _ adminArgs) error {
			if _, err := adminUpdateSettingScalar(c, captcha.CaptchaEnabledKey.Name(), enabled); err != nil {
				return adminRPCError(c, "captcha_set_failed", err)
			}
			return control.EmitData(map[string]any{captcha.CaptchaEnabledKey.Name(): enabled})
		},
	})
}

// RunAdminCaptchaReset implements `control admin captcha reset
// [--provider X]` — omit the flag to reset every captcha key.
func RunAdminCaptchaReset(rawCtx any, args []string) error {
	var provider string
	var target *captcha.Provider
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&provider, "provider", "", "reset one provider's row (omit = all captcha settings)")
		},
		BeforeDial: func(adminArgs) error {
			if provider == "" {
				return nil
			}
			p, err := captcha.ParseProvider(provider)
			if err != nil {
				return control.EmitErrorWith("invalid_request", err)
			}
			target = &p
			return nil
		},
		Run: func(c *control.Client, _ adminArgs) error {
			keys := captchaSettingKeys()
			if target != nil {
				keys = []string{captcha.DescriptorFor(*target).Name()}
				// Resetting the row of the SELECTED external provider alone
				// would leave a selection whose row has no keys, which the
				// cross-key rule refuses. Return the selection to its default
				// in the SAME request, so the state that rule sees is legal.
				state, err := readCaptchaState(c)
				if err != nil {
					return adminRPCError(c, "rpc_failed", err)
				}
				if state.selected == *target && *target != captcha.ProviderAltcha {
					keys = append([]string{captcha.CaptchaSelectedKey.Name()}, keys...)
				}
			}
			// ONE transaction. The hub clears every key together and runs
			// the cross-key rules once over the whole result, so no order
			// can make an intermediate state illegal and no refusal can
			// leave part of the set already cleared. The loop this replaced
			// destroyed the selection and two provider rows before
			// answering that it had reset nothing.
			resp, err := c.AdminSettingsService().ResetSettings(context.Background(),
				connect.NewRequest(&leapmuxv1.ResetSettingsRequest{Keys: keys}))
			if err != nil {
				return adminRPCError(c, "reset_failed", err)
			}
			reset := make([]string, 0, len(resp.Msg.GetValues()))
			for _, v := range resp.Msg.GetValues() {
				reset = append(reset, v.GetKey())
			}
			return control.EmitData(map[string]any{"reset": reset})
		},
	})
}

// rateLimitOperation resolves the --operation flag to its settings key.
// The catalogue is the authority, so a typo answers with the known names
// instead of travelling to the hub as an unknown settings key.
func rateLimitOperation(op string) (string, error) {
	known := ratelimit.KnownOperations()
	names := make([]string, 0, len(known))
	for _, k := range known {
		names = append(names, string(k))
	}
	if op == "" {
		return "", fmt.Errorf("--operation is required (known: %s)", strings.Join(names, ", "))
	}
	key, ok := ratelimit.LimitKey(ratelimit.Operation(op))
	if !ok {
		return "", fmt.Errorf("unknown operation %q (known: %s)", op, strings.Join(names, ", "))
	}
	return key.Name(), nil
}

// rateLimitTarget carries the --operation flag of the three verbs that
// address one operation, and the settings key that it resolves to.
//
// The flag and its resolution travel together because the catalogue lookup
// has to happen before the dial: a typo must answer with the known
// operation names, not with a connection error.
type rateLimitTarget struct {
	operation string
	// Key is the settings key of the operation. resolve fills it, so Run
	// reads it and never repeats the lookup.
	Key string
}

func (t *rateLimitTarget) bind(fs *flag.FlagSet, usage string) {
	fs.StringVar(&t.operation, "operation", "", usage)
}

// resolve matches adminVerbSpec.BeforeDial, so a verb assigns it directly.
func (t *rateLimitTarget) resolve(adminArgs) error {
	key, err := rateLimitOperation(t.operation)
	if err != nil {
		return control.EmitError("invalid_request", err.Error())
	}
	t.Key = key
	return nil
}

func RunAdminRateLimitList(rawCtx any, args []string) error {
	return adminVerb(rawCtx, args, adminVerbSpec{
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminSettingsService().ListSettings(context.Background(), connect.NewRequest(&leapmuxv1.ListSettingsRequest{}))
			if err != nil {
				return adminRPCError(c, "rpc_failed", err)
			}
			rows := make([]map[string]any, 0)
			for _, v := range resp.Msg.GetValues() {
				if strings.HasPrefix(v.GetKey(), ratelimit.SettingKeyPrefix) {
					rows = append(rows, settingValueJSON(v))
				}
			}
			return control.EmitData(rows)
		},
	})
}

func RunAdminRateLimitSet(rawCtx any, args []string) error {
	var target rateLimitTarget
	var maxAttempts, window int64
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
			target.bind(fs, "operation to limit (elevation)")
			fs.Int64Var(&maxAttempts, "max-attempts", 0, "failed attempts allowed per window (1-1000); 0 restores the default")
			fs.Int64Var(&window, "window", 0, "window seconds (60-86400); 0 restores the default")
		},
		BeforeDial: func(a adminArgs) error {
			if err := target.resolve(a); err != nil {
				return err
			}
			if !a.Passed("max-attempts") && !a.Passed("window") {
				return control.EmitError("invalid_request", "pass --max-attempts, --window, or both")
			}
			return nil
		},
		Run: func(c *control.Client, a adminArgs) error {
			// Tune one field or both; the partial merge keeps the other.
			// `enabled` is NOT written here — `rate-limit enable|disable` owns
			// the switch, so adjusting a window cannot silently re-arm a
			// limiter an operator deliberately turned off.
			//
			// A field passed as 0 restores the operation's catalogue
			// default. target.resolve already proved the operation is
			// catalogued, so the lookup below always finds it.
			def, _ := ratelimit.DefaultLimits(ratelimit.Operation(target.operation))
			doc := map[string]any{}
			if a.Passed("max-attempts") {
				doc["max_attempts"] = defaultIfZero(maxAttempts, def.MaxAttempts)
			}
			if a.Passed("window") {
				doc["window_seconds"] = defaultIfZero(window, def.WindowSeconds)
			}
			value, err := adminUpdateSetting(c, target.Key, doc)
			if err != nil {
				return adminRPCError(c, "rate_limit_set_failed", err)
			}
			return control.EmitData(settingValueJSON(value))
		},
	})
}

func RunAdminRateLimitSetEnabled(rawCtx any, args []string, enabled bool) error {
	var target rateLimitTarget
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
			target.bind(fs, "operation to limit (elevation)")
		},
		BeforeDial: target.resolve,
		Run: func(c *control.Client, _ adminArgs) error {
			value, err := adminUpdateSetting(c, target.Key, map[string]any{"enabled": enabled})
			if err != nil {
				return adminRPCError(c, "rate_limit_set_failed", err)
			}
			return control.EmitData(settingValueJSON(value))
		},
	})
}

func RunAdminRateLimitReset(rawCtx any, args []string) error {
	var target rateLimitTarget
	return adminVerb(rawCtx, args, adminVerbSpec{
		Flags: func(fs *flag.FlagSet) {
			target.bind(fs, "operation to reset (elevation)")
		},
		BeforeDial: target.resolve,
		Run: func(c *control.Client, _ adminArgs) error {
			resp, err := c.AdminSettingsService().ResetSetting(context.Background(), connect.NewRequest(&leapmuxv1.ResetSettingRequest{Key: target.Key}))
			if err != nil {
				return adminRPCError(c, "reset_failed", err)
			}
			return control.EmitData(settingValueJSON(resp.Msg.GetValue()))
		},
	})
}
