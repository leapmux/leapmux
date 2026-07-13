-- name: CreateOrg :exec
INSERT INTO orgs (id, name) VALUES ($1, $2);

-- name: GetOrgByID :one
SELECT * FROM orgs WHERE id = $1 AND deleted_at IS NULL;

-- name: GetOrgByIDIncludeDeleted :one
SELECT * FROM orgs WHERE id = $1;


-- name: SoftDeleteOrg :exec
UPDATE orgs SET deleted_at = NOW() WHERE id = $1;

-- name: HardDeleteOrgsBefore :execresult
-- NOTE: Use CTE form (not LIMIT in subquery) for CockroachDB compatibility.
-- An org is hard-deletable only once no user references it. users.org_id has no
-- ON DELETE clause, so an org still referenced by a (possibly soft-deleted,
-- not-yet-hard-deleted) user would otherwise abort this DELETE on a foreign-key
-- violation: deleting a user now soft-deletes its personal org in the same
-- transaction, so both become eligible for hard-delete together, and the chunked
-- HardDeleteUsersBefore (LIMIT 1000) can leave straggler soft-deleted users whose
-- personal orgs land in this batch. Gating here keeps the users->orgs cleanup
-- order correct under bulk deletes without requiring the users step to drain
-- fully in one run; the org is reaped on a later pass once its user is gone.
WITH to_delete AS (
    SELECT o.id FROM orgs o
    WHERE o.deleted_at IS NOT NULL AND o.deleted_at < $1
      AND NOT EXISTS (SELECT 1 FROM users u WHERE u.org_id = o.id)
    LIMIT 1000
)
DELETE FROM orgs WHERE id IN (SELECT id FROM to_delete);
