import { describe, expect, it } from 'vitest'
import { AgentActivityState } from '~/generated/proto/leapmux/v1/agent_pb'
import { activityInterruptsWork, createAgentActivityStore } from '~/stores/agentActivity.store'

const { WORKING, IDLE, WAITING_FOR_USER } = AgentActivityState

describe('createAgentActivityStore', () => {
  it('reports an agent nothing has spoken about as idle', () => {
    const store = createAgentActivityStore()

    // A spinner shown before any evidence is worse than one shown a beat late.
    expect(store.isBusy('never-seen')).toBe(false)
  })

  it('holds what the worker last said', () => {
    const store = createAgentActivityStore()

    store.apply('a1', WORKING)
    expect(store.isBusy('a1')).toBe(true)

    store.apply('a1', IDLE)
    expect(store.isBusy('a1')).toBe(false)
  })

  it('reports the settle edge, and only that edge', () => {
    const store = createAgentActivityStore()

    // The turn-end alert reads this return, so only the settle may answer true.
    expect(store.apply('a1', WORKING)).toBe(false)
    expect(store.apply('a1', WORKING)).toBe(false)
    expect(store.apply('a1', IDLE)).toBe(true)
    // The worker broadcasts on transition, but the same value still reaches a
    // client twice -- a catch-up replay landing beside a live event -- and
    // ringing on arrival would ring twice for one settle.
    expect(store.apply('a1', IDLE)).toBe(false)
  })

  it('does not call an idle report for an agent it never saw working a settle', () => {
    const store = createAgentActivityStore()

    // A NOTIFY-mode tab can subscribe after the turn started and receive the
    // idle report alone. Nothing the user watched finished, so nothing rings.
    expect(store.apply('a1', IDLE)).toBe(false)
    // The write still lands, so the next real turn settles normally.
    expect(store.apply('a1', WORKING)).toBe(false)
    expect(store.apply('a1', IDLE)).toBe(true)
  })

  it('does not settle an agent whose state was forgotten mid-turn', () => {
    const store = createAgentActivityStore()
    store.apply('a1', WORKING)

    // The worker-offline sweep forgets the agent. When the worker returns, its
    // catch-up replay reports idle -- for a turn that died with the old link,
    // not one this client watched finish.
    store.forget('a1')

    expect(store.apply('a1', IDLE)).toBe(false)
  })

  it('keeps agents independent', () => {
    const store = createAgentActivityStore()

    store.apply('a1', WORKING)
    store.apply('a2', IDLE)

    expect(store.isBusy('a1')).toBe(true)
    expect(store.isBusy('a2')).toBe(false)
  })

  it('forgets one agent on tab close', () => {
    const store = createAgentActivityStore()
    store.apply('a1', WORKING)
    store.apply('a2', WORKING)

    store.forget('a1')

    expect(store.isBusy('a1')).toBe(false)
    expect(store.isBusy('a2')).toBe(true)
  })

  it('separates what the indicator asks from what the close guard asks', () => {
    const store = createAgentActivityStore()

    // One state, two questions. The spinner must stop while the user answers a
    // permission prompt -- they are looking straight at it -- but the turn is
    // still in flight, so a close would kill it along with every background task
    // under it. A single boolean could answer only one of the two.
    store.apply('a1', WAITING_FOR_USER)

    expect(store.isBusy('a1')).toBe(false)
    expect(activityInterruptsWork(WAITING_FOR_USER)).toBe(true)
  })

  it('agrees with itself for the states that are not ambiguous', () => {
    const store = createAgentActivityStore()

    store.apply('working', WORKING)
    expect(store.isBusy('working')).toBe(true)
    expect(activityInterruptsWork(WORKING)).toBe(true)

    store.apply('idle', IDLE)
    expect(store.isBusy('idle')).toBe(false)
    expect(activityInterruptsWork(IDLE)).toBe(false)

    // An agent nothing has reported on yet reads like an idle one.
    expect(store.isBusy('never-seen')).toBe(false)
    expect(activityInterruptsWork(AgentActivityState.UNSPECIFIED)).toBe(false)
  })

  it('treats a move into waiting as a settle', () => {
    const store = createAgentActivityStore()
    store.apply('a1', WORKING)

    // The turn stopped making progress and the user is the one who must act.
    // That is the alert, and it used to ride on busy -> false.
    expect(store.apply('a1', WAITING_FOR_USER)).toBe(true)
    // Answering the prompt resumes work, which is no settle.
    expect(store.apply('a1', WORKING)).toBe(false)
  })

  describe('seedPublished', () => {
    it('writes the state without reporting a settle, so a reconnect greets nobody', () => {
      const store = createAgentActivityStore()
      store.apply('a1', WORKING)

      store.seedPublished('a1', IDLE)

      expect(store.isBusy('a1'), 'the state still seeds').toBe(false)
    })

    it('clears an edge baseline the worker no longer stands behind', () => {
      // A link that dropped with no offline sweep leaves a WORKING baseline the
      // Worker retired while this client was away. The published level is what
      // the Worker last broadcast, so it is the answer that baseline must hold.
      const store = createAgentActivityStore()
      store.apply('a1', WORKING)

      store.seedPublished('a1', IDLE)

      expect(store.apply('a1', IDLE), 'a repeat of what the worker already sent is not a settle').toBe(false)
    })

    it('keeps a settle the worker still holds in its window', () => {
      // The published level reads WORKING for as long as the debounce runs, so
      // the transition that ends it still finds an edge to report.
      const store = createAgentActivityStore()
      store.apply('a1', WORKING)

      store.seedPublished('a1', WORKING)

      expect(store.apply('a1', IDLE), 'the held settle still rings when it lands').toBe(true)
    })

    it('establishes the baseline for a tab whose only writer is the seed', () => {
      // A NOTIFY-mode tab gets no catch-up replay, so a hydration seed is the
      // only write it takes before the first transition. That seed must leave a
      // baseline the settle can measure against, or the turn this tab watched
      // end rings for nobody.
      const store = createAgentActivityStore()

      store.seedPublished('a1', WORKING)

      expect(store.apply('a1', IDLE), 'the agent finished while this client watched').toBe(true)
    })

    it('keeps what it holds when the worker sends no opinion', () => {
      const store = createAgentActivityStore()
      store.apply('a1', WORKING)

      store.seedPublished('a1', AgentActivityState.UNSPECIFIED)

      expect(store.isBusy('a1'), 'UNSPECIFIED must not overwrite a real answer').toBe(true)
    })
  })

  describe('forget', () => {
    it('forgets the edge baseline with the state', () => {
      const store = createAgentActivityStore()
      store.apply('a1', WORKING)

      store.forget('a1')

      expect(store.apply('a1', IDLE), 'a retired agent leaves no edge behind').toBe(false)
    })
  })
})
