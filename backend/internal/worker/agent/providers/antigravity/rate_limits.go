package antigravity

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
)

type antigravityQuotaSummary struct {
	Groups  []antigravityQuotaGroup  `json:"groups"`
	Buckets []antigravityQuotaBucket `json:"buckets"`
}

type antigravityQuotaGroup struct {
	DisplayName string                   `json:"displayName"`
	Buckets     []antigravityQuotaBucket `json:"buckets"`
}

type antigravityQuotaBucket struct {
	ID                string   `json:"bucketId"`
	DisplayName       string   `json:"displayName"`
	Window            string   `json:"window"`
	RemainingFraction *float64 `json:"remainingFraction"`
	Disabled          bool     `json:"disabled"`
	ResetTime         string   `json:"resetTime"`
}

func (summary antigravityQuotaSummary) rateLimitValues() map[string]any {
	values := make(map[string]any)
	addBucket := func(group string, bucket antigravityQuotaBucket) {
		if bucket.ID == "" || bucket.Disabled || bucket.RemainingFraction == nil {
			return
		}
		if _, published := values[bucket.ID]; published {
			return
		}
		remaining := max(0, min(1, *bucket.RemainingFraction))
		status := contracts.RateLimitStatusAllowed
		if remaining == 0 {
			status = contracts.RateLimitStatusExceeded
		}
		value := map[string]any{
			contracts.RateLimitFieldLabel:       bucket.label(group),
			contracts.RateLimitFieldUtilization: 1 - remaining,
			contracts.RateLimitFieldStatus:      status,
		}
		if reset, err := time.Parse(time.RFC3339, bucket.ResetTime); err == nil {
			value[contracts.RateLimitFieldResetsAt] = reset.Unix()
		}
		values[bucket.ID] = value
	}
	// The grouped representation supplies the pool label when both lists contain a bucket.
	for _, group := range summary.Groups {
		for _, bucket := range group.Buckets {
			addBucket(strings.TrimSpace(group.DisplayName), bucket)
		}
	}
	for _, bucket := range summary.Buckets {
		addBucket("", bucket)
	}
	return values
}

func (bucket antigravityQuotaBucket) label(group string) string {
	window := strings.TrimSpace(bucket.DisplayName)
	switch bucket.Window {
	case "weekly":
		window = "Weekly"
	case "5h":
		window = "5-hour"
	}
	if window == "" {
		window = bucket.Window
	}
	if group != "" {
		if window == "" || window == group {
			return group
		}
		return group + " · " + window
	}
	if window == "" {
		return "Quota"
	}
	return window
}

func (a *Agent) refreshAccountState() {
	a.Mu.Lock()
	ctx, generation := a.streamCtx, a.generation
	current := a.accountRefreshCurrentLocked(ctx, generation)
	a.Mu.Unlock()
	if !current {
		return
	}

	var catalogWork sync.WaitGroup
	catalogWork.Go(func() { a.refreshAccountCatalog(ctx, generation) })
	defer catalogWork.Wait()
	summary, err := a.rpc.quotaSummary(ctx)
	if err != nil {
		slog.Debug("antigravity refresh account quota", "agent_id", a.AgentID(), "error", err)
		return
	}
	values := summary.rateLimitValues()
	a.Mu.Lock()
	current = a.accountRefreshCurrentLocked(ctx, generation)
	a.Mu.Unlock()
	if !current {
		return
	}
	a.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyRateLimits: map[string]any{
		contracts.RateLimitUpdateFieldMode:   contracts.RateLimitUpdateModeReplace,
		contracts.RateLimitUpdateFieldValues: values,
	}})
}

func (a *Agent) accountRefreshCurrentLocked(ctx context.Context, generation uint64) bool {
	return generation == a.generation && a.cascadeCurrentLocked(a.root) && ctx.Err() == nil && !a.IsDiscardingOutput()
}
