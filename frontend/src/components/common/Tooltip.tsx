import type { JSX } from 'solid-js'
import { createSignal, createUniqueId, onCleanup, Show } from 'solid-js'
import { Portal } from 'solid-js/web'
import * as styles from './Tooltip.css'

const SHOW_DELAY_MS = 700
const HIDE_DELAY_MS = 100

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

  const show = () => {
    if (!props.text)
      return
    clearTimers()
    showTimer = setTimeout(() => {
      const rect = getTriggerRect()
      if (!rect)
        return
      // Position above the trigger, centered horizontally.
      // transform: translate(-50%, -100%) places the tooltip's
      // bottom-center at this point.
      setPos({ top: rect.top - 6, left: rect.left + rect.width / 2 })
      setVisible(true)

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
    hideTimer = setTimeout(setVisible, HIDE_DELAY_MS, false)
  }

  onCleanup(clearTimers)

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
            ref={tooltipEl}
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
