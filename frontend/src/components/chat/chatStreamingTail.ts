import type { Accessor } from 'solid-js'
import { createComputed, createEffect, createMemo, createSignal, onCleanup } from 'solid-js'
import { createRafCoalescer } from '~/lib/rafCoalesce'
import { renderMarkdown } from '~/lib/renderMarkdown'

// ---------------------------------------------------------------------------
// Streaming-tail lifecycle
//
// Two tightly-coupled jobs the live tail needs, extracted from ChatView so the
// intricate stream->persisted-row handoff has a named, unit-testable home instead
// of a signal cluster + three effects scattered through the ~1200-line component:
//
//  1. RENDER: throttle the streaming markdown to one remark+shiki pass per frame
//     (createRafCoalescer) so a burst of stream chunks doesn't run the pipeline per
//     delta. renderedStreamHtml holds the latest rendered HTML (empty when idle).
//  2. HANDOFF: when streaming ends, the same text is persisted as a real assistant
//     row. That row mounts UNMEASURED, so its virtual slot is an estimate while the
//     just-rendered bubble already knows its true height -- swapping immediately
//     blinks. So we keep the in-flow streaming bubble (or a captured copy of its
//     HTML) covering the replacement row until it has real measured geometry, then
//     let the virtualized row take over. streamReplacementTailId names the covered
//     row; heldStreamReplacement is the captured copy for the gap between "streaming
//     cleared" and "row measured".
//
// The machine reads streaming text/type, the windowed-away flag, and the current tail
// id, and asks the virtualizer whether a row is measured yet. It owns no DOM; ChatView
// renders streamingTailRender() at the tail and consults isCoveredByInFlowTail /
// streamReplacementTailId for the per-row hide-until-measured decision.
// ---------------------------------------------------------------------------

/**
 * A captured copy of the streaming bubble's rendered HTML, held over the persisted
 * replacement row (`tailId`) during the gap between streaming clearing and that row's
 * first measurement. `type` carries the plan-vs-plain rendering mode.
 */
interface HeldStreamReplacement {
  tailId: string
  html: string
  type?: string
}

/** What ChatView paints at the live tail: rendered markdown HTML plus the streaming type ('plan' gets plan chrome). */
export interface StreamingTailRender {
  html: string
  type: string | undefined
}

export interface StreamingTailDeps {
  /** The live streaming text (empty string when not streaming). */
  streamingText: Accessor<string>
  /** The streaming render type ('plan' renders with plan chrome; undefined is plain markdown). */
  streamingType: Accessor<string | undefined>
  /**
   * Whether the window is scrolled away from the live tail. While windowed away the
   * loaded bottom isn't the real bottom, so the tail bubble and any held replacement
   * are dropped (ChatView hides the tail UI entirely).
   */
  hasNewerMessages: Accessor<boolean>
  /** The id of the last visible entry -- the persisted row a finished stream is replaced by (undefined when empty). */
  tailVisibleId: Accessor<string | undefined>
  /** Whether the virtualizer already has a real measured height for a row (vs. an estimate). */
  hasMeasuredHeight: (id: string) => boolean
}

export interface StreamingTail {
  /** The throttled streaming markdown HTML (empty when not streaming). ChatView re-sticks to the bottom on its change. */
  renderedStreamHtml: Accessor<string>
  /**
   * What to paint at the live tail: the live streaming HTML while streaming, else the
   * held replacement copy during the handoff, else undefined (nothing to paint).
   */
  streamingTailRender: Accessor<StreamingTailRender | undefined>
  /** The persisted row a just-finished stream still covers (undefined when none). */
  streamReplacementTailId: Accessor<string | undefined>
  /** Whether the given row is the replacement tail currently covered by the in-flow streaming / held bubble. */
  isCoveredByInFlowTail: (id: string) => boolean
}

/**
 * Build the streaming-tail lifecycle. Call within a reactive owner (ChatView's body): it
 * allocates signals, a rAF coalescer, three effects, and an onCleanup that aborts the
 * coalescer.
 */
export function createStreamingTail(deps: StreamingTailDeps): StreamingTail {
  // Throttle streaming text markdown rendering to animation frames to avoid running the
  // full remark+shiki pipeline on every streaming chunk.
  const [renderedStreamHtml, setRenderedStreamHtml] = createSignal('')
  const [heldStreamReplacement, setHeldStreamReplacement] = createSignal<HeldStreamReplacement | undefined>()
  let latestStreamingText = ''
  let latestStreamingType: string | undefined
  let latestRenderedStreamHtml = ''
  const streamCoalescer = createRafCoalescer<string>(text =>
    setRenderedStreamHtml(renderMarkdown(text, true)),
  )

  createEffect(() => {
    const html = renderedStreamHtml()
    if (deps.streamingText() && html)
      latestRenderedStreamHtml = html
  })

  createEffect(() => {
    const text = deps.streamingText()
    if (!text) {
      streamCoalescer.abort()
      setRenderedStreamHtml('')
      return
    }
    latestStreamingText = text
    latestStreamingType = deps.streamingType()
    latestRenderedStreamHtml = ''
    setHeldStreamReplacement(undefined)
    streamCoalescer.push(text)
  })

  onCleanup(() => streamCoalescer.abort())

  const [streamReplacementTailId, setStreamReplacementTailId] = createSignal<string | undefined>()
  let streamingTailWasVisible = false
  let streamReplacementBaselineTailId: string | undefined
  let awaitingStreamReplacementTail = false
  const markStreamReplacementTail = (tailId: string | undefined): boolean => {
    if (tailId === undefined || tailId === streamReplacementBaselineTailId)
      return false
    awaitingStreamReplacementTail = false
    setStreamReplacementTailId(tailId)
    return true
  }
  // Keep the in-flow streaming bubble covering a persisted replacement row until that
  // row has real measured geometry; otherwise the indicator gap is anchored to an
  // estimated virtual spacer height while the visible bubble overflows it.
  const captureHeldStreamReplacement = (tailId: string | undefined): void => {
    if (tailId === undefined || latestStreamingText === '' || deps.hasMeasuredHeight(tailId))
      return
    setHeldStreamReplacement({
      tailId,
      html: latestRenderedStreamHtml || renderMarkdown(latestStreamingText, true),
      type: latestStreamingType,
    })
  }
  const streamingTailRender = createMemo<StreamingTailRender | undefined>(() => {
    if (deps.streamingText()) {
      return {
        html: renderedStreamHtml(),
        type: deps.streamingType(),
      }
    }
    const held = heldStreamReplacement()
    if (held === undefined)
      return undefined
    return {
      html: held.html,
      type: held.type,
    }
  })
  const isCoveredByInFlowTail = (id: string): boolean =>
    streamReplacementTailId() === id && streamingTailRender() !== undefined
  createEffect(() => {
    const held = heldStreamReplacement()
    if (held === undefined)
      return
    if (streamReplacementTailId() !== held.tailId || deps.hasNewerMessages() || deps.hasMeasuredHeight(held.tailId))
      setHeldStreamReplacement(undefined)
  })
  // A createComputed (not createEffect): the replacement-tail decision must settle in
  // the SAME computation pass that ChatView's hide-until-measured memo reads it, not one
  // flush later. If it lagged, a just-persisted unmeasured tail would flicker an awaiting
  // skeleton (then a lingering crossfade) for the one pass before the cover caught up --
  // undoing the seamless stream->row handoff this machine exists to provide.
  createComputed(() => {
    const streamingTailVisible = !!deps.streamingText() && !deps.hasNewerMessages()
    const tailId = deps.tailVisibleId()
    if (streamingTailVisible) {
      if (!streamingTailWasVisible) {
        streamingTailWasVisible = true
        streamReplacementBaselineTailId = tailId
        awaitingStreamReplacementTail = false
        setStreamReplacementTailId(undefined)
      }
      else {
        markStreamReplacementTail(tailId)
      }
      return
    }

    if (streamingTailWasVisible) {
      streamingTailWasVisible = false
      if (!markStreamReplacementTail(tailId)) {
        // The persisted assistant row can arrive after streaming clears, with
        // hidden lifecycle/meta rows in between. Keep one tail-change exemption
        // pending so that eventual visible row does not blink behind premeasure.
        awaitingStreamReplacementTail = true
        setStreamReplacementTailId(undefined)
      }
      else {
        captureHeldStreamReplacement(tailId)
      }
      return
    }

    if (awaitingStreamReplacementTail && markStreamReplacementTail(tailId)) {
      captureHeldStreamReplacement(tailId)
      return
    }

    if (streamReplacementTailId() !== undefined && streamReplacementTailId() !== tailId)
      setStreamReplacementTailId(undefined)
  })

  return { renderedStreamHtml, streamingTailRender, streamReplacementTailId, isCoveredByInFlowTail }
}
