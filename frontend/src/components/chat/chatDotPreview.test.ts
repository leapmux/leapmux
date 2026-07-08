import type { DotCluster } from './chatRailPolicy'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { MarkType } from '~/generated/leapmux/v1/agent_pb'
import { createDotPreview } from './chatDotPreview'
import * as styles from './ChatScrollRail.css'

// Flush queued Solid effects (the stale-hover re-anchor effect is not a pull-based memo).
const tick = () => new Promise<void>(resolve => setTimeout(resolve, 0))

// activeDot / popoverTopPx are memos (pull-based), so these read synchronously after a signal
// write -- no effect flush needed. The scrub-warm DEBOUNCE effect (instant-hover vs settled-scrub
// timing) is exercised end-to-end by ChatScrollRail.test.tsx; here we pin the pure reactive
// selection + placement the component delegates to this unit.

function dot(seq: bigint, topPx: number, count = 1): DotCluster {
  return { seq, topPx, type: MarkType.USER_MESSAGE, count }
}

describe('createdotpreview', () => {
  it('activeDot is the hovered dot when not dragging', () =>
    createRoot((dispose) => {
      const [dots] = createSignal<DotCluster[]>([dot(5n, 100)])
      const [drag] = createSignal<number | null>(null)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const p = createDotPreview({ dots, drag, railHeight, thumbHeightPx })
      expect(p.activeDot()).toBeNull()
      p.setHoverDot(dot(5n, 100))
      expect(p.activeDot()?.seq).toBe(5n)
      dispose()
    }))

  it('a scrub target takes precedence over a hovered dot (one popover, never two)', () =>
    createRoot((dispose) => {
      // centerAxisY(0.5, 200, 24) = 12 + 0.5*176 = 100, so the dot at topPx 100 is under the thumb
      // centre while dragging; hovering a DIFFERENT dot must not open a rival popover.
      const [dots] = createSignal<DotCluster[]>([dot(5n, 100), dot(9n, 20)])
      const [drag] = createSignal<number | null>(0.5)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const p = createDotPreview({ dots, drag, railHeight, thumbHeightPx })
      p.setHoverDot(dot(9n, 20))
      expect(p.activeDot()?.seq).toBe(5n) // the scrub target wins over the hover
      dispose()
    }))

  it('activeDot falls back to the hovered dot when the scrub is between dots', () =>
    createRoot((dispose) => {
      const [dots] = createSignal<DotCluster[]>([dot(9n, 20)]) // topPx 20 is far from the thumb centre (100)
      const [drag] = createSignal<number | null>(0.5)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const p = createDotPreview({ dots, drag, railHeight, thumbHeightPx })
      p.setHoverDot(dot(9n, 20))
      expect(p.activeDot()?.seq).toBe(9n) // no dot within scrub range -> the hover shows
      dispose()
    }))

  it('popoverTopPx clamps a top/bottom dot inside the rail so the card is not clipped', () =>
    createRoot((dispose) => {
      const [dots] = createSignal<DotCluster[]>([dot(1n, 0), dot(2n, 200)])
      const [drag] = createSignal<number | null>(null)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const p = createDotPreview({ dots, drag, railHeight, thumbHeightPx })
      const half = Math.min(styles.PREVIEW_POPOVER_MAX_H_PX / 2, 200 / 2)
      p.setHoverDot(dot(1n, 0)) // a dot at the very top
      expect(p.popoverTopPx()).toBe(half)
      p.setHoverDot(dot(2n, 200)) // a dot at the very bottom
      expect(p.popoverTopPx()).toBe(200 - half)
      dispose()
    }))

  it('re-anchors the hovered popover to the same-seq dot when a streaming turn shifts its topPx', async () => {
    await createRoot(async (dispose) => {
      const [dots, setDots] = createSignal<DotCluster[]>([dot(5n, 100)])
      const [drag] = createSignal<number | null>(null)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const p = createDotPreview({ dots, drag, railHeight, thumbHeightPx })
      p.setHoverDot(dot(5n, 100))
      expect(p.activeDot()?.seq).toBe(5n)
      // maxSeq ticks during a streaming turn: the SAME seq's cluster re-rounds to a new topPx and
      // <For> hands over a fresh (unequal) object. The popover must FOLLOW the dot, not vanish.
      setDots([dot(5n, 101)])
      await tick()
      expect(p.activeDot()?.seq).toBe(5n)
      expect(p.activeDot()?.topPx).toBe(101) // re-anchored to the shifted cluster
      dispose()
    })
  })

  it('clears the hovered popover only when its seq is truly gone from the rail', async () => {
    await createRoot(async (dispose) => {
      const [dots, setDots] = createSignal<DotCluster[]>([dot(5n, 100)])
      const [drag] = createSignal<number | null>(null)
      const [railHeight] = createSignal(200)
      const [thumbHeightPx] = createSignal(24)
      const p = createDotPreview({ dots, drag, railHeight, thumbHeightPx })
      p.setHoverDot(dot(5n, 100))
      expect(p.activeDot()?.seq).toBe(5n)
      // The hovered mark's message was deleted/reseq'd -- no dot carries seq 5 anymore, so the
      // popover clears rather than pointing at a stale cluster.
      setDots([dot(9n, 100)])
      await tick()
      expect(p.activeDot()).toBeNull()
      dispose()
    })
  })
})
