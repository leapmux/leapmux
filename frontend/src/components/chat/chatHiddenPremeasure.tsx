import type { JSX } from 'solid-js'
import type { ClassifiedEntry } from './chatEntryCache'
import type { VirtualItem } from './useChatVirtualizer'
import { batch, createEffect, createSignal, For, on, onCleanup } from 'solid-js'
import { monotonicNow } from '~/lib/monotonicNow'
import * as styles from './ChatView.css'
import { spanLinesReservedWidth } from './widgets/SpanLines.geometry'

export interface ChatDomPremeasureCandidate {
  entry: ClassifiedEntry
  item: VirtualItem
}

export interface ChatDomPremeasureProps {
  candidates: readonly ChatDomPremeasureCandidate[]
  contentWidthPx: number
  renderBubble: (entry: ClassifiedEntry) => JSX.Element
  onMeasure: (id: string, height: number, heightKey: string | undefined, measureDurationMs: number, settled: boolean) => boolean
}

function scheduleFrame(cb: () => void): () => void {
  if (typeof requestAnimationFrame === 'function') {
    const frame = requestAnimationFrame(cb)
    return () => cancelAnimationFrame(frame)
  }
  let cancelled = false
  queueMicrotask(() => {
    if (!cancelled)
      cb()
  })
  return () => {
    cancelled = true
  }
}

interface PremeasureReadResult {
  height: number
  settled: boolean
  measureDurationMs: number
}

interface PremeasureTask {
  /** Layout reads only (getBoundingClientRect + the pending-image query) -- no commits. */
  read: () => PremeasureReadResult
  /** Commit the read result (the host's onMeasure); runs inside the shared batch(). */
  apply: (result: PremeasureReadResult) => void
}

/**
 * One shared frame for ALL pending premeasure rows, split into a read phase and a
 * commit phase. A band shift mounts up to the whole look-ahead band at once, and each
 * row's onMeasure commit synchronously rebuilds the offset map, rewrites the spacer
 * height, and runs the scroll re-pin effect -- so interleaving each row's rect read
 * with its commit forced a fresh reflow per row (k reflow + O(window) rebuild cycles
 * per band shift). Reading every pending row's rect FIRST costs one reflow total, and
 * committing them inside one batch() lets the downstream effects (spacer write,
 * re-pin) run once per frame instead of once per row.
 */
function createSharedPremeasureFrame() {
  const pending = new Map<string, PremeasureTask>()
  let cancelFrame: (() => void) | undefined
  const flush = () => {
    cancelFrame = undefined
    const tasks = [...pending.values()]
    pending.clear()
    // Phase 1: every layout read against one clean layout (the first pays the reflow).
    const results = tasks.map(task => task.read())
    // Phase 2: every commit in one reactive batch.
    batch(() => {
      tasks.forEach((task, i) => task.apply(results[i]))
    })
  }
  return {
    /** Queue (or replace) the pending measure for `key`; one frame serves all keys. */
    schedule(key: string, task: PremeasureTask) {
      pending.set(key, task)
      if (!cancelFrame)
        cancelFrame = scheduleFrame(flush)
    },
    /** Drop a row's pending measure (its element is unmounting). */
    cancel(key: string) {
      pending.delete(key)
    },
    dispose() {
      cancelFrame?.()
      cancelFrame = undefined
      pending.clear()
    },
  }
}

type SharedPremeasureFrame = ReturnType<typeof createSharedPremeasureFrame>

function PremeasureRow(props: {
  candidate: ChatDomPremeasureCandidate
  renderBubble: (entry: ClassifiedEntry) => JSX.Element
  onMeasure: (id: string, height: number, heightKey: string | undefined, measureDurationMs: number, settled: boolean) => boolean
  frame: SharedPremeasureFrame
}): JSX.Element {
  let rowEl: HTMLDivElement | undefined
  const [resizeObservationEnabled, setResizeObservationEnabled] = createSignal(true)
  const observationKey = () => `${props.candidate.item.id}\0${props.candidate.item.heightKey ?? ''}`
  const hasPendingImages = (): boolean => {
    const root = rowEl
    if (!root)
      return false
    return Array.from(root.querySelectorAll('img')).some(img => !img.complete)
  }
  const scheduleMeasure = (id: string, heightKey: string | undefined, onMeasure: typeof props.onMeasure): void => {
    // Keyed by row id, so a re-schedule for the same row (heightKey churn, an image
    // settling) replaces its pending task instead of measuring twice in one frame.
    props.frame.schedule(id, {
      read: () => {
        const started = monotonicNow()
        const height = rowEl?.getBoundingClientRect().height ?? 0
        const settled = !hasPendingImages()
        return { height, settled, measureDurationMs: monotonicNow() - started }
      },
      apply: ({ height, settled, measureDurationMs }) => {
        const accepted = onMeasure(id, height, heightKey, measureDurationMs, settled)
        if (accepted && !settled)
          setResizeObservationEnabled(false)
      },
    })
  }
  createEffect(on(observationKey, () => {
    setResizeObservationEnabled(true)
  }))
  createEffect(() => {
    const { item } = props.candidate
    scheduleMeasure(item.id, item.heightKey, props.onMeasure)
  })
  createEffect(() => {
    const root = rowEl
    observationKey()
    if (!root || !resizeObservationEnabled() || typeof ResizeObserver === 'undefined')
      return
    const observer = new ResizeObserver(() => {
      const { item } = props.candidate
      scheduleMeasure(item.id, item.heightKey, props.onMeasure)
    })
    observer.observe(root)
    onCleanup(() => observer.disconnect())
  })
  createEffect(() => {
    const root = rowEl
    if (!root)
      return
    const { item } = props.candidate
    const onImageSettled = () => scheduleMeasure(item.id, item.heightKey, props.onMeasure)
    const images = Array.from(root.querySelectorAll('img'))
    if (images.length === 0)
      return
    for (const img of images) {
      img.addEventListener('load', onImageSettled)
      img.addEventListener('error', onImageSettled)
    }
    onCleanup(() => {
      for (const img of images) {
        img.removeEventListener('load', onImageSettled)
        img.removeEventListener('error', onImageSettled)
      }
    })
  })
  onCleanup(() => props.frame.cancel(props.candidate.item.id))

  const reservedLeftPx = () => spanLinesReservedWidth(props.candidate.entry.parsedSpanLines.length)

  return (
    <div ref={rowEl} class={styles.premeasureRow}>
      <div style={{ 'margin-left': `${reservedLeftPx()}px` }}>
        {props.renderBubble(props.candidate.entry)}
      </div>
    </div>
  )
}

export function ChatHiddenPremeasure(props: ChatDomPremeasureProps): JSX.Element {
  // One measurement frame shared by every row (see createSharedPremeasureFrame): all
  // rows mounted in one flush read their rects together, then commit together.
  const frame = createSharedPremeasureFrame()
  onCleanup(() => frame.dispose())
  return (
    <div
      class={styles.premeasureRoot}
      style={{ width: `${Math.max(1, props.contentWidthPx)}px` }}
      aria-hidden="true"
    >
      <For each={props.candidates}>
        {candidate => (
          <PremeasureRow
            candidate={candidate}
            renderBubble={props.renderBubble}
            onMeasure={props.onMeasure}
            frame={frame}
          />
        )}
      </For>
    </div>
  )
}
