package codebuddy

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Stream-json exposes AskUserQuestion only after the host declares support.
// Declare no MCP form capability until this adapter can serve that protocol.
func (a *Agent) initializeCapabilities(timeout time.Duration) (codebuddyControlResult, error) {
	return a.sendControlAndWait(`{"subtype":"initialize","capabilities":{"askUserQuestion":true}}`, timeout)
}

// Native lookup uses JavaScript's full Unicode lowercase conversion. Simple
// lowercase conversion differs for dotted I and contextual Greek sigma.
func codebuddyOutputStyleKey(name string) string {
	return cases.Lower(language.Und).String(name)
}

func normalizeCodebuddyOutputStyles(available []string) []string {
	styles := make([]string, 0, len(available))
	seen := make(map[string]bool, len(available))
	for _, name := range available {
		key := codebuddyOutputStyleKey(name)
		if strings.TrimSpace(name) == "" || seen[key] {
			continue
		}
		seen[key] = true
		styles = append(styles, name)
	}
	return styles
}

// Keep a configured custom style absent from the initial native snapshot.
// CodeBuddy loads custom styles asynchronously after that snapshot.
func codebuddyOutputStyleName(available []string, current string) string {
	if strings.TrimSpace(current) == "" {
		return ""
	}
	key := codebuddyOutputStyleKey(current)
	for _, name := range available {
		if codebuddyOutputStyleKey(name) == key {
			return name
		}
	}
	return current
}

func codebuddyOutputStyleGroup(styles []string, current string) *leapmuxv1.AvailableOptionGroup {
	if styles == nil {
		return nil
	}
	if strings.TrimSpace(current) == "" {
		current = ""
	}
	defs := make([]agent.OptionDef, 0, len(styles)+1)
	currentKey := codebuddyOutputStyleKey(current)
	seenCurrent := false
	for _, name := range styles {
		id := name
		if current != "" && codebuddyOutputStyleKey(name) == currentKey {
			id = current
			seenCurrent = true
		}
		defs = append(defs, agent.OptionDef{Id: id, Name: name, Default: id == current})
	}
	if current != "" && !seenCurrent {
		defs = append([]agent.OptionDef{{Id: current, Name: current, Default: true}}, defs...)
	}
	if len(defs) == 0 {
		return nil
	}
	return agent.SelectGroup(OptionOutputStyle, "Output Style", agent.OptionOrderProviderSecond, current, defs)
}

func (a *Agent) refreshOutputStyleCatalog() {
	if a.IsStopped() || a.Context().Err() != nil {
		return
	}
	result, err := a.initializeCapabilities(a.opts.EffectiveAPITimeout())
	if err != nil {
		if !a.IsStopped() && a.Context().Err() == nil {
			slog.Debug("CodeBuddy output style catalog refresh failed", "agent_id", a.AgentID(), "error", err)
		}
		return
	}
	if !result.HasOutputStyleCatalog || a.IsStopped() || a.Context().Err() != nil {
		return
	}
	styles := normalizeCodebuddyOutputStyles(result.OutputStyles)
	a.mu.Lock()
	// The later response's output_style can differ from the style retained by
	// the native session. Enrich the choices without changing that selection.
	listChanged := !slices.Equal(styles, a.outputStyles)
	a.outputStyles = styles
	a.mu.Unlock()
	if listChanged {
		// An empty refresh publishes changed choices without changing settings
		// or process status. The sink rejects a retired process's publication.
		a.sink.PersistSettingsRefresh(nil)
	}
}

// loadModelCatalog reads the live models before Start publishes the first
// option groups. CodeBuddy accepts this control request before the first prompt.
//
// CodeBuddy answers from its own process. A late answer therefore means a slow
// start, and a cold start on a loaded machine can take longer than any short
// fixed wait. A wait that ends early leaves the agent with no live catalog
// until its next restart, so the wait lasts as long as the startup handshake
// of the agent allows. A process exit ends it sooner.
func (a *Agent) loadModelCatalog() error {
	result, err := a.sendControlAndWait(`{"subtype":"get_available_models"}`, a.opts.EffectiveStartupTimeout())
	if err != nil {
		return err
	}
	if !result.HasModelCatalog {
		return fmt.Errorf("CodeBuddy returned no availableModels list")
	}
	a.mu.Lock()
	a.models = normalizeCodebuddyModels(result.Models)
	a.mu.Unlock()
	return nil
}

func normalizeCodebuddyModels(available []codebuddyModelInfo) []codebuddyModelInfo {
	seen := make(map[string]bool, len(available))
	models := make([]codebuddyModelInfo, 0, len(available))
	for _, model := range available {
		model.ID = strings.TrimSpace(model.ID)
		if model.ID == "" || seen[model.ID] {
			continue
		}
		seen[model.ID] = true
		models = append(models, model)
	}
	return models
}

// codebuddyModelGroup lists the live catalog and preserves a selected model
// that the catalog no longer offers, so its current value stays visible.
func codebuddyModelGroup(models []codebuddyModelInfo, current string) *leapmuxv1.AvailableOptionGroup {
	defs := make([]agent.OptionDef, 0, len(models)+1)
	seenCurrent := false
	for _, model := range models {
		if model.ID == "" {
			continue
		}
		defs = append(defs, agent.OptionDef{
			Id: model.ID, Name: agent.NameOrID(model.Name, model.ID),
			Description: model.Description, Default: model.ID == current,
		})
		seenCurrent = seenCurrent || model.ID == current
	}
	if current != "" && !seenCurrent {
		defs = append([]agent.OptionDef{{Id: current, Name: current, Default: true}}, defs...)
	}
	if len(defs) == 0 {
		return nil
	}
	return agent.SelectGroup(agent.OptionIDModel, agent.ModelGroupLabel, agent.OptionOrderModel, current, defs)
}

func codebuddyModelOffered(models []codebuddyModelInfo, model string) bool {
	for _, available := range models {
		if available.ID == model {
			return true
		}
	}
	return false
}
