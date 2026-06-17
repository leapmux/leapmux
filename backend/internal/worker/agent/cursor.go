package agent

import (
	"context"
	"math"
	"strconv"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const (
	CursorCLIModeAgent = "agent"
	CursorCLIModePlan  = "plan"
	CursorCLIModeAsk   = "ask"

	cursorCLIModelAuto     = "auto"
	cursorCLIModelAutoWire = "default[]"
)

// CursorCLIAgent manages a single Cursor CLI ACP process.
type CursorCLIAgent struct {
	acpBase
}

// StartCursorCLI starts a Cursor CLI ACP agent process and performs the handshake.
func StartCursorCLI(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	return acpStart(ctx, opts, sink, acpStartSpec[CursorCLIAgent]{
		provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		providerName: "cursor",
		binaryName:   "cursor-agent",
		baseArgs:     []string{"acp"},
		newAgent:     func() *CursorCLIAgent { return &CursorCLIAgent{} },
		base:         func(a *CursorCLIAgent) *acpBase { return &a.acpBase },
		configure: func(a *CursorCLIAgent) {
			// Cursor stores the normalized (display) model id, not the wire form. The
			// live normalizer is sourced from the registry (the same one NormalizeModelID
			// uses) so the offline-label and live paths can't diverge.
			a.model = normalizeCursorModelID(opts.Model())
			a.modelIDNormalizer = modelIDNormalizerFor(leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR)
			// Cursor writes models through setCursorModel (id -> wire form); the base
			// UpdateSettings / reapply / refresh use this via effectiveSetModel, so Cursor
			// needs no overrides of its own.
			a.modelSetter = a.setCursorModel
			a.modelDecorator = decorateCursorModel
			a.modeChannel = modeChannelPermissionMode
			a.extraMethod = a.handleExtraMethod
		},
		afterHandshake: func(a *CursorCLIAgent, handshake *acpSessionResult, opts Options) error {
			return a.applyPermissionModeStartup(handshake, opts, CursorCLIModeAgent, normalizeCursorModelID(opts.Model()))
		},
	})
}

func normalizeCursorModelID(model string) string {
	if model == cursorCLIModelAutoWire {
		return cursorCLIModelAuto
	}
	return model
}

// cursorModelBracketParams extracts the key=value metadata Cursor bakes into a model
// id's trailing brackets, e.g. "claude-fable-5[thinking=true,context=300k,effort=high]"
// -> {thinking:true, context:300k, effort:high}. Returns nil when there is no bracket
// or it is empty (e.g. "default[]"). The bracketed id IS the wire id Cursor expects, so
// callers parse it for display metadata without rewriting the id.
func cursorModelBracketParams(id string) map[string]string {
	open := strings.IndexByte(id, '[')
	if open < 0 || !strings.HasSuffix(id, "]") {
		return nil
	}
	inner := id[open+1 : len(id)-1]
	if inner == "" {
		return nil
	}
	params := make(map[string]string)
	for _, pair := range strings.Split(inner, ",") {
		if k, v, ok := strings.Cut(pair, "="); ok && strings.TrimSpace(k) != "" {
			params[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return params
}

// parseCursorContextWindow parses a Cursor context value like "300k"/"272k"/"200000"
// into a token count, or 0 when unparseable.
func parseCursorContextWindow(v string) int64 {
	if v == "" {
		return 0
	}
	mult := int64(1)
	switch v[len(v)-1] {
	case 'k', 'K':
		mult, v = 1000, v[:len(v)-1]
	case 'm', 'M':
		mult, v = 1_000_000, v[:len(v)-1]
	}
	n, err := strconv.ParseFloat(v, 64)
	// ParseFloat accepts "inf"/"nan"; int64(±Inf) and int64(NaN) are
	// implementation-defined, so reject non-finite values rather than surfacing a
	// garbage context window in the picker.
	if err != nil || n <= 0 || math.IsInf(n, 0) || math.IsNaN(n) {
		return 0
	}
	// A finite but out-of-int64-range value (an absurd server-reported context like
	// "99999999999999999999k") also converts to an implementation-defined garbage int64
	// (it saturates to MaxInt64 on arm64, wraps to MinInt64 on amd64), so reject it too.
	// float64(math.MaxInt64) rounds up to 2^63, so >= catches everything that overflows.
	scaled := n * float64(mult)
	if scaled >= float64(math.MaxInt64) {
		return 0
	}
	return int64(scaled)
}

// decorateCursorModel surfaces the metadata Cursor bakes into a model id (which the
// server reports only inside the opaque bracketed id, not in the model's name) as the
// ModelInfo's ContextWindow and a human-readable Description, so the picker shows the
// effort / reasoning / extended-thinking / context window each variant carries. It also
// replaces the server's bare-id model name with a friendly display name.
func decorateCursorModel(m *ModelInfo) {
	// Cursor's server reports a model's name as the bare bracket-less id
	// ("composer-2.5", "claude-opus-4-8"); humanize it into a friendly display name when
	// the server didn't already supply a better one (it does only for "Auto"). Done
	// before the params early-return so bracket-less variants ("gemini-3.1-pro[]") are
	// humanized too.
	humanized := false
	if bare := stripModelIDBrackets(m.Id); m.DisplayName == "" || strings.EqualFold(m.DisplayName, bare) {
		m.DisplayName = humanizeModelID(m.Id)
		humanized = true
	}
	params := cursorModelBracketParams(m.Id)
	if len(params) == 0 {
		return
	}
	if cw := parseCursorContextWindow(params["context"]); cw > 0 {
		m.ContextWindow = cw
	}
	// Append the variant's distinguishing attribute to the display name so two variants of
	// the same base model don't collapse to identical picker labels: the reasoning-effort
	// level when present ("GPT 5.5" -> "GPT 5.5 Medium"), else the extended-thinking or fast
	// flag ("Composer 2.5" -> "Composer 2.5 Fast"). Only an AUTO-humanized name is suffixed
	// -- a real server-provided name is trusted to already disambiguate, so it is left as-is
	// (no "Composer 2.5 (Fast) Fast"). The fuller form stays in the tooltip Description below.
	if humanized {
		if suffix := cursorModelNameSuffix(params); suffix != "" {
			m.DisplayName += " " + suffix
		}
	}
	var parts []string
	if params["thinking"] == "true" {
		parts = append(parts, "Extended thinking")
	}
	// Cursor reports a model's reasoning-effort level under "effort" (Claude) or "reasoning"
	// (GPT) -- the same concept (cursorReasoningLevel), mutually exclusive in practice. Show it
	// ONCE, preferring "effort", so this tooltip can't disagree with the name suffix, which
	// also collapses the two keys via cursorReasoningLevel. (A model reporting both -- which
	// would contradict the same-concept assumption -- then renders consistently in both places.)
	if e := params["effort"]; e != "" {
		parts = append(parts, capitalizeFirst(e)+" effort")
	} else if r := params["reasoning"]; r != "" {
		parts = append(parts, capitalizeFirst(r)+" reasoning")
	}
	if params["fast"] == "true" {
		parts = append(parts, "Fast")
	}
	if len(parts) == 0 {
		return
	}
	suffix := strings.Join(parts, " · ")
	if m.Description != "" {
		m.Description += " · " + suffix
	} else {
		m.Description = suffix
	}
}

// cursorReasoningLevel returns a model's reasoning-effort level from whichever bracket
// key Cursor used -- "effort" on Claude models, "reasoning" on GPT models -- which are
// the same concept. Returns "" when neither is present.
func cursorReasoningLevel(params map[string]string) string {
	if level := params["effort"]; level != "" {
		return level
	}
	return params["reasoning"]
}

// cursorModelNameSuffix returns the short distinguishing suffix for a model variant's
// display name, preferring the reasoning-effort level (cased like cursorEffortLabel),
// then the extended-thinking flag, then the fast flag. Returns "" when the variant carries
// no distinguishing attribute. Keeps variants of the same base model from rendering as
// identical labels in the picker; the fuller attribute list lives in the Description.
func cursorModelNameSuffix(params map[string]string) string {
	if level := cursorReasoningLevel(params); level != "" {
		return cursorEffortLabel(level)
	}
	if params["thinking"] == "true" {
		return "Thinking"
	}
	if params["fast"] == "true" {
		return "Fast"
	}
	return ""
}

// cursorEffortLabel renders a Cursor reasoning-effort level for the model-name suffix,
// casing the compound "xhigh" nicely as "XHigh"; every other level just capitalizes.
func cursorEffortLabel(level string) string {
	if strings.EqualFold(level, "xhigh") {
		return "XHigh"
	}
	return capitalizeFirst(level)
}

func cursorModelIDForWire(model string) string {
	if model == cursorCLIModelAuto {
		return cursorCLIModelAutoWire
	}
	return model
}

func fallbackCursorCLIModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: CursorCLIModeAgent, Name: "Agent"},
		{Id: CursorCLIModePlan, Name: "Plan"},
		{Id: CursorCLIModeAsk, Name: "Ask"},
	}
}

func (a *CursorCLIAgent) setCursorModel(model string) error {
	// Send the wire id but store the normalized (display) id, so b.model never
	// transiently holds the wire form "default[]" (see setModelViaConfigOption).
	if err := a.setModelViaConfigOption(cursorModelIDForWire(model)); err != nil {
		return err
	}
	a.mu.Lock()
	a.model = model
	a.mu.Unlock()
	return nil
}

var cursorCLIAvailableModels = []*ModelInfo{
	{Id: cursorCLIModelAuto, DisplayName: "Auto", Description: "Automatically selects the best available Cursor model", IsDefault: true},
}

func init() {
	registerAgentFactory(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		StartCursorCLI,
		cursorCLIAvailableModels,
		staticSecondaryGroup(modeChannelPermissionMode, fallbackCursorCLIModes()),
		"LEAPMUX_CURSOR_DEFAULT_MODEL",
		"",
		"cursor-agent",
	)
	setModelIDNormalizer(leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, normalizeCursorModelID)
}
