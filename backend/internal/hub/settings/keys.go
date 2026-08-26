package settings

import (
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/httpsec"
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
// (GetSystemInfo, the sender, verification gating) asks for: it needs the
// host AND the from address, which are the two fields a relay cannot be
// used without and neither of which has a usable default.
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
// error naming a field the operator has not reached yet.
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
// enforces. All hot: api_seconds is read per request by the timeout
// interceptor, the worker-facing pair via GetTimeouts.
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
// Both are guards, not quotas — the defaults are meant never to be
// reached by a person. The connection cap limits how many long-lived
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
// restart-class: pool minimum floors are derived from them (and from the
// max message size) at startup, before any pool exists. A zero field
// means auto-size from the process's own memory limit, so multi-instance
// hubs on heterogeneous machines keep per-process budgets.
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

// DefaultMaxMessageSizeBytes is the default application payload budget;
// channelwire owns the number and channelwire.ResolveMaxMessageSize stays
// the one resolver.
const DefaultMaxMessageSizeBytes = channelwire.MaxMessageSize

// validatePublicURL ports the old canonical public_url rule: scheme+host
// only, nothing else — the hub appends its own routes. A trailing slash,
// userinfo, query, or fragment is refused rather than normalized or
// ignored: BaseURL returns the stored string verbatim, and any of those
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

// validateLimits ports the connection-cap floor: four is the least that
// can be called working (an active tab holds two sockets), and zero means
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

	// Solo mints no session: the auth interceptor authenticates every
	// procedure as the synthetic solo user and refreshes nothing, and the
	// bootstrapped solo user has no password hash, so Login cannot
	// succeed. Nothing reads this duration there.
	KeySessionDurationSeconds = NewKey[int64]("session_duration_seconds").
					WithDefault(DefaultSessionDurationSeconds).
					WithValidate(validateSessionDuration).
					WithUI(UIMeta{
			Category:     "general",
			Title:        "Session duration",
			Summary:      "idle session lifetime in seconds (300 to 315360000)",
			HiddenInSolo: true,
			Fields: []Field{{
				Name: "", Label: "Session duration", Kind: FieldInt,
				Min:  ptrconv.Ptr(MinSessionDurationSeconds),
				Max:  ptrconv.Ptr(MaxSessionDurationSeconds),
				Unit: "seconds",
			}},
		})

	// Solo sets and reads no cookie -- the solo rung precedes the cookie
	// rung in every auth ladder. Its other job, the scheme that BaseURL
	// derives, reaches only the mail renderer and the /auth/cli/*
	// endpoints, and both are unreachable in solo (no mail recipient, and
	// the CLI endpoints accept cookies only). KeyPublicURL below does NOT
	// go through BaseURL, so hiding this one cannot affect it.
	KeySecureCookies = NewKey[bool]("secure_cookies").
				WithUI(UIMeta{
			Category:     "general",
			Title:        "Secure cookies",
			Summary:      "use __Host- prefixed cookies (behind TLS); changing it signs everyone out",
			HiddenInSolo: true,
			Fields:       []Field{{Name: "", Label: "Secure cookies", Kind: FieldBool}},
		})

	// The one general-category key that STAYS in solo, which is why that
	// category has no whole-category hide. A solo hub is not localhost-only:
	// `leapmux solo -listen 0.0.0.0:4327` serves a LAN or Tailscale address,
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

	// Solo can send no mail at all, so the relay has nothing to carry.
	// There are two senders: the verification email, which rides sign-up
	// and email change (solo refuses both), and the worker registration
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
				Min: ptrconv.Ptr(int64(channelwire.MaxPlaintextPerChunk)),
				Max: ptrconv.Ptr(int64(channelwire.MaxConfigurableMessageSize)), Unit: "bytes",
			}},
		}).Restart()

	KeyQueueBudget = NewKey[QueueBudgetValue]("queue_budget").
			WithValidate(validateQueueBudget).
			WithUI(UIMeta{
			Category: "advanced",
			Title:    "Queue budgets",
			Summary:  "outbound queue memory pool budgets in bytes (0 = auto-size); applies after restart",
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
		KeySMTP,
		KeyTimeouts,
		KeyLimits,
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
// Resolved here rather than seeded as a row at startup so the key keeps
// reporting customized=false in dev — a row stays an operator write — and
// so dev-ness is never persisted into a database another hub shares.
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
// name. All three spellings resolve that way -- ":4327", "0.0.0.0:4327" and
// "[::]:4327". Only ":4327" did before, and the other two produced
// "http://0.0.0.0:4327": an address no browser can open, printed into every
// verification and password-reset mail, into the device-code
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
// the machine holds, and localhost is the one the hub's own links can name.
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
	// Split the port off so the wildcard host can be recognised. The split
	// and the wildcard test both live in httpsec, which is the leaf the
	// captcha gate and the passkey origin resolution reach for as well: one
	// definition of "what host does this bind address mean to a browser",
	// rather than three that agree only by comment.
	hostPart, port := httpsec.SplitBindHostPort(host)
	if hostPart == "" || httpsec.IsWildcardHost(hostPart) {
		return "localhost" + port
	}
	return host
}
