import type { AgentGoal as ProtoAgentGoal } from '~/generated/proto/leapmux/v1/agent_pb'
import type { TodoItem } from '~/models/todo'
import { GOAL_STATUS_TOKEN } from '~/generated/contracts/worker-vocab'
import { AgentGoalAction, AgentGoalStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { assignDefined } from '~/lib/jsonPick'

// Provider-neutral session goals and their conversions.
//
// A session goal holds one standing objective for the agent.
// The worker normalizes each provider's native goal into the shared message.
// The frontend stores one optional goal for each agent.
//
// This module keeps the goal model and conversions separate from the window store.
// It also reads generated worker tokens and the shared JSON helper.

/**
 * The neutral goal status.
 *
 * `blocked` means that the goal needs attention.
 * `unknown` preserves a goal whose native status has no recognized neutral state.
 * The worker derives `dormant` when no live process serves an agent with a stored goal.
 * The database keeps the last reported status and never stores `dormant`.
 */
export type GoalStatus = 'active' | 'paused' | 'blocked' | 'done' | 'dormant' | 'unknown'

/** One goal action, in the spelling the UI and the RPC share. */
export type GoalAction = 'set' | 'clear' | 'pause' | 'resume'

export interface SessionGoal {
  nativeId?: string
  objective: string
  status: GoalStatus
  /**
   * The provider's native status word or its last-check reason.
   * Examples include `usageLimited` and `notSatisfied`.
   * The neutral status controls the state-dependent actions.
   * This field preserves detail that the neutral status cannot express.
   */
  statusDetail?: string
  createdAt?: string
}

/**
 * The optional counters from the ephemeral session-info channel.
 * The stored goal and these counters arrive through separate messages.
 * Providers report different counter sets.
 * An absent field stays absent because zero would state a value that the provider did not report.
 */
export interface GoalProgress {
  tokensUsed?: number
  tokenBudget?: number
  timeUsedSeconds?: number
  iterations?: number
}

/**
 * The complete goal surface that a component receives:
 * - The stored goal.
 * - The reported counters.
 * - The supported actions.
 * - The optional action handler.
 *
 * One object carries these fields through each component interface.
 * Separate optional props could omit a field without a type error.
 * That omission could remove counters or controls from a visible goal.
 */
export interface GoalSurface {
  /** The stored goal, or undefined when the agent has none. */
  current?: SessionGoal
  /** The volatile counters. Empty rather than absent, so the card reads fields. */
  progress: GoalProgress
  /**
   * The actions that the running agent supports.
   * An empty list hides each unsupported control.
   * The list stays separate from `current` so the empty state can offer Set.
   * A stored goal can remain visible after the process stops and its actions disappear.
   */
  actions: GoalAction[]
  /** Perform one action. Absent makes the surface read-only. */
  onAction?: (action: GoalAction) => void
}

/**
 * Report whether the agent has a stored goal or supports Set.
 * Supported actions alone cannot determine whether there is a goal to display.
 * A read-only goal still needs a surface.
 * An empty surface without Set would offer no route to a first goal.
 * Both surface builders use this shared rule.
 */
export function hasGoalSurface(surface: GoalSurface): boolean {
  return surface.current !== undefined || surface.actions.includes('set')
}

/** Converts the wire goal to the store shape. */
export function protoGoalToStore(g: ProtoAgentGoal): SessionGoal {
  const goal: SessionGoal = {
    objective: g.objective,
    status: goalStatusFromProto(g.status),
  }
  assignDefined(goal, 'nativeId', g.nativeId || undefined)
  assignDefined(goal, 'statusDetail', g.statusDetail || undefined)
  assignDefined(goal, 'createdAt', g.createdAt || undefined)
  return goal
}

/**
 * Convert the running agent's supported actions from the wire enum.
 * Keep the actions separate from the optional goal.
 * The empty state needs the Set capability before a goal exists.
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
    // The explicit zero value retains its existing blocked behavior.
    case AgentGoalStatus.UNSPECIFIED:
    case AgentGoalStatus.BLOCKED:
      return 'blocked'
    case AgentGoalStatus.DONE:
      return 'done'
    case AgentGoalStatus.DORMANT:
      return 'dormant'
    // Preserve an unrecognized status without assigning active or blocked behavior.
    // Dormant uses its own declared status and never reaches this fallback.
    case AgentGoalStatus.UNKNOWN:
    default:
      return 'unknown'
  }
}

/**
 * Read the generated status token from a stored notification payload.
 * The token vocabulary differs from the proto enum that the worker broadcasts.
 * Use the generated constants so the shared contract remains the source of truth.
 * An absent or unrecognized token returns undefined.
 * The caller then chooses its explicit fallback.
 */
export function goalStatusFromWire(token: string | undefined): GoalStatus | undefined {
  switch (token) {
    case GOAL_STATUS_TOKEN.Active:
      return 'active'
    case GOAL_STATUS_TOKEN.Paused:
      return 'paused'
    case GOAL_STATUS_TOKEN.Blocked:
      return 'blocked'
    case GOAL_STATUS_TOKEN.Done:
      return 'done'
    case GOAL_STATUS_TOKEN.Dormant:
      return 'dormant'
    case GOAL_STATUS_TOKEN.Unknown:
      return 'unknown'
    default:
      return undefined
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
    case 'dormant':
      return 'Not running'
    case 'unknown':
      return 'Unknown'
  }
}

/**
 * The shared result for a goal action:
 * - Hide an action that the provider does not support.
 * - Enable an action that the current goal permits.
 * - Disable a supported action when the current goal refuses it.
 *
 * The disabled result carries the reason for the tooltip.
 * The control and its tooltip must use the same result.
 */
export type GoalActionState
  = | { kind: 'hidden' }
    | { kind: 'enabled' }
    | { kind: 'disabled', reason: string }

/**
 * Report an action's state from one goal surface.
 * The goal and supported actions describe the same agent.
 * Separate arguments could pair one agent's goal with another agent's actions.
 * This rule reads neither the counters nor the handler, so its type requires only the fields that it reads.
 */
export function goalActionState(
  surface: Pick<GoalSurface, 'current' | 'actions'>,
  action: GoalAction,
): GoalActionState {
  const goal = surface.current
  if (!surface.actions.includes(action))
    return { kind: 'hidden' }
  // Set creates the first goal and therefore requires no current goal.
  if (action === 'set')
    return { kind: 'enabled' }
  if (!goal)
    return { kind: 'disabled', reason: 'This session has no goal' }
  // Pause requires an active goal. Resume requires a paused goal.
  // Keep a refused supported action visible with its reason.
  if (action === 'pause' && goal.status !== 'active')
    return { kind: 'disabled', reason: 'Only an active goal can be paused' }
  if (action === 'resume' && goal.status !== 'paused')
    return { kind: 'disabled', reason: 'Only a paused goal can be resumed' }
  return { kind: 'enabled' }
}

/**
 * Report whether the Goals & To-dos section has content to display.
 * A to-do list keeps the section visible without a goal surface.
 * A goal surface keeps the section visible even when its current goal is absent.
 * That empty card can offer the route to a first goal.
 *
 * The caller supplies the surface that the section renders.
 * The surface builders use hasGoalSurface to omit a card with no goal and no Set capability.
 * This rule belongs beside the goal model because the goal can keep an empty to-do section visible.
 */
export function shouldShowGoalsAndTodosSection(
  todos: TodoItem[],
  goal: GoalSurface | undefined,
): boolean {
  return todos.length > 0 || goal !== undefined
}
