import type { CardLane } from './chatDotPreview'
import type { DotCluster } from './chatRailPolicy'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MarkType } from '~/generated/proto/leapmux/v1/agent_pb'
import { cardLaneFor, createDotPreview, POINTER_CLOSE_DELAY_MS } from './chatDotPreview'

// Flush queued Solid effects (the stale-hover re-anchor effect is not a pull-based memo).
const tick = () => new Promise<void>(resolve => setTimeout(resolve, 0))

// activeDot is a memo (pull-based), so these read synchronously after a signal write -- no
// effect flush needed. The scrub-warm DEBOUNCE effect (instant-hover vs settled-scrub timing) is
// exercised end-to-end by ChatScrollRail.test.tsx; here we pin the pure reactive selection and
// the card placement (cardLaneFor) the component delegates to this unit, plus the open/close
// policy (which needs fake timers for the close delay).

afterEach(() => {
  vi.useRealTimers()
})

function dot(seq: bigint, topPx: number, count = 1): DotCluster {
  return {
    seq,
    topPx,
    type: MarkType.USER_MESSAGE,
    count,
  }
}

describe('createDotPreview', () => {
  it('reports the hovered dot as activeDot when not dragging', () =>
    createRoot((dispose) => {
      const [dots] = createSignal<DotCluster[]>([
        dot(5n, 100),
      ])
      const [drag] = createSignal<number | null>(null)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const p = createDotPreview({
        dots,
        drag,
        railHeight,
        thumbHeightPx,
      })
      expect(p.activeDot()).toBeNull()
      p.openDot(dot(5n, 100))
      expect(p.activeDot()?.seq).toBe(5n)
      dispose()
    }))

  it('a scrub target takes precedence over a hovered dot (one card, never two)', () =>
    createRoot((dispose) => {
      // centerAxisY(0.5, 200, 24) = 12 + 0.5*176 = 100, so the dot at topPx 100 is under the thumb
      // centre while dragging; hovering a DIFFERENT dot must not open a rival card.
      const [dots] = createSignal<DotCluster[]>([
        dot(5n, 100),
        dot(9n, 20),
      ])
      const [drag] = createSignal<number | null>(0.5)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const p = createDotPreview({
        dots,
        drag,
        railHeight,
        thumbHeightPx,
      })
      p.openDot(dot(9n, 20))
      expect(p.activeDot()?.seq).toBe(5n) // the scrub target wins over the hover
      dispose()
    }))

  it('a scrub that abandons the pressed dot shows no card while the thumb is between dots', () =>
    createRoot((dispose) => {
      const [dots] = createSignal<DotCluster[]>([
        dot(9n, 20),
      ]) // topPx 20 is far from the thumb centre (100)
      const [drag] = createSignal<number | null>(0.5)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const p = createDotPreview({
        dots,
        drag,
        railHeight,
        thumbHeightPx,
      })
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
      const [dots] = createSignal<DotCluster[]>([
        dot(9n, 20),
      ])
      const [drag, setDrag] = createSignal<number | null>(0.5)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const p = createDotPreview({
        dots,
        drag,
        railHeight,
        thumbHeightPx,
      })
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
      const [dots] = createSignal<DotCluster[]>([
        dot(9n, 20),
      ])
      // A release lands between dots and the hold pins the thumb there while an out-of-window
      // seek fetches -- drag() stays non-null for all of it, with the pointer already up.
      const [drag] = createSignal<number | null>(0.5)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const p = createDotPreview({
        dots,
        drag,
        railHeight,
        thumbHeightPx,
      })
      p.openDot(dot(9n, 20)) // the reader hovers a dot while the fetch is still in flight
      // Masking the hover channel for as long as drag() is non-null would show nothing at all
      // here, for as long as the fetch took.
      expect(p.activeDot()?.seq).toBe(9n)
      dispose()
    }))

  it('re-anchors the hovered card to the same-seq dot when a streaming turn shifts its topPx', async () => {
    await createRoot(async (dispose) => {
      const [dots, setDots] = createSignal<DotCluster[]>([
        dot(5n, 100),
      ])
      const [drag] = createSignal<number | null>(null)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const p = createDotPreview({
        dots,
        drag,
        railHeight,
        thumbHeightPx,
      })
      p.openDot(dot(5n, 100))
      expect(p.activeDot()?.seq).toBe(5n)
      // maxSeq ticks during a streaming turn: the SAME seq's cluster re-rounds to a new topPx and
      // <For> hands over a fresh (unequal) object. The card must FOLLOW the dot, not vanish.
      setDots([
        dot(5n, 101),
      ])
      await tick()
      expect(p.activeDot()?.seq).toBe(5n)
      expect(p.activeDot()?.topPx).toBe(101) // re-anchored to the shifted cluster
      dispose()
    })
  })

  it('clears the hovered card only when its seq is truly gone from the rail', async () => {
    await createRoot(async (dispose) => {
      const [dots, setDots] = createSignal<DotCluster[]>([
        dot(5n, 100),
      ])
      const [drag] = createSignal<number | null>(null)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const p = createDotPreview({
        dots,
        drag,
        railHeight,
        thumbHeightPx,
      })
      p.openDot(dot(5n, 100))
      expect(p.activeDot()?.seq).toBe(5n)
      // The hovered mark's message was deleted/reseq'd -- no dot carries seq 5 anymore, so the
      // card clears rather than pointing at a stale cluster.
      setDots([
        dot(9n, 100),
      ])
      await tick()
      expect(p.activeDot()).toBeNull()
      dispose()
    })
  })

  it('re-anchors a FOCUSED dot too, so a keyboard reader keeps the card a streaming turn moved', async () => {
    await createRoot(async (dispose) => {
      const [dots, setDots] = createSignal<DotCluster[]>([
        dot(5n, 100),
      ])
      const [drag] = createSignal<number | null>(null)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const p = createDotPreview({
        dots,
        drag,
        railHeight,
        thumbHeightPx,
      })
      // The focus channel is a second holder of a cluster reference, so it needs the same
      // re-anchor the pointer channel gets -- otherwise a streaming tick strands a focused dot's
      // card on a torn-down cluster and it vanishes with no blur to explain it.
      p.focusDot(dot(5n, 100))
      setDots([
        dot(5n, 101),
      ])
      await tick()
      expect(p.activeDot()?.topPx).toBe(101)

      // And it still clears when the seq is genuinely gone.
      setDots([
        dot(9n, 100),
      ])
      await tick()
      expect(p.activeDot()).toBeNull()
      dispose()
    })
  })

  it('a re-anchor during a streaming turn does not extend a card the pointer already left', async () => {
    vi.useFakeTimers()
    await createRoot(async (dispose) => {
      const [dots, setDots] = createSignal<DotCluster[]>([
        dot(5n, 100),
      ])
      const [drag] = createSignal<number | null>(null)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const p = createDotPreview({
        dots,
        drag,
        railHeight,
        thumbHeightPx,
      })
      p.openDot(dot(5n, 100))
      p.closeSoon()
      // A streaming turn re-rounds the same seq's topPx while the close is pending. The re-anchor
      // must follow the dot WITHOUT touching the timer -- otherwise a streaming conversation
      // cancels the close on every tick and pins the card open for good.
      setDots([
        dot(5n, 101),
      ])
      await vi.advanceTimersByTimeAsync(POINTER_CLOSE_DELAY_MS - 50)
      expect(p.activeDot()?.topPx).toBe(101) // the re-anchor really ran within the close window
      await vi.advanceTimersByTimeAsync(50)
      expect(p.activeDot()).toBeNull() // and it left the pending close alone
      dispose()
    })
  })
})

/**
 * The top edge (rail px) at which a browser lays out a card that is `heightPx` tall in `lane`.
 * jsdom computes no layout, so this models the CSS of previewCardLane and previewCard
 * (`./ChatScrollRail.css.ts`): a flex column whose single item has auto block margins. Positive
 * free space goes to the margins in equal halves. With negative free space the margins are 0 and
 * the item stays at the start of the line, which is the top of a column and the bottom of a
 * reversed column.
 */
function cardTopEdge(lane: CardLane, heightPx: number): number {
  const freePx = lane.heightPx - heightPx
  if (freePx >= 0)
    return lane.topPx + freePx / 2
  return lane.edge === 'top'
    ? lane.topPx
    : lane.topPx + lane.heightPx - heightPx
}

describe('cardLaneFor', () => {
  it('keeps a near-edge card inside the rail in the frame its preview grows it', () => {
    // The card showed "Loading preview…" and was 32px tall. The preview lands and the card is
    // 51px tall. The browser lays the card out at that height in this frame, so this first paint
    // must already sit inside the rail. The lane reads no card height, so no report can lag it.
    expect(cardTopEdge(cardLaneFor(8, 200), 32)).toBe(0)
    expect(cardTopEdge(cardLaneFor(8, 200), 51)).toBe(0)
    expect(cardTopEdge(cardLaneFor(192, 200), 32)).toBe(200 - 32)
    expect(cardTopEdge(cardLaneFor(192, 200), 51)).toBe(200 - 51)
  })

  it('centres a card on its dot when the card fits between the dot and the nearer rail edge', () => {
    expect(cardTopEdge(cardLaneFor(300, 600), 40)).toBe(280)
    expect(cardTopEdge(cardLaneFor(100, 600), 40)).toBe(80)
    expect(cardTopEdge(cardLaneFor(500, 600), 40)).toBe(480)
  })

  it('pushes a card in from the rail edge that it would cross, by its own height', () => {
    // A one-line card near the top stays next to its dot. Clamping it as though it were as tall
    // as the 200px max-height cap would put it ~90px away from the dot it describes.
    expect(cardTopEdge(cardLaneFor(8, 600), 40)).toBe(0)
    expect(cardTopEdge(cardLaneFor(0, 200), 120)).toBe(0)
    expect(cardTopEdge(cardLaneFor(200, 200), 120)).toBe(80)
  })

  it('centres each card on its own dot on a rail shorter than twice the card cap', () => {
    // Clamped against the cap, every card on such a rail sat at the rail midpoint.
    expect(cardTopEdge(cardLaneFor(40, 160), 40)).toBe(20)
    expect(cardTopEdge(cardLaneFor(120, 160), 40)).toBe(100)
  })

  it('places every card that fits the rail where a clamp on its laid-out height puts it', () => {
    const railPx = 300
    for (let dotPx = 0; dotPx <= railPx; dotPx += 5) {
      for (let heightPx = 0; heightPx <= railPx; heightPx += 7) {
        const clamped = Math.min(Math.max(dotPx - heightPx / 2, 0), railPx - heightPx)
        expect(cardTopEdge(cardLaneFor(dotPx, railPx), heightPx), `dot ${dotPx}, card ${heightPx}`).toBe(clamped)
      }
    }
  })

  it('reaches the nearer rail edge on both sides of its dot', () => {
    expect(cardLaneFor(30, 200)).toEqual({ topPx: 0, heightPx: 60, edge: 'top' })
    expect(cardLaneFor(100, 200)).toEqual({ topPx: 0, heightPx: 200, edge: 'top' })
    expect(cardLaneFor(150, 200)).toEqual({ topPx: 100, heightPx: 100, edge: 'bottom' })
  })

  it('fits a card as tall as the rail between both rail edges', () => {
    // The card's max-height is never above the rail height (PREVIEW_CARD_RAIL_HEIGHT_VAR), so a
    // card as tall as the rail is the tallest one. Wherever its dot sits, the card then fills the
    // rail exactly: its top edge is on the rail top, and its bottom edge on the rail bottom.
    const railPx = 100
    for (let dotPx = 0; dotPx <= railPx; dotPx += 5)
      expect(cardTopEdge(cardLaneFor(dotPx, railPx), railPx), `dot ${dotPx}`).toBe(0)
  })

  it('treats a dot outside the rail as one on the nearer rail edge', () => {
    expect(cardLaneFor(-20, 200)).toEqual({ topPx: 0, heightPx: 0, edge: 'top' })
    expect(cardLaneFor(260, 200)).toEqual({ topPx: 200, heightPx: 0, edge: 'bottom' })
  })

  it('gives an empty lane at the rail top before the rail has a height', () => {
    expect(cardLaneFor(12, 0)).toEqual({ topPx: 0, heightPx: 0, edge: 'top' })
    expect(cardLaneFor(12, -5)).toEqual({ topPx: 0, heightPx: 0, edge: 'top' })
  })
})

describe('createDotPreview open/close policy', () => {
  /** A controller over one dot at topPx 100, with no drag in progress. */
  function oneDot() {
    const [dots] = createSignal<DotCluster[]>([
      dot(5n, 100),
      dot(9n, 20),
    ])
    const [drag] = createSignal<number | null>(null)
    const [railHeight] = createSignal(200)
    const [thumbHeightPx] = createSignal(24)
    return createDotPreview({
      dots,
      drag,
      railHeight,
      thumbHeightPx,
    })
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

  it('closes with no delay, and drops a pending close, when closeNow runs', () => {
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
