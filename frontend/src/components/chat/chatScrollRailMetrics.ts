import type { Accessor } from 'solid-js'
import type { UseChatVirtualizerResult } from './useChatVirtualizer'
import { createEffect, createSignal, on, onCleanup } from 'solid-js'
import { createRafCoalescer } from '~/lib/rafCoalesce'
import { distFromBottom, readLogicalScrollTop } from './chatScrollGeometry'

// ---------------------------------------------------------------------------
// Scroll-rail metrics sampler
//
// Reactively samples the chat scroll container's position -- the inputs the rail's thumb
// geometry is computed from -- via a passive scroll listener + ResizeObserver, re-sampling
// on a geometry commit that moves the offset map without a scroll event. Extracted from
// ChatScrollRail so the "metrics sampling" job has a name and a seam, and the component is
// left to orchestrate geometry rather than manage DOM listeners.
// ---------------------------------------------------------------------------

export interface RailScrollMetrics {
  /**
   * The content coordinate of the viewport top: the logical (clamped) scrollTop of the
   * scroll container, converted to content coordinates, where the rail's row offsets live.
   */
  contentTop: number
  /** distFromBottom(el): scrollHeight - scrollTop - clientHeight, for the bottom-edge snap. A scroll-coordinate distance. */
  dist: number
  /** The scroll container's viewport height. */
  clientHeight: number
}

export interface RailMetricsInputs {
  /** The chat scroll container (the element the native scrollbar was hidden on). */
  scrollEl: Accessor<HTMLDivElement | undefined>
  /**
   * The virtualizer members that the metrics read. `contentTopOf` converts each sample to
   * content coordinates. A change of `totalHeight`, `geometryVersion`, or `layoutShift` can move
   * the content without a scroll event, so each one is a trigger to sample again.
   */
  geometry: Accessor<Pick<UseChatVirtualizerResult, 'totalHeight' | 'geometryVersion' | 'contentTopOf' | 'layoutShift'>>
}

/**
 * Sample the scroll container's metrics reactively. The signal uses a VALUE equals so an
 * identical re-sample (a ResizeObserver tick with unchanged size, or a rAF landing on the
 * same scrollTop) does NOT invalidate the thumb memos downstream.
 *
 * Must be created within an owner scope (it wires effects + onCleanup); ChatScrollRail
 * creates it once at component top level.
 */
export function createRailMetrics(inp: RailMetricsInputs): Accessor<RailScrollMetrics> {
  const [metrics, setMetrics] = createSignal<RailScrollMetrics>(
    { contentTop: 0, dist: 0, clientHeight: 0 },
    {
      equals: (a, b) => a.contentTop === b.contentTop && a.dist === b.dist && a.clientHeight === b.clientHeight,
    },
  )

  const sample = () => {
    const el = inp.scrollEl()
    if (!el)
      return
    setMetrics({
      contentTop: Math.max(0, inp.geometry().contentTopOf(readLogicalScrollTop(el))),
      dist: distFromBottom(el),
      clientHeight: el.clientHeight,
    })
  }

  // Attach the passive scroll listener + ResizeObserver whenever the scroll element changes.
  createEffect(on(inp.scrollEl, (el) => {
    if (!el)
      return
    // rAF-coalesce the scroll burst through the shared helper (one sample per frame) rather
    // than re-hand-rolling the schedule-once + cancel-on-teardown lifecycle.
    const scrollCoalescer = createRafCoalescer<void>(sample)
    const onScroll = () => scrollCoalescer.push()
    el.addEventListener('scroll', onScroll, { passive: true })
    const ro = new ResizeObserver(() => sample())
    ro.observe(el)
    sample()
    onCleanup(() => {
      el.removeEventListener('scroll', onScroll)
      ro.disconnect()
      scrollCoalescer.abort()
    })
  }))

  // A change of the total height, a row-height commit, or a change of the layout shift can
  // move the content without a scroll event; re-sample so the thumb recomputes against the new
  // mapping. rAF-coalesced (like the
  // scroll path above) so a streaming turn's burst of commits forces at most ONE layout read
  // per frame rather than one synchronous read per commit -- the value-equals signal drops the
  // redundant re-samples, and the thumb lagging a commit by a single frame is imperceptible.
  // Where rAF is unavailable (SSR / some test envs), the coalescer samples synchronously.
  const geoCoalescer = createRafCoalescer<void>(sample)
  createEffect(on(() => {
    const geometry = inp.geometry()
    return [
      geometry.totalHeight(),
      geometry.geometryVersion(),
      geometry.layoutShift(),
    ]
  }, () => geoCoalescer.push()))
  onCleanup(() => geoCoalescer.abort())

  return metrics
}
