import { describe, expect, it } from 'vitest'
import { createScrollVelocity, VELOCITY_IDLE_MS } from './chatScrollVelocity'

/** A tracker over a clock that the test advances. */
function tracker() {
  let t = 0
  const velocity = createScrollVelocity({
    now: () => t,
    idleMs: VELOCITY_IDLE_MS,
  })
  return {
    velocity,
    at: (ms: number) => {
      t = ms
    },
  }
}

describe('createScrollVelocity', () => {
  it('returns 0 before two samples', () => {
    const { velocity } = tracker()
    expect(velocity.speed()).toBe(0)
    velocity.sample(100)
    expect(velocity.speed()).toBe(0)
  })

  it('measures the speed between two samples', () => {
    const { velocity, at } = tracker()
    velocity.sample(0)
    at(16)
    velocity.sample(160)
    expect(velocity.speed()).toBe(10)
  })

  it('measures an upward move as a positive speed', () => {
    const { velocity, at } = tracker()
    velocity.sample(1000)
    at(10)
    velocity.sample(900)
    expect(velocity.speed()).toBe(10)
  })

  it('returns 0 after the idle time', () => {
    const { velocity, at } = tracker()
    velocity.sample(0)
    at(16)
    velocity.sample(160)
    at(16 + VELOCITY_IDLE_MS)
    expect(velocity.speed()).toBe(10)
    at(16 + VELOCITY_IDLE_MS + 1)
    expect(velocity.speed()).toBe(0)
  })

  it('drops a sample with no interval', () => {
    const { velocity } = tracker()
    velocity.sample(0)
    velocity.sample(500)
    expect(velocity.speed()).toBe(0)
  })

  it('measures the next sample from the last timed baseline after a dropped one', () => {
    const { velocity, at } = tracker()
    velocity.sample(0)
    velocity.sample(500)
    at(10)
    velocity.sample(100)
    expect(velocity.speed()).toBe(10)
  })

  it('measures from a programmatic write', () => {
    const { velocity, at } = tracker()
    velocity.sample(0)
    at(100)
    velocity.syncToProgrammatic(1000)
    at(116)
    velocity.sample(1016)
    expect(velocity.speed()).toBe(1)
  })

  it('keeps the idle time measured from the last real sample after a write', () => {
    const { velocity, at } = tracker()
    velocity.sample(0)
    at(16)
    velocity.sample(160)
    at(100)
    velocity.syncToProgrammatic(5000)
    at(16 + VELOCITY_IDLE_MS + 1)
    expect(velocity.speed()).toBe(0)
  })

  it('ignores a programmatic write before the first sample', () => {
    const { velocity, at } = tracker()
    velocity.syncToProgrammatic(500)
    at(16)
    velocity.sample(600)
    expect(velocity.speed()).toBe(0)
  })
})
