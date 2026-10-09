import type { MessageInitShape } from '@bufbuild/protobuf'
import type { GoalSurface } from './chatGoal'
import type { AgentGoal as ProtoAgentGoal } from '~/generated/proto/leapmux/v1/agent_pb'
import type { TodoItem } from '~/models/todo'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { GOAL_STATUS_TOKEN } from '~/generated/contracts/worker-vocab'
import { AgentGoalAction, AgentGoalSchema, AgentGoalStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import {
  goalActionsFromProto,
  goalActionState,
  goalActionToProto,
  goalStatusFromWire,
  goalStatusLabel,
  protoGoalToStore,
  shouldShowGoalsAndTodosSection,
} from './chatGoal'

function protoGoal(over: MessageInitShape<typeof AgentGoalSchema> = {}): ProtoAgentGoal {
  return create(AgentGoalSchema, {
    objective: 'ship it',
    status: AgentGoalStatus.ACTIVE,
    statusDetail: '',
    createdAt: '',
    ...over,
  })
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

  // This explicit UNSPECIFIED zero-value case retains blocked behavior.
  // UNKNOWN and unrecognized numeric statuses have separate neutral Unknown cases.
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

  // Hide an unsupported action instead of displaying a control that cannot run.
  it('hides an action the agent does not support', () => {
    expect(goalActionState({ current: active, actions: ['set', 'clear'] }, 'pause')).toEqual({ kind: 'hidden' })
    // An absent current key and an explicit undefined key both mean that there is no goal.
    expect(goalActionState({ actions: [] }, 'set')).toEqual({ kind: 'hidden' })
  })

  // Pause and Resume require different states.
  // A supported action that the current state refuses stays visible with its reason.
  it('enables pause only for an active goal, and resume only for a paused one', () => {
    expect(goalActionState({ current: active, actions: [...all] }, 'pause')).toEqual({ kind: 'enabled' })
    expect(goalActionState({ current: active, actions: [...all] }, 'resume'))
      .toEqual({ kind: 'disabled', reason: 'Only a paused goal can be resumed' })
    expect(goalActionState({ current: paused, actions: [...all] }, 'resume')).toEqual({ kind: 'enabled' })
    expect(goalActionState({ current: paused, actions: [...all] }, 'pause'))
      .toEqual({ kind: 'disabled', reason: 'Only an active goal can be paused' })
  })

  // Set requires no current goal because it creates the first goal.
  it('enables set when there is no goal at all', () => {
    expect(goalActionState({ actions: ['set'] }, 'set')).toEqual({ kind: 'enabled' })
    expect(goalActionState({ actions: ['set', 'clear'] }, 'clear'))
      .toEqual({ kind: 'disabled', reason: 'This session has no goal' })
  })

  // Dormant is neither active nor paused.
  // Refuse both actions with their existing state requirements.
  it('refuses both pause and resume for a dormant goal, with a reason', () => {
    const dormant = protoGoalToStore(protoGoal({ status: AgentGoalStatus.DORMANT }))
    expect(goalActionState({ current: dormant, actions: [...all] }, 'pause'))
      .toEqual({ kind: 'disabled', reason: 'Only an active goal can be paused' })
    expect(goalActionState({ current: dormant, actions: [...all] }, 'resume'))
      .toEqual({ kind: 'disabled', reason: 'Only a paused goal can be resumed' })
    expect(goalActionState({ current: dormant, actions: [...all] }, 'clear')).toEqual({ kind: 'enabled' })
  })

  /**
   * The function receives the goal and actions from one surface.
   * It has no separate argument that could supply another agent's action list.
   */
  it('reads the goal and the verb list from the same surface', () => {
    const surface: GoalSurface = { current: paused, progress: {}, actions: [...all] }
    expect(goalActionState(surface, 'resume')).toEqual({ kind: 'enabled' })
    // Remove the action from the same surface's supported list.
    expect(goalActionState({ ...surface, actions: ['set'] }, 'resume')).toEqual({ kind: 'hidden' })
  })
})

describe('goalStatusFromWire', () => {
  // The transcript uses a stored status token rather than the broadcast proto enum.
  it('reads every stored token', () => {
    expect(goalStatusFromWire('active')).toBe('active')
    expect(goalStatusFromWire('paused')).toBe('paused')
    expect(goalStatusFromWire('blocked')).toBe('blocked')
    expect(goalStatusFromWire('done')).toBe('done')
    expect(goalStatusFromWire('dormant')).toBe('dormant')
  })

  // An absent or unrecognized token returns undefined so the caller can choose its fallback.
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
    // Dormant reports that no process serves the goal.
    // It must not report a fault during a worker restart.
    expect(goalStatusLabel('dormant')).toBe('Not running')
  })
})

describe('shouldShowGoalsAndTodosSection', () => {
  const oneTodo: TodoItem = { rowKey: '1', content: 'Ship', status: 'pending', activeForm: '' }
  const settable: GoalSurface = { progress: {}, actions: ['set'] }

  it('shows the section for a to-do', () => {
    expect(shouldShowGoalsAndTodosSection([oneTodo], undefined)).toBe(true)
  })

  it('shows the section for a goal surface without any to-dos', () => {
    expect(shouldShowGoalsAndTodosSection([], settable)).toBe(true)
  })

  it('hides the section when both parts are absent', () => {
    expect(shouldShowGoalsAndTodosSection([], undefined)).toBe(false)
  })

  // A goal surface keeps the section visible, regardless of its current goal's state.
  it('shows the section for a goal that exists but cannot be changed', () => {
    const readOnly: GoalSurface = {
      current: { objective: 'Ship the release', status: 'active' },
      progress: {},
      actions: [],
    }
    expect(shouldShowGoalsAndTodosSection([], readOnly)).toBe(true)
  })
})

describe('shared unknown goals', () => {
  it.each(['provider-future-state', 'active', 'blocked', ''])('retains native detail %j under the explicit unknown status', (statusDetail) => {
    const wire = create(AgentGoalSchema, {
      nativeId: 'native-unknown-goal',
      objective: 'Ship the exact objective',
      status: AgentGoalStatus.UNKNOWN,
      statusDetail,
      createdAt: '2026-10-09T00:00:00.000Z',
    })
    const converted = protoGoalToStore(wire)
    expect(converted).toEqual({
      nativeId: 'native-unknown-goal',
      objective: 'Ship the exact objective',
      status: 'unknown',
      ...(statusDetail === '' ? {} : { statusDetail }),
      createdAt: '2026-10-09T00:00:00.000Z',
    })
    expect(wire.status).toBe(AgentGoalStatus.UNKNOWN)
    expect(wire.statusDetail).toBe(statusDetail)
  })

  it.each([-2147483648, -1, 99, 2147483647])('reports neutral unknown for an unrecognized numeric status %s', (status) => {
    const wire = create(AgentGoalSchema, { objective: 'Keep the goal', status, statusDetail: 'opaque-native-state' })
    const converted = protoGoalToStore(wire)
    expect(converted.status).toBe('unknown')
    expect(converted.statusDetail).toBe('opaque-native-state')
    expect(goalStatusLabel(converted.status)).toBe('Unknown')
  })

  it('reads the generated unknown token without inferring an active or blocked state', () => {
    expect(goalStatusFromWire(GOAL_STATUS_TOKEN.Unknown)).toBe('unknown')
    const converted = protoGoalToStore(create(AgentGoalSchema, { status: AgentGoalStatus.UNKNOWN }))
    expect(goalStatusLabel(converted.status)).toBe('Unknown')
    expect(converted.objective).toBe('')
    expect(converted.statusDetail).toBeUndefined()
  })

  it('retains supported clear and replace controls while refusing pause and resume for unknown', () => {
    const current = protoGoalToStore(create(AgentGoalSchema, { objective: 'Keep the goal', status: AgentGoalStatus.UNKNOWN }))
    const surface: GoalSurface = { current, progress: {}, actions: ['set', 'clear', 'pause', 'resume'] }
    expect(goalActionState(surface, 'pause')).toEqual({ kind: 'disabled', reason: 'Only an active goal can be paused' })
    expect(goalActionState(surface, 'resume')).toEqual({ kind: 'disabled', reason: 'Only a paused goal can be resumed' })
    expect(goalActionState(surface, 'set')).toEqual({ kind: 'enabled' })
    expect(goalActionState(surface, 'clear')).toEqual({ kind: 'enabled' })
    expect(shouldShowGoalsAndTodosSection([], surface)).toBe(true)
    expect(current.status).toBe('unknown')
  })
})
