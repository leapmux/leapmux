import type { GoalSurface } from './chatGoal'
import type { AgentGoal as ProtoAgentGoal } from '~/generated/proto/leapmux/v1/agent_pb'
import { describe, expect, it } from 'vitest'
import { AgentGoalAction, AgentGoalStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import {
  goalActionsFromProto,
  goalActionState,
  goalActionToProto,
  goalStatusFromWire,
  goalStatusLabel,
  protoGoalToStore,
} from './chatGoal'

function protoGoal(over: Partial<ProtoAgentGoal> = {}): ProtoAgentGoal {
  return {
    objective: 'ship it',
    status: AgentGoalStatus.ACTIVE,
    statusDetail: '',
    createdAt: '',
    updatedAt: '',
    ...over,
  } as ProtoAgentGoal
}

describe('protoGoalToStore', () => {
  it('carries the objective, the neutral status and the provider detail', () => {
    const goal = protoGoalToStore(protoGoal({
      objective: 'every test passes',
      status: AgentGoalStatus.BLOCKED,
      statusDetail: 'usageLimited',
    }))
    expect(goal.objective).toBe('every test passes')
    expect(goal.status).toBe('blocked')
    expect(goal.statusDetail).toBe('usageLimited')
  })

  it('collapses an empty detail to undefined so shallow compares stay stable', () => {
    expect(protoGoalToStore(protoGoal({ statusDetail: '' })).statusDetail).toBeUndefined()
  })

  // A goal stored before this build understood its status reads as BLOCKED, not
  // ACTIVE: an unreadable status is one nothing can act on, so the card must not
  // offer Pause for it.
  it('reads an unspecified status as blocked', () => {
    expect(protoGoalToStore(protoGoal({ status: AgentGoalStatus.UNSPECIFIED })).status).toBe('blocked')
  })

  it('maps every status the worker can send', () => {
    const cases: [AgentGoalStatus, string][] = [
      [AgentGoalStatus.ACTIVE, 'active'],
      [AgentGoalStatus.PAUSED, 'paused'],
      [AgentGoalStatus.BLOCKED, 'blocked'],
      [AgentGoalStatus.DONE, 'done'],
      [AgentGoalStatus.DORMANT, 'dormant'],
    ]
    for (const [wire, want] of cases)
      expect(protoGoalToStore(protoGoal({ status: wire })).status).toBe(want)
  })
})

describe('goalActionsFromProto', () => {
  it('maps the four actions and drops an unspecified one', () => {
    expect(goalActionsFromProto([
      AgentGoalAction.SET,
      AgentGoalAction.CLEAR,
      AgentGoalAction.PAUSE,
      AgentGoalAction.RESUME,
      AgentGoalAction.UNSPECIFIED,
    ])).toEqual(['set', 'clear', 'pause', 'resume'])
  })

  it('round-trips through goalActionToProto', () => {
    for (const action of ['set', 'clear', 'pause', 'resume'] as const)
      expect(goalActionsFromProto([goalActionToProto(action)])).toEqual([action])
  })
})

describe('goalActionState', () => {
  const all = ['set', 'clear', 'pause', 'resume'] as const
  const active = protoGoalToStore(protoGoal({ status: AgentGoalStatus.ACTIVE }))
  const paused = protoGoalToStore(protoGoal({ status: AgentGoalStatus.PAUSED }))

  // A provider's gap is PERMANENT -- Claude Code has no pause -- so its button
  // would never light up and is better absent than dead.
  it('hides an action the agent does not support', () => {
    expect(goalActionState({ current: active, actions: ['set', 'clear'] }, 'pause')).toEqual({ kind: 'hidden' })
    expect(goalActionState({ current: undefined, actions: [] }, 'set')).toEqual({ kind: 'hidden' })
  })

  // Pause and resume are opposites: offering both would leave one that does
  // nothing on a goal already in that state. The refused one stays RENDERED,
  // because it comes back.
  it('enables pause only for an active goal, and resume only for a paused one', () => {
    expect(goalActionState({ current: active, actions: [...all] }, 'pause')).toEqual({ kind: 'enabled' })
    expect(goalActionState({ current: active, actions: [...all] }, 'resume'))
      .toEqual({ kind: 'disabled', reason: 'Only a paused goal can be resumed' })
    expect(goalActionState({ current: paused, actions: [...all] }, 'resume')).toEqual({ kind: 'enabled' })
    expect(goalActionState({ current: paused, actions: [...all] }, 'pause'))
      .toEqual({ kind: 'disabled', reason: 'Only an active goal can be paused' })
  })

  // The one action that does not need a goal to exist -- it is how the first one
  // arrives, and the empty state's button depends on exactly this.
  it('enables set when there is no goal at all', () => {
    expect(goalActionState({ current: undefined, actions: ['set'] }, 'set')).toEqual({ kind: 'enabled' })
    expect(goalActionState({ current: undefined, actions: ['set', 'clear'] }, 'clear'))
      .toEqual({ kind: 'disabled', reason: 'This session has no goal' })
  })

  // A dormant goal is waiting, not active and not paused, so neither verb
  // applies -- and each says which state it needs rather than going silent.
  it('refuses both pause and resume for a dormant goal, with a reason', () => {
    const dormant = protoGoalToStore(protoGoal({ status: AgentGoalStatus.DORMANT }))
    expect(goalActionState({ current: dormant, actions: [...all] }, 'pause'))
      .toEqual({ kind: 'disabled', reason: 'Only an active goal can be paused' })
    expect(goalActionState({ current: dormant, actions: [...all] }, 'resume'))
      .toEqual({ kind: 'disabled', reason: 'Only a paused goal can be resumed' })
    expect(goalActionState({ current: dormant, actions: [...all] }, 'clear')).toEqual({ kind: 'enabled' })
  })

  /**
   * Both fields come from ONE surface, which is what the signature is for. A
   * caller cannot pair one agent's goal with another agent's verb list, because
   * there is no second argument to pair it with.
   */
  it('reads the goal and the verb list from the same surface', () => {
    const surface: GoalSurface = { current: paused, progress: {}, actions: [...all] }
    expect(goalActionState(surface, 'resume')).toEqual({ kind: 'enabled' })
    // Narrow that ONE surface's verbs, and the same goal now hides the verb.
    expect(goalActionState({ ...surface, actions: ['set'] }, 'resume')).toEqual({ kind: 'hidden' })
  })
})

describe('goalStatusFromWire', () => {
  // The transcript renderer reads the token the worker PERSISTS, which is a
  // different vocabulary from the proto enum it broadcasts.
  it('reads every stored token', () => {
    expect(goalStatusFromWire('active')).toBe('active')
    expect(goalStatusFromWire('paused')).toBe('paused')
    expect(goalStatusFromWire('blocked')).toBe('blocked')
    expect(goalStatusFromWire('done')).toBe('done')
    expect(goalStatusFromWire('dormant')).toBe('dormant')
  })

  // Undefined rather than a guess, so the caller can fall back instead of
  // asserting something the worker did not say.
  it('answers undefined for a token it does not know', () => {
    expect(goalStatusFromWire('')).toBeUndefined()
    expect(goalStatusFromWire(undefined)).toBeUndefined()
    expect(goalStatusFromWire('somethingNew')).toBeUndefined()
  })
})

describe('goalStatusLabel', () => {
  it('names every status', () => {
    expect(goalStatusLabel('active')).toBe('Active')
    expect(goalStatusLabel('paused')).toBe('Paused')
    expect(goalStatusLabel('done')).toBe('Achieved')
    expect(goalStatusLabel('blocked')).toBe('Needs attention')
    // A dormant goal is WAITING, not failing. Labelling it "Needs attention"
    // would report a fault every time a worker restarts.
    expect(goalStatusLabel('dormant')).toBe('Not running')
  })
})
