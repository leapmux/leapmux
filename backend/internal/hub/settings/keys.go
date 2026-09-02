package settings

import (
	"fmt"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/httpsec"
	"github.com/leapmux/leapmux/internal/hub/listenset"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

// DefaultSessionDuration mirrors auth.DefaultSessionDuration (7 days) and
// MinSessionDuration (5 minutes); expressed in seconds here because the
// stored document is seconds.
const (
	DefaultSessionDurationSeconds int64 = 7 * 24 * 60 * 60
	MinSessionDurationSeconds     int64 = 5 * 60
	// MaxSessionDurationSeconds caps the session lifetime at ten years.
	//
	// The ceiling is what makes SessionDuration safe: it multiplies the
	// stored seconds by time.Second, and an int64 the validator let through
	// would OVERFLOW that multiply and hand the auth path an arbitrary --
	// possibly negative -- duration. A negative one reads as an already
	// expired session, so the hub would sign everyone out immediately.
	// Ten years is far past any real session, so the cap costs an operator
	// nothing and removes the overflow entirely.
	MaxSessionDurationSeconds int64 = 10 * 365 * 24 * 60 * 60
)

// SMTP TLS modes for SMTPValue.TLSMode.
//
// STARTTLS is the default and most common production setting: connect in
// plaintext, then upgrade to TLS via the STARTTLS extension (typically
// port 587). Implicit dials TLS directly (port 465, the legacy "SMTPS"
// pattern). None is plaintext-only and should normally only be used for
// trusted localhost relays — validation rejects it for non-localhost
// relays that require auth, because Go's smtp.PlainAuth refuses to send
// credentials over an unencrypted, non-localhost connection.
const (
	SMTPTLSModeSTARTTLS = "starttls"
	SMTPTLSModeImplicit = "implicit"
	SMTPTLSModeNone     = "none"
)

// tlsModeEnumValues is the one source for the tls_mode enum: the UI's
// EnumValues and validateSMTP's allowed-set check both derive from it, so
// a mode added here appears everywhere at once.
var tlsModeEnumValues = []EnumValue{
	{Value: SMTPTLSModeSTARTTLS, Label: "STARTTLS", Help: "Connect in plaintext, then upgrade to TLS (typically port 587)."},
	{Value: SMTPTLSModeImplicit, Label: "Implicit TLS", Help: "Dial TLS directly (port 465, the legacy SMTPS pattern)."},
	{Value: SMTPTLSModeNone, Label: "None", Help: "Plaintext only; for trusted localhost relays."},
}

// SessionDuration reads session_duration as a time.Duration.
func SessionDuration(s *Snapshot) time.Duration {
	secs := KeySessionDurationSeconds.Of(s)
	if secs < MinSessionDurationSeconds {
		return time.Duration(MinSessionDurationSeconds) * time.Second
	}
	return time.Duration(secs) * time.Second
}

// SMTPValue is the smtp key's public shape; the password lives in the
// encrypted half. Enabled() is the single fact every consumer
// (GetSystemInfo, the sender, the verification requirement) asks for: it
// needs the host AND the from address, which are the two fields a relay
// cannot work without and neither of which has a usable default.
type SMTPValue struct {
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	Username    string `json:"username,omitempty"`
	FromAddress string `json:"from_address,omitempty"`
	TLSMode     string `json:"tls_mode,omitempty"`
	Password    string `json:"password,omitempty"`
}

// Enabled reports whether SMTP can actually send: a relay host AND a from
// address. A relay needs both, so a value that carries one of them is a
// half-configured relay, not a usable one.
//
// This is what lets validateSMTP accept a partly staged document. The
// admin surface writes ONE field per row, so a rule that refused an absent
// from address would refuse the host write that comes before it, with an
// error that specifies a field the operator did not reach yet.
func (v SMTPValue) Enabled() bool { return v.Host != "" && v.FromAddress != "" }

// validateSMTP ports the SMTP cross-field rules from the old
// config.Validate.
//
// One rule governs the whole function: a MALFORMED value fails at the
// write that introduced it, and an ABSENT one is staging. The tls_mode
// enum test and the from-address syntax test therefore run even when the
// host is still empty, because a typo must not wait for the later host
// write that first makes it reachable — and the from-address test is
// skipped when the field is empty, because "not written yet" is not a
// typo. Enabled() is what holds the pair together: it reports false until
// the host and the from address are both present, so no consumer can dial
// a half-staged relay.
func validateSMTP(v SMTPValue) error {
	if !EnumAllowed(tlsModeEnumValues, v.TLSMode) {
		return fmt.Errorf("unsupported smtp tls mode %q (supported: %s, %s, %s)",
			v.TLSMode, SMTPTLSModeSTARTTLS, SMTPTLSModeImplicit, SMTPTLSModeNone)
	}
	if v.FromAddress != "" {
		if _, err := mail.ParseAddress(v.FromAddress); err != nil {
			return fmt.Errorf("smtp from address %q is not a valid email", v.FromAddress)
		}
	}
	if v.Host == "" {
		// Email disabled: the other fields may still be staged (an admin
		// configuring piecemeal), but nothing inconsistent is possible.
		return nil
	}
	if v.Port < 1 || v.Port > 65535 {
		return fmt.Errorf("smtp port must be between 1 and 65535 (got %d)", v.Port)
	}
	if v.TLSMode == SMTPTLSModeNone && v.Username != "" &&
		!isLocalhost(v.Host) {
		return fmt.Errorf("smtp tls mode %q with credentials against a remote host would send the password in the clear", SMTPTLSModeNone)
	}
	return nil
}

// isLocalhost mirrors the exact criteria Go's smtp.PlainAuth applies
// (net/smtp isLocalhost: the three literals, nothing more): a host the
// validator accepts as safe for plaintext credentials is precisely a
// host PlainAuth will actually send them to. "*.localhost" suffixes and
// other loopback addresses (127.0.0.2) fail PlainAuth at Send time, so
// accepting them here would only pass a configuration that cannot work.
func isLocalhost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// TimeoutsValue groups the RPC timeout budget the hub advertises and
// enforces. All hot: the timeout interceptor reads api_seconds per
// request, and GetTimeouts reads the worker-facing pair.
type TimeoutsValue struct {
	APITimeoutSeconds          int64 `json:"api_seconds,omitempty"`
	AgentStartupTimeoutSeconds int64 `json:"agent_startup_seconds,omitempty"`
	WorktreeCreateSecs         int64 `json:"worktree_create_seconds,omitempty"`
}

// DefaultTimeouts is the timeouts key's default: ten seconds per API
// request, five minutes for an agent startup, one minute to create a
// worktree.
var DefaultTimeouts = TimeoutsValue{
	APITimeoutSeconds:          10,
	AgentStartupTimeoutSeconds: 300,
	WorktreeCreateSecs:         60,
}

// APITimeout returns api_seconds as a duration.
func (v TimeoutsValue) APITimeout() time.Duration {
	return time.Duration(v.APITimeoutSeconds) * time.Second
}

// AgentStartupTimeout returns agent_startup_seconds as a duration.
func (v TimeoutsValue) AgentStartupTimeout() time.Duration {
	return time.Duration(v.AgentStartupTimeoutSeconds) * time.Second
}

// WorktreeCreateTimeout returns worktree_create_seconds as a duration.
func (v TimeoutsValue) WorktreeCreateTimeout() time.Duration {
	return time.Duration(v.WorktreeCreateSecs) * time.Second
}

// LimitsValue groups the per-user connection and worker caps. Both are
// hot, and they are the one push consumer: the hub subscribes to their
// changes and pushes them into the existing atomic setters (see the
// PropagationHot rule for why these keys and no others).
//
// Both are guards, not quotas — a person is never meant to reach the
// defaults. The connection cap limits how many long-lived
// sockets one user may hold at once (an active tab holds two,
// /ws/userevents plus /ws/channel): the queue pools limit the BYTES each
// class of connection may hold, but a pool member is a connection, not a
// user, so without the cap "open more tabs" reaches memory the pools
// cannot refuse. The worker cap closes the same gap for the worker pool:
// a Worker's Connect stream is a pool member the connection cap never
// sees (it takes no lease), and registration keys carry no quota, so N
// keys would otherwise mint N members, each holding a floor the pool may
// not reclaim.
//
// The fields carry no omitempty: zero is the stored, meaningful value
// "unlimited", and omitempty would drop it from the stored document so
// the next decode merged the non-zero default back over it.
type LimitsValue struct {
	MaxConnectionsPerUser int64 `json:"max_connections_per_user"`
	MaxWorkersPerUser     int64 `json:"max_workers_per_user"`
}

// QueueBudgetValue limits the three outbound queue memory pools. All
// restart-class: the hub derives the pool minimum floors from them (and
// from the max message size) at startup, before any pool exists. A zero field
// means auto-size from the process's own memory limit, so the budget follows
// the machine the hub runs on, not the one a predecessor ran on.
//
// The fields carry no omitempty: zero is the stored, meaningful value
// "auto-size", and omitempty would drop it from the listing JSON so the
// preferences dialog rendered an empty number field instead of 0.
// queueBudgetHelp states the rule Min/Max cannot carry: the legal set is
// 0 or an interval, not one interval.
const queueBudgetHelp = "0 auto-sizes this pool from the process memory limit; any other value must be at least the class floor."

type QueueBudgetValue struct {
	RelayBytes      int64 `json:"relay_bytes"`
	WorkerBytes     int64 `json:"worker_bytes"`
	UserEventsBytes int64 `json:"userevents_bytes"`
}

// MailLimitsValue caps outbound-mail abuse; see KeyMailLimits. The fields
// carry no omitempty: zero is the stored, meaningful value "no block" /
// "unlimited", and omitempty would drop it from the stored document so the
// next decode merged the non-zero default back over it.
type MailLimitsValue struct {
	FailureCooldownSeconds int64 `json:"failure_cooldown_seconds"`
	RecipientMax           int64 `json:"recipient_max"`
	RecipientWindowSeconds int64 `json:"recipient_window_seconds"`
}

// DefaultMailLimits sizes both knobs far above every legitimate flow -- a
// person verifying an address costs two or three mails in a minute -- and
// far below what a loop costs: a failed-send retry storm gets one attempt
// per ten seconds, and one inbox gets ten mails per hour no matter how
// many accounts sent them.
var DefaultMailLimits = MailLimitsValue{
	FailureCooldownSeconds: 10,
	RecipientMax:           10,
	RecipientWindowSeconds: 3600,
}

// validateMailLimits keeps the cooldown at or under the resend cooldown it
// backs (60s): a longer window would leave one failed send blocking an
// account's mail longer than a successful send does, and the stamp
// derivation (failedSendBlockedUntil, service/resend_cooldown.go) clamps to
// the same bound as a second guard. The recipient window stays inside a
// day: a window longer than that stops being a rate limit and starts being
// a denial of service to a legitimate address.
func validateMailLimits(v MailLimitsValue) error {
	if v.FailureCooldownSeconds < 0 || v.FailureCooldownSeconds > 60 {
		return fmt.Errorf("failure cooldown must be between 0 and 60 seconds (got %d)", v.FailureCooldownSeconds)
	}
	if v.RecipientMax < 0 || v.RecipientMax > 1000 {
		return fmt.Errorf("per-recipient max must be between 0 and 1000 (got %d)", v.RecipientMax)
	}
	if v.RecipientWindowSeconds < 60 || v.RecipientWindowSeconds > 86400 {
		return fmt.Errorf("per-recipient window must be between 60 and 86400 seconds (got %d)", v.RecipientWindowSeconds)
	}
	return nil
}

// EmailFailureCooldown reads mail_limits.failure_cooldown_seconds as a
// time.Duration. Zero means a failed send blocks nothing.
func EmailFailureCooldown(s *Snapshot) time.Duration {
	return time.Duration(KeyMailLimits.Of(s).FailureCooldownSeconds) * time.Second
}

// MailRecipientBudget reads the per-recipient mail budget: most mails one
// recipient address may receive per window. A max of zero means no budget.
func MailRecipientBudget(s *Snapshot) (max int64, window time.Duration) {
	v := KeyMailLimits.Of(s)
	return v.RecipientMax, time.Duration(v.RecipientWindowSeconds) * time.Second
}

// DefaultMaxMessageSizeBytes is the default application payload budget;
// channelwire owns the number and channelwire.ResolveMaxMessageSize stays
// the one resolver.
const DefaultMaxMessageSizeBytes = contracts.MaxMessageSize

// validatePublicURL ports the old canonical public_url rule: scheme+host
// only, nothing else — the hub appends its own routes. The validator
// refuses a trailing slash, userinfo, query, or fragment rather than
// normalizing or ignoring it: BaseURL returns the stored string verbatim, and any of those
// would corrupt every URL the hub derives from it (double-slash links,
// credentials leaked into email footers).
func validatePublicURL(v string) error {
	if v == "" {
		return nil
	}
	u, err := url.Parse(v)
	if err != nil {
		return fmt.Errorf("parse public url: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("public url must be a scheme+host base URL like https://hub.example.com")
	}
	if u.Path != "" {
		return fmt.Errorf("public url must not carry a path or trailing slash (the hub appends its own routes)")
	}
	if u.User != nil {
		return fmt.Errorf("public url must not carry credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("public url must not carry a query or fragment")
	}
	return nil
}

// validateSessionDuration keeps the idle-timeout floor: below it the
// setting stops being a session duration and becomes a logout loop.
func validateSessionDuration(seconds int64) error {
	if seconds < MinSessionDurationSeconds {
		return fmt.Errorf("session duration must be at least %ds (got %ds)", MinSessionDurationSeconds, seconds)
	}
	if seconds > MaxSessionDurationSeconds {
		return fmt.Errorf("session duration must be at most %ds (got %ds)", MaxSessionDurationSeconds, seconds)
	}
	return nil
}

// MaxTimeoutSeconds caps every timeout budget at one day.
//
// The ceiling is what makes GetTimeouts safe: the RPC carries these as
// int32 seconds, so an int64 the validator lets through would WRAP on the
// narrowing conversion and hand the client an arbitrary — possibly
// negative — timeout. A day is far past any real budget, so the cap costs
// an operator nothing and removes the overflow entirely.
const MaxTimeoutSeconds = 86400

// validateTimeouts keeps every budget strictly positive and inside the
// range the wire can carry; a zero timeout would fail every call it
// applies to, and one past MaxTimeoutSeconds cannot reach a client
// intact.
func validateTimeouts(v TimeoutsValue) error {
	for _, f := range []struct {
		name  string
		value int64
	}{
		{"api timeout", v.APITimeoutSeconds},
		{"agent startup timeout", v.AgentStartupTimeoutSeconds},
		{"worktree create timeout", v.WorktreeCreateSecs},
	} {
		if f.value < 1 || f.value > MaxTimeoutSeconds {
			return fmt.Errorf("%s must be between 1s and %ds (got %ds)", f.name, MaxTimeoutSeconds, f.value)
		}
	}
	return nil
}

// validateLimits ports the connection-cap floor: four is the smallest
// working value (an active tab holds two sockets), and zero means
// unlimited rather than "refuse everyone".
func validateLimits(v LimitsValue) error {
	if err := validateCap("max_connections_per_user", v.MaxConnectionsPerUser, 4); err != nil {
		return err
	}
	return validateCap("max_workers_per_user", v.MaxWorkersPerUser, 1)
}

func validateCap(name string, v, floor int64) error {
	if v < 0 {
		return fmt.Errorf("%s must not be negative (got %d)", name, v)
	}
	if v > 0 && v < floor {
		return fmt.Errorf("%s must be 0 (unlimited) or at least %d (got %d)", name, floor, v)
	}
	return nil
}

// validateQueueBudget checks each explicit budget against the same
// geometry the config resolver clamps auto-sized budgets into: below the
// class minimum a pool is structurally degenerate (or refuses to build at
// all — sendq.NewPool panics when the floor exceeds the capacity), and
// above config.MaxQueueMemoryBudget the queue only delays a needed
// reclaim. Zero means auto and is always allowed. The floor and the
// ceiling both come from config, the one owner of the queue-class
// geometry, so the validator can never disagree with the resolver.
func validateQueueBudget(v QueueBudgetValue) error {
	// An ordered slice, not a map: the loop returns on the FIRST failure,
	// and Go randomizes map iteration, so a document with two bad budgets
	// would report a different field on each run. validateTimeouts uses a
	// slice for the same reason.
	for _, f := range []struct {
		name  string
		value int64
	}{
		{"relay_bytes", v.RelayBytes},
		{"worker_bytes", v.WorkerBytes},
		{"userevents_bytes", v.UserEventsBytes},
	} {
		if f.value < 0 {
			return fmt.Errorf("queue budget %s must not be negative (got %d)", f.name, f.value)
		}
		floor, err := config.QueueBudgetFloor(f.name)
		if err != nil {
			return err
		}
		if f.value > 0 && f.value < floor {
			return fmt.Errorf("queue budget %s must be 0 (auto-size) or at least %d bytes (got %d)", f.name, floor, f.value)
		}
		if f.value > config.MaxQueueMemoryBudget {
			return fmt.Errorf("queue budget %s exceeds the %d byte ceiling (got %d)", f.name, config.MaxQueueMemoryBudget, f.value)
		}
	}
	return nil
}

// validateMaxMessageSize delegates the payload-range rule to channelwire,
// which owns the floor (the largest frame the CRDT resume path must
// always carry) and the ceiling (the reassembly memory limit).
func validateMaxMessageSize(v int64) error {
	if v <= 0 {
		return fmt.Errorf("max message size must be positive (got %d)", v)
	}
	return channelwire.ValidateMaxMessageSize(int(v))
}

// MaxExtraListenAddresses caps how many addresses extra_listen_addresses may
// hold. Every entry costs a listener, a serve goroutine and a file descriptor
// for the life of the process, and the apply path binds them one at a time
// while it holds the listener set's lock. A machine with more than eight
// interfaces to publish on wants a wildcard, which is one entry.
const MaxExtraListenAddresses = 8

// ExtraListenValue is the extra_listen_addresses key's shape: the addresses an
// administrator adds BESIDE the one -listen gives.
//
// The -listen address is not in here and cannot be. It is read before the
// database opens, so a hub whose stored settings are unreadable still binds
// the address its operator named on the command line.
type ExtraListenValue struct {
	Addresses []string `json:"addresses,omitempty"`
}

// Addrs parses the stored list. It reports the first bad entry rather than
// skipping it: the validator refused that entry at the write, so one here
// means the row was edited outside the hub, and binding the rest of a document
// nobody validated would publish an address list the operator never approved.
func (v ExtraListenValue) Addrs() ([]listenset.Addr, error) {
	return listenset.ParseAll(v.Addresses)
}

// ExtraListenAddresses reads the key as parsed addresses. A stored document
// that no longer parses degrades to NO extra addresses, with the error for the
// caller to log: the alternative is a hub that refuses to serve at all, and
// the -listen address is always still bound.
func ExtraListenAddresses(s *Snapshot) ([]listenset.Addr, error) {
	return KeyExtraListenAddresses.Of(s).Addrs()
}

// validateExtraListen enforces what the picker already produces, because the
// admin CLI writes the same key and a name there would expose an address the
// operator never saw.
func validateExtraListen(v ExtraListenValue) error {
	if len(v.Addresses) > MaxExtraListenAddresses {
		return fmt.Errorf("at most %d extra listen addresses (got %d); use a wildcard address to serve every interface",
			MaxExtraListenAddresses, len(v.Addresses))
	}
	seen := make(map[string]bool, len(v.Addresses))
	for i, raw := range v.Addresses {
		addr, err := listenset.Parse(raw)
		if err != nil {
			return fmt.Errorf("extra listen address %d: %w", i+1, err)
		}
		// Port 0 asks the operating system to choose, which -listen may do
		// and a stored address may not: the hub would answer on a port
		// nobody can be told, and a different one after every restart.
		if addr.Port() == 0 {
			return fmt.Errorf("extra listen address %d (%q) has port 0; give the port to connect to", i+1, raw)
		}
		// A NAME is refused, and this is the reason: the hub binds whatever it
		// resolves to at bind time, so a name that answers loopback today can
		// answer a public address after a DNS change nobody made here. Every
		// other kind states exactly one thing to bind.
		if addr.Kind() == listenset.KindHost {
			return fmt.Errorf("extra listen address %d (%q) is a host name; give an IP address, or %q for every interface",
				i+1, raw, "*:"+strconv.Itoa(addr.Port()))
		}
		canonical := addr.String()
		if seen[canonical] {
			return fmt.Errorf("extra listen address %d (%q) repeats %s", i+1, raw, canonical)
		}
		seen[canonical] = true
	}
	return nil
}

// The hub-core keys. Domain packages declare their own keys next to the
// code that consumes them (captcha.*, rate_limit.*); these are the keys
// with no closer home.
var (
	// Solo refuses SignUp outright (AuthService.SignUp), so the whole
	// sign-up category is inert there. Both keys carry HiddenInSolo.
	KeySignupEnabled = NewKey[bool]("signup_enabled").
				WithUI(UIMeta{
			Category:     "signup",
			Title:        "Open sign-up",
			Summary:      "whether user sign-up is open",
			HiddenInSolo: true,
			Fields:       []Field{{Name: "", Label: "Open sign-up", Kind: FieldBool}},
		})

	// Solo READS this, so it stays administrable there. A solo hub whose
	// account holds a password authenticates its TCP callers with an ordinary
	// session, and Login mints that session for the duration this key states.
	// The solo rung short-circuits only a credential-free caller.
	KeySessionDurationSeconds = NewKey[int64]("session_duration_seconds").
					WithDefault(DefaultSessionDurationSeconds).
					WithValidate(validateSessionDuration).
					WithUI(UIMeta{
			Category: "general",
			Title:    "Session duration",
			Summary:  "idle session lifetime in seconds (300 to 315360000)",
			Fields: []Field{{
				Name: "", Label: "Session duration", Kind: FieldInt,
				Min:  ptrconv.Ptr(MinSessionDurationSeconds),
				Max:  ptrconv.Ptr(MaxSessionDurationSeconds),
				Unit: "seconds",
			}},
		})

	// Solo READS this too, and hiding it took the answer away from the one
	// deployment that needs it most. Every session cookie a solo hub writes
	// -- from Login, and from the ChangePassword that hands the first
	// password's author a session -- takes its __Host- prefix and its Secure
	// attribute from here. A solo hub published on a LAN behind a TLS proxy
	// must be able to ask for one, and no other key can.
	KeySecureCookies = NewKey[bool]("secure_cookies").
				WithUI(UIMeta{
			Category: "general",
			Title:    "Secure cookies",
			Summary:  "use __Host- prefixed cookies (behind TLS); changing it signs everyone out",
			Fields:   []Field{{Name: "", Label: "Secure cookies", Kind: FieldBool}},
		})

	// Solo reads this too, so the whole general category stays there. A solo
	// hub is not localhost-only: `leapmux solo -listen 0.0.0.0:4327` and the
	// extra_listen_addresses setting both serve a LAN or Tailscale address,
	// and public_url is how that hub tells a REMOTE worker where to dial.
	// It reaches the solo launcher's banner and, as worker_hub_url in
	// GetSystemInfo, the `leapmux worker --hub ...` command that
	// RegisterWorkerDialog prints. Hiding it would take the setting out of
	// the preferences dialog AND out of `leapmux control admin settings`, because
	// HiddenInSolo drops the key from ListSettings.
	KeyPublicURL = NewKey[string]("public_url").
			WithValidate(validatePublicURL).
			WithUI(UIMeta{
			Category: "general",
			Title:    "Public base URL",
			Summary:  "public base URL when running behind a reverse proxy (scheme+host only)",
			Fields: []Field{{
				Name: "", Label: "Public base URL", Kind: FieldString,
				Placeholder: "https://hub.example.com",
			}},
		})

	// Open app registration (RFC 7591). OFF by default, and the default is
	// the decision: an anonymous caller who can create a registration can
	// create a row that appears on a consent screen, which is a phishing
	// surface as much as a convenience.
	//
	// It is NOT hidden in solo. A solo hub authorizes apps like any other --
	// the solo rung yields to a presented bearer, so a scoped credential
	// binds there too -- and an operator who runs `leapmux solo -listen
	// 0.0.0.0:4327` on a LAN has exactly the same decision to make.
	//
	// The metadata document reads it: with the setting off,
	// registration_endpoint is ABSENT from
	// /.well-known/oauth-authorization-server, so a conformant client library
	// does not try and does not report the refusal as a hub failure.
	KeyOpenAppRegistration = NewKey[bool]("open_app_registration").
				WithUI(UIMeta{
			Category: "apps",
			Title:    "Open app registration",
			Summary:  "let any caller register an app through RFC 7591 dynamic registration",
			Fields:   []Field{{Name: "", Label: "Open app registration", Kind: FieldBool}},
		})

	// Solo can send no mail at all, so the relay has nothing to carry.
	// There are two senders: the verification email, which accompanies
	// sign-up and email change (solo refuses both), and the worker registration
	// instructions, which need a verified address that the solo user can
	// never obtain -- bootstrap creates it with an empty email and
	// RequestEmailChange refuses solo.
	KeySMTP = NewKey[SMTPValue]("smtp").
		WithDefault(SMTPValue{Port: 587, TLSMode: SMTPTLSModeSTARTTLS}).
		WithValidate(validateSMTP).
		SecretFields("password").
		WithUI(UIMeta{
			Category:     "email",
			Title:        "SMTP relay",
			Summary:      "SMTP relay configuration; the password lives in the secret half",
			HiddenInSolo: true,
			Fields: []Field{
				{Name: "host", Label: "Host", Kind: FieldString, Placeholder: "smtp.example.com"},
				{Name: "port", Label: "Port", Kind: FieldInt, Min: ptrconv.Ptr[int64](1), Max: ptrconv.Ptr[int64](65535)},
				{Name: "username", Label: "Username", Kind: FieldString},
				{Name: "from_address", Label: "From address", Kind: FieldString, Placeholder: "leapmux@example.com"},
				{Name: "tls_mode", Label: "TLS mode", Kind: FieldEnum, EnumValues: tlsModeEnumValues},
				{Name: "password", Label: "Password", Kind: FieldString, Secret: true},
			},
		})

	// The addresses an administrator publishes the hub on, beside the one
	// -listen gives. Solo only, and HiddenInHub says so: `leapmux hub` and
	// `leapmux dev` already bind every interface by default and already
	// authenticate every caller, so the key would only offer them a way to
	// break a working deployment.
	//
	// HOT, and it has to be. The whole point of the panel is that the hub
	// starts answering on the new address when the operator clicks Apply; a
	// restart-class key would store the intent and serve nothing.
	//
	// The value is a list of strings rather than of a structured address,
	// because listenset.Addr's identity IS its canonical string -- storing the
	// parts would let a stored row state a host and a kind that disagree.
	KeyExtraListenAddresses = NewKey[ExtraListenValue]("extra_listen_addresses").
				WithValidate(validateExtraListen).
				WithUI(UIMeta{
			Category:    "network",
			Title:       "Network access",
			Summary:     "additional addresses this hub accepts connections on, beside the one -listen gives",
			HiddenInHub: true,
			// A WHOLE-VALUE custom editor: one field, no name, so the client
			// owns the document rather than one property of it. The addresses
			// and the password the panel sets are one decision an operator
			// applies together, and a per-field row would offer Apply for half
			// of it. `theme` and `keybindings` carry the same shape.
			//
			// It also has to be whole-value for a duller reason: the schema
			// test refuses FieldCustom on a []string, because a plain list of
			// strings is what FieldStringList already edits.
			Fields: []Field{{
				Name: "", Label: "Network access", Kind: FieldCustom,
				CustomID: "networkAccess",
				Help:     "Every network address asks for a sign-in once the account has a password.",
			}},
		})

	KeyTimeouts = NewKey[TimeoutsValue]("timeouts").
			WithDefault(DefaultTimeouts).
			WithValidate(validateTimeouts).
			WithUI(UIMeta{
			Category: "limits",
			Title:    "Timeouts",
			Summary:  "API/agent-startup/worktree-create timeouts in seconds",
			Fields: []Field{
				{Name: "api_seconds", Label: "API timeout", Kind: FieldInt,
					Min: ptrconv.Ptr[int64](1), Max: ptrconv.Ptr[int64](MaxTimeoutSeconds), Unit: "seconds"},
				{Name: "agent_startup_seconds", Label: "Agent startup timeout", Kind: FieldInt,
					Min: ptrconv.Ptr[int64](1), Max: ptrconv.Ptr[int64](MaxTimeoutSeconds), Unit: "seconds"},
				{Name: "worktree_create_seconds", Label: "Worktree create timeout", Kind: FieldInt,
					Min: ptrconv.Ptr[int64](1), Max: ptrconv.Ptr[int64](MaxTimeoutSeconds), Unit: "seconds"},
			},
		})

	KeyLimits = NewKey[LimitsValue]("limits").
			WithDefault(LimitsValue{MaxConnectionsPerUser: 32, MaxWorkersPerUser: 64}).
			WithValidate(validateLimits).
			WithUI(UIMeta{
			Category: "limits",
			Title:    "Per-user caps",
			Summary:  "per-user connection and worker caps (0 = unlimited)",
			// Both rules are UNIONS -- 0, or at least the floor -- which Min
			// cannot express, so Help states the part the control cannot.
			Fields: []Field{
				{Name: "max_connections_per_user", Label: "Max connections per user", Kind: FieldInt,
					Help: "0 means unlimited; any other value must be at least 4.",
					Min:  ptrconv.Ptr[int64](0), Unit: "count"},
				{Name: "max_workers_per_user", Label: "Max workers per user", Kind: FieldInt,
					Help: "0 means unlimited.",
					Min:  ptrconv.Ptr[int64](0), Unit: "count"},
			},
		})

	KeyMaxMessageSizeBytes = NewKey[int64]("max_message_size_bytes").
				WithDefault(int64(DefaultMaxMessageSizeBytes)).
				WithValidate(validateMaxMessageSize).
				WithUI(UIMeta{
			Category: "advanced",
			Title:    "Maximum message size",
			Summary:  "maximum application payload size (64 KiB - 64 MiB); applies after restart",
			Fields: []Field{{
				Name: "", Label: "Maximum message size", Kind: FieldInt,
				Min: ptrconv.Ptr(int64(contracts.MaxPlaintextPerChunk)),
				Max: ptrconv.Ptr(int64(contracts.MaxConfigurableMessageSize)), Unit: "bytes",
			}},
		}).Restart()

	KeyQueueBudget = NewKey[QueueBudgetValue]("queue_budget").
			WithValidate(validateQueueBudget).
			WithUI(UIMeta{
			Category: "advanced",
			Title:    "Queue budgets",
			Summary:  "memory budgets for the outbound queue pools, in bytes (0 = auto-size); applies after restart",
			// The rule these fields enforce is a UNION -- 0, or at least the
			// class floor -- which Min/Max cannot express, so the declared
			// interval is wider than the validator. Help states the part the
			// control cannot.
			Fields: []Field{
				{Name: "relay_bytes", Label: "Queue budget - relay", Kind: FieldInt, Unit: "bytes",
					Help: queueBudgetHelp,
					Min:  ptrconv.Ptr[int64](0), Max: ptrconv.Ptr[int64](config.MaxQueueMemoryBudget)},
				{Name: "worker_bytes", Label: "Queue budget - worker", Kind: FieldInt, Unit: "bytes",
					Help: queueBudgetHelp,
					Min:  ptrconv.Ptr[int64](0), Max: ptrconv.Ptr[int64](config.MaxQueueMemoryBudget)},
				{Name: "userevents_bytes", Label: "Queue budget - user events", Kind: FieldInt, Unit: "bytes",
					Help: queueBudgetHelp,
					Min:  ptrconv.Ptr[int64](0), Max: ptrconv.Ptr[int64](config.MaxQueueMemoryBudget)},
			},
		}).Restart()

	// MailLimitsValue caps the abuse surfaces of outbound mail. Two knobs,
	// one row: the cooldown a FAILED send leaves behind, and the
	// per-recipient budget that stops one inbox being bombed through many
	// accounts. The mint cooldown (60s, per account) already caps
	// successful sends; these close the paths it cannot see.
	KeyMailLimits = NewKey[MailLimitsValue]("mail_limits").
			WithDefault(DefaultMailLimits).
			WithValidate(validateMailLimits).
			WithUI(UIMeta{
			Category:     "rate-limits",
			Title:        "Mail limits",
			Summary:      "failed-send cooldown and per-recipient mail budget",
			HiddenInSolo: true,
			Fields: []Field{
				{Name: "failure_cooldown_seconds", Label: "Failed-send cooldown", Kind: FieldInt,
					Help: "how long a failed send blocks the next verification or recovery mail (0 = no block; at most the 60s resend cooldown).",
					Min:  ptrconv.Ptr[int64](0), Max: ptrconv.Ptr[int64](60), Unit: "seconds"},
				{Name: "recipient_max", Label: "Per-recipient max", Kind: FieldInt,
					Help: "most mails one recipient address gets per window (0 = unlimited).",
					Min:  ptrconv.Ptr[int64](0), Max: ptrconv.Ptr[int64](1000), Unit: "count"},
				{Name: "recipient_window_seconds", Label: "Per-recipient window", Kind: FieldInt,
					Min: ptrconv.Ptr[int64](60), Max: ptrconv.Ptr[int64](86400), Unit: "seconds"},
			},
		})
)

// CoreDescriptors lists the hub-core keys for manager registration.
//
// The order IS the display order: the admin CLI and the preferences
// dialog both render the descriptors as the manager reports them, and
// nothing sorts. A key moved in this list moves in both surfaces.
func CoreDescriptors() []Descriptor {
	return []Descriptor{
		KeySignupEnabled,
		KeyPublicURL,
		KeySecureCookies,
		KeySessionDurationSeconds,
		KeyOpenAppRegistration,
		KeyExtraListenAddresses,
		KeySMTP,
		KeyTimeouts,
		KeyLimits,
		KeyMailLimits,
		KeyMaxMessageSizeBytes,
		KeyQueueBudget,
	}
}

// EmailVerificationEffective is true when SMTP is configured. The database
// stores the true verification state; this gate controls runtime access.
func EmailVerificationEffective(s *Snapshot) bool {
	return KeySMTP.Of(s).Enabled()
}

// SignupEnabledEffective applies the dev-mode default at read time: dev
// mode runs the full multi-user path with open signup as the dev-friendly
// default, but a stored row (an explicit `settings set`) always wins.
// This function resolves it here rather than seeding a row at startup, so
// the key keeps reporting customized=false in dev — a row stays an operator
// write — and so the hub never persists dev-ness into a database another
// hub shares.
func SignupEnabledEffective(s *Snapshot, devMode bool) bool {
	if devMode && !s.Customized(KeySignupEnabled) {
		return true
	}
	return KeySignupEnabled.Of(s)
}

// BaseURL derives the hub's public base URL: the configured public_url,
// else scheme-from-secure_cookies plus the listen host. listen is the
// process's bootstrap listen address, so it is a parameter rather than a
// setting -- it specifies the socket the OS handed this process, not a fact
// about the hub instance.
//
// A WILDCARD bind answers on every address the machine holds, so no single
// host derives from it and localhost is the one the hub's own links can
// use. All three spellings resolve that way -- ":4327", "0.0.0.0:4327" and
// "[::]:4327". Only ":4327" did before, and the other two produced
// "http://0.0.0.0:4327": an address no browser can open, printed into every
// verification and account-recovery mail, into the device-code
// verification_uri the CLI displays, and registered as the OAuth
// redirect_uri. It is the same assumption the rest of the hub already makes
// for a wildcard bind -- webauthn.servesLoopback accepts one as a loopback
// deployment, and the captcha secure-context gate reads it the same way.
//
// An EMPTY listen is different and stays hostless. It means there is no TCP
// listener at all (the desktop app reaches its hub over a local socket), so
// there is no browser-reachable address to invent. RPConfigFromSettings
// depends on that: an empty host is how passkeys report themselves cleanly
// unavailable instead of running against a fabricated origin.
//
// A deployment that browsers really reach by a LAN address or a hostname
// must set public_url. That is not an extra demand: without it, passkeys
// already refuse every non-loopback origin.
func BaseURL(s *Snapshot, listen string) string {
	if u := KeyPublicURL.Of(s); u != "" {
		return u
	}
	scheme := "http"
	if KeySecureCookies.Of(s) {
		scheme = "https"
	}
	return scheme + "://" + browserHostForListen(listen)
}

// browserHostForListen is the authority a browser reaches this hub at,
// derived from the bind address. A WILDCARD bind answers on every address
// the machine holds, and localhost is the one the hub's own links can use.
// httpsec.IsWildcardHost answers for every spelling of it, so "[::0]:4327"
// and "0:0:0:0:0:0:0:0:4327" resolve the same way ":4327" does.
//
// An empty listen returns empty, which is not an oversight: it means no TCP
// listener exists, and a caller that gets no host reports passkeys and mail
// links as unavailable rather than pointing them somewhere invented. An
// empty HOST with a port (":4327") is the wildcard, not the absent listener,
// which is why the two are separate branches.
func browserHostForListen(listen string) string {
	host := strings.TrimSpace(listen)
	if host == "" {
		return ""
	}
	// Split the port off so this function can recognise the wildcard host.
	// The split and the wildcard test both live in httpsec, which the
	// captcha gate and the passkey origin resolution also use: one
	// definition of "what host does this bind address mean to a browser",
	// rather than three that agree only by comment.
	hostPart, port := httpsec.SplitBindHostPort(host)
	if hostPart == "" || httpsec.IsWildcardHost(hostPart) {
		return "localhost" + port
	}
	return host
}
