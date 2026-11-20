package dirac

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/envprobe"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// diracNoAutoUpdateEnv turns off the update that Dirac starts from its startup
// path: a detached process that replaces the install of Dirac itself (dist/cli.mjs
// autoUpdateOnStartup). The ACP mode takes that path. Only the exact value `1`
// counts. The worker pins it, after the user's profile, so no run downloads a
// release in the middle of a session.
const diracNoAutoUpdateEnv = "DIRAC_NO_AUTO_UPDATE=1"

// Dirac uses each exported value exactly as the shell passes it to Node.
var diracProfilePathVariables = map[string]string{"DIRAC_DIR": "", "HOME": "", "USERPROFILE": ""}

// Start starts a Dirac ACP process and performs the handshake.
//
// `dirac --acp` is the launch. The provider route and the model come from the
// DIRAC_PROVIDER / DIRAC_MODEL / DIRAC_API_KEY / DIRAC_BASE_URL environment,
// which the E2E isolation recipe and a real installation both set. A resume
// uses `session/load` to load its session and replay the native history.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	return startWithClock(ctx, opts, sink, quartz.NewReal())
}

func startWithClock(ctx context.Context, opts agent.Options, sink agent.ProviderServices, clock quartz.Clock) (agent.Agent, error) {
	return acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration: Registration(),
		ProviderName: "dirac",
		// ACP reads the native subagentsEnabled preference. The CLI's
		// --subagents option does not reach its ACP startup path.
		BaseArgs:  []string{"--acp"},
		PinnedEnv: []string{diracNoAutoUpdateEnv},
		ObserveEnvironment: &launch.ObservationRequest{
			PathVariables: diracProfilePathVariables,
		},
		SessionConfig: acp.SessionConfig{
			NewMethod: acp.MethodSessionNew,
			// Load restores the native session and replays its history.
			// The idle-output handler discards root history already stored here.
			ResumeMethod: acp.MethodSessionLoad,
		},
		NewAgent: func() *Agent {
			return &Agent{root: diracHome(agent.StoredSessionQuery{HomeDir: opts.HomeDir}), resumedSession: opts.ResumeSessionID}
		},
		Base: func(a *Agent) *acp.Base { return &a.Base },
		Configure: func(a *Agent, _ agent.ProviderServices) acp.Hooks {
			return a.configure(clock)
		},
		AfterHandshake: func(a *Agent, handshake *acp.SessionResult, opts agent.Options) error {
			return a.ApplyPermissionModeStartup(handshake, opts, contracts.DiracModeAct, opts.Model())
		},
	})
}

// configure returns the hooks of one agent.
func (a *Agent) configure(clock quartz.Clock) acp.Hooks {
	a.clock = clock
	return acp.Hooks{
		// Enabling these native settings can remove a permission card before
		// Dirac's ACP bridge submits the reader's response to that card.
		ConfigOptionRequiresIdle: func(id, value string) bool {
			return value == "true" && (id == contracts.DiracConfigYolo || id == contracts.DiracConfigAutoApprove)
		},
		WithBypassPermission: func(claim func()) {
			a.WithOptionValue(contracts.DiracConfigYolo, "true", claim)
		},
		PermissionAllowsBypass: permissionAllowsBypass,
		ModeChannel:            acp.ModeChannelPermissionMode,
		EffortConfigID:         contracts.DiracConfigReasoningEffort,
		// Dirac advertises `dev.dirac/whisper` in the initialize `_meta` as a
		// bare capability flag, not the sessionSteer.method shape the shared
		// parser reads, so the advertised steer method is a constant of this
		// package.
		AdvertisedSteerMethod: func(response []byte) string {
			return diracAdvertisedSteerMethod(response)
		},
		PromptParams:           diracPromptParams,
		ExtraMethod:            a.handleExtraMethod,
		SessionMetadataHandler: a.observeDiracNotice,
		ClearProviderState:     a.clearChildState,
		BeforeWaitCleanup:      a.beforeWaitCleanup,
		// Decode the effective profile the launch observed before opening a
		// native session, so the pre-stop checkpoint reads the root Dirac
		// actually used rather than the Worker's assumed one.
		InitializeResponse: func(response []byte) {
			a.applyObservedProfile()
		},
		// Confirm a native checkpoint before pipe EOF ends the process.
		PrepareStop: a.waitForDiracResumeCheckpoint,
		// Dirac's `respond` tool is the control plane: a question rides
		// elicitation and a plan defers its approval to the next prompt. The
		// base answers both, so no provider control reader is needed.
	}
}

// applyObservedProfile records the launch's candidate before ACP opens a
// session. A checkpoint must also link the exact session to a task in it.
// Child archive reads keep their existing profile resolution.
func (a *Agent) applyObservedProfile() {
	snapshot, ok := decodeDiracEnvironmentObservation(a.PreambleMetaValue(envprobe.ObservationKey))
	if ok {
		a.checkpointProfile = diracEffectiveHome(snapshot)
	}
	if a.checkpointProfile == "" {
		slog.Debug("the Dirac launch profile could not be observed", "agent_id", a.AgentID())
		return
	}
}

// decodeDiracEnvironmentObservation requires exactly the inputs requested from
// the native helper. An absent value and a reported empty value differ.
func decodeDiracEnvironmentObservation(encoded string) (envprobe.Snapshot, bool) {
	if strings.ContainsAny(encoded, "\r\n") {
		return envprobe.Snapshot{}, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return envprobe.Snapshot{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	// Pointer values distinguish null from an exported empty string or a
	// false presence flag. The helper reports every requested variable.
	var wire struct {
		Values     map[string]*string `json:"values"`
		WorkingDir string             `json:"workingDir"`
		Paths      map[string]*string `json:"paths"`
		Present    map[string]*bool   `json:"present"`
	}
	count := len(diracProfilePathVariables)
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF ||
		len(wire.Values) != count || len(wire.Paths) != count || len(wire.Present) != count ||
		!absoluteDiracLaunchPath(wire.WorkingDir) {
		return envprobe.Snapshot{}, false
	}
	snapshot := envprobe.Snapshot{
		Values: make(map[string]string, count), WorkingDir: wire.WorkingDir,
		Paths: make(map[string]string, count), Present: make(map[string]bool, count),
	}
	for name := range diracProfilePathVariables {
		value, path, present := wire.Values[name], wire.Paths[name], wire.Present[name]
		if value == nil || path == nil || present == nil || strings.ContainsRune(*value, 0) ||
			(!*present && *value != "") || (*value == "" && *path != "") || (*value != "" && !absoluteDiracLaunchPath(*path)) {
			return envprobe.Snapshot{}, false
		}
		snapshot.Values[name], snapshot.Paths[name], snapshot.Present[name] = *value, *path, *present
	}
	return snapshot, true
}

func absoluteDiracLaunchPath(path string) bool {
	return filepath.IsAbs(path) && !strings.ContainsRune(path, 0)
}

// diracEffectiveHome applies Node's lexical path resolution before opening
// the native archive. Node reads USERPROFILE on Windows and HOME on Unix.
// With that variable unset it uses the current system user's home.
func diracEffectiveHome(snapshot envprobe.Snapshot) string {
	if dir := snapshot.Paths["DIRAC_DIR"]; dir != "" {
		return filepath.Clean(dir)
	}
	homeVariable := "HOME"
	if runtime.GOOS == "windows" {
		homeVariable = "USERPROFILE"
	}
	home := snapshot.Paths[homeVariable]
	if !snapshot.Present[homeVariable] {
		account, err := user.Current()
		if err != nil || !absoluteDiracLaunchPath(account.HomeDir) {
			return ""
		}
		home = account.HomeDir
	} else if home == "" {
		home = snapshot.WorkingDir
	}
	return filepath.Join(home, ".dirac")
}
