import type { JSX } from 'solid-js'
import type { MarkdownRenderContext } from './renderContext'
import type { LocalMarkdownImageLease } from '~/lib/localMarkdownImages'
import { createEffect, createMemo, createSignal, onCleanup, Show } from 'solid-js'
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
import { createMarkdownLinkTooltip } from './markdownLinkTooltip'
import { useMarkdownLocalLinks } from './markdownLocalLinks'
import { markdownHtmlForContext, rowCachedMarkdownHtml } from './markdownRendering'
import { useRenderReadiness, useRequestReadiness } from './renderReadiness'
import { SKELETON_LINE_PITCH_PX, SkeletonLines } from './widgets/ChatRowSkeleton'

/** The most skeleton lines that a pending Markdown body shows. */
const MARKDOWN_PENDING_MAX_LINES = 6

const INTERACTIVE_LOCAL_SELECTOR = `[${LOCAL_LINK_ATTRIBUTE}], [${LOCAL_IMAGE_CLICKABLE_ATTRIBUTE}]`
const REMOTE_LINK_SELECTOR = 'a[href]'

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

function closestInRoot<T extends Element>(
  startEl: Element,
  root: HTMLElement,
  selector: string,
): T | undefined {
  const matched = startEl.closest<T>(selector)
  if (!matched || !root.contains(matched))
    return undefined
  return matched
}

function findInteractiveLocalTarget(start: EventTarget | null, root: HTMLElement): HTMLElement | undefined {
  const startEl = startElementInRoot(start, root)
  if (!startEl)
    return undefined
  const interactive = closestInRoot<HTMLElement>(startEl, root, INTERACTIVE_LOCAL_SELECTOR)
  if (!interactive)
    return undefined
  // An external anchor inside the rendered Markdown body takes precedence over
  // any local-image or local-link span it contains or sits inside.
  if (closestInRoot<HTMLAnchorElement>(startEl, root, REMOTE_LINK_SELECTOR))
    return undefined
  return interactive
}

function findTooltipTarget(
  start: EventTarget | null,
  root: HTMLElement,
  opts: { hasRemote: boolean, localActive: boolean, hasLocal: boolean },
): HTMLElement | undefined {
  const startEl = startElementInRoot(start, root)
  if (!startEl)
    return undefined
  if (opts.hasRemote) {
    const remote = closestInRoot<HTMLAnchorElement>(startEl, root, REMOTE_LINK_SELECTOR)
    if (remote)
      return remote
  }
  if (!opts.localActive || !opts.hasLocal)
    return undefined
  return closestInRoot<HTMLElement>(startEl, root, INTERACTIVE_LOCAL_SELECTOR)
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
  /** Suppress delegated hover/focus link tooltips (used when an outer `<Tooltip>` owns hover, or inside a tooltip popover). */
  disableLinkTooltips?: boolean
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
  const markupFeatures = createMemo(() => {
    const h = html()
    if (!h)
      return { hasLinks: false, hasImages: false, hasRemote: false }
    return {
      hasLinks: h.includes(LOCAL_LINK_ATTRIBUTE),
      hasImages: h.includes(LOCAL_IMAGE_ATTRIBUTE),
      hasRemote: h.includes('<a '),
    }
  })
  const hasLocalMarkup = () => {
    const features = markupFeatures()
    return features.hasLinks || features.hasImages
  }
  const hasTooltipTargets = () => (localLinksActive() && hasLocalMarkup()) || markupFeatures().hasRemote

  const htmlPending = () => html() === undefined
  const drawsOwnSkeleton = useRenderReadiness(() => props.context, htmlPending)
  const imageReadiness = useRequestReadiness(() => props.context)

  const [bodyEl, setBodyEl] = createSignal<HTMLDivElement | undefined>(undefined)

  const linkTooltip = createMarkdownLinkTooltip({
    bodyEl,
    html,
    enabled: () => props.disableLinkTooltips !== true && !isPremeasure(),
    hasTargets: hasTooltipTargets,
    localActive: localLinksActive,
    resolveTarget: (node, el) => {
      const features = markupFeatures()
      return findTooltipTarget(node, el, {
        hasRemote: features.hasRemote,
        localActive: localLinksActive(),
        hasLocal: features.hasLinks || features.hasImages,
      })
    },
    isLocalTarget: target => !(target instanceof HTMLAnchorElement),
    getTooltipText: (target) => {
      if (target instanceof HTMLAnchorElement)
        return target.href || target.getAttribute('href') || undefined
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
    },
  })

  // Effect 1a: prepend remote link icons once per rendered HTML body, decoupled
  // from local-link context and premeasureMode changes.
  createEffect(() => {
    const el = bodyEl()
    const currentHtml = html()
    if (!el || !currentHtml || !markupFeatures().hasRemote)
      return
    for (const anchor of el.querySelectorAll<HTMLAnchorElement>(REMOTE_LINK_SELECTOR))
      syncMarkdownLinkIcon(anchor, 'remote')
  })

  // Effect 1b: synchronize local link/image interactivity and local link icons.
  // Separated from async image hydration so `premeasureMode` or link-attribute
  // changes never tear down active image leases.
  createEffect(() => {
    const el = bodyEl()
    const currentHtml = html()
    const { hasLinks, hasImages } = markupFeatures()
    if (!el || !currentHtml || (!hasLinks && !hasImages))
      return

    const active = localLinksActive()
    const flavor = active
      ? localLinks!.flavor
      : 'posix'
    const premeasure = isPremeasure()

    if (hasLinks) {
      for (const link of el.querySelectorAll<HTMLElement>(`[${LOCAL_LINK_ATTRIBUTE}]`)) {
        setInteractiveSpanRole(link, active, premeasure)
        syncMarkdownLinkIcon(
          link,
          active
            ? classifyLocalLinkKind(link.getAttribute(LOCAL_LINK_ATTRIBUTE) ?? '', flavor)
            : undefined,
        )
      }
    }

    if (hasImages) {
      for (const span of el.querySelectorAll<HTMLElement>(`[${LOCAL_IMAGE_ATTRIBUTE}]`)) {
        const clickable = active && !span.closest(`${REMOTE_LINK_SELECTOR}, [${LOCAL_LINK_ATTRIBUTE}]`)
        setInteractiveSpanRole(span, clickable, premeasure)
        if (clickable)
          span.setAttribute(LOCAL_IMAGE_CLICKABLE_ATTRIBUTE, '')
        else
          span.removeAttribute(LOCAL_IMAGE_CLICKABLE_ATTRIBUTE)
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

    if (!el || !currentHtml || !markupFeatures().hasImages) {
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
    linkTooltip.dismiss()
    localLinks.onActivate(rawHref)
  }

  const handleClick = (event: MouseEvent) => {
    if (!isInteractive() || !hasLocalMarkup())
      return
    if (event.button !== 0 || event.defaultPrevented)
      return
    const el = bodyEl()
    if (!el || hasSelectionInside(el))
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
        onPointerOver={linkTooltip.onPointerOver}
        onPointerOut={linkTooltip.onPointerOut}
        onFocusIn={linkTooltip.onFocusIn}
        onFocusOut={linkTooltip.onFocusOut}
      />
      <linkTooltip.Popover />
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
