import type { AgentGoal as ProtoAgentGoal } from '~/generated/proto/leapmux/v1/agent_pb'
import { GOAL_STATUS_TOKEN } from '~/generated/contracts/worker-vocab'
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

/**
 * The neutral status.
 *
 * `blocked` means "stopped and needs you". `dormant` means "the objective is
 * stored and no live process pursues it" -- the worker DERIVES it, from whether
 * a process serves the agent, and no provider ever reports it. It is therefore
 * the one status no row holds.
 */
export type GoalStatus = 'active' | 'paused' | 'blocked' | 'done' | 'dormant'

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

/**
 * One agent's session goal, as a component receives it: the stored goal, the
 * volatile counters beside it, what the running agent can do with it, and the
 * way to ask for one of those things.
 *
 * The four travel TOGETHER because none of them renders the card alone. The
 * counters measure the goal, the actions decide which controls are live, and
 * the handler is what makes them do anything -- so a component that holds three
 * of the four draws a card that is wrong rather than incomplete.
 *
 * One parameter object rather than four props, because these crossed seven
 * component interfaces under three naming schemes (`goal*`, `activeGoal*`, and
 * GoalCard's bare `progress`). Threading four fields through seven hops meant
 * every hop restated all four and could drop one silently: an omitted optional
 * prop is not a type error, and the symptom is a control that never arms. One
 * field cannot be partly forwarded.
 */
export interface GoalSurface {
  /** The stored goal, or undefined when the agent has none. */
  current?: SessionGoal
  /** The volatile counters. Empty rather than absent, so the card reads fields. */
  progress: GoalProgress
  /**
   * What the RUNNING agent can do. Empty when no process is running, or when
   * the provider reports a goal but cannot change one -- either way every
   * control is disabled with the reason on it.
   *
   * Apart from `current` because it must exist when a goal does not: the empty
   * state's "Set a goal" button asks exactly this question. A goal also SURVIVES
   * a restart -- the projection reads it as dormant and keeps the objective --
   * so the card can still say what the agent attempted while nothing can act on
   * it.
   */
  actions: GoalAction[]
  /** Perform one action. Absent makes the surface read-only. */
  onAction?: (action: GoalAction) => void
}

/** The surface for an agent with no goal, no counters and no live process. */
export const EMPTY_GOAL_SURFACE: GoalSurface = { progress: {}, actions: [] }

/** Converts the wire goal to the store shape. */
export function protoGoalToStore(g: ProtoAgentGoal): SessionGoal {
  return {
    objective: g.objective,
    status: goalStatusFromProto(g.status),
    statusDetail: g.statusDetail || undefined,
    createdAt: g.createdAt || undefined,
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
    case AgentGoalStatus.DORMANT:
      return 'dormant'
    // UNSPECIFIED reaches here only for a status token this build does not
    // know, which today means a goal stored by a NEWER worker. Reading it as
    // `blocked` keeps the card honest: it says the goal is not progressing, and
    // it never offers Pause for a state nothing can act on.
    //
    // A goal waiting for its session to resume does NOT arrive here -- it
    // carries `dormant`, its own state, precisely so it is not reported as a
    // fault.
    default:
      return 'blocked'
  }
}

/**
 * The status from the token the worker PERSISTS, as opposed to the proto enum
 * it broadcasts.
 *
 * The transcript renderer reads a stored `goal_status` string out of a
 * notification payload, and this is the only place that vocabulary is spelled,
 * so a token renamed on the Go side fails here instead of silently degrading
 * every transcript row. `subagentEndedEntry` narrows its sibling payload the
 * same way and for the same reason.
 *
 * An unknown token answers undefined rather than guessing, so the caller can
 * fall back rather than assert something the worker did not say.
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
  }
}

/**
 * What the UI should do with one goal action, right now.
 *
 * ONE question, because the card previously asked two and they could disagree:
 * a control's enabled state came from one predicate and its tooltip from
 * another, both re-deriving the same rules over the same two inputs.
 *
 * The three answers are distinct situations, and each deserves its own
 * treatment. A provider's gap is PERMANENT -- Claude Code has no pause or
 * resume, and Reasonix can report a goal but never change one -- so a control
 * for it would never light up and is better absent than dead. A control the
 * current goal state refuses comes BACK, so it holds its place and says why. A
 * disabled control with no explanation is what this exists to avoid.
 */
export type GoalActionState
  = | { kind: 'hidden' }
    | { kind: 'enabled' }
    | { kind: 'disabled', reason: string }

/**
 * Whether one verb is hidden, live, or refused for a goal surface.
 *
 * It takes the SURFACE, not a goal and an action list side by side. The two
 * fields travel together for a reason -- they describe one agent -- and two
 * parameters let a caller pair one agent's goal with another agent's verbs,
 * which is exactly the split `GoalSurface` exists to remove. `Pick` rather than
 * the whole surface, because the answer never depends on the counters or the
 * handler, and a test that had to build a `progress` field this never reads
 * would say otherwise.
 */
export function goalActionState(
  surface: Pick<GoalSurface, 'current' | 'actions'>,
  action: GoalAction,
): GoalActionState {
  const goal = surface.current
  if (!surface.actions.includes(action))
    return { kind: 'hidden' }
  // Setting a goal is the one action that does not need one to exist -- it is
  // how the first one arrives.
  if (action === 'set')
    return { kind: 'enabled' }
  if (!goal)
    return { kind: 'disabled', reason: 'This session has no goal' }
  // Pause and resume are opposites, so only one of them applies at a time
  // however capable the provider is. Offering both would leave one that does
  // nothing on a goal already in that state.
  if (action === 'pause' && goal.status !== 'active')
    return { kind: 'disabled', reason: 'Only an active goal can be paused' }
  if (action === 'resume' && goal.status !== 'paused')
    return { kind: 'disabled', reason: 'Only a paused goal can be resumed' }
  return { kind: 'enabled' }
}

/**
 * Whether this agent has a goal surface at all: a goal to show, or the ability
 * to be given one.
 *
 * The rule lives HERE rather than at its call sites because three of them ask
 * it -- the sidebar section's visibility, the work panel's tab list, and the
 * panel's empty state -- and a rule spelled three times can be spelled three
 * ways. A surface that appears in the sidebar but has no Goal tab, or a Goal
 * tab that can never hold anything, are both what that drift looks like.
 */
export function hasGoalSurface(surface: GoalSurface): boolean {
  return surface.current !== undefined || surface.actions.includes('set')
}
