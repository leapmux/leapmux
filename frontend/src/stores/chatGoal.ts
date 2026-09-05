import type { AgentGoal as ProtoAgentGoal } from '~/generated/proto/leapmux/v1/agent_pb'
import { AgentGoalAction, AgentGoalStatus } from '~/generated/proto/leapmux/v1/agent_pb'

// ---------------------------------------------------------------------------
// Provider-neutral session-goal model + conversions
//
// A session goal is a standing objective the agent works toward, re-checked at
// the end of every turn until the condition holds. Codex, ZCode, Claude Code
// and Reasonix each report one in their own wire shape; the worker normalizes
// them, so this module sees one shape and the UI has one renderer.
//
// There is at most ONE goal per agent -- every CLI with the feature enforces
// that itself -- so this is a single value, not a list.
//
// A leaf module: it imports only the generated proto types, so the chat store,
// the work panel and the indicator chip share one shape without routing
// conversions through the window store.
// ---------------------------------------------------------------------------

/** The neutral status. `blocked` means "stopped and needs you". */
export type GoalStatus = 'active' | 'paused' | 'blocked' | 'done'

/** One goal action, in the spelling the UI and the RPC share. */
export type GoalAction = 'set' | 'clear' | 'pause' | 'resume'

export interface SessionGoal {
  objective: string
  status: GoalStatus
  /**
   * The provider's OWN status word ("usageLimited", "notSatisfied",
   * "verifying", "budget_spend"), or its last-check reason. `status` decides
   * what the card offers; this says why, and it is the only place the
   * precision lost by mapping five vocabularies onto four values survives.
   */
  statusDetail?: string
  createdAt?: string
  updatedAt?: string
}

/**
 * The volatile half of the goal, delivered on the ephemeral session-info
 * channel rather than with the goal itself.
 *
 * Every field is optional because no two providers report the same counters:
 * Codex sends tokens and seconds but no iteration count, ZCode sends seconds
 * and an iteration but no tokens, Claude Code sends only an iteration count.
 * An absent field must render as absent -- a zero here would state a number the
 * provider never gave.
 */
export interface GoalProgress {
  tokensUsed?: number
  tokenBudget?: number
  timeUsedSeconds?: number
  iterations?: number
}

/** Converts the wire goal to the store shape. */
export function protoGoalToStore(g: ProtoAgentGoal): SessionGoal {
  return {
    objective: g.objective,
    status: goalStatusFromProto(g.status),
    statusDetail: g.statusDetail || undefined,
    createdAt: g.createdAt || undefined,
    updatedAt: g.updatedAt || undefined,
  }
}

/**
 * The actions the running agent can perform, from the wire enum.
 *
 * Kept apart from the goal itself because it must exist when a goal does NOT:
 * "this agent can set a goal" is exactly what the empty state needs to know,
 * and a capability hanging off an absent goal could never say it.
 */
export function goalActionsFromProto(actions: AgentGoalAction[]): GoalAction[] {
  return actions.map(goalActionFromProto).filter((a): a is GoalAction => a !== undefined)
}

function goalStatusFromProto(s: AgentGoalStatus): GoalStatus {
  switch (s) {
    case AgentGoalStatus.ACTIVE:
      return 'active'
    case AgentGoalStatus.PAUSED:
      return 'paused'
    case AgentGoalStatus.DONE:
      return 'done'
    // UNSPECIFIED reaches here only for a goal the worker stored before this
    // build understood its status. Reading it as `blocked` keeps the card
    // honest: it says the goal is not progressing, and it never offers Pause
    // for a state nothing can act on.
    default:
      return 'blocked'
  }
}

function goalActionFromProto(a: AgentGoalAction): GoalAction | undefined {
  switch (a) {
    case AgentGoalAction.SET:
      return 'set'
    case AgentGoalAction.CLEAR:
      return 'clear'
    case AgentGoalAction.PAUSE:
      return 'pause'
    case AgentGoalAction.RESUME:
      return 'resume'
    default:
      return undefined
  }
}

/** The wire enum for an action, for the update RPC. */
export function goalActionToProto(action: GoalAction): AgentGoalAction {
  switch (action) {
    case 'set':
      return AgentGoalAction.SET
    case 'clear':
      return AgentGoalAction.CLEAR
    case 'pause':
      return AgentGoalAction.PAUSE
    case 'resume':
      return AgentGoalAction.RESUME
  }
}

/** The label for a status, as the card and the chip show it. */
export function goalStatusLabel(status: GoalStatus): string {
  switch (status) {
    case 'active':
      return 'Active'
    case 'paused':
      return 'Paused'
    case 'done':
      return 'Achieved'
    case 'blocked':
      return 'Needs attention'
  }
}

/**
 * Whether an action is offered, and enabled, right now.
 *
 * Two gates, and they answer different questions. `supportedActions` is what
 * the running AGENT can do at all; the status check is whether the action means
 * anything for the goal in its current state.
 */
export function goalActionAvailable(
  goal: SessionGoal | undefined,
  supportedActions: GoalAction[],
  action: GoalAction,
): boolean {
  if (!supportedActions.includes(action))
    return false
  // Setting a goal is the one action that does not need one to exist -- it is
  // how the first one arrives.
  if (action === 'set')
    return true
  if (!goal)
    return false
  // Pause and resume are opposites, so only one of them can apply at a time
  // however capable the provider is. Offering both would leave one that does
  // nothing on a goal already in that state.
  if (action === 'pause')
    return goal.status === 'active'
  if (action === 'resume')
    return goal.status === 'paused'
  return true
}

/**
 * The reason a control that IS rendered sits disabled, for its tooltip.
 *
 * Only ever asked about an action the agent supports, because an action it does
 * not support is not rendered at all (see goalActionSupported). The two are
 * different situations and deserve different answers: an unsupported action can
 * never become available, so a permanently dead button is noise, while a
 * supported one that the current goal state refuses comes back and must hold
 * its place so the reader can see where it went.
 *
 * A disabled control with no explanation is what this exists to avoid.
 */
export function goalActionDisabledReason(
  goal: SessionGoal | undefined,
  supportedActions: GoalAction[],
  action: GoalAction,
): string | undefined {
  if (goalActionAvailable(goal, supportedActions, action))
    return undefined
  if (!goal)
    return 'This session has no goal'
  if (action === 'pause')
    return 'Only an active goal can be paused'
  if (action === 'resume')
    return 'Only a paused goal can be resumed'
  return 'This agent cannot do that right now'
}

/**
 * Whether the agent offers this action AT ALL, which decides whether the
 * control is rendered.
 *
 * Separate from goalActionAvailable, which decides whether a rendered control is
 * enabled. A provider's gap is permanent -- Claude Code has no pause or resume,
 * and Reasonix can report a goal but never change one -- so a button for it
 * would never light up and is better absent than dead.
 */
export function goalActionSupported(supportedActions: GoalAction[], action: GoalAction): boolean {
  return supportedActions.includes(action)
}
