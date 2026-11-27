package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/captcha"
	"github.com/leapmux/leapmux/internal/hub/ratelimit"
)

func defaultIfZero[T int64 | float64](passed, def T) T {
	if passed == 0 {
		return def
	}
	return passed
}

func captchaSettingKeys() []string {
	descriptors := captcha.SettingsDescriptors()
	keys := make([]string, 0, len(descriptors))
	for _, d := range descriptors {
		keys = append(keys, d.Name())
	}
	return keys
}

func newAdminCaptchaShow() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              "get",
		Short:            "Show the captcha configuration in effect",
		QuietUnsupported: true,
		Examples: []cli.Example{
			{Command: "leapmux admin captcha get"},
		},
		Run: func(inv *cli.Invocation) (any, error) {
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resp, err := c.AdminSettingsService().ListSettings(inv.Context, &leapmuxv1.ListSettingsRequest{})
			if err != nil {
				return nil, err
			}
			wanted := captchaSettingKeys()
			values := make(map[string]adminSettingRow)
			for _, v := range resp.GetValues() {
				if slices.Contains(wanted, v.GetKey()) {
					values[v.GetKey()] = adminSettingRowOf(v)
				}
			}
			return values, nil
		},
	})
}

type captchaTuningSpec struct {
	flagName  string
	providers []string
	jsonKey   string
	secret    bool
	value     func(o *captchaSetOptions, family captcha.AltchaSettings) any
}

// captchaTuningSpecs is sorted alphabetically by flagName so foreign-flag
// refusals report the same flag first on every run.
var captchaTuningSpecs = []captchaTuningSpec{
	{
		flagName:  "algorithm",
		providers: []string{captcha.ProviderAlias(captcha.ProviderAltcha)},
		jsonKey:   "algorithm",
		value:     func(o *captchaSetOptions, _ captcha.AltchaSettings) any { return o.algorithm },
	},
	{
		flagName:  "cost",
		providers: []string{captcha.ProviderAlias(captcha.ProviderAltcha)},
		jsonKey:   "cost",
		value: func(o *captchaSetOptions, family captcha.AltchaSettings) any {
			return defaultIfZero(o.cost, family.Cost)
		},
	},
	{
		flagName:  "expires",
		providers: []string{captcha.ProviderAlias(captcha.ProviderAltcha)},
		jsonKey:   "challenge_expiry_seconds",
		value: func(o *captchaSetOptions, _ captcha.AltchaSettings) any {
			return defaultIfZero(o.expires, captcha.DefaultAltchaSettings().ChallengeExpirySeconds)
		},
	},
	{
		flagName:  "memory-cost",
		providers: []string{captcha.ProviderAlias(captcha.ProviderAltcha)},
		jsonKey:   "memory_cost",
		value: func(o *captchaSetOptions, family captcha.AltchaSettings) any {
			return defaultIfZero(o.memoryCost, family.MemoryCost)
		},
	},
	{
		flagName:  "min-score",
		providers: []string{captcha.ProviderAlias(captcha.ProviderRecaptchaV3)},
		jsonKey:   "min_score",
		value: func(o *captchaSetOptions, _ captcha.AltchaSettings) any {
			return defaultIfZero(o.minScore, captcha.DefaultRecaptchaV3Settings().MinScore)
		},
	},
	{
		flagName:  "parallelism",
		providers: []string{captcha.ProviderAlias(captcha.ProviderAltcha)},
		jsonKey:   "parallelism",
		value: func(o *captchaSetOptions, family captcha.AltchaSettings) any {
			return defaultIfZero(o.parallelism, family.Parallelism)
		},
	},
	{
		flagName:  "secret",
		providers: []string{captcha.ProviderAlias(captcha.ProviderRecaptchaV3), captcha.ProviderAlias(captcha.ProviderTurnstile)},
		jsonKey:   "secret_key",
		secret:    true,
		value:     func(o *captchaSetOptions, _ captcha.AltchaSettings) any { return o.secret },
	},
	{
		flagName:  "site-key",
		providers: []string{captcha.ProviderAlias(captcha.ProviderRecaptchaV3), captcha.ProviderAlias(captcha.ProviderTurnstile)},
		jsonKey:   "site_key",
		value:     func(o *captchaSetOptions, _ captcha.AltchaSettings) any { return o.siteKey },
	},
}

func refuseCaptchaForeignFlags(inv *cli.Invocation, target captcha.Provider) error {
	alias := captcha.ProviderAlias(target)
	for _, spec := range captchaTuningSpecs {
		if !inv.Changed(spec.flagName) || slices.Contains(spec.providers, alias) {
			continue
		}
		return cli.Usagef(
			"--%s applies only to %s; the target provider is %s",
			spec.flagName, strings.Join(spec.providers, " and "), alias)
	}
	return nil
}

type captchaState struct {
	selected        captcha.Provider
	siteKeySet      map[string]bool
	secretSet       map[string]bool
	altchaAlgorithm string
}

func readCaptchaState(ctx context.Context, c *control.HubClient) (captchaState, error) {
	out := captchaState{
		selected:        captcha.ProviderAltcha,
		siteKeySet:      map[string]bool{},
		secretSet:       map[string]bool{},
		altchaAlgorithm: captcha.DefaultAltchaSettings().Algorithm,
	}
	resp, err := c.AdminSettingsService().ListSettings(ctx, &leapmuxv1.ListSettingsRequest{})
	if err != nil {
		return out, err
	}
	found := false
	for _, v := range resp.GetValues() {
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
		for _, set := range v.GetSecretSet() {
			if set {
				out.secretSet[v.GetKey()] = true
				break
			}
		}
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
		return out, fmt.Errorf("this hub reports no captcha configuration")
	}
	return out, nil
}

type captchaSetOptions struct {
	provider, algorithm, siteKey, secret   string
	minScore                               float64
	cost, memoryCost, parallelism, expires int64
}

func (o *captchaSetOptions) bind(fs *pflag.FlagSet) {
	fs.StringVar(&o.provider, "provider", "", "provider alias; omit to tune the active provider")
	cli.SetEnum(fs, "provider", captcha.SupportedProviders()...)
	cli.SetFlagGroup(fs, "provider", "Provider")

	fs.StringVar(&o.algorithm, "algorithm", "", "ALTCHA algorithm")
	cli.SetEnum(fs, "algorithm", captcha.SupportedAltchaAlgorithms()...)
	fs.Int64Var(&o.cost, "cost", 0, "algorithm cost parameter; 0 restores the default")
	fs.Int64Var(&o.memoryCost, "memory-cost", 0, "algorithm memory cost; 0 restores the default")
	fs.Int64Var(&o.parallelism, "parallelism", 0, "algorithm parallelism; 0 restores the default")
	fs.Int64Var(&o.expires, "expires", 0, "challenge expiry seconds; 0 restores the default")
	for _, name := range []string{"algorithm", "cost", "memory-cost", "parallelism", "expires"} {
		cli.SetFlagGroup(fs, name, "ALTCHA")
	}

	fs.StringVar(&o.siteKey, "site-key", "", "provider site key")
	fs.StringVar(&o.secret, "secret", "", "provider secret (stored encrypted)")
	fs.Float64Var(&o.minScore, "min-score", 0, "recaptcha_v3 minimum score (0 < v <= 1); 0 restores the default")
	for _, name := range []string{"site-key", "secret", "min-score"} {
		cli.SetFlagGroup(fs, name, "External Providers")
	}
}

func (o *captchaSetOptions) validateLocal(inv *cli.Invocation) (*captcha.Provider, error) {
	if !anyLocalFlagChanged(inv) {
		return nil, cli.Usagef("nothing to set (pass --provider or a tuning flag)")
	}
	if inv.Changed("secret") && o.secret == "" {
		return nil, cli.Usagef("--secret must not be empty; an empty secret fails every verification")
	}
	if inv.Changed("site-key") && o.siteKey == "" {
		return nil, cli.Usagef("--site-key must not be empty; an empty site key fails every verification")
	}
	if inv.Changed("algorithm") {
		if err := captcha.ValidateAltchaAlgorithm(o.algorithm); err != nil {
			return nil, cli.Wrap(cli.CodeUsage, err, "")
		}
	}
	for _, f := range []struct {
		name string
		val  int64
	}{
		{"cost", o.cost},
		{"memory-cost", o.memoryCost},
		{"parallelism", o.parallelism},
		{"expires", o.expires},
	} {
		if inv.Changed(f.name) && f.val < 0 {
			return nil, cli.Usagef("--%s must be 0 (default) or positive (got %d)", f.name, f.val)
		}
	}
	if inv.Changed("min-score") && (o.minScore < 0 || o.minScore > 1 || math.IsNaN(o.minScore)) {
		return nil, cli.Usagef("--min-score must be 0 (default) or between 0 (exclusive) and 1 (got %g)", o.minScore)
	}
	if !inv.Changed("provider") {
		return nil, nil
	}
	p, err := captcha.ParseProvider(o.provider)
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "")
	}
	if err := refuseCaptchaForeignFlags(inv, p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (o *captchaSetOptions) planWrites(
	inv *cli.Invocation,
	state captchaState,
	explicitTarget *captcha.Provider,
) (target captcha.Provider, switching bool, writes []*leapmuxv1.SettingWrite, err error) {
	target = state.selected
	if explicitTarget != nil {
		target = *explicitTarget
	}
	if err := refuseCaptchaForeignFlags(inv, target); err != nil {
		return target, false, nil, err
	}
	switching = target != state.selected

	if target != captcha.ProviderAltcha {
		row := captcha.DescriptorFor(target).Name()
		var missing, flags []string
		if !inv.Changed("site-key") && !state.siteKeySet[row] {
			missing, flags = append(missing, "site key"), append(flags, "--site-key")
		}
		if !inv.Changed("secret") && !state.secretSet[row] {
			missing, flags = append(missing, "secret"), append(flags, "--secret")
		}
		if len(missing) > 0 {
			return target, switching, nil, cli.Usagef(
				"%s has no stored %s; pass %s in this invocation",
				captcha.ProviderAlias(target),
				strings.Join(missing, " and "), strings.Join(flags, " and "))
		}
	}

	alg := state.altchaAlgorithm
	if inv.Changed("algorithm") {
		alg = o.algorithm
	}
	family, err := captcha.DefaultAltchaSettingsFor(alg)
	if err != nil {
		family = captcha.DefaultAltchaSettings()
	}

	doc := map[string]any{}
	secretDoc := map[string]any{}
	for _, spec := range captchaTuningSpecs {
		if !inv.Changed(spec.flagName) {
			continue
		}
		if spec.secret {
			secretDoc[spec.jsonKey] = spec.value(o, family)
		} else {
			doc[spec.jsonKey] = spec.value(o, family)
		}
	}

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
	var publicJSON, secretJSON json.RawMessage
	if len(doc) > 0 {
		if publicJSON, err = json.Marshal(doc); err != nil {
			return target, switching, nil, err
		}
	}
	if len(secretDoc) > 0 {
		if secretJSON, err = json.Marshal(secretDoc); err != nil {
			return target, switching, nil, err
		}
	}
	addWrite(captcha.DescriptorFor(target).Name(), publicJSON, secretJSON)

	if switching {
		alias, err := json.Marshal(captcha.ProviderAlias(target))
		if err != nil {
			return target, switching, nil, err
		}
		enabled, err := json.Marshal(true)
		if err != nil {
			return target, switching, nil, err
		}
		addWrite(captcha.CaptchaSelectedKey.Name(), alias, nil)
		addWrite(captcha.CaptchaEnabledKey.Name(), enabled, nil)
	}
	return target, switching, writes, nil
}

type adminCaptchaSetResult struct {
	Updated   bool   `json:"updated"`
	Provider  string `json:"provider"`
	Activated bool   `json:"activated"`
}

// newAdminCaptchaSet builds `admin captcha set`.
func newAdminCaptchaSet() *cobra.Command {
	var opts captchaSetOptions
	return cli.NewLeaf(cli.Leaf{
		Use:              "set",
		Short:            "Change the captcha settings, or switch the provider",
		Mutates:          true,
		QuietUnsupported: true,
		Examples: []cli.Example{
			{Command: "leapmux admin captcha set --provider turnstile --site-key 0x4AAA --secret s3cr3t"},
		},
		Flags: opts.bind,
		Run: func(inv *cli.Invocation) (any, error) {
			explicitTarget, err := opts.validateLocal(inv)
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			state, err := readCaptchaState(inv.Context, c)
			if err != nil {
				return nil, err
			}
			target, switching, writes, err := opts.planWrites(inv, state, explicitTarget)
			if err != nil {
				return nil, err
			}
			if len(writes) == 0 {
				// The caller passed --provider for the already-active provider
				// with no tuning flags; return an idempotent no-op.
				return adminCaptchaSetResult{Updated: false, Provider: captcha.ProviderAlias(target), Activated: false}, nil
			}
			if _, err := c.AdminSettingsService().UpdateSettings(inv.Context,
				&leapmuxv1.UpdateSettingsRequest{Writes: writes}); err != nil {
				return nil, err
			}
			return adminCaptchaSetResult{Updated: true, Provider: captcha.ProviderAlias(target), Activated: switching}, nil
		},
	})
}

func newAdminCaptchaSetEnabled(enabled bool, use, short string, examples []cli.Example) *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              use,
		Short:            short,
		Examples:         examples,
		Mutates:          true,
		QuietUnsupported: true,
		Run: func(inv *cli.Invocation) (any, error) {
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			if _, err := updateSettingJSON(inv.Context, c, captcha.CaptchaEnabledKey.Name(), enabled); err != nil {
				return nil, err
			}
			return map[string]bool{captcha.CaptchaEnabledKey.Name(): enabled}, nil
		},
	})
}

type adminCaptchaResetResult struct {
	Reset []string `json:"reset"`
}

func (r adminCaptchaResetResult) QuietLines() []string { return r.Reset }

func newAdminCaptchaReset() *cobra.Command {
	var provider string
	return cli.NewLeaf(cli.Leaf{
		Use:         "reset",
		Short:       "Restore the captcha defaults of every provider, or of one",
		Mutates:     true,
		Destructive: true,
		Examples: []cli.Example{
			{Command: "leapmux admin captcha reset"},
			{Comment: "Reset one provider.", Command: "leapmux admin captcha reset --provider altcha"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.StringVar(&provider, "provider", "", "reset one provider's row (omit = all captcha settings)")
			cli.SetEnum(fs, "provider", captcha.SupportedProviders()...)
		},
		Run: func(inv *cli.Invocation) (any, error) {
			var target *captcha.Provider
			if inv.Changed("provider") {
				p, err := captcha.ParseProvider(provider)
				if err != nil {
					return nil, cli.Wrap(cli.CodeUsage, err, "")
				}
				target = &p
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			keys := captchaSettingKeys()
			if target != nil {
				keys = []string{captcha.DescriptorFor(*target).Name()}
				if *target != captcha.ProviderAltcha {
					state, err := readCaptchaState(inv.Context, c)
					if err != nil {
						return nil, err
					}
					if state.selected == *target {
						keys = append([]string{captcha.CaptchaSelectedKey.Name()}, keys...)
					}
				}
			}
			resp, err := c.AdminSettingsService().ResetSettings(inv.Context,
				&leapmuxv1.ResetSettingsRequest{Keys: keys})
			if err != nil {
				return nil, err
			}
			reset := make([]string, 0, len(resp.GetValues()))
			for _, v := range resp.GetValues() {
				reset = append(reset, v.GetKey())
			}
			return adminCaptchaResetResult{Reset: reset}, nil
		},
	})
}

func rateLimitOperation(op string) (string, error) {
	known := ratelimit.KnownOperations()
	names := make([]string, 0, len(known))
	for _, k := range known {
		names = append(names, string(k))
	}
	hint := "known operations: " + strings.Join(names, ", ")
	if op == "" {
		return "", cli.Usagef("<operation> is empty").WithHint("%s", hint)
	}
	key, ok := ratelimit.LimitKey(ratelimit.Operation(op))
	if !ok {
		return "", cli.Usagef("unknown operation %q", op).WithHint("%s", hint)
	}
	return key.Name(), nil
}

type adminRateLimitListResult struct {
	RateLimits []adminSettingRow `json:"rate_limits"`
}

func (r adminRateLimitListResult) QuietLines() []string {
	out := make([]string, 0, len(r.RateLimits))
	for _, row := range r.RateLimits {
		out = append(out, row.Key)
	}
	return out
}

func newAdminRateLimitList() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the rate limits of every operation",
		Examples: []cli.Example{
			{Command: "leapmux admin rate-limit list"},
		},
		Run: func(inv *cli.Invocation) (any, error) {
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resp, err := c.AdminSettingsService().ListSettings(inv.Context, &leapmuxv1.ListSettingsRequest{})
			if err != nil {
				return nil, err
			}
			rows := make([]adminSettingRow, 0)
			for _, v := range resp.GetValues() {
				if strings.HasPrefix(v.GetKey(), ratelimit.SettingKeyPrefix) {
					rows = append(rows, adminSettingRowOf(v))
				}
			}
			return adminRateLimitListResult{RateLimits: rows}, nil
		},
	})
}

func newAdminRateLimitSet() *cobra.Command {
	var maxAttempts, window int64
	return cli.NewLeaf(cli.Leaf{
		Use:     "set <operation>",
		Short:   "Change the rate limit of an operation",
		Mutates: true,
		Examples: []cli.Example{
			{Command: "leapmux admin rate-limit set elevation --max-attempts 5 --window 900"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.Int64Var(&maxAttempts, "max-attempts", 0, "allowed failed attempts (or admitted requests for anonymous/setup limits) per window (1-1000); 0 restores the default")
			fs.Int64Var(&window, "window", 0, "window seconds (60-86400); 0 restores the default")
		},
		Complete: completeRateLimitOperations,
		Run: func(inv *cli.Invocation) (any, error) {
			op := strings.TrimSpace(inv.Arg("operation"))
			key, err := rateLimitOperation(op)
			if err != nil {
				return nil, err
			}
			if !inv.Changed("max-attempts") && !inv.Changed("window") {
				return nil, cli.Usagef("pass --max-attempts, --window, or both")
			}
			if inv.Changed("max-attempts") && (maxAttempts < 0 || maxAttempts > 1000) {
				return nil, cli.Usagef("--max-attempts must be 0 (default) or between 1 and 1000 (got %d)", maxAttempts)
			}
			if inv.Changed("window") && (window < 0 || (window > 0 && (window < 60 || window > 86400))) {
				return nil, cli.Usagef("--window must be 0 (default) or between 60 and 86400 (got %d)", window)
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			def, _ := ratelimit.DefaultLimits(ratelimit.Operation(op))
			doc := map[string]any{}
			if inv.Changed("max-attempts") {
				doc["max_attempts"] = defaultIfZero(maxAttempts, def.MaxAttempts)
			}
			if inv.Changed("window") {
				doc["window_seconds"] = defaultIfZero(window, def.WindowSeconds)
			}
			value, err := updateSettingJSON(inv.Context, c, key, doc)
			if err != nil {
				return nil, err
			}
			return adminSettingRowOf(value), nil
		},
	})
}

func newAdminRateLimitSetEnabled(enabled bool, use, short string, examples []cli.Example) *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:      use,
		Short:    short,
		Examples: examples,
		Mutates:  true,
		Complete: completeRateLimitOperations,
		Run: func(inv *cli.Invocation) (any, error) {
			key, err := rateLimitOperation(strings.TrimSpace(inv.Arg("operation")))
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			value, err := updateSettingJSON(inv.Context, c, key, map[string]any{"enabled": enabled})
			if err != nil {
				return nil, err
			}
			return adminSettingRowOf(value), nil
		},
	})
}

func newAdminRateLimitReset() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:         "reset <operation>",
		Short:       "Restore the default rate limit of an operation",
		Mutates:     true,
		Destructive: true,
		Examples: []cli.Example{
			{Command: "leapmux admin rate-limit reset elevation"},
		},
		Complete: completeRateLimitOperations,
		Run: func(inv *cli.Invocation) (any, error) {
			key, err := rateLimitOperation(strings.TrimSpace(inv.Arg("operation")))
			if err != nil {
				return nil, err
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			value, _, err := resetOneSetting(inv.Context, c, key)
			if err != nil {
				return nil, err
			}
			return adminSettingRowOf(value), nil
		},
	})
}
