import type { DotCluster } from './chatRailPolicy'
import { createEffect, createMemo, on, onCleanup, onMount, Show } from 'solid-js'
import { MarkType } from '~/generated/proto/leapmux/v1/agent_pb'
import { createRafResizeObserver } from '~/lib/resizeObserver'
import { hasScrollRoom } from './chatScrollGeometry'
import * as styles from './ChatScrollRail.css'
import { MarkdownText } from './messageRenderers'

// ---------------------------------------------------------------------------
// Scroll-rail dot preview presentation
//
// The preview-card surface for a rail jump dot, split from ChatScrollRail so the component
// is left to own geometry + interaction. The card owns its own pointer and wheel traffic (it
// renders INSIDE the rail, so untouched traffic reaches the rail's handlers) and reports when
// the reader holds it; the "which dot is active", "how long a released card stays open", and
// "warm its preview" decisions stay in createDotPreview.
// ---------------------------------------------------------------------------

/** Fallback label for a mark whose content has no previewable text. */
function markLabel(type: number): string {
  return type === MarkType.CONTROL_RESPONSE ? 'Your response' : 'Your message'
}

/** "N messages" wording shared by a cluster dot's aria-label and its card header. */
function clusterCountLabel(count: number): string {
  return `${count} messages`
}

/** Accessible label / card fallback for a dot: a count for a cluster, else the mark type. */
export function dotLabel(cluster: DotCluster): string {
  return cluster.count > 1 ? clusterCountLabel(cluster.count) : markLabel(cluster.type)
}

/**
 * The id a focused dot's `aria-describedby` points at, so the card its focus opened becomes that
 * dot's description. Without it a screen-reader user tabbing the rail hears only "Your message"
 * while a sighted reader gets the preview -- and this rail gave focus its own open channel, so the
 * card is a surface the keyboard reaches by design.
 *
 * ONE constant rather than createUniqueId: the rail renders at most one card at a time (a single
 * <Show>), so a per-instance id would buy nothing and would have to be lifted into the rail to be
 * handed back to the dots. A dot whose card is not open points at nothing, which is valid and
 * resolves to no description.
 */
export const DOT_PREVIEW_CARD_ID = 'chat-scroll-rail-preview-card'

export interface DotPreviewCardProps {
  /** The card's rail-Y, already clamped by the rail so a near-edge card is not clipped. */
  topPx: number
  /**
   * The dot the card describes. Passed WHOLE rather than re-flattened into a mark type and a
   * count, so the card cannot be handed a type and a count that belong to different dots, and so
   * `type` keeps its MarkType enum instead of widening to a bare number at this boundary.
   */
  dot: DotCluster
  /**
   * Reactive read of a mark's preview text: undefined = still resolving, '' = resolved with no
   * previewable text (show the mark-type label), else the snippet. The SAME `(seq) => text` shape
   * the rail takes from its own host, rather than a thunk that closes over one dot.
   */
  previewFor: (seq: bigint) => string | undefined
  /** The reader holds the card: the pointer arrived on it, or pressed inside it. */
  onHoldStart: () => void
  /**
   * Nothing holds the card anymore: the pointer left it, no press is down inside it, and it holds
   * no selection.
   */
  onHoldEnd: () => void
  /**
   * A press inside the card went down (true) or ended (false). While it is down the card owns the
   * pointer outright: the rail must not let a dot that the selection drag crosses re-target the
   * card, because re-rendering the body under a live selection collapses it.
   */
  onPressChange: (down: boolean) => void
  /**
   * The card's rendered height (px), reported on mount and whenever it changes. The rail clamps
   * the card's Y against this, so it must be the real height and not the max-height cap -- and it
   * must keep coming, because the preview arrives after a fetch and the markdown reflows when it
   * lands.
   */
  onHeightChange: (px: number) => void
}

/**
 * The preview card for the active jump dot: an aggregate "N messages" header when the dot is a
 * cluster, then the representative's preview -- rendered markdown (the marked message's content
 * is markdown), a mark-type label when there's no previewable text, or a loading line while the
 * fetch is in flight. Reads `previewFor` reactively so a slow fetch that lands after the card
 * opens still fills it in. The preview string is already length-bounded upstream
 * (truncatePreview), so rendered markdown stays small.
 *
 * The card is a PLACE the reader goes, not only a label: its text is selectable, and it scrolls
 * when the preview is taller than the card. That is what the hold callbacks are for. The card
 * reports "the reader holds me" and "nothing holds me anymore", and createDotPreview turns the
 * second one into a DELAYED close (see POINTER_CLOSE_DELAY_MS), which is what gives the reader
 * time to cross the gutter between the dot and the card.
 *
 * Three separate things hold it, and it reports the end only when all three let go. The HOVER is
 * the obvious one. The PRESS matters as much: selecting to the end of a line commonly drags the
 * pointer past the card's edge, and the pointerleave that fires there would otherwise close the
 * card and destroy the selection with it. The SELECTION outlives both: the release that ends a
 * drag-out selection regularly lands outside the card, so a card that closed on it would take the
 * reader's finished selection with it, a delay later, before they could copy it.
 */
export function DotPreviewCard(props: DotPreviewCardProps) {
  const preview = () => props.previewFor(props.dot.seq)

  let cardEl: HTMLDivElement | undefined

  // The three holds on the card, all of which must let go before it reports a hold end: the
  // pointer is over it, a press inside it is still down, and the reader's SELECTION is still
  // inside it. Plain locals rather than signals -- nothing renders from them.
  let hovered = false
  let pressPointerId: number | undefined
  let pressEnd: ((event: PointerEvent) => void) | undefined
  let selectionEnd: (() => void) | undefined

  const dropPressListeners = () => {
    if (pressEnd === undefined)
      return
    window.removeEventListener('pointerup', pressEnd)
    window.removeEventListener('pointercancel', pressEnd)
    pressEnd = undefined
    pressPointerId = undefined
    props.onPressChange(false)
  }

  const dropSelectionListener = () => {
    if (selectionEnd === undefined)
      return
    document.removeEventListener('selectionchange', selectionEnd)
    selectionEnd = undefined
  }

  // A card torn down mid-press (its dot's message was deleted) must not leave its listeners on the
  // window, and must not leave the rail's dots standing down for a press that can no longer end.
  // dropPressListeners reports the press end for that reason; nothing reports a HOLD end, because
  // the card is already gone and there is nothing left to close.
  onCleanup(() => {
    dropPressListeners()
    dropSelectionListener()
    // The measured height belongs to THIS card. Retract it, or the next card's very first frame is
    // clamped against a height it does not have. A dot-to-dot swap keeps the same element and
    // never reaches here, so its observer reports the new height instead.
    props.onHeightChange(0)
  })

  /**
   * True while a non-empty document selection STARTED inside this card.
   *
   * The anchor is the test, not the whole range. The case this hold exists for is a drag that
   * leaves the card -- selecting to the end of a line runs the pointer out past the right edge --
   * and the browser extends the selection to wherever the pointer went, so its end regularly lands
   * in the transcript underneath. Requiring both ends inside would fail on exactly the drag it is
   * meant to protect. The anchor is where the reader began, so it also keeps the card OUT of a
   * selection that started in the transcript and merely swept across it.
   */
  const holdsSelection = (): boolean => {
    const selection = document.getSelection()
    if (!cardEl || !selection || selection.isCollapsed)
      return false
    return cardEl.contains(selection.anchorNode)
  }

  const releaseIfFree = () => {
    if (hovered || pressEnd !== undefined || selectionEnd !== undefined)
      return
    props.onHoldEnd()
  }

  /**
   * Take the SELECTION hold if the reader left a selection behind in the card.
   *
   * The press hold alone is not enough. Selecting to the end of a line drags the pointer past the
   * card's edge, so the release regularly lands outside, and closing on that release destroys the
   * selection with the text nodes it points at -- before the reader can reach a keyboard and copy
   * it. Hold the card for exactly as long as the selection lives instead of for a fixed delay, and
   * let the `selectionchange` that collapses it (the reader's next click anywhere) release it.
   */
  const holdSelectionIfAny = () => {
    if (selectionEnd !== undefined || !holdsSelection())
      return
    const end = () => {
      if (holdsSelection())
        return
      dropSelectionListener()
      releaseIfFree()
    }
    selectionEnd = end
    document.addEventListener('selectionchange', end)
  }

  const onPointerDown = (event: PointerEvent) => {
    // The card renders inside the rail, so without this a press to select text bubbles to the
    // rail's own pointerdown and starts a thumb drag from the card's Y. preventDefault is
    // deliberately NOT called: that default action IS the text selection the card exists to allow.
    event.stopPropagation()
    // Only the primary button opens a press hold, for the reason beginRailPress states for the
    // rail itself: a context menu can swallow the pointerup of a secondary press, and a hold that
    // never ends would pin the card open (and the whole rail lit) with no way back.
    if (event.button !== 0 || pressEnd !== undefined)
      return
    // The press is held on the WINDOW, not on the card: the pointerup that ends a selection drag
    // regularly lands outside the card, and a listener on the card would never see it. It is
    // keyed to THIS pointer, so a second pointer's release (a pen tap while a mouse selects, on
    // the hybrid devices where the card stays interactive) cannot end a press it never started.
    const end = (ended: PointerEvent) => {
      if (ended.pointerId !== pressPointerId)
        return
      dropPressListeners()
      holdSelectionIfAny()
      releaseIfFree()
    }
    pressPointerId = event.pointerId
    pressEnd = end
    window.addEventListener('pointerup', end)
    window.addEventListener('pointercancel', end)
    props.onPressChange(true)
    // A card that opened under a stationary cursor fires no pointerenter, so a press can be the
    // FIRST evidence that the reader is on the card. Report the hold, or the card closes under a
    // selection drag that started without one.
    hovered = true
    props.onHoldStart()
  }

  /**
   * A wheel over the card. The card renders inside the rail, so the wheel would otherwise reach
   * the rail's forwarder and scroll the TRANSCRIPT out from under the card the reader is reading.
   * Keep the delta here while the card still has room to scroll that way, and let it through when
   * it does not -- a card whose preview fits is not a scroller, and swallowing the wheel there
   * would make it the dead zone that the rail's forwarder exists to remove.
   *
   * A wheel with NO vertical component is let through for the same reason, but the rail's
   * forwarder cannot serve it: that forwarder applies deltaY only, and it calls preventDefault, so
   * a sideways wheel reaching it moves nothing AND loses the browser's own horizontal scroll of
   * whatever lies under the card. Stop it here instead of handing it on to be swallowed.
   */
  const onWheel = (event: WheelEvent & { currentTarget: HTMLDivElement }) => {
    if (event.deltaY === 0 || hasScrollRoom(event.currentTarget, event.deltaY))
      event.stopPropagation()
  }

  // The card element OUTLIVES the dot it describes: <Show> is not keyed, so moving from one dot to
  // the next swaps the props and leaves the same scroller in place. Send it back to the top, or a
  // reader who scrolled deep into one preview opens the next one already scrolled past its header
  // and its first line.
  //
  // Keyed on the seq through a MEMO, not on `props.dot` and not on a bare `() => props.dot.seq`.
  // `on` re-runs whenever its source notifies, whatever the value, and a streaming turn hands over
  // a fresh cluster object for the SAME seq on every persisted row (see chatDotPreview.reanchor) --
  // so either of those would reset the scroll under a reader who is mid-read, several times a
  // second. The memo notifies only when the seq itself changes, which is the one moment the card
  // starts describing a different message.
  const shownSeq = createMemo(() => props.dot.seq)
  createEffect(on(shownSeq, () => {
    if (cardEl)
      cardEl.scrollTop = 0
  }, { defer: true }))

  // Report the card's real height so the rail can centre it on its dot without clipping. An
  // observer rather than a one-shot measure: the preview arrives from a fetch after the card
  // opens, and the markdown reflows the card when it lands. The rAF-batched wrapper is the house
  // one -- a raw ResizeObserver callback that drives a reactive write can resize another observed
  // element inside the same delivery, which is the "undelivered notifications" loop.
  onMount(() => {
    const el = cardEl
    if (!el)
      return
    props.onHeightChange(el.offsetHeight)
    const observer = createRafResizeObserver(() => props.onHeightChange(el.offsetHeight))
    observer?.observe(el)
    onCleanup(() => observer?.disconnect())
  })

  return (
    <div
      ref={cardEl}
      id={DOT_PREVIEW_CARD_ID}
      class={styles.previewCard}
      data-testid="chat-scroll-rail-preview"
      style={{ top: `${props.topPx}px` }}
      onPointerEnter={() => {
        hovered = true
        props.onHoldStart()
      }}
      onPointerLeave={() => {
        hovered = false
        releaseIfFree()
      }}
      onPointerDown={onPointerDown}
      onWheel={onWheel}
    >
      <Show when={props.dot.count > 1}>
        <div class={styles.dotPreviewCount}>{clusterCountLabel(props.dot.count)}</div>
      </Show>
      <Show
        when={preview() !== undefined}
        fallback={<span class={styles.dotPreviewLoading}>Loading preview…</span>}
      >
        <Show when={preview()} fallback={<span>{markLabel(props.dot.type)}</span>}>
          <div class={styles.dotPreviewMarkdown}>
            <MarkdownText text={preview()!} />
          </div>
        </Show>
      </Show>
    </div>
  )
}
