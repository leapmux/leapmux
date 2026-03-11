import type { CompassState } from './compassPhysics'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createCompassSimulation } from './compassPhysics'

describe('compassPhysics', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    let perfNow = 0
    vi.spyOn(performance, 'now').mockImplementation(() => {
      return perfNow
    })
    // Advance in small steps so each interval callback sees incremental time
    ;(globalThis as any).__advanceTime = (ms: number) => {
      const step = 67 // match TICK_MS
      let remaining = ms
      while (remaining > 0) {
        const delta = Math.min(step, remaining)
        perfNow += delta
        vi.advanceTimersByTime(delta)
        remaining -= delta
      }
    }
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    delete (globalThis as any).__advanceTime
  })

  const advanceTime = (ms: number) => (globalThis as any).__advanceTime(ms)

  it('should call onUpdate on start', () => {
    const states: CompassState[] = []
    const sim = createCompassSimulation(s => states.push({ ...s }))
    sim.start()

    expect(states.length).toBe(1)
    expect(states[0].angle).toBeDefined()

    sim.stop()
  })

  it('should update state over time', () => {
    const states: CompassState[] = []
    const sim = createCompassSimulation(s => states.push({ ...s }))
    sim.start()

    const initialCount = states.length
    advanceTime(500)

    expect(states.length).toBeGreaterThan(initialCount)
    sim.stop()
  })

  it('should stop updating after stop()', () => {
    const states: CompassState[] = []
    const sim = createCompassSimulation(s => states.push({ ...s }))
    sim.start()
    advanceTime(200)
    sim.stop()

    const countAfterStop = states.length
    advanceTime(500)

    expect(states.length).toBe(countAfterStop)
  })

  it('should not start twice', () => {
    const states: CompassState[] = []
    const sim = createCompassSimulation(s => states.push({ ...s }))
    sim.start()
    sim.start() // second call should be no-op

    advanceTime(200)
    sim.stop()

    expect(states.length).toBeGreaterThan(0)
  })

  it('should produce angular motion (angle changes over time)', () => {
    const states: CompassState[] = []
    const sim = createCompassSimulation(s => states.push({ ...s }))
    sim.start()

    advanceTime(3000)
    sim.stop()

    const angles = states.map(s => s.angle)
    const uniqueAngles = new Set(angles.map(a => Math.round(a * 100)))
    expect(uniqueAngles.size).toBeGreaterThan(1)
  })
})
