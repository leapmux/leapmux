import type { AgentGoal as ProtoAgentGoal } from '~/generated/proto/leapmux/v1/agent_pb'
import { createRoot } from 'solid-js'
import { unwrap } from 'solid-js/store'
import { describe, expect, it } from 'vitest'
import { AgentGoalAction, AgentGoalStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { createGoalStore } from './chatGoalStore'

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

describe('createGoalStore', () => {
  it('holds a goal per agent and reports undefined for one with none', () => {
    createRoot((dispose) => {
      const store = createGoalStore()
      store.replace('a', protoGoal({ objective: 'first' }), [])
      expect(store.get('a')?.objective).toBe('first')
      expect(store.get('b')).toBeUndefined()
      dispose()
    })
  })

  /**
   * The flicker guard.
   *
   * `set` replaces the leaf whole, so every field read becomes a new value and
   * the whole card rebuilds -- which loses the tooltip under the pointer and
   * restarts the status dot's animation. The goal hits this on every turn,
   * because a status detail moves while the objective sits still.
   */
  it('keeps the stored object identical when a field changes', () => {
    createRoot((dispose) => {
      const store = createGoalStore()
      store.replace('a', protoGoal({ objective: 'same', statusDetail: 'active' }), [])
      const before = unwrap(store.get('a')!)
      store.replace('a', protoGoal({ objective: 'same', statusDetail: 'verifying' }), [])
      const after = unwrap(store.get('a')!)
      expect(after).toBe(before)
      expect(store.get('a')?.statusDetail).toBe('verifying')
      dispose()
    })
  })

  it('drops the goal when the agent has none', () => {
    createRoot((dispose) => {
      const store = createGoalStore()
      store.replace('a', protoGoal(), [])
      store.replace('a', undefined, [AgentGoalAction.SET])
      expect(store.get('a')).toBeUndefined()
      // The capability outlives the goal, which is what lets the empty state
      // offer "Set a goal".
      expect(store.supportedActions('a')).toEqual(['set'])
      dispose()
    })
  })

  /**
   * A MERGE, not a replace: a provider broadcasts only the counters it has, and
   * the session-info channel dedups per key -- so a payload carrying just
   * `tokensUsed` must not blank an iteration count that arrived earlier.
   */
  it('merges progress instead of replacing it', () => {
    createRoot((dispose) => {
      const store = createGoalStore()
      store.setProgress('a', { tokensUsed: 100, iterations: 3 })
      store.setProgress('a', { tokensUsed: 250 })
      expect(store.progress('a')).toEqual({ tokensUsed: 250, iterations: 3 })
      dispose()
    })
  })

  /**
   * The counters belong to ONE goal. A fresh goal that kept the previous one's
   * token count would show a number from work it never did -- and for Codex the
   * correction is a completed tool call away, which is long enough to read.
   */
  it('drops the counters when the goal is cleared', () => {
    createRoot((dispose) => {
      const store = createGoalStore()
      store.replace('a', protoGoal({ createdAt: 't1' }), [])
      store.setProgress('a', { tokensUsed: 900 })
      store.replace('a', undefined, [])
      expect(store.progress('a')).toEqual({})
      dispose()
    })
  })

  // Codex puts no goal id on the wire, so a goal restarted with the SAME
  // objective differs only by its created stamp.
  it('drops the counters when the goal is replaced', () => {
    createRoot((dispose) => {
      const store = createGoalStore()
      store.replace('a', protoGoal({ objective: 'same', createdAt: 't1' }), [])
      store.setProgress('a', { tokensUsed: 900 })
      store.replace('a', protoGoal({ objective: 'same', createdAt: 't2' }), [])
      expect(store.progress('a')).toEqual({})
      dispose()
    })
  })

  it('keeps the counters across an update to the same goal', () => {
    createRoot((dispose) => {
      const store = createGoalStore()
      store.replace('a', protoGoal({ createdAt: 't1', statusDetail: 'active' }), [])
      store.setProgress('a', { tokensUsed: 900 })
      store.replace('a', protoGoal({ createdAt: 't1', statusDetail: 'verifying' }), [])
      expect(store.progress('a')).toEqual({ tokensUsed: 900 })
      dispose()
    })
  })

  it('forgets everything for a closed agent', () => {
    createRoot((dispose) => {
      const store = createGoalStore()
      store.replace('a', protoGoal(), [AgentGoalAction.SET])
      store.setProgress('a', { tokensUsed: 5 })
      store.remove('a')
      expect(store.get('a')).toBeUndefined()
      expect(store.progress('a')).toEqual({})
      expect(store.supportedActions('a')).toEqual([])
      dispose()
    })
  })
})
