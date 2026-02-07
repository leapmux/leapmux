-- name: GetUserPreferences :one
SELECT * FROM user_preferences WHERE user_id = ?;

-- name: UpsertUserPreferences :exec
INSERT INTO user_preferences (user_id, theme, terminal_theme, ui_font_custom_enabled, mono_font_custom_enabled, ui_fonts, mono_fonts, diff_view, turn_end_sound, turn_end_sound_volume, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
ON CONFLICT (user_id) DO UPDATE SET
  theme = excluded.theme,
  terminal_theme = excluded.terminal_theme,
  ui_font_custom_enabled = excluded.ui_font_custom_enabled,
  mono_font_custom_enabled = excluded.mono_font_custom_enabled,
  ui_fonts = excluded.ui_fonts,
  mono_fonts = excluded.mono_fonts,
  diff_view = excluded.diff_view,
  turn_end_sound = excluded.turn_end_sound,
  turn_end_sound_volume = excluded.turn_end_sound_volume,
  updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now');
