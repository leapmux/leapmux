package kilo

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// OptionSwarm is the option of Kilo Swarm: the shared message board that a
// session and its subagents post to and read (the board_post and board_read
// tools). Kilo turns it on by default.
const OptionSwarm = "kiloSwarm"

// kiloSharedAgentBoardEnv is Kilo's switch of Kilo Swarm. Kilo reads it only at
// start, so a change restarts the agent. An explicit `false` in Kilo's own
// configuration (`shared_agent_board`) still turns the board off: Kilo lets
// either explicit disable win over an explicit enable.
const kiloSharedAgentBoardEnv = "KILO_EXPERIMENTAL_SHARED_AGENT_BOARD"

// kiloConfigRoute is the daemon route that answers Kilo's resolved configuration.
const kiloConfigRoute = "/config"

// kiloConfigReadTimeout limits one read of the configuration.
const kiloConfigReadTimeout = 30 * time.Second

// kiloSwarmConfigOffReason states why the reader cannot turn Kilo Swarm on.
const kiloSwarmConfigOffReason = "Your Kilo configuration turns Kilo Swarm off (shared_agent_board: false)."

var kiloSwarmGroup = agent.SelectGroup(OptionSwarm, "Kilo Swarm", agent.OptionOrderProviderFirst, "true", []agent.OptionDef{
	{Id: "false", Name: "Off", Description: "Offer no shared agent board. The agent and its subagents coordinate through their task results alone."},
	{Id: "true", Name: "On", Description: "Let the agent and its subagents post to and read a shared message board.", Default: true},
})

// kiloSwarmFromOptions reads the launch value of Kilo Swarm. An absent value
// is Kilo's own default, on.
func kiloSwarmFromOptions(options optionmap.Map) (bool, error) {
	if options.Get(OptionSwarm) == "" {
		return true, nil
	}
	return options.Bool(OptionSwarm)
}

// kiloLaunchEnv pins Kilo Swarm to the launch value, over an inherited value.
func kiloLaunchEnv(opts agent.Options) ([]string, error) {
	swarm, err := kiloSwarmFromOptions(opts.Options)
	if err != nil {
		return nil, fmt.Errorf("kilo swarm: %w", err)
	}
	return []string{kiloSharedAgentBoardEnv + "=" + strconv.FormatBool(swarm)}, nil
}

// swarmOptionGroups reports Kilo Swarm with its effective value. Kilo's own
// configuration can turn the board off whatever the launch value is; the group
// then offers Off alone and states why.
func (a *Agent) swarmOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.Mu.Lock()
	swarm, configOff := a.swarm, a.swarmConfigOff
	a.Mu.Unlock()
	if !configOff {
		return []*leapmuxv1.AvailableOptionGroup{providerkit.LiveGroup(kiloSwarmGroup, strconv.FormatBool(swarm))}
	}
	group := providerkit.LiveGroup(kiloSwarmGroup, "false")
	group.Mutable = false
	group.ReadOnlyReason = kiloSwarmConfigOffReason
	for _, option := range group.Options {
		if option.GetId() == "false" {
			group.Options = []*leapmuxv1.AvailableOption{option}
			break
		}
	}
	return []*leapmuxv1.AvailableOptionGroup{group}
}

// UpdateSettings applies a change of Kilo Swarm through a restart, because
// Kilo reads its switch only at start. It refuses a value that is neither true
// nor false, and an On that Kilo's own configuration overrides, before any
// other setting changes.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	if options.Get(OptionSwarm) != "" {
		swarm, err := options.Bool(OptionSwarm)
		if err != nil {
			return a.SettingsSnapshot()
		}
		a.Mu.Lock()
		running, configOff := a.swarm, a.swarmConfigOff
		a.Mu.Unlock()
		if configOff && swarm {
			return a.SettingsSnapshot()
		}
		if swarm != running && !configOff {
			return agent.RestartRequiredSettings(options)
		}
	}
	return a.updateClientSettings(options)
}

// readSwarmConfig reads whether Kilo's own configuration turns Kilo Swarm
// off, and republishes the group when it does. A failed read changes nothing.
// ctx is the context of the daemon bridge.
func (a *Agent) readSwarmConfig(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, kiloConfigReadTimeout)
	defer cancel()
	raw, err := a.ReadDaemon(ctx, kiloConfigRoute)
	if err != nil {
		slog.Debug("kilo could not read its configuration", "agent_id", a.AgentID(), "error", err)
		return
	}
	var config struct {
		SharedAgentBoard *bool `json:"shared_agent_board"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		slog.Warn("kilo: unreadable configuration", "agent_id", a.AgentID(), "error", err)
		return
	}
	configOff := config.SharedAgentBoard != nil && !*config.SharedAgentBoard
	a.Mu.Lock()
	changed := a.swarmConfigOff != configOff
	a.swarmConfigOff = configOff
	a.Mu.Unlock()
	if changed {
		// The frontend reads a changed option catalog from a status broadcast.
		a.Sink().BroadcastStatusActive(a.CurrentSessionID())
	}
}

// AutoApproveGroup defines Kilo's client approval option.
//
// Kilo implements its `--auto` mode in its own client. The
// client answers each permission request once, and a request that a rule
// denies never reaches it. The daemon has no such mode to switch on, and its
// session rule update only appends rules, so it cannot switch one off again.
// LeapMux is the client here, so it answers the requests itself while the
// option is on.
var AutoApproveGroup = providerkit.ClientAutoApproveGroup()

// acpPermissionKindAllowAlways is the ACP kind of a permission option that
// allows the call always. Only the worker reads it, so it stays out of the
// shared ACP contract.
const acpPermissionKindAllowAlways = "allow_always"

// autoApproveFromOptions reads the launch value of the Auto-approve option.
func autoApproveFromOptions(options optionmap.Map) (bool, error) {
	return options.Bool(contracts.ClientApprovalOptionAutoApprove)
}

// localOptionGroups reports the current client approval and Swarm settings.
func (b *Agent) localOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	b.Mu.Lock()
	autoApprove := b.autoApprove
	b.Mu.Unlock()
	groups := []*leapmuxv1.AvailableOptionGroup{providerkit.LiveGroup(AutoApproveGroup, strconv.FormatBool(autoApprove))}
	groups = append(groups, b.swarmOptionGroups()...)
	return groups
}

// applyLocalOption applies a new Auto-approve value at once. The base then
// approves each request that already waits (Base.UpdateSettings).
func (b *Agent) applyLocalOption(id, value string) (bool, error) {
	if id != contracts.ClientApprovalOptionAutoApprove {
		return false, nil
	}
	autoApprove, err := optionmap.Map{id: value}.Bool(id)
	if err != nil {
		return true, err
	}
	b.setAutoApprove(autoApprove)
	return true, nil
}

// setAutoApprove sets the value of the Auto-approve option.
func (b *Agent) setAutoApprove(autoApprove bool) {
	b.Mu.Lock()
	b.autoApprove = autoApprove
	b.Mu.Unlock()
}

// UpdateSettings refuses an Auto-approve value that is neither true nor
// false before any other setting changes. A failed local write would ask the
// service for a restart, and the restart would refuse the same value.
func (b *Agent) updateClientSettings(options optionmap.Map) agent.SettingsApplyResult {
	if _, err := autoApproveFromOptions(options); err != nil {
		return b.SettingsSnapshot()
	}
	return b.Base.UpdateSettings(options)
}

// withAutoApprove runs claim while Auto-approve is on. The claim runs under
// the settings lock, so a change of the option cannot pass between the check
// and the approval.
func (b *Agent) withAutoApprove(claim func()) {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	if b.autoApprove && !b.StoppedLocked() {
		claim()
	}
}

// permissionOffersAlways reports whether Auto-approve may answer a permission
// request: the request must offer an option that allows the call always.
//
// Kilo offers no such option for a request that it reserves for a person: a
// batch of skill shell commands, or a command that leaves the sandbox. Kilo's
// own `--auto` does not approve those either. Only requests that offer an
// always-allow option are eligible for automatic approval.
func permissionOffersAlways(line *providerkit.ParsedLine) bool {
	var request struct {
		Options []struct {
			Kind string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(line.Params, &request) != nil {
		return false
	}
	for _, option := range request.Options {
		if option.Kind == acpPermissionKindAllowAlways {
			return true
		}
	}
	return false
}
