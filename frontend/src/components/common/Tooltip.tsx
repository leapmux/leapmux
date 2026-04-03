import type { JSX } from 'solid-js'
import { createSignal, createUniqueId, onCleanup, Show } from 'solid-js'
import { Portal } from 'solid-js/web'
import * as styles from './Tooltip.css'

const SHOW_DELAY_MS = 700
const HIDE_DELAY_MS = 100
/** Extra margin around trigger rect for the pointermove hit-test. */
const HOVER_MARGIN_PX = 4

/** Dismiss callback of the currently visible tooltip (at most one). */
let activeHide: (() => void) | undefined

export interface TooltipProps {
  /** Tooltip text. When empty/undefined, the tooltip is disabled. */
  text: string | undefined
  children: JSX.Element
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
  let triggerEl: HTMLElement | undefined
  let tooltipEl: HTMLDivElement | undefined
  const tooltipId = createUniqueId()
  const [visible, setVisible] = createSignal(false)
  const [pos, setPos] = createSignal({ top: 0, left: 0 })
  let showTimer: ReturnType<typeof setTimeout> | undefined
  let hideTimer: ReturnType<typeof setTimeout> | undefined

  const clearTimers = () => {
    clearTimeout(showTimer)
    clearTimeout(hideTimer)
    showTimer = undefined
    hideTimer = undefined
  }

  /** The wrapper uses display:contents, so get the rect from the first child. */
  const getTriggerRect = () =>
    (triggerEl?.firstElementChild ?? triggerEl)?.getBoundingClientRect()

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

  onCleanup(dismiss)

  return (
    <>
      <span
        ref={(el) => { triggerEl = el }}
        style={{ display: 'contents' }}
        onMouseEnter={show}
        onMouseLeave={hide}
        onFocusIn={show}
        onFocusOut={hide}
        aria-describedby={visible() ? `tooltip-${tooltipId}` : undefined}
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
