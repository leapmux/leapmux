import type { JSX } from 'solid-js'
import { createEffect, createSignal, createUniqueId, onCleanup, onMount, Show } from 'solid-js'
import { Portal } from 'solid-js/web'
import * as styles from './Tooltip.css'

const SHOW_DELAY_MS = 700
const HIDE_DELAY_MS = 100
/** Extra margin around trigger rect for the pointermove hit-test. */
const HOVER_MARGIN_PX = 4
const WHITESPACE_RE = /\s+/

/** Dismiss callback of the currently visible tooltip (at most one). */
let activeHide: (() => void) | undefined

export interface TooltipProps {
  /** Tooltip text. When empty/undefined, the tooltip is disabled. */
  text: string | undefined
  /**
   * When set, applies an aria-label to the target element.
   * Use `true` to reuse `text`, or pass a string for an explicit label.
   */
  ariaLabel?: string | true
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
 * Portal-based tooltip that escapes overflow:hidden containers.
 *
 * Oat UI's built-in tooltips (`[data-tooltip]`) use CSS-only `::before`/`::after`
 * pseudo-elements with `position: absolute`. Because pseudo-elements cannot escape
 * a containing block's overflow clipping, tooltips inside any ancestor with
 * `overflow: hidden` (sidebars, tiles, tab bars, etc.) are clipped or invisible.
 *
 * This component solves the problem by rendering tooltip content into document.body
 * via a SolidJS Portal and positioning it with getBoundingClientRect().
 */
export function Tooltip(props: TooltipProps) {
  let triggerWrapperEl: HTMLSpanElement | undefined
  let tooltipEl: HTMLDivElement | undefined
  const tooltipId = createUniqueId()
  const [visible, setVisible] = createSignal(false)
  const [pos, setPos] = createSignal({ top: 0, left: 0 })
  const [targetEl, setTargetEl] = createSignal<TooltipTarget | undefined>()
  let showTimer: ReturnType<typeof setTimeout> | undefined
  let hideTimer: ReturnType<typeof setTimeout> | undefined
  let warnedInvalidChild = false

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

  const show = () => {
    if (!props.text)
      return
    clearTimers()
    showTimer = setTimeout(() => {
      const rect = getTriggerRect()
      if (!rect)
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
    }, SHOW_DELAY_MS)
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

    const handleShow = () => show()
    const handleHide = () => hide()
    // Clicking (or activating via Space/Enter) means the user is taking an
    // action — dismiss immediately so the tooltip doesn't linger over a
    // menu or state change. `click` fires for both mouse and keyboard.
    const handleDismiss = () => dismiss()

    target.addEventListener('mouseenter', handleShow)
    target.addEventListener('mouseleave', handleHide)
    target.addEventListener('focusin', handleShow)
    target.addEventListener('focusout', handleHide)
    target.addEventListener('click', handleDismiss)

    onCleanup(() => {
      target.removeEventListener('mouseenter', handleShow)
      target.removeEventListener('mouseleave', handleHide)
      target.removeEventListener('focusin', handleShow)
      target.removeEventListener('focusout', handleHide)
      target.removeEventListener('click', handleDismiss)
    })
  })

  createEffect(() => {
    const target = targetEl()
    if (!target)
      return

    const originalDescribedBy = target.getAttribute('aria-describedby')
    const baseIds = (originalDescribedBy ?? '')
      .split(WHITESPACE_RE)
      .filter(Boolean)
      .filter(id => id !== `tooltip-${tooltipId}`)

    createEffect(() => {
      const nextIds = visible() && props.text
        ? [...baseIds, `tooltip-${tooltipId}`]
        : baseIds
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
        style={{ display: 'contents' }}
      >
        {props.children}
      </span>
      <Show when={visible() && props.text}>
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
            {props.text}
          </div>
        </Portal>
      </Show>
    </>
  )
}
