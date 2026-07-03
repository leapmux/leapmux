import type { FlingSkeletonRegistry } from './chatSkeletonCrossfade'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createDelayedSet, createFlingSkeletonRegistry, createLingerSet, createRowUpgradePhase } from './chatSkeletonCrossfade'

describe('chatskeletoncrossfade', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  describe('createlingerset', () => {
    it('lingers an id that leaves the active set, then drops it after lingerMs', () => {
      const [active, setActive] = createSignal<string[]>(['a', 'b'])
      let lingering!: () => ReadonlySet<string>
      let dispose!: () => void
      createRoot((d) => {
        dispose = d
        lingering = createLingerSet(active, 1000).lingeringIds
      })
      // Nothing lingers while both are active.
      expect([...lingering()]).toEqual([])
      setActive(['a']) // 'b' left the active set
      expect([...lingering()]).toEqual(['b'])
      vi.advanceTimersByTime(999)
      expect([...lingering()]).toEqual(['b']) // still within the fade beat
      vi.advanceTimersByTime(1)
      expect([...lingering()]).toEqual([]) // dropped after the beat
      dispose()
    })

    it('cancels the linger the moment the id re-enters the active set', () => {
      const [active, setActive] = createSignal<string[]>(['a'])
      let lingering!: () => ReadonlySet<string>
      let dispose!: () => void
      createRoot((d) => {
        dispose = d
        lingering = createLingerSet(active, 1000).lingeringIds
      })
      setActive([]) // 'a' left -> lingering
      expect([...lingering()]).toEqual(['a'])
      setActive(['a']) // re-entered -> linger cancelled immediately
      expect([...lingering()]).toEqual([])
      // The cancelled timer must not re-drop (or re-add) it later.
      vi.advanceTimersByTime(1000)
      expect([...lingering()]).toEqual([])
      dispose()
    })

    it('clears pending timers on cleanup so a stale timer cannot fire', () => {
      const [active, setActive] = createSignal<string[]>(['a'])
      let lingering!: () => ReadonlySet<string>
      let dispose!: () => void
      createRoot((d) => {
        dispose = d
        lingering = createLingerSet(active, 1000).lingeringIds
      })
      setActive([]) // linger 'a'
      expect([...lingering()]).toEqual(['a'])
      dispose()
      expect(() => vi.advanceTimersByTime(1000)).not.toThrow()
    })
  })

  describe('createdelayedset', () => {
    it('adds an id only after it has stayed active for delayMs', () => {
      const [active, setActive] = createSignal<string[]>([])
      let delayed!: () => ReadonlySet<string>
      let dispose!: () => void
      createRoot((d) => {
        dispose = d
        delayed = createDelayedSet(active, 500).delayedIds
      })
      setActive(['a']) // enters the active set -> delay timer armed, not shown yet
      expect([...delayed()]).toEqual([])
      vi.advanceTimersByTime(499)
      expect([...delayed()]).toEqual([]) // still within the show-delay
      vi.advanceTimersByTime(1)
      expect([...delayed()]).toEqual(['a']) // appears once the delay elapses
      dispose()
    })

    it('never shows an id that leaves before the delay elapses (fast reveal)', () => {
      const [active, setActive] = createSignal<string[]>(['a'])
      let delayed!: () => ReadonlySet<string>
      let dispose!: () => void
      createRoot((d) => {
        dispose = d
        delayed = createDelayedSet(active, 500).delayedIds
      })
      vi.advanceTimersByTime(200) // measured quickly...
      setActive([]) // ...and revealed before the show-delay
      vi.advanceTimersByTime(500)
      expect([...delayed()]).toEqual([]) // no skeleton ever appeared
      dispose()
    })

    it('drops an already-shown id the moment it leaves the active set', () => {
      const [active, setActive] = createSignal<string[]>(['a'])
      let delayed!: () => ReadonlySet<string>
      let dispose!: () => void
      createRoot((d) => {
        dispose = d
        delayed = createDelayedSet(active, 500).delayedIds
      })
      vi.advanceTimersByTime(500)
      expect([...delayed()]).toEqual(['a']) // slow wait -> skeleton shown
      setActive([]) // measured -> leaves
      expect([...delayed()]).toEqual([]) // drops immediately (createLingerSet handles the fade)
      dispose()
    })

    it('re-arms the delay when an id leaves and re-enters', () => {
      const [active, setActive] = createSignal<string[]>(['a'])
      let delayed!: () => ReadonlySet<string>
      let dispose!: () => void
      createRoot((d) => {
        dispose = d
        delayed = createDelayedSet(active, 500).delayedIds
      })
      vi.advanceTimersByTime(300)
      setActive([]) // left before appearing -> pending timer cancelled
      setActive(['a']) // re-entered -> a FRESH delay, not the leftover 200ms
      vi.advanceTimersByTime(300)
      expect([...delayed()]).toEqual([]) // the earlier 300ms must not carry over
      vi.advanceTimersByTime(200)
      expect([...delayed()]).toEqual(['a'])
      dispose()
    })

    it('clears pending timers on cleanup so a stale timer cannot fire', () => {
      const [active] = createSignal<string[]>(['a'])
      let dispose!: () => void
      createRoot((d) => {
        dispose = d
        createDelayedSet(active, 500)
      })
      dispose()
      expect(() => vi.advanceTimersByTime(500)).not.toThrow()
    })
  })

  describe('createrowupgradephase', () => {
    it('starts real when the row is not entering mid-fling', () => {
      const [fast] = createSignal(false)
      let phase!: () => string
      let dispose!: () => void
      createRoot((d) => {
        dispose = d
        phase = createRowUpgradePhase({ fastScrollActive: fast, hasMeasuredHeight: () => true }, 'a', 1000)
      })
      expect(phase()).toBe('real')
      dispose()
    })

    it('mounts a skeleton mid-fling, then crossfades to real once the scroll settles', () => {
      const [fast, setFast] = createSignal(true)
      let phase!: () => string
      let dispose!: () => void
      createRoot((d) => {
        dispose = d
        phase = createRowUpgradePhase({ fastScrollActive: fast, hasMeasuredHeight: () => true }, 'a', 1000)
      })
      expect(phase()).toBe('skeleton')
      setFast(false) // scroll settled
      expect(phase()).toBe('crossfade')
      vi.advanceTimersByTime(1000)
      expect(phase()).toBe('real')
      dispose()
    })

    it('reaches real even when a NEW fling starts during the crossfade beat', () => {
      // A second fling arriving mid-crossfade re-runs the phase effect; the timer that
      // finishes the crossfade must NOT be cancelled by that re-run (else the row is
      // stranded at 'crossfade' with a dead skeleton overlay that never fades out).
      const [fast, setFast] = createSignal(true)
      let phase!: () => string
      let dispose!: () => void
      createRoot((d) => {
        dispose = d
        phase = createRowUpgradePhase({ fastScrollActive: fast, hasMeasuredHeight: () => true }, 'a', 1000)
      })
      expect(phase()).toBe('skeleton')
      setFast(false) // scroll settled -> crossfade begins
      expect(phase()).toBe('crossfade')
      setFast(true) // a NEW fling starts DURING the crossfade beat
      vi.advanceTimersByTime(1000)
      expect(phase()).toBe('real') // must still complete, not stick at 'crossfade'
      dispose()
    })

    it('mounts real for an UNMEASURED row even mid-fling', () => {
      const [fast] = createSignal(true)
      let phase!: () => string
      let dispose!: () => void
      createRoot((d) => {
        dispose = d
        phase = createRowUpgradePhase({ fastScrollActive: fast, hasMeasuredHeight: () => false }, 'a', 1000)
      })
      expect(phase()).toBe('real')
      dispose()
    })

    it('never downgrades once real: a fling starting AFTER mount does not re-skeleton', () => {
      const [fast, setFast] = createSignal(false)
      let phase!: () => string
      let dispose!: () => void
      createRoot((d) => {
        dispose = d
        phase = createRowUpgradePhase({ fastScrollActive: fast, hasMeasuredHeight: () => true }, 'a', 1000)
      })
      expect(phase()).toBe('real')
      setFast(true) // a later fling must not tear a real row back to a skeleton
      expect(phase()).toBe('real')
      dispose()
    })
  })

  describe('createflingskeletonregistry', () => {
    const measuredVirt = (fast: () => boolean, measured: (id: string) => boolean = () => true) =>
      ({ fastScrollActive: fast, hasMeasuredHeight: measured })

    it('adds a measured row that mounts mid-fling, and drops it once the scroll settles', () => {
      const [fast, setFast] = createSignal(true)
      let registry!: FlingSkeletonRegistry
      let dispose!: () => void
      createRoot((d) => {
        dispose = d
        registry = createFlingSkeletonRegistry(measuredVirt(fast), 1000)
        registry.trackRow('a')
      })
      expect([...registry.skeletonIds()]).toEqual(['a'])
      setFast(false) // scroll settles -> phase leaves 'skeleton' -> row drops from the set
      expect([...registry.skeletonIds()]).toEqual([])
      dispose()
    })

    it('does not add an unmeasured row (it mounts real, never a skeleton)', () => {
      const [fast] = createSignal(true)
      let registry!: FlingSkeletonRegistry
      let dispose!: () => void
      createRoot((d) => {
        dispose = d
        registry = createFlingSkeletonRegistry(measuredVirt(fast, () => false), 1000)
        registry.trackRow('a')
      })
      expect([...registry.skeletonIds()]).toEqual([])
      dispose()
    })

    it('does not add a row that mounts while not fast-scrolling, even if a fling starts later', () => {
      const [fast, setFast] = createSignal(false)
      let registry!: FlingSkeletonRegistry
      let dispose!: () => void
      createRoot((d) => {
        dispose = d
        registry = createFlingSkeletonRegistry(measuredVirt(fast), 1000)
        registry.trackRow('a')
      })
      expect([...registry.skeletonIds()]).toEqual([])
      setFast(true) // a fling AFTER mount must not tear a settled row into a skeleton
      expect([...registry.skeletonIds()]).toEqual([])
      dispose()
    })

    it('drops a skeleton row from the set when its row unmounts', () => {
      const [fast] = createSignal(true)
      let registry!: FlingSkeletonRegistry
      let disposeRow!: () => void
      let dispose!: () => void
      createRoot((d) => {
        dispose = d
        registry = createFlingSkeletonRegistry(measuredVirt(fast), 1000)
        // A nested root models one row's lifetime, disposable independently.
        createRoot((dRow) => {
          disposeRow = dRow
          registry.trackRow('a')
        })
      })
      expect([...registry.skeletonIds()]).toEqual(['a'])
      disposeRow() // the row scrolls out / unmounts
      expect([...registry.skeletonIds()]).toEqual([])
      dispose()
    })

    it('tracks several rows independently, keeping only the measured skeletons', () => {
      const [fast, setFast] = createSignal(true)
      const measured = new Set(['a', 'b'])
      let registry!: FlingSkeletonRegistry
      let dispose!: () => void
      createRoot((d) => {
        dispose = d
        registry = createFlingSkeletonRegistry(measuredVirt(fast, id => measured.has(id)), 1000)
        registry.trackRow('a')
        registry.trackRow('b')
        registry.trackRow('c') // unmeasured -> mounts real, never enters the set
      })
      expect([...registry.skeletonIds()].sort()).toEqual(['a', 'b'])
      setFast(false)
      expect([...registry.skeletonIds()]).toEqual([])
      dispose()
    })
  })
})
