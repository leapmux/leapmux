import type { JSX } from 'solid-js'
import type { ImageRenderActions } from '../renderContext'
import type { ImageResultSource, ImageSkipReason } from '~/lib/imageBlocks'
import type { ImageDimensions } from '~/lib/imageDimensions'
import { batch, createEffect, createMemo, createSignal, For, Match, on, onCleanup, Show, Switch, untrack } from 'solid-js'
import { imageRenderInfo, imageSkipPlaceholder } from '~/lib/imageBlocks'
import { sniffImageDimensionsFromDataUrl } from '~/lib/imageDimensions'
import { UNTRUSTED_LINK_ATTRIBUTE } from '~/lib/untrustedLinkClicks'
import { useRequestReadiness } from '../renderReadiness'
import { TOOL_IMAGE_MAX_HEIGHT_PX, toolImage, toolImageBox, toolImageBoxFailed, toolImageButton, toolImageRow, toolInputSummary } from '../toolStyles.css'

interface FileImageDisplay {
  path: string | undefined
  source: ImageResultSource
  loading: boolean
  error: string
  decodeError: boolean
}

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
  /** Loading and opening, assembled where the message and the agent are in scope. */
  actions?: ImageRenderActions
  /**
   * Hold the display geometry: a scroll-critical pass or an active text selection
   * must not replace the shown picture mid-pass. Absent means never hold.
   */
  holdDisplay?: () => boolean
}): JSX.Element {
  const [loaded, setLoaded] = createSignal<ImageResultSource>()
  const [loading, setLoading] = createSignal(false)
  const [loadError, setLoadError] = createSignal('')
  const [decodeError, setDecodeError] = createSignal(false)
  // Whether the reader started the last read with the Retry button.
  const [retrying, setRetrying] = createSignal(false)
  const filePath = () => !props.source.data && !props.source.url
    ? props.source.filePath
    : undefined

  // The size of a file image, from the first bytes of its file. The view asks for it
  // whatever `deferLoad` says, so it can reserve the image box before it reads the
  // image: a hidden premeasure copy then measures the height that the row keeps.
  const [sizeRead, setSizeRead] = createSignal<{ path: string, size: ImageDimensions | null }>()
  /**
   * The size that the header of the file gave: `undefined` while no answer exists (and
   * for an image with no file path), and `null` for a file whose read gave no size or
   * failed.
   *
   * The answer of the view's own ask comes first, and the view keeps it. Before that
   * answer arrives, the size cache answers, with no subscription: a size that the cache
   * holds shows in the first render. So nothing that the resolver learns or drops later
   * changes what a mounted view decided, in particular whether it reads its image.
   */
  const headerSize = (): ImageDimensions | null | undefined => {
    const path = filePath()
    if (!path)
      return undefined
    const read = sizeRead()
    return read?.path === path
      ? read.size
      : untrack(() => props.actions?.cachedFileImageSize(path))
  }
  /**
   * Whether only the image itself gives the height of this view: neither the source nor
   * the header of the file states a size.
   *
   * Every copy of the row then reads the whole image at once, whatever `deferLoad` says,
   * and reports no height while that read runs (see `heightUnknown`). So each copy draws
   * the same content: the image, or the error of its read. A copy that waited for its row
   * to come near the view would draw one line where another copy measured that content,
   * and the row would change its height when the reader reaches it.
   */
  const needsImageForHeight = (): boolean =>
    props.source.dimensions === undefined && headerSize() === null
  /**
   * Whether a read of the file can start at all.
   *
   * `loadFile` and the display model below read the SAME accessor, so they
   * cannot disagree. They did: a premeasure pass and an offscreen row both
   * refuse the read, while the display still reported `loading`. The row then
   * said "Loading image..." for a read nobody started, and Retry stays hidden
   * for as long as `loading` holds -- so the row offered no way out.
   *
   * The answer does not depend on what the resolver holds. The view asks for an image
   * that the resolver holds also: the resolver answers with no request, and the view
   * then keeps the image when the resolver drops it at its limit.
   */
  // A memo, so that its readers run again only when the answer changes. `deferLoad` runs
  // again at each geometry change and at each end of a scroll, and the effect below starts
  // the read again each time it runs.
  const canLoad = createMemo((): boolean => Boolean(filePath())
    && !!props.actions?.loadFileImage
    && (!props.actions.deferLoad() || needsImageForHeight()))
  let generation = 0
  const loadFile = (refresh = false): void => {
    const path = filePath()
    const read = props.actions?.loadFileImage
    if (!canLoad() || !path || !read)
      return
    const current = ++generation
    // One update: a reader of these signals must never see a retry as a first read.
    batch(() => {
      if (refresh) {
        setLoaded(undefined)
        setDecodeError(false)
      }
      setRetrying(refresh)
      setLoading(true)
      setLoadError('')
    })
    void (refresh
      ? read(path, { refresh: true })
      : read(path)).then((source) => {
      if (current === generation)
        setLoaded(source)
    }).catch((error: unknown) => {
      if (current === generation) {
        setLoadError(error instanceof Error
          ? error.message
          : String(error))
      }
    }).finally(() => {
      if (current === generation)
        setLoading(false)
    })
  }
  createEffect(on(
    () => [
      filePath(),
      props.actions?.loadFileImage,
      canLoad(),
    ] as const,
    () => {
      generation++
      setLoaded(undefined)
      setLoadError('')
      setLoading(false)
      setRetrying(false)
      loadFile()
      onCleanup(() => {
        generation++
      })
    },
  ))
  const display = createMemo<FileImageDisplay>((previous) => {
    const path = filePath()
    const paused = !props.actions?.deferLoad() && (props.holdDisplay?.() ?? false)
    // Keep the current geometry while a scroll or a text selection holds the display.
    if (path && paused && previous?.path === path)
      return previous
    const resolved = loaded() ?? (path
      ? props.actions?.cachedFileImage(path)
      : undefined)
    const error = loadError()
    // The resolved copy's description fills an absent one; absent stays absent
    // rather than an explicitly undefined key.
    const description = resolved
      ? props.source.description ?? resolved.description
      : undefined
    return {
      path,
      source: resolved
        ? {
            ...props.source,
            ...resolved,
            ...(description !== undefined
              ? { description }
              : {}),
          }
        : props.source,
      // The view has no image to show, and a read runs or starts in the effect above.
      loading: !resolved && (loading() || (canLoad() && !error)),
      error,
      decodeError: decodeError(),
    }
  })
  const source = () => display().source
  const info = createMemo(() => imageRenderInfo(source()))
  createEffect(on(() => info().src, () => setDecodeError(false)))
  const dims = createMemo(() => {
    const stated = source().dimensions
    if (stated)
      return stated
    const src = info().src
    const sniffed = src
      ? sniffImageDimensionsFromDataUrl(src)
      : null
    // Last, the size from the header of the file: it is all that a view knows before
    // it read the image.
    return sniffed ?? headerSize() ?? null
  })
  // Flips when the decoded image disagrees with the sniffed dimensions
  // (sniffer bug / exotic file): the reservation is dropped and layout falls
  // back to natural sizing, so a wrong sniff can never permanently distort
  // the box — it just degrades to today's measure-on-load behavior.
  const [reservationBroken, setReservationBroken] = createSignal(false)
  createEffect(on(() => info().src, () => setReservationBroken(false)))
  const reserved = () => (reservationBroken()
    ? null
    : dims())

  const sizeStyle = () => {
    const d = reserved()
    return d
      ? imageReservationStyle(d)
      : undefined
  }

  const verifyReservation = (img: HTMLImageElement) => {
    if (img.getAttribute('src') !== info().src)
      return
    const d = dims()
    if (!d || reservationBroken())
      return
    const { naturalWidth: nw, naturalHeight: nh } = img
    // naturalWidth/naturalHeight are post-EXIF-orientation in modern
    // browsers, matching the sniffer's own orientation handling.
    if (nw > 0 && nh > 0 && !imageReservationMatchesDecoded(d, { naturalWidth: nw, naturalHeight: nh }))
      setReservationBroken(true)
  }

  // The size read of a file image. The wait for it is `heightUnknown` below.
  createEffect(on(
    () => [
      filePath(),
      props.actions?.loadFileImageSize,
      props.source.dimensions !== undefined,
    ] as const,
    ([path, readSize, stated]) => {
      // A size that the source states never yields to the header, so the header needs no read.
      if (!path || stated)
        return
      // The view keeps its own copy of a size that the cache holds: the cache can drop the
      // size later (its limit, a clear), and the box of a row that shows must stay.
      const cached = untrack(() => props.actions?.cachedFileImageSize(path))
      if (cached !== undefined)
        setSizeRead({ path, size: cached })
      if (!readSize)
        return
      let current = true
      onCleanup(() => {
        current = false
      })
      const settle = (size: ImageDimensions | undefined): void => {
        if (current)
          setSizeRead({ path, size: size ?? null })
      }
      // The view asks for a size that the cache holds also. The resolver answers such an
      // ask with no request, and counts it: it drops the sizes that no view asked for the
      // longest. The read never rejects by its contract. A rejection still counts as no size.
      void readSize(path).then(settle, () => settle(undefined))
    },
  ))
  /**
   * Whether the view does not know the height of its picture yet:
   *
   * - No source gives a size, and its size read runs: no answer exists for the path, and
   *   a reader exists to give one.
   * - Only the image gives the height (see `needsImageForHeight`), and the view has no
   *   image to show while its first read runs. That holds also when the cache gave the
   *   "no size" answer at once.
   *
   * The second state follows the first one with no gap for a size read that gives no
   * size, because `display().loading` is true from the update that settles the size.
   *
   * A read that the reader started with Retry is no such state. The row shows its status
   * during that read, as it showed the error before: a row that reports a wait is hidden,
   * and the press of a button must not hide the row that holds the button.
   */
  const heightUnknown = createMemo((): boolean => {
    if (!filePath() || dims() !== null)
      return false
    if (headerSize() === undefined)
      return props.actions?.loadFileImageSize !== undefined
    return display().loading && !retrying()
  })
  // The wait that this view reports to the readiness scope of its row copy: no reader
  // measures the copy while the view does not know the height of its picture. One wait
  // covers the size read and the read of the whole image that follows it, so both end at
  // one limit (see useRequestReadiness).
  const requests = useRequestReadiness(() => props.actions)
  createEffect(on(
    () => [
      filePath(),
      heightUnknown(),
    ] as const,
    ([, unknown]) => {
      // Each path has its own wait, with its own limit. The cleanup ends the wait before
      // the next run of this effect, and when the view unmounts.
      if (unknown)
        onCleanup(requests.begin())
    },
  ))

  const open = () => {
    const opener = props.actions?.openImage
    if (opener === undefined)
      return
    const src = source()
    opener({
      index: props.index ?? 0,
      ...(src.filePath !== undefined
        ? { filePath: src.filePath }
        : {}),
      ...(props.title !== undefined
        ? { title: props.title }
        : {}),
    })
  }
  const openable = () => Boolean(props.actions?.openImage)

  // What the image is, for a reader who cannot see it.
  const imageLabel = () => source().description || source().mimeType || 'image'

  const image = () => (
    <img
      class={toolImage}
      style={sizeStyle()}
      src={info().src}
      alt={imageLabel()}
      loading={props.actions?.premeasurePass()
        ? 'eager'
        : 'lazy'}
      decoding="async"
      referrerpolicy="no-referrer"
      data-size-reserved={reserved()
        ? '1'
        : undefined}
      onLoad={e => verifyReservation(e.currentTarget)}
      onError={(event) => {
        if (event.currentTarget.getAttribute('src') === info().src)
          setDecodeError(true)
      }}
    />
  )

  // What the view says in place of an image that does not show: that its read runs, or
  // why it failed, with a way to try again. Nothing while the image only waits for its
  // row to come near the view.
  const failed = () => !display().loading && Boolean(display().error || display().decodeError)
  const hasStatus = () => display().loading || failed()
  const status = () => (
    <>
      <div class={toolInputSummary}>
        {display().loading
          ? 'Loading image…'
          : display().error || 'The image could not be decoded'}
      </div>
      <Show when={filePath() && failed()}>
        <div><button type="button" class="small outline" onClick={() => loadFile(true)}>Retry image</button></div>
      </Show>
    </>
  )
  // The description sits below the box as it sits below the image, so the image replaces
  // the box and the row keeps its height.
  const description = () => (
    <Show when={props.source.description}>
      <div class={toolInputSummary}>{props.source.description}</div>
    </Show>
  )

  return (
    <Switch
      fallback={(
        <ImageResultPlaceholder
          source={source()}
          {...(info().reason !== undefined
            ? { reason: info().reason }
            : {})}
        />
      )}
    >
      <Match when={info().src && !display().decodeError}>
        <div class={toolImageRow}>
          <Show when={openable()} fallback={image()}>
            <button type="button" class={toolImageButton} onClick={open} aria-label="Open image">
              {image()}
            </button>
          </Show>
          {description()}
        </div>
      </Match>
      {/* The size of a file image is known, so the row holds its box in every state:
          while the image waits, while it loads, and when its read failed. The image
          then replaces the box, and the row keeps its height. Only a file image takes
          the box. No read gives an image with no file, so no image can replace its box:
          such an image shows its label, and the link of an external image. */}
      <Match when={filePath() && reserved()}>
        <div class={toolImageRow}>
          {/* A box with no status stands for the image, and states it to a reader who
              cannot see it. A box with a status holds text and a button, which the
              image role would hide from that reader. */}
          <div
            class={toolImageBox}
            classList={{
              [toolImageBoxFailed]: failed(),
            }}
            style={sizeStyle()}
            data-size-reserved="1"
            role={hasStatus()
              ? undefined
              : 'img'}
            aria-label={hasStatus()
              ? undefined
              : imageLabel()}
          >
            <Show when={hasStatus()}>{status()}</Show>
          </div>
          {description()}
        </div>
      </Match>
      <Match when={hasStatus()}>
        <div class={toolImageRow}>{status()}</div>
      </Match>
    </Switch>
  )
}

/** Every image a tool result carries, in wire order. */
export function ImageResultList(props: {
  sources: readonly ImageResultSource[]
  /** Number of images that the message renders before this list. */
  indexOffset?: number
  /** See {@link ImageResultView}'s own `title`. */
  title?: string
  actions?: ImageRenderActions
  holdDisplay?: () => boolean
}): JSX.Element {
  return (
    <For each={props.sources}>
      {(source, index) => (
        <ImageResultView
          source={source}
          index={(props.indexOffset ?? 0) + index()}
          {...(props.title !== undefined
            ? { title: props.title }
            : {})}
          {...(props.actions !== undefined
            ? { actions: props.actions }
            : {})}
          {...(props.holdDisplay !== undefined
            ? { holdDisplay: props.holdDisplay }
            : {})}
        />
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
    return url.startsWith('http://') || url.startsWith('https://')
      ? url
      : ''
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
              {...{ [UNTRUSTED_LINK_ATTRIBUTE]: '' }}
            >
              open ↗
            </a>
          </div>
        )}
      </Show>
    </div>
  )
}
