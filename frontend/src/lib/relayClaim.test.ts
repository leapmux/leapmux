import { describe, expect, it, vi } from 'vitest'
import { relayClaim } from './relayClaim'

describe('relayClaim', () => {
  it('hands out a distinct, increasing id per claim', () => {
    const a = relayClaim.claim()
    const b = relayClaim.claim()
    const c = relayClaim.claim()
    expect(b).toBeGreaterThan(a)
    expect(c).toBeGreaterThan(b)
  })

  // 0 is the "unowned" sentinel, so a real claim must never collide with it.
  it('never hands out the unowned sentinel', () => {
    expect(relayClaim.claim()).toBeGreaterThan(0)
  })

  it('lets the current claimant release', () => {
    const id = relayClaim.claim()
    expect(relayClaim.releaseIfClaimable(id)).toBe(true)
  })

  // A superseded wrapper must leave the relay alone: CloseChannelRelay tears down
  // whichever relay is installed, so releasing here would kill the successor's.
  it('refuses a superseded id', () => {
    const a = relayClaim.claim()
    relayClaim.claim()
    expect(relayClaim.releaseIfClaimable(a)).toBe(false)
  })

  // An unowned relay must not be stranded just because the wrapper that displaced the
  // caller never got a relay of its own.
  it('lets a superseded id release once the relay is unowned', () => {
    const a = relayClaim.claim()
    const b = relayClaim.claim()
    relayClaim.abandon(b)
    expect(relayClaim.releaseIfClaimable(a)).toBe(true)
  })

  // Releasing surrenders the claim, which is what makes the post-open guard's second
  // release (after close() already released) still able to reap the relay the
  // in-flight open installed.
  it('leaves the relay unowned after a release, so a repeat release is allowed', () => {
    const id = relayClaim.claim()
    expect(relayClaim.releaseIfClaimable(id)).toBe(true)
    expect(relayClaim.releaseIfClaimable(id)).toBe(true)
  })

  it('abandon by the claimant marks the relay unowned', () => {
    const id = relayClaim.claim()
    relayClaim.abandon(id)
    // Unowned: any id may now reap it.
    expect(relayClaim.releaseIfClaimable(id + 1000)).toBe(true)
  })

  // Once a further successor has claimed, an earlier wrapper's abandon is a no-op --
  // otherwise it would disarm the successor's predecessor check and let a third
  // wrapper tear down a relay the successor owns.
  it('abandon by a superseded id does not clear the successor claim', () => {
    const a = relayClaim.claim()
    const b = relayClaim.claim()
    relayClaim.abandon(a)
    // b still holds, so a is still refused...
    expect(relayClaim.releaseIfClaimable(a)).toBe(false)
    // ...and b can still release.
    expect(relayClaim.releaseIfClaimable(b)).toBe(true)
  })

  // The sidecar's relay owner OUTLIVES a webview reload, so the id sequence must keep
  // advancing across one -- a per-load counter restarting at 0/1 would mint an id
  // BELOW the owner the sidecar still holds, and the sidecar's fence
  // (`current.owner > relayID`) would reject the fresh page's legitimate open of the
  // still-live relay as superseded, wedging the channel. The persisted high-water
  // mark keeps ids monotonic across the reload (mirrors nextOrgEventsRelayId).
  it('mints ids above the previous page load across a reload', async () => {
    // A session that reconnected at least once advances the mark past its seed.
    const first = relayClaim.claim()
    const second = relayClaim.claim()
    expect(second).toBeGreaterThan(first)

    // Simulate a reload: the module's in-memory counter resets to null, but the
    // persisted mark in localStorage survives (as the sidecar's owner does).
    vi.resetModules()
    const { relayClaim: reloaded } = await import('./relayClaim')

    expect(reloaded.claim()).toBeGreaterThan(second)
  })
})
