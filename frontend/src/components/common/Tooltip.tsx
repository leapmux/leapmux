import type { JSX } from 'solid-js'
import { createEffect, createSignal, createUniqueId, getOwner, onCleanup, onMount, runWithOwner, Show } from 'solid-js'
import { Portal } from 'solid-js/web'
import { srOnly } from '~/styles/shared.css'
import { holdIsOverMenu, touchReleaseOpensMenu } from './contextMenuGesture'
import * as styles from './Tooltip.css'

/** Exported so a test advances its fake timers by the real delay. */
export const SHOW_DELAY_MS = 700
/** Exported so a test advances its fake timers by the real delay. */
export const HIDE_DELAY_MS = 100
/** Extra margin around trigger rect for the pointermove hit-test. */
const HOVER_MARGIN_PX = 4
/** Sub-pixel slack when comparing rects/scroll sizes for clip detection. */
const CLIP_TOLERANCE_PX = 1
const WHITESPACE_RE = /\s+/

/** Dismiss callback of the currently visible tooltip (at most one). */
let activeHide: (() => void) | undefined

/** Hide the visible tooltip, if any. A menu that is about to open calls this. */
export function dismissActiveTooltip(): void {
  activeHide?.()
}

export interface TooltipProps {
  /** Plain-text tooltip. When both `text` and `content` are empty, the tooltip is disabled. */
  text?: string
  /**
   * Rich JSX content rendered inside the tooltip. Takes precedence over
   * `text` for display; pass `text` alongside if you need an aria-label
   * source (or if the plain-text form is preferable in some cases).
   */
  content?: JSX.Element
  /**
   * The id of an element that ALREADY states this reason on screen. When set,
   * the tooltip points `aria-describedby` at that element and renders no
   * offscreen copy of `text`.
   *
   * For the surface that shows the reason twice on purpose: a dialog that
   * greys out a destructive action states why in its body, so a reader who
   * never hovers still learns it, AND wraps the control so a reader who does
   * hover learns it there. Without this the same sentence reaches the
   * accessibility tree twice -- a screen reader announces the reason, then
   * announces it again as the control's description -- and any locator that
   * matches on the TEXT resolves to two elements and fails on strict mode.
   */
  describedBy?: string
  /**
   * When set, applies an aria-label to the target element.
   * Use `true` to reuse `text`, or pass a string for an explicit label.
   */
  ariaLabel?: string | true
  /**
   * Controls when the tooltip appears.
   * - `'always'` (default): show on hover/focus regardless of target visibility.
   * - `'clipped'`: only show when the target's content is truncated (e.g.
   *   `text-overflow: ellipsis`) or its bounding rect extends beyond an
   *   overflow-hidden/scrollable ancestor or the viewport. Useful for
   *   ellipsized labels where the tooltip is redundant when the full text
   *   already fits.
   *
   * Note: clip-detection is visual. Screen-reader users won't get the tooltip
   * text when `'clipped'` suppresses it, so reserve this mode for cases
   * where the tooltip text duplicates the on-screen label.
   */
  showWhen?: 'always' | 'clipped'
  children: JSX.Element
}

type TooltipTarget = Element & {
  addEventListener: Element['addEventListener']
  removeEventListener: Element['removeEventListener']
  getBoundingClientRect: Element['getBoundingClientRect']
  getAttribute: Element['getAttribute']
  setAttribute: Element['setAttribute']
  removeAttribute: Element['removeAttribute']
}

/**
 * Every element type that can carry `disabled`. A `<span>` cannot, so its own
 * disabled state is always the state of the control that ENCLOSES it.
 */
const DISABLEABLE = 'button, input, select, textarea, fieldset, optgroup, option'

/**
 * The control whose disabled state governs `el`: `el` itself when it can be
 * disabled, otherwise the nearest one that encloses it, or null when none does.
 *
 * It matches on element TYPE rather than on `:disabled` deliberately. jsdom
 * resolves the `:disabled` pseudo-class through the element's own window and
 * throws for a node that is not in a document yet -- which is every node before
 * SolidJS inserts the tree it just built.
 */
function disablingHost(el: Element): Element | null {
  return el.closest(DISABLEABLE)
}

/**
 * True when the browser will not dispatch a pointer event to this element
 * ITSELF, which is the question the wrapper's box answers.
 *
 * `:disabled` rather than the `disabled` property, so a control disabled by an
 * enclosing `<fieldset>` counts as well. `aria-disabled` is the other half: it
 * looks disabled and takes pointer events, so a tooltip on it needs no wrapper
 * -- but this component still owes the description below, because a screen
 * reader reads it as unavailable and the reader deserves the reason either way.
 *
 * Deliberately NOT an ancestor test, although a disabled control is inert for
 * its whole subtree. The wrapper this drives sits INSIDE that subtree too, so a
 * box there catches nothing -- it would only add a layout box to every label
 * inside every disabled control, for a hover that still cannot happen. The
 * description below is the route that does work, and it does use the ancestor.
 */
function targetRefusesPointerEvents(el: Element): boolean {
  return el.matches(':disabled')
}

/**
 * True when the element presents itself as unavailable, by any mechanism.
 *
 * The ENCLOSING control counts. A disabled form control is inert for its whole
 * subtree, so a `<span>` inside a disabled `<button>` is as unhoverable as the
 * button -- and `ClippedText` puts its target exactly there, on the label
 * INSIDE a control. Without the ancestor, a clipped value inside a disabled
 * control had no route back at all: no hover, and no description either.
 */
function targetIsDisabled(el: Element): boolean {
  if (el.getAttribute('aria-disabled') === 'true')
    return true
  const host = disablingHost(el)
  return host !== null && host.matches(':disabled')
}

/** True if the element's computed overflow on either axis clips its content. */
function clipsOverflow(el: Element): boolean {
  const cs = getComputedStyle(el)
  return cs.overflowX !== 'visible' || cs.overflowY !== 'visible'
}

/**
 * Detects whether the target is visually clipped — either by its own
 * overflow (truncated text) or by an ancestor / viewport.
 *
 * Auto-detect strategy:
 * 1. If the target itself has non-visible overflow on an axis where its
 *    scroll size exceeds its client size, it truncates its own content.
 * 2. Otherwise, walk parent elements; for each one whose computed overflow
 *    isn't `visible`, check whether the target's rect extends past it
 *    HORIZONTALLY. The vertical axis is deliberately not tested: a row
 *    scrolled part-way out of a list fits its own box, and treating that as
 *    clipped shows a tooltip that only repeats the visible label.
 * 3. Finally, treat the viewport edges as a clipping boundary.
 *
 * Limitation: this walks `parentElement`, not containing-block ancestors,
 * so it can over-report for `position: fixed` targets nested inside an
 * overflow-hidden container that doesn't actually clip them.
 */
function isTargetClipped(target: Element): boolean {
  // `<input>` and `<textarea>` always clip overflowing value/text regardless
  // of their computed overflow (which browsers typically report as `visible`).
  const intrinsicallyClips = target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement
  const csTarget = intrinsicallyClips ? null : getComputedStyle(target)
  const clipsSelfX = intrinsicallyClips || csTarget!.overflowX !== 'visible'
  const clipsSelfY = intrinsicallyClips || csTarget!.overflowY !== 'visible'
  if (clipsSelfX && target.scrollWidth - target.clientWidth > CLIP_TOLERANCE_PX)
    return true
  if (clipsSelfY && target.scrollHeight - target.clientHeight > CLIP_TOLERANCE_PX)
    return true

  const rect = target.getBoundingClientRect()
  // Stop at <body>; the viewport check below covers <html> and the visual viewport.
  for (let ancestor = target.parentElement; ancestor && ancestor !== document.body; ancestor = ancestor.parentElement) {
    if (!clipsOverflow(ancestor))
      continue
    // Use the client box (border-inside, scrollbar-excluded) instead of the
    // bounding rect, so a target hidden behind the ancestor's scrollbar
    // still counts as clipped.
    //
    // The HORIZONTAL axis only. A container that deliberately runs past its
    // scroller (`sectionItems` is `width: max-content`) hides text that no
    // ellipsis marks, and this walk is the only detector for it. The vertical
    // axis carries no such case: a row scrolled half out of a list FITS its own
    // box, and reporting it as clipped shows a tooltip that repeats a label the
    // reader can already read.
    const ar = ancestor.getBoundingClientRect()
    const visibleLeft = ar.left + ancestor.clientLeft
    const visibleRight = visibleLeft + ancestor.clientWidth
    if (
      rect.left < visibleLeft - CLIP_TOLERANCE_PX
      || rect.right > visibleRight + CLIP_TOLERANCE_PX
    ) {
      return true
    }
  }

  // documentElement.clientWidth/Height excludes the page scrollbar, unlike
  // window.innerWidth/Height — the latter would over-include the scrollbar
  // and miss targets clipped by it at the viewport edge. Fall back to
  // innerWidth/Height when documentElement reports 0 (e.g. jsdom).
  const viewportWidth = document.documentElement.clientWidth || window.innerWidth
  const viewportHeight = document.documentElement.clientHeight || window.innerHeight
  return (
    rect.left < -CLIP_TOLERANCE_PX
    || rect.top < -CLIP_TOLERANCE_PX
    || rect.right > viewportWidth + CLIP_TOLERANCE_PX
    || rect.bottom > viewportHeight + CLIP_TOLERANCE_PX
  )
}

/**
 * Portal-based tooltip that escapes overflow:hidden containers.
 *
 * Oat UI's built-in tooltips (`[data-tooltip]`) use CSS-only `::before`/`::after`
 * pseudo-elements with `position: absolute`. Because pseudo-elements cannot escape
 * a containing block's overflow clipping, tooltips inside any ancestor with
 * `overflow: hidden` (sidebars, tiles, tab bars, etc.) are clipped or invisible.
 *
 * This component solves the problem by rendering tooltip content into document.body
 * via a SolidJS Portal and positioning it with getBoundingClientRect().
 *
 * ## A DISABLED child
 *
 * It works on one, and that is the whole reason `title` is banned on a DOM
 * element (see `no-restricted-syntax` in `eslint.config.ts`). A `title` long
 * enough to state a reason BECOMES the control's accessible name, so a screen
 * reader announces the remedy where the label belongs and every by-name lookup
 * stops matching. Two mechanics carry the disabled case:
 *
 *   - The wrapper takes a real box, and the hover listeners sit on the wrapper
 *     AND on the target at the same time. A disabled control dispatches no
 *     pointer event of its own, and the wrapper is `display: contents`
 *     otherwise, which puts it outside the box tree and therefore outside the
 *     hit test. The box is reactive, because `disabled` moves while the tooltip
 *     is mounted -- a button is disabled while a request is in flight.
 *   - An offscreen description sits beside the wrapper and stays in
 *     `aria-describedby` for as long as the control is disabled. Nothing else
 *     can reach a screen-reader user there: a disabled control takes no focus,
 *     so `focusin` never fires and the tooltip can only ever open under a
 *     pointer. A caller that ALREADY states the reason on screen passes
 *     `describedBy` instead, and then this renders no copy of its own -- see
 *     that prop for why a second copy is worse than none.
 *
 * TWO predicates, not one, and the difference is which mechanism the control
 * uses. `:disabled` refuses pointer events, so it needs the box. `aria-disabled`
 * only LOOKS unavailable and still dispatches its own events, so it needs the
 * description and nothing else. An empty tooltip needs neither: with no text
 * and no content there is nothing a hover could open, so the wrapper stays
 * boxless and this component installs no observer.
 *
 * The description is PLAIN TEXT, so it comes from `text`. A `content`-only
 * tooltip on a disabled control has no description to give; pass `text`
 * alongside it.
 */
export function Tooltip(props: TooltipProps) {
  // Capture the reactive owner at setup. Tooltip's show/hide handlers run
  // from DOM event listeners (mouseenter, focusin, click), which are
  // outside any Solid reactive scope. When `show()` reads `props.content`,
  // the lazy JSX getter calls createComponent and the new computation has
  // no parent — it leaks. Re-entering this owner inside the event handlers
  // ensures any computations created during show/hide get parented to
  // the Tooltip and disposed when it unmounts.
  const tooltipOwner = getOwner()

  let triggerWrapperEl: HTMLSpanElement | undefined
  let tooltipEl: HTMLDivElement | undefined
  const tooltipId = createUniqueId()
  const ownDescriptionId = `tooltip-desc-${tooltipId}`
  /** The element `aria-describedby` points at: the caller's, or our own. */
  const descriptionId = () => props.describedBy ?? ownDescriptionId
  /** Whether this component renders the offscreen copy itself. */
  const ownsDescription = () => !props.describedBy
  const [visible, setVisible] = createSignal(false)
  /**
   * The target presents itself as unavailable, by EITHER mechanism.
   *
   * It drives the offscreen description alone. A screen reader announces an
   * `aria-disabled` control as unavailable exactly as it announces a
   * `:disabled` one, so this component owes the reader the reason either way.
   */
  const [describedAsDisabled, setDescribedAsDisabled] = createSignal(false)
  /**
   * The target refuses pointer events, so the wrapper must take a box.
   *
   * The NARROWER predicate. An `aria-disabled` control still dispatches its own
   * pointer events, so it needs neither a box around it nor the listeners moved
   * -- and giving it one put an inert inline-flex box into whatever row it sits
   * in.
   */
  const [targetRefusesPointer, setTargetRefusesPointer] = createSignal(false)
  const [pos, setPos] = createSignal({ top: 0, left: 0 })
  const [targetEl, setTargetEl] = createSignal<TooltipTarget | undefined>()
  let showTimer: ReturnType<typeof setTimeout> | undefined
  let hideTimer: ReturnType<typeof setTimeout> | undefined
  let warnedInvalidChild = false

  /** Whether this tooltip has anything to show. */
  const hasTooltipContent = () => Boolean(props.text || props.content)

  /**
   * Whether the wrapper takes a real box instead of `display: contents`.
   *
   * BOTH facts. A control that refuses pointer events needs the box so the
   * hover can be caught somewhere, and a tooltip with nothing to show has
   * nothing to catch a hover FOR -- so an empty tooltip on a disabled control
   * put an inert box into a dialog footer's flex row at rest and again for the
   * whole submit, for a tooltip that can never open.
   */
  const wrapperTakesBox = () => targetRefusesPointer() && hasTooltipContent()

  const resolveTargetEl = (): TooltipTarget | undefined => {
    const node = triggerWrapperEl?.firstElementChild
    if (!(node instanceof Element) || triggerWrapperEl?.childElementCount !== 1) {
      if (import.meta.env.DEV && !warnedInvalidChild) {
        warnedInvalidChild = true
        console.warn('Tooltip requires exactly one direct DOM element child.')
      }
      return undefined
    }
    return node as TooltipTarget
  }

  const clearTimers = () => {
    clearTimeout(showTimer)
    clearTimeout(hideTimer)
    showTimer = undefined
    hideTimer = undefined
  }

  /**
   * Follow the target's disabled state for as long as this tooltip lives.
   *
   * An observer rather than a one-time read at mount: `disabled` is reactive at
   * nearly every call site -- a button is disabled while its request is in
   * flight, and Preferences disables Add passkey the moment the hub's answer
   * lands. A read at mount would leave the wrapper boxless for a control that
   * became disabled a tick later, and the tooltip would then never open.
   *
   * The filter is what makes it cheap: an attribute observer with an explicit
   * `attributeFilter` costs nothing while those attributes do not change.
   */
  createEffect(() => {
    // eslint-disable-next-line solid/reactivity -- the effect re-runs on targetEl and its cleanup detaches the old target
    const target = targetEl()
    // NOTHING TO SHOW, so nothing to observe. This is per INSTANCE, and the
    // instance count is not limited by the call sites: `ClippedText`,
    // `RelativeTime` and `IconButton` each render a Tooltip unconditionally, so
    // a busy chat screen holds hundreds and a virtualized list allocates and
    // disconnects one for each recycled row on the scroll path.
    if (!target || !hasTooltipContent()) {
      setDescribedAsDisabled(false)
      setTargetRefusesPointer(false)
      return
    }
    const sync = () => {
      setDescribedAsDisabled(targetIsDisabled(target))
      setTargetRefusesPointer(targetRefusesPointerEvents(target))
    }
    sync()
    const observer = new MutationObserver(sync)
    observer.observe(target, { attributes: true, attributeFilter: ['disabled', 'aria-disabled'] })
    // The ENCLOSING control too, when the target is not one itself: a label
    // inside a button is disabled by that button, so watching only the label
    // would leave the description missing for as long as the control stays
    // disabled -- exactly the window the description exists to cover.
    const host = disablingHost(target)
    if (host && host !== target)
      observer.observe(host, { attributes: true, attributeFilter: ['disabled', 'aria-disabled'] })
    onCleanup(() => observer.disconnect())
  })

  const getTriggerRect = () => targetEl()?.getBoundingClientRect()

  /** Dismiss this tooltip immediately. */
  const dismiss = () => {
    clearTimers()
    if (activeHide === dismiss)
      activeHide = undefined
    // eslint-disable-next-line ts/no-use-before-define -- mutual recursion between dismiss and onPointerMove
    document.removeEventListener('pointermove', onPointerMove)
    setVisible(false)
  }

  /**
   * Global pointermove handler active while the tooltip is visible.
   * Hides the tooltip when the pointer leaves the trigger bounds,
   * working around unreliable mouseleave on display:contents elements.
   */
  const onPointerMove = (e: PointerEvent) => {
    const rect = getTriggerRect()
    if (!rect) {
      dismiss()
      return
    }
    const { clientX: x, clientY: y } = e
    if (
      x < rect.left - HOVER_MARGIN_PX
      || x > rect.right + HOVER_MARGIN_PX
      || y < rect.top - HOVER_MARGIN_PX
      || y > rect.bottom + HOVER_MARGIN_PX
    ) {
      dismiss()
    }
  }

  /** Show the tooltip now, if it has content and passes the clip test. */
  const present = () => {
    // Recheck at fire time: a hold can start after `show` armed the delay.
    if (holdIsOverMenu() || touchReleaseOpensMenu())
      return
    const target = targetEl()
    const rect = target?.getBoundingClientRect()
    if (!target || !rect)
      return

    if (props.showWhen === 'clipped' && !isTargetClipped(target))
      return

    // Dismiss any other visible tooltip first.
    activeHide?.()
    activeHide = dismiss

    // Position above the trigger, centered horizontally.
    // transform: translate(-50%, -100%) places the tooltip's
    // bottom-center at this point.
    setPos({ top: rect.top - 6, left: rect.left + rect.width / 2 })
    setVisible(true)

    // Start watching pointer position as a fallback for mouseleave.
    document.addEventListener('pointermove', onPointerMove)

    // Clamp to viewport after the tooltip renders.
    requestAnimationFrame(() => {
      if (!tooltipEl)
        return
      const tr = tooltipEl.getBoundingClientRect()
      let { top, left } = pos()
      // Clamp horizontally
      const halfW = tr.width / 2
      if (left - halfW < 4)
        left = halfW + 4
      else if (left + halfW > window.innerWidth - 4)
        left = window.innerWidth - 4 - halfW
      // If tooltip would go above viewport, flip below the trigger
      if (tr.top < 4)
        top = rect.bottom + 6 + tr.height
      setPos({ top, left })
    })
  }

  const show = () => {
    if (!props.text && !props.content)
      return
    clearTimers()
    showTimer = setTimeout(present, SHOW_DELAY_MS)
  }

  const hide = () => {
    clearTimers()
    hideTimer = setTimeout(dismiss, HIDE_DELAY_MS)
  }

  onMount(() => {
    setTargetEl(resolveTargetEl())
  })

  createEffect(() => {
    const target = targetEl()
    if (!target)
      return
    // BOTH elements, always, and never one chosen from the disabled state.
    //
    // The target carries the ordinary case. The wrapper carries the control the
    // browser refuses to dispatch to, which dispatches no pointer event of its
    // own -- the effect below gives the wrapper a box for exactly that. Every
    // handler is idempotent, so the pair that a bubbling `focusin` or `click`
    // delivers twice costs one cleared timer.
    //
    // Choosing ONE host made the disabled state a dependency of this effect,
    // and a flip while the pointer already rested on the control re-hosted the
    // listeners with no new `mouseenter` behind them: that hover's tooltip
    // never opened. Listening on both removes the re-hosting rather than
    // ordering it.
    //
    // The RECT still comes from the target either way, so the tooltip points at
    // the control rather than at the box around it.
    const listenEls: Element[] = triggerWrapperEl ? [triggerWrapperEl, target] : [target]

    // Re-establish the Tooltip's reactive owner inside the event listener
    // callbacks. Without this, reading `props.content` (a lazy JSX getter)
    // from inside `show()` calls createComponent without a parent owner —
    // the resulting Solid computation never gets disposed and accumulates
    // across every tooltip-show. See the corresponding comment near the
    // owner capture at the top of this component.
    const wrapInOwner = (fn: () => void): (() => void) =>
      tooltipOwner ? () => { runWithOwner(tooltipOwner, fn) } : fn
    // A long-press menu is up, or its release just fired a compatibility
    // mouseenter. Presenting now would stack the tooltip above the menu.
    //
    // A NAMED function, like the two handlers below it. `show` reads
    // `props.text` and `props.content`, and an inline function expression
    // carrying those reads into a call that `solid/reactivity` cannot follow --
    // `wrapInOwner` is a plain local helper, not a tracked scope. The lint rule
    // reports it as reactivity that will be ignored. The reads are correct
    // here, because the wrapped result IS an event handler, so the fix is to
    // let the rule see a reference rather than to silence it.
    const showUnlessMenuOwnsPress = () => {
      if (holdIsOverMenu() || touchReleaseOpensMenu())
        return
      show()
    }
    const handleShow = wrapInOwner(showUnlessMenuOwnsPress)
    const handleHide = wrapInOwner(hide)
    // Clicking (or activating via Space/Enter) means that the user takes an
    // action — dismiss immediately so the tooltip doesn't linger over a
    // menu or state change. `click` fires for both mouse and keyboard.
    const handleDismiss = wrapInOwner(dismiss)

    for (const el of listenEls) {
      el.addEventListener('mouseenter', handleShow)
      el.addEventListener('mouseleave', handleHide)
      el.addEventListener('focusin', handleShow)
      el.addEventListener('focusout', handleHide)
      el.addEventListener('click', handleDismiss)
    }

    onCleanup(() => {
      for (const el of listenEls) {
        el.removeEventListener('mouseenter', handleShow)
        el.removeEventListener('mouseleave', handleHide)
        el.removeEventListener('focusin', handleShow)
        el.removeEventListener('focusout', handleHide)
        el.removeEventListener('click', handleDismiss)
      }
    })
  })

  createEffect(() => {
    // eslint-disable-next-line solid/reactivity -- the effect re-runs on targetEl and its cleanup detaches the old target
    const target = targetEl()
    if (!target)
      return

    const originalDescribedBy = target.getAttribute('aria-describedby')
    const baseIds = (originalDescribedBy ?? '')
      .split(WHITESPACE_RE)
      .filter(Boolean)
      .filter(id => id !== `tooltip-${tooltipId}`)

    createEffect(() => {
      const nextIds = [...baseIds]
      // The offscreen description, for as long as the control is disabled. It
      // is the only route to a screen-reader user there, so it does NOT wait
      // for the tooltip to open -- nothing can open it without a pointer.
      if (describedAsDisabled() && props.text)
        nextIds.push(descriptionId())
      if (visible() && (props.text || props.content))
        nextIds.push(`tooltip-${tooltipId}`)
      if (nextIds.length > 0)
        target.setAttribute('aria-describedby', nextIds.join(' '))
      else
        target.removeAttribute('aria-describedby')
    })

    onCleanup(() => {
      if (originalDescribedBy != null)
        target.setAttribute('aria-describedby', originalDescribedBy)
      else
        target.removeAttribute('aria-describedby')
    })
  })

  createEffect(() => {
    // eslint-disable-next-line solid/reactivity -- the effect re-runs on targetEl and its cleanup detaches the old target
    const target = targetEl()
    if (!target)
      return

    const originalAriaLabel = target.getAttribute('aria-label')

    createEffect(() => {
      const nextAriaLabel = props.ariaLabel === true
        ? props.text
        : props.ariaLabel
      if (nextAriaLabel)
        target.setAttribute('aria-label', nextAriaLabel)
      else if (originalAriaLabel != null)
        target.setAttribute('aria-label', originalAriaLabel)
      else
        target.removeAttribute('aria-label')
    })

    onCleanup(() => {
      if (originalAriaLabel != null)
        target.setAttribute('aria-label', originalAriaLabel)
      else
        target.removeAttribute('aria-label')
    })
  })

  onCleanup(dismiss)

  return (
    <>
      <span
        ref={(el) => {
          triggerWrapperEl = el
        }}
        // `display: contents` everywhere it can be, so the wrapper adds no box
        // to any layout that did not ask for one. A child that REFUSES POINTER
        // EVENTS forces the exception, and only while this tooltip has
        // something to show: a boxless element is not in the hit test, so it
        // would never see the pointer that the child itself refuses.
        // `inline-flex` hugs the child and stays one item of whatever row it
        // sits in.
        style={{ display: wrapperTakesBox() ? 'inline-flex' : 'contents' }}
      >
        {props.children}
      </span>
      <Show when={describedAsDisabled() && props.text && ownsDescription()}>
        <span id={ownDescriptionId} class={srOnly}>{props.text}</span>
      </Show>
      <Show when={visible() && (props.text || props.content)}>
        <Portal>
          <div
            id={`tooltip-${tooltipId}`}
            ref={(el) => {
              tooltipEl = el
              // Enter the top layer so the tooltip renders above native
              // popover="auto" elements (e.g. DropdownMenu).
              requestAnimationFrame(() => {
                if (el.isConnected)
                  el.showPopover()
              })
            }}
            popover="manual"
            role="tooltip"
            class={styles.tooltip}
            style={{
              top: `${pos().top}px`,
              left: `${pos().left}px`,
              transform: 'translate(-50%, -100%)',
            }}
          >
            {props.content ?? props.text}
          </div>
        </Portal>
      </Show>
    </>
  )
}
