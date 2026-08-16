package settings

import (
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/internal/hub/config"
)

// DefaultSessionDuration mirrors auth.DefaultSessionDuration (7 days) and
// MinSessionDuration (5 minutes); expressed in seconds here because the
// stored document is seconds.
const (
	DefaultSessionDurationSeconds int64 = 7 * 24 * 60 * 60
	MinSessionDurationSeconds     int64 = 5 * 60
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

// SessionDuration reads session_duration as a time.Duration.
func SessionDuration(s *Snapshot) time.Duration {
	secs := KeySessionDurationSeconds.Of(s)
	if secs < MinSessionDurationSeconds {
		return time.Duration(MinSessionDurationSeconds) * time.Second
	}
	return time.Duration(secs) * time.Second
}

// SMTPValue is the smtp key's public shape; the password lives in the
// encrypted half. A zero Host means email is disabled — the single fact
// every consumer (GetSystemInfo, the sender, verification gating) asks
// for.
type SMTPValue struct {
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	Username    string `json:"username,omitempty"`
	FromAddress string `json:"from_address,omitempty"`
	TLSMode     string `json:"tls_mode,omitempty"`
	Password    string `json:"password,omitempty"`
}

// Enabled reports whether SMTP is configured at all.
func (v SMTPValue) Enabled() bool { return v.Host != "" }

// validateSMTP ports the SMTP cross-field rules from the old
// config.Validate. The tls_mode enum check runs even when the host is
// still empty, so a typo in a piecemeal-staged document fails at the
// write that introduced it rather than at the later host write that
// first makes it reachable.
func validateSMTP(v SMTPValue) error {
	switch v.TLSMode {
	case SMTPTLSModeSTARTTLS, SMTPTLSModeImplicit, SMTPTLSModeNone:
	default:
		return fmt.Errorf("unsupported smtp tls mode %q (supported: %s, %s, %s)",
			v.TLSMode, SMTPTLSModeSTARTTLS, SMTPTLSModeImplicit, SMTPTLSModeNone)
	}
	if v.Host == "" {
		// Email disabled: the other fields may still be staged (an admin
		// configuring piecemeal), but nothing inconsistent is possible.
		return nil
	}
	if v.Port < 1 || v.Port > 65535 {
		return fmt.Errorf("smtp port must be between 1 and 65535 (got %d)", v.Port)
	}
	if _, err := mail.ParseAddress(v.FromAddress); err != nil {
		return fmt.Errorf("smtp from address %q is not a valid email", v.FromAddress)
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
type QueueBudgetValue struct {
	RelayBytes      int64 `json:"relay_bytes,omitempty"`
	WorkerBytes     int64 `json:"worker_bytes,omitempty"`
	UserEventsBytes int64 `json:"userevents_bytes,omitempty"`
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
	return nil
}

// validateTimeouts keeps every budget strictly positive; a zero timeout
// would fail every call it applies to.
func validateTimeouts(v TimeoutsValue) error {
	if v.APITimeoutSeconds < 1 {
		return fmt.Errorf("api timeout must be at least 1s (got %ds)", v.APITimeoutSeconds)
	}
	if v.AgentStartupTimeoutSeconds < 1 {
		return fmt.Errorf("agent startup timeout must be at least 1s (got %ds)", v.AgentStartupTimeoutSeconds)
	}
	if v.WorktreeCreateSecs < 1 {
		return fmt.Errorf("worktree create timeout must be at least 1s (got %ds)", v.WorktreeCreateSecs)
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
	for name, b := range map[string]int64{
		"relay_bytes":      v.RelayBytes,
		"worker_bytes":     v.WorkerBytes,
		"userevents_bytes": v.UserEventsBytes,
	} {
		if b < 0 {
			return fmt.Errorf("queue budget %s must not be negative (got %d)", name, b)
		}
		floor, err := config.QueueBudgetFloor(name)
		if err != nil {
			return err
		}
		if b > 0 && b < floor {
			return fmt.Errorf("queue budget %s must be 0 (auto-size) or at least %d bytes (got %d)", name, floor, b)
		}
		if b > config.MaxQueueMemoryBudget {
			return fmt.Errorf("queue budget %s exceeds the %d byte ceiling (got %d)", name, config.MaxQueueMemoryBudget, b)
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
	KeySignupEnabled = NewKey[bool]("signup_enabled").
				WithDoc("whether user sign-up is open", "boolean")

	KeyEmailVerificationRequired = NewKey[bool]("email_verification_required").
					WithDoc("require verified email before sign-in (needs smtp configured)", "boolean")

	KeySessionDurationSeconds = NewKey[int64]("session_duration_seconds").
					WithDefault(DefaultSessionDurationSeconds).
					WithValidate(validateSessionDuration).
					WithDoc("idle session lifetime in seconds (minimum 300)", "integer")

	KeySecureCookies = NewKey[bool]("secure_cookies").
				WithDoc("use __Host- prefixed cookies (behind TLS); changing it signs everyone out", "boolean")

	KeyPublicURL = NewKey[string]("public_url").
			WithValidate(validatePublicURL).
			WithDoc("public base URL when running behind a reverse proxy (scheme+host only)", "string")

	KeySMTP = NewKey[SMTPValue]("smtp").
		WithDefault(SMTPValue{Port: 587, TLSMode: SMTPTLSModeSTARTTLS}).
		WithValidate(validateSMTP).
		SecretFields("password").
		WithDoc("SMTP relay configuration; the password lives in the secret half",
			`{"host", "port", "username", "from_address", "tls_mode"} + secret {"password"}`)

	KeyTimeouts = NewKey[TimeoutsValue]("timeouts").
			WithDefault(DefaultTimeouts).
			WithValidate(validateTimeouts).
			WithDoc("API/agent-startup/worktree-create timeouts in seconds",
			`{"api_seconds", "agent_startup_seconds", "worktree_create_seconds"}`)

	KeyLimits = NewKey[LimitsValue]("limits").
			WithDefault(LimitsValue{MaxConnectionsPerUser: 32, MaxWorkersPerUser: 64}).
			WithValidate(validateLimits).
			WithDoc("per-user connection and worker caps (0 = unlimited)",
			`{"max_connections_per_user", "max_workers_per_user"}`)

	KeyMaxMessageSizeBytes = NewKey[int64]("max_message_size_bytes").
				WithDefault(int64(DefaultMaxMessageSizeBytes)).
				WithValidate(validateMaxMessageSize).
				WithDoc("maximum application payload size (64 KiB - 64 MiB); applies after restart", "integer").
				Restart()

	KeyQueueBudget = NewKey[QueueBudgetValue]("queue_budget").
			WithValidate(validateQueueBudget).
			WithDoc("outbound queue memory pool budgets in bytes (0 = auto-size); applies after restart",
			`{"relay_bytes", "worker_bytes", "userevents_bytes"}`).
		Restart()
)

// CoreDescriptors lists the hub-core keys for manager registration, in a
// stable order.
func CoreDescriptors() []Descriptor {
	return []Descriptor{
		KeySignupEnabled,
		KeyEmailVerificationRequired,
		KeySessionDurationSeconds,
		KeySecureCookies,
		KeyPublicURL,
		KeySMTP,
		KeyTimeouts,
		KeyLimits,
		KeyMaxMessageSizeBytes,
		KeyQueueBudget,
	}
}

// SMTPConfigured is the cross-key rule the old config enforced at
// startup: requiring email verification without an SMTP relay would lock
// every signup behind an email that can never be sent.
func SMTPConfigured(s *Snapshot) error {
	if !KeyEmailVerificationRequired.Of(s) {
		return nil
	}
	if !KeySMTP.Of(s).Enabled() {
		return fmt.Errorf("email_verification_required=true needs smtp host to be configured first")
	}
	return nil
}

// EmailVerificationEffective applies the SMTPConfigured rule at read
// time: a state that somehow bypassed the write-path validation (direct
// SQL) degrades to "verification off" rather than locking every signup
// behind an email that can never be sent.
func EmailVerificationEffective(s *Snapshot) bool {
	return KeyEmailVerificationRequired.Of(s) && KeySMTP.Of(s).Enabled()
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
// setting — it names the socket the OS handed this process, not a fact
// about the hub instance.
func BaseURL(s *Snapshot, listen string) string {
	if u := KeyPublicURL.Of(s); u != "" {
		return u
	}
	scheme := "http"
	if KeySecureCookies.Of(s) {
		scheme = "https"
	}
	host := listen
	if strings.HasPrefix(host, ":") {
		host = "localhost" + host
	}
	return scheme + "://" + host
}
