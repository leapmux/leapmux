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
    expect(Number.isFinite(states[0].angle)).toBe(true)

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
    // Run two simulations for the same duration: one with a single start(),
    // one with a redundant second start(). If the second start() ever spins
    // up a second interval, the doubled run would observe ~2x as many ticks.
    const singleStates: CompassState[] = []
    const singleSim = createCompassSimulation(s => singleStates.push({ ...s }))
    singleSim.start()
    advanceTime(500)
    singleSim.stop()

    const doubleStates: CompassState[] = []
    const doubleSim = createCompassSimulation(s => doubleStates.push({ ...s }))
    doubleSim.start()
    doubleSim.start() // must be a no-op, not a second interval
    advanceTime(500)
    doubleSim.stop()

    expect(singleStates.length).toBeGreaterThan(1)
    expect(doubleStates.length).toBe(singleStates.length)
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
