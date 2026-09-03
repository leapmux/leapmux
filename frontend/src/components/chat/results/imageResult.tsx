import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { ImageResultSource, ImageSkipReason } from '~/lib/imageBlocks'
import { createMemo, createSignal, For, Show } from 'solid-js'
import { imageRenderInfo, imageSkipPlaceholder } from '~/lib/imageBlocks'
import { sniffImageDimensionsFromDataUrl } from '~/lib/imageDimensions'
import { TOOL_IMAGE_MAX_HEIGHT_PX, toolImage, toolImageButton, toolImageRow, toolInputSummary } from '../toolStyles.css'

/**
 * The shared renderer for an image a tool returned.
 *
 * Every provider that can put pixels in a tool result -- Claude (a `Read` on an
 * image, a `Bash` whose stdout is a data URI, its MCP bridge), Codex (MCP,
 * `dynamicToolCall`, `imageGeneration`), Pi, and the ACP agents (OpenCode,
 * Kilo, Goose) -- normalizes to `ImageResultSource` and mounts these
 * components. Nothing here knows which provider it is serving.
 */

/**
 * The render policy (`imageRenderInfo`, `ImageSkipReason`, the MIME allowlist
 * and the size cap) lives in `~/lib/imageBlocks`, beside the parser.
 *
 * It moved because this row is not its only consumer. `imageBlockToMarkdown`
 * feeds every quote, scroll-rail preview and Markdown body, and the IMAGE tab's
 * `decodeImageBytes` builds a Blob from the same source -- neither can import a
 * component module, so while the policy lived here both hand-copied the size cap
 * and neither applied the MIME allowlist. One policy, three destinations.
 */

/**
 * Inline style reserving an image's exact final box before it decodes, so
 * the decode is layout-neutral and the row's measured height never changes:
 *   height = min(h, h/w * containerWidth, MAX) is what auto layout yields
 *   after load; `aspect-ratio` + this width reproduces it exactly.
 */
export function imageReservationStyle(dims: { width: number, height: number }): Record<string, string> {
  const widthAtMaxHeight = (TOOL_IMAGE_MAX_HEIGHT_PX * dims.width / dims.height).toFixed(2)
  return {
    'aspect-ratio': `${dims.width} / ${dims.height}`,
    'width': `min(${dims.width}px, 100%, ${widthAtMaxHeight}px)`,
  }
}

export function imageReservationMatchesDecoded(
  dims: { width: number, height: number },
  decoded: { naturalWidth: number, naturalHeight: number },
): boolean {
  return decoded.naturalWidth === dims.width && decoded.naturalHeight === dims.height
}

/**
 * One image: the `<img>` when it is renderable, else the placeholder.
 *
 * When the render context supplies `onOpenImage`, the image is wrapped in a
 * button that opens it in its own tab. Without the callback it renders as a
 * bare `<img>` -- a button that does nothing is worse than no button, and it
 * would take a tab stop.
 */
export function ImageResultView(props: {
  source: ImageResultSource
  /** Index of this image within its message, for the tab reference. */
  index?: number
  /**
   * Display name for the tab this image opens into. Supplied by the renderer
   * that mounted it, which is the layer that already has one: an MCP body
   * knows "Playwright / screenshot", an ACP row knows its `description`, and
   * only a bare Claude tool result has nothing better than its span type --
   * which the bubble fills in when this is absent.
   */
  title?: string
  context?: RenderContext
}): JSX.Element {
  const info = createMemo(() => imageRenderInfo(props.source))
  const dims = createMemo(() => {
    const stated = props.source.dimensions
    if (stated)
      return stated
    const src = info().src
    return src ? sniffImageDimensionsFromDataUrl(src) : null
  })
  // Flips when the decoded image disagrees with the sniffed dimensions
  // (sniffer bug / exotic file): the reservation is dropped and layout falls
  // back to natural sizing, so a wrong sniff can never permanently distort
  // the box — it just degrades to today's measure-on-load behavior.
  const [reservationBroken, setReservationBroken] = createSignal(false)
  const reserved = () => (reservationBroken() ? null : dims())

  const sizeStyle = () => {
    const d = reserved()
    return d ? imageReservationStyle(d) : undefined
  }

  const verifyReservation = (img: HTMLImageElement) => {
    const d = dims()
    if (!d || reservationBroken())
      return
    const { naturalWidth: nw, naturalHeight: nh } = img
    // naturalWidth/naturalHeight are post-EXIF-orientation in modern
    // browsers, matching the sniffer's own orientation handling.
    if (nw > 0 && nh > 0 && !imageReservationMatchesDecoded(d, { naturalWidth: nw, naturalHeight: nh }))
      setReservationBroken(true)
  }

  const open = () => props.context?.onOpenImage?.({
    index: props.index ?? 0,
    filePath: props.source.filePath,
    title: props.title,
  })
  const openable = () => Boolean(props.context?.onOpenImage)

  const image = () => (
    <img
      class={toolImage}
      style={sizeStyle()}
      src={info().src}
      alt={props.source.mimeType ?? 'image'}
      loading={props.context?.premeasureMode ? 'eager' : 'lazy'}
      decoding="async"
      referrerpolicy="no-referrer"
      data-size-reserved={reserved() ? '1' : undefined}
      onLoad={e => verifyReservation(e.currentTarget)}
    />
  )

  return (
    <Show
      when={info().src}
      fallback={<ImageResultPlaceholder source={props.source} reason={info().reason} />}
    >
      <div class={toolImageRow}>
        <Show when={openable()} fallback={image()}>
          <button type="button" class={toolImageButton} onClick={open} aria-label="Open image">
            {image()}
          </button>
        </Show>
      </div>
    </Show>
  )
}

/** Every image a tool result carries, in wire order. */
export function ImageResultList(props: {
  sources: ImageResultSource[]
  /** See {@link ImageResultView}'s own `title`. */
  title?: string
  context?: RenderContext
}): JSX.Element {
  return (
    <For each={props.sources}>
      {(source, index) => (
        <ImageResultView source={source} index={index()} title={props.title} context={props.context} />
      )}
    </For>
  )
}

function ImageResultPlaceholder(props: {
  source: ImageResultSource
  reason?: ImageSkipReason
}): JSX.Element {
  const externalUrl = () => {
    const url = props.source.url ?? ''
    return url.startsWith('http://') || url.startsWith('https://') ? url : ''
  }
  // The same wording the Markdown path emits, from the one helper -- a reader
  // who meets the same refused image in a quote reads the same sentence.
  const label = () => imageSkipPlaceholder(props.reason, props.source.mimeType)
  return (
    <div class={toolImageRow}>
      <Show
        when={externalUrl()}
        fallback={<div class={toolInputSummary}>{label()}</div>}
      >
        {url => (
          <div class={toolInputSummary}>
            {label()}
            {' — '}
            <a
              href={url()}
              target="_blank"
              rel="noopener noreferrer nofollow"
              referrerpolicy="no-referrer"
            >
              open ↗
            </a>
          </div>
        )}
      </Show>
    </div>
  )
}
