package config

import (
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/knadh/koanf/v2"
	"github.com/leapmux/leapmux/channelwire"
	internalconfig "github.com/leapmux/leapmux/internal/config"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/util/memlimit"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/util/validate"
)

const (
	defaultListen     = ":4327"
	defaultConfigDir  = "~/.config/leapmux/hub"
	defaultConfigFile = "~/.config/leapmux/hub/hub.yaml"
	defaultLogLevel   = "info"
)

// Default timeout values (in seconds).
const (
	// The Hub's outbound queues are budgeted in THREE pools -- browser channel
	// relays, worker Connect streams, and user-event subscribers -- rather than
	// one shared total.
	//
	// Sharing would let one class's backlog reclaim memory from another, and the
	// three failures are not interchangeable. Dropping a channel relay costs a
	// reconnect plus a Noise re-handshake per channel; dropping a worker takes
	// every user's channels on that machine with it and has no replay in the
	// frontend->worker direction; dropping a user-event subscriber costs a
	// reconnect and a delta resume, and before its bootstrap frame is even on
	// the wire it costs a snapshot instead of a delta, with no disconnect at
	// all. A budget is the wrong place to make those compete, so the blast
	// radius is bounded by construction instead.
	//
	// The *QueueMemoryShare constants are the shares of the process's memory
	// basis each pool auto-sizes to, in QueueMemoryShareDenominator-ths.
	// Together they come to a quarter of the basis.
	//
	// Relay gets the most because it carries the bulk direction -- terminal
	// output and agent events, one frame per PTY read -- where a Worker stream
	// carries keystrokes, RPCs and control frames.
	//
	// User events gets an equal share to Worker despite carrying the least
	// traffic, because throughput is not what binds it. Its steady-state frames
	// are small AND shared -- one buffer charged once however many subscribers
	// hold it -- but its worst case is the frame that OPENS a connection: an
	// account's whole visible state, unique per subscriber, up to
	// channelwire.UserEventsReadLimit and charged crdt.ResidentFactor times that
	// because the proto tree stays alive beside the marshaled buffer. At a
	// thirty-second of an 8 GiB basis that was eight such frames in flight, and
	// the moment they all arrive at once is a Hub restart -- the reconnect storm
	// this whole mechanism exists for. The shed is still the cheapest in the
	// Hub, but it is paid by retry backoff, and there is no eviction in that
	// pool to clear the condition any sooner.
	//
	// A numerator over a shared denominator rather than a float divisor: the
	// arithmetic on a byte count stays exact, and the startup log can name the
	// share as "4/32" instead of the "1/8" a reciprocal would print for one
	// class and "1/16" for another. The denominator is 32 rather than 16 because
	// every slice user events has gained came out of relay's share and left
	// Worker untouched at the 1/16 it has always had; a coarser denominator
	// would have forced the two to move together.
	QueueMemoryShareDenominator = 32
	RelayQueueMemoryShare       = 4
	WorkerQueueMemoryShare      = 2
	UserEventsQueueMemoryShare  = 2

	// MinQueueMembersAtFloor is how many connections a budget must be able to
	// hold at their full guaranteed working set for the admission rule to mean
	// anything.
	//
	// Below it the pool is structurally degenerate rather than merely small:
	// floor() clamps up to sendq.DefaultMinFloor, so a budget at or under one
	// floor pins the threshold at that floor for EVERY occupancy, the dynamic
	// Capacity-minus-used branch never engages, and total resident becomes
	// members x floor with nothing bounding it -- the per-connection-times-
	// unbounded-connections shape these pools exist to remove. A 1 MiB relay
	// budget did exactly that and passed validation.
	MinQueueMembersAtFloor = 4
	// DefaultMaxConnectionsPerUser bounds how many long-lived connections one
	// user may hold at once -- every /ws/channel and /ws/userevents socket
	// authenticated as them, wherever it came from.
	//
	// The queue pools bound the BYTES each class of connection may hold, but a
	// pool member is a connection, not a user: a member's guaranteed working set
	// multiplies by a connection count nothing limits, so "open more tabs" is how
	// a client reaches memory the pools cannot refuse.
	//
	// It bounds ONE USER's contribution, not the pool's member count -- 32 users
	// holding one socket each reach 32 members without this ever being
	// consulted. What keeps that solvent is the per-class minimum: the
	// user-events budget is clamped up to 2 x (UserEventsReadLimit + headroom),
	// which is 32 MiB + 8 KiB on every basis, against 32 x DefaultMinFloor =
	// 32 MiB of guaranteed floors. The aggregate signal, for the cases arithmetic
	// cannot promise, is sendq.Pool's overcommits counter.
	//
	// A guard, not a quota: 32 is meant never to be reached by a person. Note it
	// counts SOCKETS -- an active tab holds two, /ws/userevents plus /ws/channel
	// once it opens one -- and that a CLI remote session, a worker relaying
	// user events, and the desktop sidecar all authenticate as the same user. In
	// solo mode every one of those is the single solo user.
	DefaultMaxConnectionsPerUser = 32
	// MinConnectionsPerUser is the smallest cap an operator may configure.
	//
	// Below it the setting stops being a cap and becomes an outage. An active
	// tab holds two sockets, so at two the first tab connects and every other
	// client the same user runs -- a second tab, the desktop sidecar, a CLI
	// remote session -- is refused; at one the first tab cannot finish
	// connecting at all. Four leaves room for a tab plus one other client,
	// which is the least that can be called working.
	MinConnectionsPerUser = 4
	// DefaultMaxWorkersPerUser bounds how many Workers one user may have
	// registered at once.
	//
	// The same missing term as MaxConnectionsPerUser, for the third pool. A
	// Worker's Connect stream is a member of the worker pool and is guaranteed
	// the same unrefusable working set, but it takes no lease, so the
	// connection cap does not see it -- and nothing else bounded the count:
	// registration keys have a five-minute TTL and no quota, so N keys mint N
	// Workers, each one a member with a floor the pool may not reclaim.
	//
	// Bounded at BOTH ends, because neither alone is the count that matters.
	// Registration refuses past the cap where an operator can act on the error;
	// workermgr.Manager refuses past it again where the pool member is actually
	// created. Registrations alone are not membership: the cap counts rows with
	// status ACTIVE, while a Connect stream is served for any row that is not
	// DELETED -- and a DEREGISTERING row keeps its stream, since that stream is
	// how the Worker is told to stop. So register/deregister cycles grew pool
	// membership without ever exceeding the cap.
	//
	// A guard, not a quota, like its sibling: 64 machines is far past what an
	// account plausibly runs, and the failure it prevents is the worker pool --
	// whose eviction is the most expensive of the three, taking every user's
	// channels on that machine -- being promised more floors than it has bytes.
	DefaultMaxWorkersPerUser = 64
	// MaxQueueMemoryBudget caps each auto-sized pool on very large hosts, where
	// a bigger ceiling is more queue than any client population can fill and
	// only delays a reclaim that should already have happened. Operators who
	// genuinely want more set the key.
	MaxQueueMemoryBudget = 8 << 30 // 8 GiB

	// The timeout defaults. Each is the value that its flag registers and the
	// value that applyDurationDefaults restores when a source leaves the key at
	// zero, so each default is one number and not one per reader.
	//
	// The session duration is not here: auth.DefaultSessionDuration is the one
	// default, because the auth package also has to answer for a session that a
	// caller mints with no configured lifetime.
	DefaultAPITimeout            = 10 * time.Second
	DefaultAgentStartupTimeout   = 5 * time.Minute
	DefaultWorktreeCreateTimeout = time.Minute
)

// Config holds the hub's runtime configuration.
type Config struct {
	Listen                    string `koanf:"listen"`
	LocalListen               string `koanf:"local_listen"`
	PublicURL                 string `koanf:"public_url"`
	DataDir                   string `koanf:"data_dir"`
	DevFrontend               string `koanf:"dev_frontend"`
	LogLevel                  string `koanf:"log_level"`
	SignupEnabled             bool   `koanf:"signup_enabled"`
	EmailVerificationRequired bool   `koanf:"email_verification_required"`
	SmtpHost                  string `koanf:"smtp_host"`
	SmtpPort                  int    `koanf:"smtp_port"`
	SmtpUsername              string `koanf:"smtp_username"`
	SmtpPassword              string `koanf:"smtp_password"`
	SmtpFromAddress           string `koanf:"smtp_from_address"`
	SmtpTLSMode               string `koanf:"smtp_tls_mode"` // See SmtpTLSMode* constants for valid values.
	// The durations below, and every other duration key, accept a unit suffix
	// and read a bare number as seconds -- see internalconfig.ParseDuration.
	// LoadWithOptions resolves a zero to the matching Default* constant, so a
	// loaded Config always carries a usable value and no reader repeats the
	// fallback.
	//
	// SessionDuration is how long a session stays valid after the user's last
	// request. Each authenticated request slides the expiry forward, so it is an
	// idle timeout and not a cap on how long a signed-in user may stay signed in.
	SessionDuration       time.Duration `koanf:"session_duration"`
	APITimeout            time.Duration `koanf:"api_timeout"`
	AgentStartupTimeout   time.Duration `koanf:"agent_startup_timeout"`
	WorktreeCreateTimeout time.Duration `koanf:"worktree_create_timeout"`
	MaxMessageSize        int           `koanf:"max_message_size"`
	// The queue budgets are int64, not int, for the reason sendq.PoolConfig
	// states about its own Capacity: MaxQueueMemoryBudget is 8 GiB, which does
	// not fit an int on a 32-bit build. Typed narrower here, an operator's
	// explicit multi-gigabyte budget would wrap BEFORE the widening conversion
	// ever saw it -- landing negative (so `configured > 0` is false and the key
	// is silently ignored) or positive-but-wrong. Widening one end of a value's
	// journey and not the other is how that goes unnoticed.
	RelayQueueMemoryBudget      int64 `koanf:"relay_queue_memory_budget"`
	WorkerQueueMemoryBudget     int64 `koanf:"worker_queue_memory_budget"`
	UserEventsQueueMemoryBudget int64 `koanf:"userevents_queue_memory_budget"`
	// MaxConnectionsPerUser is used as read -- there is no resolver, because
	// unlike the queue budgets its default is a constant rather than something
	// derived from the machine. That lets the flag default BE the default, which
	// leaves 0 free to mean exactly one thing: unlimited.
	MaxConnectionsPerUser int64 `koanf:"max_connections_per_user"`
	// MaxWorkersPerUser is the same bound for the third pool -- see
	// DefaultMaxWorkersPerUser -- and is read the same way, with no resolver.
	MaxWorkersPerUser int64         `koanf:"max_workers_per_user"`
	SecureCookies     bool          `koanf:"secure_cookies"`
	EncryptionKeyPath string        `koanf:"encryption_key_path"`
	Storage           StorageConfig `koanf:"storage"`
	SoloMode          bool
	DevMode           bool              // Dev mode: non-solo but with auto-bootstrapped admin
	Extras            map[string]string // Extra flag values not in the hub Config struct

}

// SMTP TLS mode constants for SmtpTLSMode.
//
// SmtpTLSModeSTARTTLS is the default and most common production setting:
// connect in plaintext, then upgrade to TLS via the STARTTLS extension
// (typically port 587). SmtpTLSModeImplicit dials TLS directly (port 465,
// the legacy "SMTPS" pattern). SmtpTLSModeNone is plaintext-only and
// should normally only be used for trusted localhost relays — the
// validation layer rejects it for non-localhost relays that require auth
// because Go's smtp.PlainAuth refuses to send credentials over an
// unencrypted, non-localhost connection.
const (
	SmtpTLSModeSTARTTLS = "starttls"
	SmtpTLSModeImplicit = "implicit"
	SmtpTLSModeNone     = "none"
)

// validSmtpTLSModes is the display string for valid smtp_tls_mode values.
const validSmtpTLSModes = "starttls, implicit, none"

// StorageType identifies a storage backend.
type StorageType string

// Storage type constants for StorageConfig.Type.
const (
	StorageTypeSQLite      StorageType = "sqlite"
	StorageTypePostgres    StorageType = "postgres"
	StorageTypeMySQL       StorageType = "mysql"
	StorageTypeCockroachDB StorageType = "cockroachdb"
	StorageTypeYugabyteDB  StorageType = "yugabytedb"
	StorageTypeTiDB        StorageType = "tidb"
)

// validStorageTypes is the display string for valid storage.type values.
const validStorageTypes = "sqlite, postgres, mysql, cockroachdb, yugabytedb, tidb"

// StorageConfig holds the storage backend configuration.
type StorageConfig struct {
	Type        StorageType    `koanf:"type"` // See StorageType* constants for valid values.
	SQLite      SQLiteConfig   `koanf:"sqlite"`
	Postgres    PostgresConfig `koanf:"postgres"`
	MySQL       MySQLConfig    `koanf:"mysql"`
	CockroachDB PostgresConfig `koanf:"cockroachdb"` // CockroachDB reuses the PostgreSQL provider.
	YugabyteDB  PostgresConfig `koanf:"yugabytedb"`  // YugabyteDB reuses the PostgreSQL provider.
	TiDB        MySQLConfig    `koanf:"tidb"`        // TiDB reuses the MySQL provider.
}

// SQLiteConfig holds SQLite-specific storage configuration.
type SQLiteConfig struct {
	Path      string `koanf:"path"`       // Database file path. Default: "{data_dir}/hub.db".
	MaxConns  int    `koanf:"max_conns"`  // Maximum open connections. Default: 4.
	CacheSize int    `koanf:"cache_size"` // Page cache size (negative = KiB, positive = pages). Default: SQLite default (-2000 = 2 MiB).
	MmapSize  int    `koanf:"mmap_size"`  // Memory-mapped I/O size in bytes. Default: 0 (disabled).
}

// PostgresConfig holds PostgreSQL-specific storage configuration.
// Also used by CockroachDB and YugabyteDB (wire-compatible).
type PostgresConfig struct {
	DSN      string `koanf:"dsn"`       // Connection string (required).
	MaxConns int    `koanf:"max_conns"` // Maximum open connections. Default: 25.
	MinConns int    `koanf:"min_conns"` // Minimum pool connections kept alive. Default: 5.
	// The pool durations keep their own meaning for zero -- leave the driver
	// default alone -- so applyDurationDefaults does not touch them.
	ConnMaxLifetime   time.Duration `koanf:"conn_max_lifetime"`   // Maximum connection lifetime. Default: 1h.
	MaxConnIdleTime   time.Duration `koanf:"max_conn_idle_time"`  // Maximum idle time per connection. Default: 5m.
	HealthCheckPeriod time.Duration `koanf:"health_check_period"` // Pool health check period. Default: 30s.
}

// MySQLConfig holds MySQL-specific storage configuration.
// Also used by TiDB (wire-compatible).
type MySQLConfig struct {
	DSN          string `koanf:"dsn"`            // Connection string (required).
	MaxConns     int    `koanf:"max_conns"`      // Maximum open connections. Default: 25.
	MaxIdleConns int    `koanf:"max_idle_conns"` // Maximum idle connections. Default: 5.
	// Zero leaves the driver default alone -- see PostgresConfig.
	ConnMaxLifetime time.Duration `koanf:"conn_max_lifetime"`  // Maximum connection lifetime. Default: 1h.
	ConnMaxIdleTime time.Duration `koanf:"conn_max_idle_time"` // Maximum idle time per connection. Default: 5m.
}

// applyDurationDefaults restores the default for each timeout that a source
// left at zero, so every reader of a loaded Config finds a usable value and
// none of them repeats the fallback.
//
// A source reaches zero two ways: it omits the key, and the defaults map has
// already answered; or it writes an explicit 0, which the operator docs define
// as "the default". A negative value never arrives here, because Validate
// rejects it first.
//
// The storage pool durations are deliberately absent. Zero has its own meaning
// there -- leave the driver default alone -- and the pool code tests for it.
func (c *Config) applyDurationDefaults() {
	for _, d := range []struct {
		target   *time.Duration
		fallback time.Duration
	}{
		{&c.SessionDuration, auth.DefaultSessionDuration},
		{&c.APITimeout, DefaultAPITimeout},
		{&c.AgentStartupTimeout, DefaultAgentStartupTimeout},
		{&c.WorktreeCreateTimeout, DefaultWorktreeCreateTimeout},
	} {
		if *d.target == 0 {
			*d.target = d.fallback
		}
	}
}

// The Resolve*QueueMemoryBudget accessors return the bytes each outbound queue
// pool may hold, plus a human-readable account of where the figure came from
// for the startup log.
//
// An explicit key wins outright. Otherwise the budget is derived from `basis`,
// the process's memory limit, because a byte constant is wrong at both ends of
// the hardware range: the same default has to be safe in a 512 MiB container
// and not needlessly stingy on a 256 GiB host. See internal/util/memlimit for
// the precedence between GOMEMLIMIT, cgroup limits and physical memory.
//
// The basis is a PARAMETER, and all three take the same one. Three shares of
// ONE number is the invariant the x/32 denominator exists to express, and as an
// accessor each call re-probed -- making it three shares of three independently
// observed bases, equal only because nothing moved between the reads. A memo
// made them equal again; a parameter makes re-probing inexpressible, and it
// keeps a configuration value object from reading /proc at all. The caller
// probes once (memlimit.Detect) and names the same figure in the startup log
// beside the budgets that divide it -- which is why QueueMemoryBudget.Source
// says "basis" rather than rendering it.
func (c *Config) ResolveRelayQueueMemoryBudget(basis memlimit.Basis) QueueMemoryBudget {
	return c.relayQueueClass().resolve(basis)
}

func (c *Config) ResolveWorkerQueueMemoryBudget(basis memlimit.Basis) QueueMemoryBudget {
	return c.workerQueueClass().resolve(basis)
}

func (c *Config) ResolveUserEventsQueueMemoryBudget(basis memlimit.Basis) QueueMemoryBudget {
	return c.userEventsQueueClass().resolve(basis)
}

// QueueMemoryBudget is one outbound queue pool's resolved sizing, plus a
// human-readable account of where the figure came from for the startup log.
//
// Capacity and MaxFrame travel together because the pool needs both and they
// answer different questions: Capacity is what every member shares, MaxFrame is
// what ONE of them must always be able to place. Returning only the total left
// the second number stranded in this package while the pool was built with
// sendq's generic defaults -- see PoolConfig.
type QueueMemoryBudget struct {
	// Name is the pool's identity at /metrics, and the key this budget is logged
	// under at startup -- see queueClass.name.
	Name string
	// Capacity is the total resident bytes this pool's members may hold.
	Capacity int64
	// MaxFrame is the largest single frame this class can produce, measured the
	// way the pool CHARGES it rather than as a payload size.
	MaxFrame int64
	// Reclaim is what this class's pool does under pressure -- see the class.
	Reclaim sendq.ReclaimPolicy
	// Source explains the Capacity for the startup log: the figure, whether it
	// was configured or auto-sized, and for an auto-sized one which share of the
	// memory basis produced it and whether a clamp moved it.
	//
	// It NAMES the basis rather than rendering it, because the basis belongs to
	// the process and not to any one budget -- the line that carries these
	// carries the basis once beside them (hub.logQueueMemoryBudgets). Rendering
	// it here printed the same figure three times per startup, and, on a host
	// whose cgroup probe failed, the same diagnosis three times inside one log
	// line.
	Source string
}

// PoolConfig renders the budget as the pool's own configuration, so the class's
// frame size reaches sendq instead of stopping at this package.
//
// MaxFloor is raised to one largest frame because that is exactly what
// sendq.DefaultMaxFloor promises and cannot deliver for a class it was not
// sized against: the guaranteed working set is what a member may fill when
// `capacity - used` has gone to nothing, so a floor below one frame means an
// otherwise-idle member cannot place a single legitimate message once the pool
// is merely full. With the default 4 MiB floor that refused a 16 MiB
// /ws/userevents bootstrap outright.
//
// MinFloor is deliberately NOT raised to the class's frame. Under crowding the
// floor is what every member is guaranteed simultaneously, so pinning it at one
// largest frame would multiply the class's biggest message by an unbounded
// connection count -- the per-connection-times-connections shape this whole
// mechanism exists to remove. A crowded pool refusing a large frame is correct
// load shedding; an idle one refusing it is not.
//
// It IS stated rather than left to sendq's zero-value default, because
// queueClass.minimum sizes every budget to MinQueueMembersAtFloor of this same
// number. Those two are the reason NewPool's MinFloor > Capacity panic cannot be
// reached from a config file, and a floor that is named in one of them and
// implied in the other is a coupling nothing would catch on the way out.
func (b QueueMemoryBudget) PoolConfig() sendq.PoolConfig {
	return sendq.PoolConfig{
		Capacity: b.Capacity,
		MinFloor: sendq.DefaultMinFloor,
		MaxFloor: max(sendq.DefaultMaxFloor, b.MaxFrame),
		Reclaim:  b.Reclaim,
	}
}

// queueClass is one pool's identity: the key an operator sets, the share it
// auto-sizes to, and the largest frame it must be able to carry.
//
// One value per class rather than three parallel lists (shares, frame bounds,
// validation table). The metric label, the config key, the share and the frame
// bound cannot disagree when they are fields of the same struct, and adding a
// fourth pool is one constructor instead of edits in four places that a reader
// has to diff against each other.
type queueClass struct {
	// name is the pool's public identity: the value of the `pool` label on
	// every leapmux_sendq_pool_* series and on leapmux_sendq_giveups_total.
	// A field rather than a literal at the composition root, so the sentence
	// above is true -- a renamed class cannot rename the config key and leave
	// the metric label behind. metrics owns the vocabulary; this carries it.
	name       string
	key        string
	share      int
	configured int64
	// reclaim is what this class's pool does under pressure. A field on the
	// class for the same reason name and share are: the answer belongs to the
	// class of connection, not to whichever member happens to ask.
	reclaim sendq.ReclaimPolicy
	// maxFrame is CHARGED bytes, not payload bytes: what the pool tests is
	// Config.Size plus Config.FrameOverhead, so a bound that covered only the
	// payload would still leave the frame unadmittable at any occupancy.
	maxFrame int64
}

// queueFrameEnvelopeHeadroom covers what a payload gains on its way to being a
// charged frame: the protobuf field tags and length varints of the envelopes it
// travels in. Generous on purpose -- it is compared against budgets measured in
// tens of megabytes, and under-stating it reinstates the very off-by-overhead
// this constant exists to close.
const queueFrameEnvelopeHeadroom = 4 << 10 // 4 KiB

func (c *Config) relayQueueClass() queueClass {
	return queueClass{
		name:       metrics.PoolRelay,
		key:        "relay_queue_memory_budget",
		share:      RelayQueueMemoryShare,
		configured: c.RelayQueueMemoryBudget,
		// A channel relay frame is the ciphertext the browser sent, bounded by
		// the socket's own read limit.
		maxFrame: channelwire.WSReadLimit + queueFrameEnvelopeHeadroom + sendq.DefaultFrameOverhead,
	}
}

func (c *Config) workerQueueClass() queueClass {
	return queueClass{
		name:       metrics.PoolWorker,
		key:        "worker_queue_memory_budget",
		share:      WorkerQueueMemoryShare,
		configured: c.WorkerQueueMemoryBudget,
		// NOT max_message_size. The Hub never holds a whole application payload
		// for a worker: it is a relay, and the only large ConnectResponse it
		// sends is a ChannelMessage carrying ciphertext read off /ws/channel,
		// whose socket is capped at WSReadLimit (ws_channel_relay.go). The
		// operator's max_message_size bounds the REASSEMBLED message that the
		// two endpoints rebuild from those chunks, which no Hub queue ever
		// materializes -- every other variant (WorkerIdentity, ChannelOpen,
		// Heartbeat, ReconcileNudge, and an empty WorkerTabInventoryResponse)
		// is a small control frame.
		//
		// Deriving this from max_message_size instead made a 64 MiB setting
		// demand a 64.25 MiB worker budget and refuse a perfectly workable
		// smaller one at startup, for a frame that cannot exist.
		//
		// Plus the private control allowance: charge tests `size > capacity -
		// reserve`, so the reserve is headroom the data path never sees.
		maxFrame: channelwire.WSReadLimit + queueFrameEnvelopeHeadroom +
			sendq.DefaultFrameOverhead + sendq.DefaultControlReserve,
	}
}

func (c *Config) userEventsQueueClass() queueClass {
	return queueClass{
		name: metrics.PoolUserEvents,
		// This class refuses the asker rather than nominating a peer. A
		// subscriber's own shed is the cheapest outcome in the Hub -- it
		// reconnects and delta-resumes, and before its opening frame is on the
		// wire it is not even a disconnect -- where evicting a peer costs a
		// connection that was keeping up. Stated here so it holds for any member
		// type this pool ever gains, not just for the one that remembers.
		reclaim:    sendq.ReclaimRefuseAsker,
		key:        "userevents_queue_memory_budget",
		share:      UserEventsQueueMemoryShare,
		configured: c.UserEventsQueueMemoryBudget,
		// A bootstrap frame -- a whole filtered account snapshot, or a resume
		// delta -- is bounded only by what the socket will carry, and a
		// user-event frame costs crdt.ResidentFactor times its wire size because
		// the queue holds the proto tree alongside the marshaled buffer. Derived
		// from crdt's own constant rather than restated, so the budget cannot
		// drift from what the queue actually charges.
		maxFrame: (channelwire.UserEventsReadLimit + queueFrameEnvelopeHeadroom) * crdt.ResidentFactor,
	}
}

func (c *Config) queueClasses() []queueClass {
	return []queueClass{c.relayQueueClass(), c.workerQueueClass(), c.userEventsQueueClass()}
}

// minimum is the smallest budget this class can work at, and it is the SAME
// number whether the budget was auto-sized or set by hand.
//
// One expression, deliberately. Auto-sizing and validation used to clamp to
// different numbers -- a flat 64 MiB against one guaranteed floor -- while a
// comment claimed they agreed, so a budget auto-sizing would never produce was
// nonetheless accepted from the config file.
//
// Two things it must clear, and the larger wins:
//
//   - One largest frame of its class. Under that, the failure is not
//     backpressure that clears when load drops; it is that class of connection
//     never working on this deployment, because a frame bigger than the whole
//     budget is unfittable at ANY occupancy.
//   - MinQueueMembersAtFloor guaranteed working sets, so the dynamic branch of
//     the admission rule actually engages. See MinQueueMembersAtFloor.
//
// Note what is NOT here: a flat byte floor. A fixed constant is the thing this
// whole mechanism replaced, and as an auto-size minimum it undid the shares on
// exactly the deployment memlimit exists to protect -- on a 512 MiB container
// the old 64 MiB floor raised two of the three pools and took 208 MiB, 40% of
// the limit, where the shares ask for 128 MiB. The shares are the design; the
// minimum's job is only to keep a pool functional, not to second-guess them.
//
// The floor term reads sendq.DefaultMinFloor, and QueueMemoryBudget.PoolConfig
// hands NewPool that same constant. Keeping the two equal is what makes
// NewPool's MinFloor > Capacity panic unreachable from a config file: this is
// the check that refuses a budget too small to honour the floor before a pool is
// ever built with one.
func (q queueClass) minimum() int64 {
	return max(q.maxFrame, MinQueueMembersAtFloor*sendq.DefaultMinFloor)
}

// resolve turns the class into the budget the Hub runs with: the configured
// value when there is one, otherwise this class's share of the detected basis,
// clamped into range.
//
// The two arms decide only capacity and how it is described. The struct is
// built ONCE, after them, because name/reclaim/maxFrame are carried over
// identically either way -- and listing carried-over fields by hand per arm is
// exactly how int64Default came to be silently dropped in prefixFlags. A sixth
// field cannot now be added to one arm only.
//
// Both arms open with `source=`, so "why is this budget this number?" is
// answered by one token whichever way it was set. The auto arm says which
// share of the basis it took and refers to the basis by name -- see
// QueueMemoryBudget.Source for why it does not render it.
func (q queueClass) resolve(basis memlimit.Basis) QueueMemoryBudget {
	var (
		capacity int64
		source   string
	)
	if q.configured > 0 {
		capacity = q.configured
		source = fmt.Sprintf("%s (source=config)", memlimit.HumanBytes(capacity))
	} else {
		capacity = basis.Bytes / QueueMemoryShareDenominator * int64(q.share)
		clamped := ""
		if minimum := q.minimum(); capacity < minimum {
			capacity, clamped = minimum, ", clamped up to the minimum"
		} else if capacity > MaxQueueMemoryBudget {
			capacity, clamped = MaxQueueMemoryBudget, ", clamped down to the maximum"
		}
		source = fmt.Sprintf("%s (source=auto, %d/%d of basis%s)",
			memlimit.HumanBytes(capacity), q.share, QueueMemoryShareDenominator, clamped)
	}
	return QueueMemoryBudget{
		Name:     q.name,
		Reclaim:  q.reclaim,
		Capacity: capacity,
		MaxFrame: q.maxFrame,
		Source:   source,
	}
}

// flagDef is one CLI flag's definition: how it is spelled, where it lands in
// the config tree, and what it defaults to.
//
// The default is ONE field rather than a pointer per type. It used to be a union
// of four -- `strDefault, intDefault, int64Default, boolDefault` -- whose "exactly
// one of these is set" rule lived only in a comment, and every row had to spell
// the three it did not use as positional `nil`s. Adding the fourth arm for the
// int64 budgets rewrote 34 untouched rows to widen that padding, which is the
// cost this shape removes: a fifth kind is now one constructor, not an edit to
// every existing flag.
//
// At package scope because the dispatch below is a method, and Go has no methods
// on a function-local type.
type flagDef struct {
	name     string
	koanfKey string
	category string
	usage    string
	// value is the flag's default, and its type is the flag's type. Set through
	// one of the constructors below so it can only ever be a kind register
	// knows.
	value any
}

// The flag constructors. Typed rather than a bare `any` field so the default's
// type is checked at the call site: `intFlag(..., "8")` does not compile, where
// a struct literal taking any would have registered a string flag under an int
// key and only failed at unmarshal.
//
// int64 is its own kind rather than reusing int because the queue-memory budgets
// reach 8 GiB, which an int cannot hold on a 32-bit build -- registering those as
// flag.Int would put the wrap back at the CLI layer that Config's int64 fields
// just removed.
func strFlag(name, koanfKey, category, usage string, def string) flagDef {
	return flagDef{name: name, koanfKey: koanfKey, category: category, usage: usage, value: def}
}

func intFlag(name, koanfKey, category, usage string, def int) flagDef {
	return flagDef{name: name, koanfKey: koanfKey, category: category, usage: usage, value: def}
}

func int64Flag(name, koanfKey, category, usage string, def int64) flagDef {
	return flagDef{name: name, koanfKey: koanfKey, category: category, usage: usage, value: def}
}

func boolFlag(name, koanfKey, category, usage string, def bool) flagDef {
	return flagDef{name: name, koanfKey: koanfKey, category: category, usage: usage, value: def}
}

// durationFlag registers a duration, not an int of seconds. The flag then takes
// the same spellings that the config file and the environment variable take,
// and the seconds count that the key carried before keeps working as the
// bare-number form. usage gains the syntax, so -help states it at each flag.
func durationFlag(name, koanfKey, category, usage string, def time.Duration) flagDef {
	return flagDef{
		name:     name,
		koanfKey: koanfKey,
		category: category,
		usage:    usage + ". " + internalconfig.UnitSyntax,
		value:    def,
	}
}

// register defines this flag on fs. The default arm panics rather than skipping:
// a kind nothing handles used to mean the flag silently vanished from -help and
// from the defaults map, with no compile or test signal, which is exactly how
// int64Default came to be dropped in prefixFlags.
func (fd flagDef) register(fs *flag.FlagSet) {
	switch v := fd.value.(type) {
	case string:
		fs.String(fd.name, v, fd.usage)
	case int:
		fs.Int(fd.name, v, fd.usage)
	case int64:
		fs.Int64(fd.name, v, fd.usage)
	case bool:
		fs.Bool(fd.name, v, fd.usage)
	case time.Duration:
		// The FlagProvider reads a set flag back through Value.String(), so the
		// destination only has to outlive this call, not be reachable from here.
		fs.Var(internalconfig.NewDurationFlag(new(time.Duration), v), fd.name, fd.usage)
	default:
		panic(fmt.Sprintf("config: flag %q has an unsupported default type %T", fd.name, fd.value))
	}
}

// ExtraFlagDef defines a string CLI flag that is not part of the hub's own
// config but should be parsed alongside it (e.g. worker-specific flags in
// solo mode).
//
// String-only by construction: extras land in Config.Extras, a
// map[string]string read back through k.String, so there is no other kind for
// StrDefault to be a default FOR.
type ExtraFlagDef struct {
	Name       string
	KoanfKey   string
	Usage      string
	StrDefault string
	// Category groups the flag in the help output; it must be one of
	// hubFlagCategoryOrder. Empty defaults to "Server options", which is where
	// the solo/dev launcher's own extras belong.
	Category string
}

// LoadOptions parameterizes differences between hub and solo/dev mode config loading.
type LoadOptions struct {
	DefaultListen     string         // default TCP listen address (hub: ":4327", solo: "127.0.0.1:4327")
	DefaultConfigDir  string         // for data_dir resolution (e.g. "~/.config/leapmux/solo")
	DefaultConfigFile string         // default config file path
	FlagSetName       string         // flag.NewFlagSet name ("hub" vs "leapmux")
	Description       string         // short command description for help output
	CLIFlags          []string       // if non-nil, only register these flags (solo exposes a subset)
	ExtraFlags        []ExtraFlagDef // additional flags not in the hub's allFlags list
	SoloMode          bool           // set on resulting Config
}

// Load parses hub configuration from defaults, config file, env vars, and CLI flags.
// Returns the config, whether -version was requested, and any error.
func Load(args []string) (*Config, bool, error) {
	return LoadWithOptions(args, LoadOptions{})
}

// LoadWithOptions parses hub configuration with customizable options.
// Zero-value LoadOptions fields fall back to hub defaults.
func LoadWithOptions(args []string, opts LoadOptions) (*Config, bool, error) {
	listen := opts.DefaultListen
	if listen == "" {
		listen = defaultListen
	}
	configDir := opts.DefaultConfigDir
	if configDir == "" {
		configDir = defaultConfigDir
	}
	configFile := opts.DefaultConfigFile
	if configFile == "" {
		configFile = defaultConfigFile
	}
	fsName := opts.FlagSetName
	if fsName == "" {
		fsName = "leapmux hub"
	}
	description := opts.Description
	if description == "" && fsName == "leapmux hub" {
		description = "Run the Hub service."
	}

	// Pre-scan for -config flag.
	configPath := internalconfig.ExtractConfigFlag(args, configFile)

	// prefixFlags creates flagDefs by prepending a CLI and koanf prefix to
	// a set of base definitions, replacing "{name}" in usage strings.
	prefixFlags := func(cliPrefix, koanfPrefix, displayName, category string, base []flagDef) []flagDef {
		out := make([]flagDef, len(base))
		for i, f := range base {
			// Copy the whole struct and override only what a prefix changes.
			// Listing the carried-over fields by hand is how int64Default came
			// to be silently dropped the moment flagDef grew it: the zero value
			// is a valid nil, so the flag simply vanished from -help with no
			// compile or test signal. Whole-struct assignment makes the next
			// field impossible to forget.
			out[i] = f
			out[i].name = cliPrefix + "-" + f.name
			out[i].koanfKey = koanfPrefix + "." + f.koanfKey
			out[i].category = category
			out[i].usage = strings.Replace(f.usage, "{name}", displayName, 1)
		}
		return out
	}

	// Base flag templates for PostgreSQL-compatible and MySQL-compatible backends.
	postgresBaseFlags := []flagDef{
		strFlag("dsn", "dsn", "", "{name} connection string", ""),
		intFlag("max-conns", "max_conns", "", "{name} maximum open connections", 25),
		intFlag("min-conns", "min_conns", "", "{name} minimum pool connections kept alive", 5),
		durationFlag("conn-max-lifetime", "conn_max_lifetime", "", "{name} connection max lifetime", time.Hour),
		durationFlag("max-conn-idle-time", "max_conn_idle_time", "", "{name} max idle time per connection", 5*time.Minute),
		durationFlag("health-check-period", "health_check_period", "", "{name} pool health check period", 30*time.Second),
	}
	mysqlBaseFlags := []flagDef{
		strFlag("dsn", "dsn", "", "{name} connection string", ""),
		intFlag("max-conns", "max_conns", "", "{name} maximum open connections", 25),
		intFlag("max-idle-conns", "max_idle_conns", "", "{name} maximum idle connections", 5),
		durationFlag("conn-max-lifetime", "conn_max_lifetime", "", "{name} connection max lifetime", time.Hour),
		durationFlag("conn-max-idle-time", "conn_max_idle_time", "", "{name} max idle time per connection", 5*time.Minute),
	}

	allFlags := []flagDef{
		strFlag("listen", "listen", "Server options", "TCP listen address (e.g. ':4327' or '127.0.0.1:4327')", listen),
		strFlag("local-listen", "local_listen", "Server options", "local IPC listen URL (unix:<path> or npipe:<name>); platform default used if empty", ""),
		strFlag("public-url", "public_url", "Server options", "public base URL when running behind a reverse proxy (e.g. 'https://hub.example.com')", ""),
		strFlag("data-dir", "data_dir", "Server options", "data directory", "."),
		strFlag("dev-frontend", "dev_frontend", "Server options", "frontend dev server URL for local development reverse proxy", ""),
		strFlag("log-level", "log_level", "Server options", "log level (debug, info, warn, error)", defaultLogLevel),
		boolFlag("signup-enabled", "signup_enabled", "Auth options", "enable user sign-up", false),
		boolFlag("email-verification-required", "email_verification_required", "Auth options", "require email verification on sign-up", false),
		durationFlag("session-duration", "session_duration", "Auth options", "how long a session stays valid after the user's last request; each request slides the expiry forward", auth.DefaultSessionDuration),
		strFlag("smtp-host", "smtp_host", "SMTP options", "SMTP server host", ""),
		intFlag("smtp-port", "smtp_port", "SMTP options", "SMTP server port", 587),
		strFlag("smtp-username", "smtp_username", "SMTP options", "SMTP username", ""),
		strFlag("smtp-password", "smtp_password", "SMTP options", "SMTP password", ""),
		strFlag("smtp-from-address", "smtp_from_address", "SMTP options", "SMTP from address", ""),
		strFlag("smtp-tls-mode", "smtp_tls_mode", "SMTP options", "SMTP TLS mode ("+validSmtpTLSModes+")", SmtpTLSModeSTARTTLS),
		durationFlag("api-timeout", "api_timeout", "Timeout and limit options", "general API timeout", DefaultAPITimeout),
		durationFlag("agent-startup-timeout", "agent_startup_timeout", "Timeout and limit options", "agent startup timeout", DefaultAgentStartupTimeout),
		durationFlag("worktree-create-timeout", "worktree_create_timeout", "Timeout and limit options", "worktree creation timeout", DefaultWorktreeCreateTimeout),
		intFlag("max-message-size", "max_message_size", "Timeout and limit options", "maximum application payload size in bytes (0 = 16 MiB default); reassembled ceiling is this plus 64 KiB headroom", 0),
		int64Flag("relay-queue-memory-budget", "relay_queue_memory_budget", "Timeout and limit options", "bytes the Hub's browser channel relays may queue between them (0 = auto-size from the process memory limit)", int64(0)),
		int64Flag("worker-queue-memory-budget", "worker_queue_memory_budget", "Timeout and limit options", "bytes the Hub's Worker connections may queue between them (0 = auto-size from the process memory limit)", int64(0)),
		int64Flag("userevents-queue-memory-budget", "userevents_queue_memory_budget", "Timeout and limit options", "bytes the Hub's user-event subscribers may hold between them (0 = auto-size from the process memory limit)", int64(0)),
		int64Flag("max-connections-per-user", "max_connections_per_user", "Timeout and limit options", "concurrent long-lived connections one user may hold (0 = unlimited); an active browser tab holds two", int64(DefaultMaxConnectionsPerUser)),
		int64Flag("max-workers-per-user", "max_workers_per_user", "Timeout and limit options", "Workers one user may have registered at once (0 = unlimited)", int64(DefaultMaxWorkersPerUser)),
		// Storage configuration
		strFlag("storage-type", "storage.type", "Storage common options", "storage backend type ("+validStorageTypes+")", ""),
		// SQLite (default)
		strFlag("storage-sqlite-path", "storage.sqlite.path", "SQLite storage options", "SQLite database file path (default: {data_dir}/hub.db)", ""),
		intFlag("storage-sqlite-max-conns", "storage.sqlite.max_conns", "SQLite storage options", "SQLite maximum open connections", sqlitedb.DefaultMaxConns),
		intFlag("storage-sqlite-cache-size", "storage.sqlite.cache_size", "SQLite storage options", "SQLite page cache size (positive = pages, negative = KiB, e.g. -65536 = 64 MiB)", 0),
		intFlag("storage-sqlite-mmap-size", "storage.sqlite.mmap_size", "SQLite storage options", "SQLite memory-mapped I/O size in bytes (0 = disabled)", 0),
	}
	// PostgreSQL and PostgreSQL-compatible backends.
	allFlags = append(allFlags, prefixFlags("storage-postgres", "storage.postgres", "PostgreSQL", "PostgreSQL storage options", postgresBaseFlags)...)
	allFlags = append(allFlags, prefixFlags("storage-cockroachdb", "storage.cockroachdb", "CockroachDB", "CockroachDB storage options", postgresBaseFlags)...)
	allFlags = append(allFlags, prefixFlags("storage-yugabytedb", "storage.yugabytedb", "YugabyteDB", "YugabyteDB storage options", postgresBaseFlags)...)
	// MySQL and MySQL-compatible backends.
	allFlags = append(allFlags, prefixFlags("storage-mysql", "storage.mysql", "MySQL", "MySQL storage options", mysqlBaseFlags)...)
	allFlags = append(allFlags, prefixFlags("storage-tidb", "storage.tidb", "TiDB", "TiDB storage options", mysqlBaseFlags)...)

	// Build the set of allowed CLI flags.
	var allowedFlags map[string]bool
	if opts.CLIFlags != nil {
		allowedFlags = make(map[string]bool, len(opts.CLIFlags))
		for _, f := range opts.CLIFlags {
			allowedFlags[f] = true
		}
	}

	// Define CLI flags and build fieldMap/defaults from the canonical list.
	fs := flag.NewFlagSet(fsName, flag.ContinueOnError)
	fs.String("config", configFile, "path to config file")
	usageCategories := map[string]string{"config": "Common options"}
	fieldMap := make(map[string]string, len(allFlags))
	defaults := make(map[string]interface{}, len(allFlags))

	for _, fd := range allFlags {
		// Always add to defaults. One assignment, not a four-arm switch: the
		// value already carries its own type.
		defaults[fd.koanfKey] = fd.value

		// Register CLI flag only if allowed.
		if allowedFlags != nil && !allowedFlags[fd.name] {
			continue
		}
		fieldMap[fd.name] = fd.koanfKey
		usageCategories[fd.name] = fd.category
		fd.register(fs)
	}
	// Register extra flags (e.g. worker-specific flags in solo mode). Same four
	// statements as the loop above, through the same flagDef, so a change to how
	// a flag is registered reaches extras too -- hand-rolling the fs.String here
	// is what let the two drift.
	//
	// One deliberate difference: no allowedFlags check. CLIFlags names the subset
	// of the HUB's own flags a launcher exposes, and an extra is by definition not
	// in that list, so honouring it here would drop every extra the caller asked
	// for.
	for _, ef := range opts.ExtraFlags {
		category := ef.Category
		if category == "" {
			category = "Server options"
		}
		fd := strFlag(ef.Name, ef.KoanfKey, category, ef.Usage, ef.StrDefault)
		defaults[fd.koanfKey] = fd.value
		fieldMap[fd.name] = fd.koanfKey
		usageCategories[fd.name] = fd.category
		fd.register(fs)
	}

	showVersion := fs.Bool("version", false, "print version and exit")
	usageCategories["version"] = "Common options"

	if err := internalconfig.ConfigureAndParse(fs, args, description, usageCategories, hubFlagCategoryOrder); err != nil {
		return nil, false, err
	}

	if *showVersion {
		return nil, true, nil
	}

	k := koanf.New(internalconfig.Delim)
	fp := internalconfig.NewFlagProvider(fs, fieldMap)

	if err := internalconfig.Load(k, defaults, configPath, "LEAPMUX_HUB_", fp); err != nil {
		return nil, false, fmt.Errorf("load config: %w", err)
	}

	var cfg Config
	if err := internalconfig.Unmarshal(k, &cfg); err != nil {
		return nil, false, fmt.Errorf("unmarshal config: %w", err)
	}

	// Resolve the timeouts before anything reads one. A negative survives this
	// step untouched, so Validate still sees what the operator wrote.
	cfg.applyDurationDefaults()

	// Validate --local-listen early: malformed values should surface at
	// startup with a clear error rather than failing later inside Serve.
	if cfg.LocalListen != "" {
		if _, _, err := locallisten.Parse(cfg.LocalListen); err != nil {
			return nil, false, fmt.Errorf("invalid local_listen: %w", err)
		}
	}

	// Canonicalize and validate --public-url. Sub-path proxying isn't supported
	// elsewhere in the codebase yet, so reject anything beyond scheme + host.
	if cfg.PublicURL != "" {
		canon, err := normalizePublicURL(cfg.PublicURL)
		if err != nil {
			return nil, false, err
		}
		cfg.PublicURL = canon
	}

	// Resolve relative data_dir against config file directory.
	cfg.DataDir = internalconfig.ResolveDataDir(cfg.DataDir, configPath, configDir)
	cfg.SoloMode = opts.SoloMode

	// Solo mode does not support a public URL: there's no proxy in front of it,
	// and exposing the option would just create surprising failure modes.
	if cfg.SoloMode && cfg.PublicURL != "" {
		return nil, false, fmt.Errorf("public_url is not supported in solo mode")
	}

	// Populate extra flag values.
	if len(opts.ExtraFlags) > 0 {
		cfg.Extras = make(map[string]string, len(opts.ExtraFlags))
		for _, ef := range opts.ExtraFlags {
			cfg.Extras[ef.KoanfKey] = k.String(ef.KoanfKey)
		}
	}

	return &cfg, false, nil
}

var hubFlagCategoryOrder = []string{
	"Common options",
	"Server options",
	"Auth options",
	"SMTP options",
	"Timeout and limit options",
	"Storage common options",
	"SQLite storage options",
	"PostgreSQL storage options",
	"CockroachDB storage options",
	"YugabyteDB storage options",
	"MySQL storage options",
	"TiDB storage options",
}

// Validate checks the configuration values and ensures required directories exist.
func (c *Config) Validate() error {
	// Re-canonicalize PublicURL so programmatic config construction (e.g. tests
	// instantiating &Config{PublicURL: ...} directly) hits the same gate as
	// LoadWithOptions. No-op when Load already canonicalized it.
	if c.PublicURL != "" {
		canon, err := normalizePublicURL(c.PublicURL)
		if err != nil {
			return err
		}
		c.PublicURL = canon
	}
	if c.SoloMode && c.PublicURL != "" {
		return fmt.Errorf("public_url is not supported in solo mode")
	}

	// Ensure data dir exists.
	if err := os.MkdirAll(c.DataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	// Validate storage configuration.
	requireField := func(value, field string) error {
		if value == "" {
			return fmt.Errorf("%s is required when storage.type is %s", field, c.Storage.Type)
		}
		return nil
	}
	switch c.Storage.Type {
	case "", StorageTypeSQLite:
		// No additional validation needed.
	case StorageTypePostgres:
		if err := requireField(c.Storage.Postgres.DSN, "storage.postgres.dsn"); err != nil {
			return err
		}
	case StorageTypeMySQL:
		if err := requireField(c.Storage.MySQL.DSN, "storage.mysql.dsn"); err != nil {
			return err
		}
	case StorageTypeCockroachDB:
		if err := requireField(c.Storage.CockroachDB.DSN, "storage.cockroachdb.dsn"); err != nil {
			return err
		}
	case StorageTypeYugabyteDB:
		if err := requireField(c.Storage.YugabyteDB.DSN, "storage.yugabytedb.dsn"); err != nil {
			return err
		}
	case StorageTypeTiDB:
		if err := requireField(c.Storage.TiDB.DSN, "storage.tidb.dsn"); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported storage.type: %q (valid: %s)", c.Storage.Type, validStorageTypes)
	}

	// SMTP / email configuration. Validation is layered:
	//   1. Normalize: empty SmtpTLSMode → starttls (handles programmatically
	//      built configs that bypass flag-parsing defaults).
	//   2. Always: SmtpTLSMode must be a valid constant — even when SMTP is
	//      disabled, so a typo in smtp_tls_mode never silently sticks.
	//   3. SmtpHost set: requires from-address and a valid port. There is no
	//      "half-configured SMTP" state.
	//   4. Email verification required: requires SmtpHost (chains through #3).
	//   5. tls_mode=none + auth + non-localhost is rejected — Go's
	//      smtp.PlainAuth refuses to send credentials over an unencrypted
	//      non-localhost connection, so this combo never works in practice.
	if c.SmtpTLSMode == "" {
		c.SmtpTLSMode = SmtpTLSModeSTARTTLS
	}
	switch c.SmtpTLSMode {
	case SmtpTLSModeSTARTTLS, SmtpTLSModeImplicit, SmtpTLSModeNone:
	default:
		return fmt.Errorf("unsupported smtp_tls_mode: %q (valid: %s)", c.SmtpTLSMode, validSmtpTLSModes)
	}
	if c.SmtpHost != "" {
		if c.SmtpFromAddress == "" {
			return fmt.Errorf("smtp_from_address is required when smtp_host is set")
		}
		if err := validate.ValidateEmail(c.SmtpFromAddress); err != nil {
			return fmt.Errorf("invalid smtp_from_address: %w", err)
		}
		if c.SmtpPort < 1 || c.SmtpPort > 65535 {
			return fmt.Errorf("smtp_port must be in [1, 65535], got %d", c.SmtpPort)
		}
		if c.SmtpTLSMode == SmtpTLSModeNone && c.SmtpUsername != "" && !isLocalhost(c.SmtpHost) {
			return fmt.Errorf("smtp_tls_mode=none with smtp_username set is rejected for non-localhost smtp_host=%q: "+
				"Go's smtp.PlainAuth refuses to send credentials over an unencrypted non-localhost connection", c.SmtpHost)
		}
	}
	if c.EmailVerificationRequired && c.SmtpHost == "" {
		return fmt.Errorf("smtp_host is required when email_verification_required is true")
	}
	if err := channelwire.ValidateConfiguredMaxMessageSize(c.MaxMessageSize); err != nil {
		return err
	}
	if err := c.validateQueueMemoryBudgets(); err != nil {
		return err
	}
	if err := c.validateMaxWorkersPerUser(); err != nil {
		return err
	}
	if err := c.validateMaxConnectionsPerUser(); err != nil {
		return err
	}
	if err := c.validateSessionDuration(); err != nil {
		return err
	}

	return nil
}

// validateSessionDuration rejects a session lifetime nobody meant.
//
// The rule itself lives in auth.ValidateSessionDuration, next to the two
// constants that set the floor. This adds only the key name, which auth cannot
// know.
//
// Zero reaches this only from a Config built in code, because
// applyDurationDefaults already replaced a loaded zero with the default.
func (c *Config) validateSessionDuration() error {
	if c.SessionDuration == 0 {
		return nil
	}
	if err := auth.ValidateSessionDuration(c.SessionDuration); err != nil {
		return fmt.Errorf("session_duration: %w", err)
	}
	return nil
}

// validateMaxConnectionsPerUser rejects a cap that would refuse connections a
// working client has to make.
//
// Zero is unlimited and always allowed. A negative is not a smaller cap, it is a
// value nobody meant, so it fails loudly rather than being silently read as
// unlimited. Between zero and MinConnectionsPerUser the setting stops bounding
// abuse and starts breaking the first tab -- see MinConnectionsPerUser for the
// arithmetic.
func (c *Config) validateMaxConnectionsPerUser() error {
	switch {
	case c.MaxConnectionsPerUser < 0:
		return fmt.Errorf("max_connections_per_user must not be negative, got %d (0 = unlimited)",
			c.MaxConnectionsPerUser)
	case c.MaxConnectionsPerUser == 0:
		return nil
	case c.MaxConnectionsPerUser < MinConnectionsPerUser:
		return fmt.Errorf(
			"max_connections_per_user must be 0 (unlimited) or at least %d, got %d: "+
				"an active browser tab holds two connections, so a smaller cap leaves "+
				"no room for a second tab or for the desktop and CLI clients",
			MinConnectionsPerUser, c.MaxConnectionsPerUser)
	}
	return nil
}

// validateMaxWorkersPerUser rejects a cap nobody meant. Zero is unlimited; a
// negative is not a smaller cap, so it fails loudly rather than being read as
// unlimited. There is no lower bound above one: unlike a browser tab, which
// needs two connections before it works at all, one Worker is a working
// deployment.
func (c *Config) validateMaxWorkersPerUser() error {
	if c.MaxWorkersPerUser < 0 {
		return fmt.Errorf("max_workers_per_user must not be negative, got %d (0 = unlimited)",
			c.MaxWorkersPerUser)
	}
	return nil
}

// validateQueueMemoryBudgets rejects an explicitly configured pool that is too
// small to do its job.
//
// No class derives its frame bound from an operator knob today -- all three
// size themselves from fixed wire limits, which is the point workerQueueClass
// argues at length -- so this runs after the other validators for no reason
// stronger than reading order. A class that ever does read one would need that
// knob checked first.
//
// Catching it here turns "every Worker fences" or "every tab's connect is
// refused" into a startup error naming the key. Only an explicitly configured
// value is checked: an auto-sized one clears the bar by construction, because
// resolve clamps to the very same queueClass.minimum.
func (c *Config) validateQueueMemoryBudgets() error {
	for _, q := range c.queueClasses() {
		if q.configured == 0 {
			continue
		}
		minimum := q.minimum()
		if q.configured < minimum {
			return fmt.Errorf("%s must be 0 (auto) or at least %s, got %s", q.key,
				memlimit.HumanBytes(minimum), memlimit.HumanBytes(q.configured))
		}
	}
	return nil
}

// isLocalhost reports whether host (a hostname or numeric IP, no port) is a
// loopback address. Used to relax validation for SMTP relays running on the
// same machine, where unencrypted credentialed AUTH is acceptable.
//
// We accept the literal name "localhost" (a hostname, not an IP — Go's
// resolver and smtp.PlainAuth both treat it specially) plus anything that
// parses as a loopback IP, which covers 127.0.0.0/8 and ::1 — matching
// the criteria smtp.PlainAuth uses.
func isLocalhost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// DefaultHubDataDir returns the default hub data directory with ~ expanded.
func DefaultHubDataDir() string {
	return internalconfig.ExpandHome(defaultConfigDir)
}

// SQLiteDBPath returns the path to the SQLite database file.
// Uses Storage.SQLite.Path if set, otherwise defaults to {DataDir}/hub.db.
func (c *Config) SQLiteDBPath() string {
	if c.Storage.SQLite.Path != "" {
		return c.Storage.SQLite.Path
	}
	return filepath.Join(c.DataDir, "hub.db")
}

// SQLiteDBConfig returns the SQLite configuration for sqlitedb.Open.
func (c *Config) SQLiteDBConfig() sqlitedb.Config {
	return sqlitedb.Config{
		MaxConns:  c.Storage.SQLite.MaxConns,
		CacheSize: c.Storage.SQLite.CacheSize,
		MmapSize:  c.Storage.SQLite.MmapSize,
	}
}

// EncryptionKeyFilePath returns the path to the encryption key ring file.
func (c *Config) EncryptionKeyFilePath() string {
	if c.EncryptionKeyPath != "" {
		return c.EncryptionKeyPath
	}
	return filepath.Join(c.DataDir, "encryption.key")
}

// BaseURL returns the public base URL of the hub. When PublicURL is set
// (typically because the hub is fronted by a reverse proxy) it wins; otherwise
// the URL is derived from Listen + SecureCookies, with a bare ":port" address
// resolved to "localhost:port".
func (c *Config) BaseURL() string {
	if c.PublicURL != "" {
		return c.PublicURL
	}
	scheme := "http"
	if c.SecureCookies {
		scheme = "https"
	}
	host := c.Listen
	if strings.HasPrefix(host, ":") {
		host = "localhost" + host
	}
	return scheme + "://" + host
}

// normalizePublicURL trims one trailing slash from raw, then validates the
// result is an absolute http(s) URL with a non-empty host and no userinfo,
// path, query, or fragment. Returns the canonical (slash-trimmed) string.
//
// Sub-path deployments (e.g. "https://example.com/leapmux") are deliberately
// rejected — the rest of the codebase concatenates this URL with a leading
// slash and does not yet support a base path.
func normalizePublicURL(raw string) (string, error) {
	trimmed := strings.TrimSuffix(raw, "/")
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid public_url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid public_url: scheme must be http or https, got %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("invalid public_url: host is required")
	}
	if u.User != nil {
		return "", fmt.Errorf("invalid public_url: userinfo is not allowed")
	}
	if u.Path != "" {
		return "", fmt.Errorf("invalid public_url: path is not allowed (sub-path deployments are not supported)")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", fmt.Errorf("invalid public_url: query is not allowed")
	}
	if u.Fragment != "" {
		return "", fmt.Errorf("invalid public_url: fragment is not allowed")
	}
	return trimmed, nil
}

// LocalListenURL returns the local IPC listen URL the hub should bind.
// If the user set --local-listen explicitly, that value is returned verbatim.
// Otherwise a per-platform default is used: unix:<data-dir>/hub.sock on Unix,
// npipe:leapmux-hub-<SID> on Windows.
func (c *Config) LocalListenURL() (string, error) {
	if c.LocalListen != "" {
		return c.LocalListen, nil
	}
	return defaultLocalListen(c.DataDir)
}
