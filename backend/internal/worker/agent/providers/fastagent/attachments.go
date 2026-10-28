package fastagent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const fastagentAudioStatusMaxBytes = 32 * 1024

// fastagentAudioQuery holds only the internal status command's output.
// Agent.audioQueryMu protects every field while the native reader appends text.
type fastagentAudioQuery struct {
	sessionID string
	text      strings.Builder
	invalid   bool
}

// prepareAudioPrompt checks the running agent rather than the launch model:
// native /model switch and /agent use can change it without an ACP setting.
// Fast Agent has no structured model-status RPC. Its local /model command
// reports the running provider without making a model request.
func (a *Agent) prepareAudioPrompt(sessionID string, attachments []agent.ClassifiedAttachment, _ []map[string]any) error {
	hasAudio, err := providerkit.ValidateAudioAttachments(attachments, (fastagentProvider{}).ValidateAttachment)
	if err != nil {
		return err
	}
	if !hasAudio {
		return nil
	}
	params, err := json.Marshal(map[string]any{"sessionId": sessionID, "prompt": []map[string]string{{"type": "text", "text": "/model"}}})
	if err != nil {
		return fmt.Errorf("encode Fast Agent model query: %w", err)
	}
	query := &fastagentAudioQuery{sessionID: sessionID}
	a.audioQueryMu.Lock()
	if a.audioQuery != nil {
		a.audioQueryMu.Unlock()
		return fmt.Errorf("a Fast Agent audio model check is already in progress")
	}
	a.audioQuery = query
	a.audioQueryMu.Unlock()
	defer func() {
		a.audioQueryMu.Lock()
		if a.audioQuery == query {
			a.audioQuery = nil
		}
		a.audioQueryMu.Unlock()
	}()
	raw, err := a.SendRequest(acp.MethodSessionPrompt, params, a.APITimeout())
	if err != nil {
		return fmt.Errorf("check Fast Agent audio model: %w", err)
	}
	a.audioQueryMu.Lock()
	a.audioQuery = nil
	report, invalid := query.text.String(), query.invalid
	a.audioQueryMu.Unlock()
	var result struct {
		StopReason string `json:"stopReason"`
	}
	if json.Unmarshal(raw, &result) != nil || result.StopReason != "end_turn" || invalid {
		return fmt.Errorf("cannot verify the running Fast Agent audio provider from its native model status")
	}
	return validateFastagentAudioProvider(report)
}

// observePromptUpdate consumes the local command's complete model report.
// Native local commands emit one Markdown chunk. Other text, including late
// output from a prior turn, keeps its normal transcript route.
func (a *Agent) observePromptUpdate(sessionID string, _ agent.ProviderServices, raw json.RawMessage) bool {
	a.audioQueryMu.Lock()
	query := a.audioQuery
	if query == nil || query.sessionID != sessionID {
		a.audioQueryMu.Unlock()
		return false
	}
	var update struct {
		Kind    string `json:"sessionUpdate"`
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &update) != nil || update.Kind != contracts.ACPUpdateAgentMessageChunk || update.Content.Type != "text" {
		a.audioQueryMu.Unlock()
		return false
	}
	heading, _, _ := strings.Cut(strings.TrimSpace(update.Content.Text), "\n")
	if heading != "# model" {
		a.audioQueryMu.Unlock()
		return false
	}
	if query.text.Len() > 0 || len(update.Content.Text) > fastagentAudioStatusMaxBytes {
		query.invalid = true
	} else if !query.invalid {
		query.text.WriteString(update.Content.Text)
	}
	a.audioQueryMu.Unlock()
	return true
}

func validateFastagentAudioProvider(report string) error {
	lines := strings.Split(strings.TrimSpace(report), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "# model" {
		return fmt.Errorf("cannot read the native Fast Agent model status")
	}
	provider, model := "", ""
	providerCount, modelCount := 0, 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Provider:") {
			value, ok := strings.CutPrefix(line, "Provider: ")
			if !ok || !strings.HasSuffix(value, ".") {
				return fmt.Errorf("cannot read the native Fast Agent provider field")
			}
			provider = strings.TrimSpace(strings.TrimSuffix(value, "."))
			providerCount++
		}
		if strings.HasPrefix(line, "Selected model:") {
			value, ok := strings.CutPrefix(line, "Selected model: ")
			if !ok || !strings.HasSuffix(value, ".") {
				return fmt.Errorf("cannot read the native Fast Agent model field")
			}
			model = strings.TrimSpace(strings.TrimSuffix(value, "."))
			modelCount++
		}
	}
	if providerCount != 1 || modelCount != 1 || provider == "" || model == "" {
		return fmt.Errorf("cannot identify the running Fast Agent model and provider from native status")
	}
	if provider != "google" {
		return fmt.Errorf("the selected Fast Agent model %q does not accept audio attachments through its %s provider", model, provider)
	}
	return nil
}
