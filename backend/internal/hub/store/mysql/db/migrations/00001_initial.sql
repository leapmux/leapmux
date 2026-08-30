-- +goose Up

-- Binary collation is declared per-table (COLLATE=utf8mb4_bin) so every string
-- column -- ids and FK columns alike -- collates byte-wise (case-sensitive),
-- keeping cross-table FK collations consistent. SET NAMES alone cannot do this:
-- it sets the *session* collation, not the column collation the tables inherit.
-- Every CREATE TABLE here and in future migrations MUST carry
-- COLLATE=utf8mb4_bin: a table without it silently inherits the server or
-- database default (typically case-INsensitive), breaking the byte-wise id
-- tiebreak ordering and FK collation consistency. Enforced by
-- TestEveryCreateTableDeclaresBinaryCollation (schema_internal_test.go), which
-- scans every migration file in this directory, and by its live twin
-- TestMySQLBinaryCollationLive (-tags integration). See
-- https://github.com/leapmux/leapmux/issues/300. The database-level default
-- is intentionally left to the operator (the app connects to a pre-created
-- database and owns only its tables).
-- Username/email/display-name case-insensitivity is handled at the application
-- layer (NormalizeUsername/NormalizeEmail lowercases on write and lookup).
--
-- TEXT/BLOB columns intentionally carry NO DEFAULT, unlike their sqlite/
-- postgres twins: MySQL only allows the expression form -- DEFAULT ('') --
-- on TEXT/BLOB, and TiDB (which runs this same migration) rejects that
-- form outright. Every INSERT must therefore supply these columns
-- explicitly.
SET NAMES utf8mb4 COLLATE utf8mb4_bin;

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
    id             VARCHAR(255) PRIMARY KEY CHECK (id <> ''),
    username       VARCHAR(255) NOT NULL,
    password_hash  TEXT NOT NULL,
    display_name   TEXT NOT NULL,
    -- Unicode-casefolded (Go strings.ToLower) copy of display_name, maintained on
    -- every write, so admin SearchUsers matches non-ASCII names case-insensitively
    -- and identically across SQLite/Postgres/MySQL (SQLite folds only ASCII, so a
    -- plain LIKE on this pre-folded column keeps the three dialects in agreement).
    display_name_folded      VARCHAR(255) NOT NULL DEFAULT '',
    email                    VARCHAR(255) NOT NULL DEFAULT '',
    email_verified           BOOLEAN NOT NULL DEFAULT FALSE,
    pending_email            VARCHAR(255) NOT NULL DEFAULT '',
    -- Stored verification code in raw 6-char form (no hyphen), drawn from
    -- verifycode.Charset. Empty when no verification is pending.
    pending_email_token      VARCHAR(16) NOT NULL DEFAULT '',
    pending_email_expires_at DATETIME(3),
    -- Counts attempts against the active pending_email_token. Reset to 0
    -- whenever a new token is issued; force-expires the token at >5.
    pending_email_attempts   INT NOT NULL DEFAULT 0,
    -- When the current verification code was issued. ONLY SetPendingEmail
    -- writes it; the resend-cooldown gate compares this column directly
    -- rather than deriving the issue time from the expiry, because
    -- ConsumeVerificationAttempt force-expires a burned code by moving
    -- the expiry to now -- a derivation would then read a brand-new
    -- burned code as issued a full lifetime ago and re-mint inside the
    -- cooldown.
    pending_email_issued_at    DATETIME(3),
    -- Account-recovery break-glass: token stored hashed (SHA-256 hex).
    pending_recovery_token      VARCHAR(64) NOT NULL DEFAULT '',
    pending_recovery_expires_at DATETIME(3),
    pending_recovery_attempts   INT NOT NULL DEFAULT 0,
    -- When the current recovery link was issued; the cooldown gate reads
    -- this, not the expiry, for the reason pending_email_issued_at states
    -- (ConsumeRecoveryAttemptByToken force-expires the same way).
    pending_recovery_issued_at DATETIME(3),
    password_set             BOOLEAN NOT NULL DEFAULT TRUE,
    is_admin                 BOOLEAN NOT NULL DEFAULT FALSE,
    prefs          MEDIUMTEXT NOT NULL,
    created_at     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    -- High-water mark bumped whenever this user's auth basis is
    -- bulk-revoked. Each bump also records a durable user-token
    -- revocation event so cookie channels and bearer caches die together
    -- with admin-CLI mutations that run in a separate process.
    tokens_revoked_at        DATETIME(3),
    -- Monotonic credential epoch. Sessions and bearer rows copy this
    -- value when issued; user-wide revocation increments it so stale
    -- credentials fail without depending on timestamp precision or
    -- cross-host clock agreement.
    auth_generation          BIGINT NOT NULL DEFAULT 0,
    deleted_at     DATETIME(3),
    -- Generated columns for partial unique index emulation
    active_username VARCHAR(255) GENERATED ALWAYS AS (CASE WHEN deleted_at IS NULL THEN username ELSE NULL END) STORED,
    active_email    VARCHAR(255) GENERATED ALWAYS AS (CASE WHEN deleted_at IS NULL AND email != '' THEN email ELSE NULL END) STORED
) COLLATE=utf8mb4_bin;
CREATE UNIQUE INDEX idx_users_active_username ON users(active_username);
CREATE UNIQUE INDEX idx_users_active_email ON users(active_email);
CREATE INDEX idx_users_deleted_at ON users(deleted_at);
CREATE INDEX idx_users_created_at ON users(created_at DESC, id DESC);
-- Verification codes are looked up per-user (the session identifies who),
-- so no global token index is needed. Index expiry instead, for cleanup.
CREATE INDEX idx_users_pending_email_expires_at ON users(pending_email_expires_at);
-- Recovery tokens are looked up by hash on complete. MySQL has no
-- partial indexes, so this covers every row (empty token is the common case).
CREATE INDEX idx_users_pending_recovery_token ON users(pending_recovery_token);
-- GetFirstAdmin scans for the earliest non-deleted admin (bootstrap path).
-- MySQL has no partial indexes; the composite indexes deleted_at as a key
-- part (IS NULL is a ref-able key part in MySQL), so the ORDER BY created_at
-- + LIMIT 1 lands on the first live admin directly instead of walking past
-- soft-deleted admins as a residual filter.
CREATE INDEX idx_users_is_admin ON users(is_admin, deleted_at, created_at);

-- Auth sessions
CREATE TABLE user_sessions (
    id              VARCHAR(255) PRIMARY KEY,
    user_id         VARCHAR(255) NOT NULL,
    expires_at      DATETIME(3) NOT NULL,
    created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    last_active_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
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
    elevation_proven_at     DATETIME(3),
    elevation_expires_at  DATETIME(3),
    user_agent      TEXT NOT NULL,
    ip_address      VARCHAR(255) NOT NULL DEFAULT '',
    -- Both or neither, enforced HERE rather than re-checked at every read.
    -- Half a pair is not a state the rest of the hub should have to consider:
    -- a deadline with no anchor is one the slide statement cannot clamp,
    -- because it has nothing to measure the absolute cap from. Same shape as
    -- the (seq, published_at) pair in revocation_events below.
    CHECK ((elevation_proven_at IS NULL) = (elevation_expires_at IS NULL)),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
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
    id            VARCHAR(255) PRIMARY KEY,
    auth_token    VARCHAR(255) NOT NULL UNIQUE,
    registered_by VARCHAR(255) NOT NULL,
    status        INT NOT NULL DEFAULT 1,
    created_at    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    last_seen_at  DATETIME(3),
    public_key    BLOB NOT NULL,
    mlkem_public_key  BLOB NOT NULL,
    slhdsa_public_key BLOB NOT NULL,
    -- True for rows created by Server.RegisterWorker, the in-process
    -- bypass the solo launcher uses to bring up the co-located local
    -- worker. The deregister handler refuses these so the user can't
    -- accidentally tear down the bundled desktop worker -- it would just
    -- re-register on next launch and the running process would noisily
    -- exit with "invalid auth token" in between.
    auto_registered BOOLEAN NOT NULL DEFAULT FALSE,
    deleted_at    DATETIME(3),
    FOREIGN KEY (registered_by) REFERENCES users(id)
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_workers_registered_by_status_created ON workers(registered_by, status, created_at DESC, id DESC);
-- Admin status-only listing (ListWorkersAdminByStatus) cannot use the
-- (registered_by, status, created_at) index because registered_by is the
-- leading column.
CREATE INDEX idx_workers_status_created ON workers(status, created_at DESC, id DESC);
-- Admin per-user listing (ListWorkersAdminByUser: registered_by=?, deleted_at IS
-- NULL, no status filter). The (registered_by, status, created_at, id) composite
-- above can't serve this query's ORDER BY because status sits between the
-- registered_by equality and the created_at sort key. MySQL has no partial
-- indexes, so deleted_at IS NULL is a residual filter rather than an index
-- predicate (mirrors idx_workers_created_at below).
CREATE INDEX idx_workers_registered_by_created ON workers(registered_by, created_at DESC, id DESC);
CREATE INDEX idx_workers_deleted_at ON workers(deleted_at);
CREATE INDEX idx_workers_created_at ON workers(created_at DESC, id DESC);

-- Worker notifications (persistent queue for reliable delivery)
CREATE TABLE worker_notifications (
    id           VARCHAR(255) PRIMARY KEY,
    worker_id    VARCHAR(255) NOT NULL,
    type         INT NOT NULL,
    payload      TEXT NOT NULL,
    status       INT NOT NULL DEFAULT 1,
    attempts     INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 5,
    created_at   DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    delivered_at DATETIME(3),
    FOREIGN KEY (worker_id) REFERENCES workers(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
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
    id          VARCHAR(255) PRIMARY KEY,
    created_by  VARCHAR(255) NOT NULL,
    created_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    expires_at  DATETIME(3) NOT NULL,
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_worker_registration_keys_expires_at ON worker_registration_keys(expires_at);
CREATE INDEX idx_worker_registration_keys_created_by ON worker_registration_keys(created_by);
CREATE INDEX idx_worker_registration_keys_created_at ON worker_registration_keys(created_at DESC, id DESC);

-- Sidebar sections (per-user grouping of sidebar panels)
CREATE TABLE workspace_sections (
    id           VARCHAR(255) PRIMARY KEY,
    user_id      VARCHAR(255) NOT NULL,
    name         TEXT NOT NULL,
    position     TEXT NOT NULL,
    section_type INT NOT NULL DEFAULT 1,
    sidebar      INT NOT NULL DEFAULT 1,
    created_at   DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    -- NULL for a custom section, the type itself otherwise. Carries the
    -- uniqueness below, because MySQL has no partial index: a unique index
    -- admits any number of NULLs, so the custom rows opt out by being NULL
    -- while every default type is unique per user. STORED rather than VIRTUAL
    -- so it can be indexed on every supported engine.
    default_section_type INT AS (CASE WHEN section_type <> 1 THEN section_type END) STORED,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_workspace_sections_user_id ON workspace_sections(user_id);
-- One default section of each type per user. section_type = 1 is
-- SECTION_TYPE_WORKSPACES_CUSTOM, which a user may hold any number of, so the
-- uniqueness applies to every OTHER type only -- see default_section_type above
-- for how the schema expresses that exemption without a partial index.
--
-- Structural, not procedural: CreateUser writes the defaults in the same
-- transaction as the user row and nothing backfills them, so a second signup
-- path that forgot to seed -- or a read path that seeded during the read, which
-- is what this replaced -- used to produce a sidebar with two of every pane,
-- indistinguishable from one another.
CREATE UNIQUE INDEX idx_workspace_sections_user_default_type
    ON workspace_sections(user_id, default_section_type);

-- Workspaces (hub-owned registry) -- must come before workspace_section_items
CREATE TABLE workspaces (
    id            VARCHAR(255) PRIMARY KEY,
    owner_user_id VARCHAR(255) NOT NULL,
    title         TEXT NOT NULL,
    is_deleted    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    deleted_at    DATETIME(3),
    FOREIGN KEY (owner_user_id) REFERENCES users(id)
) COLLATE=utf8mb4_bin;
-- MySQL has no partial indexes; (owner_user_id, is_deleted) covers the
-- live-owner ListAccessible seek that SQLite/Postgres express as
-- idx_workspaces_owner_live WHERE is_deleted = 0/FALSE.
--
-- There is deliberately no separate idx_workspaces_owner_user_id here, unlike
-- SQLite/Postgres. Because this index is a plain composite rather than a
-- partial one, its leftmost prefix is owner_user_id, so it also serves the
-- un-filtered owner probes: HardDeleteUsersBefore's NOT EXISTS gate and the
-- owner_user_id foreign key (InnoDB adds no auto-index when an existing index
-- has the FK column as its leftmost prefix). SQLite/Postgres still need the
-- plain index because their live-owner index is partial and cannot answer a
-- query that must see soft-deleted rows.
CREATE INDEX idx_workspaces_owner_live ON workspaces(owner_user_id, is_deleted);
CREATE INDEX idx_workspaces_deleted_at ON workspaces(deleted_at);

-- Workspace-to-section assignments (per-user)
CREATE TABLE workspace_section_items (
    user_id      VARCHAR(255) NOT NULL,
    workspace_id VARCHAR(255) NOT NULL,
    section_id   VARCHAR(255) NOT NULL,
    position     TEXT NOT NULL,
    PRIMARY KEY (user_id, workspace_id),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (section_id) REFERENCES workspace_sections(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_workspace_section_items_section ON workspace_section_items(section_id);

-- See sqlite migration for full rationale on the CRDT schema.
CREATE TABLE user_op_batches (
    user_id       VARCHAR(255) NOT NULL,
    physical_ms   BIGINT NOT NULL,
    logical       BIGINT NOT NULL,
    last_logical  BIGINT NOT NULL,
    origin_client VARCHAR(255) NOT NULL,
    principal_id  VARCHAR(255) NOT NULL,
    batch_id      VARCHAR(255) NOT NULL,
    body_hash     BLOB NOT NULL,
    batch_payload LONGBLOB NOT NULL,
    transitions_payload LONGBLOB NOT NULL,
    op_count      INT NOT NULL CHECK (op_count > 0),
    epoch         BIGINT NOT NULL,
    committed_at  DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),  -- operator-facing audit stamp only; NOT a retention predicate (see idx_user_op_batches_physical_ms)
    PRIMARY KEY (user_id, physical_ms, logical, origin_client),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE UNIQUE INDEX idx_user_op_batches_dedup ON user_op_batches(user_id, batch_id);
-- Backs DeleteUserOpBatchesBeforePhysical, the cross-user retention sweep.
-- The PK leads with user_id, which that query does not bind, so without this
-- the hourly sweep full-scans every user's rows -- including the final pass
-- that exists only to prove nothing is left to delete.
CREATE INDEX idx_user_op_batches_physical_ms ON user_op_batches(physical_ms);

CREATE TABLE user_state (
    user_id           VARCHAR(255) NOT NULL,
    state_payload    LONGBLOB NOT NULL,
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
    epoch_started_at DATETIME(6) NOT NULL,
    updated_at       DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (user_id),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;

CREATE TABLE workspace_tab_owned (
    user_id      VARCHAR(255) NOT NULL,
    workspace_id VARCHAR(255) NOT NULL,
    tab_type     INT NOT NULL,
    tab_id       VARCHAR(255) NOT NULL,
    worker_id    VARCHAR(255) NOT NULL,
    tile_id      VARCHAR(255) NOT NULL,
    position     TEXT NOT NULL,
    PRIMARY KEY (user_id, tab_id),
    -- The users(id) FK mirrors the sibling CRDT tables (user_op_batches,
    -- user_state, user_recent_batch_ids, lifecycle_outbox): without it a
    -- blank-owner row is insertable but no delete path can bind it away.
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
-- (user_id, worker_id), not worker_id alone: ListOwnedTabsByWorker binds both,
-- so a single-column index could only seek worker_id and then re-filter every
-- match by owner.
CREATE INDEX idx_workspace_tab_owned_worker    ON workspace_tab_owned(user_id, worker_id);
CREATE INDEX idx_workspace_tab_owned_workspace ON workspace_tab_owned(workspace_id);

CREATE TABLE workspace_tab_rendered (
    user_id      VARCHAR(255) NOT NULL,
    workspace_id VARCHAR(255) NOT NULL,
    tab_type     INT NOT NULL,
    tab_id       VARCHAR(255) NOT NULL,
    worker_id    VARCHAR(255) NOT NULL,
    tile_id      VARCHAR(255) NOT NULL,
    position     TEXT NOT NULL,
    PRIMARY KEY (user_id, tab_id),
    -- Same users(id) FK as workspace_tab_owned above.
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_workspace_tab_rendered_workspace ON workspace_tab_rendered(workspace_id);
-- No tab_id-only index: LocateAccessibleRenderedTab now binds user_id as well,
-- so PRIMARY KEY (user_id, tab_id) serves it as a point lookup. A standalone
-- tab_id index would be write amplification with no reader.

CREATE TABLE user_recent_batch_ids (
    user_id                VARCHAR(255) NOT NULL,
    batch_id              VARCHAR(255) NOT NULL,
    body_hash             BLOB NOT NULL,
    principal_id          VARCHAR(255) NOT NULL,
    canonical_physical_ms BIGINT NOT NULL,
    canonical_logical     BIGINT NOT NULL,
    canonical_client      VARCHAR(255) NOT NULL,
    op_count              INT NOT NULL CHECK (op_count > 0),
    epoch                 BIGINT NOT NULL,
    expires_at            DATETIME(6) NOT NULL,
    PRIMARY KEY (user_id, batch_id),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_user_recent_batch_ids_expires ON user_recent_batch_ids(expires_at);

CREATE TABLE lifecycle_outbox (
    id          BIGINT PRIMARY KEY AUTO_INCREMENT,
    user_id      VARCHAR(255) NOT NULL,
    op_type     VARCHAR(16) NOT NULL,
    payload     LONGBLOB NOT NULL,
    enqueued_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    consumed_at DATETIME(6),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_lifecycle_outbox_pending ON lifecycle_outbox(user_id, id);

CREATE TABLE revocation_events (
    id         VARCHAR(255) PRIMARY KEY,
    kind       VARCHAR(32) NOT NULL CHECK (kind IN ('session', 'session_revoked', 'api_token', 'api_token_rotation', 'delegation_token', 'user_tokens', 'user_info')),
    subject_id VARCHAR(255) NOT NULL,
    user_id    VARCHAR(255) NOT NULL,
    revoked_at DATETIME(3) NOT NULL,
    user_auth_generation BIGINT NOT NULL DEFAULT 0,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    seq BIGINT UNIQUE CHECK (seq IS NULL OR seq > 0),
    published_at DATETIME(6),
    CHECK ((seq IS NULL) = (published_at IS NULL))
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_revocation_events_pending ON revocation_events(seq, created_at, id);
CREATE INDEX idx_revocation_events_published ON revocation_events(published_at, seq);
CREATE INDEX idx_revocation_events_session_revoked ON revocation_events(kind, subject_id);

CREATE TABLE revocation_event_sequence (
    id       INT PRIMARY KEY CHECK (id = 1),
    last_seq BIGINT NOT NULL CHECK (last_seq >= 0)
) COLLATE=utf8mb4_bin;
INSERT INTO revocation_event_sequence (id, last_seq) VALUES (1, 0);

CREATE TABLE hub_runtime_lease (
    singleton_id     TINYINT PRIMARY KEY CHECK (singleton_id = 1),
    holder_id        VARCHAR(64) NOT NULL CHECK (holder_id <> ''),
    cursor_seq       BIGINT NOT NULL CHECK (cursor_seq >= 0),
    lease_expires_at DATETIME(6) NOT NULL
) COLLATE=utf8mb4_bin;

-- See sqlite migration for full rationale on api_tokens.
-- Registered apps. See the sqlite migration for the full rationale on each
-- column; the shape is identical.
CREATE TABLE oauth_clients (
    client_id             VARCHAR(255) PRIMARY KEY,
    -- NULL = hub-wide; non-NULL = that user's private app. One column carries
    -- the whole visibility rule.
    owner_user_id         VARCHAR(255),
    created_by_user_id    VARCHAR(255),
    -- NULL is a PUBLIC client and non-NULL is a CONFIDENTIAL one.
    secret_hash           VARBINARY(64),
    client_name           VARCHAR(255) NOT NULL,
    icon_blob             BLOB,
    icon_media_type       VARCHAR(255) NOT NULL DEFAULT '',
    client_uri            VARCHAR(2048) NOT NULL DEFAULT '',
    -- Newline-delimited exact-match list.
    redirect_uris         TEXT NOT NULL,
    -- Space-delimited RFC 6749 section 3.3: the ceiling on what this app may
    -- reach. LIVE rather than consent-time, and narrowing only; see the sqlite
    -- twin.
    scopes                TEXT NOT NULL,
    grant_types           VARCHAR(512) NOT NULL DEFAULT 'authorization_code refresh_token',
    elevation_allowed     BOOLEAN NOT NULL DEFAULT FALSE,
    registration_source   VARCHAR(16) NOT NULL
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
    verified_at           DATETIME(3),
    verified_by_user_id   VARCHAR(255),
    created_at            DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at            DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    revoked_at            DATETIME(3),
    CHECK ((verified_at IS NULL) = (verified_by_user_id IS NULL)),
    FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (created_by_user_id) REFERENCES users(id) ON DELETE SET NULL
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_oauth_clients_owner ON oauth_clients(owner_user_id, created_at DESC, client_id DESC);
CREATE INDEX idx_oauth_clients_revoked_at ON oauth_clients(revoked_at);

-- The two built-in registrations are NOT seeded here; see the sqlite
-- migration's note. store.SeedBuiltIns seeds and reconciles them on every
-- store open, rewriting only the columns that are constants of the build.

CREATE TABLE api_tokens (
    id                            VARCHAR(255) PRIMARY KEY,
    user_id                       VARCHAR(255) NOT NULL,
    -- RESTRICT, not CASCADE: deleting an app with live credentials is refused;
    -- the surface offers revoke, which cascades with lifecycle effects.
    client_id                     VARCHAR(255) NOT NULL,
    installation_name             VARCHAR(255) NOT NULL,
    -- No DEFAULT: an INSERT that omits the grant must fail at the schema.
    granted_scopes                TEXT NOT NULL,
    secret_hash                   VARBINARY(64) NOT NULL,
    refresh_hash                  VARBINARY(64),
    previous_refresh_hash         VARBINARY(64),
    previous_refresh_expires_at   DATETIME(3),
    created_at                    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    auth_generation               BIGINT NOT NULL DEFAULT 0,
    last_used_at                  DATETIME(3),
    last_rotated_at               DATETIME(3),
    expires_at                    DATETIME(3),
    refresh_expires_at            DATETIME(3),
    revoked_at                    DATETIME(3),
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
    elevation_proven_at           DATETIME(3),
    elevation_expires_at          DATETIME(3),
    CHECK ((elevation_proven_at IS NULL) = (elevation_expires_at IS NULL)),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (client_id) REFERENCES oauth_clients(client_id) ON DELETE RESTRICT
) COLLATE=utf8mb4_bin;
-- Serves the app-connection statements ("which credentials does this app
-- hold?"), the disconnect cascade and the RESTRICT check on deleting an app:
-- without it every one of them scans api_tokens. The sqlite and postgres
-- twins carry the same index; MySQL's InnoDB would derive one from the
-- foreign key, but TiDB does not index an unenforced FK clause.
CREATE INDEX idx_api_tokens_client ON api_tokens(client_id);
CREATE INDEX idx_api_tokens_revoked_at ON api_tokens(revoked_at);
-- Serves the DeleteExpiredAPITokensBefore sweep of live-but-dead rows: seek
-- the tokens whose access expiry passed instead of scanning every live one.
-- expires_at is the driving column because the sweep's refresh term is an OR
-- with IS NULL, which no index can seek. Mirrors
-- idx_delegation_tokens_expires_at. MySQL has no partial indexes, so
-- revoked_at IS NULL stays a residual filter.
CREATE INDEX idx_api_tokens_expires_at ON api_tokens(expires_at);
-- Keyset index for the admin ListAllAPITokens listing. MySQL has no partial
-- indexes, so revoked_at IS NULL stays a residual filter; the (created_at DESC,
-- id DESC) shape still lets the composite ORDER BY ride the index.
CREATE INDEX idx_api_tokens_created_at ON api_tokens(created_at DESC, id DESC);
-- Keyset index for the admin ListAllAPITokensByUser listing (the --user-id
-- path): the leading user_id equality seeks, and (created_at DESC, id DESC)
-- rides the composite ORDER BY. Plain (no partial indexes in MySQL);
-- revoked_at IS NULL stays a residual filter. Mirrors
-- idx_workers_registered_by_created. Its leftmost user_id prefix also
-- enforces the user_id FK (no separate user_id index).
CREATE INDEX idx_api_tokens_user_created ON api_tokens(user_id, created_at DESC, id DESC);

CREATE TABLE delegation_tokens (
    id                            VARCHAR(255) PRIMARY KEY,
    user_id                       VARCHAR(255) NOT NULL,
    worker_id                     VARCHAR(255) NOT NULL,
    agent_id                      VARCHAR(255) NOT NULL DEFAULT '',
    terminal_id                   VARCHAR(255) NOT NULL DEFAULT '',
    issued_for_tab_id             VARCHAR(255) NOT NULL DEFAULT '',
    issued_for_tab_type           INT NOT NULL DEFAULT 0,
    -- What the minting worker delegated, already narrowed to the delegation
    -- ceiling. No DEFAULT, as on api_tokens.
    granted_scopes                TEXT NOT NULL,
    secret_hash                   VARBINARY(64) NOT NULL,
    refresh_hash                  VARBINARY(64),
    created_at                    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    auth_generation               BIGINT NOT NULL DEFAULT 0,
    last_used_at                  DATETIME(3),
    expires_at                    DATETIME(3) NOT NULL,
    refresh_expires_at            DATETIME(3),
    revoked_at                    DATETIME(3),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (worker_id) REFERENCES workers(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_delegation_tokens_worker_agent ON delegation_tokens(worker_id, agent_id);
CREATE INDEX idx_delegation_tokens_revoked_at ON delegation_tokens(revoked_at);
-- Keyset index for the admin ListAllDelegationTokens listing (see
-- idx_api_tokens_created_at for the rationale).
CREATE INDEX idx_delegation_tokens_created_at ON delegation_tokens(created_at DESC, id DESC);
-- Per-user keyset twin (see idx_api_tokens_user_created). Its leftmost
-- user_id prefix also enforces the user_id FK (no separate user_id index).
CREATE INDEX idx_delegation_tokens_user_created ON delegation_tokens(user_id, created_at DESC, id DESC);
-- Serves the hourly DeleteExpiredDelegationTokensBefore sweep of this
-- high-churn table: seek the expired live rows instead of scanning every
-- live token. Plain (no partial indexes in MySQL); revoked_at IS NULL is a
-- residual filter.
CREATE INDEX idx_delegation_tokens_expires_at ON delegation_tokens(expires_at);

CREATE TABLE device_authorizations (
    device_code           VARCHAR(255) PRIMARY KEY,
    user_code             VARCHAR(64) NOT NULL UNIQUE,
    device_name           VARCHAR(255) NOT NULL DEFAULT '',
    -- Which app started the flow; see the sqlite migration.
    client_id             VARCHAR(255) NOT NULL,
    -- What the app asked for; the row is the only carrier across the two
    -- machines this flow spans.
    requested_scopes      TEXT NOT NULL,
    -- What the approval granted. It binds at the consent.
    granted_scopes        TEXT NOT NULL,
    user_id               VARCHAR(255),
    approved              INT NOT NULL DEFAULT 0,        -- 0 pending, 1 approved, 2 denied
    last_polled_at        DATETIME(3),
    interval_seconds      INT NOT NULL DEFAULT 5,
    created_at            DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    expires_at            DATETIME(3) NOT NULL,
    consumed_at           DATETIME(3),
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
    elevate_token_id      VARCHAR(255),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (client_id) REFERENCES oauth_clients(client_id) ON DELETE RESTRICT
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_device_authorizations_expires_at ON device_authorizations(expires_at);

-- One-shot RFC 6749 section 4.1 authorization codes; see the sqlite migration.
CREATE TABLE oauth_authorization_codes (
    code                  VARCHAR(255) PRIMARY KEY,
    user_id               VARCHAR(255) NOT NULL,
    client_id             VARCHAR(255) NOT NULL,
    code_challenge        VARCHAR(255) NOT NULL,
    redirect_uri          VARCHAR(2048) NOT NULL DEFAULT '',
    granted_scopes        TEXT NOT NULL,
    installation_name     VARCHAR(255) NOT NULL DEFAULT '',
    created_at            DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    expires_at            DATETIME(3) NOT NULL,
    consumed_at           DATETIME(3),
    minted_token_id       VARCHAR(255),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (client_id) REFERENCES oauth_clients(client_id) ON DELETE RESTRICT
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_oauth_authorization_codes_expires_at ON oauth_authorization_codes(expires_at);

-- OAuth identity providers (admin-configured)
CREATE TABLE oauth_providers (
    id              VARCHAR(255) PRIMARY KEY,
    provider_type   VARCHAR(255) NOT NULL,
    name            VARCHAR(255) NOT NULL,
    issuer_url      TEXT NOT NULL,
    client_id       VARCHAR(255) NOT NULL,
    client_secret   BLOB NOT NULL,
    scopes          TEXT NOT NULL,
    trust_email     BOOLEAN NOT NULL DEFAULT TRUE,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) COLLATE=utf8mb4_bin;

-- Links between local users and OAuth provider identities
CREATE TABLE oauth_user_links (
    user_id          VARCHAR(255) NOT NULL,
    provider_id      VARCHAR(255) NOT NULL,
    provider_subject VARCHAR(255) NOT NULL,
    created_at       DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (user_id, provider_id),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (provider_id) REFERENCES oauth_providers(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE UNIQUE INDEX idx_oauth_user_links_provider_subject ON oauth_user_links(provider_id, provider_subject);

-- Encrypted OAuth tokens per user per provider
CREATE TABLE oauth_tokens (
    user_id         VARCHAR(255) NOT NULL,
    provider_id     VARCHAR(255) NOT NULL,
    access_token    BLOB NOT NULL,
    refresh_token   BLOB NOT NULL,
    token_type      VARCHAR(255) NOT NULL DEFAULT 'Bearer',
    expires_at      DATETIME(3) NOT NULL,
    key_version     BIGINT NOT NULL DEFAULT 1,
    updated_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (user_id, provider_id),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (provider_id) REFERENCES oauth_providers(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE INDEX idx_oauth_tokens_provider_id ON oauth_tokens(provider_id);
CREATE INDEX idx_oauth_tokens_expires_at ON oauth_tokens(expires_at);
CREATE INDEX idx_oauth_tokens_key_version ON oauth_tokens(key_version);

-- Short-lived OAuth state for CSRF + PKCE during auth flow
CREATE TABLE oauth_states (
    state           VARCHAR(255) PRIMARY KEY,
    provider_id     VARCHAR(255) NOT NULL,
    pkce_verifier   TEXT NOT NULL,
    nonce_hash      VARCHAR(255) NOT NULL DEFAULT '',
    redirect_uri    TEXT NOT NULL,
    -- 'login' starts a sign-in; 'reauth' proves the identity again for an
    -- ALREADY signed-in session, to elevate it. The callback branches on
    -- this: a reauth state must never create a session or link an identity.
    -- The CHECK is the enforcement, not the DEFAULT. Go's zero value for the
    -- column is "", never 'login', so an explicit insert never reaches the
    -- DEFAULT, and the callback treats every value that is not 'reauth' as a
    -- login -- which may create a session or link an identity.
    purpose         VARCHAR(16) NOT NULL DEFAULT 'login' CHECK (purpose IN ('login', 'reauth')),
    -- The session the reauth leg elevates on success. Empty for 'login'.
    session_id      VARCHAR(255) NOT NULL DEFAULT '',
    expires_at      DATETIME(3) NOT NULL,
    created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    FOREIGN KEY (provider_id) REFERENCES oauth_providers(id)
) COLLATE=utf8mb4_bin;

-- Pending OAuth signups (new users choosing their username)
CREATE TABLE pending_oauth_signups (
    token            VARCHAR(255) PRIMARY KEY,
    provider_id      VARCHAR(255) NOT NULL,
    provider_subject VARCHAR(255) NOT NULL,
    nonce_hash       VARCHAR(255) NOT NULL DEFAULT '',
    email            TEXT NOT NULL,
    display_name     TEXT NOT NULL,
    access_token     BLOB NOT NULL,
    refresh_token    BLOB NOT NULL,
    token_type       VARCHAR(255) NOT NULL DEFAULT 'Bearer',
    token_expires_at DATETIME(3) NOT NULL,
    key_version      BIGINT NOT NULL DEFAULT 1,
    redirect_uri     TEXT NOT NULL,
    expires_at       DATETIME(3) NOT NULL,
    created_at       DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    FOREIGN KEY (provider_id) REFERENCES oauth_providers(id)
) COLLATE=utf8mb4_bin;

-- Passkey (WebAuthn) credentials for email-local accounts
CREATE TABLE passkey_credentials (
    id              VARCHAR(255) PRIMARY KEY,
    user_id         VARCHAR(255) NOT NULL,
    credential_id   VARBINARY(1023) NOT NULL,   -- plaintext: login lookup + unique index (WebAuthn max 1023 bytes; not BLOB -- MySQL/TiDB reject unique indexes on TEXT/BLOB)
    public_key      BLOB NOT NULL,   -- keystore-encrypted COSE public key, AAD: 'passkey_public_key:' || id
    sign_count      BIGINT NOT NULL DEFAULT 0,
    aaguid          BLOB,
    backup_eligible BOOLEAN NOT NULL DEFAULT FALSE,
    backup_state    BOOLEAN NOT NULL DEFAULT FALSE,
    transports      TEXT NOT NULL,
    friendly_name   TEXT NOT NULL,
    key_version     BIGINT NOT NULL DEFAULT 1,  -- active keystore version at write; for reencrypt scans
    created_at      DATETIME(3) NOT NULL,
    last_used_at    DATETIME(3),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) COLLATE=utf8mb4_bin;
CREATE UNIQUE INDEX idx_passkey_credentials_credential_id ON passkey_credentials(credential_id);
CREATE INDEX idx_passkey_credentials_user_id ON passkey_credentials(user_id);
CREATE INDEX idx_passkey_credentials_key_version ON passkey_credentials(key_version);

-- Ephemeral WebAuthn ceremony state (signup, login, register, elevation)
CREATE TABLE webauthn_sessions (
    id           VARCHAR(255) PRIMARY KEY,
    kind         VARCHAR(32) NOT NULL,
    user_id      VARCHAR(255),
    payload_json TEXT NOT NULL,              -- '{}' or keystore-encrypted signup draft (base64), AAD: 'webauthn_payload:' || id
    session_data BLOB NOT NULL,              -- keystore-encrypted ceremony state, AAD: 'webauthn_session:' || id
    expires_at   DATETIME(3) NOT NULL,
    created_at   DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    CHECK (kind IN ('signup', 'login', 'register', 'elevation'))
) COLLATE=utf8mb4_bin;
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
    `key`      VARCHAR(255) PRIMARY KEY,
    value      TEXT,    -- JSON document, public half
    secret     BLOB,    -- keystore-encrypted JSON, secret half
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    CHECK (value IS NOT NULL OR secret IS NOT NULL)
) COLLATE=utf8mb4_bin;

-- Consumed ALTCHA salts: single-use enforcement for solved challenges,
-- shared across hub instances and restarts. A row's presence means the
-- salt's solution was accepted once; the cleanup loop purges rows past
-- their challenge expiry. External providers (reCAPTCHA, Turnstile)
-- enforce single use at their siteverify endpoint and need no table.
CREATE TABLE altcha_used_salts (
    salt       VARCHAR(255) PRIMARY KEY,
    expires_at DATETIME(3) NOT NULL
) COLLATE=utf8mb4_bin;
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
