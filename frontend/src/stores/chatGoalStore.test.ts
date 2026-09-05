import type { AgentGoal as ProtoAgentGoal } from '~/generated/proto/leapmux/v1/agent_pb'
import { createRoot } from 'solid-js'
import { unwrap } from 'solid-js/store'
import { describe, expect, it } from 'vitest'
import { AgentGoalAction, AgentGoalStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { createGoalStore } from './chatGoalStore'

// The worker's ordering stamps, in the layout it sends: fixed-width UTC, so a
// plain string compare orders two of them. Named rather than inlined, because
// which one is older is the whole point of every test that uses them.
const STAMP_1 = '2026-09-06T10:00:00.000Z'
const STAMP_2 = '2026-09-06T10:00:01.000Z'
const STAMP_3 = '2026-09-06T10:00:02.000Z'

function protoGoal(over: Partial<ProtoAgentGoal> = {}): ProtoAgentGoal {
  return {
    objective: 'ship it',
    status: AgentGoalStatus.ACTIVE,
    statusDetail: '',
    createdAt: '',
    ...over,
  } as ProtoAgentGoal
}

describe('createGoalStore', () => {
  it('holds a goal per agent and reports undefined for one with none', () => {
    createRoot((dispose) => {
      const store = createGoalStore()
      store.replace('a', protoGoal({ objective: 'first' }), [], STAMP_1)
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
      const store = createGoalStore()
      store.replace('a', protoGoal(), [], STAMP_1)
      store.replace('a', undefined, [AgentGoalAction.SET], STAMP_2)
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
      store.replace('a', protoGoal({ createdAt: 't1' }), [], STAMP_1)
      store.setProgress('a', { tokensUsed: 900 })
      store.replace('a', undefined, [], STAMP_2)
      expect(store.progress('a')).toEqual({})
      dispose()
    })
  })

  // Codex puts no goal id on the wire, so a goal restarted with the SAME
  // objective differs only by its created stamp.
  it('drops the counters when the goal is replaced', () => {
    createRoot((dispose) => {
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
      const store = createGoalStore()
      store.replace('a', protoGoal({ createdAt: 't1', statusDetail: 'active' }), [], STAMP_1)
      store.setProgress('a', { tokensUsed: 900 })
      store.replace('a', protoGoal({ createdAt: 't1', statusDetail: 'verifying' }), [], STAMP_2)
      expect(store.progress('a')).toEqual({ tokensUsed: 900 })
      dispose()
    })
  })

  /**
   * The stale cold-load race, which is the reason the stamp exists.
   *
   * Three paths answer "what is this agent's goal": the live AgentGoalChanged
   * broadcast, the WatchEvents replay on (re)subscribe, and the
   * ListAgentMessages cold load. The cold load reads the row on the worker and
   * lands in the browser a round trip later, so a clear that happened in
   * between is undone by the older answer arriving second.
   */
  it('drops an answer older than the one already applied', () => {
    createRoot((dispose) => {
      const store = createGoalStore()
      store.replace('a', protoGoal({ objective: 'current' }), [AgentGoalAction.CLEAR], STAMP_2)
      // The cold load read the row before the goal above was written.
      store.replace('a', protoGoal({ objective: 'stale' }), [], STAMP_1)
      expect(store.get('a')?.objective).toBe('current')
      expect(store.supportedActions('a')).toEqual(['clear'])
      dispose()
    })
  })

  it('does not let a stale answer resurrect a cleared goal', () => {
    createRoot((dispose) => {
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
      const store = createGoalStore()
      store.replace('a', protoGoal({ objective: 'first' }), [], STAMP_1)
      store.replace('a', protoGoal({ objective: 'second' }), [], STAMP_2)
      expect(store.get('a')?.objective).toBe('second')
      dispose()
    })
  })

  /**
   * An EQUAL stamp must still land. The worker re-broadcasts an unchanged goal
   * on purpose when an agent process registers, to deliver the capabilities
   * beside it -- dropping that broadcast would leave every control disabled.
   */
  it('applies an answer whose stamp matches, to pick up new capabilities', () => {
    createRoot((dispose) => {
      const store = createGoalStore()
      store.replace('a', protoGoal({ objective: 'ship it' }), [], STAMP_1)
      expect(store.supportedActions('a')).toEqual([])
      store.replace('a', protoGoal({ objective: 'ship it' }), [AgentGoalAction.PAUSE], STAMP_1)
      expect(store.supportedActions('a')).toEqual(['pause'])
      dispose()
    })
  })

  // An agent that never had a goal carries an empty stamp, and the first real
  // answer must not be read as older than it.
  it('accepts the first stamped answer after an empty one', () => {
    createRoot((dispose) => {
      const store = createGoalStore()
      store.replace('a', undefined, [AgentGoalAction.SET], '')
      store.replace('a', protoGoal({ objective: 'the first goal' }), [], STAMP_1)
      expect(store.get('a')?.objective).toBe('the first goal')
      dispose()
    })
  })

  // The stamps are per agent. One agent's newer answer must never block
  // another's older-but-current one.
  it('orders each agent on its own stamp', () => {
    createRoot((dispose) => {
      const store = createGoalStore()
      store.replace('a', protoGoal({ objective: 'for a' }), [], STAMP_3)
      store.replace('b', protoGoal({ objective: 'for b' }), [], STAMP_1)
      expect(store.get('b')?.objective).toBe('for b')
      dispose()
    })
  })

  // A reopened tab starts over: `remove` drops the agent's stamp with the rest
  // of its state, so the next cold load is not compared against a stamp from a
  // session that is gone.
  it('forgets the applied stamp when the agent is removed', () => {
    createRoot((dispose) => {
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
