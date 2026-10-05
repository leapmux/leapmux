package grok

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Grok changes its session goal only through the `/goal` prompt command, and it
// reports every change of the goal on `goal_updated`:
//
//	goal  "<objective> [--budget <tokens>] | status | pause | resume | clear"
//
// The report is the source of truth for the card, so LeapMux does not observe
// the command it sends. A text route that observed would state a goal before
// Grok accepted it, and a refused command would leave that goal on the card.
//
// The command goes to Grok's own prompt queue at once (sendGoalCommand), not
// to the worker's queue. Grok runs the whole goal loop inside one turn: the
// planner, each implementer round and each evaluation. The worker's queue
// waits for an idle agent, so a command in it reached Grok only after the goal
// stopped by itself, and Grok then answered "Goal is already paused." Grok
// queues a prompt that arrives during a turn, and its goal loop yields to that
// prompt before the next round.
const (
	grokGoalCommand           = "/goal"
	grokGoalAdvertisedCommand = "goal"
)

// grokGoalRoute is Grok's user-message goal vocabulary. `status` is a query:
// Grok answers it with a host message, and reads an objective with that one
// word as the query.
var grokGoalRoute = providerkit.GoalTextRoute{
	Provider:   "grok",
	Command:    grokGoalCommand,
	ClearArgs:  []string{"clear"},
	PauseArgs:  []string{"pause"},
	ResumeArgs: []string{"resume"},
	QueryArgs:  []string{"status"},
}

// grokGoalBudgetSuffix matches the trailing budget flag that Grok splits off an
// objective: `--budget` as its own word, then a final all-digit word.
var grokGoalBudgetSuffix = regexp.MustCompile(`\s--budget\s+\d+$`)

var _ agent.GoalWriter = (*Agent)(nil)

// SupportedGoalActions reports the four verbs, and only while Grok advertises
// the command. `[goal] enabled = false` removes it, and the browser then draws
// no goal control rather than one that does nothing.
func (a *Agent) SupportedGoalActions() []agent.GoalAction {
	if !a.HasAvailableCommand(grokGoalAdvertisedCommand) {
		return nil
	}
	return []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume}
}

// PerformGoalAction builds the prompt that changes Grok's goal and sends it to
// Grok's own queue (sendGoalCommand). It queues nothing in the worker, and
// `goal_updated` then reports what Grok did with the command.
func (a *Agent) PerformGoalAction(action agent.GoalAction, objective string) (agent.GoalOutcome, error) {
	if !a.HasAvailableCommand(grokGoalAdvertisedCommand) {
		return agent.GoalOutcome{}, agent.ErrGoalControlUnsupported
	}
	if action == agent.GoalActionSet {
		// Grok takes a trailing `--budget <tokens>` off the objective as a cap.
		// The goal card has no budget field, so an objective that ends that way
		// is text the user wrote, and Grok would store a shorter objective.
		folded := strings.Join(strings.Fields(objective), " ")
		if grokGoalBudgetSuffix.MatchString(folded) {
			return agent.GoalOutcome{}, fmt.Errorf("%w: grok %s reads the trailing %q as a token budget",
				agent.ErrGoalObjectiveIsCommand, grokGoalCommand, folded[grokGoalBudgetSuffix.FindStringIndex(folded)[0]+1:])
		}
	}
	command, err := grokGoalRoute.CommandText(action, objective)
	if err != nil {
		return agent.GoalOutcome{}, err
	}
	return agent.GoalOutcome{}, a.sendGoalCommand(command)
}

// sendGoalCommand sends one `/goal` command to Grok as a prompt of its own,
// outside the base's one-prompt slot, so it never waits for the running turn.
// Grok queues it behind that turn and runs it before the next round of a goal.
//
// The command carries no prompt id of LeapMux's, so the queue notification
// that starts it opens it as a turn of Grok's own (handleQueueChanged), and its
// turn_completed ends that turn. When it starts while a prompt of LeapMux's
// waits for its response, the base queues the command's turn behind that
// prompt, which is the order in which Grok answers the two. The response of
// the command only states a refusal: Grok answers the command's own reply
// inside its turn.
//
// acp.Base.SendPromptDetached is impossible here. It runs the PromptParams hook
// (adjustPromptParams), which states an id and makes the command a prompt of
// LeapMux's. handleQueueChanged then opens no turn for it, and the reader sees
// an idle agent while Grok runs the goal.
func (a *Agent) sendGoalCommand(command string) error {
	return a.WithSessionID(func(sessionID string) error {
		// A prompt of the base refuses a stopped agent in the same words.
		if a.IsStopped() {
			return fmt.Errorf("agent is stopped")
		}
		if sessionID == "" {
			return fmt.Errorf("agent has no active session")
		}
		params, err := json.Marshal(map[string]any{
			"sessionId": sessionID,
			"prompt":    acp.BuildPromptBlocks(command, nil),
		})
		if err != nil {
			return fmt.Errorf("marshal the Grok goal command: %w", err)
		}
		return a.SendDetachedRequest(acp.MethodSessionPrompt, params, func(_ json.RawMessage, err error) {
			if err == nil || !a.IsCurrentSession(sessionID) || a.IsStopped() {
				return
			}
			slog.Warn("grok goal command failed", "agent_id", a.AgentID(), "command", command, "error", err)
			a.Sink().PersistLeapMuxNotification(map[string]any{
				contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
				contracts.NotificationFieldError: fmt.Sprintf("%s failed: %v", command, err),
			})
		})
	})
}

// grokGoalUpdate is the part of `goal_updated` that the goal card shows.
type grokGoalUpdate struct {
	GoalID       string `json:"goal_id"`
	Objective    string `json:"objective"`
	Status       string `json:"status"`
	TokenBudget  *int64 `json:"token_budget"`
	TokensUsed   *int64 `json:"tokens_used"`
	ElapsedMs    *int64 `json:"elapsed_ms"`
	WorkerRounds *int32 `json:"total_worker_rounds"`
	PauseMessage string `json:"pause_message"`
}

// Grok's goal status words.
const (
	grokGoalStatusActive           = "active"
	grokGoalStatusUserPaused       = "user_paused"
	grokGoalStatusBackOffPaused    = "back_off_paused"
	grokGoalStatusNoProgressPaused = "no_progress_paused"
	grokGoalStatusInfraPaused      = "infra_paused"
	grokGoalStatusDoomLoopPaused   = "doom_loop_paused"
	grokGoalStatusBlocked          = "blocked"
	grokGoalStatusBudgetLimited    = "budget_limited"
	grokGoalStatusComplete         = "complete"
	grokGoalStatusCleared          = "cleared"
)

// grokGoalStatus maps Grok's nine status words onto the four neutral ones.
//
// Every paused word maps to PAUSED, because `/goal resume` continues each of
// them. `doom_loop_paused` is the legacy name of a user pause. An unknown word
// maps to blocked for the reason Codex's does: a status this build cannot read
// is one it must not offer Pause for.
func grokGoalStatus(wire string) agent.GoalStatus {
	switch wire {
	case grokGoalStatusActive:
		return agent.GoalStatusActive
	case grokGoalStatusUserPaused, grokGoalStatusBackOffPaused, grokGoalStatusNoProgressPaused,
		grokGoalStatusInfraPaused, grokGoalStatusDoomLoopPaused:
		return agent.GoalStatusPaused
	case grokGoalStatusComplete:
		return agent.GoalStatusDone
	default:
		return agent.GoalStatusBlocked
	}
}

// grokGoalStatusDetail keeps what the neutral status loses: the reason for a
// pause or a block. A plain `active`, `user_paused` or `complete` needs no word.
func grokGoalStatusDetail(update grokGoalUpdate) string {
	switch update.Status {
	case grokGoalStatusActive, grokGoalStatusUserPaused, grokGoalStatusDoomLoopPaused, grokGoalStatusComplete:
		return ""
	}
	word := strings.ReplaceAll(update.Status, "_", " ")
	if message := strings.TrimSpace(update.PauseMessage); message != "" {
		return word + ": " + message
	}
	return word
}

// handleGoalUpdated folds one goal report into the card. `cleared` and an empty
// goal id state that no goal exists.
func (a *Agent) handleGoalUpdated(raw json.RawMessage) {
	var update grokGoalUpdate
	if err := json.Unmarshal(raw, &update); err != nil {
		slog.Warn("grok goal_updated unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}
	if update.Status == grokGoalStatusCleared || update.GoalID == "" {
		a.Sink().ClearGoal(false)
		return
	}
	status := grokGoalStatus(update.Status)
	var seconds *int64
	if update.ElapsedMs != nil {
		value := *update.ElapsedMs / 1000
		seconds = &value
	}
	a.Sink().UpsertGoal(agent.GoalUpdate{
		NativeID:        update.GoalID,
		Objective:       update.Objective,
		Status:          status,
		StatusDetail:    grokGoalStatusDetail(update),
		TokensUsed:      update.TokensUsed,
		TokenBudget:     update.TokenBudget,
		TimeUsedSeconds: seconds,
		Iterations:      update.WorkerRounds,
	})
}

// seedAvailableCommands reads the command set that Grok states in its
// initialize response. The session's own available_commands_update arrives
// only later, and the goal control would wait for it otherwise.
func (a *Agent) seedAvailableCommands(response []byte) {
	var initialize struct {
		Meta struct {
			AvailableCommands []struct {
				Name string `json:"name"`
			} `json:"availableCommands"`
		} `json:"_meta"`
	}
	if json.Unmarshal(response, &initialize) != nil || len(initialize.Meta.AvailableCommands) == 0 {
		return
	}
	names := make([]string, 0, len(initialize.Meta.AvailableCommands))
	for _, command := range initialize.Meta.AvailableCommands {
		names = append(names, command.Name)
	}
	a.ReplaceAvailableCommands(names)
}
