package agent

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
)

// Claude Code permission mode values.
const (
	PermissionModeDefault           = "default"
	PermissionModePlan              = "plan"
	PermissionModeAcceptEdits       = "acceptEdits"
	PermissionModeBypassPermissions = "bypassPermissions"
	PermissionModeDontAsk           = "dontAsk"
	PermissionModeAuto              = "auto"
)

// autoModeUnavailableErrorPrefix is the prefix of the error message Claude
// Code returns when set_permission_mode:auto is rejected (regardless of
// reason: admin settings, plan circuit-breaker, or unsupported model).
const autoModeUnavailableErrorPrefix = "Cannot set permission mode to auto"

// claudeCodeControlResult holds the outcome of a pending control request.
type claudeCodeControlResult struct {
	Success               bool
	Mode                  string
	Error                 string
	OutputStyle           string
	AvailableOutputStyles []string
	FastModeState         string // "off", "cooldown", "on", or "" (unavailable)
	// Models / UnavailableModels carry the model catalog from the initialize
	// response (see claudeCodeModelInfo). UnavailableModels are the
	// visible-but-not-selectable entries the CLI reports separately; we drop
	// them during conversion. Note: Claude Code only emits unavailable_models to an
	// allowlisted first-party host entrypoint (currently the VS Code extension), so
	// for LeapMux's "cli" entrypoint this is effectively always empty -- the dynamic
	// catalog drops only `disabled` rows. Parsing it anyway keeps us correct if that
	// host allowlist ever widens.
	Models            []claudeCodeModelInfo
	UnavailableModels []claudeCodeModelInfo
	RawResponse       json.RawMessage
}

// claudeCodeModelInfo is one entry of the `models` array in the Claude Code
// initialize control response (SDK schema ModelInfoSchema). Only the fields
// LeapMux consumes are decoded; adaptive-thinking/fast-mode/auto-mode flags are
// derived separately (modelSupportsAdaptiveThinking) and omitted here.
type claudeCodeModelInfo struct {
	Value                 string   `json:"value"`                 // id passed to --model / set_model (e.g. "opus[1m]"); "default" is an alias sentinel
	DisplayName           string   `json:"displayName"`           // e.g. "Opus (1M context)"
	Description           string   `json:"description"`           // capability blurb shown on hover
	SupportsEffort        bool     `json:"supportsEffort"`        // false ⇒ no effort selector (e.g. Haiku)
	SupportedEffortLevels []string `json:"supportedEffortLevels"` // CLI levels, weakest→strongest: low|medium|high|xhigh|max
	Disabled              bool     `json:"disabled"`              // visible but not selectable; dropped during conversion
}

// Option keys for Claude Code option groups.
const (
	ClaudeOptionOutputStyle    = "outputStyle"
	ClaudeOptionFastMode       = "fastMode"
	ClaudeOptionAlwaysThinking = "alwaysThinkingEnabled"
)

// Extended Thinking option IDs. Claude Code only exposes a single
// alwaysThinkingEnabled boolean — it picks thinking.type:"adaptive" vs
// "enabled" per-model — so there is nothing to store beyond on/off. The
// UI label for the "on" option is set per-model in AvailableOptionGroups
// ("Adaptive" for Opus/Sonnet, "On" for Haiku).
const (
	AlwaysThinkingOn  = "on"
	AlwaysThinkingOff = "off"
)

// Fast Mode option IDs.
const (
	FastModeOn  = "on"
	FastModeOff = "off"
)

// ClaudeCodeAgent manages a single Claude Code process.
type ClaudeCodeAgent struct {
	processBase // shared process lifecycle (Stop, Wait, Stderr, etc.)

	model      string
	effort     string
	workingDir string
	homeDir    string
	sink       OutputSink

	// Claude Code-specific state.
	contextUsage           *contextUsageSnapshot
	lastAgentStatus        string
	thirdPartyFromSettings bool // third-party LLM provider detected from settings at startup

	pendingControlMu        sync.Mutex
	pendingControl          map[string]chan<- claudeCodeControlResult
	confirmedPermissionMode string
	// deferredPermissionModeReqID is the request_id of the LATEST set_permission_mode toggle
	// whose ack the CLI deferred (it holds the response until the active turn ends). Guarded by
	// a.mu, alongside confirmedPermissionMode. claudeCodeHandleControlResponse folds back ONLY
	// the deferred ack whose request_id matches this, so a stale/duplicate ack -- or an earlier
	// toggle's ack arriving after a newer toggle superseded it -- can't clobber the confirmed
	// mode. Tracking only the latest (a later toggle overwrites it) means a superseded toggle's
	// ack is ignored, leaving the mode the user last asked for. Empty when nothing is pending.
	deferredPermissionModeReqID string

	// Settings state from initialize response and runtime updates.
	outputStyle           string
	availableOutputStyles []string
	fastMode              string // "on" / "off"
	alwaysThinking        string // "on" / "off"
	autoModeAvailable     bool

	// availableModels is the model catalog discovered from the initialize
	// response. It is written only during StartClaudeCode's pre-registration
	// startup handshake (convertClaudeModels, then a possible ensureSettledModelListed
	// insert) and never mutated afterward, so reads are safe without a.mu (callers may
	// already hold it). nil/empty ⇒ fall back to the static claudeCodeAvailableModels
	// catalog.
	availableModels []*ModelInfo
}

// StartClaudeCode spawns a new Claude Code process and begins reading its output.
// The sink receives parsed output events via the Agent.HandleOutput method.
//
// Claude Code with --input-format stream-json does not produce any output
// (including the init message) until it receives input on stdin. Therefore,
// StartClaudeCode returns immediately without waiting for output. The session ID is
// extracted later from the init message when the first user message triggers
// output from Claude.
func StartClaudeCode(ctx context.Context, opts Options, sink OutputSink) (*ClaudeCodeAgent, error) {
	TraceStartupPhase(opts.AgentID, "claude_begin")
	ctx, cancel := context.WithCancel(ctx)

	// Check Claude Code settings files for third-party LLM provider env vars.
	// If detected, we omit --model/--effort entirely (simple command).
	// If not detected, we use a conditional shell command that checks env
	// vars at runtime (the user may have them in their shell profile).
	thirdPartyFromSettings := detectThirdPartyProvider(opts.HomeDir, opts.WorkingDir)

	baseArgs := []string{
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--permission-prompt-tool", "stdio",
		"--setting-sources", "user,project,local",
		// Emit summarized thinking text in thinking blocks; without this,
		// thinking blocks arrive with an empty `thinking` field.
		"--thinking-display", "summarized",
	}

	if opts.ResumeSessionID != "" {
		baseArgs = append(baseArgs, "--resume", opts.ResumeSessionID)
	}

	// opts.Model() is the raw stored/operator-default value, which may be a legacy
	// or fully-qualified id (a persisted "opus", a "claude-opus-4-8" from
	// LEAPMUX_CLAUDE_DEFAULT_MODEL). Canonicalize it up front so both the --model
	// arg we forward and the initial a.model live in the same alias space the
	// catalog and post-init refresh use -- otherwise launch would forward a bare
	// "opus"/fully-qualified id and a.model would read it raw until the first
	// get_settings refresh corrected it.
	launchModel := normalizeClaudeCodeModel(opts.Model())

	var modelEffortArgs []string
	if !thirdPartyFromSettings {
		// The dynamic catalog isn't known until the initialize response
		// arrives (below), so launch-time effort resolution uses the static
		// catalog. Fable and the other shipped models live there; a model
		// known only to a newer CLI downgrades ultracode→xhigh here and is
		// re-enabled post-init by buildStartupFlagSettings.
		modelEffortArgs = newEffortResolver(claudeCodeAvailableModels).buildModelEffortArgs(launchModel, opts.Effort())
	}

	// Always probe for a shell-profile third-party provider unless settings
	// already flagged one: a default-model launch sends no --model/--effort
	// (empty modelEffortArgs) but must still detect a provider configured in the
	// user's rc files so OptionGroups() can hide the model/effort UI.
	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(ctx, shellWrapSpec{
		Shell:           opts.Shell,
		LoginShell:      opts.LoginShell,
		BinaryName:      "claude",
		StripEnvKeys:    []string{"CLAUDECODE"},
		BaseArgs:        baseArgs,
		ModelEffortArgs: modelEffortArgs,
		ProbeThirdParty: !thirdPartyFromSettings,
		WorkingDir:      opts.WorkingDir,
	})

	cmd.Env = envutil.FilterEnv(cmd.Environ(), "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT")
	cmd.Env = append(cmd.Env, "CLAUDE_CODE_ENTRYPOINT=cli")
	if opts.LoginShell {
		// Set CLAUDECODE=1 so the user's shell rc files can detect they are
		// being sourced inside Claude Code and skip conflicting aliases.
		// The inner command unsets it before invoking claude.
		cmd.Env = append(cmd.Env, "CLAUDECODE=1")
	}
	cmd.Env = FinalizeAgentEnv(cmd.Env, opts)

	// setupProcessPipes configures SIGTERM cancel, WaitDelay, and opens
	// stdin/stdout/stderr pipes.
	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &ClaudeCodeAgent{
		processBase:            newProcessBase(opts, "claude", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix),
		model:                  launchModel,
		effort:                 opts.Effort(),
		workingDir:             opts.WorkingDir,
		homeDir:                opts.HomeDir,
		sink:                   sink,
		thirdPartyFromSettings: thirdPartyFromSettings,
		pendingControl:         make(map[string]chan<- claudeCodeControlResult),
		alwaysThinking:         AlwaysThinkingOn,
	}

	TraceStartupPhase(opts.AgentID, "before_exec_start")
	if err := a.startCmd(cmd, cancel); err != nil {
		return nil, err
	}
	TraceStartupPhase(opts.AgentID, "after_exec_start")

	// Drain stderr in a background goroutine.
	a.drainStderr(stderrPipe)

	// Read stdout in a background goroutine. Output will only arrive after
	// the first message is sent to stdin (Claude Code behavior with
	// --input-format stream-json).
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutputLoop(scanner)

	// cleanup terminates the agent process and waits for it to exit.
	// This ensures no orphaned process or goroutine is left behind.
	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}

	// Run the control-protocol startup handshake (initialize -> extract settings ->
	// permission mode -> apply persisted flag settings -> refresh). On a hard failure
	// tear down the just-spawned process so no orphan process or goroutine survives.
	if err := a.runStartupHandshake(ctx, opts); err != nil {
		cleanup()
		return nil, err
	}

	return a, nil
}

// runStartupHandshake drives the control-protocol exchange that must complete before
// StartClaudeCode hands back a usable agent: it sends initialize, captures the model
// catalog and the other settings the response carries, applies the permission mode and
// any persisted flag settings, then refreshes the stored settings from the CLI. It
// returns a formatted startup error on a hard failure (initialize / set_permission_mode);
// the caller tears the process down. Every field write here runs on the StartClaudeCode
// goroutine before the agent is registered with the manager -- the same lock-free
// pre-registration window buildStartupFlagSettings documents -- so no a.mu is taken.
func (a *ClaudeCodeAgent) runStartupHandshake(ctx context.Context, opts Options) error {
	timeout := opts.startupTimeout()

	// Send "initialize" as the first control request, matching the Agent SDK
	// protocol. This triggers Claude Code to emit the init system message
	// (which contains the session_id) and establishes the control protocol.
	TraceStartupPhase(opts.AgentID, "before_initialize")
	initResp, err := a.sendControlAndWait(ctx, `{"subtype":"initialize"}`, timeout)
	if err != nil {
		return a.formatStartupError("initialize", err)
	}
	TraceStartupPhase(opts.AgentID, "after_initialize")

	// Extract settings from the initialize response.
	if initResp.OutputStyle != "" {
		a.outputStyle = initResp.OutputStyle
	}
	a.availableOutputStyles = initResp.AvailableOutputStyles
	// Discover the model catalog from the initialize response. An empty result
	// (old CLI or parse failure) leaves a.availableModels nil and OptionGroups()'s
	// model projection falls back to the static catalog. For a third-party provider it
	// surfaces no model group regardless (the UI hides model/effort), but effortResolver() still
	// resolves over whatever the response carried (with the static fallback) so
	// effort/window resolution has a usable catalog.
	a.availableModels = convertClaudeModels(initResp.Models, initResp.UnavailableModels)
	if initResp.FastModeState == FastModeOn || initResp.FastModeState == "cooldown" {
		a.fastMode = FastModeOn
	} else {
		a.fastMode = FastModeOff
	}

	TraceStartupPhase(opts.AgentID, "before_permission_mode")
	resp, err := a.applyStartupPermissionMode(ctx, StringOrDefault(opts.PermissionMode(), PermissionModeDefault), timeout)
	if err != nil {
		return a.formatStartupError("set_permission_mode", err)
	}
	a.confirmedPermissionMode = resp.Mode
	TraceStartupPhase(opts.AgentID, "after_permission_mode")

	// Apply persisted options that differ from initialized defaults.
	if flagSettings := a.buildStartupFlagSettings(opts.Options); len(flagSettings) > 0 {
		if err := a.sendApplyFlagSettings(ctx, flagSettings, timeout); err != nil {
			slog.Warn("apply_flag_settings at startup failed", "agent_id", a.agentID, "error", err)
		}
	}
	// Refresh from the CLI once startup completes so the persisted effort
	// reflects the value the CLI actually picked (e.g. when we launched
	// with --effort omitted because Leapmux resolved the effort option to
	// "auto"). Run even if apply_flag_settings failed so the DB mirrors
	// the CLI's actual state rather than what we tried to set.
	a.refreshSettingsFromAgent(timeout)
	a.ensureSettledModelListed()
	return nil
}

// ensureSettledModelListed adds the settled model to the dynamic picker catalog when
// the CLI's selectable list omits it but the static fallback fully describes it. The
// account-default sentinel can resolve to a concrete model the CLI does NOT surface
// as a separately selectable row -- an account whose default is Opus, which Claude
// Code exposes only behind "default", is the motivating case. After
// refreshSettingsFromAgent settles a.model onto that concrete id, the picker
// (effortCatalog renders a.availableModels verbatim) would show no matching row,
// leaving the model unnamed in the trigger, unselected in the RadioGroup, and without
// an effort menu. Inserting the static-catalog entry fixes all three from the backend
// alone -- the frontend lookups (modelDisplayName, the model RadioGroup's current
// value, effortItems) all key on a.model and now find it.
//
// We inject ONLY a model the static catalog lists, never a synthesized placeholder
// for a model in neither catalog. A model in neither catalog is already "unknown" to
// effortResolver.definedEfforts (which scans both catalogs), and the effort/ultracode
// trust path deliberately TRUSTS an unknown model's CLI report rather than clamping it
// (see effortFromApplied / updateFlagSettings). Injecting an effort-less placeholder
// would flip definedEfforts to "known with no efforts", silently downgrading a session
// the CLI is running at ultracode/xhigh -- the exact relabel those methods guard
// against. A static-catalog model is the safe set: it is ALREADY known via the
// fallback, so injecting the same entry changes no resolver verdict; it only makes the
// picker show what the resolver could already speak to. A genuinely new model (one
// that postdates the static catalog and the CLI hides behind "default") stays unlisted
// -- the lesser evil -- until it is added to the static catalog, the way Fable was.
//
// Runs only from runStartupHandshake, on the StartClaudeCode goroutine in the
// pre-registration window: a.availableModels is written only here during startup
// (convertClaudeModels, then this), and the lock-free readers either run on this same
// goroutine (refreshSettingsFromAgent) or only on a later user turn, which happens-
// after the agent is registered. So no a.mu is needed and no concurrent reader exists.
//
// No-op when:
//   - the model is unresolved (empty, or still the literal sentinel because
//     get_settings degraded -- see refreshSettingsFromAgent);
//   - the dynamic list is empty (old CLI / parse failure): effortCatalog already
//     falls back to the static catalog, which lists every shipped model, and
//     appending one entry here would REPLACE that full fallback with a singleton;
//   - the settled model is already listed (the common case: the default resolved to
//     a listed model, or the user pinned one);
//   - the model is in neither catalog (see above -- left unlisted on purpose).
func (a *ClaudeCodeAgent) ensureSettledModelListed() {
	if UsesAccountDefaultModel(a.model) {
		return
	}
	if len(a.availableModels) == 0 || FindAvailableModel(a.availableModels, a.model) != nil {
		return
	}
	entry := FindAvailableModel(claudeCodeAvailableModels, a.model)
	if entry == nil {
		return
	}
	// Place the resolved model at its CANONICAL slot -- the position it holds in the
	// static catalog's most->least-powerful ordering (sentinel, Fable, Opus, Sonnet,
	// Haiku) -- rather than right after the sentinel. The CLI's own selectable list
	// already follows that ordering, so inserting the resolved model before the first
	// listed model that outranks it drops it exactly where the static catalog puts it:
	// opus[1m] (which the CLI hides behind "default") lands AFTER Fable, not jammed
	// between the sentinel and Fable. A naive "right after the sentinel" insert put a
	// resolved Opus ahead of Fable, contradicting the picker's documented order. Models
	// the static catalog doesn't know (a future dynamic-only id) rank last, so the
	// resolved static model sorts ahead of them. The inserted pointer is the shared
	// static-catalog entry, read only exactly as effortCatalog hands out
	// claudeCodeAvailableModels directly -- withDefaultModelMarked and
	// withModelGroupDefaultMarked both clone before touching it.
	rank := canonicalModelRank(a.model)
	insertAt := len(a.availableModels)
	for i, m := range a.availableModels {
		if canonicalModelRank(m.GetId()) > rank {
			insertAt = i
			break
		}
	}
	a.availableModels = slices.Insert(a.availableModels, insertAt, entry)
}

// canonicalModelRank returns modelID's index in the static claudeCodeAvailableModels
// catalog, whose order IS the canonical most->least-powerful picker ordering (the
// account-default sentinel first, then Fable, Opus, Sonnet, Haiku). A model the static
// catalog does not list (a future dynamic-only id) ranks last so it sorts after every
// catalog-known model. ensureSettledModelListed uses it to drop a resolved-but-unlisted
// model into its canonical slot instead of right after the sentinel.
func canonicalModelRank(modelID string) int {
	if i := slices.IndexFunc(claudeCodeAvailableModels, func(m *ModelInfo) bool {
		return m.GetId() == modelID
	}); i >= 0 {
		return i
	}
	return len(claudeCodeAvailableModels)
}

// buildStartupFlagSettings builds an apply_flag_settings payload for the option
// settings that differ from the initialized defaults.
//
// Reads a.effort/a.model without holding a.mu: this runs only from
// StartClaudeCode, before the agent is registered with the manager and thus
// before any concurrent UpdateSettings/refreshSettingsFromAgent can touch those
// fields, so the lock-free read is safe. Do not call it post-registration.
func (a *ClaudeCodeAgent) buildStartupFlagSettings(options map[string]string) map[string]interface{} {
	fs := map[string]interface{}{}
	maps.Copy(fs, a.reconcileStartupEffortFlags())
	if v := options[ClaudeOptionOutputStyle]; v != "" && v != a.outputStyle {
		fs[ClaudeOptionOutputStyle] = v
	}
	if v := options[ClaudeOptionFastMode]; v != "" && v != a.fastMode {
		fs[ClaudeOptionFastMode] = flagSettingOnOff(v)
	}
	if v := options[ClaudeOptionAlwaysThinking]; v != "" && v != a.alwaysThinking {
		fs[ClaudeOptionAlwaysThinking] = flagSettingThinking(v)
	}
	return fs
}

// reconcileStartupEffortFlags returns the apply_flag_settings needed to bring a
// freshly launched session's effort into agreement with the dynamic catalog, or nil
// for a third-party session (which has no model/effort UI -- see hidesModelEffortUI).
// The capability reconciliation itself lives on effortResolver (reconcileStartupFlags);
// this method applies the agent-level gate and hands the launch-time a.model/a.effort
// to the resolver it reconciles against.
func (a *ClaudeCodeAgent) reconcileStartupEffortFlags() map[string]interface{} {
	// A third-party session presents no model/effort UI and (when detected from
	// settings) launches with no --model/--effort; pushing effort/ultracode flags
	// would apply settings its user can neither see nor control, so leave it at the
	// CLI's own resolution. Gated on the same predicate AvailableModels uses so the
	// "hidden UI" and "no effort push" decisions can't drift.
	if a.hidesModelEffortUI() {
		return nil
	}
	return a.effortResolver().reconcileStartupFlags(a.model, a.effort)
}

// reconcileStartupFlags returns the apply_flag_settings that bring a freshly launched
// model+effort into agreement with this (dynamic) resolver. The effort was resolved
// at LAUNCH against the static catalog (the dynamic one wasn't known pre-init); the
// model/effort passed here still hold the launch values, since refreshSettingsFromAgent
// runs later. We reconstruct what launch sent as a launchEffortPlan (against the static
// catalog) and compare it against r (dynamic, with the static fallback that still
// recognizes a model the live CLI filtered out):
//
//   - Omitted: launch sent no --effort, so the CLI is at its own default. Usually
//     nothing to do -- except the S9 case handled by reconcileOmittedLaunch.
//   - Ultracode: the resolver confirms ultracode for the model, so complete the
//     combo (re-pin effortLevel:xhigh + ultracode:true) whether launch sent the
//     xhigh base (re-pin) or a lower one (upgrade). The launch path deferred the
//     ultracode boolean to here -- it is applied nowhere else -- so this also
//     re-enables a filtered model's ultracode the static fallback still vouches for.
//     Re-pinning is safe even if the live CLI no longer offers xhigh for the model:
//     Claude Code clamps an unsupported effortLevel to "high" at resolution and never
//     rejects apply_flag_settings (verified against the 2.1.170 binary), so an
//     over-pin degrades gracefully instead of stranding the session.
//   - Downgrade: the dynamic catalog runs the model at a different level than launch
//     sent (e.g. the live CLI dropped xhigh), so emit the corrected level and clear
//     ultracode defensively.
//
// Returns nil when launch and the dynamic resolution already agree (the common case)
// or when the model is unknown to both catalogs (nothing to reconcile against).
func (r effortResolver) reconcileStartupFlags(model, effort string) map[string]interface{} {
	plan := newEffortResolver(claudeCodeAvailableModels).planLaunch(model, effort)
	if plan.omitted {
		return r.reconcileOmittedLaunch(model, effort)
	}
	if _, known := r.definedEfforts(model); !known {
		// Unknown to both the dynamic catalog and the static fallback: we can't
		// reconcile against capabilities we don't have, so leave the session at
		// whatever --effort launch sent. (launchRunsUltracode implies known, so this
		// check can precede the ultracode/downgrade tail without dropping that case.)
		return nil
	}
	// Launch sent plan.level; emit only when the dynamic resolution differs from it.
	// The non-empty skipLevel also clears ultracode defensively on a downgrade.
	return r.reconciledEffortFlags(model, effort, plan.level)
}

// reconciledEffortFlags returns the apply_flag_settings that bring model/effort into
// agreement with this resolver: the xhigh+ultracode combo when the resolver confirms
// ultracode for the model, otherwise the resolved effortLevel. skipLevel suppresses
// emission when the resolved level already equals it (the startup "launch already
// sent this" no-op); pass "" to always emit a non-auto level (the omitted-launch
// path, which has no launch level to compare against). A non-empty skipLevel also
// means launch sent a level -- and so may have set an ultracode boolean a downgrade
// must undo -- so the effortLevel carries an explicit ultracode:false then; the
// omitted path (skipLevel "") sent no effort and so has no ultracode to clear.
// (clearUltracode is thus fully determined by skipLevel, not a separate knob.)
// Returns nil when there is nothing to apply (auto/empty resolution, or the level
// already matches skipLevel). Shared by reconcileStartupFlags and reconcileOmittedLaunch
// so the two can't drift on the ultracode-combo and auto-passthrough rules.
func (r effortResolver) reconciledEffortFlags(model, effort, skipLevel string) map[string]interface{} {
	if r.launchRunsUltracode(model, effort) {
		return ultracodeFlagSettings()
	}
	target := r.resolveEffort(model, effort)
	if target == "" || target == EffortAuto || target == skipLevel {
		return nil
	}
	fs := map[string]interface{}{"effortLevel": target}
	if skipLevel != "" {
		fs["ultracode"] = false
	}
	return fs
}

// reconcileOmittedLaunch handles the launch-omitted-effort case. Launch sends no
// --effort for the sentinel, EffortAuto/"" (the CLI keeps its own default -- nothing
// to reconcile), and a model the STATIC catalog considers effort-less. The last is
// the only omit that leaves a CONCRETE stored effort on a real model, so it is the
// only one that can disagree with the dynamic catalog: if the live CLI actually
// offers efforts for that model (S9 -- a model effort-less in the static catalog but
// effort-bearing in the dynamic one), apply the dynamic-resolved effort so the live
// selector and the running session agree instead of silently running the CLI default.
func (r effortResolver) reconcileOmittedLaunch(model, effort string) map[string]interface{} {
	if effort == "" || effort == EffortAuto || model == DefaultModelSentinel {
		return nil
	}
	efforts, known := r.definedEfforts(model)
	if !known || len(efforts) == 0 {
		// The dynamic catalog agrees the model is effort-less (or doesn't know it):
		// nothing to apply.
		return nil
	}
	// Launch sent no --effort, so there is no launch level to match against (skipLevel
	// ""), which also signals there is no launch-sent ultracode boolean to clear.
	return r.reconciledEffortFlags(model, effort, "")
}

// Interrupt aborts the current turn by sending the Claude Code
// interrupt control_request. This matches the wire format the
// frontend's buildInterruptRequest produced before the dedicated RPC,
// and the receiving claudeProvider.IsInterrupt detects:
//
//	{"type":"control_request","request_id":"...",
//	 "request":{"subtype":"interrupt"}}
//
// The request is best-effort: if the agent has already exited or is
// mid-stop the call returns the underlying error so the caller can
// surface it, but a no-active-turn agent won't fail — Claude Code
// silently ack's interrupts received outside a turn.
func (a *ClaudeCodeAgent) Interrupt() error {
	if a.IsStopped() {
		return fmt.Errorf("agent is stopped")
	}
	// Use the agent's own context so a process exit unblocks the
	// wait. APITimeout caps how long we hold the caller; the control
	// protocol itself is fast (single round-trip).
	_, err := a.sendControlAndWait(a.ctx, `{"subtype":"interrupt"}`, a.APITimeout())
	return err
}

// SendInput writes a user message to the agent's stdin.
func (a *ClaudeCodeAgent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.stopped {
		return fmt.Errorf("agent is stopped")
	}

	msg := UserInputMessage{
		Type: MessageTypeUser,
		Message: UserInputContent{
			Role: "user",
		},
	}

	if len(attachments) == 0 {
		// Plain text — backward compatible string content.
		msg.Message.Content = content
	} else {
		// Multimodal — build a content block array.
		blocks := buildClaudeContentBlocks(content, classifyAttachments(attachments))
		msg.Message.Content = blocks
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal input: %w", err)
	}

	data = append(data, '\n')
	if err := a.writeStdin(data); err != nil {
		return fmt.Errorf("write stdin: %w", err)
	}

	return nil
}

// buildClaudeContentBlocks converts text + classified attachments into Claude
// Code's content block format: text blocks, image blocks (base64), and document
// blocks (PDF).
func buildClaudeContentBlocks(content string, classified []classifiedAttachment) []interface{} {
	var blocks []interface{}
	if content != "" {
		blocks = append(blocks, map[string]interface{}{
			"type": "text",
			"text": content,
		})
	}
	for _, attachment := range classified {
		switch attachment.kind {
		case attachmentKindText:
			blocks = append(blocks, map[string]interface{}{
				"type": "text",
				"text": buildInlineTextAttachmentBlock(attachment),
			})
		case attachmentKindPDF:
			blocks = append(blocks, map[string]interface{}{
				"type": "document",
				"source": map[string]interface{}{
					"type":       "base64",
					"media_type": attachment.mimeType,
					"data":       base64.StdEncoding.EncodeToString(attachment.data),
				},
			})
		default:
			blocks = append(blocks, map[string]interface{}{
				"type": "image",
				"source": map[string]interface{}{
					"type":       "base64",
					"media_type": attachment.mimeType,
					"data":       base64.StdEncoding.EncodeToString(attachment.data),
				},
			})
		}
	}
	return blocks
}

// availableModelCatalog returns the Claude Code model/effort catalog projected
// into OptionGroups: the per-agent list discovered from the initialize response
// when present, else the static claudeCodeAvailableModels fallback (see
// effortCatalog). Returns nil when a third-party LLM provider is detected (from
// settings at startup, or the shell wrapper's can_change_model_and_effort=false
// metadata), which omits the model/effort groups so the frontend hides them.
//
// The returned slice and every ModelInfo/EffortInfo it points at are shared,
// immutable catalog data: the same effortTier* pointers back multiple model
// slices (both the static catalog and the converted dynamic list), so a mutation
// through any returned entry would corrupt every model that shares it. Callers
// MUST treat the result as read-only; copy before mutating.
func (a *ClaudeCodeAgent) availableModelCatalog() []*ModelInfo {
	if a.hidesModelEffortUI() {
		return nil
	}
	return a.effortCatalog()
}

// hidesModelEffortUI reports whether this session presents no model/effort UI: a
// third-party LLM provider detected from settings at startup, or one the shell
// wrapper flagged via can_change_model_and_effort=false. AvailableModels returns
// nil in that case (hiding the model/effort settings), and the startup effort
// reconcile is skipped, so a session whose user can neither see nor control effort
// is never pushed an effort/ultracode apply_flag_settings.
func (a *ClaudeCodeAgent) hidesModelEffortUI() bool {
	return a.thirdPartyFromSettings || a.preambleMetaValue("can_change_model_and_effort") == "false"
}

// effortCatalog returns the dynamic-first model list backing AvailableModels: the
// per-agent list discovered from the initialize response when present, else the
// static claudeCodeAvailableModels fallback. AvailableModels layers the third-party
// gate on top; effortCatalog itself is ungated, so that gate lives in one place.
//
// This is the picker view -- a whole-list dynamic-or-static swap that shows only the
// models the CLI reported. Effort/ultracode/context-window resolution does NOT use it:
// those go through effortResolver, which carries the static catalog as a PER-ENTRY
// fallback so a model the live CLI dropped from its list still resolves its
// capabilities and window.
//
// availableModels is written only during the pre-registration startup handshake and
// never mutated afterward, so this read is safe without a.mu.
func (a *ClaudeCodeAgent) effortCatalog() []*ModelInfo {
	if len(a.availableModels) > 0 {
		return a.availableModels
	}
	return claudeCodeAvailableModels
}

// OptionGroups returns every Claude configuration axis as config option
// groups: model and effort (omitted for third-party providers that hide
// model/effort UI), output style (when the CLI reports styles), fast mode,
// extended thinking, and the permission mode group (with "auto" filtered when
// the startup probe rejected it). Each group carries its confirmed current
// value; the manager re-derives the model group's default badge.
func (a *ClaudeCodeAgent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.mu.Lock()
	model, effort, mode := a.model, a.effort, a.confirmedPermissionMode
	outputStyle, fastMode, thinking := a.outputStyle, a.fastMode, a.alwaysThinking
	availStyles := a.availableOutputStyles
	autoAvail := a.autoModeAvailable
	a.mu.Unlock()

	var groups []*leapmuxv1.AvailableOptionGroup

	if catalog := a.availableModelCatalog(); len(catalog) > 0 {
		// Claude carries an extra per-model sub_group (extended thinking) beyond effort, so it
		// passes claudeModelSubGroups; otherwise this is the same model+effort projection Codex
		// and Pi use.
		groups = append(groups, modelAndEffortGroups(catalog, model, effort, EffortGroupLabel, claudeModelSubGroups)...)
	} else if model != "" {
		// Hidden model/effort UI (a third-party session, or can_change_model_and_effort
		// =false): there is no selectable catalog, but the session is running a concrete
		// model. Surface it (and a concrete effort, when set) as READ-ONLY groups so
		// `remote agent get`/list and the UI show what's running instead of a blank --
		// the model isn't user-changeable here, so the groups are non-mutable. The model
		// name is humanized (claudeFallbackDisplayName) so the readout matches the
		// selectable catalog's friendly names rather than showing the raw bracketed id.
		groups = append(groups, readOnlyModelAndEffortGroups(model, claudeFallbackDisplayName(model), effort)...)
	}

	if len(availStyles) > 0 {
		defs := make([]optDef, 0, len(availStyles))
		for _, s := range availStyles {
			defs = append(defs, optDef{Id: s, Name: titleCaseID(s, ""), Default: s == "default"})
		}
		groups = append(groups, selectGroup(ClaudeOptionOutputStyle, "Output Style", OptionOrderProviderThird, outputStyle, defs))
	}

	groups = append(groups, selectGroup(ClaudeOptionFastMode, "Fast Mode", OptionOrderProviderSecond, fastMode, []optDef{
		{Id: FastModeOn, Name: "On"},
		{Id: FastModeOff, Name: "Off", Default: true},
	}))

	groups = append(groups, thinkingGroupForModel(model, thinking))

	if pg := optionids.GroupByID(AvailableOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE), OptionIDPermissionMode); pg != nil {
		groups = append(groups, livePermissionModeGroup(pg, mode, autoAvail))
	}

	return groups
}

// thinkingGroupForModel builds the extended-thinking group for a model. The
// enabled option's id is always "on"; only its display label varies by model
// ("Adaptive" for models that pick thinking.type:"adaptive", "On" otherwise --
// see modelSupportsAdaptiveThinking), so a model switch must re-emit this group.
func thinkingGroupForModel(model, current string) *leapmuxv1.AvailableOptionGroup {
	enabledName := "On"
	if modelSupportsAdaptiveThinking(model) {
		enabledName = "Adaptive"
	}
	return selectGroup(ClaudeOptionAlwaysThinking, "Extended Thinking", OptionOrderProviderFirst, current, []optDef{
		{Id: AlwaysThinkingOn, Name: enabledName, Default: true},
		{Id: AlwaysThinkingOff, Name: "Off"},
	})
}

// claudeModelSubGroups is Claude's modelSubGroupsFunc: each model carries its
// effort group AND its extended-thinking group (whose enabled label is per
// model), so the frontend rebuilds both the instant the model selection changes
// rather than waiting for the relaunch a model switch triggers. The carried
// groups hold no current value (the frontend overlays the live selection); only
// their option lists and defaults are model-dependent.
func claudeModelSubGroups(m *ModelInfo) []*leapmuxv1.AvailableOptionGroup {
	groups := effortSubGroups(m)
	if m != nil {
		groups = append(groups, thinkingGroupForModel(m.Id, ""))
	}
	return groups
}

// livePermissionModeGroup builds a writable permission-mode group from the
// provider's static template, setting the confirmed current value and hiding
// "auto" when the startup probe rejected it (so the UI can't offer a mode this
// Claude Code instance can't enter).
func livePermissionModeGroup(static *leapmuxv1.AvailableOptionGroup, current string, autoAvail bool) *leapmuxv1.AvailableOptionGroup {
	if static != nil && !autoAvail {
		// Hide "auto" when the startup probe rejected it: filter the template (never mutate
		// the shared static group) so the UI can't offer a mode this Claude Code instance
		// can't enter. liveGroup then overlays the live current value and supplies the
		// id/label/default/order from the template.
		//
		// NEVER filter out the value the session is CURRENTLY in, though: if a live switch to
		// "auto" succeeded after a transient startup probe failure left autoAvail=false, the
		// confirmed current is "auto" while the probe result is stale. Dropping it would leave
		// CurrentValue="auto" with no matching option -- an off-spec current the frontend can't
		// render as selected (it clamps to the default, silently showing the wrong mode). Keeping
		// the current value selectable guarantees the "current is always an option" invariant
		// regardless of how autoAvail drifts, mirroring buildOptionGroup's injection for ACP.
		static = filterGroupOptions(static, func(o *leapmuxv1.AvailableOption) bool {
			return o.GetId() != PermissionModeAuto || o.GetId() == current
		})
	}
	return liveGroup(static, current)
}

// ultracodeFlagSettings is the apply_flag_settings payload that enables the
// xhigh+ultracode combo. "ultracode" is not a real --effort value: the CLI
// models it as the boolean settings key `ultracode` layered on top of
// effortLevel:"xhigh". Shared by the startup path (buildStartupFlagSettings)
// and the live path (claudeEffortFlagSettings) so the wire encoding -- in
// particular the "xhigh base" -- is defined in exactly one place. Returns a
// fresh map each call so callers can mutate it freely.
func ultracodeFlagSettings() map[string]interface{} {
	return map[string]interface{}{"effortLevel": EffortXHigh, "ultracode": true}
}

// claudeEffortFlagSettings returns the effortLevel/ultracode keys to merge into an
// apply_flag_settings payload to move curEffort -> newEffort. nil = no change.
//
// Selecting ultracode sends {effortLevel:"xhigh", ultracode:true} (see
// ultracodeFlagSettings); switching away from it sends the new level plus an
// explicit ultracode:false so the boolean is cleared.
func claudeEffortFlagSettings(newEffort, curEffort string) map[string]interface{} {
	if newEffort == "" || newEffort == EffortAuto || newEffort == curEffort {
		return nil
	}
	if newEffort == EffortUltracode {
		return ultracodeFlagSettings()
	}
	fs := map[string]interface{}{"effortLevel": newEffort}
	if curEffort == EffortUltracode {
		fs["ultracode"] = false // explicitly clear when leaving ultracode
	}
	return fs
}

// updateFlagSettings builds the effort/ultracode portion of a live
// apply_flag_settings payload for moving curEffort -> the requested newEffort on
// targetModel (the model the change lands on). It resolves newEffort against the
// model first, so an unsupported combo can't be pushed to the CLI.
//
// The additional clause beyond claudeEffortFlagSettings handles the model-only change:
// when newEffort is empty there is no effort delta, so claudeEffortFlagSettings
// returns nil and the CLI's per-session `ultracode` boolean would persist even
// after switching onto a model that can't run it (e.g. opus+ultracode ->
// sonnet). We force ultracode:false whenever the session is leaving ultracode for
// a KNOWN model whose catalog doesn't offer it, so the boolean never outlives the
// model that supports it. A model unknown to BOTH catalogs is exempted (the
// `known &&` gate): like the decode side, we trust the CLI's own ultracode report
// for a model the catalog can't speak to rather than downgrading a running session.
//
// Clearing the boolean alone is not enough: the CLI's apply_flag_settings treats
// model/effortLevel/ultracode as independent keys and does NOT re-resolve
// effortLevel on a model change (verified against the Claude Code 2.1.x binary --
// the handler is three separate `if (key in settings)` blocks, and the effective
// effort is `ultracode ? "xhigh" : effortLevel`). So a bare {model, ultracode:false}
// would leave effortLevel pinned at the ultracode base "xhigh" -- a level the new
// model may not support -- and the session would keep running at xhigh. When the
// caller requested no explicit effort (so claudeEffortFlagSettings left no
// effortLevel key), we therefore also pin the level to the target model's
// xhigh-resolved fallback, which mirrors the decode side (effortFromApplied
// falls back to the xhigh base when ultracode is cleared) and lands the live path
// on the same effort a relaunch would pick. resolveEffort downgrades
// xhigh to "high" for models that don't offer it (e.g. sonnet -> "high", its
// default), so the pinned level is always one the target model can run.
//
// The UI resets effort to auto on a model change and restarts (IsEffortAutoTransition
// short-circuits UpdateSettings before this runs), so today this whole branch only
// matters for non-UI/raw callers; it is defensive so such a caller can't strand an
// unsupported effortLevel on the live session.
func (r effortResolver) updateFlagSettings(targetModel, newEffort, curEffort string) map[string]interface{} {
	if targetModel == DefaultModelSentinel {
		// A session stuck on the unresolved account-default sentinel (the degraded path
		// where get_settings never echoed a concrete applied.model) has no concrete model
		// to resolve an effort against. Pushing an effortLevel here would pin a level the
		// CLI's actual resolved model may not support, so emit no effort delta -- the same
		// "the sentinel keeps the CLI's own resolution" stance the launch path takes by
		// omitting --effort. The effort settles once the model resolves.
		return nil
	}
	fs := claudeEffortFlagSettings(r.resolveEffort(targetModel, newEffort), curEffort)
	// Gate the ultracode strip on the model being KNOWN, mirroring effortFromApplied's
	// final guard (the `known &&` at the decode side): a model in NEITHER catalog
	// running ultracode is trusted as the CLI's own authoritative report (see
	// trustCLIUltracodeReport) -- the live path must not relabel it just because the
	// catalog can't confirm xhigh support. Without this guard supportsUltracode rejects
	// the unknown model, so unsupportedUltracode(curEffort, unknown) is true and an
	// unrelated/model-only update (newEffort=="") would silently push {ultracode:false,
	// effortLevel:"xhigh"} and downgrade a session the CLI is happily running at
	// ultracode -- directly contradicting the decode side that just trusted the same
	// model. The primary "leaving ultracode for a KNOWN unsupporting model" case
	// (opus+ultracode -> sonnet) still fires: sonnet is known.
	_, known := r.definedEfforts(targetModel)
	if known && r.unsupportedUltracode(curEffort, targetModel) {
		if fs == nil {
			fs = map[string]interface{}{}
		}
		fs["ultracode"] = false
		if _, ok := fs["effortLevel"]; !ok {
			fs["effortLevel"] = r.resolveEffort(targetModel, EffortXHigh)
		}
	}
	return fs
}

// effortFromApplied decodes the effort/ultracode pair that get_settings reports
// back into LeapMux's internal effort value -- the decode-side inverse of
// claudeEffortFlagSettings. The CLI reports an active ultracode session as
// effortLevel:"xhigh" plus ultracode:true, so applied.ultracode==true maps to the
// internal "ultracode" -- trusted as the CLI's authoritative report, EXCEPT for a
// model the catalog KNOWS lacks ultracode (Sonnet/Haiku), which is never promoted.
// A model the catalog does NOT know (e.g. one the CLI reported in unavailable_models,
// so convertClaudeModels filtered it from the dynamic catalog) is trusted too: the
// CLI is the authority on what it actually applied, so a running ultracode session
// is not relabeled to xhigh just because its model dropped out of the catalog. The
// account-default sentinel is the one unknown model NOT trusted here: a session
// stuck on the literal "default" (the CLI never echoed a concrete applied.model) has
// no real model behind it, so a CLI ultracode:true report passes the effort through
// (e.g. "xhigh") rather than minting a phantom "ultracode" against the placeholder. An
// unentitled session reports ultracode:false and we keep the reported level (e.g.
// "xhigh"). When ultracode is explicitly turned off but applied.effort is omitted,
// we fall back to ultracode's "xhigh" launch base instead of leaving a stale
// "ultracode". curEffort is retained when applied.effort is omitted or empty.
//
// applied.effort is reported as a concrete effort enum or null (get_settings
// sends `typeof effort === "string" ? effort : null`), never an empty string,
// so the `!= ""` guard below is purely defensive: a malformed/empty report
// retains curEffort rather than blanking the stored effort to "".
//
// The final guard catches the remaining mislabel path the switch can't: when the
// CLI omits the ultracode field entirely (ultracode == nil) a stale
// curEffort=="ultracode" would otherwise survive onto a model the catalog KNOWS
// can't run it (e.g. a model switch that didn't touch effort), so we clear it to
// the xhigh base. An unknown model is left alone here for the same trust reason.
func (r effortResolver) effortFromApplied(appliedEffort *string, ultracode *bool, curEffort, model string) string {
	effort := curEffort
	if appliedEffort != nil && *appliedEffort != "" {
		effort = *appliedEffort
	}
	_, known := r.definedEfforts(model)
	if ultracode != nil {
		switch {
		case *ultracode && r.trustCLIUltracodeReport(model, known):
			effort = EffortUltracode // overrides the "xhigh" reported in applied.effort
		case !*ultracode && effort == EffortUltracode:
			effort = EffortXHigh // ultracode cleared; fall back to its xhigh launch base
		}
	}
	if known && r.unsupportedUltracode(effort, model) {
		effort = EffortXHigh
	}
	return effort
}

// trustCLIUltracodeReport reports whether a CLI applied.ultracode==true report
// should be promoted to the internal EffortUltracode for this model. The CLI is
// the authority on what it actually applied, so we trust the report both for a
// model the catalog confirms supports ultracode AND for a model the catalog does
// NOT know (e.g. one the live CLI filtered into unavailable_models but that the
// session is still running) -- a running ultracode session is not relabeled to
// xhigh just because its model dropped out of the catalog. The one exception is
// the account-default sentinel: it has no concrete model behind it, so a session
// stuck on the literal "default" must not mint a phantom ultracode against the
// placeholder. `known` is effortFromApplied's definedEfforts verdict for `model`,
// reused here for the unknown-model short-circuit; the known-model case defers to
// supportsUltracode so the "the catalog advertises ultracode" membership test lives
// in exactly one place. This is the inverse of supportsUltracode on an unknown
// model (which rejects it) -- the CLI report is trusted where the catalog is silent.
func (r effortResolver) trustCLIUltracodeReport(model string, known bool) bool {
	return model != DefaultModelSentinel && (!known || r.supportsUltracode(model))
}

// UpdateSettings applies settings changes via the apply_flag_settings control
// request, avoiding a process restart. Returns true if the update was handled
// (or nothing changed), false if a restart is needed.
func (a *ClaudeCodeAgent) UpdateSettings(options optionmap.Map) bool {
	a.mu.Lock()
	curModel, curEffort := a.model, a.effort
	curPermissionMode := a.confirmedPermissionMode
	curOutputStyle, curFastMode, curThinking := a.outputStyle, a.fastMode, a.alwaysThinking
	availStyles := a.availableOutputStyles
	a.mu.Unlock()

	// Normalize the requested model to the same canonical id space a.model and the catalog
	// use, so a re-spelled-but-identical model (a CLI alias like "claude-opus-4-8[1m]" vs the
	// stored "opus[1m]") is recognized as unchanged -- no redundant apply_flag_settings, and
	// the effort resolver's per-model lookup (which keys on normalized catalog ids) can still
	// find the model to apply its ultracode-downgrade guard. The DefaultModelSentinel ("default")
	// passes through normalization unchanged, so the sentinel restart check below still fires.
	reqModel := normalizeClaudeCodeModel(options[OptionIDModel])
	reqEffort := options[OptionIDEffort]

	// Switching to EffortAuto can't be done live: the CLI doesn't accept
	// effortLevel="auto" for apply_flag_settings, and the only way to go
	// back to "let Claude pick" is to re-launch without --effort. Signal
	// the caller to restart instead.
	if IsEffortAutoTransition(reqEffort, curEffort) {
		return false
	}

	// Switching to the account-default sentinel can't be done live either:
	// apply_flag_settings stores the model string verbatim (it does NOT resolve
	// "default" the way set_model and the --model-omitted launch path do), so it
	// would strand the session on a bogus "default" model. Re-launch so startup
	// resolves it to the concrete model, mirroring the EffortAuto restart above.
	if reqModel == DefaultModelSentinel && reqModel != curModel {
		return false
	}

	flagSettings := map[string]interface{}{}

	if reqModel != "" && reqModel != curModel {
		flagSettings["model"] = reqModel
	}
	// Resolve the requested effort against the model it will run under (the new model
	// when this update also switches model) so a combined model+effort change can't
	// push an unsupported effort -- e.g. {model:"sonnet", ultracode:true} -- to the
	// CLI. The UI sends single-field updates, so this only bites non-UI/raw callers,
	// but it keeps the live path consistent with buildModelEffortArgs's launch-time
	// downgrade.
	targetModel := curModel
	if reqModel != "" {
		targetModel = reqModel
	}
	maps.Copy(flagSettings, a.effortResolver().updateFlagSettings(targetModel, reqEffort, curEffort))

	if v := options[ClaudeOptionOutputStyle]; v != "" && v != curOutputStyle {
		if !slices.Contains(availStyles, v) {
			return false
		}
		flagSettings[ClaudeOptionOutputStyle] = v
	}
	if v := options[ClaudeOptionFastMode]; v != "" && v != curFastMode {
		flagSettings[ClaudeOptionFastMode] = flagSettingOnOff(v)
	}
	if v := options[ClaudeOptionAlwaysThinking]; v != "" && v != curThinking {
		flagSettings[ClaudeOptionAlwaysThinking] = flagSettingThinking(v)
	}

	if len(flagSettings) > 0 {
		if err := a.sendApplyFlagSettings(a.ctx, flagSettings, a.APITimeout()); err != nil {
			slog.Error("apply_flag_settings failed", "agent_id", a.agentID, "error", err)
			return false
		}
	}

	if mode := options[OptionIDPermissionMode]; mode != "" && mode != curPermissionMode {
		if !a.applyPermissionModeLive(mode) {
			return false
		}
	}

	// Read back, persist, and broadcast the flag-settings result ONLY after every live apply has
	// landed -- NOT right after sendApplyFlagSettings above. This keeps a combined change
	// all-or-nothing: if the permission-mode apply fails and returns false for a restart, no
	// half-applied model/effort is broadcast (or folded into a.model/a.effort) first. The flag
	// settings were already pushed to the CLI; deferring only their read-back/broadcast leaves the
	// in-memory state consistent on the failure path, and the restart supersedes the live apply.
	if len(flagSettings) > 0 {
		a.refreshSettingsFromAgent(a.APITimeout())
	}
	return true
}

// applyPermissionModeLive applies a permission-mode change to the running CLI via
// set_permission_mode, returning false (the caller must restart) only on a hard failure. Caller
// holds NO lock. Extracted from UpdateSettings so the model/effort flag-settings build is no
// longer interleaved with the permission-mode RPC's three-way ack handling.
//
// The control request is capped well below APITimeout: idle, the CLI acks set_permission_mode in
// well under a second, but while a turn is streaming it defers the ack until the turn ends, so
// holding the caller (and the per-agent lifecycle lock) for the full APITimeout and then restarting
// would needlessly kill the in-flight turn.
func (a *ClaudeCodeAgent) applyPermissionModeLive(mode string) bool {
	resp, err := a.sendSetPermissionMode(a.ctx, mode, min(permissionModeApplyTimeout, a.APITimeout()))
	switch {
	case err == nil:
		a.mu.Lock()
		a.confirmedPermissionMode = resp.Mode
		// A live switch that LANDED on auto proves the session can enter it, so clear a stale
		// autoModeAvailable=false a transient startup probe failure may have left behind. Without
		// this, OptionGroups would keep filtering "auto" out of the picker even though the session
		// is running it. (livePermissionModeGroup still keeps the current value selectable as a
		// backstop, but updating the flag makes the catalog state accurate, not just self-correcting.)
		if resp.Mode == PermissionModeAuto {
			a.autoModeAvailable = true
		}
		a.mu.Unlock()
	case errors.Is(err, errControlTimeout):
		// The ack is deferred because a turn is in progress, but the CLI still queues
		// and applies the mode. Treat it as accepted-pending: record the requested mode
		// optimistically and return true so the caller persists it WITHOUT restarting
		// (which would abort the turn) or rolling the optimistic UI back. When the turn
		// ends the CLI sends the deferred control_response, which claudeCodeHandleControlResponse
		// folds back -- reconciling confirmedPermissionMode (and the persisted row) to the
		// mode the CLI actually applied if it differs from this optimistic value.
		slog.Info("set_permission_mode ack deferred (turn in progress); applying optimistically",
			"agent_id", a.agentID, "mode", mode)
		a.mu.Lock()
		a.confirmedPermissionMode = mode
		a.mu.Unlock()
	default:
		slog.Error("set_permission_mode failed", "agent_id", a.agentID, "mode", mode, "error", err)
		return false
	}
	return true
}

// refreshSettingsFromAgent sends get_settings and updates internal state with
// the actual applied values from Claude Code.
func (a *ClaudeCodeAgent) refreshSettingsFromAgent(timeout time.Duration) {
	resp, err := a.sendControlAndWait(a.ctx, `{"subtype":"get_settings"}`, timeout)
	if err != nil {
		slog.Warn("get_settings failed", "agent_id", a.agentID, "error", err)
		return
	}
	if len(resp.RawResponse) == 0 {
		return
	}

	var settings struct {
		Effective struct {
			OutputStyle           string `json:"outputStyle"`
			FastMode              *bool  `json:"fastMode"`
			AlwaysThinkingEnabled *bool  `json:"alwaysThinkingEnabled"`
		} `json:"effective"`
		Applied struct {
			Model     string  `json:"model"`
			Effort    *string `json:"effort"`
			Ultracode *bool   `json:"ultracode"`
		} `json:"applied"`
	}
	if err := json.Unmarshal(resp.RawResponse, &settings); err != nil {
		slog.Warn("get_settings response parse failed", "agent_id", a.agentID, "error", err)
		return
	}

	a.mu.Lock()
	if settings.Applied.Model != "" {
		// Settle a.model onto the concrete model the CLI resolved. Once the concrete
		// identity is known it -- not the "default" sentinel -- is what the tab selects,
		// persists, broadcasts, and resolves effort/window against: the sentinel is only
		// the pre-resolution placeholder and never reclaims precedence once a concrete
		// model exists (so a relaunch pins the concrete model rather than re-resolving
		// the account default -- intended). ensureSettledModelListed then surfaces this
		// model in the picker when the CLI's selectable list omitted it.
		a.model = normalizeClaudeCodeModel(settings.Applied.Model)
	} else if a.model == DefaultModelSentinel {
		// The account-default sentinel launches without --model and relies on
		// get_settings echoing the concrete model the CLI resolved. Claude Code 2.1.x
		// always populates applied.model with a concrete id -- get_settings computes it
		// eagerly via getMainLoopModel(), which resolves the account default and never
		// returns empty or the literal "default" (verified against the 2.1.170 binary).
		// So this branch is defense-in-depth against a malformed/forward-incompatible
		// response: an empty applied.model would otherwise strand a.model on the literal
		// "default" and leak it into persistence, the broadcast, and the settings-changed
		// notification. We can't synthesize the concrete model, so surface it.
		slog.Warn("get_settings omitted applied.model for the account-default launch; model stays unresolved",
			"agent_id", a.agentID)
	}
	// a.model is updated just above from applied.model, so the ultracode/model
	// gate inside effortFromApplied sees the model the CLI actually applied.
	// effortResolver reads a.availableModels lock-free, so it is safe to call here
	// while a.mu is held.
	a.effort = a.effortResolver().effortFromApplied(settings.Applied.Effort, settings.Applied.Ultracode, a.effort, a.model)
	if settings.Effective.OutputStyle != "" {
		a.outputStyle = settings.Effective.OutputStyle
	}
	// get_settings' `effective` is the CLI's MERGED settings map (verified by
	// disassembling the 2.1.170 binary: getSettings spreads PU().settings).
	// apply_flag_settings DELETES a key when sent null, so a flag cleared to its
	// default is ABSENT from `effective` and decodes here as a nil *bool. A nil thus
	// means "at the CLI default", NOT "unchanged": settle the field on that concrete
	// default rather than leaving the previous value stale. Otherwise turning Fast
	// Mode off (flagSettingOnOff sends null) or Extended Thinking on
	// (flagSettingThinking sends null) strands a.fastMode/a.alwaysThinking on the
	// prior setting, which then persists -- desyncing the settings-changed baseline
	// from the running session, so a later toggle compares against the wrong stored
	// value and its notification silently no-ops or shows a reversed transition.
	if settings.Effective.FastMode != nil && *settings.Effective.FastMode {
		a.fastMode = FastModeOn
	} else {
		a.fastMode = FastModeOff // nil == cleared to the CLI default (off)
	}
	if settings.Effective.AlwaysThinkingEnabled != nil && !*settings.Effective.AlwaysThinkingEnabled {
		a.alwaysThinking = AlwaysThinkingOff
	} else {
		a.alwaysThinking = AlwaysThinkingOn // nil == cleared to the CLI default (on)
	}
	model, effort := a.model, a.effort
	outputStyle, fastMode, thinking := a.outputStyle, a.fastMode, a.alwaysThinking
	a.mu.Unlock()

	slog.Info("agent settings refreshed",
		"agent_id", a.agentID,
		"model", model,
		"effort", effort,
		"outputStyle", outputStyle,
		"fastMode", fastMode,
		"alwaysThinking", thinking,
	)

	// get_settings does not report permission mode, so OMIT it from the refresh
	// map: an absent key preserves the stored DB value, including startup-time raw
	// set_permission_mode changes that are applied again after startup. model/
	// effort/outputStyle/fastMode/thinking are all concrete here, so they upsert.
	a.sink.PersistSettingsRefresh(map[string]string{
		OptionIDModel:              model,
		OptionIDEffort:             effort,
		ClaudeOptionOutputStyle:    outputStyle,
		ClaudeOptionFastMode:       fastMode,
		ClaudeOptionAlwaysThinking: thinking,
	})
}

// flagSettingOnOff maps an "on"/"off" string to a boolean flag setting value
// for apply_flag_settings. "on" → true, anything else → nil (which resets
// the flag to its default).
func flagSettingOnOff(v string) interface{} {
	if v == FastModeOn {
		return true
	}
	return nil
}

// flagSettingThinking maps an "on"/"off" string to the alwaysThinkingEnabled
// flag value for apply_flag_settings. "off" → false (thinking disabled).
// Anything else returns nil, which removes the key from flagSettings and
// lets Claude Code fall back to its default-on behavior — internally picking
// type:"adaptive" or type:"enabled" per its own model gate.
func flagSettingThinking(v string) interface{} {
	if v == AlwaysThinkingOff {
		return false
	}
	return nil
}

// normalizeClaudeCodeModel collapses the fully-qualified model ID that
// Claude Code's get_settings "applied.model" field returns (e.g.
// "claude-opus-4-7", "claude-haiku-4-5-20251001", "claude-sonnet-4-6[1m]")
// back to the short alias leapmux uses (opus[1m], sonnet, sonnet[1m], haiku).
// Short aliases pass through unchanged.
//
// Rules:
//   - Strip an optional "claude-" prefix.
//   - Preserve a trailing "[...]" bracket suffix (the 1M-context marker).
//   - Keep only the leading alphabetic token (opus/sonnet/haiku), dropping
//     version numbers (e.g. "-4-7") and date suffixes (e.g. "-20251001").
//   - Fable and Opus ship only as 1M-context models, so every spelling of
//     either collapses to "fable[1m]" / "opus[1m]" regardless of suffix --
//     bare "opus" (the legacy standard-context alias the CLI no longer lists)
//     is canonicalized to "opus[1m]" like every other Opus spelling.
func normalizeClaudeCodeModel(model string) string {
	if model == "" {
		return ""
	}
	// Lowercase first so a mixed-case CLI value (e.g. "OPUS[1M]", "Claude-Sonnet")
	// collapses to the same canonical alias the static catalog and a.model use; the
	// catalog id space is all lowercase, so an uppercased value would otherwise
	// never match its own entry.
	core := strings.TrimPrefix(strings.ToLower(model), "claude-")
	var suffix string
	if i := strings.IndexByte(core, '['); i >= 0 {
		suffix = core[i:]
		core = core[:i]
	}
	// The family alias is the first run of [a-z], AFTER skipping any leading non-alpha
	// (digits/hyphens). Family-first ids ("opus-4-6") have the family first, but a
	// version-first id ("3-5-sonnet") leads with numeric version tokens; skipping them
	// finds the family in either layout, where a from-position-0 scan returned "" for
	// the version-first shape and leaked the raw id (so a running version-first model
	// never matched its own catalog entry). A purely-numeric core ("123") still yields
	// "" and falls through to the raw-display fallback below.
	start := 0
	for start < len(core) {
		if c := core[start]; c >= 'a' && c <= 'z' {
			break
		}
		start++
	}
	end := start
	for end < len(core) {
		if c := core[end]; c < 'a' || c > 'z' {
			break
		}
		end++
	}
	alias := core[start:end]
	if alias == "" {
		// Unrecognized shape — return the original input unchanged so the
		// caller can still display it.
		return model
	}
	// Fable and Opus ship only as 1M-context models, and their canonical ids
	// carry the "[1m]" marker -- matching what the live CLI reports
	// ("claude-fable-5[1m]", "claude-opus-4-8[1m]"). There is no standard-context
	// Fable, and the standard-context Opus is a legacy id the live CLI no longer
	// lists, so every spelling of either (bare "fable"/"opus" from an operator
	// override or an older CLI listing, a fully-qualified value, an already-"[1m]"
	// id, or a "[1m-beta]"-style decoration) collapses to "<family>[1m]" so a
	// running Fable/Opus always matches its own catalog entry instead of splitting
	// into "<family>" vs "<family>[1m]". If a standard-context Opus is ever
	// reintroduced, this collapse must be revisited.
	if alias == "fable" || alias == "opus" {
		return alias + "[1m]"
	}
	return alias + suffix
}

// modelSupportsAdaptiveThinking reports whether Claude Code emits
// thinking.type:"adaptive" for this model (Opus and Sonnet) versus
// thinking.type:"enabled" with a budget (Haiku). Unknown model strings
// default to true to match Claude Code's first-party fallback. This is
// used purely to pick the "Adaptive" vs "On" display label in
// AvailableOptionGroups — the wire payload (alwaysThinkingEnabled) is
// identical either way.
//
// Expects the short alias (e.g. "opus", "haiku"). `a.model` is kept
// normalized after every refreshSettingsFromAgent call, so internal
// callers can pass it directly without re-normalizing.
func modelSupportsAdaptiveThinking(model string) bool {
	return !strings.HasPrefix(model, "haiku")
}

// Claude Code effort levels are model-dependent. Keep each slice ordered
// strongest → weakest so the RadioGroup renders in the same order. Descriptions
// mirror Claude Code's own effort copy (binary MP5()/ultracode strings) so the
// LeapMux selector reads identically to the CLI's /effort menu.

// Individual effort tiers, defined once and composed into the per-model slices
// below. Sharing one definition per tier keeps the descriptions single-sourced
// so a copy edit (like the one this list just received) can't drift between the
// Opus and Sonnet menus. The entries are immutable catalog data; the slices
// reference the same pointers (as opus and opus[1m] already share a whole slice).
//
// Because the pointers are shared, they MUST be treated as read-only after init:
// mutating one tier (e.g. its Description) would change it for every model slice
// that references it. OptionGroups()'s model projection hands these out without
// copying, so the read-only contract extends to its callers.
var (
	effortTierAuto      = &EffortInfo{Id: "auto", Name: "Auto", Description: "Let Claude decide the appropriate effort"}
	effortTierUltracode = &EffortInfo{Id: "ultracode", Name: "Ultracode", Description: "xhigh effort plus standing dynamic-workflow orchestration"}
	effortTierMax       = &EffortInfo{Id: "max", Name: "Max", Description: "Maximum capability with deepest reasoning"}
	effortTierXHigh     = &EffortInfo{Id: EffortXHigh, Name: "X-High", Description: "Deeper reasoning than high, just below maximum"}
	effortTierHigh      = &EffortInfo{Id: EffortHigh, Name: "High", Description: "Comprehensive implementation with extensive testing and documentation"}
	effortTierMedium    = &EffortInfo{Id: "medium", Name: "Medium", Description: "Balanced approach with standard implementation and testing"}
	effortTierLow       = &EffortInfo{Id: "low", Name: "Low", Description: "Quick, straightforward implementation with minimal overhead"}
)

// claudeEffortXHighMax is used by models that support both xhigh and max,
// plus the xhigh+ultracode combo (currently Opus).
var claudeEffortXHighMax = []*EffortInfo{
	effortTierAuto, effortTierUltracode, effortTierMax, effortTierXHigh, effortTierHigh, effortTierMedium, effortTierLow,
}

// claudeEffortMax is used by models that support max but not xhigh
// (currently Sonnet 4.6, older Opus).
var claudeEffortMax = []*EffortInfo{
	effortTierAuto, effortTierMax, effortTierHigh, effortTierMedium, effortTierLow,
}

// Claude context-window sizes. The CLI does not report a window, so we infer it
// from the model id (see claudeContextWindowForValue). Naming the two values
// single-sources the "[1m]-suffix ⇒ 1M, else 200K" rule shared by the static
// catalog entries, claudeContextWindowForValue, and the unresolved-sentinel
// fallback in extractAndBroadcastUsage.
const (
	claudeStandardContextWindow   = 200_000
	claudeOneMillionContextWindow = 1_000_000
)

// claudeCodeAvailableModels is the static model catalog. It is the source of
// truth for DefaultModel(provider) (registry defaultModels) and the fallback
// OptionGroups()'s model projection uses when the per-agent dynamic catalog is empty
// (old CLI, third-party provider, or parse failure). When the live CLI reports its
// own catalog, the dynamic list (convertClaudeModels) supersedes this.
//
// The leading DefaultModelSentinel entry is the IsDefault choice: a new tab (and
// any account, including non-Opus tiers) starts on it, and buildModelEffortArgs
// omits --model so the CLI resolves it to that account's concrete default --
// which get_settings then reports back, so the tab settles on the real model
// after startup. It carries no efforts: the effort menu appears once the
// concrete model is known, which also keeps a fresh launch from forwarding an
// --effort the resolved model may not support. The concrete models follow in
// most→least powerful order, matching Claude Code's own ordering ("Fable for the
// hardest problems, Opus for complex work, Sonnet for most tasks, Haiku for
// quick questions").
var claudeCodeAvailableModels = []*ModelInfo{
	{Id: DefaultModelSentinel, DisplayName: "Default (recommended)", Description: "Use your account's default model", IsDefault: true},
	// Fable 5 is 1M-context only; its canonical id carries the [1m] marker so it
	// matches the live CLI's "claude-fable-5[1m]" (normalizeClaudeCodeModel
	// collapses every Fable spelling, bare "fable" included, to "fable[1m]"). The
	// display name omits "(1M context)" -- there is no standard-context Fable to
	// distinguish it from.
	{Id: "fable[1m]", DisplayName: "Fable 5", Description: "Most powerful for the hardest problems", DefaultEffort: EffortXHigh, SupportedEfforts: claudeEffortXHighMax, ContextWindow: claudeOneMillionContextWindow},
	// opus is the legacy standard-context alias. normalizeClaudeCodeModel now
	// collapses every Opus spelling (bare "opus" included) to "opus[1m]", so no
	// path resolves to this entry by id anymore -- it is retained purely as a
	// Hidden, exact-match-only safety net for any un-normalized legacy id that
	// might still reach a FindAvailableModel lookup. FindAvailableModel matches
	// raw ids, so this entry can never shadow the selectable opus[1m] below.
	// Hidden from the picker: the live CLI no longer lists the standard-context
	// Opus -- only opus[1m] -- so the static fallback must not resurrect it as a
	// selectable option.
	{Id: "opus", DisplayName: "Opus", Description: "Most capable for complex work", DefaultEffort: EffortXHigh, SupportedEfforts: claudeEffortXHighMax, ContextWindow: claudeStandardContextWindow, Hidden: true},
	{Id: "opus[1m]", DisplayName: "Opus (1M context)", Description: "Most capable for complex work", DefaultEffort: EffortXHigh, SupportedEfforts: claudeEffortXHighMax, ContextWindow: claudeOneMillionContextWindow},
	{Id: "sonnet", DisplayName: "Sonnet", Description: "Best for everyday tasks", DefaultEffort: EffortHigh, SupportedEfforts: claudeEffortMax, ContextWindow: claudeStandardContextWindow},
	{Id: "sonnet[1m]", DisplayName: "Sonnet (1M context)", Description: "Best for everyday tasks", DefaultEffort: EffortHigh, SupportedEfforts: claudeEffortMax, ContextWindow: claudeOneMillionContextWindow},
	{Id: "haiku", DisplayName: "Haiku", Description: "Fastest for quick answers", ContextWindow: claudeStandardContextWindow},
}

// claudeEffortLevels lists the Claude Code CLI effort levels weakest->strongest,
// each paired with the shared AvailableEffort tier pointer. Its order IS the rank
// (claudeSupportedEfforts walks it in reverse for a strongest->weakest menu) and
// its membership defines which CLI levels we recognize, so ordering and tier
// mapping are single-sourced -- a new tier can't be added to one without the
// other (the previous parallel rank/tier maps could silently disagree, sorting an
// unmapped level as rank 0). Reusing the shared effortTier* pointers keeps the
// descriptions identical to the static catalog.
type claudeEffortLevel struct {
	level string
	tier  *EffortInfo
}

var claudeEffortLevels = []claudeEffortLevel{
	{"low", effortTierLow},
	{"medium", effortTierMedium},
	{EffortHigh, effortTierHigh},
	{EffortXHigh, effortTierXHigh},
	{"max", effortTierMax},
}

// recognizedClaudeEffortLevels returns the claudeEffortLevels entries the model
// supports, ordered strongest->weakest (the rank table walked in reverse), with
// levels the model doesn't list dropped. It single-sources the reverse rank walk
// shared by claudeSupportedEfforts (which maps the entries to tier pointers) and
// claudeDefaultEffort's fallback (which takes the strongest entry's level).
func recognizedClaudeEffortLevels(supported map[string]bool) []claudeEffortLevel {
	out := make([]claudeEffortLevel, 0, len(claudeEffortLevels))
	for i := len(claudeEffortLevels) - 1; i >= 0; i-- {
		if supported[claudeEffortLevels[i].level] {
			out = append(out, claudeEffortLevels[i])
		}
	}
	return out
}

// convertClaudeModels converts the model list from the Claude Code initialize
// response into LeapMux's AvailableModel catalog, mirroring Codex's
// queryAvailableModels: efforts are ordered strongest→weakest with an "auto"
// sentinel prepended, and the LeapMux-only "ultracode" tier is offered for any
// model whose CLI effort levels include xhigh (ultracode == xhigh + standing
// workflow orchestration, so xhigh support is the entitlement we key off).
//
// Entries the user can't actually select are dropped: disabled entries and
// anything reported in unavailable_models. The DefaultModelSentinel ("default")
// entry IS surfaced -- it is a real "let the CLI pick the account default"
// option (the model-side analogue of EffortAuto); buildModelEffortArgs omits
// --model for it and withDefaultModelMarked gives it the IsDefault badge.
// IsDefault is left unset here -- the manager applies it. Returns nil when
// models is empty so OptionGroups()'s model projection falls back to the static catalog.
//
// The returned entries reuse the shared effortTier* pointers, so the same
// read-only contract as claudeCodeAvailableModels applies (see OptionGroups).
func convertClaudeModels(models, unavailable []claudeCodeModelInfo) []*ModelInfo {
	if len(models) == 0 {
		return nil
	}
	skip := buildModelSkipSet(unavailable)
	out := make([]*ModelInfo, 0, len(models))
	seen := make(map[string]bool, len(models))
	sentinelSeen := false
	for _, m := range models {
		if m.Value == "" || m.Disabled {
			continue
		}
		// The account-default sentinel is identified by its RAW value ("default")
		// and OWNS the reserved DefaultModelSentinel id deterministically. Route it
		// before normalizing so its dedup can't race a concrete model that merely
		// normalizes to "default" (below); keep only the first occurrence. This also
		// precedes the unavailable_models (skip) filter -- the account default is
		// always selectable, so a "default" reported in unavailable_models is ignored
		// (a disabled:true sentinel IS dropped, via the m.Disabled guard above).
		if isDefaultSentinel(m.Value) {
			if !sentinelSeen {
				sentinelSeen = true
				out = append(out, convertClaudeModel(m, DefaultModelSentinel))
			}
			continue
		}
		// Normalize the CLI value into the same alias space a.model lives in
		// (refreshSettingsFromAgent stores normalizeClaudeCodeModel(applied.model)).
		// The CLI may report a fully-qualified value (e.g. "claude-fable-5[1m]")
		// for the account-resolved model; storing it verbatim would mean a running
		// model never matches its own catalog entry, breaking effort/ultracode
		// lookups and the frontend's effort selector and display name.
		id := normalizeClaudeCodeModel(m.Value)
		// A concrete model can't claim the sentinel's reserved id: launch
		// (buildModelEffortArgs) and the badge logic (defaultModelForList) treat
		// id=="default" as the sentinel, so a concrete model whose value merely
		// normalizes to "default" (e.g. "default5") would be mishandled as the
		// sentinel. Drop it -- deterministically, regardless of CLI ordering --
		// rather than letting it masquerade.
		if id == DefaultModelSentinel {
			continue
		}
		// Drop unavailable and duplicate entries, both keyed by normalized id: an
		// unavailable model reported under a different spelling than its models
		// entry is still filtered, and two entries that normalize to the same id
		// (or a malformed payload repeating a value) collapse to one rather than
		// rendering twice and making the catalog lookups (first match wins)
		// ambiguous.
		if skip[id] || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, convertClaudeModel(m, id))
	}
	return out
}

// buildModelSkipSet collects the normalized ids of unavailable_models so
// convertClaudeModels can filter them. Keying by normalized id, not raw value,
// matters because the models and unavailable_models arrays may spell the same
// model differently (one fully-qualified like "claude-fable-5[1m]", one aliased
// like "fable[1m]"); both collapse to the same id, so matching on the raw value
// could leak an unavailable model through.
func buildModelSkipSet(unavailable []claudeCodeModelInfo) map[string]bool {
	skip := make(map[string]bool, len(unavailable))
	for _, m := range unavailable {
		if m.Value == "" {
			continue
		}
		skip[normalizeClaudeCodeModel(m.Value)] = true
	}
	return skip
}

// convertClaudeModel converts one CLI model entry (already normalized to id) into
// an AvailableModel. IsDefault is left unset -- the manager applies it. The caller
// (convertClaudeModels) owns sentinel routing: it passes id == DefaultModelSentinel
// only for the genuine account-default entry (matched on the RAW value) and has
// already dropped any concrete model whose value merely normalizes to "default", so
// the id check below is exact -- no need to re-examine the raw value here.
func convertClaudeModel(m claudeCodeModelInfo, id string) *ModelInfo {
	displayName := m.DisplayName
	if displayName == "" {
		displayName = claudeFallbackDisplayName(id)
	}
	am := &ModelInfo{
		Id:          id,
		DisplayName: displayName,
		Description: m.Description,
	}
	// The account-default sentinel carries no efforts or context window: those
	// belong to the concrete model it resolves to, which isn't known until after
	// startup. This mirrors the static catalog's "default" entry and keeps a fresh
	// launch from forwarding an --effort/ultracode the resolved model may not
	// support (the CLI reports the sentinel WITH a full effort menu, which we
	// deliberately drop here).
	if id == DefaultModelSentinel {
		return am
	}
	supported := normalizedEffortLevelSet(m)
	am.DefaultEffort = claudeDefaultEffort(supported)
	am.SupportedEfforts = claudeSupportedEfforts(supported)
	am.ContextWindow = claudeContextWindowForValue(id)
	return am
}

// claudeFallbackDisplayName builds a display name from a normalized model id when
// the CLI omits one. It title-cases the alias and renders a 1M-context variant's
// bracket suffix as " (1M context)" -- matching the static catalog's "Opus (1M
// context)" rather than the raw "Opus[1m]" titleCaseID would otherwise produce.
// It detects the variant through is1MContextVariant (the single home for the "[1m]"
// marker) rather than a literal "[1m]" suffix, so a decorated spelling like
// "opus[1m-beta]" -- which claudeContextWindowForValue already sizes at 1M -- is
// named consistently instead of falling through to a garbled "Opus[1m Beta]".
func claudeFallbackDisplayName(id string) string {
	if is1MContextVariant(id) {
		// is1MContextVariant guarantees a '[' (and a trailing ']'), so the bracket
		// group is the suffix to strip; LastIndexByte cannot return -1 here.
		return titleCaseID(id[:strings.LastIndexByte(id, '[')], "") + " (1M context)"
	}
	return titleCaseID(id, "")
}

// isDefaultSentinel reports whether a raw CLI model value is the account-default
// sentinel. Case-insensitive so a "Default" spelling is still recognized; matched
// against the RAW value (not the normalized id) so a concrete model that merely
// normalizes to "default" keeps its own efforts (see convertClaudeModel).
func isDefaultSentinel(value string) bool {
	return strings.EqualFold(value, DefaultModelSentinel)
}

// normalizedEffortLevelSet returns the set of recognized-or-not CLI effort levels
// a model reports, lowercased so a mixed-case "XHigh" still matches. Returns nil
// when the model has no effort support (Haiku), which both claudeSupportedEfforts
// and claudeDefaultEffort read as "hide the selector / no default". Building it
// once lets both share a single scan of SupportedEffortLevels.
func normalizedEffortLevelSet(m claudeCodeModelInfo) map[string]bool {
	if !m.SupportsEffort {
		return nil
	}
	set := make(map[string]bool, len(m.SupportedEffortLevels))
	for _, lvl := range m.SupportedEffortLevels {
		set[strings.ToLower(lvl)] = true
	}
	return set
}

// claudeSupportedEfforts builds the AvailableEffort list from a model's level set
// (normalizedEffortLevelSet), reusing the shared tier pointers. Models without
// effort support, or whose reported levels we don't recognize at all, get no
// efforts -- which hides the effort selector rather than showing an auto-only stub.
// The "auto" sentinel always leads; "ultracode" follows when the model supports
// xhigh; the recognized CLI levels then follow strongest->weakest. The order comes
// from claudeEffortLevels (walked in reverse), not the CLI's reported order, so an
// unexpected ordering can't scramble the menu.
func claudeSupportedEfforts(supported map[string]bool) []*EffortInfo {
	recognized := recognizedClaudeEffortLevels(supported)
	// A model that claims effort support but lists only levels we don't recognize
	// has no usable menu, so hide the selector instead of emitting a lone "auto".
	if len(recognized) == 0 {
		return nil
	}
	efforts := make([]*EffortInfo, 0, len(recognized)+2)
	efforts = append(efforts, effortTierAuto)
	if supported[EffortXHigh] {
		efforts = append(efforts, effortTierUltracode)
	}
	for _, lvl := range recognized {
		efforts = append(efforts, lvl.tier)
	}
	return efforts
}

// claudeDefaultEffort picks a model's default effort from its level set. The CLI
// does not report one, so we prefer xhigh (Opus/Fable), else high (Sonnet) -- the
// product-chosen sweet spot, deliberately NOT merely the strongest level (max is
// overkill as a default for a model that also offers xhigh). A model with no
// effort support gets "" -- inert, since the effort selector is hidden and the
// launch path omits --effort for it.
func claudeDefaultEffort(supported map[string]bool) string {
	if supported[EffortXHigh] {
		return EffortXHigh
	}
	if supported[EffortHigh] {
		return EffortHigh
	}
	// Neither xhigh nor high is offered (e.g. a max-only model). Fall back to the
	// strongest recognized level the model does support so an effort-bearing model
	// never ends up with a non-empty selector but an empty default -- a state the
	// frontend's effortValueForModel would otherwise have to coerce. Returns ""
	// only when no level is recognized, matching claudeSupportedEfforts hiding the
	// selector.
	if recognized := recognizedClaudeEffortLevels(supported); len(recognized) > 0 {
		return recognized[0].level // strongest-first ordering
	}
	return ""
}

// claudeContextWindowForValue infers a model's context window from its id. The CLI
// does not report one; the only signal it exposes is the bracketed 1M-context marker
// (is1MContextVariant). Everything else uses Claude's standard 200K window. This
// follows the same suffix rule the concrete static-catalog entries use (the "default"
// sentinel is the exception -- it has no window until it resolves), so new models stay
// correct without a per-model table.
func claudeContextWindowForValue(value string) int64 {
	// Fable 5 is always 1M. Its canonical id is "fable[1m]" (caught by the suffix
	// rule below), but a raw, un-normalized "fable" can still reach here from an API
	// model id, so accept the bare alias too rather than mis-sizing it to 200K.
	if value == "fable" || is1MContextVariant(value) {
		return claudeOneMillionContextWindow
	}
	return claudeStandardContextWindow
}

// is1MContextVariant reports whether a model id carries the CLI's 1M-context marker: a
// bracketed suffix whose content begins with "1m" (case-insensitive). This matches the
// plain "[1m]" the CLI ships today and tolerates a decorated spelling ("[1M]",
// "[1m-beta]", "[1m-preview]") so a future labelling of the 1M beta still resolves to
// the larger window instead of silently reporting the 200K standard one. Anchored on a
// trailing bracket group, so a stray "1m" elsewhere in the id can't false-positive.
// This is THE single place that recognizes the marker -- widen it here, not at call
// sites, if the CLI changes the spelling.
func is1MContextVariant(id string) bool {
	open := strings.LastIndexByte(id, '[')
	if open < 0 || !strings.HasSuffix(id, "]") {
		return false
	}
	inner := strings.ToLower(id[open+1 : len(id)-1])
	return strings.HasPrefix(inner, "1m")
}

// sendControlAndWait sends a control request to the agent and waits for the
// response. The requestBody should be the JSON for the "request" field only
// (e.g. `{"subtype":"initialize"}`). Returns the control result or an error
// on timeout/cancellation/failure.
func (a *ClaudeCodeAgent) sendControlAndWait(ctx context.Context, requestBody string, timeout time.Duration) (claudeCodeControlResult, error) {
	_, resp, err := a.sendControlAndWaitWithID(ctx, requestBody, timeout)
	return resp, err
}

// sendControlAndWaitWithID is sendControlAndWait that also returns the generated request_id, so
// a caller that needs to correlate a LATE (deferred) control_response with the request it
// belongs to -- the set_permission_mode path, whose ack the CLI holds until an active turn
// ends -- can record that id. Most callers use sendControlAndWait and ignore it.
func (a *ClaudeCodeAgent) sendControlAndWaitWithID(ctx context.Context, requestBody string, timeout time.Duration) (string, claudeCodeControlResult, error) {
	requestID := generateRequestID()
	ch := make(chan claudeCodeControlResult, 1)
	a.registerPendingControl(requestID, ch)

	msg := fmt.Sprintf(`{"type":"control_request","request_id":"%s","request":%s}`, requestID, requestBody)
	if err := a.SendRawInput([]byte(msg)); err != nil {
		a.unregisterPendingControl(requestID)
		// A write failure almost always means the child closed its stdin —
		// i.e. it exited before we could hand off the request. Wait briefly
		// for the wait goroutine to finalize so callers see "agent process
		// exited with code N" (with captured stderr) instead of a raw
		// "broken pipe" symptom. This also removes a race in
		// TestAgent_EarlyExitDetected where, on fast Linux runners, the
		// subprocess exits before the initialize write reaches the pipe.
		select {
		case <-a.processDone:
			return requestID, claudeCodeControlResult{}, a.processExitError()
		case <-time.After(1 * time.Second):
			return requestID, claudeCodeControlResult{}, err
		}
	}
	TraceStartupPhase(a.agentID, "control_stdin_write")

	select {
	case resp := <-ch:
		a.unregisterPendingControl(requestID)
		if !resp.Success {
			return requestID, resp, fmt.Errorf("%s", resp.Error)
		}
		return requestID, resp, nil
	case <-a.processDone:
		a.unregisterPendingControl(requestID)
		return requestID, claudeCodeControlResult{}, a.processExitError()
	case <-ctx.Done():
		a.unregisterPendingControl(requestID)
		return requestID, claudeCodeControlResult{}, ctx.Err()
	case <-time.After(timeout):
		a.unregisterPendingControl(requestID)
		return requestID, claudeCodeControlResult{}, errControlTimeout
	}
}

// errControlTimeout is returned by sendControlAndWait when the agent does not respond to a
// control request within the timeout. It is a sentinel (its message is unchanged) so the
// live permission-mode path can tell a deferred ack -- the CLI holds the set_permission_mode
// response until an active turn ends -- from a genuine failure via errors.Is.
var errControlTimeout = errors.New("timeout waiting for agent to respond")

// sendApplyFlagSettings marshals flagSettings into an apply_flag_settings
// control request and sends it, returning the control error (if any). The
// envelope shape lives here so the startup path (StartClaudeCode) and the live
// path (UpdateSettings) don't each hand-roll the same JSON literal.
func (a *ClaudeCodeAgent) sendApplyFlagSettings(ctx context.Context, flagSettings map[string]interface{}, timeout time.Duration) error {
	body, _ := json.Marshal(map[string]interface{}{
		"subtype":  "apply_flag_settings",
		"settings": flagSettings,
	})
	_, err := a.sendControlAndWait(ctx, string(body), timeout)
	return err
}

// applyStartupPermissionMode sets the agent's permission mode during startup
// while also detecting whether auto mode is available for this session.
// a.autoModeAvailable is updated as a side effect; the returned result
// reflects the mode actually applied (which may be default if the requested
// auto mode was rejected with autoModeUnavailableErrorPrefix).
//
// When requested == auto a single set_permission_mode call serves both
// purposes; otherwise auto is probed first (leaving the session briefly in
// auto on success) before the requested mode is applied to restore the
// intended state. Transient probe errors are treated as unavailable so the
// UI does not offer a mode the agent cannot enter.
func (a *ClaudeCodeAgent) applyStartupPermissionMode(ctx context.Context, requested string, timeout time.Duration) (claudeCodeControlResult, error) {
	if requested == PermissionModeAuto {
		resp, err := a.sendSetPermissionMode(ctx, PermissionModeAuto, timeout)
		switch {
		case err == nil:
			a.setAutoModeAvailable(true)
			return resp, nil
		case isAutoModeUnavailableError(err):
			slog.Warn("requested auto permission mode is unavailable; falling back to default",
				"agent_id", a.agentID)
			a.setAutoModeAvailable(false)
			return a.sendSetPermissionMode(ctx, PermissionModeDefault, timeout)
		default:
			return claudeCodeControlResult{}, err
		}
	}

	if _, err := a.sendSetPermissionMode(ctx, PermissionModeAuto, timeout); err != nil {
		if !isAutoModeUnavailableError(err) {
			slog.Warn("auto-mode probe failed (transient); treating as unavailable",
				"agent_id", a.agentID, "error", err)
		}
		a.setAutoModeAvailable(false)
	} else {
		a.setAutoModeAvailable(true)
	}
	return a.sendSetPermissionMode(ctx, requested, timeout)
}

// sendSetPermissionMode issues set_permission_mode and falls back to the
// requested mode when the response omits the applied mode field, so callers
// always receive a non-empty resp.Mode on success.
// permissionModeApplyTimeout caps how long the live UpdateSettings path waits for a
// set_permission_mode ack. Kept short so a permission-mode toggle made while a turn is
// streaming (the CLI defers the ack until the turn ends) fails fast and is applied
// optimistically, rather than blocking for APITimeout and then restarting the agent.
const permissionModeApplyTimeout = 2 * time.Second

func (a *ClaudeCodeAgent) sendSetPermissionMode(ctx context.Context, mode string, timeout time.Duration) (claudeCodeControlResult, error) {
	body, err := json.Marshal(map[string]string{
		"subtype": "set_permission_mode",
		"mode":    mode,
	})
	if err != nil {
		return claudeCodeControlResult{}, err
	}
	requestID, resp, err := a.sendControlAndWaitWithID(ctx, string(body), timeout)
	if errors.Is(err, errControlTimeout) {
		// The CLI deferred this ack until the active turn ends. Remember THIS request's id (as
		// the latest pending toggle) so claudeCodeHandleControlResponse folds back only the ack
		// that belongs to it -- not a stale/duplicate ack, nor an earlier toggle this one
		// supersedes. The turn is still streaming, so the deferred ack cannot arrive before this
		// returns, hence no race with the optimistic write the caller does next.
		a.mu.Lock()
		a.deferredPermissionModeReqID = requestID
		a.mu.Unlock()
	}
	if err == nil && resp.Mode == "" {
		resp.Mode = mode
	}
	return resp, err
}

// setAutoModeAvailable stores the auto-mode probe result under a.mu so
// concurrent readers (e.g. AvailableOptionGroups) observe a consistent value.
func (a *ClaudeCodeAgent) setAutoModeAvailable(v bool) {
	a.mu.Lock()
	a.autoModeAvailable = v
	a.mu.Unlock()
}

// isAutoModeUnavailableError reports whether err is the Claude Code
// control_response rejection for set_permission_mode:auto (admin-disabled,
// plan-gated, or unsupported-model).
func isAutoModeUnavailableError(err error) bool {
	return err != nil && strings.Contains(err.Error(), autoModeUnavailableErrorPrefix)
}

func (a *ClaudeCodeAgent) registerPendingControl(requestID string, ch chan<- claudeCodeControlResult) {
	a.pendingControlMu.Lock()
	defer a.pendingControlMu.Unlock()
	a.pendingControl[requestID] = ch
}

func (a *ClaudeCodeAgent) unregisterPendingControl(requestID string) {
	a.pendingControlMu.Lock()
	defer a.pendingControlMu.Unlock()
	delete(a.pendingControl, requestID)
}

// handlePendingControlResponse checks if a parsed line is a control_response
// matching a pending request. If so, it sends the result to the waiting
// channel and returns true (the line should be consumed, not forwarded).
func (a *ClaudeCodeAgent) handlePendingControlResponse(line *parsedLine) bool {
	// Quick check using the pre-parsed Type field.
	if line.Type != claudeMsgTypeControlResponse {
		return false
	}

	var envelope struct {
		Response struct {
			Subtype   string          `json:"subtype"`
			RequestID string          `json:"request_id"`
			Response  json.RawMessage `json:"response"`
			Error     string          `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal(line.Raw, &envelope); err != nil {
		return false
	}

	reqID := envelope.Response.RequestID
	a.pendingControlMu.Lock()
	ch, ok := a.pendingControl[reqID]
	a.pendingControlMu.Unlock()

	if !ok {
		return false
	}

	// Parse known fields from the inner response object.
	var innerResponse struct {
		Mode                  string                `json:"mode"`
		OutputStyle           string                `json:"output_style"`
		AvailableOutputStyles []string              `json:"available_output_styles"`
		FastModeState         string                `json:"fast_mode_state"`
		Models                []claudeCodeModelInfo `json:"models"`
		UnavailableModels     []claudeCodeModelInfo `json:"unavailable_models"`
	}
	if len(envelope.Response.Response) > 0 {
		// Best-effort: a partial/unknown response shape still yields the fields it
		// does carry. But log a type-mismatch failure (e.g. a schema-drifted models
		// array) so dynamic model discovery silently falling back to the static
		// catalog is diagnosable rather than indistinguishable from an old CLI.
		if err := json.Unmarshal(envelope.Response.Response, &innerResponse); err != nil {
			slog.Warn("failed to parse control response inner fields",
				"agent_id", a.agentID, "request_id", reqID, "error", err)
		}
	}

	result := claudeCodeControlResult{
		Success:               envelope.Response.Subtype == "success",
		Mode:                  innerResponse.Mode,
		Error:                 envelope.Response.Error,
		OutputStyle:           innerResponse.OutputStyle,
		AvailableOutputStyles: innerResponse.AvailableOutputStyles,
		FastModeState:         innerResponse.FastModeState,
		Models:                innerResponse.Models,
		UnavailableModels:     innerResponse.UnavailableModels,
		RawResponse:           envelope.Response.Response,
	}
	ch <- result
	return true
}

func (a *ClaudeCodeAgent) readOutputLoop(scanner *bufio.Scanner) {
	a.readOutput(scanner, a.handlePendingControlResponse, a.handleOutput)
}

// handleOutput adapts the parsedLine to the existing HandleOutput method,
// passing the pre-parsed Type to avoid re-parsing the envelope.
func (a *ClaudeCodeAgent) handleOutput(line *parsedLine) {
	a.handleClaudeOutput(line.Raw, line.Type)
}

func generateRequestID() string {
	b := make([]byte, 13)
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	for i := range b {
		b[i] = chars[rand.IntN(len(chars))]
	}
	return string(b)
}

// effortResolver answers model effort/ultracode capability questions against one
// catalog. The launch path constructs it over the static claudeCodeAvailableModels
// (the dynamic catalog isn't known until initialize completes); post-init paths
// construct it over the per-agent catalog via a.effortResolver. Wrapping the
// catalog once removes the trailing parameter every resolution helper used to
// thread, and makes "which catalog" an explicit choice at the few construction
// sites instead of an argument carried through the whole chain.
type effortResolver struct {
	// catalog is the primary catalog consulted first: the static one at launch, the
	// per-agent dynamic one post-init.
	catalog []*ModelInfo
	// fallback is consulted only when catalog doesn't list a model, so a model the
	// live CLI dropped from the dynamic list (e.g. reported in unavailable_models)
	// but that the agent is actually running still resolves to its real
	// capabilities instead of being treated as unknown. nil for the launch resolver
	// and the single-catalog resolvers tests build via newEffortResolver.
	fallback []*ModelInfo
}

func newEffortResolver(catalog []*ModelInfo) effortResolver {
	return effortResolver{catalog: catalog}
}

// effortResolver returns a resolver over the agent's per-agent dynamic catalog,
// with the static claudeCodeAvailableModels as a fallback for models the dynamic
// list omits. Dynamic takes precedence, so a capability the live CLI genuinely
// dropped (the model is still listed, minus a level) still wins; the fallback only
// rescues a model the CLI filtered out entirely (e.g. into unavailable_models) but
// that the session is still running -- keeping the decode (effortFromApplied),
// live-update (updateFlagSettings), and startup (reconcileStartupEffortFlags) paths
// from downgrading a filtered session's effort/ultracode out of agreement with each
// other. availableModels is written only during the pre-registration startup
// handshake and never mutated afterward, so this read is safe without a.mu (callers
// may already hold it).
func (a *ClaudeCodeAgent) effortResolver() effortResolver {
	return effortResolver{catalog: a.availableModels, fallback: claudeCodeAvailableModels}
}

// launchEffortPlan captures what the --effort launch flag did for a model+effort,
// resolved over the static catalog at launch: whether --effort was omitted and the
// level it was set to (the xhigh base for an ultracode launch). buildModelEffortArgs
// and reconcileStartupEffortFlags both derive it from planLaunch, so the "what launch
// sent" view is single-sourced and the launch flags can't drift from what startup
// reconciles against. Whether the launch was the xhigh+ultracode combo is deliberately
// NOT stored here: both consumers re-derive it from launchRunsUltracode (startup
// against the DYNAMIC catalog, buildModelEffortArgs against the static one), so there
// is no captured boolean to fall out of sync with the level.
type launchEffortPlan struct {
	omitted bool   // launch sent no --effort (CLI stays at its own default)
	level   string // the --effort value sent ("" when omitted; "xhigh" for the ultracode base)
}

// planLaunch resolves model+effort into the launch flag plan. See launchEffortPlan.
func (r effortResolver) planLaunch(model, effort string) launchEffortPlan {
	if r.launchOmitsEffort(model, effort) {
		return launchEffortPlan{omitted: true}
	}
	if r.launchRunsUltracode(model, effort) {
		// The --effort launch flag accepts only low|medium|high|xhigh|max: ultracode
		// has no flag value, so launch at its xhigh base and let buildStartupFlagSettings
		// layer the ultracode boolean on post-init.
		return launchEffortPlan{level: EffortXHigh}
	}
	return launchEffortPlan{level: r.resolveEffort(model, effort)}
}

// buildModelEffortArgs constructs the --model and --effort CLI arguments for
// Claude Code. The DefaultModelSentinel (and the empty model) omits BOTH --model
// and --effort so the CLI resolves the account's own default model AND that model's
// own default effort (get_settings then reports the concrete model/effort, which
// refreshSettingsFromAgent stores -- the model-side analogue of EffortAuto for
// effort). Forwarding a concrete --effort here would be resolved against the
// sentinel's empty effort menu and could push an unsupported level onto whatever the
// default resolves to (e.g. --effort on a Haiku account default). Haiku does not
// support --effort at all. EffortAuto also omits --effort so the CLI picks its own
// default. A consequence: a LEAPMUX_CLAUDE_DEFAULT_EFFORT set without a concrete
// LEAPMUX_CLAUDE_DEFAULT_MODEL is not applied to a sentinel-default agent (see
// effortOrDefault). Other models each expose a subset of effort levels in the catalog;
// any effort not in the subset is downgraded to "high" as a universal safe fallback.
// Called at launch over the static catalog (the dynamic one isn't known until
// initialize completes).
func (r effortResolver) buildModelEffortArgs(model, effort string) []string {
	var args []string
	if !UsesAccountDefaultModel(model) {
		args = []string{"--model", model}
	}
	plan := r.planLaunch(model, effort)
	if plan.omitted {
		return args
	}
	return append(args, "--effort", plan.level)
}

// definedEfforts returns the effort list the catalog defines for modelID and
// whether the model is known at all. It checks the primary catalog first, then the
// fallback, so a model the dynamic list omits but the static catalog still has
// resolves to its real efforts (see effortResolver). The single lookup is shared by
// supports and supportsUltracode, which differ only in how they treat an unknown
// model.
func (r effortResolver) definedEfforts(modelID string) (efforts []*EffortInfo, known bool) {
	if modelID == DefaultModelSentinel {
		// The account-default sentinel is a placeholder, not a concrete model: its
		// real efforts belong to whatever the CLI resolves it to. Report it as
		// unresolved so effort resolution passes the requested effort through rather
		// than clamping it against the empty effort list the sentinel's catalog entry
		// carries. Reached only in the degraded path where get_settings never echoed a
		// concrete applied.model, so a.model is stuck on "default".
		return nil, false
	}
	// Prefer a catalog entry that actually carries efforts. The dynamic catalog
	// takes precedence, but a known model can land in it with an EMPTY effort list
	// -- the live CLI reported it with supportsEffort:false, or with only effort
	// levels we don't recognize (schema drift) -- and such an empty dynamic entry
	// must not shadow a populated static-fallback entry for the same model. So we
	// keep scanning past an empty match for a populated one, while still reporting
	// the model as known. When no populated entry exists (a genuinely effort-less
	// model like Haiku, whose fallback entry is empty too) the known-but-empty
	// verdict stands. This preserves the legitimate "CLI dropped a level" case: a
	// dynamic entry with FEWER but non-empty efforts still wins over the fallback.
	for _, cat := range [][]*ModelInfo{r.catalog, r.fallback} {
		for _, m := range cat {
			// Guard nil entries to match FindAvailableModel/withDefaultModelMarked,
			// which already treat catalogs as possibly nil-bearing; convertClaudeModels
			// never emits nil today, but this keeps the catalog-walking helpers uniform.
			if m != nil && m.Id == modelID {
				if len(m.SupportedEfforts) > 0 {
					return m.SupportedEfforts, true
				}
				known = true // matched, but no efforts here; keep looking for a populated entry
			}
		}
	}
	return nil, known
}

// contextWindow returns the context window for modelID, consulting the primary
// catalog first and then the fallback -- mirroring definedEfforts so a model the
// live CLI dropped from its list but the session is still running resolves its
// window from the static fallback instead of reporting "unknown". Returns 0 when
// neither catalog knows the model (the unresolved account-default sentinel, whose
// catalog entry carries no window) -- the usage broadcast then omits context_window.
func (r effortResolver) contextWindow(modelID string) int64 {
	if w := modelContextWindow(r.catalog, modelID); w > 0 {
		return w
	}
	return modelContextWindow(r.fallback, modelID)
}

// effortListContains reports whether efforts holds an entry with the given ID.
func effortListContains(efforts []*EffortInfo, id string) bool {
	return slices.ContainsFunc(efforts, func(e *EffortInfo) bool { return e.Id == id })
}

// supports reports whether the given effort ID is in the known SupportedEfforts
// list for the given model. Unknown models are trusted (returns true) so new
// aliases work without a code change.
func (r effortResolver) supports(modelID, effort string) bool {
	efforts, known := r.definedEfforts(modelID)
	if !known {
		return true
	}
	return effortListContains(efforts, effort)
}

// supportsUltracode reports whether the model's catalog entry offers the ultracode
// tier. Unlike supports, it does NOT trust unknown models: ultracode forces
// --effort xhigh, so enabling it on a model we can't confirm supports xhigh would
// risk a level the model rejects. Because convertClaudeModels adds ultracode to any
// catalog entry whose CLI levels include xhigh, this stays consistent with the UI:
// a model is ultracode-capable here exactly when its AvailableModels entry
// advertises it (Opus, Fable, and any future xhigh model the live CLI reports).
func (r effortResolver) supportsUltracode(modelID string) bool {
	efforts, known := r.definedEfforts(modelID)
	return known && effortListContains(efforts, EffortUltracode)
}

// launchRunsUltracode reports whether launching model+effort runs the
// xhigh+ultracode combo: the requested effort is ultracode AND the model supports
// it. It is the single source of truth shared by buildModelEffortArgs (which
// launches at the xhigh base) and buildStartupFlagSettings (which layers the
// ultracode boolean back on), so the two can't disagree about whether a launch is
// an ultracode launch.
func (r effortResolver) launchRunsUltracode(model, effort string) bool {
	return effort == EffortUltracode && r.supportsUltracode(model)
}

// launchOmitsEffort reports whether launching model+effort sends no --effort flag,
// leaving the CLI at its own resolved default for the model. The account-default
// sentinel (and an empty model, which likewise sends no --model so the CLI picks
// the account default) and EffortAuto/"" always omit it; a model the catalog KNOWS
// has no effort support (Haiku) also omits it -- expressed via the catalog rather
// than a "haiku" literal, so an effort-less model the CLI introduces is handled
// without a code change. Unknown models are trusted and pass their effort through.
// Shared by buildModelEffortArgs (which omits --effort) and planLaunch, so the
// launch flags and the startup reconcile see the same "sent nothing" set.
func (r effortResolver) launchOmitsEffort(model, effort string) bool {
	if effort == "" || effort == EffortAuto || UsesAccountDefaultModel(model) {
		return true
	}
	efforts, known := r.definedEfforts(model)
	return known && len(efforts) == 0
}

// unsupportedUltracode reports whether effort is the ultracode tier while model
// can't run it. This is the recurring "ultracode requested/stored/being-left on a
// model whose catalog doesn't offer it" condition that the resolve (resolveEffort),
// decode (effortFromApplied), and live-update (updateFlagSettings) paths each must
// guard -- naming it once keeps a future model-capability change to a single edit
// instead of three that must be kept in agreement.
func (r effortResolver) unsupportedUltracode(effort, model string) bool {
	return effort == EffortUltracode && !r.supportsUltracode(model)
}

// resolveEffort resolves a requested effort against the model it will run under: an
// effort the model's catalog doesn't support is downgraded to the universal-safe
// EffortHigh, and "ultracode" stays "ultracode" only for a model that actually
// supports it (otherwise it too becomes EffortHigh -- this catches unknown models,
// which supports trusts but which we can't confirm are xhigh-capable). EffortAuto
// and "" pass through for the caller to handle.
func (r effortResolver) resolveEffort(model, effort string) string {
	if effort == "" || effort == EffortAuto {
		return effort
	}
	if !r.supports(model, effort) {
		return EffortHigh
	}
	if r.unsupportedUltracode(effort, model) {
		return EffortHigh
	}
	return effort
}

func init() {
	registerAgentFactory(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		func(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
			return StartClaudeCode(ctx, opts, sink)
		},
		claudeCodeAvailableModels,
		[]*leapmuxv1.AvailableOptionGroup{{
			Id:           OptionIDPermissionMode,
			Label:        "Permission Mode",
			DefaultValue: PermissionModeDefault,
			Mutable:      true,
			Order:        OptionOrderPermissionMode,
			Options: []*leapmuxv1.AvailableOption{
				{
					Id:          PermissionModeDefault,
					Name:        "Default",
					Description: "Standard behavior, prompts for dangerous operations.",
				},
				{
					Id:          PermissionModePlan,
					Name:        "Plan Mode",
					Description: "Planning mode, no actual tool execution.",
				},
				{
					Id:          PermissionModeAcceptEdits,
					Name:        "Accept Edits",
					Description: "Auto-accept file edit operations.",
				},
				{
					Id:          PermissionModeBypassPermissions,
					Name:        "Bypass Permissions",
					Description: "Bypass all permission checks (requires allowDangerouslySkipPermissions).",
				},
				{
					Id:          PermissionModeDontAsk,
					Name:        "Don't Ask",
					Description: "Don't prompt for permissions, deny if not pre-approved.",
				},
				{
					Id:          PermissionModeAuto,
					Name:        "Auto Mode",
					Description: "Uses an AI classifier to auto-approve safe tool calls and falls back to prompting for risky ones.",
				},
			},
		}},
		"LEAPMUX_CLAUDE_DEFAULT_MODEL",
		"LEAPMUX_CLAUDE_DEFAULT_EFFORT",
		"claude",
	)
	// Each Claude model carries its effort AND extended-thinking groups, so the
	// frontend rebuilds both on a model switch (the static fallback needs this
	// too, hence the registry override rather than only Claude.OptionGroups).
	setModelSubGroups(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, claudeModelSubGroups)
	setModelIDNormalizer(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, normalizeClaudeCodeModel)
	// model + permissionMode (static group) + effort (built from the model catalog).
	setAdditionalOptionIDs(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, OptionIDEffort)
}
