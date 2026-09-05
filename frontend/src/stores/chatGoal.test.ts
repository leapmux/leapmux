import type { AgentGoal as ProtoAgentGoal } from '~/generated/proto/leapmux/v1/agent_pb'
import { describe, expect, it } from 'vitest'
import { AgentGoalAction, AgentGoalStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import {
  goalActionAvailable,
  goalActionDisabledReason,
  goalActionsFromProto,
  goalActionSupported,
  goalActionToProto,
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

describe('goalActionAvailable', () => {
  const all = ['set', 'clear', 'pause', 'resume'] as const
  const active = protoGoalToStore(protoGoal({ status: AgentGoalStatus.ACTIVE }))
  const paused = protoGoalToStore(protoGoal({ status: AgentGoalStatus.PAUSED }))

  it('refuses an action the agent does not support', () => {
    expect(goalActionAvailable(active, ['set', 'clear'], 'pause')).toBe(false)
  })

  // Pause and resume are opposites: offering both would leave one that does
  // nothing on a goal already in that state.
  it('offers pause only for an active goal, and resume only for a paused one', () => {
    expect(goalActionAvailable(active, [...all], 'pause')).toBe(true)
    expect(goalActionAvailable(active, [...all], 'resume')).toBe(false)
    expect(goalActionAvailable(paused, [...all], 'resume')).toBe(true)
    expect(goalActionAvailable(paused, [...all], 'pause')).toBe(false)
  })

  // The one action that does not need a goal to exist -- it is how the first one
  // arrives, and the empty state's button depends on exactly this.
  it('offers set when there is no goal at all', () => {
    expect(goalActionAvailable(undefined, ['set'], 'set')).toBe(true)
    expect(goalActionAvailable(undefined, ['set', 'clear'], 'clear')).toBe(false)
  })
})

describe('goalActionSupported', () => {
  /**
   * Decides whether a control is RENDERED, which is a different question from
   * whether it is enabled. A provider's gap is permanent -- Claude Code has no
   * pause -- so its button would never light up and is better absent.
   */
  it('reports only what the agent offers at all', () => {
    expect(goalActionSupported(['set', 'clear'], 'clear')).toBe(true)
    expect(goalActionSupported(['set', 'clear'], 'pause')).toBe(false)
    expect(goalActionSupported([], 'set')).toBe(false)
  })
})

describe('goalActionDisabledReason', () => {
  const active = protoGoalToStore(protoGoal({ status: AgentGoalStatus.ACTIVE }))

  it('says nothing for an action that is available', () => {
    expect(goalActionDisabledReason(active, ['pause'], 'pause')).toBeUndefined()
  })

  // Only asked about a SUPPORTED action, because an unsupported one is never
  // rendered -- so every answer here is about the goal's current state.
  it('explains a pause that the goal state refuses', () => {
    const paused = protoGoalToStore(protoGoal({ status: AgentGoalStatus.PAUSED }))
    expect(goalActionDisabledReason(paused, ['pause', 'resume'], 'pause'))
      .toBe('Only an active goal can be paused')
    expect(goalActionDisabledReason(paused, ['pause', 'resume'], 'resume')).toBeUndefined()
  })

  it('explains an action that needs a goal when there is none', () => {
    expect(goalActionDisabledReason(undefined, ['clear'], 'clear')).toBe('This session has no goal')
  })
})

describe('goalStatusLabel', () => {
  it('names every status', () => {
    expect(goalStatusLabel('active')).toBe('Active')
    expect(goalStatusLabel('paused')).toBe('Paused')
    expect(goalStatusLabel('done')).toBe('Achieved')
    expect(goalStatusLabel('blocked')).toBe('Needs attention')
  })
})
