package kilo

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

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
	return a.FamilyBase.UpdateSettings(options)
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
