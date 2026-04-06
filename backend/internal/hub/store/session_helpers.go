package store

// UserToSessionWithUser converts a User to a SessionWithUser.
// Used by NoSQL backends that cannot JOIN sessions with users.
func UserToSessionWithUser(u *User) *SessionWithUser {
	return &SessionWithUser{
		UserID:        u.ID,
		OrgID:         u.OrgID,
		Username:      u.Username,
		IsAdmin:       u.IsAdmin,
		EmailVerified: u.EmailVerified,
	}
}

// SessionsToActive converts UserSessions to ActiveSessions using a username map.
// Sessions whose user is not found in the map (deleted users) are skipped.
func SessionsToActive(sessions []UserSession, usernames map[string]string) []ActiveSession {
	result := make([]ActiveSession, 0, len(sessions))
	for _, s := range sessions {
		username := usernames[s.UserID]
		if username == "" {
			continue
		}
		result = append(result, ActiveSession{
			ID:           s.ID,
			UserID:       s.UserID,
			Username:     username,
			CreatedAt:    s.CreatedAt,
			LastActiveAt: s.LastActiveAt,
			ExpiresAt:    s.ExpiresAt,
			IPAddress:    s.IPAddress,
			UserAgent:    s.UserAgent,
		})
	}
	return result
}
