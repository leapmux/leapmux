import type { DotCluster } from './chatRailPolicy'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MarkType } from '~/generated/proto/leapmux/v1/agent_pb'
import { createDotPreview, POINTER_CLOSE_DELAY_MS } from './chatDotPreview'

// Flush queued Solid effects (the stale-hover re-anchor effect is not a pull-based memo).
const tick = () => new Promise<void>(resolve => setTimeout(resolve, 0))

// activeDot / cardTopPx are memos (pull-based), so these read synchronously after a signal
// write -- no effect flush needed. The scrub-warm DEBOUNCE effect (instant-hover vs settled-scrub
// timing) is exercised end-to-end by ChatScrollRail.test.tsx; here we pin the pure reactive
// selection + placement the component delegates to this unit, plus the open/close policy (which
// needs fake timers for the close delay).

afterEach(() => {
  vi.useRealTimers()
})

function dot(seq: bigint, topPx: number, count = 1): DotCluster {
  return { seq, topPx, type: MarkType.USER_MESSAGE, count }
}

describe('createDotPreview', () => {
  it('activeDot is the hovered dot when not dragging', () =>
    createRoot((dispose) => {
      const [dots] = createSignal<DotCluster[]>([dot(5n, 100)])
      const [drag] = createSignal<number | null>(null)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const [cardHeightPx] = createSignal(0)
      const p = createDotPreview({ dots, drag, railHeight, thumbHeightPx, cardHeightPx })
      expect(p.activeDot()).toBeNull()
      p.openDot(dot(5n, 100))
      expect(p.activeDot()?.seq).toBe(5n)
      dispose()
    }))

  it('a scrub target takes precedence over a hovered dot (one card, never two)', () =>
    createRoot((dispose) => {
      // centerAxisY(0.5, 200, 24) = 12 + 0.5*176 = 100, so the dot at topPx 100 is under the thumb
      // centre while dragging; hovering a DIFFERENT dot must not open a rival card.
      const [dots] = createSignal<DotCluster[]>([dot(5n, 100), dot(9n, 20)])
      const [drag] = createSignal<number | null>(0.5)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const [cardHeightPx] = createSignal(0)
      const p = createDotPreview({ dots, drag, railHeight, thumbHeightPx, cardHeightPx })
      p.openDot(dot(9n, 20))
      expect(p.activeDot()?.seq).toBe(5n) // the scrub target wins over the hover
      dispose()
    }))

  it('a scrub that abandons the pressed dot shows no card while the thumb is between dots', () =>
    createRoot((dispose) => {
      const [dots] = createSignal<DotCluster[]>([dot(9n, 20)]) // topPx 20 is far from the thumb centre (100)
      const [drag] = createSignal<number | null>(0.5)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const [cardHeightPx] = createSignal(0)
      const p = createDotPreview({ dots, drag, railHeight, thumbHeightPx, cardHeightPx })
      // A mouse press starts ON a dot, so the pointer channel holds that dot for the whole
      // gesture: the captured drag fires no pointerleave to release it. The rail ABANDONS the
      // channel when the grab becomes a scrub, which is what stops the card pointing at a dot the
      // reader scrubbed away from.
      p.openDot(dot(9n, 20))
      p.abandonDot()
      expect(p.activeDot()).toBeNull()
      dispose()
    }))

  it('leaves an abandoned pointer channel closed when the drag ends, rather than springing back', () =>
    createRoot((dispose) => {
      const [dots] = createSignal<DotCluster[]>([dot(9n, 20)])
      const [drag, setDrag] = createSignal<number | null>(0.5)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const [cardHeightPx] = createSignal(0)
      const p = createDotPreview({ dots, drag, railHeight, thumbHeightPx, cardHeightPx })
      p.openDot(dot(9n, 20))
      p.abandonDot() // the grab became a scrub
      setDrag(null) // ...and the drag ended between dots
      // The pressed dot must NOT come back. A mask that only hid it would hand it straight back
      // here, pointing at a dot the reader scrubbed away from.
      expect(p.activeDot()).toBeNull()
      dispose()
    }))

  it('shows a dot the pointer reaches DURING the post-release hold, which drag() keeps open', () =>
    createRoot((dispose) => {
      const [dots] = createSignal<DotCluster[]>([dot(9n, 20)])
      // A release lands between dots and the hold pins the thumb there while an out-of-window
      // seek fetches -- drag() stays non-null for all of it, with the pointer already up.
      const [drag] = createSignal<number | null>(0.5)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const [cardHeightPx] = createSignal(0)
      const p = createDotPreview({ dots, drag, railHeight, thumbHeightPx, cardHeightPx })
      p.openDot(dot(9n, 20)) // the reader hovers a dot while the fetch is still in flight
      // Masking the hover channel for as long as drag() is non-null would show nothing at all
      // here, for as long as the fetch took.
      expect(p.activeDot()?.seq).toBe(9n)
      dispose()
    }))

  it('cardTopPx clamps a top/bottom dot inside the rail so the card is not clipped', () =>
    createRoot((dispose) => {
      const [dots] = createSignal<DotCluster[]>([dot(1n, 0), dot(2n, 200)])
      const [drag] = createSignal<number | null>(null)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const [cardHeightPx] = createSignal(120)
      const p = createDotPreview({ dots, drag, railHeight, thumbHeightPx, cardHeightPx })
      p.openDot(dot(1n, 0)) // a dot at the very top
      expect(p.cardTopPx()).toBe(60) // pushed down by its own half-height, not past the wrapper
      p.openDot(dot(2n, 200)) // a dot at the very bottom
      expect(p.cardTopPx()).toBe(140)
      dispose()
    }))

  it('cardTopPx clamps on the card\'s MEASURED height, not on the max-height cap', () =>
    createRoot((dispose) => {
      const [dots] = createSignal<DotCluster[]>([dot(1n, 8)])
      const [drag] = createSignal<number | null>(null)
      const [railHeight] = createSignal(600)
      const [thumbHeightPx] = createSignal(24)
      const [cardHeightPx] = createSignal(40) // a one-line preview, far short of the 200px cap
      const p = createDotPreview({ dots, drag, railHeight, thumbHeightPx, cardHeightPx })
      p.openDot(dot(1n, 8))
      // Clamping against half the CAP would put this card at y=100 -- 92px from the 8px dot it
      // describes, pointing at nothing. Its own half-height is the real floor.
      expect(p.cardTopPx()).toBe(20)
      dispose()
    }))

  it('cardTopPx still points at its dot on a rail shorter than the card cap', () =>
    createRoot((dispose) => {
      const [dots] = createSignal<DotCluster[]>([dot(1n, 40), dot(2n, 120)])
      const [drag] = createSignal<number | null>(null)
      const [railHeight] = createSignal(160) // shorter than 2 * the 200px cap
      const [thumbHeightPx] = createSignal(24)
      const [cardHeightPx] = createSignal(40)
      const p = createDotPreview({ dots, drag, railHeight, thumbHeightPx, cardHeightPx })
      // Against the cap the clamp interval collapsed to the single point rh/2, so EVERY dot's
      // card sat at the rail midpoint -- the short-viewport case the e2e run exercises.
      p.openDot(dot(1n, 40))
      expect(p.cardTopPx()).toBe(40)
      p.openDot(dot(2n, 120))
      expect(p.cardTopPx()).toBe(120)
      dispose()
    }))

  it('puts an unmeasured card exactly on its dot, rather than at a guessed offset', () =>
    createRoot((dispose) => {
      const [dots] = createSignal<DotCluster[]>([dot(1n, 8)])
      const [drag] = createSignal<number | null>(null)
      const [railHeight] = createSignal(600)
      const [thumbHeightPx] = createSignal(24)
      const [cardHeightPx] = createSignal(0) // the frame before the card reports its size
      const p = createDotPreview({ dots, drag, railHeight, thumbHeightPx, cardHeightPx })
      p.openDot(dot(1n, 8))
      expect(p.cardTopPx()).toBe(8)
      dispose()
    }))

  it('re-anchors the hovered card to the same-seq dot when a streaming turn shifts its topPx', async () => {
    await createRoot(async (dispose) => {
      const [dots, setDots] = createSignal<DotCluster[]>([dot(5n, 100)])
      const [drag] = createSignal<number | null>(null)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const [cardHeightPx] = createSignal(0)
      const p = createDotPreview({ dots, drag, railHeight, thumbHeightPx, cardHeightPx })
      p.openDot(dot(5n, 100))
      expect(p.activeDot()?.seq).toBe(5n)
      // maxSeq ticks during a streaming turn: the SAME seq's cluster re-rounds to a new topPx and
      // <For> hands over a fresh (unequal) object. The card must FOLLOW the dot, not vanish.
      setDots([dot(5n, 101)])
      await tick()
      expect(p.activeDot()?.seq).toBe(5n)
      expect(p.activeDot()?.topPx).toBe(101) // re-anchored to the shifted cluster
      dispose()
    })
  })

  it('clears the hovered card only when its seq is truly gone from the rail', async () => {
    await createRoot(async (dispose) => {
      const [dots, setDots] = createSignal<DotCluster[]>([dot(5n, 100)])
      const [drag] = createSignal<number | null>(null)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const [cardHeightPx] = createSignal(0)
      const p = createDotPreview({ dots, drag, railHeight, thumbHeightPx, cardHeightPx })
      p.openDot(dot(5n, 100))
      expect(p.activeDot()?.seq).toBe(5n)
      // The hovered mark's message was deleted/reseq'd -- no dot carries seq 5 anymore, so the
      // card clears rather than pointing at a stale cluster.
      setDots([dot(9n, 100)])
      await tick()
      expect(p.activeDot()).toBeNull()
      dispose()
    })
  })

  it('re-anchors a FOCUSED dot too, so a keyboard reader keeps the card a streaming turn moved', async () => {
    await createRoot(async (dispose) => {
      const [dots, setDots] = createSignal<DotCluster[]>([dot(5n, 100)])
      const [drag] = createSignal<number | null>(null)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const [cardHeightPx] = createSignal(0)
      const p = createDotPreview({ dots, drag, railHeight, thumbHeightPx, cardHeightPx })
      // The focus channel is a second holder of a cluster reference, so it needs the same
      // re-anchor the pointer channel gets -- otherwise a streaming tick strands a focused dot's
      // card on a torn-down cluster and it vanishes with no blur to explain it.
      p.focusDot(dot(5n, 100))
      setDots([dot(5n, 101)])
      await tick()
      expect(p.activeDot()?.topPx).toBe(101)

      // And it still clears when the seq is genuinely gone.
      setDots([dot(9n, 100)])
      await tick()
      expect(p.activeDot()).toBeNull()
      dispose()
    })
  })

  it('a re-anchor during a streaming turn does not extend a card the pointer already left', async () => {
    vi.useFakeTimers()
    await createRoot(async (dispose) => {
      const [dots, setDots] = createSignal<DotCluster[]>([dot(5n, 100)])
      const [drag] = createSignal<number | null>(null)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const [cardHeightPx] = createSignal(0)
      const p = createDotPreview({ dots, drag, railHeight, thumbHeightPx, cardHeightPx })
      p.openDot(dot(5n, 100))
      p.closeSoon()
      // A streaming turn re-rounds the same seq's topPx while the close is pending. The re-anchor
      // must follow the dot WITHOUT touching the timer -- otherwise a streaming conversation
      // cancels the close on every tick and pins the card open for good.
      setDots([dot(5n, 101)])
      await vi.advanceTimersByTimeAsync(POINTER_CLOSE_DELAY_MS - 50)
      expect(p.activeDot()?.topPx).toBe(101) // the re-anchor really ran within the close window
      await vi.advanceTimersByTimeAsync(50)
      expect(p.activeDot()).toBeNull() // and it left the pending close alone
      dispose()
    })
  })
})

describe('createDotPreview open/close policy', () => {
  /** A controller over one dot at topPx 100, with no drag in progress. */
  function oneDot() {
    const [dots] = createSignal<DotCluster[]>([dot(5n, 100), dot(9n, 20)])
    const [drag] = createSignal<number | null>(null)
    const [railHeight] = createSignal(200)
    const [thumbHeightPx] = createSignal(24)
    const [cardHeightPx] = createSignal(0)
    return createDotPreview({ dots, drag, railHeight, thumbHeightPx, cardHeightPx })
  }

  it('holds the card open for the close delay after the pointer leaves, then closes it', () => {
    vi.useFakeTimers()
    createRoot((dispose) => {
      const p = oneDot()
      p.openDot(dot(5n, 100))
      p.closeSoon()
      // The whole point of the delay: the card outlives the pointer leaving the dot, so the
      // reader can cross the gutter and land on it.
      vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS - 1)
      expect(p.activeDot()?.seq).toBe(5n)
      vi.advanceTimersByTime(1)
      expect(p.activeDot()).toBeNull()
      dispose()
    })
  })

  it('cancels a pending close when the pointer comes back (onto the dot or onto the card)', () => {
    vi.useFakeTimers()
    createRoot((dispose) => {
      const p = oneDot()
      p.openDot(dot(5n, 100))
      p.closeSoon()
      vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS - 1)
      p.openDot(dot(5n, 100)) // the card re-declares its own dot when the pointer reaches it
      vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS * 10) // the superseded timer never fires
      expect(p.activeDot()?.seq).toBe(5n)
      dispose()
    })
  })

  it('replaces the card at once when another dot opens during the close delay', () => {
    vi.useFakeTimers()
    createRoot((dispose) => {
      const p = oneDot()
      p.openDot(dot(5n, 100))
      p.closeSoon()
      p.openDot(dot(9n, 20)) // the pointer reached a second dot -- no wait, and no stale card
      expect(p.activeDot()?.seq).toBe(9n)
      // The first dot's pending close must not take the second dot's card down with it.
      vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS * 10)
      expect(p.activeDot()?.seq).toBe(9n)
      dispose()
    })
  })

  it('closeNow closes with no delay, and drops a pending close', () => {
    vi.useFakeTimers()
    createRoot((dispose) => {
      const p = oneDot()
      p.openDot(dot(5n, 100))
      p.closeSoon()
      p.closeNow()
      expect(p.activeDot()).toBeNull()
      expect(vi.getTimerCount()).toBe(0)
      dispose()
    })
  })

  it('keeps a FOCUSED dot open when the pointer channel closes, and drops it on blur', () => {
    vi.useFakeTimers()
    createRoot((dispose) => {
      const p = oneDot()
      p.focusDot(dot(5n, 100))
      p.openDot(dot(5n, 100)) // the pointer visits the card the focus opened
      p.closeSoon() // ...and leaves it again
      vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS * 10)
      expect(p.activeDot()?.seq).toBe(5n) // focus still holds it
      p.blurDot()
      expect(p.activeDot()).toBeNull() // and lets go at once, with no delay to wait out
      dispose()
    })
  })

  it('lets the pointer show a different dot than the focused one', () => {
    createRoot((dispose) => {
      const p = oneDot()
      p.focusDot(dot(9n, 20))
      p.openDot(dot(5n, 100))
      // The pointer is the input the reader is steering, so it wins while it holds a dot.
      expect(p.activeDot()?.seq).toBe(5n)
      p.closeNow()
      expect(p.activeDot()).toBeNull() // a teardown drops BOTH channels
      dispose()
    })
  })

  it('lets FOCUS take the card from a pointer parked on another dot', () => {
    createRoot((dispose) => {
      const p = oneDot()
      // The cursor rests on dot 5 and never moves, so no pointerleave is coming to release it --
      // the state a reader is in the moment after they use the rail.
      p.openDot(dot(5n, 100))
      p.focusDot(dot(9n, 20))
      // A channel the pointer could mask would leave dot 5's card under dot 9's focus ring, and
      // the warm effect fetching dot 5's message, for every dot the reader tabbed to.
      expect(p.activeDot()?.seq).toBe(9n)
      dispose()
    })
  })

  it('lets FOCUS take the card mid-close, with no delay left to wait out', () => {
    vi.useFakeTimers()
    createRoot((dispose) => {
      const p = oneDot()
      p.openDot(dot(5n, 100))
      p.closeSoon() // the pointer left dot 5; its close is pending
      p.focusDot(dot(9n, 20))
      // Not "dot 5 for the rest of the delay, then dot 9": reaching a dot by focus replaces the
      // card at once, exactly as reaching one by hover or by a scrub does.
      expect(p.activeDot()?.seq).toBe(9n)
      vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS * 10)
      expect(p.activeDot()?.seq).toBe(9n) // and the superseded close cannot take it down
      dispose()
    })
  })

  it('drops a pending close timer when its owner is disposed', () => {
    vi.useFakeTimers()
    createRoot((dispose) => {
      const p = oneDot()
      p.openDot(dot(5n, 100))
      p.closeSoon()
      expect(vi.getTimerCount()).toBe(1)
      dispose()
      // A timer surviving the dispose would write a signal of a torn-down rail.
      expect(vi.getTimerCount()).toBe(0)
    })
  })
})
