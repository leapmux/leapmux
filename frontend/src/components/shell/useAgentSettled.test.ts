import type { UseAgentSettledOpts } from './useAgentSettled'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createActiveClientStore } from '~/lib/presence/activeClient'
import { useAgentSettled } from './useAgentSettled'

/**
 * jsdom leaves HTMLMediaElement.play() unimplemented: it returns undefined
 * instead of a promise, so the `.catch` in the handler throws a TypeError. The
 * spy also IS the assertion -- the handler plays a module-level Audio element
 * that no caller can reach.
 */
let play: ReturnType<typeof vi.spyOn>
/** Every `leapmux:turn-end-played` the handler dispatched in one test. */
let rang: CustomEvent[]

function record(e: Event) {
  rang.push(e as CustomEvent)
}

beforeEach(() => {
  // Fake performance so monotonicNow (the cooldown clock) advances with
  // vi.advanceTimersByTime.
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'performance'] })
  play = vi.spyOn(HTMLMediaElement.prototype, 'play').mockResolvedValue(undefined)
  rang = []
  window.addEventListener('leapmux:turn-end-played', record)
})

afterEach(() => {
  window.removeEventListener('leapmux:turn-end-played', record)
  play.mockRestore()
  vi.useRealTimers()
})

const ACTIVE_WORKSPACE = 'w-1'

function makeHandler(overrides: Partial<UseAgentSettledOpts> = {}) {
  return useAgentSettled({
    preferences: {
      turnEndSound: () => 'ding-dong',
      turnEndSoundVolume: () => 50,
    },
    activeClient: createActiveClientStore(),
    effectiveClientId: () => 'client-a',
    getActiveWorkspaceId: () => ACTIVE_WORKSPACE,
    ownClientId: () => 'client-a',
    isAgentClosing: () => false,
    isSubagent: () => false,
    ...overrides,
  })
}

describe('useAgentSettled', () => {
  it('rings and dispatches the test hook when a root agent settles', () => {
    makeHandler()('a-1')

    expect(play).toHaveBeenCalledTimes(1)
    expect(rang).toHaveLength(1)
    expect(rang[0].detail).toMatchObject({
      agentId: 'a-1',
      ownClientId: 'client-a',
      effectiveClientId: 'client-a',
    })
  })

  it('takes the volume from the preference, on a 0-100 scale', () => {
    makeHandler({
      preferences: { turnEndSound: () => 'ding-dong', turnEndSoundVolume: () => 40 },
    })('a-1')

    expect((play.mock.instances[0] as HTMLAudioElement).volume).toBeCloseTo(0.4)
  })

  it('stays quiet when the sound preference is off', () => {
    makeHandler({
      preferences: { turnEndSound: () => 'none', turnEndSoundVolume: () => 50 },
    })('a-1')

    expect(play).not.toHaveBeenCalled()
    expect(rang).toHaveLength(0)
  })

  describe('subagent tabs', () => {
    it('stays quiet when the settled agent is a subagent transcript', () => {
      makeHandler({ isSubagent: (id: string) => id === 'child-1' })('child-1')

      expect(play).not.toHaveBeenCalled()
      expect(rang).toHaveLength(0)
    })

    it('leaves the cooldown unspent, so the ROOT still rings right after', () => {
      // The settle order a spawn produces: the subagent finishes, and the
      // parent settles a moment later because that child was its last work. A
      // child that spent the 60s cooldown would silence the settle the user is
      // actually waiting for.
      const settled = makeHandler({ isSubagent: (id: string) => id.startsWith('child-') })
      settled('child-1')
      vi.advanceTimersByTime(50)
      settled('root-1')

      expect(play).toHaveBeenCalledTimes(1)
      expect(rang.map(e => e.detail.agentId)).toEqual(['root-1'])
    })

    it('rings for a tab whose lineage has not arrived, rather than swallowing it', () => {
      // A child restored from the CRDT after a reload carries no parent link
      // until listAgents replies. A swallowed completion is worse than a
      // spurious one, so the unknown case rings.
      makeHandler({ isSubagent: () => undefined })('a-1')

      expect(rang.map(e => e.detail.agentId)).toEqual(['a-1'])
    })

    it('leaves the cooldown unspent for a tab whose lineage has not arrived', () => {
      // The same settle can turn out to be a child's once hydration lands, and
      // a child must never take the minute the root's own settle needs.
      const settled = makeHandler({
        isSubagent: (id: string) => (id === 'unknown-1' ? undefined : false),
      })
      settled('unknown-1')
      vi.advanceTimersByTime(50)
      settled('root-1')

      expect(rang.map(e => e.detail.agentId)).toEqual(['unknown-1', 'root-1'])
    })

    it('still rings for the root when a sibling child is open', () => {
      const settled = makeHandler({ isSubagent: (id: string) => id === 'child-1' })
      settled('root-1')

      expect(rang.map(e => e.detail.agentId)).toEqual(['root-1'])
    })
  })

  it('absorbs a blocked autoplay and still dispatches the test hook', async () => {
    // The handler exists to ring for a tab the user is NOT looking at, and a
    // browser rejects play() with NotAllowedError until that tab has seen a
    // user gesture. Without the `.catch` every ding logs an unhandled
    // rejection.
    const unhandled: unknown[] = []
    const onUnhandled = (reason: unknown) => unhandled.push(reason)
    process.on('unhandledRejection', onUnhandled)
    try {
      play.mockRejectedValueOnce(new DOMException('autoplay blocked', 'NotAllowedError'))
      makeHandler()('a-1')

      expect(rang.map(e => e.detail.agentId)).toEqual(['a-1'])
      // setImmediate stays real: the fake-timer set above covers setTimeout and
      // performance only, so this lets the rejection reach a listener if one
      // ever gets it.
      await new Promise(resolve => setImmediate(resolve))
      expect(unhandled).toEqual([])
    }
    finally {
      process.off('unhandledRejection', onUnhandled)
    }
  })

  it('stays quiet for an agent whose tab is closing', () => {
    makeHandler({ isAgentClosing: id => id === 'a-1' })('a-1')

    expect(play).not.toHaveBeenCalled()
  })

  describe('tool-use count', () => {
    it('suppresses a turn that used no tool', () => {
      makeHandler()('a-1', 0)

      expect(play).not.toHaveBeenCalled()
    })

    it('rings for a turn that used tools', () => {
      makeHandler()('a-1', 3)

      expect(play).toHaveBeenCalledTimes(1)
    })

    it('rings when the settle carries no count at all', () => {
      // A permission prompt and a process exit settle the agent with no turn
      // behind them, and a provider that cannot count reports none either.
      // UNDEFINED must not read as zero.
      makeHandler()('a-1', undefined)

      expect(play).toHaveBeenCalledTimes(1)
    })
  })

  describe('active-client gate', () => {
    it('suppresses the ding when another client is active', () => {
      const activeClient = createActiveClientStore()
      activeClient.update(ACTIVE_WORKSPACE, 'client-b')
      makeHandler({ activeClient })('a-1')

      expect(play).not.toHaveBeenCalled()
    })

    it('rings when this client is the active one', () => {
      const activeClient = createActiveClientStore()
      activeClient.update(ACTIVE_WORKSPACE, 'client-a')
      makeHandler({ activeClient })('a-1')

      expect(play).toHaveBeenCalledTimes(1)
    })

    it('rings when no client leads, rather than swallowing the settle', () => {
      const activeClient = createActiveClientStore()
      activeClient.update(ACTIVE_WORKSPACE, 'client-b')
      makeHandler({ activeClient, effectiveClientId: () => '' })('a-1')

      expect(play).toHaveBeenCalledTimes(1)
    })

    it('rings before a workspace is active, rather than swallowing the settle', () => {
      // activeWorkspaceId() is null until the bootstrap lands and across a
      // workspace switch. The gate needs a workspace to ask about a leader, so
      // it does not run at all there -- the same degraded-plays rule the two
      // cases above state.
      const activeClient = createActiveClientStore()
      activeClient.update(ACTIVE_WORKSPACE, 'client-b')
      makeHandler({ activeClient, getActiveWorkspaceId: () => null })('a-1')

      expect(play).toHaveBeenCalledTimes(1)
      expect(rang.map(e => e.detail.agentId)).toEqual(['a-1'])
    })

    it('rings when the active workspace is undefined too', () => {
      const activeClient = createActiveClientStore()
      activeClient.update(ACTIVE_WORKSPACE, 'client-b')
      makeHandler({ activeClient, getActiveWorkspaceId: () => undefined })('a-1')

      expect(play).toHaveBeenCalledTimes(1)
    })
  })

  describe('cooldown', () => {
    it('rings one time per minute', () => {
      const settled = makeHandler()
      settled('a-1')
      vi.advanceTimersByTime(59_000)
      settled('a-2')

      expect(play).toHaveBeenCalledTimes(1)
    })

    it('rings again once the minute passes', () => {
      const settled = makeHandler()
      settled('a-1')
      vi.advanceTimersByTime(60_001)
      settled('a-2')

      expect(play).toHaveBeenCalledTimes(2)
      expect(rang.map(e => e.detail.agentId)).toEqual(['a-1', 'a-2'])
    })

    it('rings the first settle after a cold load', () => {
      // performance.now() starts near zero under fake timers, which is exactly
      // the state a page load leaves. A numeric zero for "never played" would
      // swallow this one.
      makeHandler()('a-1')

      expect(play).toHaveBeenCalledTimes(1)
    })
  })
})
