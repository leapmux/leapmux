-- name: CreateOrg :exec
INSERT INTO orgs (id, name) VALUES (?, ?);

-- name: GetOrgByID :one
SELECT * FROM orgs WHERE id = ? AND deleted_at IS NULL;

-- name: GetOrgByIDIncludeDeleted :one
SELECT * FROM orgs WHERE id = ?;


-- name: SoftDeleteOrg :exec
UPDATE orgs SET deleted_at = NOW(3) WHERE id = ?;

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
-- The derived table works around MySQL's restriction on referencing the deleted
-- table in a subquery of a DELETE.
DELETE FROM orgs WHERE id IN (
    SELECT o.id FROM (
        SELECT orgs.id FROM orgs
        WHERE orgs.deleted_at IS NOT NULL AND orgs.deleted_at < ?
          AND NOT EXISTS (SELECT 1 FROM users u WHERE u.org_id = orgs.id)
        LIMIT 1000
    ) o
);
