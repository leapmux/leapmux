import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { createMemo, createSignal, For, Show } from 'solid-js'
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
 * MIME types we will render inline. SVG is intentionally excluded — SVGs can
 * carry script and we don't sandbox them.
 */
const RENDERABLE_IMAGE_MIME_TYPES = new Set<string>([
  'image/png',
  'image/jpeg',
  'image/gif',
  'image/webp',
  'image/avif',
])

/**
 * Cap on the base64-encoded length of an inline image. Beyond this we
 * fall back to the placeholder rather than constructing a giant data URL.
 * 7 MB base64 ≈ 5 MB raw — comfortably covers screenshot-sized images.
 */
const MAX_INLINE_IMAGE_BASE64_LEN = 7 * 1024 * 1024

/** Why an image is shown as a placeholder rather than inline. */
export type ImageSkipReason = 'no-data' | 'unsupported-mime' | 'too-large' | 'external-url' | 'unknown-shape'

/**
 * Decision-tree for rendering an image source:
 *
 *   - Inline base64 + allowlisted MIME + under size cap → `<img src="data:...">`.
 *   - Already-formed `data:` URL with allowlisted MIME → render as-is.
 *   - http(s) URL → not rendered inline; the placeholder shows the URL as a
 *     link so the user can open it in a new tab.
 *   - Anything else → text placeholder so the user knows an image was
 *     returned but we're not rendering it (unknown MIME, bare base64
 *     without MIME, oversized inline data, etc.).
 */
export function imageRenderInfo(source: ImageResultSource): {
  src?: string
  via?: 'inline'
  reason?: ImageSkipReason
} {
  const url = source.url
  if (url) {
    // Already a complete data: URL — accept iff its MIME is allowlisted.
    const DATA_URL_PREFIX = 'data:'
    if (url.startsWith(DATA_URL_PREFIX)) {
      const comma = url.indexOf(',')
      if (comma < 0)
        return { reason: 'unknown-shape' }
      const meta = url.slice(DATA_URL_PREFIX.length, comma).toLowerCase()
      const semi = meta.indexOf(';')
      const mime = semi < 0 ? meta : meta.slice(0, semi)
      if (!RENDERABLE_IMAGE_MIME_TYPES.has(mime))
        return { reason: 'unsupported-mime' }
      if (url.length - comma - 1 > MAX_INLINE_IMAGE_BASE64_LEN)
        return { reason: 'too-large' }
      return { src: url, via: 'inline' }
    }
    // http(s) URL — show as an opt-in external link via the placeholder.
    if (url.startsWith('http://') || url.startsWith('https://'))
      return { reason: 'external-url' }
    return { reason: 'unknown-shape' }
  }

  const data = source.data
  if (!data)
    return { reason: 'no-data' }

  // Plain base64 — only render when MIME is explicitly provided + allowlisted.
  const mime = (source.mimeType ?? '').toLowerCase()
  if (!RENDERABLE_IMAGE_MIME_TYPES.has(mime))
    return { reason: 'unsupported-mime' }
  if (data.length > MAX_INLINE_IMAGE_BASE64_LEN)
    return { reason: 'too-large' }
  return { src: `data:${mime};base64,${data}`, via: 'inline' }
}

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
  const label = () => {
    const mime = props.source.mimeType
    const suffix = mime ? `: ${mime}` : ''
    if (props.reason === 'too-large')
      return `[image${suffix} — too large to render inline]`
    if (props.reason === 'unsupported-mime')
      return `[image${suffix} — unsupported format]`
    return `[image${suffix}]`
  }
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
