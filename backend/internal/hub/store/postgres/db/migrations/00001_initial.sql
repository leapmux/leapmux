-- +goose Up

-- Users
-- users.id carries CHECK (id <> '') because it is the parent key every
-- owner-keyed row hangs off. store.CreateUserParams.Validate refuses a blank id
-- at the Go API, but that closes only the store as a route to the shape; raw SQL
-- (an operator repair script, a restored file, a seed) could still land one, and
-- from there every REFERENCES users(id) below would point at it without
-- complaint. The CHECK is what makes the blank-owner family unrepresentable
-- rather than merely unreachable through one API.
--
-- NOTE: enforced on SQLite, PostgreSQL, CockroachDB and YugabyteDB. TiDB parses
-- and IGNORES CHECK constraints unless tidb_enable_check_constraint is ON --
-- see mysql.go, which sets it alongside tidb_enable_foreign_key.
CREATE TABLE users (
    id             TEXT COLLATE "C" PRIMARY KEY CHECK (id <> ''),
    username       TEXT NOT NULL,
    password_hash  TEXT NOT NULL,
    display_name   TEXT NOT NULL DEFAULT '',
    -- Unicode-casefolded (Go strings.ToLower) copy of display_name, maintained on
    -- every write, so admin SearchUsers matches non-ASCII names case-insensitively
    -- and identically across SQLite/Postgres/MySQL (SQLite folds only ASCII, so a
    -- plain LIKE on this pre-folded column keeps the three dialects in agreement).
    display_name_folded      TEXT NOT NULL DEFAULT '',
    email                    TEXT NOT NULL DEFAULT '',
    email_verified           BOOLEAN NOT NULL DEFAULT FALSE,
    pending_email            TEXT NOT NULL DEFAULT '',
    -- Stored verification code in raw 6-char form (no hyphen), drawn from
    -- verifycode.Charset. Empty when no verification is pending.
    pending_email_token      VARCHAR(16) NOT NULL DEFAULT '',
    pending_email_expires_at TIMESTAMPTZ,
    -- Counts attempts against the active pending_email_token. Reset to 0
    -- whenever a new token is issued; force-expires the token at >5.
    pending_email_attempts   INTEGER NOT NULL DEFAULT 0,
    -- When the current verification code was issued. ONLY SetPendingEmail
    -- writes it; the resend-cooldown gate compares this column directly
    -- rather than deriving the issue time from the expiry, because
    -- ConsumeVerificationAttempt force-expires a burned code by moving
    -- the expiry to now -- a derivation would then read a brand-new
    -- burned code as issued a full lifetime ago and re-mint inside the
    -- cooldown.
    pending_email_issued_at    TIMESTAMPTZ,
    -- Account-recovery break-glass: token stored hashed (SHA-256 hex).
    pending_recovery_token      VARCHAR(64) NOT NULL DEFAULT '',
    pending_recovery_expires_at TIMESTAMPTZ,
    pending_recovery_attempts   INTEGER NOT NULL DEFAULT 0,
    -- When the current recovery link was issued; the cooldown gate reads
    -- this, not the expiry, for the reason pending_email_issued_at states
    -- (ConsumeRecoveryAttemptByToken force-expires the same way).
    pending_recovery_issued_at TIMESTAMPTZ,
    password_set             BOOLEAN NOT NULL DEFAULT TRUE,
    is_admin                 BOOLEAN NOT NULL DEFAULT FALSE,
    prefs          TEXT NOT NULL DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- High-water mark bumped whenever this user's auth basis is
    -- bulk-revoked. Each bump also records a durable user-token
    -- revocation event so cookie channels and bearer caches die together
    -- with admin-CLI mutations that run in a separate process.
    tokens_revoked_at        TIMESTAMPTZ,
    -- Monotonic credential epoch. Sessions and bearer rows copy this
    -- value when issued; user-wide revocation increments it so stale
    -- credentials fail without depending on timestamp precision or
    -- cross-host clock agreement.
    auth_generation          BIGINT NOT NULL DEFAULT 0,
    deleted_at     TIMESTAMPTZ
);
CREATE UNIQUE INDEX idx_users_username ON users(username) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX idx_users_email ON users(email) WHERE email != '' AND deleted_at IS NULL;
CREATE INDEX idx_users_deleted_at ON users(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX idx_users_created_at ON users(created_at DESC, id DESC) WHERE deleted_at IS NULL;
-- Verification codes are looked up per-user (the session identifies who),
-- so no global token index is needed. Index expiry instead, for cleanup.
CREATE INDEX idx_users_pending_email_expires_at ON users(pending_email_expires_at) WHERE pending_email_expires_at IS NOT NULL;
-- Recovery tokens are looked up by hash on complete; index non-empty
-- values so Completes do not scan the users table.
CREATE INDEX IF NOT EXISTS idx_users_pending_recovery_token ON users(pending_recovery_token) WHERE pending_recovery_token <> '';
-- GetFirstAdmin scans for the earliest non-deleted admin (bootstrap path).
-- Partial on (is_admin, deleted_at) keeps the index tiny;
-- created_at lets the ORDER BY + LIMIT 1 hit the first leaf directly.
CREATE INDEX idx_users_is_admin ON users(created_at) WHERE is_admin AND deleted_at IS NULL;

-- Auth sessions
CREATE TABLE user_sessions (
    id              TEXT COLLATE "C" PRIMARY KEY,
    user_id         TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_active_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    auth_generation BIGINT NOT NULL DEFAULT 0,
    -- Sudo-mode elevation, written only by the elevate RPCs and the OAuth
    -- re-authentication leg. elevation_proven_at is the instant the step-up factor
    -- was proven and never moves; elevation_expires_at is the sliding deadline. A
    -- NULL in either column means the session was never elevated.
    --
    -- Two columns, not one. A single sliding column cannot carry an absolute
    -- cap, so a user who acts every two hours would hold the privilege for
    -- ever. elevation_proven_at anchors that cap, and the slide statement clamps
    -- elevation_expires_at to elevation_proven_at + the maximum total window.
    elevation_proven_at     TIMESTAMPTZ,
    elevation_expires_at  TIMESTAMPTZ,
    user_agent      TEXT NOT NULL DEFAULT '',
    ip_address      TEXT NOT NULL DEFAULT '',
    -- Both or neither, enforced HERE rather than re-checked at every read.
    -- Half a pair is not a state the rest of the hub should have to consider:
    -- a deadline with no anchor is one the slide statement cannot clamp,
    -- because it has nothing to measure the absolute cap from. Same shape as
    -- the (seq, published_at) pair in revocation_events below.
    CHECK ((elevation_proven_at IS NULL) = (elevation_expires_at IS NULL))
);
-- Serves the plain user_id lookups (prefix) AND the per-user keyset listing
-- ListUserSessionsByUserID (user_id =, ORDER BY last_active_at DESC, id DESC),
-- so that query both seeks and rides the index instead of sorting.
CREATE INDEX idx_user_sessions_user_last_active ON user_sessions(user_id, last_active_at DESC, id DESC);
CREATE INDEX idx_user_sessions_expires_at_last_active ON user_sessions(expires_at, last_active_at);
-- The active-session listing orders by (last_active_at DESC, id DESC) while
-- filtering expires_at with a range predicate. A range on the leading
-- expires_at column means the index above can never provide that order (the
-- engine would top-N sort every page), so the ORDER BY gets its own index and
-- the expiry check runs as a residual filter during the ordered scan.
CREATE INDEX idx_user_sessions_last_active ON user_sessions(last_active_at DESC, id DESC);

-- Registered workers
CREATE TABLE workers (
    id            TEXT COLLATE "C" PRIMARY KEY,
    auth_token    TEXT COLLATE "C" NOT NULL UNIQUE,
    registered_by TEXT COLLATE "C" NOT NULL REFERENCES users(id),
    status        INTEGER NOT NULL DEFAULT 1,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at  TIMESTAMPTZ,
    public_key    BYTEA NOT NULL DEFAULT '',
    mlkem_public_key  BYTEA NOT NULL DEFAULT '',
    slhdsa_public_key BYTEA NOT NULL DEFAULT '',
    -- True for rows created by Server.RegisterWorker, the in-process
    -- bypass the solo launcher uses to bring up the co-located local
    -- worker. The deregister handler refuses these so the user can't
    -- accidentally tear down the bundled desktop worker -- it would just
    -- re-register on next launch and the running process would noisily
    -- exit with "invalid auth token" in between.
    auto_registered BOOLEAN NOT NULL DEFAULT FALSE,
    deleted_at    TIMESTAMPTZ
);
-- Non-partial on purpose (matches MySQL): ListWorkersByUserID and
-- ListWorkersAdminByUserAndStatus filter on registered_by + status with NO
-- deleted_at predicate, so a WHERE deleted_at IS NULL partial index would be
-- ineligible and every page would fall back to a full table scan plus sort.
-- The leading registered_by column also serves HardDeleteUsersBefore's
-- NOT EXISTS (workers.registered_by = users.id) point probe over ALL rows
-- (including soft-deleted), so no separate registered_by-only index is needed.
CREATE INDEX idx_workers_registered_by_status_created ON workers(registered_by, status, created_at DESC, id DESC);
-- Admin status-only listing (ListWorkersAdminByStatus) cannot use the
-- (registered_by, status, created_at) index because registered_by is the
-- leading column. Non-partial on purpose: the query carries no deleted_at
-- filter (status=3 lists soft-deleted workers), so a WHERE deleted_at IS NULL
-- partial index would be ineligible and every page would fall back to a full
-- table scan plus sort.
CREATE INDEX idx_workers_status_created ON workers(status, created_at DESC, id DESC);
-- Admin per-user listing (ListWorkersAdminByUser): see the sqlite migration for
-- the full rationale. Partial on deleted_at IS NULL because the query filters
-- it; the (registered_by, status, created_at, id) composite above can't serve
-- this query's ORDER BY (status breaks the created_at order within a
-- registered_by prefix).
CREATE INDEX idx_workers_registered_by_created ON workers(registered_by, created_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_workers_deleted_at ON workers(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX idx_workers_created_at ON workers(created_at DESC, id DESC) WHERE deleted_at IS NULL;

-- Worker notifications (persistent queue for reliable delivery)
CREATE TABLE worker_notifications (
    id           TEXT COLLATE "C" PRIMARY KEY,
    worker_id    TEXT COLLATE "C" NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    type         INTEGER NOT NULL,
    payload      TEXT NOT NULL DEFAULT '{}',
    status       INTEGER NOT NULL DEFAULT 1,
    attempts     INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 5,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at TIMESTAMPTZ
);
CREATE INDEX idx_worker_notifications_worker_status ON worker_notifications(worker_id, status);

-- Active worker registration keys.
--
-- Created by an authenticated user via the frontend. The worker presents
-- the row's `id` as a bearer credential (Authorization: Bearer <key>) on
-- the WorkerConnectorService.Register RPC; the hub atomically consumes
-- the row and creates a workers row in one transaction.
--
-- The store implements soft-delete by setting expires_at to a past instant.
-- The cleanup loop hard-deletes rows whose expires_at is older than the
-- retention cutoff.
CREATE TABLE worker_registration_keys (
    id          TEXT COLLATE "C" PRIMARY KEY,
    created_by  TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_worker_registration_keys_expires_at ON worker_registration_keys(expires_at);
CREATE INDEX idx_worker_registration_keys_created_by ON worker_registration_keys(created_by);
CREATE INDEX idx_worker_registration_keys_created_at ON worker_registration_keys(created_at DESC, id DESC);


-- Sidebar sections (per-user grouping of sidebar panels)
CREATE TABLE workspace_sections (
    id           TEXT COLLATE "C" PRIMARY KEY,
    user_id      TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    -- COLLATE "C" on every position column: ORDER BY position must sort byte-wise to match the Go/TS lexorank comparator regardless of alphabet.
    position     TEXT COLLATE "C" NOT NULL,
    section_type INTEGER NOT NULL DEFAULT 1,
    sidebar      INTEGER NOT NULL DEFAULT 1,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_workspace_sections_user_id ON workspace_sections(user_id);
-- One default section of each type per user. section_type = 1 is
-- SECTION_TYPE_WORKSPACES_CUSTOM, which a user may hold any number of, so the
-- uniqueness applies to every OTHER type only.
--
-- Structural, not procedural: CreateUser writes the defaults in the same
-- transaction as the user row and nothing backfills them, so a second signup
-- path that forgot to seed -- or a read path that seeded during the read, which
-- is what this replaced -- used to produce a sidebar with two of every pane,
-- indistinguishable from one another.
CREATE UNIQUE INDEX idx_workspace_sections_user_default_type
    ON workspace_sections(user_id, section_type) WHERE section_type <> 1;

-- Workspaces (hub-owned registry) -- must come before workspace_section_items
CREATE TABLE workspaces (
    id            TEXT COLLATE "C" PRIMARY KEY,
    owner_user_id TEXT COLLATE "C" NOT NULL REFERENCES users(id),
    title         TEXT NOT NULL DEFAULT '',
    is_deleted    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at    TIMESTAMPTZ
);
CREATE INDEX idx_workspaces_owner_live ON workspaces(owner_user_id) WHERE is_deleted = FALSE;
CREATE INDEX idx_workspaces_owner_user_id ON workspaces(owner_user_id);
CREATE INDEX idx_workspaces_deleted_at ON workspaces(deleted_at) WHERE deleted_at IS NOT NULL;

-- Workspace-to-section assignments (per-user)
CREATE TABLE workspace_section_items (
    user_id      TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id TEXT COLLATE "C" NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    section_id   TEXT COLLATE "C" NOT NULL REFERENCES workspace_sections(id) ON DELETE CASCADE,
    position     TEXT COLLATE "C" NOT NULL,
    PRIMARY KEY (user_id, workspace_id)
);
CREATE INDEX idx_workspace_section_items_section ON workspace_section_items(section_id);

-- See sqlite migration for full rationale on the CRDT schema (op
-- journal, materialized state blob, derived tab views, dedup table,
-- and lifecycle outbox).
CREATE TABLE user_op_batches (
    user_id        TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    physical_ms   BIGINT NOT NULL,
    logical       BIGINT NOT NULL,
    last_logical  BIGINT NOT NULL,
    origin_client TEXT COLLATE "C" NOT NULL,
    principal_id  TEXT COLLATE "C" NOT NULL,
    batch_id      TEXT COLLATE "C" NOT NULL,
    body_hash     BYTEA NOT NULL,
    batch_payload BYTEA NOT NULL,
    transitions_payload BYTEA NOT NULL,
    op_count      INTEGER NOT NULL CHECK (op_count > 0),
    epoch         BIGINT NOT NULL,
    committed_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),  -- operator-facing audit stamp only; NOT a retention predicate (see idx_user_op_batches_physical_ms)
    PRIMARY KEY (user_id, physical_ms, logical, origin_client)
);
CREATE UNIQUE INDEX idx_user_op_batches_dedup ON user_op_batches(user_id, batch_id);
-- Backs DeleteUserOpBatchesBeforePhysical, the cross-user retention sweep.
-- The PK leads with user_id, which that query does not bind, so without this
-- the hourly sweep full-scans every user's rows -- including the final pass
-- that exists only to prove nothing is left to delete.
CREATE INDEX idx_user_op_batches_physical_ms ON user_op_batches(physical_ms);

CREATE TABLE user_state (
    user_id           TEXT COLLATE "C" PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    state_payload    BYTEA NOT NULL,
    -- state_payload.compaction_watermark.physical, projected out of the blob so
    -- SQL can filter on it. One-way derived, exactly like user_op_batches'
    -- physical_ms/logical over batch_payload: written from the same struct in
    -- the same statement and rebuildable from the payload alone.
    --
    -- It is the only safe upper limit on op-batch deletion. Bootstrap rebuilds
    -- a user as state_payload + every batch ABOVE this watermark, so a batch at
    -- or below it is absorbed and a batch above it is the sole surviving copy
    -- of those ops. The cross-user retention sweep joins on it for that reason.
    compaction_physical_ms BIGINT NOT NULL DEFAULT 0,
    current_epoch    BIGINT NOT NULL DEFAULT 1,
    epoch_started_at TIMESTAMPTZ NOT NULL,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE workspace_tab_owned (
    -- The users(id) FK mirrors the sibling CRDT tables (user_op_batches,
    -- user_state, user_recent_batch_ids, lifecycle_outbox): without it a
    -- blank-owner row is insertable but no delete path can bind it away.
    user_id      TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id TEXT COLLATE "C" NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    tab_type     INTEGER NOT NULL,
    tab_id       TEXT COLLATE "C" NOT NULL,
    worker_id    TEXT COLLATE "C" NOT NULL,
    tile_id      TEXT COLLATE "C" NOT NULL,
    position     TEXT COLLATE "C" NOT NULL,
    PRIMARY KEY (user_id, tab_id)
);
-- (user_id, worker_id), not worker_id alone: ListOwnedTabsByWorker binds both,
-- so a single-column index could only seek worker_id and then re-filter every
-- match by owner.
CREATE INDEX idx_workspace_tab_owned_worker    ON workspace_tab_owned(user_id, worker_id);
CREATE INDEX idx_workspace_tab_owned_workspace ON workspace_tab_owned(workspace_id);

CREATE TABLE workspace_tab_rendered (
    -- Same users(id) FK as workspace_tab_owned above.
    user_id      TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id TEXT COLLATE "C" NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    tab_type     INTEGER NOT NULL,
    tab_id       TEXT COLLATE "C" NOT NULL,
    worker_id    TEXT COLLATE "C" NOT NULL,
    tile_id      TEXT COLLATE "C" NOT NULL,
    position     TEXT COLLATE "C" NOT NULL,
    PRIMARY KEY (user_id, tab_id)
);
CREATE INDEX idx_workspace_tab_rendered_workspace ON workspace_tab_rendered(workspace_id);
-- No tab_id-only index: LocateAccessibleRenderedTab now binds user_id as well,
-- so PRIMARY KEY (user_id, tab_id) serves it as a point lookup. A standalone
-- tab_id index would be write amplification with no reader.

CREATE TABLE user_recent_batch_ids (
    user_id                TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    batch_id              TEXT COLLATE "C" NOT NULL,
    body_hash             BYTEA NOT NULL,
    principal_id          TEXT COLLATE "C" NOT NULL,
    canonical_physical_ms BIGINT NOT NULL,
    canonical_logical     BIGINT NOT NULL,
    canonical_client      TEXT COLLATE "C" NOT NULL,
    op_count              INTEGER NOT NULL CHECK (op_count > 0),
    epoch                 BIGINT NOT NULL,
    expires_at            TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, batch_id)
);
CREATE INDEX idx_user_recent_batch_ids_expires ON user_recent_batch_ids(expires_at);

CREATE TABLE lifecycle_outbox (
    id          BIGSERIAL PRIMARY KEY,
    user_id      TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    op_type     TEXT NOT NULL,
    payload     BYTEA NOT NULL,
    enqueued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    consumed_at TIMESTAMPTZ
);
CREATE INDEX idx_lifecycle_outbox_pending ON lifecycle_outbox(user_id, id) WHERE consumed_at IS NULL;

CREATE TABLE revocation_events (
    id         TEXT COLLATE "C" PRIMARY KEY,
    kind       TEXT NOT NULL CHECK (kind IN ('session', 'session_revoked', 'api_token', 'api_token_rotation', 'delegation_token', 'user_tokens', 'user_info')),
    subject_id TEXT COLLATE "C" NOT NULL,
    user_id    TEXT COLLATE "C" NOT NULL,
    revoked_at TIMESTAMPTZ NOT NULL,
    user_auth_generation BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    seq BIGINT UNIQUE CHECK (seq IS NULL OR seq > 0),
    published_at TIMESTAMPTZ,
    CHECK ((seq IS NULL) = (published_at IS NULL))
);
CREATE INDEX idx_revocation_events_pending ON revocation_events(created_at, id) WHERE seq IS NULL;
CREATE INDEX idx_revocation_events_published ON revocation_events(published_at, seq) WHERE seq IS NOT NULL;
CREATE INDEX idx_revocation_events_session_revoked ON revocation_events(subject_id) WHERE kind = 'session_revoked';

CREATE TABLE revocation_event_sequence (
    id       INTEGER PRIMARY KEY CHECK (id = 1),
    last_seq BIGINT NOT NULL CHECK (last_seq >= 0)
);
INSERT INTO revocation_event_sequence (id, last_seq) VALUES (1, 0);

CREATE TABLE hub_runtime_lease (
    singleton_id     SMALLINT PRIMARY KEY CHECK (singleton_id = 1),
    holder_id        TEXT COLLATE "C" NOT NULL CHECK (holder_id <> ''),
    cursor_seq       BIGINT NOT NULL CHECK (cursor_seq >= 0),
    lease_expires_at TIMESTAMPTZ NOT NULL
);

-- Registered apps. See the sqlite migration for the full rationale on each
-- column; the shape is identical.
CREATE TABLE oauth_clients (
    client_id             TEXT COLLATE "C" PRIMARY KEY,
    -- NULL = hub-wide; non-NULL = that user's private app. One column carries
    -- the whole visibility rule.
    owner_user_id         TEXT COLLATE "C" REFERENCES users(id) ON DELETE CASCADE,
    created_by_user_id    TEXT COLLATE "C" REFERENCES users(id) ON DELETE SET NULL,
    -- NULL is a PUBLIC client and non-NULL is a CONFIDENTIAL one.
    secret_hash           BYTEA,
    client_name           TEXT NOT NULL,
    icon_blob             BYTEA,
    icon_media_type       TEXT NOT NULL DEFAULT '',
    client_uri            TEXT NOT NULL DEFAULT '',
    -- Newline-delimited exact-match list.
    redirect_uris         TEXT NOT NULL DEFAULT '',
    -- Space-delimited RFC 6749 section 3.3: the ceiling on what this app may
    -- reach. LIVE rather than consent-time, and narrowing only; see the sqlite
    -- twin.
    scopes                TEXT NOT NULL DEFAULT '',
    grant_types           TEXT NOT NULL DEFAULT 'authorization_code refresh_token',
    elevation_allowed     BOOLEAN NOT NULL DEFAULT FALSE,
    registration_source   TEXT COLLATE "C" NOT NULL
        CHECK (registration_source IN ('builtin', 'admin', 'user', 'dynamic')),
    -- WHO vouched, and WHEN. The two move together, which the CHECK below
    -- enforces: a row with one and not the other describes a vouch nobody can
    -- read.
    --
    -- verified_by_user_id carries NO foreign key, and that is what makes the
    -- CHECK possible. ON DELETE SET NULL would clear this column while
    -- verified_at stayed set, which violates the CHECK -- so deleting a
    -- vouching administrator would fail, and TiDB refuses the schema outright
    -- rather than at that moment. The column is an ATTRIBUTION rather than a
    -- live reference: an administrator vouched for this app on this date, and
    -- deleting their account does not unmake that. The surface renders a name
    -- it cannot resolve as no name, which is the honest reading.
    verified_at           TIMESTAMPTZ,
    verified_by_user_id   TEXT COLLATE "C",
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at            TIMESTAMPTZ,
    CHECK ((verified_at IS NULL) = (verified_by_user_id IS NULL))
);
CREATE INDEX idx_oauth_clients_owner ON oauth_clients(owner_user_id, created_at DESC, client_id DESC);
CREATE INDEX idx_oauth_clients_revoked_at ON oauth_clients(revoked_at) WHERE revoked_at IS NOT NULL;

-- The two built-in registrations are NOT seeded here; see the sqlite
-- migration's note. store.SeedBuiltIns seeds and reconciles them on every
-- store open, rewriting only the columns that are constants of the build.

-- See sqlite migration for full rationale on api_tokens.
CREATE TABLE api_tokens (
    id                            TEXT COLLATE "C" PRIMARY KEY,
    user_id                       TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- RESTRICT, not CASCADE: deleting an app with live credentials is refused;
    -- the surface offers revoke, which cascades with lifecycle effects.
    client_id                     TEXT COLLATE "C" NOT NULL REFERENCES oauth_clients(client_id) ON DELETE RESTRICT,
    installation_name             TEXT NOT NULL,
    -- No DEFAULT: an INSERT that omits the grant must fail at the schema.
    granted_scopes                TEXT NOT NULL,
    secret_hash                   BYTEA NOT NULL,
    refresh_hash                  BYTEA,
    previous_refresh_hash         BYTEA,
    previous_refresh_expires_at   TIMESTAMPTZ,
    created_at                    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    auth_generation               BIGINT NOT NULL DEFAULT 0,
    last_used_at                  TIMESTAMPTZ,
    last_rotated_at               TIMESTAMPTZ,
    expires_at                    TIMESTAMPTZ,
    refresh_expires_at            TIMESTAMPTZ,
    revoked_at                    TIMESTAMPTZ,
    -- Elevation ("sudo mode") for an app credential, the same pair
    -- user_sessions carries and enforced by the same rule. The credential
    -- proves a factor through the browser step-up leg (/oauth/step-up), and
    -- every restricted action slides elevation_expires_at forward, clamped to
    -- elevation_proven_at + the maximum total window. The leg and the write
    -- both require oauth_clients.elevation_allowed.
    --
    -- Without it a stolen credential file administered the hub outright: the
    -- gate that protects the settings, the user surface and the mint admitted
    -- any bearer, because a bearer had no row to stamp. It has one now, so
    -- possession of the file is no longer sufficient on its own.
    elevation_proven_at           TIMESTAMPTZ,
    elevation_expires_at          TIMESTAMPTZ,
    CHECK ((elevation_proven_at IS NULL) = (elevation_expires_at IS NULL))
);
-- user_id only: the remaining job is the user_id seek for the
-- ByUserIncludingRevoked listing, which the partial
-- idx_api_tokens_user_created cannot serve.
CREATE INDEX idx_api_tokens_user ON api_tokens(user_id);
-- Serves the app-connection listing and the RESTRICT check on deleting an app.
CREATE INDEX idx_api_tokens_client ON api_tokens(client_id);
CREATE INDEX idx_api_tokens_revoked_at ON api_tokens(revoked_at) WHERE revoked_at IS NOT NULL;
-- Keyset index for the admin ListAllAPITokens listing: partial on
-- revoked_at IS NULL to match the query's live-token filter, with the trailing
-- id DESC so the composite ORDER BY rides the index instead of top-N sorting.
CREATE INDEX idx_api_tokens_created_at ON api_tokens(created_at DESC, id DESC) WHERE revoked_at IS NULL;
-- Keyset index for the admin ListAllAPITokensByUser listing (the --user-id
-- path): the leading user_id equality seeks, and (created_at DESC, id DESC)
-- rides the composite ORDER BY -- without it the per-user page pays a seek on
-- idx_api_tokens_user plus a sort. Mirrors idx_workers_registered_by_created.
CREATE INDEX idx_api_tokens_user_created ON api_tokens(user_id, created_at DESC, id DESC) WHERE revoked_at IS NULL;
-- Serves the DeleteExpiredAPITokensBefore sweep of live-but-dead rows: seek
-- the tokens whose access expiry passed instead of scanning every live one.
-- expires_at is the driving column because the sweep's refresh term is an OR
-- with IS NULL, which no index can seek. Mirrors
-- idx_delegation_tokens_expires_at, partial on revoked_at IS NULL to match
-- the sweep's own filter.
CREATE INDEX idx_api_tokens_expires_at ON api_tokens(expires_at) WHERE revoked_at IS NULL;

CREATE TABLE delegation_tokens (
    id                            TEXT COLLATE "C" PRIMARY KEY,
    user_id                       TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    worker_id                     TEXT COLLATE "C" NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    agent_id                      TEXT COLLATE "C" NOT NULL DEFAULT '',
    terminal_id                   TEXT COLLATE "C" NOT NULL DEFAULT '',
    issued_for_tab_id             TEXT COLLATE "C" NOT NULL DEFAULT '',
    issued_for_tab_type           INTEGER NOT NULL DEFAULT 0,
    -- What the minting worker delegated, already narrowed to the delegation
    -- ceiling. No DEFAULT, as on api_tokens.
    granted_scopes                TEXT NOT NULL,
    secret_hash                   BYTEA NOT NULL,
    refresh_hash                  BYTEA,
    created_at                    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    auth_generation               BIGINT NOT NULL DEFAULT 0,
    last_used_at                  TIMESTAMPTZ,
    expires_at                    TIMESTAMPTZ NOT NULL,
    refresh_expires_at            TIMESTAMPTZ,
    revoked_at                    TIMESTAMPTZ
);
CREATE INDEX idx_delegation_tokens_user ON delegation_tokens(user_id);
CREATE INDEX idx_delegation_tokens_worker_agent ON delegation_tokens(worker_id, agent_id);
CREATE INDEX idx_delegation_tokens_revoked_at ON delegation_tokens(revoked_at) WHERE revoked_at IS NOT NULL;
-- Keyset index for the admin ListAllDelegationTokens listing (see
-- idx_api_tokens_created_at for the rationale).
CREATE INDEX idx_delegation_tokens_created_at ON delegation_tokens(created_at DESC, id DESC) WHERE revoked_at IS NULL;
-- Per-user keyset twin (see idx_api_tokens_user_created).
CREATE INDEX idx_delegation_tokens_user_created ON delegation_tokens(user_id, created_at DESC, id DESC) WHERE revoked_at IS NULL;
-- Serves the hourly DeleteExpiredDelegationTokensBefore sweep of this
-- high-churn table: seek the expired live rows instead of scanning every
-- live token. Partial on revoked_at IS NULL to match the sweep's filter.
CREATE INDEX idx_delegation_tokens_expires_at ON delegation_tokens(expires_at) WHERE revoked_at IS NULL;

CREATE TABLE device_authorizations (
    device_code           TEXT COLLATE "C" PRIMARY KEY,
    user_code             TEXT COLLATE "C" NOT NULL UNIQUE,
    device_name           TEXT NOT NULL DEFAULT '',
    -- Which app started the flow; see the sqlite migration.
    client_id             TEXT COLLATE "C" NOT NULL REFERENCES oauth_clients(client_id) ON DELETE RESTRICT,
    -- What the app asked for; the row is the only carrier across the two
    -- machines this flow spans.
    requested_scopes      TEXT NOT NULL DEFAULT '',
    -- What the approval granted. It binds at the consent.
    granted_scopes        TEXT NOT NULL DEFAULT '',
    user_id               TEXT COLLATE "C" REFERENCES users(id) ON DELETE CASCADE,
    approved              INTEGER NOT NULL DEFAULT 0,        -- 0 pending, 1 approved, 2 denied
    last_polled_at        TIMESTAMPTZ,
    interval_seconds      INTEGER NOT NULL DEFAULT 5,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at            TIMESTAMPTZ NOT NULL,
    consumed_at           TIMESTAMPTZ,
    -- The app credential this grant ELEVATES, when it elevates one
    -- rather than minting one. NULL for a login: those two flows differ only
    -- in what the approval does, so they share the row, the TTL, the poll
    -- throttle, the expiry sweep and the activation page.
    --
    -- No foreign key, deliberately: the row outlives nothing here, and a
    -- revoked or deleted credential must make the approval a no-op rather
    -- than fail the insert. The approval re-reads api_tokens under the
    -- approving user's own id, so a grant that specifies a row somebody else
    -- owns elevates nothing.
    elevate_token_id      TEXT COLLATE "C"
);
CREATE INDEX idx_device_authorizations_expires_at ON device_authorizations(expires_at);

-- One-shot RFC 6749 section 4.1 authorization codes; see the sqlite migration.
CREATE TABLE oauth_authorization_codes (
    code                  TEXT COLLATE "C" PRIMARY KEY,
    user_id               TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_id             TEXT COLLATE "C" NOT NULL REFERENCES oauth_clients(client_id) ON DELETE RESTRICT,
    code_challenge        TEXT NOT NULL,
    redirect_uri          TEXT NOT NULL DEFAULT '',
    granted_scopes        TEXT NOT NULL DEFAULT '',
    installation_name     TEXT NOT NULL DEFAULT '',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at            TIMESTAMPTZ NOT NULL,
    consumed_at           TIMESTAMPTZ,
    minted_token_id       TEXT COLLATE "C"
);
CREATE INDEX idx_oauth_authorization_codes_expires_at ON oauth_authorization_codes(expires_at);

-- OAuth identity providers (admin-configured)
CREATE TABLE oauth_providers (
    id              TEXT COLLATE "C" PRIMARY KEY,
    provider_type   TEXT NOT NULL,
    name            TEXT NOT NULL,
    issuer_url      TEXT NOT NULL DEFAULT '',
    client_id       TEXT COLLATE "C" NOT NULL,
    client_secret   BYTEA NOT NULL,
    scopes          TEXT NOT NULL DEFAULT 'openid profile email',
    trust_email     BOOLEAN NOT NULL DEFAULT TRUE,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Links between local users and OAuth provider identities
CREATE TABLE oauth_user_links (
    user_id          TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider_id      TEXT COLLATE "C" NOT NULL REFERENCES oauth_providers(id) ON DELETE CASCADE,
    provider_subject TEXT COLLATE "C" NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, provider_id)
);
CREATE UNIQUE INDEX idx_oauth_user_links_provider_subject ON oauth_user_links(provider_id, provider_subject);

-- Encrypted OAuth tokens per user per provider
CREATE TABLE oauth_tokens (
    user_id         TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider_id     TEXT COLLATE "C" NOT NULL REFERENCES oauth_providers(id) ON DELETE CASCADE,
    access_token    BYTEA NOT NULL,
    refresh_token   BYTEA NOT NULL,
    token_type      TEXT NOT NULL DEFAULT 'Bearer',
    expires_at      TIMESTAMPTZ NOT NULL,
    key_version     BIGINT NOT NULL DEFAULT 1,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, provider_id)
);
CREATE INDEX idx_oauth_tokens_provider_id ON oauth_tokens(provider_id);
CREATE INDEX idx_oauth_tokens_expires_at ON oauth_tokens(expires_at);
CREATE INDEX idx_oauth_tokens_key_version ON oauth_tokens(key_version);

-- Short-lived OAuth state for CSRF + PKCE during auth flow
CREATE TABLE oauth_states (
    state           TEXT COLLATE "C" PRIMARY KEY,
    provider_id     TEXT COLLATE "C" NOT NULL REFERENCES oauth_providers(id),
    pkce_verifier   TEXT NOT NULL,
    nonce_hash      TEXT COLLATE "C" NOT NULL DEFAULT '',
    redirect_uri    TEXT NOT NULL DEFAULT '',
    -- 'login' starts a sign-in; 'reauth' proves the identity again for an
    -- ALREADY signed-in session, to elevate it. The callback branches on
    -- this: a reauth state must never create a session or link an identity.
    -- The CHECK is the enforcement, not the DEFAULT. Go's zero value for the
    -- column is "", never 'login', so an explicit insert never reaches the
    -- DEFAULT, and the callback treats every value that is not 'reauth' as a
    -- login -- which may create a session or link an identity.
    purpose         TEXT COLLATE "C" NOT NULL DEFAULT 'login' CHECK (purpose IN ('login', 'reauth')),
    -- The session the reauth leg elevates on success. Empty for 'login'.
    session_id      TEXT COLLATE "C" NOT NULL DEFAULT '',
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Pending OAuth signups (new users choosing their username)
CREATE TABLE pending_oauth_signups (
    token            TEXT COLLATE "C" PRIMARY KEY,
    provider_id      TEXT COLLATE "C" NOT NULL REFERENCES oauth_providers(id),
    provider_subject TEXT COLLATE "C" NOT NULL,
    nonce_hash       TEXT COLLATE "C" NOT NULL DEFAULT '',
    email            TEXT NOT NULL DEFAULT '',
    display_name     TEXT NOT NULL DEFAULT '',
    access_token     BYTEA NOT NULL,
    refresh_token    BYTEA NOT NULL,
    token_type       TEXT NOT NULL DEFAULT 'Bearer',
    token_expires_at TIMESTAMPTZ NOT NULL,
    key_version      BIGINT NOT NULL DEFAULT 1,
    redirect_uri     TEXT NOT NULL DEFAULT '',
    expires_at       TIMESTAMPTZ NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Passkey (WebAuthn) credentials for email-local accounts
CREATE TABLE passkey_credentials (
    id              TEXT COLLATE "C" PRIMARY KEY,
    user_id         TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credential_id   BYTEA NOT NULL,   -- plaintext: login lookup + unique index
    public_key      BYTEA NOT NULL,   -- keystore-encrypted COSE public key, AAD: 'passkey_public_key:' || id
    sign_count      BIGINT NOT NULL DEFAULT 0,
    aaguid          BYTEA,
    backup_eligible BOOLEAN NOT NULL DEFAULT FALSE,
    backup_state    BOOLEAN NOT NULL DEFAULT FALSE,
    transports      TEXT NOT NULL DEFAULT '[]',
    friendly_name   TEXT NOT NULL DEFAULT '',
    key_version     BIGINT NOT NULL DEFAULT 1,  -- active keystore version at write; for reencrypt scans
    created_at      TIMESTAMPTZ NOT NULL,
    last_used_at    TIMESTAMPTZ
);
CREATE UNIQUE INDEX idx_passkey_credentials_credential_id ON passkey_credentials(credential_id);
CREATE INDEX idx_passkey_credentials_user_id ON passkey_credentials(user_id);
CREATE INDEX idx_passkey_credentials_key_version ON passkey_credentials(key_version);

-- Ephemeral WebAuthn ceremony state (signup, login, register, elevation)
CREATE TABLE webauthn_sessions (
    id           TEXT COLLATE "C" PRIMARY KEY,
    kind         TEXT NOT NULL CHECK (kind IN (
        'signup', 'login', 'register', 'elevation'
    )),
    user_id      TEXT COLLATE "C" REFERENCES users(id) ON DELETE CASCADE,
    payload_json TEXT NOT NULL DEFAULT '{}',  -- '{}' or keystore-encrypted signup draft (base64), AAD: 'webauthn_payload:' || id
    session_data BYTEA NOT NULL,             -- keystore-encrypted ceremony state, AAD: 'webauthn_session:' || id
    expires_at   TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_webauthn_sessions_expires_at ON webauthn_sessions(expires_at);

-- Ceremony begins delete prior rows per user and kind on every Begin*;
-- without this index each begin full-scans the not-yet-swept rows.
CREATE INDEX idx_webauthn_sessions_user_kind ON webauthn_sessions(user_id, kind);

-- Instance-level hub settings: one row per setting key. `value` is a JSON
-- document whose shape is defined by the owning package's typed key handle
-- (internal/hub/settings); secret-bearing keys keep the secret half in the
-- keystore-encrypted column (AAD bound to the key name). An absent row means
-- the code default, so adding, removing, or reshaping a setting is a Go
-- change only -- never a migration. This table is the single home for all
-- runtime-changeable configuration (auth policy, SMTP, timeouts, limits,
-- captcha providers, rate-limit overrides).
CREATE TABLE hub_settings (
    key        TEXT COLLATE "C" PRIMARY KEY,
    value      TEXT,      -- JSON document, public half
    secret     BYTEA,     -- keystore-encrypted JSON, secret half
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (value IS NOT NULL OR secret IS NOT NULL)
);

-- Consumed ALTCHA salts: single-use enforcement for solved challenges,
-- shared across hub instances and restarts. A row's presence means the
-- salt's solution was accepted once; the cleanup loop purges rows past
-- their challenge expiry. External providers (reCAPTCHA, Turnstile)
-- enforce single use at their siteverify endpoint and need no table.
CREATE TABLE altcha_used_salts (
    salt       TEXT COLLATE "C" PRIMARY KEY,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_altcha_used_salts_expires_at ON altcha_used_salts(expires_at);

-- +goose Down
DROP TABLE IF EXISTS oauth_authorization_codes;
DROP TABLE IF EXISTS device_authorizations;
DROP TABLE IF EXISTS delegation_tokens;
DROP TABLE IF EXISTS api_tokens;
DROP TABLE IF EXISTS oauth_clients;
DROP TABLE IF EXISTS hub_runtime_lease;
DROP TABLE IF EXISTS revocation_events;
DROP TABLE IF EXISTS revocation_event_sequence;
DROP TABLE IF EXISTS pending_oauth_signups;
DROP TABLE IF EXISTS altcha_used_salts;
DROP TABLE IF EXISTS webauthn_sessions;
DROP TABLE IF EXISTS passkey_credentials;
DROP TABLE IF EXISTS hub_settings;
DROP TABLE IF EXISTS oauth_states;
DROP TABLE IF EXISTS oauth_tokens;
DROP TABLE IF EXISTS oauth_user_links;
DROP TABLE IF EXISTS oauth_providers;
DROP TABLE IF EXISTS lifecycle_outbox;
DROP TABLE IF EXISTS user_recent_batch_ids;
DROP TABLE IF EXISTS workspace_tab_rendered;
DROP TABLE IF EXISTS workspace_tab_owned;
DROP TABLE IF EXISTS user_state;
DROP TABLE IF EXISTS user_op_batches;
DROP TABLE IF EXISTS workspace_section_items;
DROP TABLE IF EXISTS workspace_sections;
DROP TABLE IF EXISTS workspaces;
DROP TABLE IF EXISTS worker_registration_keys;
DROP TABLE IF EXISTS worker_notifications;
DROP TABLE IF EXISTS workers;
DROP TABLE IF EXISTS user_sessions;
DROP TABLE IF EXISTS users;
