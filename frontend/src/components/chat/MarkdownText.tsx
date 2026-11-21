import type { JSX } from 'solid-js'
import type { MarkdownRenderContext } from './renderContext'
import { createEffect, createMemo, createSignal, onCleanup, Show, untrack } from 'solid-js'
import { AnchoredTooltip } from '~/components/common/Tooltip'
import { cachedInnerHtml } from '~/lib/htmlFragmentCache'
import {
  LOCAL_IMAGE_ALT_ATTRIBUTE,
  LOCAL_IMAGE_ATTRIBUTE,
  LOCAL_IMAGE_CLICKABLE_ATTRIBUTE,
  LOCAL_IMAGE_ERROR_CLASS,
  LOCAL_IMAGE_FALLBACK_CLASS,
  LOCAL_LINK_ATTRIBUTE,
  tooltipPathForLocalLink,
} from '~/lib/localLinks'
import { acquireLocalMarkdownImage, peekLocalMarkdownImage } from '~/lib/localMarkdownImages'
import { prefetchMarkdownHtml } from '~/lib/renderMarkdown'
import { syntaxThemeGeneration } from '~/lib/syntaxThemeStore'
import { markdownContent } from './markdownEditor/markdownContent.css'
import { useMarkdownLocalLinks } from './markdownLocalLinks'
import { markdownHtmlForContext, rowCachedMarkdownHtml } from './markdownRendering'
import { useRenderReadiness, useRequestReadiness } from './renderReadiness'
import { SKELETON_LINE_PITCH_PX, SkeletonLines } from './widgets/ChatRowSkeleton'

/** The most skeleton lines that a pending Markdown body shows. */
const MARKDOWN_PENDING_MAX_LINES = 6

const INTERACTIVE_LOCAL_SELECTOR = `[${LOCAL_LINK_ATTRIBUTE}], [${LOCAL_IMAGE_CLICKABLE_ATTRIBUTE}]`

function findInteractiveLocalTarget(start: EventTarget | null, root: HTMLElement): HTMLElement | undefined {
  if (!(start instanceof Node))
    return undefined
  const startEl = start instanceof Element
    ? start
    : start.parentElement
  if (!startEl || !root.contains(startEl))
    return undefined
  const interactive = startEl.closest<HTMLElement>(INTERACTIVE_LOCAL_SELECTOR)
  if (!interactive || !root.contains(interactive))
    return undefined
  // An external anchor inside the rendered Markdown body takes precedence over
  // any local-image or local-link span it contains or sits inside.
  const closerAnchor = startEl.closest<HTMLElement>('a[href]')
  if (closerAnchor && root.contains(closerAnchor))
    return undefined
  return interactive
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

function hasSelectionInside(root: HTMLElement): boolean {
  const selection = window.getSelection()
  if (!selection || selection.isCollapsed)
    return false
  const anchorInside = selection.anchorNode !== null && root.contains(selection.anchorNode)
  const focusInside = selection.focusNode !== null && root.contains(selection.focusNode)
  return anchorInside || focusInside
}

function hideEnclosingPopover(target: HTMLElement): void {
  const popover = target.closest<HTMLElement>('[popover]')
  if (!popover || typeof popover.hidePopover !== 'function')
    return
  let isOpen = false
  try {
    isOpen = popover.matches(':popover-open')
  }
  catch {
    // jsdom does not support the `:popover-open` pseudo-class selector.
    isOpen = popover.hasAttribute('data-popover-open')
  }
  if (!isOpen)
    return
  try {
    popover.hidePopover()
  }
  catch {
    // Ignore InvalidStateError if the popover already closed.
  }
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

  const htmlPending = () => html() === undefined
  const drawsOwnSkeleton = useRenderReadiness(() => props.context, htmlPending)
  const imageReadiness = useRequestReadiness(() => props.context)

  const [bodyEl, setBodyEl] = createSignal<HTMLDivElement | undefined>(undefined)
  const [hoveredTarget, setHoveredTarget] = createSignal<HTMLElement | undefined>(undefined)
  const [focusedTarget, setFocusedTarget] = createSignal<HTMLElement | undefined>(undefined)
  const [dismissNonce, setDismissNonce] = createSignal(0)

  const dismissTooltip = () => {
    setHoveredTarget(undefined)
    setFocusedTarget(undefined)
    setDismissNonce(n => n + 1)
  }

  const activeTooltipTarget = () => hoveredTarget() ?? focusedTarget()
  const activeTooltipText = () => {
    const target = activeTooltipTarget()
    if (!target || !localLinks || !localLinks.workerId)
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

  let prevHtml: string | undefined
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

    const htmlReplaced = prevHtml !== undefined && currentHtml !== prevHtml
    prevHtml = currentHtml

    if (!el || !currentHtml || !el.isConnected) {
      dismissTooltip()
      return
    }

    if (!active) {
      dismissTooltip()
      for (const link of el.querySelectorAll<HTMLElement>(`[${LOCAL_LINK_ATTRIBUTE}]`)) {
        link.removeAttribute('role')
        link.removeAttribute('tabindex')
      }
      for (const span of el.querySelectorAll<HTMLElement>(`[${LOCAL_IMAGE_ATTRIBUTE}]`)) {
        span.removeAttribute('role')
        span.removeAttribute('tabindex')
        span.removeAttribute(LOCAL_IMAGE_CLICKABLE_ATTRIBUTE)
        const onlyFallback = span.childElementCount === 1
          && span.firstElementChild?.classList.contains(LOCAL_IMAGE_FALLBACK_CLASS)
        if (!onlyFallback)
          renderLocalImageFallback(span)
      }
      return
    }

    // Drop any hover or focus anchor detached by an HTML swap or DOM update.
    const prevHovered = untrack(hoveredTarget)
    const prevFocused = untrack(focusedTarget)
    let targetDetached = false
    if (prevHovered && !prevHovered.isConnected) {
      setHoveredTarget(undefined)
      targetDetached = true
    }
    if (prevFocused && !prevFocused.isConnected) {
      setFocusedTarget(undefined)
      targetDetached = true
    }
    if (targetDetached || htmlReplaced)
      setDismissNonce(n => n + 1)

    for (const link of el.querySelectorAll<HTMLElement>(`[${LOCAL_LINK_ATTRIBUTE}]`)) {
      link.setAttribute('role', 'button')
      if (premeasure)
        link.removeAttribute('tabindex')
      else
        link.setAttribute('tabindex', '0')
    }

    const controller = new AbortController()
    const leases: Array<() => void> = []
    const endReadinessFns: Array<() => void> = []
    let disposed = false

    onCleanup(() => {
      disposed = true
      controller.abort()
      for (const end of endReadinessFns)
        end()
      for (const release of leases)
        release()
    })

    for (const span of el.querySelectorAll<HTMLElement>(`[${LOCAL_IMAGE_ATTRIBUTE}]`)) {
      const rawSrc = span.getAttribute(LOCAL_IMAGE_ATTRIBUTE) ?? ''
      const alt = span.getAttribute(LOCAL_IMAGE_ALT_ATTRIBUTE) ?? ''
      const insideLink = Boolean(span.closest(`a[href], [${LOCAL_LINK_ATTRIBUTE}]`))
      if (!insideLink) {
        span.setAttribute(LOCAL_IMAGE_CLICKABLE_ATTRIBUTE, '')
        span.setAttribute('role', 'button')
        if (premeasure)
          span.removeAttribute('tabindex')
        else
          span.setAttribute('tabindex', '0')
      }
      else {
        span.removeAttribute(LOCAL_IMAGE_CLICKABLE_ATTRIBUTE)
        span.removeAttribute('role')
        span.removeAttribute('tabindex')
      }

      const req = {
        workerId,
        rawSrc,
        baseDir,
        workingDir,
        homeDir,
        flavor,
      }
      const hit = peekLocalMarkdownImage(req)
      if (hit && hit.result.kind === 'ok') {
        leases.push(hit.release)
        renderLocalImageElement(span, hit.result.url, alt, hit.result.width, hit.result.height)
        continue
      }

      // Reset any revoked blob <img> or stale error text from a prior context
      // before starting the worker read.
      renderLocalImageFallback(span)
      const endRequest = imageReadiness.begin()
      endReadinessFns.push(endRequest)
      void acquireLocalMarkdownImage(req, controller.signal)
        .then((lease) => {
          if (disposed || controller.signal.aborted || !span.isConnected) {
            lease.release()
            return
          }
          if (lease.result.kind === 'ok') {
            leases.push(lease.release)
            renderLocalImageElement(span, lease.result.url, alt, lease.result.width, lease.result.height)
          }
          else {
            lease.release()
            renderLocalImageFallback(span, lease.result.reason)
          }
        })
        .catch(() => {
          // acquireLocalMarkdownImage converts failures into `{ kind: 'error' }`.
        })
        .finally(() => {
          endRequest()
        })
    }
  })

  const activateTarget = (target: HTMLElement) => {
    const rawHref = rawHrefForInteractive(target)
    if (!rawHref || !localLinks)
      return
    dismissTooltip()
    hideEnclosingPopover(target)
    localLinks.onActivate(rawHref)
  }

  const handleClick = (event: MouseEvent) => {
    if (!isInteractive())
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
    if (!isInteractive())
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
    if (!isInteractive() || event.pointerType === 'touch')
      return
    const el = bodyEl()
    if (!el)
      return
    const target = findInteractiveLocalTarget(event.target, el)
    if (!target)
      return
    if (event.relatedTarget instanceof Node && target.contains(event.relatedTarget))
      return
    setHoveredTarget(target)
  }

  const handlePointerOut = (event: PointerEvent) => {
    const current = hoveredTarget()
    if (!current)
      return
    if (event.relatedTarget instanceof Node && current.contains(event.relatedTarget))
      return
    setHoveredTarget(undefined)
  }

  const handleFocusIn = (event: FocusEvent) => {
    if (!isInteractive())
      return
    const el = bodyEl()
    if (!el)
      return
    const target = findInteractiveLocalTarget(event.target, el)
    if (target)
      setFocusedTarget(target)
  }

  const handleFocusOut = (event: FocusEvent) => {
    const current = focusedTarget()
    if (!current)
      return
    if (event.relatedTarget instanceof Node && current.contains(event.relatedTarget))
      return
    setFocusedTarget(undefined)
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
      <Show when={isInteractive()}>
        <AnchoredTooltip
          target={activeTooltipTarget()}
          text={activeTooltipText()}
          dismissNonce={dismissNonce()}
        />
      </Show>
    </Show>
  )
}

/**
 * Render nothing, but start rendering `text` at low priority. Place it where a
 * collapsed section hides a Markdown body, so that expanding the section finds the
 * HTML cached: the row then re-measures at once instead of waiting for the worker.
 *
 * It prefetches again after a syntax theme change, which empties the HTML cache.
 * The hidden premeasure copy of a row prefetches nothing: the reader expands only
 * the visible copy, which prefetches the same body when it mounts. The idle
 * warm-up mounts a premeasure copy of each unmeasured row in the transcript, so a
 * prefetch there would render and store every collapsed body, mostly text that the
 * reader never opens.
 */
export function MarkdownPrefetch(props: { text: string, context?: MarkdownRenderContext | undefined }): JSX.Element {
  createEffect(() => {
    syntaxThemeGeneration()
    if (props.context?.premeasureMode !== true)
      prefetchMarkdownHtml(props.text)
  })
  return null
}
