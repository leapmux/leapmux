package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"golang.org/x/sync/errgroup"
)

type openCodeModelInfo struct {
	ID           string `json:"id"`
	ProviderID   string `json:"providerID"`
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	Capabilities struct {
		Input []string `json:"input"`
	} `json:"capabilities"`
	Variants []struct {
		ID string `json:"id"`
	} `json:"variants"`
	Limit struct {
		Context int64 `json:"context"`
	} `json:"limit"`
}

type openCodeAgentInfo struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Mode        string            `json:"mode"`
	Hidden      bool              `json:"hidden"`
	Model       *openCodeModelRef `json:"model"`
}

type openCodeCatalog struct {
	models       []openCodeModelInfo
	agents       []openCodeAgentInfo
	defaultModel openCodeModelRef
	defaultAgent string
}

const (
	openCodeCatalogActivationPollTag        = "opencode-catalog-activation-poll"
	openCodeCatalogActivationInitialBackoff = 50 * time.Millisecond
	openCodeCatalogActivationMaxBackoff     = time.Second
	openCodeDefaultModelWarmupBudget        = 15 * time.Second
	openCodeRequestedModelWarmupBudget      = 3 * time.Second
)

func (info openCodeAgentInfo) selectableAsPrimary() bool {
	return !info.Hidden && (info.Mode == "primary" || info.Mode == "all")
}

// The stream greeting can arrive before the location's plugins finish activating.
// OpenCode stops waiting after five seconds and reports that specific condition.
// Keep waiting during startup so the catalog includes every enabled plugin.
// A 200 {"data": null} response from /api/model/default is retried only within
// openCodeDefaultModelWarmupBudget so unconfigured setups fail fast instead of
// waiting out the full startup timeout. When an explicit requestedModel is not
// yet present in a freshly booted catalog (OpenCode v2.4+ briefly serves built-in
// models before custom config providers finish loading), keep polling within
// openCodeRequestedModelWarmupBudget before returning the catalog as-is.
func awaitOpenCodeCatalog(ctx context.Context, rpc openCodeRPC, clock quartz.Clock, timeout time.Duration) (openCodeCatalog, error) {
	return awaitOpenCodeCatalogWithModel(ctx, rpc, clock, timeout, "")
}

func awaitOpenCodeCatalogWithModel(ctx context.Context, rpc openCodeRPC, clock quartz.Clock, timeout time.Duration, requestedModel string) (openCodeCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	startupDeadline := clock.Now().Add(timeout)
	var defaultModelDeadline time.Time
	var requestedModelDeadline time.Time
	delay := openCodeCatalogActivationInitialBackoff
	for {
		catalog, err := loadOpenCodeCatalog(ctx, rpc)
		if err == nil {
			if requestedModel == "" || catalog.findModel(requestedModel) != nil {
				return catalog, nil
			}
			now := clock.Now()
			if requestedModelDeadline.IsZero() {
				requestedModelDeadline = now.Add(openCodeRequestedModelWarmupBudget)
				delay = openCodeCatalogActivationInitialBackoff
			}
			remaining := requestedModelDeadline.Sub(now)
			if remaining <= 0 {
				return catalog, nil
			}
			startupRemaining := startupDeadline.Sub(now)
			if startupRemaining <= 0 {
				return catalog, nil
			}
			wait := min(delay, remaining, startupRemaining)
			timer := clock.NewTimer(wait, openCodeCatalogActivationPollTag)
			select {
			case <-ctx.Done():
				timer.Stop()
				return openCodeCatalog{}, ctx.Err()
			case <-timer.C:
			}
			delay = min(delay*2, openCodeCatalogActivationMaxBackoff)
			continue
		}
		isDefaultModelPending := errors.Is(err, errOpenCodeDefaultModelPending)
		if !isDefaultModelPending && !openCodePluginActivationPending(err) {
			return openCodeCatalog{}, err
		}
		now := clock.Now()
		wait := delay
		if isDefaultModelPending {
			if defaultModelDeadline.IsZero() {
				defaultModelDeadline = now.Add(openCodeDefaultModelWarmupBudget)
			}
			remaining := defaultModelDeadline.Sub(now)
			if remaining <= 0 {
				return openCodeCatalog{}, errOpenCodeNoDefaultModel
			}
			wait = min(wait, remaining)
		}
		startupRemaining := startupDeadline.Sub(now)
		if startupRemaining <= 0 {
			return openCodeCatalog{}, errors.Join(err, context.DeadlineExceeded)
		}
		wait = min(wait, startupRemaining)
		timer := clock.NewTimer(wait, openCodeCatalogActivationPollTag)
		select {
		case <-ctx.Done():
			timer.Stop()
			return openCodeCatalog{}, errors.Join(err, ctx.Err())
		case <-timer.C:
		}
		delay = min(delay*2, openCodeCatalogActivationMaxBackoff)
	}
}

var (
	errOpenCodeDefaultModelPending = errors.New("default model not ready yet")
	errOpenCodeNoDefaultModel      = errors.New("OpenCode reported no default model configured")
)

func openCodePluginActivationPending(err error) bool {
	var status *providerkit.HTTPStatusError
	if !errors.As(err, &status) || status.StatusCode != http.StatusServiceUnavailable {
		return false
	}
	var response struct {
		Tag     string `json:"_tag"`
		Service string `json:"service"`
		Message string `json:"message"`
	}
	return json.Unmarshal([]byte(status.Body), &response) == nil &&
		response.Tag == "ServiceUnavailableError" && response.Service == "plugin" &&
		response.Message == "Location plugins did not finish activating"
}

func loadOpenCodeCatalog(ctx context.Context, rpc openCodeRPC) (openCodeCatalog, error) {
	var catalog openCodeCatalog
	// Model endpoints wait for plugin activation; the agent endpoint reads immediately.
	// OpenCode v2.4+ can also return 200 {"data": null} briefly before models finish loading.
	model, err := openCodeData[openCodeModelInfo](ctx, rpc, http.MethodGet, "/api/model/default", nil)
	if err != nil {
		if errors.Is(err, errOpenCodeNoData) {
			return openCodeCatalog{}, fmt.Errorf("read the OpenCode default model: %w: %w", errOpenCodeDefaultModelPending, err)
		}
		return openCodeCatalog{}, fmt.Errorf("read the OpenCode default model: %w", err)
	}
	catalog.defaultModel = openCodeModelRef{ProviderID: model.ProviderID, ID: model.ID}
	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error {
		models, err := openCodeData[[]openCodeModelInfo](ctx, rpc, http.MethodGet, "/api/model", nil)
		catalog.models = models
		return err
	})
	group.Go(func() error {
		agents, err := openCodeData[[]openCodeAgentInfo](ctx, rpc, http.MethodGet, "/api/agent", nil)
		catalog.agents = agents
		return err
	})
	if err := group.Wait(); err != nil {
		return openCodeCatalog{}, fmt.Errorf("read the OpenCode catalog: %w", err)
	}
	// The native list puts its resolved selectable default first.
	for _, entry := range catalog.agents {
		if entry.selectableAsPrimary() {
			catalog.defaultAgent = entry.ID
			break
		}
	}
	if err := catalog.validate(); err != nil {
		return openCodeCatalog{}, err
	}
	return catalog, nil
}

func openCodeModelKey(ref openCodeModelRef) string {
	if ref.ProviderID == "" || ref.ID == "" {
		return ""
	}
	return ref.ProviderID + "/" + ref.ID
}

func (c openCodeCatalog) validate() error {
	modelKeys := make(map[string]struct{}, len(c.models))
	for _, model := range c.models {
		key := openCodeModelKey(openCodeModelRef{ProviderID: model.ProviderID, ID: model.ID})
		if key == "" {
			return errors.New("the OpenCode catalog contains a model without an ID or provider")
		}
		if _, duplicate := modelKeys[key]; duplicate {
			return fmt.Errorf("the OpenCode catalog repeats model %q", key)
		}
		modelKeys[key] = struct{}{}
		variants := make(map[string]struct{}, len(model.Variants))
		for _, variant := range model.Variants {
			if variant.ID == "" {
				return fmt.Errorf("OpenCode model %q has a variant without an ID", key)
			}
			if _, duplicate := variants[variant.ID]; duplicate {
				return fmt.Errorf("OpenCode model %q repeats variant %q", key, variant.ID)
			}
			variants[variant.ID] = struct{}{}
		}
	}
	if c.findModel(openCodeModelKey(c.defaultModel)) == nil {
		return errors.New("the OpenCode default model is absent or disabled in its catalog")
	}
	agentIDs := make(map[string]struct{}, len(c.agents))
	for _, entry := range c.agents {
		if entry.ID == "" {
			return errors.New("the OpenCode catalog contains an agent without an ID")
		}
		if _, duplicate := agentIDs[entry.ID]; duplicate {
			return fmt.Errorf("the OpenCode catalog repeats agent %q", entry.ID)
		}
		agentIDs[entry.ID] = struct{}{}
	}
	if len(c.primaryAgents()) == 0 {
		return errors.New("the OpenCode catalog contains no visible primary agent")
	}
	return nil
}

func (c openCodeCatalog) findModel(key string) *openCodeModelInfo {
	for i := range c.models {
		model := &c.models[i]
		if model.Enabled && key == openCodeModelKey(openCodeModelRef{ProviderID: model.ProviderID, ID: model.ID}) {
			return model
		}
	}
	return nil
}

func (c openCodeCatalog) modelRef(key string) (openCodeModelRef, bool) {
	model := c.findModel(key)
	if model == nil {
		return openCodeModelRef{}, false
	}
	return openCodeModelRef{ProviderID: model.ProviderID, ID: model.ID}, true
}

func (c openCodeCatalog) primaryAgents() []agent.OptionDef {
	var options []agent.OptionDef
	for _, entry := range c.agents {
		if !entry.selectableAsPrimary() {
			continue
		}
		label := entry.Name
		if strings.TrimSpace(label) == "" {
			label = providerkit.TitleCaseID(entry.ID, "")
		}
		options = append(options, agent.OptionDef{Id: entry.ID, Name: label, Description: entry.Description, Default: entry.ID == c.defaultAgent})
	}
	return options
}

func (c openCodeCatalog) offersAgent(id string) bool {
	for _, entry := range c.agents {
		if entry.ID == id && entry.selectableAsPrimary() {
			return true
		}
	}
	return false
}

func (c openCodeCatalog) modelOptions() []*agent.ModelInfo {
	models := make([]*agent.ModelInfo, 0, len(c.models))
	for _, native := range c.models {
		if !native.Enabled {
			continue
		}
		key := openCodeModelKey(openCodeModelRef{ProviderID: native.ProviderID, ID: native.ID})
		label := native.Name
		if strings.TrimSpace(label) == "" {
			label = native.ID
		}
		info := &agent.ModelInfo{
			Id: key, DisplayName: label, IsDefault: key == openCodeModelKey(c.defaultModel),
			ContextWindow:      max(0, native.Limit.Context),
			SupportsAudioInput: slices.Contains(native.Capabilities.Input, "audio"),
		}
		var efforts []*agent.EffortInfo
		for _, variant := range native.Variants {
			if variant.ID == "default" {
				continue
			}
			efforts = append(efforts, providerkit.EffortTier(variant.ID))
		}
		if len(efforts) > 0 {
			providerkit.SortEffortsDescending(efforts)
			for _, effort := range efforts {
				effort.Id = openCodeVariantOption(effort.Id)
			}
			info.DefaultEffort = agent.EffortAuto
			info.SupportedEfforts = append([]*agent.EffortInfo{{Id: agent.EffortAuto, Name: "Model default"}}, efforts...)
		}
		models = append(models, info)
	}
	return models
}

// Native variant IDs share no namespace with the app's default-effort option.
const openCodeVariantOptionPrefix = "variant:"

func openCodeVariantOption(nativeID string) string {
	if nativeID == "" {
		return agent.EffortAuto
	}
	return openCodeVariantOptionPrefix + nativeID
}

func openCodeVariantFromOption(value string) (string, bool) {
	if value == agent.EffortAuto {
		return "", true
	}
	nativeID, found := strings.CutPrefix(value, openCodeVariantOptionPrefix)
	return nativeID, found && nativeID != ""
}

func (c openCodeCatalog) settleVariant(ref openCodeModelRef) (openCodeModelRef, error) {
	model := c.findModel(openCodeModelKey(ref))
	if model == nil {
		return openCodeModelRef{}, fmt.Errorf("OpenCode model %q is absent or disabled", openCodeModelKey(ref))
	}
	if ref.Variant == "" || ref.Variant == "default" {
		ref.Variant = ""
		return ref, nil
	}
	for _, variant := range model.Variants {
		if variant.ID == ref.Variant {
			return ref, nil
		}
	}
	ref.Variant = ""
	return ref, nil
}

func (c openCodeCatalog) effectiveModel(selected *openCodeModelRef, primaryAgent string) (openCodeModelRef, error) {
	if selected != nil {
		if c.findModel(openCodeModelKey(*selected)) == nil {
			return openCodeModelRef{}, fmt.Errorf("OpenCode model %q is absent or disabled", openCodeModelKey(*selected))
		}
		return *selected, nil
	}
	if primaryAgent == "" {
		primaryAgent = c.defaultAgent
	}
	for _, entry := range c.agents {
		if entry.ID == primaryAgent && entry.Model != nil && c.findModel(openCodeModelKey(*entry.Model)) != nil {
			return *entry.Model, nil
		}
	}
	return c.defaultModel, nil
}
