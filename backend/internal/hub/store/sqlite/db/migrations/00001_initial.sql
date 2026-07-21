-- +goose Up

-- Personal organizations: exactly one per user, created with the account,
-- soft-deleted with it. name mirrors the username (renamed together).
CREATE TABLE orgs (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    deleted_at  DATETIME
);
CREATE UNIQUE INDEX idx_orgs_name ON orgs(name) WHERE deleted_at IS NULL;
CREATE INDEX idx_orgs_deleted_at ON orgs(deleted_at) WHERE deleted_at IS NOT NULL;

-- Users
CREATE TABLE users (
    id             TEXT PRIMARY KEY,
    org_id         TEXT NOT NULL REFERENCES orgs(id),
    username       TEXT NOT NULL,
    password_hash  TEXT NOT NULL,
    display_name   TEXT NOT NULL DEFAULT '',
    -- Unicode-casefolded (Go strings.ToLower) copy of display_name, maintained on
    -- every write, so admin SearchUsers matches non-ASCII names case-insensitively
    -- and identically across SQLite/Postgres/MySQL. SQLite's built-in LOWER/LIKE
    -- fold only ASCII, so a bare LIKE on display_name would diverge from Postgres
    -- ILIKE / MySQL LOWER; querying this pre-folded column with a plain LIKE does not.
    display_name_folded      TEXT NOT NULL DEFAULT '',
    email                    TEXT NOT NULL DEFAULT '',
    email_verified           INTEGER NOT NULL DEFAULT 0,
    pending_email            TEXT NOT NULL DEFAULT '',
    -- Stored verification code in raw 6-char form (no hyphen), drawn
    -- from verifycode.Charset. Empty when no verification is pending.
    -- (SQLite does not enforce VARCHAR width but we declare it for
    -- schema-doc symmetry with postgres/mysql.)
    pending_email_token      VARCHAR(16) NOT NULL DEFAULT '',
    pending_email_expires_at DATETIME,
    -- Counts attempts against the active pending_email_token. Reset to 0
    -- whenever a new token is issued; force-expires the token at >5.
    pending_email_attempts   INTEGER NOT NULL DEFAULT 0,
    password_set             INTEGER NOT NULL DEFAULT 1,
    is_admin                 INTEGER NOT NULL DEFAULT 0,
    prefs          TEXT NOT NULL DEFAULT '{}',
    created_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    -- High-water mark bumped whenever this user's auth basis is
    -- bulk-revoked (admin user delete / reset-password / session
    -- revoke-user, ChangePassword). Each bump also records a durable
    -- user-token revocation event that closes cookie channels and
    -- evicts bearer caches across hub processes.
    tokens_revoked_at        DATETIME,
    -- Monotonic credential epoch. Sessions and bearer rows copy this
    -- value when issued; user-wide revocation increments it so stale
    -- credentials fail without depending on timestamp precision or
    -- cross-host clock agreement.
    auth_generation          BIGINT NOT NULL DEFAULT 0,
    deleted_at     DATETIME
);
CREATE INDEX idx_users_org_id ON users(org_id);
CREATE UNIQUE INDEX idx_users_username ON users(username) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX idx_users_email ON users(email) WHERE email != '' AND deleted_at IS NULL;
CREATE INDEX idx_users_deleted_at ON users(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX idx_users_created_at ON users(created_at DESC, id DESC) WHERE deleted_at IS NULL;
-- Verification codes are looked up per-user (the session identifies who),
-- so no global token index is needed. Index expiry instead, for cleanup.
CREATE INDEX idx_users_pending_email_expires_at ON users(pending_email_expires_at) WHERE pending_email_expires_at IS NOT NULL;
-- GetFirstAdmin scans for the earliest non-deleted admin (bootstrap path).
-- Partial on (is_admin, deleted_at) keeps the index tiny; indexing on
-- created_at lets the ORDER BY + LIMIT 1 hit the first leaf directly.
-- The predicate MUST be spelled `is_admin = 1` -- SQLite's partial-index
-- matcher is syntactic, so a bare `is_admin` predicate never matches
-- GetFirstAdmin's `is_admin = 1` term and the index goes unused.
CREATE INDEX idx_users_is_admin ON users(created_at) WHERE is_admin = 1 AND deleted_at IS NULL;

-- Auth sessions
CREATE TABLE user_sessions (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at      DATETIME NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_active_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    auth_generation BIGINT NOT NULL DEFAULT 0,
    user_agent      TEXT NOT NULL DEFAULT '',
    ip_address      TEXT NOT NULL DEFAULT ''
);
-- Serves the plain user_id lookups (prefix) AND the per-user keyset listing
-- ListUserSessionsByUserID (user_id =, ORDER BY last_active_at DESC, id DESC),
-- so that query both seeks and rides the index instead of TEMP-sorting.
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
    id            TEXT PRIMARY KEY,
    auth_token    TEXT NOT NULL UNIQUE,
    registered_by TEXT NOT NULL REFERENCES users(id),
    status        INTEGER NOT NULL DEFAULT 1,
    created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_seen_at  DATETIME,
    public_key    BLOB NOT NULL DEFAULT '',
    mlkem_public_key  BLOB NOT NULL DEFAULT '',
    slhdsa_public_key BLOB NOT NULL DEFAULT '',
    -- True for rows created by Server.RegisterWorker, the in-process
    -- bypass the solo launcher uses to bring up the co-located local
    -- worker. The deregister handler refuses these so the user can't
    -- accidentally tear down the bundled desktop worker -- it would just
    -- re-register on next launch and the running process would noisily
    -- exit with "invalid auth token" in between.
    auto_registered INTEGER NOT NULL DEFAULT 0,
    deleted_at    DATETIME
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
-- Admin per-user listing (ListWorkersAdminByUser: registered_by=?, deleted_at IS
-- NULL, no status filter). The (registered_by, status, created_at, id) composite
-- above can't serve this query's ORDER BY because status sits between the
-- registered_by equality and the created_at sort key -- within a registered_by
-- prefix rows are grouped by status, not ordered by created_at, so every page
-- would top-N sort. This partial index closes that gap and completes the
-- 'composite ORDER BY rides an index' invariant for all five worker keyset
-- families. (ListWorkersByUserID is still served by the composite above: it
-- filters registered_by + status=1, so the composite's prefix matches exactly.)
CREATE INDEX idx_workers_registered_by_created ON workers(registered_by, created_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_workers_deleted_at ON workers(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX idx_workers_created_at ON workers(created_at DESC, id DESC) WHERE deleted_at IS NULL;

-- Worker notifications (persistent queue for reliable delivery)
CREATE TABLE worker_notifications (
    id           TEXT PRIMARY KEY,
    worker_id    TEXT NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    type         INTEGER NOT NULL,
    payload      TEXT NOT NULL DEFAULT '{}',
    status       INTEGER NOT NULL DEFAULT 1,
    attempts     INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 5,
    created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    delivered_at DATETIME
);
CREATE INDEX idx_worker_notifications_worker_status ON worker_notifications(worker_id, status);

-- Active worker registration keys.
--
-- Created by an authenticated user via the frontend. The worker presents
-- the row's `id` as a bearer credential (Authorization: Bearer <key>) on
-- the WorkerConnectorService.Register RPC; the hub atomically consumes
-- the row and creates a workers row in one transaction.
--
-- Soft-delete is implemented by setting expires_at to a past instant.
-- The cleanup loop hard-deletes rows whose expires_at is older than the
-- retention cutoff.
CREATE TABLE worker_registration_keys (
    id          TEXT PRIMARY KEY,
    created_by  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    expires_at  DATETIME NOT NULL
);
CREATE INDEX idx_worker_registration_keys_expires_at ON worker_registration_keys(expires_at);
CREATE INDEX idx_worker_registration_keys_created_by ON worker_registration_keys(created_by);
CREATE INDEX idx_worker_registration_keys_created_at ON worker_registration_keys(created_at DESC, id DESC);


-- Sidebar sections (per-user organization of sidebar panels)
CREATE TABLE workspace_sections (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    position     TEXT NOT NULL,
    section_type INTEGER NOT NULL DEFAULT 1,
    sidebar      INTEGER NOT NULL DEFAULT 1,
    created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX idx_workspace_sections_user_id ON workspace_sections(user_id);

-- Workspaces (hub-owned registry) -- must come before workspace_section_items
CREATE TABLE workspaces (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL REFERENCES orgs(id),
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    title         TEXT NOT NULL DEFAULT '',
    is_deleted    INTEGER NOT NULL DEFAULT 0,
    created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    deleted_at    DATETIME
);
CREATE INDEX idx_workspaces_org_owner ON workspaces(org_id, owner_user_id) WHERE is_deleted = 0;
CREATE INDEX idx_workspaces_owner_user_id ON workspaces(owner_user_id);
CREATE INDEX idx_workspaces_deleted_at ON workspaces(deleted_at) WHERE deleted_at IS NOT NULL;

-- Workspace-to-section assignments (per-user)
CREATE TABLE workspace_section_items (
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    section_id   TEXT NOT NULL REFERENCES workspace_sections(id) ON DELETE CASCADE,
    position     TEXT NOT NULL,
    PRIMARY KEY (user_id, workspace_id)
);
CREATE INDEX idx_workspace_section_items_section ON workspace_section_items(section_id);

-- CRDT op-batch journal. The per-org CRDT manager appends every committed
-- batch here in the same transaction that updates the in-memory state and
-- the derived workspace_tab_owned / workspace_tab_rendered views. One row
-- per OpBatch (not per OrgOp); ops within a batch share a contiguous
-- canonical HLC range anchored at (physical_ms, logical) with op_count
-- ops, so the last op's logical = logical + op_count - 1.
--
-- Compaction periodically drops rows whose batch's last canonical HLC <=
-- compaction_watermark; the surviving batch_ids move to
-- org_recent_batch_ids so retries within ~14 days remain idempotent.
CREATE TABLE org_op_batches (
    org_id        TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    physical_ms   BIGINT NOT NULL,                  -- first op's canonical_hlc.physical (= last op's; ops in a batch share physical_ms)
    logical       BIGINT NOT NULL,                  -- first op's canonical_hlc.logical
    last_logical  BIGINT NOT NULL,                  -- last op's canonical_hlc.logical (= logical + op_count - 1); precomputed for compaction filter
    origin_client TEXT NOT NULL,                    -- canonical_hlc.client_id (hub-stamped; identical for every op in the batch)
    principal_id  TEXT NOT NULL,                    -- authenticated user; for principal-aware dedup
    batch_id      TEXT NOT NULL,                    -- client-minted; dedup key
    body_hash     BLOB NOT NULL,                    -- SHA-256 of OpBatch with per-op HLC/origin fields stripped
    batch_payload BLOB NOT NULL,                    -- proto-marshalled OpBatch (ops carry per-op canonical_hlc)
    op_count      INTEGER NOT NULL CHECK (op_count > 0),
    epoch         BIGINT NOT NULL,                  -- the org's epoch at commit time
    committed_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (org_id, physical_ms, logical, origin_client)
);
CREATE UNIQUE INDEX idx_org_op_batches_dedup ON org_op_batches(org_id, batch_id);

-- One materialized OrgCrdtState blob per org. The manager rewrites this
-- row only on compaction or lifecycle-outbox processing; per-batch
-- commits update only org_op_batches + the index views to keep the hot
-- path off the multi-MB blob.
CREATE TABLE org_state (
    org_id           TEXT PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE,
    state_payload    BLOB NOT NULL,
    current_epoch    BIGINT NOT NULL DEFAULT 1,
    epoch_started_at DATETIME NOT NULL,
    updated_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Worker-ownership view: every non-tombstoned tab in the org. Worker
-- reconciliation reads from this view (NOT from _rendered) so a tab
-- dropped by projection-repair doesn't cause the worker to delete a
-- still-live agent / terminal / file-tab.
CREATE TABLE workspace_tab_owned (
    org_id       TEXT NOT NULL,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    tab_type     INTEGER NOT NULL,
    tab_id       TEXT NOT NULL,
    worker_id    TEXT NOT NULL,
    tile_id      TEXT NOT NULL,
    position     TEXT NOT NULL,
    PRIMARY KEY (org_id, tab_id)
);
CREATE INDEX idx_workspace_tab_owned_worker    ON workspace_tab_owned(worker_id);
CREATE INDEX idx_workspace_tab_owned_workspace ON workspace_tab_owned(workspace_id);

-- UI / projection view: subset of workspace_tab_owned whose tab passes
-- projection (live tile, no duplicate-grid-cell tie-break, no
-- cycle-break drop). ListTabs / GetTab read from this view.
CREATE TABLE workspace_tab_rendered (
    org_id       TEXT NOT NULL,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    tab_type     INTEGER NOT NULL,
    tab_id       TEXT NOT NULL,
    worker_id    TEXT NOT NULL,
    tile_id      TEXT NOT NULL,
    position     TEXT NOT NULL,
    PRIMARY KEY (org_id, tab_id)
);
CREATE INDEX idx_workspace_tab_rendered_workspace ON workspace_tab_rendered(workspace_id);
-- LocateAccessibleRenderedTab filters on tab_id alone; the PK has tab_id
-- as the trailing column so it is not seekable.
CREATE INDEX idx_workspace_tab_rendered_tab_id ON workspace_tab_rendered(tab_id);

-- Recently-committed batch_ids retained for ~14 days (one full epoch
-- window) so retries are idempotent without scanning the journal. The
-- signed-epoch check covers retries older than this window. The canonical
-- HLC tuple is the batch's first op; combined with op_count it lets the
-- manager reconstruct every CommittedOp.canonical_hlc for retries
-- byte-for-byte.
CREATE TABLE org_recent_batch_ids (
    org_id                TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    batch_id              TEXT NOT NULL,
    body_hash             BLOB NOT NULL,
    principal_id          TEXT NOT NULL,
    canonical_physical_ms BIGINT NOT NULL,
    canonical_logical     BIGINT NOT NULL,
    canonical_client      TEXT NOT NULL,
    op_count              INTEGER NOT NULL CHECK (op_count > 0),
    epoch                 BIGINT NOT NULL,
    expires_at            DATETIME NOT NULL,
    PRIMARY KEY (org_id, batch_id)
);
CREATE INDEX idx_org_recent_batch_ids_expires ON org_recent_batch_ids(expires_at);

-- Transactional outbox for workspace lifecycle events. Lifecycle RPCs
-- (CreateWorkspace / RenameWorkspace / DeleteWorkspace) write to
-- `workspaces` and to this table inside the same DB transaction; the
-- per-org manager goroutine drains the outbox post-commit, applies any
-- carried CRDT ops, mutates manager-internal state slots, broadcasts the
-- lifecycle event, and stamps consumed_at. All inside a single
-- transaction so a mid-process crash is replayable.
CREATE TABLE lifecycle_outbox (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id      TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    op_type     TEXT NOT NULL,                 -- "create" | "rename" | "delete"
    payload     BLOB NOT NULL,                 -- proto-marshalled lifecycle event + ops
    enqueued_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    consumed_at DATETIME
);
CREATE INDEX idx_lifecycle_outbox_pending ON lifecycle_outbox(org_id, id) WHERE consumed_at IS NULL;

-- Durable revocation stream. Token/user mutations write pending events
-- in the same transaction as the state transition. Watchers publish those
-- facts into a gapless seq stream before consuming them, so cursors are
-- based on durable sequence numbers rather than wall-clock timestamps.
CREATE TABLE revocation_events (
    id         TEXT PRIMARY KEY,
    kind       TEXT NOT NULL CHECK (kind IN ('session', 'api_token', 'api_token_rotation', 'delegation_token', 'user_tokens', 'user_info')),
    subject_id TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    revoked_at DATETIME NOT NULL,
    user_auth_generation BIGINT NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    seq BIGINT UNIQUE CHECK (seq IS NULL OR seq > 0),
    published_at DATETIME,
    CHECK ((seq IS NULL) = (published_at IS NULL))
);
CREATE INDEX idx_revocation_events_pending ON revocation_events(created_at, id) WHERE seq IS NULL;
CREATE INDEX idx_revocation_events_published ON revocation_events(published_at, seq) WHERE seq IS NOT NULL;

CREATE TABLE revocation_event_sequence (
    id       INTEGER PRIMARY KEY CHECK (id = 1),
    last_seq BIGINT NOT NULL CHECK (last_seq >= 0)
);
INSERT INTO revocation_event_sequence (id, last_seq) VALUES (1, 0);

CREATE TABLE hub_runtime_lease (
    singleton_id     INTEGER PRIMARY KEY CHECK (singleton_id = 1),
    holder_id        TEXT NOT NULL CHECK (holder_id <> ''),
    cursor_seq       BIGINT NOT NULL CHECK (cursor_seq >= 0),
    lease_expires_at DATETIME NOT NULL
);

-- Durable, low-churn API tokens used by the leapmux remote CLI (and any
-- future mobile / IDE / integration). Each row's id appears verbatim in
-- the bearer string ("lmx_<id>_<secret>") so verification is a single
-- primary-key lookup. The secret is HMAC-SHA256(secret, server_pepper)
-- so a leaked snapshot still requires the pepper to verify.
--
-- previous_refresh_hash + previous_refresh_expires_at implement
-- refresh-token rotation with reuse detection: if a presented refresh
-- matches previous_refresh_hash within the grace window, we treat it as
-- a benign client retry and return the same new pair; if it matches
-- after the window, we treat it as compromise and revoke the row.
CREATE TABLE api_tokens (
    id                            TEXT PRIMARY KEY,
    user_id                       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_type                   TEXT NOT NULL,                 -- 'cli', future: 'mobile', 'desktop', 'integration'
    client_name                   TEXT NOT NULL,                 -- user-visible (hostname, etc.)
    secret_hash                   BLOB NOT NULL,
    refresh_hash                  BLOB,
    previous_refresh_hash         BLOB,
    previous_refresh_expires_at   DATETIME,
    scope                         TEXT NOT NULL DEFAULT 'remote:*',
    created_at                    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    auth_generation               BIGINT NOT NULL DEFAULT 0,
    last_used_at                  DATETIME,
    last_rotated_at               DATETIME,
    expires_at                    DATETIME,
    refresh_expires_at            DATETIME,
    revoked_at                    DATETIME
);
-- user_id only: the client_type filters are the optional OR-form and never
-- seek; the remaining job is the user_id seek for the ByUserIncludingRevoked
-- listing, which the partial idx_api_tokens_user_created cannot serve.
CREATE INDEX idx_api_tokens_user ON api_tokens(user_id);
CREATE INDEX idx_api_tokens_revoked_at ON api_tokens(revoked_at) WHERE revoked_at IS NOT NULL;
-- Keyset index for the admin ListAllAPITokens listing: partial on
-- revoked_at IS NULL to match the query's live-token filter, with the trailing
-- id DESC so the composite ORDER BY rides the index instead of top-N sorting.
CREATE INDEX idx_api_tokens_created_at ON api_tokens(created_at DESC, id DESC) WHERE revoked_at IS NULL;
-- Keyset index for the admin ListAllAPITokensByUser listing (the --user-id
-- path): the leading user_id equality seeks, and (created_at DESC, id DESC)
-- rides the composite ORDER BY -- without it the per-user page pays a seek on
-- idx_api_tokens_user plus a TEMP B-TREE sort. Mirrors
-- idx_workers_registered_by_created.
CREATE INDEX idx_api_tokens_user_created ON api_tokens(user_id, created_at DESC, id DESC) WHERE revoked_at IS NULL;

-- Ephemeral, high-churn delegation tokens minted by workers when a
-- spawned agent (or opt-in terminal) calls into the hub or a sibling
-- worker on behalf of the spawning user. Scope is (user_id,
-- workspace_id); issued_for_tab_id is provenance only.
--
-- A nightly cleanup hard-deletes revoked rows older than 7d.
CREATE TABLE delegation_tokens (
    id                            TEXT PRIMARY KEY,
    user_id                       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    worker_id                     TEXT NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    workspace_id                  TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    agent_id                      TEXT NOT NULL DEFAULT '',
    terminal_id                   TEXT NOT NULL DEFAULT '',
    issued_for_tab_id             TEXT NOT NULL DEFAULT '',
    issued_for_tab_type           INTEGER NOT NULL DEFAULT 0,
    secret_hash                   BLOB NOT NULL,
    refresh_hash                  BLOB,
    created_at                    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    auth_generation               BIGINT NOT NULL DEFAULT 0,
    last_used_at                  DATETIME,
    expires_at                    DATETIME NOT NULL,
    refresh_expires_at            DATETIME,
    revoked_at                    DATETIME
);
CREATE INDEX idx_delegation_tokens_user ON delegation_tokens(user_id);
CREATE INDEX idx_delegation_tokens_worker_agent ON delegation_tokens(worker_id, agent_id);
CREATE INDEX idx_delegation_tokens_workspace ON delegation_tokens(workspace_id);
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

-- RFC 8628 device authorizations. The CLI starts the flow on a headless
-- machine, the user activates the user_code from any browser, then the
-- CLI polls for the result. Rows live for `expires_at`; the cleanup
-- loop hard-deletes expired rows daily.
CREATE TABLE device_authorizations (
    device_code           TEXT PRIMARY KEY,
    user_code             TEXT NOT NULL UNIQUE,
    device_name           TEXT NOT NULL DEFAULT '',
    user_id               TEXT REFERENCES users(id) ON DELETE CASCADE,
    approved              INTEGER NOT NULL DEFAULT 0,        -- 0 pending, 1 approved, 2 denied
    last_polled_at        DATETIME,
    interval_seconds      INTEGER NOT NULL DEFAULT 5,
    created_at            DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    expires_at            DATETIME NOT NULL,
    consumed_at           DATETIME
);
CREATE INDEX idx_device_authorizations_expires_at ON device_authorizations(expires_at);

-- One-shot authorization codes for the local-redirect CLI flow. Each row
-- is consumed exactly once during /auth/cli/token exchange.
CREATE TABLE cli_authorization_codes (
    code                  TEXT PRIMARY KEY,
    user_id               TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_challenge        TEXT NOT NULL,
    device_name           TEXT NOT NULL DEFAULT '',
    created_at            DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    expires_at            DATETIME NOT NULL,
    consumed_at           DATETIME
);
CREATE INDEX idx_cli_authorization_codes_expires_at ON cli_authorization_codes(expires_at);

-- OAuth identity providers (admin-configured)
CREATE TABLE oauth_providers (
    id              TEXT PRIMARY KEY,
    provider_type   TEXT NOT NULL,  -- 'oidc' or 'github'
    name            TEXT NOT NULL,  -- display name
    issuer_url      TEXT NOT NULL DEFAULT '',  -- OIDC issuer (empty for GitHub)
    client_id       TEXT NOT NULL,
    client_secret   BLOB NOT NULL,  -- encrypted with encryption key, AAD: 'oauth_provider:' || id
    scopes          TEXT NOT NULL DEFAULT 'openid profile email',
    trust_email     INTEGER NOT NULL DEFAULT 1,  -- trust provider-reported email as verified
    enabled         INTEGER NOT NULL DEFAULT 1,
    created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- Links between local users and OAuth provider identities
CREATE TABLE oauth_user_links (
    user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider_id      TEXT NOT NULL REFERENCES oauth_providers(id) ON DELETE CASCADE,
    provider_subject TEXT NOT NULL,  -- sub claim (OIDC) or user ID (GitHub)
    created_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (user_id, provider_id)
);
CREATE UNIQUE INDEX idx_oauth_user_links_provider_subject ON oauth_user_links(provider_id, provider_subject);

-- Encrypted OAuth tokens per user per provider
CREATE TABLE oauth_tokens (
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider_id     TEXT NOT NULL REFERENCES oauth_providers(id) ON DELETE CASCADE,
    access_token    BLOB NOT NULL,   -- encrypted, AAD: 'access_token:' || user_id || ':' || provider_id
    refresh_token   BLOB NOT NULL,   -- encrypted, AAD: 'refresh_token:' || user_id || ':' || provider_id
    token_type      TEXT NOT NULL DEFAULT 'Bearer',
    expires_at      DATETIME NOT NULL,
    key_version     INTEGER NOT NULL DEFAULT 1,
    updated_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (user_id, provider_id)
);
CREATE INDEX idx_oauth_tokens_provider_id ON oauth_tokens(provider_id);
CREATE INDEX idx_oauth_tokens_expires_at ON oauth_tokens(expires_at);
CREATE INDEX idx_oauth_tokens_key_version ON oauth_tokens(key_version);

-- Short-lived OAuth state for CSRF + PKCE during auth flow
CREATE TABLE oauth_states (
    state           TEXT PRIMARY KEY,
    provider_id     TEXT NOT NULL REFERENCES oauth_providers(id),
    pkce_verifier   TEXT NOT NULL,
    redirect_uri    TEXT NOT NULL DEFAULT '',
    expires_at      DATETIME NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- Pending OAuth signups (new users choosing their username)
CREATE TABLE pending_oauth_signups (
    token            TEXT PRIMARY KEY,
    provider_id      TEXT NOT NULL REFERENCES oauth_providers(id),
    provider_subject TEXT NOT NULL,
    email            TEXT NOT NULL DEFAULT '',
    display_name     TEXT NOT NULL DEFAULT '',
    access_token     BLOB NOT NULL,
    refresh_token    BLOB NOT NULL,
    token_type       TEXT NOT NULL DEFAULT 'Bearer',
    token_expires_at DATETIME NOT NULL,
    key_version      INTEGER NOT NULL DEFAULT 1,
    redirect_uri     TEXT NOT NULL DEFAULT '',
    expires_at       DATETIME NOT NULL,
    created_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- +goose Down
DROP TABLE IF EXISTS cli_authorization_codes;
DROP TABLE IF EXISTS device_authorizations;
DROP TABLE IF EXISTS delegation_tokens;
DROP TABLE IF EXISTS api_tokens;
DROP TABLE IF EXISTS hub_runtime_lease;
DROP TABLE IF EXISTS revocation_events;
DROP TABLE IF EXISTS revocation_event_sequence;
DROP TABLE IF EXISTS pending_oauth_signups;
DROP TABLE IF EXISTS oauth_states;
DROP TABLE IF EXISTS oauth_tokens;
DROP TABLE IF EXISTS oauth_user_links;
DROP TABLE IF EXISTS oauth_providers;
DROP TABLE IF EXISTS lifecycle_outbox;
DROP TABLE IF EXISTS org_recent_batch_ids;
DROP TABLE IF EXISTS workspace_tab_rendered;
DROP TABLE IF EXISTS workspace_tab_owned;
DROP TABLE IF EXISTS org_state;
DROP TABLE IF EXISTS org_op_batches;
DROP TABLE IF EXISTS workspace_section_items;
DROP TABLE IF EXISTS workspace_sections;
DROP TABLE IF EXISTS workspaces;
DROP TABLE IF EXISTS worker_registration_keys;
DROP TABLE IF EXISTS worker_notifications;
DROP TABLE IF EXISTS workers;
DROP TABLE IF EXISTS user_sessions;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS orgs;
