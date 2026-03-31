-- name: GetUserPreferences :one
SELECT * FROM user_preferences WHERE user_id = ?;

-- name: UpsertUserPreferences :exec
INSERT INTO user_preferences (user_id, prefs, updated_at)
VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
ON CONFLICT (user_id) DO UPDATE SET
  prefs = excluded.prefs,
  updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now');
