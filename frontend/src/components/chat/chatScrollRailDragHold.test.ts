import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createDragReleaseHold } from './chatScrollRailDragHold'

/** Flush pending microtasks (a macrotask runs after every queued microtask). */
const tick = () => new Promise<void>(resolve => setTimeout(resolve, 0))

// Deterministic requestAnimationFrame: the hold schedules its post-landing clear on a frame,
// so the test drives frames by hand rather than racing a real ~16ms rAF timer. cancelAnimation-
// Frame genuinely removes the callback, so a superseded clear can be asserted as never firing.
let rafCallbacks: Map<number, () => void>
let nextRafId: number
function runFrame() {
  const cbs = [...rafCallbacks.values()]
  rafCallbacks.clear()
  cbs.forEach(cb => cb())
}
beforeEach(() => {
  rafCallbacks = new Map()
  nextRafId = 0
  vi.stubGlobal('requestAnimationFrame', (cb: () => void) => {
    const id = ++nextRafId
    rafCallbacks.set(id, cb)
    return id
  })
  vi.stubGlobal('cancelAnimationFrame', (id: number) => {
    rafCallbacks.delete(id)
  })
})
afterEach(() => vi.unstubAllGlobals())

/** Run `body` inside a root, rejecting the returned promise on throw so async assertions surface. */
function inRoot(body: (dispose: () => void) => Promise<void>): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    createRoot(async (dispose) => {
      try {
        await body(dispose)
        dispose()
        resolve()
      }
      catch (e) {
        dispose()
        reject(e instanceof Error ? e : new Error(String(e)))
      }
    })
  })
}

describe('createdragreleasehold', () => {
  it('begin() claims ownership and rejects a rival concurrent grab until end()', () =>
    createRoot((dispose) => {
      const hold = createDragReleaseHold()
      expect(hold.begin()).toBe(true) // first grab claims
      expect(hold.begin()).toBe(false) // a rival second-finger grab is dropped
      hold.end() // the pointer lifecycle ended
      expect(hold.begin()).toBe(true) // a fresh grab can claim again
      dispose()
    }))

  it('preview() sets and clears the live drag fraction', () =>
    createRoot((dispose) => {
      const hold = createDragReleaseHold()
      expect(hold.fraction()).toBeNull()
      hold.preview(0.42)
      expect(hold.fraction()).toBe(0.42)
      hold.preview(null) // an aborted drag drops the preview
      expect(hold.fraction()).toBeNull()
      dispose()
    }))

  it('release() pins the fraction, then clears it ONE FRAME after the seek resolves (scrolled)', () =>
    inRoot(async () => {
      const hold = createDragReleaseHold()
      hold.release(0.7, () => Promise.resolve(true)) // the seek moved the view
      expect(hold.fraction()).toBe(0.7) // pinned at the release fraction (no flash back to origin)
      await tick() // the seek resolves; the clear is SCHEDULED for the next frame, not run now
      expect(hold.fraction()).toBe(0.7) // still pinned until the landing frame
      runFrame() // the next frame: the metrics-derived thumb has caught up to the landing
      expect(hold.fraction()).toBeNull() // handed off without a flash
    }))

  it('an ambient change during the seek\'s async fetch does NOT clear the hold early (out-of-window)', () =>
    inRoot(async () => {
      const hold = createDragReleaseHold()
      // An out-of-window seek awaits a fetch before landOnSeq scrolls. Frames elapse during it --
      // each would carry an ambient metrics change (window swap / streaming / RO tick). None may
      // hand off the pin, because the clear is armed off the seek's RESOLUTION, not those changes.
      let land!: (scrolled: boolean) => void
      hold.release(0.6, () => new Promise<boolean>((r) => {
        land = r
      }))
      expect(hold.fraction()).toBe(0.6)
      runFrame()
      runFrame()
      expect(hold.fraction()).toBe(0.6) // still pinned through the whole in-flight fetch
      land(true) // the fetch resolves and the landing scrolled
      await tick()
      expect(hold.fraction()).toBe(0.6) // still pinned until the landing frame
      runFrame()
      expect(hold.fraction()).toBeNull() // now handed off
    }))

  it('release() clears immediately when the seek scrolled nowhere (no landing frame will come)', () =>
    inRoot(async () => {
      const hold = createDragReleaseHold()
      hold.release(0.3, () => Promise.resolve(false)) // a landing that scrolls nowhere
      expect(hold.fraction()).toBe(0.3) // pinned immediately after release
      await tick()
      // Cleared on resolution -- no frame wait -- so the thumb never stays stuck dragging.
      expect(hold.fraction()).toBeNull()
      expect(rafCallbacks.size).toBe(0) // and no clear frame was scheduled
    }))

  it('release() clears when the seek throws or rejects (no stuck dragging state)', () =>
    inRoot(async () => {
      const hold = createDragReleaseHold()
      hold.release(0.4, () => Promise.reject(new Error('seek failed')))
      expect(hold.fraction()).toBe(0.4)
      await tick()
      expect(hold.fraction()).toBeNull()

      hold.release(0.6, () => {
        throw new Error('sync seek failed')
      })
      expect(hold.fraction()).toBe(0.6)
      await tick()
      expect(hold.fraction()).toBeNull()
    }))

  it('a fresh grab supersedes a still-pending release: its stale clear frame cannot clear the new preview', () =>
    inRoot(async () => {
      const hold = createDragReleaseHold()
      hold.release(0.3, () => Promise.resolve(true)) // would schedule a clear frame...
      await tick() // the seek resolves and schedules the clear frame
      expect(rafCallbacks.size).toBe(1)
      expect(hold.begin()).toBe(true) // ...but a fresh grab claims ownership, cancelling it
      expect(rafCallbacks.size).toBe(0) // the pending clear frame was cancelled
      hold.preview(0.9) // the new drag live-previews
      runFrame() // no-op: the stale clear frame is gone
      expect(hold.fraction()).toBe(0.9) // not cleared: begin() cancelled the clear + bumped the epoch
    }))

  it('a newer release supersedes an older one\'s pending clear (epoch guard)', () =>
    inRoot(async () => {
      const hold = createDragReleaseHold()
      hold.release(0.2, () => Promise.resolve(true))
      await tick() // older release's clear frame scheduled
      hold.release(0.8, () => Promise.resolve(true)) // a newer release supersedes it
      expect(hold.fraction()).toBe(0.8) // pinned at the newer fraction; the older clear was cancelled
      await tick()
      runFrame() // only the newer release's clear frame runs
      expect(hold.fraction()).toBeNull()
    }))
})
