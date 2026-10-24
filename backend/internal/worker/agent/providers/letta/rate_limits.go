package letta

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
)

type lettaQuotaOwner struct {
	scope        runtimeScope
	modelHandle  string
	providerName string
	eligible     bool
}

type lettaQuotaAccount struct {
	backend      string
	providerName string
	connectionID string
}

type lettaQuotaState struct {
	owner   lettaQuotaOwner
	account lettaQuotaAccount
	ctx     context.Context
	cancel  context.CancelFunc
}

type lettaQuotaCommand struct {
	lettaCommand
	Target       string `json:"target"`
	ProviderName string `json:"provider_name,omitempty"`
	ForceRefresh bool   `json:"force_refresh,omitempty"`
}

type lettaQuotaWindow struct {
	Label              string   `json:"label"`
	UsedPercent        *float64 `json:"usedPercent"`
	WindowDurationMins *float64 `json:"windowDurationMins"`
	ResetsAt           *float64 `json:"resetsAt"`
}

type lettaIndividualLimit struct {
	RemainingPercent *float64 `json:"remainingPercent"`
	ResetsAt         *float64 `json:"resetsAt"`
}

type lettaQuotaSnapshot struct {
	ProviderName    string                `json:"providerName"`
	LimitReached    *bool                 `json:"limitReached"`
	Primary         *lettaQuotaWindow     `json:"primary"`
	Secondary       *lettaQuotaWindow     `json:"secondary"`
	Additional      []*lettaQuotaWindow   `json:"additional"`
	IndividualLimit *lettaIndividualLimit `json:"individualLimit"`
}

type lettaQuotaError struct {
	Code         string   `json:"code"`
	Message      string   `json:"message"`
	RetryAfterMs *float64 `json:"retryAfterMs"`
}

type lettaQuotaResponse struct {
	lettaReplyHeader
	Target string              `json:"target"`
	Usage  *lettaQuotaSnapshot `json:"usage"`
	Error  lettaQuotaError     `json:"error"`
}

type lettaQuotaConnection struct {
	Connected    bool   `json:"is_connected"`
	ID           string `json:"id"`
	Name         string `json:"provider_name"`
	ProviderType string `json:"provider_type"`
	AuthType     string `json:"auth_type"`
}

type lettaQuotaProvider struct {
	ProviderType    string                  `json:"provider_type"`
	OAuthProviderID string                  `json:"oauth_provider_id"`
	Connections     *[]lettaQuotaConnection `json:"connected_providers"`
}

type lettaQuotaConnectionsResponse struct {
	lettaReplyHeader
	Target    string                `json:"target"`
	Providers *[]lettaQuotaProvider `json:"providers"`
	Error     string                `json:"error"`
}

func (a *Agent) quotaOwner() lettaQuotaOwner {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	owner := lettaQuotaOwner{scope: runtimeScope{AgentID: a.agentID, ConversationID: a.conversationID}, modelHandle: a.settings.model}
	prefix, _, hasProvider := strings.Cut(owner.modelHandle, "/")
	owner.providerName = prefix
	if !hasProvider || prefix == "" || owner.scope.AgentID == "" || owner.scope.ConversationID == "" {
		return owner
	}
	kind := a.settings.modelProvider
	for _, model := range a.catalog.models {
		if model.id != owner.modelHandle {
			continue
		}
		for _, variant := range model.variants {
			if variant.providerType != "" {
				kind = variant.providerType
				break
			}
		}
		break
	}
	if kind == "" {
		kind = a.catalog.providerAliases[prefix]
		if kind == "" && (prefix == "openai-codex" || prefix == "chatgpt-plus-pro") {
			kind = "openai-codex"
		}
	}
	owner.eligible = kind == "chatgpt_oauth" || kind == "openai-codex"
	return owner
}

func (a *Agent) requestQuotaRefresh() {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.requestQuotaRefreshLocked()
}

// requestQuotaRefreshLocked runs inside the output dispatcher.
// It publishes ownership changes immediately but leaves native I/O to the worker.
func (a *Agent) requestQuotaRefreshLocked() {
	if a.ws == nil || a.Context() == nil || a.Context().Err() != nil || a.outputEnded {
		return
	}
	owner := a.quotaOwner()
	if a.quotaState.ctx == nil || a.quotaState.owner != owner {
		if a.quotaState.cancel != nil {
			a.quotaState.cancel()
		}
		ctx, cancel := context.WithCancel(a.Context())
		a.quotaState = lettaQuotaState{owner: owner, ctx: ctx, cancel: cancel}
		a.publishQuota(map[string]any{})
	}
	if owner.eligible {
		a.quotaWork.Run("quota", a.refreshNativeQuota)
	}
}

// ownsQuotaLocked reads quota state while the caller holds dispatchMu.
func (a *Agent) ownsQuotaLocked(owner lettaQuotaOwner, ctx context.Context) bool {
	return !a.outputEnded && ctx != nil && ctx.Err() == nil && a.quotaState.owner == owner && a.quotaOwner() == owner
}

func (a *Agent) refreshNativeQuota() {
	a.dispatchMu.Lock()
	state := a.quotaState
	active := state.owner.eligible && a.ownsQuotaLocked(state.owner, state.ctx)
	a.dispatchMu.Unlock()
	if !active {
		return
	}
	info, err := a.requestNativeSettings(state.ctx, lettaSettingsCommand{lettaCommand: newLettaCommand("app_server_info", a.nextRequestID())})
	if err != nil {
		slog.Debug("letta quota backend lookup failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.dispatchMu.Lock()
	active = a.ownsQuotaLocked(state.owner, state.ctx)
	a.dispatchMu.Unlock()
	if !active {
		return
	}
	account, connected, err := a.resolveQuotaProvider(state.ctx, state.owner, info.Backend)
	if err != nil {
		slog.Debug("letta quota connection lookup failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.dispatchMu.Lock()
	if !a.ownsQuotaLocked(state.owner, state.ctx) {
		a.dispatchMu.Unlock()
		return
	}
	if !connected {
		a.quotaState.account = lettaQuotaAccount{}
		a.publishQuota(map[string]any{})
		a.dispatchMu.Unlock()
		return
	}
	if a.quotaState.account != account {
		if a.quotaState.account != (lettaQuotaAccount{}) {
			a.publishQuota(map[string]any{})
		}
		a.quotaState.account = account
	}
	now := a.Clock().Now()
	for key, retryAt := range a.quotaRetries {
		if !retryAt.After(now) {
			delete(a.quotaRetries, key)
		}
	}
	delay := a.quotaRetries[account].Sub(now)
	a.dispatchMu.Unlock()
	if delay > 0 {
		timer := a.Clock().NewTimer(delay, "letta quota retry")
		defer timer.Stop("letta quota retry")
		select {
		case <-timer.C:
		case <-state.ctx.Done():
			return
		case <-a.ProcessDone():
			return
		}
	}
	command := lettaQuotaCommand{lettaCommand: newLettaCommand(lettaChatGPTUsageRead, a.nextRequestID()), Target: account.backend, ProviderName: account.providerName, ForceRefresh: true}
	var response lettaQuotaResponse
	if err := a.awaitNativeReply(state.ctx, command, &response); err != nil {
		slog.Debug("letta quota read failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if !a.ownsQuotaLocked(state.owner, state.ctx) || a.quotaState.account != account || response.Target != account.backend {
		return
	}
	if !*response.Success {
		switch response.Error.Code {
		case "not_connected", "unauthorized", "forbidden", "refresh_failed":
			a.publishQuota(map[string]any{})
		case "rate_limited":
			if a.quotaRetries == nil {
				a.quotaRetries = make(map[lettaQuotaAccount]time.Time)
			}
			a.quotaRetries[account] = a.Clock().Now().Add(lettaQuotaRetryDelay(response.Error.RetryAfterMs))
			a.quotaWork.Run("quota", a.refreshNativeQuota)
		}
		return
	}
	if response.Usage == nil || response.Usage.ProviderName != account.providerName {
		return
	}
	delete(a.quotaRetries, account)
	a.publishQuota(lettaQuotaValues(response.Usage))
}

func (a *Agent) resolveQuotaProvider(ctx context.Context, owner lettaQuotaOwner, backend string) (lettaQuotaAccount, bool, error) {
	if backend == "api" {
		return lettaQuotaAccount{backend: backend, providerName: owner.providerName}, true, nil
	}
	if backend != "local" {
		return lettaQuotaAccount{}, false, errors.New("the Letta quota backend is unavailable")
	}
	command := lettaQuotaCommand{lettaCommand: newLettaCommand(lettaListConnectProviders, a.nextRequestID()), Target: backend}
	var response lettaQuotaConnectionsResponse
	if err := a.awaitNativeReply(ctx, command, &response); err != nil {
		return lettaQuotaAccount{}, false, err
	}
	if !*response.Success {
		return lettaQuotaAccount{}, false, fmt.Errorf("the Letta connection lookup failed: %s", response.Error)
	}
	if response.Target != backend || response.Providers == nil {
		return lettaQuotaAccount{}, false, errors.New("the Letta connection lookup contains no matching provider list")
	}
	connections := make(map[string]lettaQuotaConnection)
	for _, provider := range *response.Providers {
		if provider.ProviderType != "chatgpt_oauth" && provider.ProviderType != "openai-codex" && provider.OAuthProviderID != "openai-codex" {
			continue
		}
		if provider.Connections == nil {
			return lettaQuotaAccount{}, false, errors.New("the Letta provider contains no connection list")
		}
		for _, connection := range *provider.Connections {
			if !connection.Connected {
				continue
			}
			if connection.Name == "" || connection.ID == "" {
				return lettaQuotaAccount{}, false, errors.New("the Letta connection has no identity")
			}
			if previous, exists := connections[connection.Name]; exists && previous != connection {
				return lettaQuotaAccount{}, false, errors.New("the Letta provider has conflicting connection identities")
			}
			connections[connection.Name] = connection
		}
	}
	providers := []string{owner.providerName}
	if owner.providerName == "openai-codex" || owner.providerName == "chatgpt-plus-pro" {
		// Native recordForProviderId uses this order. The quota command's
		// omitted-provider default uses the reverse order and can select another account.
		providers = []string{"openai-codex", "chatgpt-plus-pro"}
	}
	for _, provider := range providers {
		connection, exists := connections[provider]
		if !exists {
			continue
		}
		if connection.AuthType != "oauth" || connection.ProviderType != "chatgpt_oauth" && connection.ProviderType != "openai-codex" {
			return lettaQuotaAccount{}, false, nil
		}
		return lettaQuotaAccount{backend: backend, providerName: connection.Name, connectionID: connection.ID}, true, nil
	}
	return lettaQuotaAccount{}, false, nil
}

// publishQuota is called under dispatchMu, never under the process-state mutex.
func (a *Agent) publishQuota(values map[string]any) {
	a.sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyRateLimits: map[string]any{
		contracts.RateLimitUpdateFieldMode:   contracts.RateLimitUpdateModeReplace,
		contracts.RateLimitUpdateFieldValues: values,
	}})
}

func lettaQuotaRetryDelay(milliseconds *float64) time.Duration {
	if milliseconds == nil || !lettaQuotaFinite(*milliseconds) || *milliseconds < 0 {
		return 30 * time.Second
	}
	const maximumMillis = int64(math.MaxInt64) / int64(time.Millisecond)
	if *milliseconds >= float64(maximumMillis) {
		return time.Duration(math.MaxInt64)
	}
	return max(time.Second, time.Duration(math.Ceil(*milliseconds))*time.Millisecond)
}

func lettaQuotaFinite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func lettaQuotaWindowInfo(window *lettaQuotaWindow, label string) map[string]any {
	if window == nil {
		return nil
	}
	info := map[string]any{}
	if window.UsedPercent != nil && lettaQuotaFinite(*window.UsedPercent) && *window.UsedPercent >= 0 {
		info[contracts.RateLimitFieldUtilization] = *window.UsedPercent / 100
		status := contracts.RateLimitStatusAllowed
		if *window.UsedPercent >= 100 {
			status = contracts.RateLimitStatusExceeded
		} else if *window.UsedPercent >= 80 {
			status = contracts.RateLimitStatusAllowedWarning
		}
		info[contracts.RateLimitFieldStatus] = status
	}
	if reset := window.ResetsAt; reset != nil && lettaQuotaFinite(*reset) && *reset >= 0 && *reset <= 253402300799 && math.Trunc(*reset) == *reset {
		info[contracts.RateLimitFieldResetsAt] = int64(*reset)
	}
	validDuration := window.WindowDurationMins != nil && lettaQuotaFinite(*window.WindowDurationMins) && *window.WindowDurationMins > 0
	if label == "" && validDuration {
		switch *window.WindowDurationMins {
		case 300:
			label = contracts.RateLimitTypeFiveHour
		case 10080:
			label = contracts.RateLimitTypeSevenDay
		default:
			label = fmt.Sprintf("%g_minute", *window.WindowDurationMins)
		}
	}
	if len(info) == 0 && !validDuration {
		return nil
	}
	if label == "" {
		label = window.Label
	}
	if label != "" {
		info[contracts.RateLimitFieldRateLimitType] = label
	}
	return info
}

func lettaQuotaValues(snapshot *lettaQuotaSnapshot) map[string]any {
	values := map[string]any{}
	if snapshot == nil {
		return values
	}
	if info := lettaQuotaWindowInfo(snapshot.Primary, ""); info != nil {
		values["primary"] = info
	}
	if info := lettaQuotaWindowInfo(snapshot.Secondary, ""); info != nil {
		values["secondary"] = info
	}
	for index, window := range snapshot.Additional {
		if window == nil {
			continue
		}
		if info := lettaQuotaWindowInfo(window, window.Label); info != nil {
			values[fmt.Sprintf("additional_%d", index)] = info
		}
	}
	if individual := snapshot.IndividualLimit; individual != nil {
		window := &lettaQuotaWindow{ResetsAt: individual.ResetsAt}
		if remaining := individual.RemainingPercent; remaining != nil && lettaQuotaFinite(*remaining) && *remaining >= 0 && *remaining <= 100 {
			used := 100 - *remaining
			window.UsedPercent = &used
		}
		if info := lettaQuotaWindowInfo(window, "individual"); info != nil {
			values["individual"] = info
		}
	}
	if snapshot.LimitReached != nil && *snapshot.LimitReached {
		values["account"] = map[string]any{contracts.RateLimitFieldRateLimitType: "account", contracts.RateLimitFieldStatus: contracts.RateLimitStatusExceeded}
	}
	return values
}
