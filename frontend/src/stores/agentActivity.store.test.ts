import { describe, expect, it } from 'vitest'
import { AgentActivityState } from '~/generated/proto/leapmux/v1/agent_pb'
import { activityInterruptsWork, createAgentActivityStore } from '~/stores/agentActivity.store'

const { WORKING, IDLE, WAITING_FOR_USER } = AgentActivityState

describe('createAgentActivityStore', () => {
  const invalidCases = [AgentActivityState.UNSPECIFIED, -1, -2147483648, 99, 2147483647]
    .flatMap(value => [
      { value, baseline: 'cold', expected: IDLE },
      { value, baseline: 'working', expected: WORKING },
    ])

  it.each(invalidCases)('refuses invalid apply $value with a $baseline baseline', ({ value, baseline, expected }) => {
    const store = createAgentActivityStore()
    if (baseline === 'working')
      store.seedPublished('a1', WORKING)

    expect(store.apply('a1', value)).toBe(false)
    expect(store.publishedState('a1')).toBe(expected)
    expect(store.isBusy('a1')).toBe(expected === WORKING)
    expect(store.apply('a1', WORKING)).toBe(false)
    expect(store.apply('a1', IDLE)).toBe(true)
    expect(store.apply('a1', IDLE)).toBe(false)
  })

  it.each(invalidCases)('refuses invalid seed $value with a $baseline baseline', ({ value, baseline, expected }) => {
    const store = createAgentActivityStore()
    if (baseline === 'working')
      store.seedPublished('a1', WORKING)

    store.seedPublished('a1', value)
    expect(store.publishedState('a1')).toBe(expected)
    expect(store.isBusy('a1')).toBe(expected === WORKING)
    expect(store.apply('a1', WORKING)).toBe(false)
    expect(store.apply('a1', IDLE)).toBe(true)
    expect(store.apply('a1', IDLE)).toBe(false)
  })

  it.each([IDLE, WORKING, WAITING_FOR_USER])('accepts and repeats published level %s through both write paths', (level) => {
    const store = createAgentActivityStore()
    expect(store.apply('a1', level)).toBe(false)
    expect(store.publishedState('a1')).toBe(level)
    expect(store.isBusy('a1')).toBe(level === WORKING)
    expect(store.apply('a1', level)).toBe(false)

    const seeded = createAgentActivityStore()
    seeded.seedPublished('a1', level)
    seeded.seedPublished('a1', level)
    expect(seeded.publishedState('a1')).toBe(level)
    expect(seeded.isBusy('a1')).toBe(level === WORKING)
    expect(seeded.apply('a1', level)).toBe(false)
  })

  it('retains one settle after repeated working publications and an invalid write', () => {
    const store = createAgentActivityStore()
    const invalidState: number = 99
    store.seedPublished('a1', WORKING)
    store.seedPublished('a1', WORKING)
    expect(store.apply('a1', WORKING)).toBe(false)
    expect(store.apply('a1', invalidState)).toBe(false)
    expect(store.publishedState('a1')).toBe(WORKING)
    expect(store.apply('a1', WAITING_FOR_USER)).toBe(true)
    expect(store.apply('a1', WAITING_FOR_USER)).toBe(false)
    expect(store.apply('a1', IDLE)).toBe(false)
  })
  it('reports an agent nothing has spoken about as idle', () => {
    const store = createAgentActivityStore()

    // An unreported agent uses the idle default without a spinner.
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

    // The alert uses this result.
    // Report only the transition out of WORKING.
    expect(store.apply('a1', WORKING)).toBe(false)
    expect(store.apply('a1', WORKING)).toBe(false)
    expect(store.apply('a1', IDLE)).toBe(true)
    // A repeated published IDLE level must not report another settle.
    expect(store.apply('a1', IDLE)).toBe(false)
  })

  it('does not call an idle report for an agent it never saw working a settle', () => {
    const store = createAgentActivityStore()

    // A NOTIFY tab can receive IDLE without receiving the earlier WORKING publication.
    // That report creates no settle.
    expect(store.apply('a1', IDLE)).toBe(false)
    // The next real turn must still establish its own settle baseline.
    expect(store.apply('a1', WORKING)).toBe(false)
    expect(store.apply('a1', IDLE)).toBe(true)
  })

  it('does not settle an agent whose state was forgotten mid-turn', () => {
    const store = createAgentActivityStore()
    store.apply('a1', WORKING)

    // A lost connection can omit the final settle.
    // Forget that old baseline before the next publication.
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

    // WAITING_FOR_USER stops the spinner while the user answers the prompt.
    // Closing the agent still interrupts its turn and background tasks.
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

    // An unreported agent uses IDLE as its display default.
    expect(store.isBusy('never-seen')).toBe(false)
    expect(activityInterruptsWork(AgentActivityState.UNSPECIFIED)).toBe(false)
  })

  it('treats a move into waiting as a settle', () => {
    const store = createAgentActivityStore()
    store.apply('a1', WORKING)

    // The user must act when the turn reaches WAITING_FOR_USER.
    // That transition reports one settle.
    expect(store.apply('a1', WAITING_FOR_USER)).toBe(true)
    // A return to WORKING resumes the turn without a settle.
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
      // A stale WORKING level can survive a lost connection.
      // The worker's published IDLE level replaces that old baseline.
      const store = createAgentActivityStore()
      store.apply('a1', WORKING)

      store.seedPublished('a1', IDLE)

      expect(store.apply('a1', IDLE), 'a repeat of what the worker already sent is not a settle').toBe(false)
    })

    it('keeps a settle the worker still holds in its window', () => {
      // A repeated WORKING seed preserves the later settle that the worker delays.
      const store = createAgentActivityStore()
      store.apply('a1', WORKING)

      store.seedPublished('a1', WORKING)

      expect(store.apply('a1', IDLE), 'the held settle still rings when it lands').toBe(true)
    })

    it('establishes the baseline for a tab whose only writer is the seed', () => {
      // A NOTIFY tab receives no replay baseline.
      // Its hydration seed must establish the level for the next live transition.
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
