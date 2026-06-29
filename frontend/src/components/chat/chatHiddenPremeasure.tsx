import type { JSX } from 'solid-js'
import type { ClassifiedEntry } from './chatEntryCache'
import type { VirtualItem } from './useChatVirtualizer'
import { createEffect, createSignal, For, on, onCleanup } from 'solid-js'
import { monotonicNow } from './chatScrollGeometry'
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

function PremeasureRow(props: {
  candidate: ChatDomPremeasureCandidate
  renderBubble: (entry: ClassifiedEntry) => JSX.Element
  onMeasure: (id: string, height: number, heightKey: string | undefined, measureDurationMs: number, settled: boolean) => boolean
}): JSX.Element {
  let rowEl: HTMLDivElement | undefined
  let cancelFrame: (() => void) | undefined
  const [resizeObservationEnabled, setResizeObservationEnabled] = createSignal(true)
  const observationKey = () => `${props.candidate.item.id}\0${props.candidate.item.heightKey ?? ''}`
  const hasPendingImages = (): boolean => {
    const root = rowEl
    if (!root)
      return false
    return Array.from(root.querySelectorAll('img')).some(img => !img.complete)
  }
  const scheduleMeasure = (id: string, heightKey: string | undefined, onMeasure: typeof props.onMeasure): void => {
    cancelFrame?.()
    cancelFrame = scheduleFrame(() => {
      const started = monotonicNow()
      const height = rowEl?.getBoundingClientRect().height ?? 0
      const settled = !hasPendingImages()
      const accepted = onMeasure(id, height, heightKey, monotonicNow() - started, settled)
      if (accepted && !settled)
        setResizeObservationEnabled(false)
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
  onCleanup(() => cancelFrame?.())

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
          />
        )}
      </For>
    </div>
  )
}
