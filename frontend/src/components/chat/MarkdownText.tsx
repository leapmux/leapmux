import type { JSX } from 'solid-js'
import type { MarkdownRenderContext } from './renderContext'
import type { AnchoredTooltipHandle } from '~/components/common/Tooltip'
import type { LocalMarkdownImageLease } from '~/lib/localMarkdownImages'
import { createEffect, createMemo, createSignal, onCleanup, Show, untrack } from 'solid-js'
import { createAnchoredTooltip } from '~/components/common/Tooltip'
import { cachedInnerHtml } from '~/lib/htmlFragmentCache'
import {
  classifyLocalLinkKind,
  LOCAL_IMAGE_ALT_ATTRIBUTE,
  LOCAL_IMAGE_ATTRIBUTE,
  LOCAL_IMAGE_CLICKABLE_ATTRIBUTE,
  LOCAL_IMAGE_ERROR_CLASS,
  LOCAL_IMAGE_FALLBACK_CLASS,
  LOCAL_LINK_ATTRIBUTE,
  tooltipPathForLocalLink,
} from '~/lib/localLinks'
import {
  acquireLocalMarkdownImage,
  peekLocalMarkdownImage,
  probeLocalMarkdownImageDimensions,
} from '~/lib/localMarkdownImages'
import { prefetchMarkdownHtml } from '~/lib/renderMarkdown'
import { syntaxThemeGeneration } from '~/lib/syntaxThemeStore'
import { markdownContent } from './markdownEditor/markdownContent.css'
import { syncMarkdownLinkIcon } from './markdownLinkIcons'
import { useMarkdownLocalLinks } from './markdownLocalLinks'
import { markdownHtmlForContext, rowCachedMarkdownHtml } from './markdownRendering'
import { useRenderReadiness, useRequestReadiness } from './renderReadiness'
import { SKELETON_LINE_PITCH_PX, SkeletonLines } from './widgets/ChatRowSkeleton'

/** The most skeleton lines that a pending Markdown body shows. */
const MARKDOWN_PENDING_MAX_LINES = 6

const INTERACTIVE_LOCAL_SELECTOR = `[${LOCAL_LINK_ATTRIBUTE}], [${LOCAL_IMAGE_CLICKABLE_ATTRIBUTE}]`

function startElementInRoot(start: EventTarget | null, root: HTMLElement): Element | undefined {
  if (!(start instanceof Node))
    return undefined
  const startEl = start instanceof Element
    ? start
    : start.parentElement
  if (!startEl || !root.contains(startEl))
    return undefined
  return startEl
}

function findRemoteLinkTarget(start: EventTarget | null, root: HTMLElement): HTMLAnchorElement | undefined {
  const startEl = startElementInRoot(start, root)
  if (!startEl)
    return undefined
  const anchor = startEl.closest<HTMLAnchorElement>('a[href]')
  if (!anchor || !root.contains(anchor))
    return undefined
  return anchor
}

function findInteractiveLocalTarget(start: EventTarget | null, root: HTMLElement): HTMLElement | undefined {
  const startEl = startElementInRoot(start, root)
  if (!startEl)
    return undefined
  const interactive = startEl.closest<HTMLElement>(INTERACTIVE_LOCAL_SELECTOR)
  if (!interactive || !root.contains(interactive))
    return undefined
  // An external anchor inside the rendered Markdown body takes precedence over
  // any local-image or local-link span it contains or sits inside.
  if (findRemoteLinkTarget(start, root))
    return undefined
  return interactive
}

function findTooltipTarget(
  start: EventTarget | null,
  root: HTMLElement,
  localActive: boolean,
): HTMLElement | undefined {
  const remote = findRemoteLinkTarget(start, root)
  if (remote)
    return remote
  if (!localActive)
    return undefined
  return findInteractiveLocalTarget(start, root)
}

function tooltipTextForRemoteLink(anchor: HTMLAnchorElement): string | undefined {
  return anchor.href || anchor.getAttribute('href') || undefined
}

function rawHrefForInteractive(el: HTMLElement): string | undefined {
  const linkHref = el.getAttribute(LOCAL_LINK_ATTRIBUTE)
  if (linkHref !== null)
    return linkHref
  const imgSrc = el.getAttribute(LOCAL_IMAGE_ATTRIBUTE)
  if (imgSrc !== null)
    return imgSrc
  return undefined
}

function setInteractiveSpanRole(el: HTMLElement, interactive: boolean, premeasure: boolean): void {
  if (!interactive) {
    el.removeAttribute('role')
    el.removeAttribute('tabindex')
    return
  }
  el.setAttribute('role', 'button')
  if (premeasure)
    el.removeAttribute('tabindex')
  else
    el.setAttribute('tabindex', '0')
}

function clearTargetOnLeave(
  event: PointerEvent | FocusEvent,
  getTarget: () => HTMLElement | undefined,
  setTarget: (next: HTMLElement | undefined) => void,
): void {
  const current = getTarget()
  if (!current)
    return
  if (event.relatedTarget instanceof Node && current.contains(event.relatedTarget))
    return
  setTarget(undefined)
}

function hasSelectionInside(root: HTMLElement): boolean {
  const selection = window.getSelection()
  if (!selection || selection.isCollapsed)
    return false
  const anchorInside = selection.anchorNode !== null && root.contains(selection.anchorNode)
  const focusInside = selection.focusNode !== null && root.contains(selection.focusNode)
  return anchorInside || focusInside
}

function renderLocalImageFallback(span: HTMLElement, errorReason?: string): void {
  const alt = span.getAttribute(LOCAL_IMAGE_ALT_ATTRIBUTE) ?? ''
  const rawSrc = span.getAttribute(LOCAL_IMAGE_ATTRIBUTE) ?? ''
  const label = alt.trim() || rawSrc

  const fallbackSpan = document.createElement('span')
  fallbackSpan.className = LOCAL_IMAGE_FALLBACK_CLASS
  fallbackSpan.textContent = label

  if (errorReason) {
    const errorSpan = document.createElement('span')
    errorSpan.className = LOCAL_IMAGE_ERROR_CLASS
    errorSpan.textContent = ` — ${errorReason}`
    span.replaceChildren(fallbackSpan, errorSpan)
  }
  else {
    span.replaceChildren(fallbackSpan)
  }
}

function renderLocalImageElement(
  span: HTMLElement,
  url: string,
  alt: string,
  width: number | undefined,
  height: number | undefined,
): void {
  const img = document.createElement('img')
  img.src = url
  img.alt = alt
  img.draggable = false
  if (width !== undefined)
    img.setAttribute('width', String(width))
  if (height !== undefined)
    img.setAttribute('height', String(height))
  span.replaceChildren(img)
}

function renderPremeasureImageBox(span: HTMLElement, width: number, height: number): void {
  const box = document.createElement('span')
  box.setAttribute('data-local-image-premeasure-box', '')
  box.style.display = 'block'
  box.style.width = `${width}px`
  box.style.maxWidth = '100%'
  box.style.maxHeight = '480px'
  box.style.aspectRatio = `${width} / ${height}`
  span.replaceChildren(box)
}

interface ActiveImageEntry {
  controller: AbortController
  spans: HTMLElement[]
  lease?: LocalMarkdownImageLease | undefined
  dims?: { width: number, height: number } | null | undefined
  endReadiness?: (() => void) | undefined
  disposed: boolean
}

function applyImageLeaseToSpans(spans: readonly HTMLElement[], lease: LocalMarkdownImageLease): void {
  for (const span of spans) {
    const alt = span.getAttribute(LOCAL_IMAGE_ALT_ATTRIBUTE) ?? ''
    if (lease.result.kind === 'ok')
      renderLocalImageElement(span, lease.result.url, alt, lease.result.width, lease.result.height)
    else
      renderLocalImageFallback(span, lease.result.reason)
  }
}

function applyPremeasureDimsToSpans(spans: readonly HTMLElement[], dims: { width: number, height: number } | null): void {
  for (const span of spans) {
    if (dims)
      renderPremeasureImageBox(span, dims.width, dims.height)
    else
      renderLocalImageFallback(span)
  }
}

function disposeImageEntry(entry: ActiveImageEntry): void {
  if (entry.disposed)
    return
  entry.disposed = true
  entry.controller.abort()
  entry.endReadiness?.()
  entry.endReadiness = undefined
  entry.lease?.release()
  entry.lease = undefined
}

/** The number of lines in `text`, counted up to `max`. Reads no further than the last counted newline. */
function lineCountUpTo(text: string, max: number): number {
  let lines = 1
  let index = text.indexOf('\n')
  while (index !== -1 && lines < max) {
    lines++
    index = text.indexOf('\n', index + 1)
  }
  return lines
}

/**
 * The placeholder of a Markdown body outside the chat while the worker renders it:
 * one shimmer line for each source line, up to {@link MARKDOWN_PENDING_MAX_LINES}.
 */
function MarkdownPendingSkeleton(props: { text: string }): JSX.Element {
  const height = () => lineCountUpTo(props.text, MARKDOWN_PENDING_MAX_LINES) * SKELETON_LINE_PITCH_PX
  return <SkeletonLines height={height()} seed="markdown-pending" testId="markdown-pending" />
}

function LinkTooltipHost(props: { onHandle: (handle: AnchoredTooltipHandle | undefined) => void }): JSX.Element {
  const handle = createAnchoredTooltip()
  untrack(() => props.onHandle(handle))
  onCleanup(() => props.onHandle(undefined))
  return <handle.Popover />
}

/**
 * Render a Markdown body through the shared remark pipeline. The HTML is produced
 * via remark + sanitizer, never arbitrary user input. It is applied through the
 * parsed-fragment cache (~/lib/htmlFragmentCache) rather than an `innerHTML`
 * binding, so a re-mounting row clones the already-parsed template instead of
 * making the browser re-parse the same markup.
 *
 * The HTML comes from a worker, so the body can mount before it exists. Until then:
 * - Inside a chat row, it reports itself pending to the row's readiness scope
 *   (`context.readiness`) and draws nothing. The row stays hidden behind its own
 *   skeleton, with its height uncommitted, until every body is ready.
 * - Elsewhere, it shows a skeleton of its own.
 */
export function MarkdownText(props: {
  text: string
  context?: MarkdownRenderContext | undefined
  /** The class of the body element. Defaults to `markdownContent`. */
  class?: string
  /** Mark the body as returned tool output; see CollapsibleContentProps.outputPreview. */
  outputPreview?: boolean
}): JSX.Element {
  // The HTML this body shows, and the text it renders. markdownHtmlForContext keeps
  // it while the reader selects or scrolls, and while a theme change renders the SAME
  // text again, so the body keeps its old colours instead of turning into a skeleton.
  let shown: { text: string, html: string } | undefined
  // A memo of its own, so that the lookup, which hashes the whole text, does not run
  // again at each worker completion that re-runs the memo below. The memo does not see
  // a later write into the row cache, and it does not need to: every write stores HTML
  // that the shared cache held first, and the memo below reads that cache next.
  const rowCached = createMemo(() => rowCachedMarkdownHtml(props.text, props.context))
  const html = createMemo(() => {
    const text = props.text
    const next = markdownHtmlForContext(text, props.context, {
      displayed: shown?.text === text
        ? shown.html
        : undefined,
      rowCached: rowCached(),
    })
    if (next !== undefined)
      shown = { text, html: next }
    return next
  })
  const localLinks = useMarkdownLocalLinks()
  const localLinksActive = () => Boolean(localLinks?.workerId)
  const isPremeasure = () => props.context?.premeasureMode === true
  const isInteractive = () => localLinksActive() && !isPremeasure()
  const hasLocalMarkup = createMemo(() => {
    const h = html()
    return Boolean(h && (h.includes(LOCAL_LINK_ATTRIBUTE) || h.includes(LOCAL_IMAGE_ATTRIBUTE)))
  })
  const hasRemoteLinks = createMemo(() => Boolean(html()?.includes('<a ')))
  const hasTooltipTargets = () => !isPremeasure() && ((localLinksActive() && hasLocalMarkup()) || hasRemoteLinks())

  const htmlPending = () => html() === undefined
  const drawsOwnSkeleton = useRenderReadiness(() => props.context, htmlPending)
  const imageReadiness = useRequestReadiness(() => props.context)

  const [bodyEl, setBodyEl] = createSignal<HTMLDivElement | undefined>(undefined)
  let tooltip: AnchoredTooltipHandle | undefined
  let hoveredTarget: HTMLElement | undefined
  let focusedTarget: HTMLElement | undefined
  let shownTarget: HTMLElement | undefined

  const tooltipTextFor = (target: HTMLElement): string | undefined => {
    if (target instanceof HTMLAnchorElement)
      return tooltipTextForRemoteLink(target)
    if (!localLinks || !localLinks.workerId)
      return undefined
    const rawHref = rawHrefForInteractive(target)
    if (!rawHref)
      return undefined
    return tooltipPathForLocalLink(rawHref, {
      baseDir: localLinks.baseDir,
      workingDir: localLinks.workingDir,
      homeDir: localLinks.homeDir,
      flavor: localLinks.flavor,
    })
  }

  const syncTooltip = () => {
    const next = hoveredTarget ?? focusedTarget
    if (next === shownTarget)
      return
    shownTarget = next
    if (next)
      tooltip?.show(next, () => tooltipTextFor(next))
    else
      tooltip?.hide()
  }

  const dismissTooltip = () => {
    hoveredTarget = undefined
    focusedTarget = undefined
    shownTarget = undefined
    tooltip?.dismiss()
  }

  // Effect 1: synchronize interactive link/image attributes, prepended link icons,
  // and tooltip detachment. Separated from async image hydration so `premeasureMode`
  // or tooltip state never tears down active image leases, and short-circuited when
  // the rendered HTML contains no local link, local image, or remote link markup.
  let prevHtmlForAttrs: string | undefined
  createEffect(() => {
    const el = bodyEl()
    const currentHtml = html()
    const active = localLinksActive()
    const flavor = active
      ? localLinks!.flavor
      : 'posix'
    const premeasure = isPremeasure()

    const htmlReplaced = prevHtmlForAttrs !== undefined && currentHtml !== prevHtmlForAttrs
    prevHtmlForAttrs = currentHtml

    if (!el || !currentHtml) {
      if (hoveredTarget || focusedTarget || shownTarget)
        dismissTooltip()
      return
    }

    const hasLinks = currentHtml.includes(LOCAL_LINK_ATTRIBUTE)
    const hasImages = currentHtml.includes(LOCAL_IMAGE_ATTRIBUTE)
    const hasRemote = currentHtml.includes('<a ')
    if (!hasLinks && !hasImages && !hasRemote) {
      if (hoveredTarget || focusedTarget || shownTarget)
        dismissTooltip()
      return
    }

    // Drop any hover or focus anchor detached by an HTML swap or DOM update.
    const hoveredDetached = Boolean(hoveredTarget && !hoveredTarget.isConnected)
    const focusedDetached = Boolean(focusedTarget && !focusedTarget.isConnected)
    if (hoveredDetached || focusedDetached || htmlReplaced) {
      if (hoveredDetached)
        hoveredTarget = undefined
      if (focusedDetached)
        focusedTarget = undefined
      shownTarget = undefined
      tooltip?.dismiss()
    }

    if (hasRemote) {
      for (const anchor of el.querySelectorAll<HTMLAnchorElement>('a[href]'))
        syncMarkdownLinkIcon(anchor, 'remote')
    }

    if (!active) {
      if (hoveredTarget && !(hoveredTarget instanceof HTMLAnchorElement))
        hoveredTarget = undefined
      if (focusedTarget && !(focusedTarget instanceof HTMLAnchorElement))
        focusedTarget = undefined
      if (!(shownTarget instanceof HTMLAnchorElement)) {
        shownTarget = undefined
        tooltip?.dismiss()
        syncTooltip()
      }
      if (hasLinks) {
        for (const link of el.querySelectorAll<HTMLElement>(`[${LOCAL_LINK_ATTRIBUTE}]`)) {
          setInteractiveSpanRole(link, false, premeasure)
          syncMarkdownLinkIcon(link, undefined)
        }
      }
      if (hasImages) {
        for (const span of el.querySelectorAll<HTMLElement>(`[${LOCAL_IMAGE_ATTRIBUTE}]`)) {
          setInteractiveSpanRole(span, false, premeasure)
          span.removeAttribute(LOCAL_IMAGE_CLICKABLE_ATTRIBUTE)
        }
      }
      return
    }

    if (hasLinks) {
      for (const link of el.querySelectorAll<HTMLElement>(`[${LOCAL_LINK_ATTRIBUTE}]`)) {
        setInteractiveSpanRole(link, true, premeasure)
        syncMarkdownLinkIcon(link, classifyLocalLinkKind(link.getAttribute(LOCAL_LINK_ATTRIBUTE) ?? '', flavor))
      }
    }

    if (hasImages) {
      for (const span of el.querySelectorAll<HTMLElement>(`[${LOCAL_IMAGE_ATTRIBUTE}]`)) {
        const insideLink = Boolean(span.closest(`a[href], [${LOCAL_LINK_ATTRIBUTE}]`))
        if (!insideLink) {
          span.setAttribute(LOCAL_IMAGE_CLICKABLE_ATTRIBUTE, '')
          setInteractiveSpanRole(span, true, premeasure)
        }
        else {
          span.removeAttribute(LOCAL_IMAGE_CLICKABLE_ATTRIBUTE)
          setInteractiveSpanRole(span, false, premeasure)
        }
      }
    }
  })

  // Effect 2: hydrate local Markdown images with streaming-safe lease persistence.
  // Retaining entries keyed by `(mode, workerId, baseDir, workingDir, homeDir, flavor, rawSrc)`
  // across `html()` updates keeps in-flight worker ReadFile RPCs alive across streaming
  // chunks instead of aborting and restarting from byte 0 on every chunk.
  const activeImages = new Map<string, ActiveImageEntry>()
  const disposeAllImages = () => {
    for (const entry of activeImages.values())
      disposeImageEntry(entry)
    activeImages.clear()
  }
  onCleanup(disposeAllImages)

  createEffect(() => {
    const el = bodyEl()
    const currentHtml = html()
    const active = localLinksActive()
    const premeasure = isPremeasure()
    const workerId = active
      ? localLinks!.workerId
      : ''
    const baseDir = active
      ? localLinks!.baseDir
      : ''
    const workingDir = active
      ? localLinks!.workingDir
      : ''
    const homeDir = active
      ? localLinks!.homeDir
      : ''
    const flavor = active
      ? localLinks!.flavor
      : 'posix'

    if (!el || !currentHtml || !currentHtml.includes(LOCAL_IMAGE_ATTRIBUTE)) {
      if (activeImages.size > 0)
        disposeAllImages()
      return
    }

    const spans = el.querySelectorAll<HTMLElement>(`[${LOCAL_IMAGE_ATTRIBUTE}]`)
    if (!active) {
      disposeAllImages()
      for (const span of spans) {
        const onlyFallback = span.childElementCount === 1
          && span.firstElementChild?.classList.contains(LOCAL_IMAGE_FALLBACK_CLASS)
        if (!onlyFallback)
          renderLocalImageFallback(span)
      }
      return
    }

    // Group spans by full request key so duplicate references and streaming chunks
    // share one live entry and one in-flight RPC.
    const grouped = new Map<string, { rawSrc: string, spans: HTMLElement[] }>()
    const modeTag = premeasure
      ? 'premeasure'
      : 'full'
    for (const span of spans) {
      const rawSrc = span.getAttribute(LOCAL_IMAGE_ATTRIBUTE) ?? ''
      const key = JSON.stringify([
        modeTag,
        workerId,
        baseDir,
        workingDir,
        homeDir,
        flavor,
        rawSrc,
      ])
      let bucket = grouped.get(key)
      if (!bucket) {
        bucket = { rawSrc, spans: [] }
        grouped.set(key, bucket)
      }
      bucket.spans.push(span)
    }

    // Dispose any prior entries no longer referenced by the current HTML or context.
    for (const [key, entry] of activeImages) {
      if (!grouped.has(key)) {
        disposeImageEntry(entry)
        activeImages.delete(key)
      }
    }

    for (const [key, { rawSrc, spans: keySpans }] of grouped) {
      const existing = activeImages.get(key)
      if (existing) {
        existing.spans = keySpans
        if (existing.lease) {
          applyImageLeaseToSpans(keySpans, existing.lease)
        }
        else if (existing.dims !== undefined) {
          applyPremeasureDimsToSpans(keySpans, existing.dims)
        }
        else {
          for (const span of keySpans) renderLocalImageFallback(span)
        }
        continue
      }

      const req = {
        workerId,
        rawSrc,
        baseDir,
        workingDir,
        homeDir,
        flavor,
      }
      const entry: ActiveImageEntry = {
        controller: new AbortController(),
        spans: keySpans,
        disposed: false,
      }
      activeImages.set(key, entry)

      const hit = peekLocalMarkdownImage(req)
      if (hit) {
        entry.lease = hit
        applyImageLeaseToSpans(keySpans, hit)
        continue
      }

      for (const span of keySpans)
        renderLocalImageFallback(span)

      const endRequest = imageReadiness.begin()
      entry.endReadiness = endRequest

      if (premeasure) {
        void probeLocalMarkdownImageDimensions(req, entry.controller.signal)
          .then((dims) => {
            if (entry.disposed || entry.controller.signal.aborted)
              return
            entry.dims = dims ?? null
            applyPremeasureDimsToSpans(entry.spans, entry.dims)
          })
          .catch(() => {
            // probeLocalMarkdownImageDimensions resolves undefined on error.
          })
          .finally(() => {
            if (entry.endReadiness === endRequest)
              entry.endReadiness = undefined
            endRequest()
          })
      }
      else {
        void acquireLocalMarkdownImage(req, entry.controller.signal)
          .then((lease) => {
            if (entry.disposed || entry.controller.signal.aborted) {
              lease.release()
              return
            }
            if (lease.result.kind === 'ok') {
              entry.lease = lease
              applyImageLeaseToSpans(entry.spans, lease)
            }
            else {
              lease.release()
              applyImageLeaseToSpans(entry.spans, lease)
            }
          })
          .catch(() => {
            // acquireLocalMarkdownImage converts failures into `{ kind: 'error' }`.
          })
          .finally(() => {
            if (entry.endReadiness === endRequest)
              entry.endReadiness = undefined
            endRequest()
          })
      }
    }
  })

  const activateTarget = (target: HTMLElement) => {
    const rawHref = rawHrefForInteractive(target)
    if (!rawHref || !localLinks)
      return
    dismissTooltip()
    localLinks.onActivate(rawHref)
  }

  const handleClick = (event: MouseEvent) => {
    const el = bodyEl()
    if (!el)
      return
    if (!isPremeasure() && hasRemoteLinks() && findRemoteLinkTarget(event.target, el)) {
      dismissTooltip()
      return
    }
    if (!isInteractive() || !hasLocalMarkup())
      return
    if (event.button !== 0 || event.defaultPrevented)
      return
    if (hasSelectionInside(el))
      return
    const target = findInteractiveLocalTarget(event.target, el)
    if (!target)
      return
    activateTarget(target)
  }

  const handleKeyDown = (event: KeyboardEvent) => {
    if (!isInteractive() || !hasLocalMarkup())
      return
    if (event.repeat || event.ctrlKey || event.metaKey || event.altKey)
      return
    if (event.key !== 'Enter' && event.key !== ' ')
      return
    if (event.key === ' ' && event.shiftKey)
      return
    const el = bodyEl()
    if (!el)
      return
    const target = findInteractiveLocalTarget(event.target, el)
    if (!target)
      return
    event.preventDefault()
    event.stopPropagation()
    activateTarget(target)
  }

  const handlePointerOver = (event: PointerEvent) => {
    if (!hasTooltipTargets() || event.pointerType === 'touch')
      return
    const el = bodyEl()
    if (!el)
      return
    const target = findTooltipTarget(event.target, el, localLinksActive())
    if (!target)
      return
    if (event.relatedTarget instanceof Node && target.contains(event.relatedTarget))
      return
    hoveredTarget = target
    syncTooltip()
  }

  const handlePointerOut = (event: PointerEvent) => {
    clearTargetOnLeave(event, () => hoveredTarget, (next) => {
      hoveredTarget = next
      syncTooltip()
    })
  }

  const handleFocusIn = (event: FocusEvent) => {
    if (!hasTooltipTargets())
      return
    const el = bodyEl()
    if (!el)
      return
    const target = findTooltipTarget(event.target, el, localLinksActive())
    if (target) {
      focusedTarget = target
      syncTooltip()
    }
  }

  const handleFocusOut = (event: FocusEvent) => {
    clearTargetOnLeave(event, () => focusedTarget, (next) => {
      focusedTarget = next
      syncTooltip()
    })
  }

  return (
    <Show
      when={!htmlPending()}
      fallback={drawsOwnSkeleton
        ? <MarkdownPendingSkeleton text={props.text} />
        : null}
    >
      <div
        class={props.class ?? markdownContent}
        data-tool-output-preview={props.outputPreview === true
          ? ''
          : undefined}
        data-local-links-active={localLinksActive()
          ? ''
          : undefined}
        ref={(el) => {
          cachedInnerHtml(() => html() ?? '')(el)
          setBodyEl(el)
          onCleanup(() => setBodyEl(prev => (prev === el
            ? undefined
            : prev)))
        }}
        onClick={handleClick}
        on:keydown={handleKeyDown}
        onPointerOver={handlePointerOver}
        onPointerOut={handlePointerOut}
        onFocusIn={handleFocusIn}
        onFocusOut={handleFocusOut}
      />
      <Show when={hasTooltipTargets()}>
        <LinkTooltipHost
          onHandle={(handle) => {
            tooltip = handle
            if (!handle) {
              hoveredTarget = undefined
              focusedTarget = undefined
              shownTarget = undefined
            }
          }}
        />
      </Show>
    </Show>
  )
}

/**
 * Render nothing, but start rendering `text` at low priority. Place it where a
 * collapsed section hides a Markdown body, so that expanding the section finds the
 * HTML already in the cache.
 *
 * Inside the hidden premeasure copy of a row (`premeasureMode: true`) this is a
 * no-op: the reader cannot expand a premeasure copy, and queuing a render for
 * every collapsed tool output on the scroll path would stall visible rows.
 */
export function MarkdownPrefetch(props: {
  text: string
  context?: MarkdownRenderContext | undefined
}): JSX.Element {
  createEffect(() => {
    if (props.context?.premeasureMode === true)
      return
    // Re-queue when the syntax theme changes, matching MarkdownText.
    syntaxThemeGeneration()
    if (props.text)
      prefetchMarkdownHtml(props.text)
  })
  return null
}
