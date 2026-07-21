-- name: CreateOrg :exec
INSERT INTO orgs (id, name) VALUES (?, ?);

-- name: GetOrgByID :one
SELECT * FROM orgs WHERE id = ? AND deleted_at IS NULL;

-- name: GetOrgByIDIncludeDeleted :one
SELECT * FROM orgs WHERE id = ?;


-- name: SoftDeleteOrg :exec
UPDATE orgs SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?;

-- name: HardDeleteOrgsBefore :execresult
-- An org is hard-deletable only once no user references it. users.org_id has no
-- ON DELETE clause, so an org still referenced by a (possibly soft-deleted,
-- not-yet-hard-deleted) user would otherwise abort this DELETE on a foreign-key
-- violation: deleting a user now soft-deletes its personal org in the same
-- transaction, so both become eligible for hard-delete together, and the chunked
-- HardDeleteUsersBefore (LIMIT 1000) can leave straggler soft-deleted users whose
-- personal orgs land in this batch. Gating here keeps the users->orgs cleanup
-- order correct under bulk deletes without requiring the users step to drain
-- fully in one run; the org is reaped on a later pass once its user is gone.
-- idx_users_org_id makes the NOT EXISTS lookup an indexed point probe.
DELETE FROM orgs WHERE rowid IN (
    SELECT o.rowid FROM orgs o
    -- Raw compare: deleted_at (canonical on every write) against the SQLiteTime
    -- cutoff (same canonical layout; see HardDeleteWorkersBefore).
    WHERE o.deleted_at IS NOT NULL AND o.deleted_at < sqlc.arg(cutoff)
      AND NOT EXISTS (SELECT 1 FROM users u WHERE u.org_id = o.id)
    LIMIT 1000
);
