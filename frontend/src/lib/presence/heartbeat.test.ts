import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { claimPresenceWhenReady, mountPresenceHeartbeat } from './heartbeat'

describe('mountPresenceHeartbeat', () => {
  beforeEach(() => {
    // Fake performance so monotonicNow (throttle clock) advances with
    // vi.advanceTimersByTime — wall Date.now is irrelevant here.
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'performance'] })
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('does NOT fire on module mount — pingNow is the stream-mount entry point', () => {
    const sender = vi.fn()
    const hb = mountPresenceHeartbeat({
      workspaceId: () => 'w1',
      sender,
    })
    // The hub's PresenceUpdate broadcast only reaches subscribers, so
    // a heartbeat sent before /ws/userevents is connected races the
    // broadcast and the client misses its own active-client signal.
    expect(sender).not.toHaveBeenCalled()
    hb.stop()
  })

  it('pingNow fires an immediate heartbeat', () => {
    const sender = vi.fn()
    const hb = mountPresenceHeartbeat({
      workspaceId: () => 'w1',
      sender,
    })
    hb.pingNow()
    expect(sender).toHaveBeenCalledTimes(1)
    expect(sender).toHaveBeenCalledWith('w1')
    hb.stop()
  })

  it('pingNow does not fire when workspace is empty', () => {
    const sender = vi.fn()
    const hb = mountPresenceHeartbeat({
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

describe('claimPresenceWhenReady', () => {
  /** Run the claim over two signals, and return the setters and the claims that it sent, by workspace. */
  function claims(initial: { bootstrapped: boolean, workspaceId: string }) {
    const sent: string[] = []
    let setBootstrapped!: (value: boolean) => void
    let setWorkspaceId!: (value: string) => void
    const dispose = createRoot((disposeRoot) => {
      const [bootstrapped, writeBootstrapped] = createSignal(initial.bootstrapped)
      const [workspaceId, writeWorkspaceId] = createSignal(initial.workspaceId)
      setBootstrapped = writeBootstrapped
      setWorkspaceId = writeWorkspaceId
      claimPresenceWhenReady({ bootstrapped, workspaceId, pingNow: () => sent.push(workspaceId()) })
      return disposeRoot
    })
    return { sent, setBootstrapped: (value: boolean) => setBootstrapped(value), setWorkspaceId: (value: string) => setWorkspaceId(value), dispose }
  }

  it('sends nothing before the stream bootstraps', () => {
    const claim = claims({ bootstrapped: false, workspaceId: 'w1' })
    claim.setWorkspaceId('w2')
    expect(claim.sent).toEqual([])
    claim.dispose()
  })

  it('claims the workspace in view when the stream bootstraps', () => {
    const claim = claims({ bootstrapped: false, workspaceId: 'w1' })
    claim.setBootstrapped(true)
    expect(claim.sent).toEqual(['w1'])
    claim.dispose()
  })

  // A reload restores the workspace in view from browser storage, which can finish after the stream bootstraps.
  it('claims the workspace that resolves after the stream bootstrapped', () => {
    const claim = claims({ bootstrapped: true, workspaceId: '' })
    expect(claim.sent).toEqual([])
    claim.setWorkspaceId('w1')
    expect(claim.sent).toEqual(['w1'])
    claim.dispose()
  })

  // The pointer event of the click that switches the workspace sends its heartbeat for the workspace that it leaves.
  it('claims the workspace that the user switches to while the stream is live', () => {
    const claim = claims({ bootstrapped: true, workspaceId: 'w1' })
    claim.setWorkspaceId('w2')
    claim.setWorkspaceId('w1')
    expect(claim.sent).toEqual(['w1', 'w2', 'w1'])
    claim.dispose()
  })

  it('claims again on each new bootstrap of the stream', () => {
    const claim = claims({ bootstrapped: true, workspaceId: 'w1' })
    claim.setBootstrapped(false)
    claim.setBootstrapped(true)
    expect(claim.sent).toEqual(['w1', 'w1'])
    claim.dispose()
  })

  it('claims nothing again while the workspace and the stream stay the same', () => {
    const claim = claims({ bootstrapped: true, workspaceId: 'w1' })
    claim.setWorkspaceId('w1')
    claim.setBootstrapped(true)
    expect(claim.sent).toEqual(['w1'])
    claim.dispose()
  })
})
