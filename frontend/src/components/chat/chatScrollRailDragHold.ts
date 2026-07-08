import type { Accessor } from 'solid-js'
import { createSignal, onCleanup } from 'solid-js'

// ---------------------------------------------------------------------------
// Scroll-rail drag-release "hold"
//
// Owns the small state machine that pins the rail thumb at its RELEASE fraction until the
// post-release seek has scrolled the view to match -- the anti-flash hand-off. Extracted
// from ChatScrollRail (where it was a signal plus three bare `let`s, a deferred effect, and
// two functions tangled through the component) so the subtle "don't flash back / don't
// stick" behaviour is one named, unit-tested unit.
//
// The lifecycle across one grab:
//   begin()   -- a fresh grab claims ownership: drop any pending clear and invalidate a prior
//                release still awaiting its seek, then raise the active guard.
//   preview() -- the controller live-previews the thumb fraction during the drag.
//   release() -- the pointer released: pin the fraction, then clear it ONE FRAME after the
//                post-release seek RESOLVES -- OR immediately if the seek scrolled NOWHERE.
//   end()     -- the pointer lifecycle ended (release/cancel/unmount): free the active guard.
//
// Why clear off the seek's own resolution (+1 animation frame) rather than off "the first
// metrics change after release": an OUT-OF-WINDOW seek awaits a network fetch, during which
// the window swap, a streaming-measurement commit, or a ResizeObserver tick each change the
// scroll metrics BEFORE the landing scroll. Keying the clear off any metrics change would hand
// off on one of those and flash the thumb back to the pre-landing position -- exactly what this
// hold exists to prevent. The seek promise resolves only AFTER its landOnSeq has scrolled, so a
// clear scheduled from its resolution is guaranteed to be after the landing; the extra frame
// lets the metrics-derived thumb catch up to the landing so the hand-off itself doesn't flash.
// ---------------------------------------------------------------------------

export interface DragReleaseHold {
  /** The preview thumb fraction (null when idle). Read by the thumb/dots/scrub geometry. */
  fraction: Accessor<number | null>
  /**
   * Set the live preview fraction during a drag (the controller's `setDrag` sink); `null`
   * clears the preview on an aborted drag (pointercancel).
   */
  preview: (fraction: number | null) => void
  /**
   * Claim a fresh grab. Returns `false` when a drag is already active (a rival second-finger
   * grab, which the caller must drop rather than start a rival listener set); returns `true`
   * after claiming ownership -- which cancels any still-pending clear AND invalidates a prior
   * release's async clear so neither can clear THIS drag mid-way.
   */
  begin: () => boolean
  /** The pointer lifecycle ended (release/cancel/unmount): free the active guard. */
  end: () => void
  /**
   * Land a release at rail fraction `fraction`: pin the preview there, then hand off once the
   * post-release seek resolves. `seek` performs the jump and may resolve to whether it actually
   * moved the view; when it resolves `true` (the landing scrolled) the pin is cleared on the
   * NEXT animation frame -- by when the metrics-derived thumb has reached the landing, so the
   * hand-off is flash-free. When it resolves `false` (a landing that scrolls nowhere -- an
   * in-window target already at the current scrollTop, or no landable row) the pin is cleared
   * immediately instead of hanging forever.
   */
  release: (fraction: number, seek: () => void | Promise<boolean>) => void
}

/**
 * Create the drag-release hold. Clears the held preview one animation frame after the
 * post-release seek RESOLVES (not on an ambient metrics change), so an out-of-window seek's
 * in-flight fetch churn can't hand off before the landing.
 *
 * Must be created within an owner scope (it wires an `onCleanup`); ChatScrollRail creates it
 * once at component top level.
 */
export function createDragReleaseHold(): DragReleaseHold {
  const [fraction, setFraction] = createSignal<number | null>(null)
  // Bumped on every release/grab so a superseded release's async clear is a no-op (a fresh
  // grab, or a newer release, invalidates an older release still awaiting its seek).
  let releaseEpoch = 0
  // True while a drag holds the pointer capture + listeners. Guards against a SECOND
  // concurrent grab (a second finger on the thumb) starting a rival drag that would orphan
  // the first's listeners. Cleared by end().
  let active = false
  // A pending "clear the hold" animation frame scheduled after a scrolled landing; cancelled
  // by a fresh grab / a newer release / cleanup so it can never clear a newer drag's preview.
  let clearRaf: number | null = null

  const cancelClear = () => {
    if (clearRaf !== null && typeof cancelAnimationFrame === 'function')
      cancelAnimationFrame(clearRaf)
    clearRaf = null
  }
  onCleanup(cancelClear)

  // Clear the pin on the next frame IF `epoch` is still the current release -- by the next
  // frame the metrics-derived thumb has caught up to the landing, so the hand-off is flash-
  // free. Falls back to a synchronous clear where rAF is unavailable (SSR / some test envs).
  const clearNextFrame = (epoch: number) => {
    const clear = () => {
      clearRaf = null
      if (epoch === releaseEpoch)
        setFraction(null)
    }
    if (typeof requestAnimationFrame === 'function')
      clearRaf = requestAnimationFrame(clear)
    else
      clear()
  }

  const clearAfterFailedSeek = (epoch: number) => {
    void Promise.resolve().then(() => {
      if (epoch === releaseEpoch)
        setFraction(null)
    })
  }

  return {
    fraction,
    preview: setFraction,
    begin() {
      if (active)
        return false
      // A fresh grab owns the preview: invalidate a prior release's async clear (bump the
      // epoch) AND cancel a still-pending clear frame so neither can clear THIS drag mid-way.
      releaseEpoch++
      cancelClear()
      active = true
      return true
    },
    end() {
      active = false
    },
    release(fraction, seek) {
      setFraction(fraction)
      const epoch = ++releaseEpoch
      // Drop a prior release's still-pending clear frame; this release owns the hand-off now.
      cancelClear()
      let seekResult: void | Promise<boolean>
      try {
        seekResult = seek()
      }
      catch {
        clearAfterFailedSeek(epoch)
        return
      }
      void Promise.resolve(seekResult).then((scrolled) => {
        // Superseded by a fresh grab / newer release: leave the current preview alone.
        if (epoch !== releaseEpoch)
          return
        if (scrolled)
          clearNextFrame(epoch) // hand off one frame after the landing scrolled
        else
          setFraction(null) // scrolled nowhere: no landing scroll will come, clear now
      }).catch(() => {
        clearAfterFailedSeek(epoch)
      })
    },
  }
}
