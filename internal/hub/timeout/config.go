package timeout

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/internal/hub/generated/db"
)

// Default timeout values (in seconds).
const (
	DefaultAPITimeout            = 10
	DefaultAgentStartupTimeout   = 30
	DefaultWorktreeCreateTimeout = 60
	DefaultWorktreeDeleteTimeout = 60
)

// Config holds configurable timeout values loaded from the database.
// All methods are safe for concurrent use.
type Config struct {
	apiTimeout            atomic.Int64
	agentStartupTimeout   atomic.Int64
	worktreeCreateTimeout atomic.Int64
	worktreeDeleteTimeout atomic.Int64
}

// NewFromDB loads timeout configuration from the database.
func NewFromDB(q *db.Queries) (*Config, error) {
	s, err := q.GetSystemSettings(context.Background())
	if err != nil {
		return nil, err
	}

	c := &Config{}
	c.refresh(s)
	return c, nil
}

// Refresh updates the timeout values from a system settings row.
func (c *Config) Refresh(s db.SystemSetting) {
	c.refresh(s)
}

func (c *Config) refresh(s db.SystemSetting) {
	c.apiTimeout.Store(clampTimeout(s.ApiTimeoutSeconds, DefaultAPITimeout))
	c.agentStartupTimeout.Store(clampTimeout(s.AgentStartupTimeoutSeconds, DefaultAgentStartupTimeout))
	c.worktreeCreateTimeout.Store(clampTimeout(s.WorktreeCreateTimeoutSeconds, DefaultWorktreeCreateTimeout))
	c.worktreeDeleteTimeout.Store(clampTimeout(s.WorktreeDeleteTimeoutSeconds, DefaultWorktreeDeleteTimeout))
}

// APITimeout returns the general API timeout.
func (c *Config) APITimeout() time.Duration {
	return time.Duration(c.apiTimeout.Load()) * time.Second
}

// AgentStartupTimeout returns the agent startup/resume timeout.
func (c *Config) AgentStartupTimeout() time.Duration {
	return time.Duration(c.agentStartupTimeout.Load()) * time.Second
}

// WorktreeCreateTimeout returns the worktree creation timeout.
func (c *Config) WorktreeCreateTimeout() time.Duration {
	return time.Duration(c.worktreeCreateTimeout.Load()) * time.Second
}

// WorktreeDeleteTimeout returns the worktree deletion timeout.
func (c *Config) WorktreeDeleteTimeout() time.Duration {
	return time.Duration(c.worktreeDeleteTimeout.Load()) * time.Second
}

func clampTimeout(val int64, defaultVal int64) int64 {
	if val <= 0 {
		return defaultVal
	}
	return val
}
