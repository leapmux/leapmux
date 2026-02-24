-- name: GetSystemSettings :one
SELECT * FROM system_settings WHERE id = 1;

-- name: UpdateSystemSettings :exec
UPDATE system_settings SET
  signup_enabled = ?,
  email_verification_required = ?,
  smtp_host = ?,
  smtp_port = ?,
  smtp_username = ?,
  smtp_password = ?,
  smtp_from_address = ?,
  smtp_use_tls = ?,
  api_timeout_seconds = ?,
  agent_startup_timeout_seconds = ?,
  worktree_create_timeout_seconds = ?,
  worktree_delete_timeout_seconds = ?,
  updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = 1;
