import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mountPresenceHeartbeat } from './heartbeat'

describe('mountPresenceHeartbeat', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    // Make Date.now advance with the fake clock so the throttle logic
    // observes elapsed time. Without this the throttle never expires
    // because Date.now stays constant across `vi.advanceTimersByTime`.
    vi.setSystemTime(new Date(0))
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('does NOT fire on module mount — pingNow is the stream-mount entry point', () => {
    const sender = vi.fn()
    const hb = mountPresenceHeartbeat({
      orgId: () => 'org',
      workspaceId: () => 'w1',
      sender,
    })
    // The hub's PresenceUpdate broadcast only reaches subscribers, so
    // a heartbeat sent before /ws/orgevents is connected races the
    // broadcast and the client misses its own active-client signal.
    expect(sender).not.toHaveBeenCalled()
    hb.stop()
  })

  it('pingNow fires an immediate heartbeat', () => {
    const sender = vi.fn()
    const hb = mountPresenceHeartbeat({
      orgId: () => 'org',
      workspaceId: () => 'w1',
      sender,
    })
    hb.pingNow()
    expect(sender).toHaveBeenCalledTimes(1)
    expect(sender).toHaveBeenCalledWith('org', 'w1')
    hb.stop()
  })

  it('pingNow does not fire when workspace is empty', () => {
    const sender = vi.fn()
    const hb = mountPresenceHeartbeat({
      orgId: () => 'org',
      workspaceId: () => null,
      sender,
    })
    hb.pingNow()
    expect(sender).not.toHaveBeenCalled()
    hb.stop()
  })

  it('input events fire a throttled heartbeat', () => {
    const sender = vi.fn()
    const hb = mountPresenceHeartbeat({
      orgId: () => 'org',
      workspaceId: () => 'w1',
      sender,
    })
    hb.pingNow() // claim presence on "stream mount"
    expect(sender).toHaveBeenCalledTimes(1)
    sender.mockClear()

    // Synthesize a keydown — within the throttle window from pingNow → drop.
    document.dispatchEvent(new KeyboardEvent('keydown'))
    expect(sender).not.toHaveBeenCalled()

    // Advance past the throttle (5s).
    vi.advanceTimersByTime(5_001)
    document.dispatchEvent(new KeyboardEvent('keydown'))
    expect(sender).toHaveBeenCalledTimes(1)

    hb.stop()
  })

  it('visibility change to visible fires immediately, bypassing throttle', () => {
    const sender = vi.fn()
    const hb = mountPresenceHeartbeat({
      orgId: () => 'org',
      workspaceId: () => 'w1',
      sender,
    })
    sender.mockClear()
    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
    document.dispatchEvent(new Event('visibilitychange'))
    expect(sender).toHaveBeenCalledTimes(1)
    hb.stop()
  })

  it('does not fire on a long idle window — the hub holds presence for the WS lifetime', () => {
    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
    const sender = vi.fn()
    const hb = mountPresenceHeartbeat({
      orgId: () => 'org',
      workspaceId: () => 'w1',
      sender,
    })
    hb.pingNow()
    sender.mockClear()
    // No input, no visibility change — sender stays silent indefinitely.
    vi.advanceTimersByTime(5 * 60_000)
    expect(sender).not.toHaveBeenCalled()
    hb.stop()
  })

  it('returned stop unbinds listeners', () => {
    const sender = vi.fn()
    const hb = mountPresenceHeartbeat({
      orgId: () => 'org',
      workspaceId: () => 'w1',
      sender,
    })
    sender.mockClear()
    hb.stop()
    // Subsequent input must not trigger sender.
    document.dispatchEvent(new KeyboardEvent('keydown'))
    vi.advanceTimersByTime(60_000)
    expect(sender).not.toHaveBeenCalled()
  })
})
