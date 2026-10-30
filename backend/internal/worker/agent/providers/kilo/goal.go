package kilo

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Kilo changes its session goal through a user-message slash command, the same
// route Goose and Claude Code take. Its command carries all FOUR verbs, which
// no other text route does:
//
//	goal  "Keep working toward a session goal. /goal <objective> or pause, resume, clear"
//
// The command is Kilo's own addition to the OpenCode base it forks -- every line
// of it is marked `kilocode_change`, and upstream OpenCode registers no goal
// command at all. That is why OpenCode offers no goal here and Kilo does.
const kiloGoalCommand = "/goal"

// kiloGoalAdvertisedCommand is the name Kilo lists in its ACP command set. The
// list carries bare names, without the leading slash.
const kiloGoalAdvertisedCommand = "goal"

// kiloGoalRoute is Kilo's user-message goal vocabulary. It builds the queued
// command of each verb and nothing else: the daemon reports the goal itself
// through the session metadata, so no delivery observer runs. Kilo steers with
// a second session/prompt, and it holds a pause or clear prompt for 60 s, an
// upstream limit.
var kiloGoalRoute = providerkit.GoalTextRoute{
	Provider:   "kilo",
	Command:    kiloGoalCommand,
	ClearArgs:  []string{"clear"},
	PauseArgs:  []string{"pause"},
	ResumeArgs: []string{"resume"},
}

var _ agent.GoalWriter = (*Agent)(nil)

// SupportedGoalActions reports the four verbs, and only while Kilo advertises
// the command. A build without the feature lists no `goal` command, and the
// browser then draws no goal control rather than one that does nothing.
func (a *Agent) SupportedGoalActions() []agent.GoalAction {
	if !a.hasGoalCommand() {
		return nil
	}
	return []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume}
}

// PerformGoalAction builds the user message that changes Kilo's goal. The queue
// delivers it; the goal changes only when the daemon's own metadata says so.
func (a *Agent) PerformGoalAction(action agent.GoalAction, objective string) (agent.GoalOutcome, error) {
	return kiloGoalRoute.Perform(action, objective)
}

func (a *Agent) hasGoalCommand() bool {
	return a.HasAvailableCommand(kiloGoalAdvertisedCommand)
}

// observeKiloGoal reads Kilo's own goal report from the metadata of one
// session.updated event. The family forwards those events for the current
// session only; the info object's own identity still gets checked, because it
// names the session the metadata belongs to.
//
// The shared goal service compares each report with the stored row and
// suppresses an unchanged one, so the reader keeps no fingerprint of its own:
// a repeated report, including the unchanged first report after a resume,
// writes nothing.
func (a *Agent) observeKiloGoal(eventType string, properties json.RawMessage) {
	if eventType != EventSessionUpdated {
		return
	}
	var envelope struct {
		Info json.RawMessage `json:"info"`
	}
	if json.Unmarshal(properties, &envelope) != nil {
		return
	}
	var info struct {
		ID       string          `json:"id"`
		Metadata json.RawMessage `json:"metadata"`
	}
	if json.Unmarshal(envelope.Info, &info) != nil {
		return
	}
	if info.ID != "" && info.ID != a.CurrentSessionID() {
		return
	}
	goal, ok := kiloGoalFromMetadata(info.Metadata)
	if !ok {
		a.Sink().ClearGoal(false)
		return
	}
	a.Sink().UpsertGoal(goal)
}

// kiloGoalFromMetadata maps the `kilo.goal` metadata record to one goal report.
// It returns false when the session holds no goal: an absent key, an empty
// metadata object, or a record without readable objective text all mean the
// same absence. The status vocabulary and both fallbacks come from Kilo's own
// GoalState.read: a known word wins, then active:true, then paused.
func kiloGoalFromMetadata(metadata json.RawMessage) (agent.GoalUpdate, bool) {
	var fields map[string]json.RawMessage
	if !providerkit.RawPresent(metadata) || json.Unmarshal(metadata, &fields) != nil {
		return agent.GoalUpdate{}, false
	}
	goal, ok := objectFields(fields["kilo.goal"])
	if !ok {
		return agent.GoalUpdate{}, false
	}
	text, ok := providerkit.RawString(goal["text"])
	if !ok || strings.TrimSpace(text) == "" {
		return agent.GoalUpdate{}, false
	}
	word, _ := providerkit.RawString(goal["status"])
	status := agent.GoalStatusPaused
	detail, _ := providerkit.RawString(goal["reason"])
	switch word {
	case "active", "waiting":
		status = agent.GoalStatusActive
	case "complete":
		status = agent.GoalStatusDone
	case "blocked":
		status = agent.GoalStatusBlocked
	case "paused":
	default:
		// An absent or unknown word: the native fallback reads active:true as
		// active and anything else as paused, the value already set above.
		if providerkit.RawBool(goal["active"]) {
			status = agent.GoalStatusActive
		}
	}
	if word == "waiting" {
		if label, valid := kiloWaitLabel(goal["wait"]); valid {
			detail = "waiting: " + label
		}
	}
	return agent.GoalUpdate{Objective: text, Status: status, StatusDetail: detail}, true
}

// kiloWaitLabel reads the wait record of a waiting goal. Kilo accepts a wakeup,
// cron or process wait with an id, and labels it with its own label or its id.
func kiloWaitLabel(raw json.RawMessage) (string, bool) {
	wait, ok := objectFields(raw)
	if !ok {
		return "", false
	}
	kind, _ := providerkit.RawString(wait["kind"])
	if kind != "wakeup" && kind != "cron" && kind != "process" {
		return "", false
	}
	id, ok := providerkit.RawString(wait["id"])
	if !ok || id == "" {
		return "", false
	}
	if label, ok := providerkit.RawString(wait["label"]); ok {
		return label, true
	}
	return id, true
}

// objectFields decodes one JSON object into its members. Any other value,
// including null, is not an object.
func objectFields(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if !providerkit.RawPresent(raw) {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, false
	}
	return fields, true
}
