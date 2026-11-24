package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/ratelimit"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
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
// same endpoint answers every registered app and `leapmux auth login
// --scope admin:read` means nothing to a third-party one. The remedy for the
// CLI's own credential is the login flow's --scope flag, whose help states
// it; a refusal is not the place to repeat it.

// requireAdminClient is newClient plus the admin exclusions: admin
// commands NEVER use the worker-IPC transport. They talk to the hub
// directly, because the worker's IPC bridge is a typing device, not a
// security boundary — anything registered there is callable by any
// worker-spawned agent, so no admin procedure is. Refusing the transport
// here (and again on the client, in case the env var leaked in some other
// way) keeps `admin ...` out of agent reach entirely.
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

// requireNonEmptyArg trims and returns the positional argument name, refusing
// an empty or whitespace-only value before dialing.
func requireNonEmptyArg(inv *cli.Invocation, name string) (string, error) {
	v := strings.TrimSpace(inv.Arg(name))
	if v == "" {
		return "", cli.Usagef("<%s> is empty", name)
	}
	return v, nil
}

// anyLocalFlagChanged reports whether the operator passed at least one flag
// declared on this leaf (ignoring persistent global flags like --hub/--json).
func anyLocalFlagChanged(inv *cli.Invocation) bool {
	changed := false
	inv.Command.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
		if f.Changed {
			changed = true
		}
	})
	return changed
}

// adminPageFlags holds the pagination pair that every paginated admin leaf
// binds and validates before dialing.
type adminPageFlags struct {
	Limit  int64
	Cursor string
}

func (p *adminPageFlags) bind(fs *pflag.FlagSet) {
	fs.Int64Var(&p.Limit, "limit", contracts.DefaultPageLimit, "page size")
	fs.StringVar(&p.Cursor, "cursor", "", "the cursor that the previous page printed")
}

func (p *adminPageFlags) validate() error {
	return validateListLimit(p.Limit)
}

func (p *adminPageFlags) request() *leapmuxv1.PageRequest {
	return &leapmuxv1.PageRequest{Limit: int32(p.Limit), Cursor: p.Cursor}
}

// minListLimit is the smallest page a verb may ask for. The ceiling is
// contracts.MaxPageLimit — the page-limit contract that the hub caps at, so
// the range this check states is always the hub's own range.
const minListLimit = 1

// validateListLimit refuses a --limit outside the page range.
func validateListLimit(limit int64) error {
	if limit < minListLimit || limit > contracts.MaxPageLimit {
		return cli.Usagef("limit must be between %d and %d", minListLimit, contracts.MaxPageLimit)
	}
	return nil
}

type adminRevocationResult struct {
	Revoked string `json:"revoked"`
}

func (r adminRevocationResult) QuietLines() []string { return []string{r.Revoked} }

type adminPurgeResult struct {
	Purged int64 `json:"purged"`
}

type adminSettingRow struct {
	Key           string          `json:"key" table:"KEY"`
	Customized    bool            `json:"customized" table:"CUSTOMIZED"`
	EffectiveJSON json.RawMessage `json:"effective_json" table:"EFFECTIVE"`
	ValueJSON     json.RawMessage `json:"value_json,omitempty"`
	UpdatedAt     string          `json:"updated_at,omitempty"`
	SecretSet     map[string]bool `json:"secret_set,omitempty"`
	Propagation   string          `json:"propagation,omitempty" table:"PROPAGATION"`
	Description   *string         `json:"description,omitempty"`
	ValueSchema   *string         `json:"value_schema,omitempty"`
	Note          string          `json:"note,omitempty"`
	NoteRestart   string          `json:"note_restart,omitempty"`
	NoteHot       string          `json:"note_hot,omitempty"`
}

func (r adminSettingRow) QuietLines() []string { return []string{r.Key} }

type adminSettingsListResult struct {
	Settings []adminSettingRow `json:"settings"`
}

func (r adminSettingsListResult) QuietLines() []string {
	out := make([]string, 0, len(r.Settings))
	for _, s := range r.Settings {
		out = append(out, s.Key)
	}
	return out
}

func adminSettingRowOf(value *leapmuxv1.SettingValue) adminSettingRow {
	row := adminSettingRow{
		Key: value.GetKey(), Customized: value.GetCustomized(),
		EffectiveJSON: settingJSONValue(value.GetEffectiveJson()),
		UpdatedAt:     formatTime(value.GetUpdatedAt()), SecretSet: value.GetSecretSet(),
	}
	if stored := value.GetValueJson(); stored != "" {
		row.ValueJSON = settingJSONValue(stored)
	}
	return row
}

// settingJSONValue retains exact numbers and distinguishes an absent stored
// value from an explicit null. An invalid document appears as plain text.
func settingJSONValue(value string) json.RawMessage {
	if value == "" {
		return json.RawMessage("null")
	}
	raw := json.RawMessage(value)
	if json.Valid(raw) {
		return raw
	}
	quoted, _ := json.Marshal(value)
	return quoted
}

// propagationName renders a key's propagation class for the operator.
func propagationName(restart bool) string {
	if restart {
		return "restart"
	}
	return "hot"
}

// addPropagation states, on a write verb's reply, whether the running hub
// already applies the written value.
func addPropagation(out *adminSettingRow, restart bool) {
	out.Propagation = propagationName(restart)
	if restart {
		out.NoteRestart = "this setting applies after a hub restart"
	} else {
		out.NoteHot = "The Hub that stored this setting applies it at once. The Hub picks up a value written directly to the shared database within its 30-second settings snapshot interval."
	}
}

// settingDescriptionText appends the domain-verb pointer to the keys whose
// dedicated verb writes several of them in one transaction.
func settingDescriptionText(key, summary string) string {
	switch {
	case slices.Contains(captchaSettingKeys(), key):
		return summary + " (prefer `leapmux admin captcha ...`)"
	case strings.HasPrefix(key, ratelimit.SettingKeyPrefix):
		return summary + " (prefer `leapmux admin rate-limit ...`)"
	}
	return summary
}

// newAdminSettingsList builds `admin settings list`.
func newAdminSettingsList() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List every setting with its effective value",
		Examples: []cli.Example{
			{Command: "leapmux admin settings list"},
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
			rows := make([]adminSettingRow, 0, len(resp.GetValues()))
			byDescr := map[string]*leapmuxv1.SettingDescriptor{}
			for _, d := range resp.GetDescriptors() {
				byDescr[d.GetKey()] = d
			}
			for _, v := range resp.GetValues() {
				row := adminSettingRowOf(v)
				if d := byDescr[v.GetKey()]; d != nil {
					row.Propagation = propagationName(d.GetRestart())
					row.Description = ptrconv.Ptr(settingDescriptionText(d.GetKey(), d.GetSummary()))
				}
				rows = append(rows, row)
			}
			return adminSettingsListResult{Settings: rows}, nil
		},
	})
}

// parseSettingValue accepts a JSON document or a bare scalar, quoting the
// scalar on the caller's behalf.
func parseSettingValue(raw string) (json.RawMessage, error) {
	if raw == "" {
		return nil, errors.New(
			`<value> is empty; pass '""' to store the empty string, or run "leapmux admin settings reset <key>" to restore the default`)
	}
	if strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[") {
		if !json.Valid([]byte(raw)) {
			return nil, fmt.Errorf("value starts with %q but is not valid JSON; check the brackets and the quoting", raw[:1])
		}
		return json.RawMessage(raw), nil
	}
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

// newAdminSettingsGet builds `admin settings get KEY`.
func newAdminSettingsGet() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "get <key>",
		Short: "Show one setting, its value and its shape",
		Examples: []cli.Example{
			{Command: "leapmux admin settings get session_duration_seconds"},
		},
		Complete: completeAdminSettingsKeys,
		Run: func(inv *cli.Invocation) (any, error) {
			key := inv.Arg("key")
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			resp, err := c.AdminSettingsService().ListSettings(inv.Context, &leapmuxv1.ListSettingsRequest{})
			if err != nil {
				return nil, err
			}
			for _, v := range resp.GetValues() {
				if v.GetKey() != key {
					continue
				}
				out := adminSettingRowOf(v)
				for _, d := range resp.GetDescriptors() {
					if d.GetKey() == key {
						out.Propagation = propagationName(d.GetRestart())
						out.Description = ptrconv.Ptr(settingDescriptionText(d.GetKey(), d.GetSummary()))
						out.ValueSchema = ptrconv.Ptr(settingShapeFromDescriptor(d))
					}
				}
				return out, nil
			}
			return nil, cli.Newf(cli.CodeNotFound, "unknown setting key %q", key).
				WithHint("run leapmux admin settings list to list setting keys")
		},
	})
}

// settingShapeFromDescriptor derives the JSON shape hint from the
// descriptor's field schema.
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

// newAdminSettingsSet builds `admin settings set KEY VALUE`.
func newAdminSettingsSet() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "set <key> <value>",
		Short:   "Set the public value of a setting from JSON or a bare value",
		Mutates: true,
		Examples: []cli.Example{
			{Command: "leapmux admin settings set signup_enabled false"},
			{Comment: "Set two fields of a setting that holds a document.", Command: `leapmux admin settings set smtp '{"host":"smtp.example.com","port":587}'`},
		},
		Complete: func(cmd *cobra.Command, spec cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
			if spec.Name == "key" {
				return completeAdminSettingsKeys(cmd, spec, toComplete)
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		Run: func(inv *cli.Invocation) (any, error) {
			value, err := parseSettingValue(inv.Arg("value"))
			if err != nil {
				return nil, cli.Wrap(cli.CodeUsage, err, "")
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			updated, restart, err := updateOneSetting(inv.Context, c, &leapmuxv1.SettingWrite{
				Key: inv.Arg("key"), PartialJson: string(value),
			})
			if err != nil {
				return nil, err
			}
			out := adminSettingRowOf(updated)
			if updated.GetEffectiveJson() != updated.GetValueJson() {
				out.Note = "The effective value differs from the stored value. See effective_json."
			}
			addPropagation(&out, restart)
			return out, nil
		},
	})
}

// newAdminSettingsSetSecret builds `admin settings set-secret KEY JSON`.
func newAdminSettingsSetSecret() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "set-secret <key> <json>",
		Short:   "Set the secret fields of a setting; the hub stores them encrypted",
		Mutates: true,
		Examples: []cli.Example{
			{Command: `leapmux admin settings set-secret smtp '{"password":"..."}'`},
		},
		Complete: func(cmd *cobra.Command, spec cli.ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective) {
			if spec.Name == "key" {
				return completeAdminSettingsKeys(cmd, spec, toComplete)
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		Run: func(inv *cli.Invocation) (any, error) {
			raw := strings.TrimSpace(inv.Arg("json"))
			if !strings.HasPrefix(raw, "{") || !json.Valid([]byte(raw)) {
				return nil, cli.Usagef(`<json> must be a JSON object of the secret fields, for example {"password":"..."}`)
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			updated, restart, err := updateOneSetting(inv.Context, c, &leapmuxv1.SettingWrite{
				Key: inv.Arg("key"), SecretPartialJson: inv.Arg("json"),
			})
			if err != nil {
				return nil, err
			}
			out := adminSettingRowOf(updated)
			addPropagation(&out, restart)
			return out, nil
		},
	})
}

// newAdminSettingsReset builds `admin settings reset KEY`.
func newAdminSettingsReset() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:         "reset <key>",
		Short:       "Restore the default value of a setting",
		Mutates:     true,
		Destructive: true,
		Examples: []cli.Example{
			{Command: "leapmux admin settings reset session_duration_seconds"},
		},
		Complete: completeAdminSettingsKeys,
		Run: func(inv *cli.Invocation) (any, error) {
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			value, restart, err := resetOneSetting(inv.Context, c, inv.Arg("key"))
			if err != nil {
				return nil, err
			}
			out := adminSettingRowOf(value)
			addPropagation(&out, restart)
			return out, nil
		},
	})
}

// newAdminCommand builds `admin`, the online, authenticated surface
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
				[]cli.Example{{Command: "leapmux admin user grant-admin alice"}}),
			newAdminUserSetAdmin(false, "revoke-admin <user>", "Take a user's administrator access away",
				[]cli.Example{{Command: "leapmux admin user revoke-admin alice"}}),
			newAdminUserResetPassword(),
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
		cli.Group("app", "Manage app registrations: what a consent screen authorizes",
			newAdminAppList(),
			newAdminAppRegister(),
			newAdminAppUpdate(),
			newAdminAppSetVerified(true, "verify <app>", "Vouch for an app, so that its consent screen stops warning",
				[]cli.Example{{Command: "leapmux admin app verify lmx_app_x1y2"}}),
			newAdminAppSetVerified(false, "unverify <app>", "Withdraw a vouch for an app",
				[]cli.Example{{Command: "leapmux admin app unverify lmx_app_x1y2"}}),
			newAdminAppSetElevation(true, "allow-elevation <app>", "Let an app ask a user to prove a factor again",
				[]cli.Example{{Command: "leapmux admin app allow-elevation lmx_app_x1y2"}}),
			newAdminAppSetElevation(false, "deny-elevation <app>", "Take that permission away; every open elevation ends at the next call",
				[]cli.Example{{Command: "leapmux admin app deny-elevation lmx_app_x1y2"}}),
			newAdminAppRevoke(),
			newAdminAppDelete(),
		),
		cli.Group("idp", "Manage the OAuth and OIDC providers that users sign in with",
			newAdminOAuthProviderAdd(),
			newAdminOAuthProviderList(),
			newAdminOAuthProviderRemove(),
			newAdminOAuthProviderSetEnabled(true, "enable <idp>", "Enable a sign-in provider",
				[]cli.Example{{Command: "leapmux admin idp enable 3hKd9s"}}),
			newAdminOAuthProviderSetEnabled(false, "disable <idp>", "Disable a sign-in provider",
				[]cli.Example{{Command: "leapmux admin idp disable 3hKd9s"}}),
			newAdminOAuthProviderRotateSecret(),
		),
		cli.Group("captcha", "Manage the bot protection: ALTCHA, reCAPTCHA v3 or Turnstile (a solo hub never asks)",
			newAdminCaptchaShow(),
			newAdminCaptchaSet(),
			newAdminCaptchaSetEnabled(true, "enable", "Turn captcha verification on",
				[]cli.Example{{Command: "leapmux admin captcha enable"}}),
			newAdminCaptchaSetEnabled(false, "disable", "Turn captcha verification off; the honeypot check stays",
				[]cli.Example{{Command: "leapmux admin captcha disable"}}),
			newAdminCaptchaReset(),
		),
		cli.Group("rate-limit", "Manage the rate limit of each user operation",
			newAdminRateLimitList(),
			newAdminRateLimitSet(),
			newAdminRateLimitSetEnabled(true, "enable <operation>", "Turn on the rate limit of an operation",
				[]cli.Example{{Command: "leapmux admin rate-limit enable elevation"}}),
			newAdminRateLimitSetEnabled(false, "disable <operation>", "Turn off the rate limit of an operation",
				[]cli.Example{{Command: "leapmux admin rate-limit disable elevation"}}),
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
		cli.Group("network", "Inspect the Hub's live TCP listeners, IPC sockets, and host network interfaces",
			newAdminNetworkStatus(),
		),
	)
	cli.DescribeArgs(admin, adminArgHelp())
	return admin
}

// adminArgHelp describes the arguments of the admin verbs.
func adminArgHelp() map[string]string {
	operations := make([]string, 0, len(ratelimit.KnownOperations()))
	for _, op := range ratelimit.KnownOperations() {
		operations = append(operations, string(op))
	}
	return map[string]string{
		"user":      control.UserArgumentHelp,
		"worker":    `A worker id or unique prefix of 4 or more characters, as "leapmux admin worker list" prints it.`,
		"reg-key":   `A registration key id or unique prefix, as "leapmux admin worker reg-key list" prints it.`,
		"app":       `An app's client id or unique prefix, as "leapmux admin app list" prints it.`,
		"idp":       `A sign-in provider id or unique prefix, as "leapmux admin idp list" prints it.`,
		"session":   `A session id or unique prefix, as "leapmux admin session list" prints it.`,
		"token":     `A token id or unique prefix, as the list command of this group prints it.`,
		"operation": "One of: " + strings.Join(operations, ", ") + ".",
		"key":       `A setting key, as "leapmux admin settings list" prints it.`,
		"value":     `A JSON value. A word that is not JSON is stored as a string.`,
		"json":      `A JSON object of the secret fields, such as {"password":"..."}.`,
	}
}
