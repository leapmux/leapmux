package kilo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

const nativeAdmissionInterval = 20 * time.Millisecond

type kiloPrompt struct {
	MessageID string           `json:"messageID"`
	Model     kiloPromptModel  `json:"model"`
	Agent     string           `json:"agent,omitempty"`
	Variant   string           `json:"variant,omitempty"`
	Parts     []kiloPromptPart `json:"parts"`
}

type kiloPromptModel struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

type kiloPromptSettings struct {
	modelID      string
	primaryAgent string
	variant      string
}

type kiloPromptPart struct {
	Type     string  `json:"type"`
	Text     *string `json:"text,omitempty"`
	URL      string  `json:"url,omitempty"`
	MIME     string  `json:"mime,omitempty"`
	Filename string  `json:"filename,omitempty"`
}

func kiloPreparedParts(prepared acp.PreparedPrompt, sessionID string) ([]kiloPromptPart, error) {
	if prepared.SessionID != sessionID || sessionID == "" {
		return nil, agent.ErrInputSessionChanged
	}
	var parts []kiloPromptPart
	var text strings.Builder
	for _, block := range prepared.Blocks {
		blockType, _ := block["type"].(string)
		switch blockType {
		case "text":
			blockText, _ := block["text"].(string)
			text.WriteString(blockText)
			parts = append(parts, kiloPromptPart{Type: "text", Text: &blockText})
		case "image":
			data, _ := block["data"].(string)
			mime, _ := block["mimeType"].(string)
			if data == "" || mime == "" {
				return nil, nil
			}
			parts = append(parts, kiloPromptPart{Type: "file", URL: "data:" + mime + ";base64," + data, MIME: mime, Filename: "image"})
		case "resource":
			resource, ok := block["resource"].(map[string]any)
			if !ok || resource == nil {
				return nil, nil
			}
			uri, _ := resource["uri"].(string)
			if strings.HasPrefix(uri, "file:") {
				return nil, nil
			}
			if resText, hasText := resource["text"].(string); hasText {
				body := "[" + uri + "]\n" + resText
				text.WriteString(body)
				parts = append(parts, kiloPromptPart{Type: "text", Text: &body})
			} else if blob, _ := resource["blob"].(string); blob != "" {
				mime, _ := resource["mimeType"].(string)
				if mime == "" {
					return nil, nil
				}
				parts = append(parts, kiloPromptPart{Type: "file", URL: "data:" + mime + ";base64," + blob, MIME: mime, Filename: "file"})
			} else {
				return nil, nil
			}
		default:
			return nil, nil
		}
	}
	// ACP owns slash-command interpretation and any block it alone can convert.
	if strings.HasPrefix(strings.TrimSpace(text.String()), "/") {
		return nil, nil
	}
	return parts, nil
}

func (a *Agent) prepareNativePrompt(receipt agent.NativeInputReceipt, prepared acp.PreparedPrompt) (func(func(json.RawMessage, error)) error, error) {
	options := a.SettingsSnapshot().ConfirmedOptions()
	return a.prepareNativePromptFor(kiloReplyTarget{
		receipt: receipt, rootSessionID: receipt.SessionID, transcript: a.Sink(),
		flush: func(ids map[string]bool) { a.FlushNativeReplyOutput(receipt.SessionID, ids) },
	}, prepared, kiloPromptSettings{
		modelID: options[agent.OptionIDModel], primaryAgent: options[agent.OptionIDPrimaryAgent], variant: options[agent.OptionIDEffort],
	})
}

func (a *Agent) prepareNativePromptFor(target kiloReplyTarget, prepared acp.PreparedPrompt, settings kiloPromptSettings) (func(func(json.RawMessage, error)) error, error) {
	parts, err := kiloPreparedParts(prepared, target.receipt.SessionID)
	if err != nil || len(parts) == 0 {
		return nil, err
	}
	provider, model, ok := strings.Cut(settings.modelID, "/")
	if !ok || provider == "" || model == "" {
		return nil, nil
	}
	variant := settings.variant
	if variant == "default" || variant == agent.EffortAuto {
		variant = ""
	}
	// Kilo sorts message IDs by the timestamp prefix when it selects a queued
	// prompt. Use its ascending prefix format and a generated unique suffix.
	messageID := fmt.Sprintf("msg_%012x%s", (a.Clock().Now().UnixMilli()*4096)&((1<<48)-1), id.Generate())
	prompt := kiloPrompt{
		MessageID: messageID, Parts: parts,
		Model: kiloPromptModel{ProviderID: provider, ModelID: model},
		Agent: settings.primaryAgent, Variant: variant,
	}
	raw, err := json.Marshal(prompt)
	if err != nil {
		return nil, err
	}
	return func(finish func(json.RawMessage, error)) error {
		return a.submitNativePrompt(target, messageID, raw, finish)
	}, nil
}

func (a *Agent) nativeMessageRoute(sessionID, messageID string) string {
	path := "/session/" + url.PathEscape(sessionID) + "/message"
	if messageID != "" {
		path += "/" + url.PathEscape(messageID)
	}
	return path + "?" + url.Values{"directory": []string{a.workingDir}}.Encode()
}

type kiloPromptResult struct {
	message kiloNativeMessage
	err     error
}

func (a *Agent) submitNativePrompt(target kiloReplyTarget, messageID string, raw json.RawMessage, finish func(json.RawMessage, error)) error {
	receipt := target.receipt
	ctx, cancel := context.WithTimeout(a.Context(), a.APITimeout())
	defer cancel()
	select {
	case <-a.daemonReady:
		if a.daemonFailed.Load() {
			return errors.New("the Kilo daemon is unavailable")
		}
	case <-ctx.Done():
		return fmt.Errorf("wait for the Kilo daemon: %w", ctx.Err())
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.CurrentSessionID() != target.rootSessionID || a.IsStopped() {
		return agent.ErrInputSessionChanged
	}
	a.nativeReplies.add(messageID, receipt)
	results := make(chan kiloPromptResult, 1)
	runCtx, stop := context.WithCancel(a.Context())
	go func() {
		select {
		case <-a.ProcessDone():
			stop()
		case <-runCtx.Done():
		}
	}()
	go func() {
		body, err := a.PostDaemon(runCtx, a.nativeMessageRoute(receipt.SessionID, ""), raw)
		var message kiloNativeMessage
		if err == nil {
			err = json.Unmarshal(body, &message)
		}
		results <- kiloPromptResult{message: message, err: err}
	}()

	ticker := a.Clock().NewTicker(nativeAdmissionInterval)
	defer ticker.Stop()
	for {
		stored, err := a.readNativeMessage(ctx, receipt.SessionID, messageID)
		if err == nil && stored.Info.Role == "user" {
			go func() {
				defer stop()
				a.finishNativePrompt(target, messageID, <-results, finish)
			}()
			return nil
		}
		select {
		case result := <-results:
			stop()
			if result.err == nil && result.message.isAssistantReply(receipt.SessionID, messageID) {
				go a.finishNativePrompt(target, messageID, result, finish)
				return nil
			}
			a.nativeReplies.remove(messageID)
			if result.err == nil {
				result.err = errors.New("the daemon returned no matching reply")
			}
			return fmt.Errorf("%w: Kilo did not confirm the input: %v", agent.ErrDeliveryUncertain, result.err)
		case <-ctx.Done():
			stop()
			a.nativeReplies.remove(messageID)
			return fmt.Errorf("%w: Kilo did not confirm the input: %v", agent.ErrDeliveryUncertain, ctx.Err())
		case <-ticker.C:
		}
	}
}
