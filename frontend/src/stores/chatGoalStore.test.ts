import type { MessageInitShape } from '@bufbuild/protobuf'
import type { GoalReplacementResult } from './chatGoalStore'
import type { AgentGoal as ProtoAgentGoal } from '~/generated/proto/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { unwrap } from 'solid-js/store'
import { describe, expect, it, onTestFinished } from 'vitest'
import { AgentGoalAction, AgentGoalSchema, AgentGoalStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { createGoalStore } from './chatGoalStore'

// The worker sends fixed-width UTC timestamps. These constants identify older and newer
// reports for the ordering tests.

const STAMP_1 = '2026-09-06T10:00:00.000Z'
const STAMP_2 = '2026-09-06T10:00:01.000Z'
const STAMP_3 = '2026-09-06T10:00:02.000Z'

function protoGoal(over: MessageInitShape<typeof AgentGoalSchema> = {}): ProtoAgentGoal {
  return create(AgentGoalSchema, {
    objective: 'ship it',
    status: AgentGoalStatus.ACTIVE,
    statusDetail: '',
    createdAt: '',
    ...over,
  })
}

describe('createGoalStore', () => {
  it.each([
    { change: 'stale', expected: 'stale' },
    { change: 'same goal', expected: 'applied' },
    { change: 'replacement', expected: 'progress-cleared' },
    { change: 'clear', expected: 'progress-cleared' },
  ] as const)('reports the actual outcome for $change', ({ change, expected }) => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.replace('outcome', protoGoal({ nativeId: 'first', createdAt: 'first-time' }), [], STAMP_2)
      store.setProgress('outcome', { tokensUsed: 150 })
      const next = change === 'clear'
        ? undefined
        : protoGoal({ nativeId: change === 'replacement' ? 'second' : 'first', createdAt: 'first-time' })
      const result = store.replace('outcome', next, [AgentGoalAction.CLEAR], change === 'stale' ? STAMP_1 : STAMP_3)
      const wanted: GoalReplacementResult = expected
      expect(result).toBe(wanted)
      if (expected === 'progress-cleared')
        expect(store.progress('outcome')).toEqual({})
      else
        expect(store.progress('outcome').tokensUsed).toBe(150)
      dispose()
    })
  })

  it('resets progress for a different native goal with the same text and time', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.replace('a', protoGoal({ nativeId: 'first', createdAt: 'same-time' }), [], STAMP_1)
      store.setProgress('a', { tokensUsed: 100 })
      store.replace('a', protoGoal({ nativeId: 'second', createdAt: 'same-time' }), [], STAMP_2)
      expect(store.get('a')?.nativeId).toBe('second')
      expect(store.progress('a')).toEqual({})
      dispose()
    })
  })

  it('keeps progress when a report adds the native identity of the same goal', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.replace('a', protoGoal({ createdAt: 'same-time' }), [], STAMP_1)
      store.setProgress('a', { tokensUsed: 100 })
      store.replace('a', protoGoal({ nativeId: 'native', createdAt: 'same-time' }), [], STAMP_2)
      expect(store.progress('a').tokensUsed).toBe(100)
      dispose()
    })
  })

  it('holds a goal per agent and reports undefined for one with none', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.replace('a', protoGoal({ objective: 'first' }), [], STAMP_1)
      expect(store.get('a')?.objective).toBe('first')
      expect(store.get('b')).toBeUndefined()
      dispose()
    })
  })

  /**
   * A plain set replaces the stored goal object. Reconciliation must retain that object when
   * only statusDetail changes, so the card keeps its tooltip and animation.
   */
  it('keeps the stored object identical when a field changes', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.replace('a', protoGoal({ objective: 'same', statusDetail: 'active' }), [], STAMP_1)
      const before = unwrap(store.get('a')!)
      store.replace('a', protoGoal({ objective: 'same', statusDetail: 'verifying' }), [], STAMP_2)
      const after = unwrap(store.get('a')!)
      expect(after).toBe(before)
      expect(store.get('a')?.statusDetail).toBe('verifying')
      dispose()
    })
  })

  it('drops the goal when the agent has none', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.replace('a', protoGoal(), [], STAMP_1)
      store.replace('a', undefined, [AgentGoalAction.SET], STAMP_2)
      expect(store.get('a')).toBeUndefined()
      // Supported actions remain after the goal clears.
      // The empty state can then offer Set.
      expect(store.supportedActions('a')).toEqual(['set'])
      dispose()
    })
  })

  /**
   * Merge supplied counters. The provider can omit counters and the session-info channel
   * suppresses unchanged fields. A token update must retain the prior iteration count.
   */
  it('merges progress instead of replacing it', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.setProgress('a', { tokensUsed: 100, iterations: 3 })
      store.setProgress('a', { tokensUsed: 250 })
      expect(store.progress('a')).toEqual({ tokensUsed: 250, iterations: 3 })
      dispose()
    })
  })

  /**
   * Progress belongs to one goal. A fresh goal must not display its predecessor's counters while
   * it waits for its next update.
   */
  it('drops the counters when the goal is cleared', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.replace('a', protoGoal({ createdAt: 't1' }), [], STAMP_1)
      store.setProgress('a', { tokensUsed: 900 })
      store.replace('a', undefined, [], STAMP_2)
      expect(store.progress('a')).toEqual({})
      dispose()
    })
  })

  // Codex supplies no native goal ID. A restarted goal with the same objective differs through createdAt.
  it('drops the counters when the goal is replaced', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.replace('a', protoGoal({ objective: 'same', createdAt: 't1' }), [], STAMP_1)
      store.setProgress('a', { tokensUsed: 900 })
      store.replace('a', protoGoal({ objective: 'same', createdAt: 't2' }), [], STAMP_2)
      expect(store.progress('a')).toEqual({})
      dispose()
    })
  })

  it('keeps the counters across an update to the same goal', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.replace('a', protoGoal({ createdAt: 't1', statusDetail: 'active' }), [], STAMP_1)
      store.setProgress('a', { tokensUsed: 900 })
      store.replace('a', protoGoal({ createdAt: 't1', statusDetail: 'verifying' }), [], STAMP_2)
      expect(store.progress('a')).toEqual({ tokensUsed: 900 })
      dispose()
    })
  })

  /**
   * Three sources supply goals:
   * - Live events.
   * - Replay.
   * - Initial history.
   * A history reply can arrive after a newer clear. Reject that older reply at the shared write boundary.
   */
  it('drops a GOAL older than the one already applied', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.replace('a', protoGoal({ objective: 'current' }), [AgentGoalAction.CLEAR], STAMP_2)
      // The history request read the older goal before this live update.
      store.replace('a', protoGoal({ objective: 'stale' }), [AgentGoalAction.CLEAR], STAMP_1)
      expect(store.get('a')?.objective).toBe('current')
      dispose()
    })
  })

  /**
   * An equal timestamp can supply new supported actions for the unchanged goal. Reject only a
   * strictly older timestamp.
   */
  it('applies the supported actions from an answer whose stamp ties', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.replace('a', protoGoal({ objective: 'current' }), [], STAMP_2)
      expect(store.supportedActions('a')).toEqual([])

      store.replace('a', protoGoal({ objective: 'current' }), [AgentGoalAction.PAUSE], STAMP_2)

      expect(store.supportedActions('a')).toEqual(['pause'])
      dispose()
    })
  })

  /**
   * An older history reply can carry actions from before the provider exit. Reject those actions
   * so the stopped process offers no Pause or Clear control.
   */
  it('drops the supported actions from an answer older than the one applied', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.replace('a', protoGoal({ objective: 'current' }), [], STAMP_2)

      store.replace('a', protoGoal({ objective: 'current' }), [AgentGoalAction.PAUSE], STAMP_1)

      expect(store.supportedActions('a')).toEqual([])
      expect(store.get('a')?.objective).toBe('current')
      dispose()
    })
  })

  it('does not let a stale answer resurrect a cleared goal', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.replace('a', protoGoal({ objective: 'ship it' }), [], STAMP_1)
      store.replace('a', undefined, [AgentGoalAction.SET], STAMP_3)
      store.replace('a', protoGoal({ objective: 'ship it' }), [], STAMP_2)
      expect(store.get('a')).toBeUndefined()
      dispose()
    })
  })

  it('applies an answer newer than the one already applied', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.replace('a', protoGoal({ objective: 'first' }), [], STAMP_1)
      store.replace('a', protoGoal({ objective: 'second' }), [], STAMP_2)
      expect(store.get('a')?.objective).toBe('second')
      dispose()
    })
  })

  /**
   * Agent registration republishes supported actions beside an unchanged goal. Apply the equal
   * timestamp so its controls can become available.
   */
  it('applies an answer whose stamp matches, to pick up new capabilities', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.replace('a', protoGoal({ objective: 'ship it' }), [], STAMP_1)
      expect(store.supportedActions('a')).toEqual([])
      store.replace('a', protoGoal({ objective: 'ship it' }), [AgentGoalAction.PAUSE], STAMP_1)
      expect(store.supportedActions('a')).toEqual(['pause'])
      dispose()
    })
  })

  // An agent without a previous goal carries an empty timestamp.
  // The first real timestamp must not compare as older than that empty value.
  it('accepts the first stamped answer after an empty one', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.replace('a', undefined, [AgentGoalAction.SET], '')
      store.replace('a', protoGoal({ objective: 'the first goal' }), [], STAMP_1)
      expect(store.get('a')?.objective).toBe('the first goal')
      dispose()
    })
  })

  // Keep timestamps separate for each agent.
  // One agent's newer reply must not refuse another agent's current reply, even when its timestamp is older.
  it('orders each agent on its own stamp', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.replace('a', protoGoal({ objective: 'for a' }), [], STAMP_3)
      store.replace('b', protoGoal({ objective: 'for b' }), [], STAMP_1)
      expect(store.get('b')?.objective).toBe('for b')
      dispose()
    })
  })

  // remove clears the stored timestamp with the agent state. A reopened tab must accept its next
  // initial history reply.

  it('forgets the applied stamp when the agent is removed', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.replace('a', protoGoal({ objective: 'gone' }), [], STAMP_3)
      store.remove('a')
      store.replace('a', protoGoal({ objective: 'reopened' }), [], STAMP_1)
      expect(store.get('a')?.objective).toBe('reopened')
      dispose()
    })
  })

  it('forgets everything for a closed agent', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      store.replace('a', protoGoal(), [AgentGoalAction.SET], STAMP_1)
      store.setProgress('a', { tokensUsed: 5 })
      store.remove('a')
      expect(store.get('a')).toBeUndefined()
      expect(store.progress('a')).toEqual({})
      expect(store.supportedActions('a')).toEqual([])
      dispose()
    })
  })
})

describe('unknown goal snapshots', () => {
  it('restores an unknown goal and its native detail on a cold store', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      const wire = create(AgentGoalSchema, { nativeId: 'unknown-native', objective: 'Keep the objective', status: AgentGoalStatus.UNKNOWN, statusDetail: 'unreadable-native-state', createdAt: STAMP_1 })
      store.replace('unknown-agent', wire, [AgentGoalAction.SET, AgentGoalAction.CLEAR], STAMP_2)
      expect(store.get('unknown-agent')).toMatchObject({ nativeId: 'unknown-native', objective: 'Keep the objective', status: 'unknown', statusDetail: 'unreadable-native-state', createdAt: STAMP_1 })
      expect(store.supportedActions('unknown-agent')).toEqual(['set', 'clear'])
      expect(store.progress('unknown-agent')).toEqual({})
      dispose()
    })
  })

  it('keeps progress and object identity while unknown changes back to a known status', () => {
    createRoot((dispose) => {
      onTestFinished(dispose)
      const store = createGoalStore()
      const wire = create(AgentGoalSchema, { nativeId: 'same-goal', objective: 'Keep the objective', status: AgentGoalStatus.ACTIVE, createdAt: STAMP_1 })
      store.replace('unknown-agent', wire, [AgentGoalAction.CLEAR], STAMP_1)
      store.setProgress('unknown-agent', { tokensUsed: 0, tokenBudget: 1000, timeUsedSeconds: 30, iterations: 2 })
      const original = unwrap(store.get('unknown-agent')!)
      store.replace('unknown-agent', create(AgentGoalSchema, { ...wire, status: AgentGoalStatus.UNKNOWN, statusDetail: 'future-state' }), [AgentGoalAction.CLEAR], STAMP_2)
      const unknownStatus = store.get('unknown-agent')?.status
      expect(unwrap(store.get('unknown-agent')!)).toBe(original)
      expect(store.progress('unknown-agent')).toEqual({ tokensUsed: 0, tokenBudget: 1000, timeUsedSeconds: 30, iterations: 2 })
      store.replace('unknown-agent', create(AgentGoalSchema, { ...wire, status: AgentGoalStatus.PAUSED }), [AgentGoalAction.RESUME], STAMP_3)
      expect(store.get('unknown-agent')?.status).toBe('paused')
      expect(store.progress('unknown-agent').iterations).toBe(2)
      expect(unknownStatus).toBe('unknown')
      dispose()
    })
  })
})
