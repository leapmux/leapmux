import type { JSX } from 'solid-js'
import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatRowItem } from './chatRowItem'
import type { RenderReadiness } from './renderReadiness'
import { batch, createEffect, createMemo, For, onCleanup, onMount } from 'solid-js'
import { emitDevEvent } from '~/lib/devInstrument'
import { monotonicNow } from '~/lib/monotonicNow'
import { createRafCoalescer } from '~/lib/rafCoalesce'
import * as styles from './ChatView.css'
import { messageRowChrome } from './messageRowLayout'
import { createRenderReadinessScope, READINESS_REQUEST_LIMIT_MS } from './renderReadiness'
import { rowLayoutMeasurement } from './rowLayoutMeasurement'
import { reservedRowContentColumnStyle } from './widgets/SpanLines.geometry'

export interface ChatDomPremeasureCandidate {
  entry: ClassifiedEntry
  item: ChatRowItem
}

/**
 * The longest time that a hidden copy waits for an image before it reports a height.
 * A copy reports no height while an image of it still loads: the image can change the
 * height of the row when it arrives. An image that neither loads nor fails within this
 * time no longer holds the copy, so one slow image does not keep its row out of the
 * list. The row then joins with the height that the copy has, and it can change that
 * height once, when the image arrives.
 *
 * The user asked for one limit for both waits of a row, so this is the limit for a
 * Worker request of a body.
 */
export const PREMEASURE_IMAGE_SETTLE_LIMIT_MS = READINESS_REQUEST_LIMIT_MS

/** Commit the height that a hidden copy measured for row `id` under `heightKey`. */
export type PremeasureMeasureHandler = (id: string, height: number, heightKey: string | undefined, measureDurationMs: number) => void

/**
 * Render the hidden copy of one row. `readiness` is the copy's own scope: its
 * asynchronous bodies (Markdown, an edit diff) report to it, and the copy commits
 * no height until they are ready.
 */
export type PremeasureBubbleRenderer = (entry: ClassifiedEntry, readiness: RenderReadiness) => JSX.Element

export interface ChatDomPremeasureProps {
  candidates: readonly ChatDomPremeasureCandidate[]
  /**
   * The width (px) of the hidden copies: the exact width of the rows of the list, not a
   * rounded one. A copy measures the height that its row takes only when it wraps its text
   * as the row does.
   */
  contentWidthPx: number
  renderBubble: PremeasureBubbleRenderer
  onMeasure: PremeasureMeasureHandler
}

interface PremeasureReadResult {
  height: number
  measureDurationMs: number
}

interface PremeasureTask {
  /** Layout reads only (getBoundingClientRect + the pending-image query) -- no commits. */
  read: () => PremeasureReadResult | undefined
  /** Commit the read result (the host's onMeasure); runs inside the shared batch(). */
  apply: (result: PremeasureReadResult) => void
}

/**
 * One shared frame for ALL pending premeasure rows, split into a read phase and a
 * commit phase. A band shift mounts up to the whole look-ahead band at once, and each
 * row's onMeasure commit synchronously rebuilds the offset map, rewrites the spacer
 * height, and runs the geometry effect of the scroll hook -- so interleaving each row's rect read
 * with its commit forced a fresh reflow per row (k reflow + O(window) rebuild cycles
 * per band shift). Reading every pending row's rect FIRST costs one reflow total, and
 * committing them inside one batch() lets the downstream effects (spacer write,
 * layout shift) run once per frame instead of once per row.
 */
function createSharedPremeasureFrame() {
  // The pending measure of each row copy, by the task key of the copy: an object that the
  // copy creates, so the task of one copy can never stand for another copy of the same row.
  const pending = new Map<object, PremeasureTask>()
  const flush = () => {
    const tasks = [
      ...pending.values(),
    ]
    pending.clear()
    // Phase 1: every layout read against one clean layout (the first pays the reflow).
    const results = tasks.map(task => task.read())
    // Phase 2: every commit in one reactive batch.
    batch(() => {
      tasks.forEach((task, i) => {
        const result = results[i]
        if (result)
          task.apply(result)
      })
    })
  }
  // One frame serves every pending task. The coalescer also handles a frame that runs
  // inside its own request, and a task that a flush schedules gets a new frame.
  const frame = createRafCoalescer<void>(flush)
  return {
    /** Queue (or replace) the pending measure for `key`; one frame serves all keys. */
    schedule(key: object, task: PremeasureTask) {
      pending.set(key, task)
      frame.push()
    },
    /** Drop a row's pending measure (its element is unmounting). */
    cancel(key: object) {
      pending.delete(key)
    },
    dispose() {
      frame.abort()
      pending.clear()
    },
  }
}

type SharedPremeasureFrame = ReturnType<typeof createSharedPremeasureFrame>

function PremeasureRow(props: {
  candidate: ChatDomPremeasureCandidate
  renderBubble: PremeasureBubbleRenderer
  onMeasure: PremeasureMeasureHandler
  frame: SharedPremeasureFrame
}): JSX.Element {
  let rowEl: HTMLDivElement | undefined
  // The key of this copy in the shared frame.
  const taskKey = {}
  // The limit of the wait for the images of this copy (see PREMEASURE_IMAGE_SETTLE_LIMIT_MS):
  // its timer, and whether the time passed. One timer for each wait: the first read that
  // finds an image still loading starts it, and a read that finds none ends it. A copy
  // keeps one candidate, and so one height key, for its life: a new key gives a new
  // candidate object, and <For> then mounts a new copy (see createChatPremeasureBands).
  let imageLimit: { passed: boolean, timer: ReturnType<typeof setTimeout> } | undefined
  const clearImageLimit = (): void => {
    if (imageLimit !== undefined)
      clearTimeout(imageLimit.timer)
    imageLimit = undefined
  }
  // This copy's asynchronous bodies wait for a worker: a Markdown body for its HTML,
  // an edit diff for its hunks. Until every one has its final content, the copy's
  // height is wrong, and committing it would reveal the row at that height and move
  // every row below when the content lands.
  const readiness = createRenderReadinessScope()
  const hasPendingImages = (): boolean => {
    const root = rowEl
    if (!root)
      return false
    // A reserved box can still be revoked by the image's decode-time verifier
    // if the sniffed dimensions were wrong, so every incomplete image holds
    // the hidden measurement until load/error fires.
    return Array.from(root.querySelectorAll('img'))
      .some(img => !img.complete)
  }
  /**
   * Whether the images of the copy hold its measure: an image still loads, and the limit
   * of the current wait did not pass. The first call that finds such an image starts the
   * limit timer, which measures the copy again when it fires. A call that finds no image
   * loading ends the wait, so an image that mounts later starts a wait of its own.
   */
  const waitsForImages = (imagesPending: boolean): boolean => {
    if (!imagesPending) {
      clearImageLimit()
      return false
    }
    if (imageLimit === undefined) {
      const limit = {
        passed: false,
        timer: setTimeout(() => {
          limit.passed = true
          scheduleMeasure()
        }, PREMEASURE_IMAGE_SETTLE_LIMIT_MS),
      }
      imageLimit = limit
    }
    return !imageLimit.passed
  }
  /**
   * Queue a measure of the current candidate in the shared frame. It reads the
   * candidate and `onMeasure` when a trigger calls it, and the read and the commit
   * below keep those values.
   */
  function scheduleMeasure(): void {
    const { entry, item: { id, heightKey } } = props.candidate
    const onMeasure = props.onMeasure
    const seq = entry.message.seq?.toString() ?? null
    props.frame.schedule(taskKey, {
      read: () => {
        const started = monotonicNow()
        const measurable = readiness.measurable()
        const height = measurable
          ? rowLayoutMeasurement(rowEl)
          : undefined
        const imagesPending = hasPendingImages()
        // An image that still loads can change the height of the row when it arrives, so
        // the copy reports nothing until each image loaded or failed, or the limit passed.
        const held = waitsForImages(imagesPending)
        emitDevEvent('leapmux:chat-premeasure', () => ({
          phase: 'read',
          id,
          seq,
          height: height ?? null,
          heightKey: heightKey ?? null,
          connected: rowEl?.isConnected ?? false,
          settled: !imagesPending,
          measurable,
        }))
        if (height === undefined || held)
          return undefined
        return { height, measureDurationMs: monotonicNow() - started }
      },
      apply: ({ height, measureDurationMs }) => {
        onMeasure(id, height, heightKey, measureDurationMs)
      },
    })
  }
  onCleanup(clearImageLimit)
  createEffect(() => scheduleMeasure())
  // Measure again when the copy becomes measurable: its last pending body is
  // ready, and no body replaces its content. The new content usually resizes the
  // copy, but nothing guarantees it, so the resize observer alone could leave the
  // row unmeasured, and so out of the chat list, forever.
  // eslint-disable-next-line solid/reactivity -- onMeasurable runs the callback inside its own effect
  readiness.onMeasurable(scheduleMeasure)
  createEffect(() => {
    const root = rowEl
    if (!root || typeof ResizeObserver === 'undefined')
      return
    const observer = new ResizeObserver(() => scheduleMeasure())
    observer.observe(root)
    onCleanup(() => observer.disconnect())
  })
  // Measure again when an image settles: the read before it gave no result. An image
  // event does not bubble, so the row listens in the capture phase. The listener on the
  // row also covers an image that mounts after the row: a Markdown body's HTML lands
  // from the worker later. An image that settles at the height that its box reserved
  // resizes nothing, so the resize observer alone would never measure the copy.
  onMount(() => {
    const root = rowEl
    if (!root)
      return
    const onImageSettled = (event: Event): void => {
      if (event.target instanceof Element && event.target.localName === 'img')
        scheduleMeasure()
    }
    root.addEventListener('load', onImageSettled, true)
    root.addEventListener('error', onImageSettled, true)
    onCleanup(() => {
      root.removeEventListener('load', onImageSettled, true)
      root.removeEventListener('error', onImageSettled, true)
    })
  })
  onCleanup(() => props.frame.cancel(taskKey))

  // Row chrome is part of the row's HEIGHT: a band's borders and vertical
  // padding add to it directly, and any bleed widens the row, which is what the
  // text inside wraps against. The measured row must therefore wear exactly what
  // the live row wears (ChatView's virtual row), or it commits a wrong height.
  const chrome = createMemo(() => messageRowChrome(
    styles.premeasureRow,
    props.candidate.entry.category.kind,
    props.candidate.entry.message.source,
  ))

  const lineCount = () => props.candidate.entry.parsedSpanLines.length

  return (
    <div ref={rowEl} class={chrome().class} data-band={chrome().band}>
      {/* Mirrors the live row's content column, ROW_BLEED_LEFT_VAR included: a
          bleeding child that measured at the wrong width would wrap its text
          differently here than in the list. This row RESERVES the rails' width
          rather than rendering them, so it takes the margin too. */}
      <div style={reservedRowContentColumnStyle(lineCount())}>
        {props.renderBubble(props.candidate.entry, readiness)}
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
      // An e2e hook, like `data-chat-scroll-container` and `data-band`: the class
      // is a hashed vanilla-extract name, so a spec has no other stable way to
      // ask "is this element the hidden COPY of a row rather than the row?".
      // A chat measurement that lands on a copy is a real defect (the copy sits
      // outside the scroll container and carries no live geometry), and without
      // this marker the failure is indistinguishable from a row that simply
      // remounted -- which is what made
      // https://github.com/leapmux/leapmux/issues/402 a three-way guess.
      data-chat-premeasure-root="true"
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
