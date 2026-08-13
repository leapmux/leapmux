import { Show } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import { statusDot } from '~/styles/shared.css'

export interface StatusDotProps {
  /** The palette class that colours the dot for this state. */
  class?: string
  /**
   * What the colour MEANS, in words.
   *
   * Required, because the dot carries its state as colour alone. It becomes the
   * element's accessible name, and the hover text when {@link StatusDotProps.tooltip}
   * is set.
   */
  label: string
  /**
   * Also show {@link StatusDotProps.label} on hover.
   *
   * Leave it off for a dot that cannot receive pointer events. A dot inside an
   * `actionSlot` carries `actionSlotResting`, which sets `pointer-events: none`
   * so it does not swallow the click aimed at the menu trigger it shares a cell
   * with -- a tooltip there would never fire.
   */
  tooltip?: boolean
  /** `data-status`, which the tests and the E2E specs select on. */
  status?: string
  testId?: string
}

/**
 * The small round status light that a sidebar row carries at its right end.
 *
 * Pairs the shape with a text alternative, the way `ClippedText` pairs a clip
 * with its tooltip: a state carried by COLOUR alone reaches nobody who cannot
 * see the colour, so `label` is required rather than optional. Each section
 * still supplies its own palette through `class`, because the states differ --
 * a worker is connected or disconnected, a background task is queued, running,
 * succeeded, failed, or stopped.
 *
 * `role="img"` is load-bearing, not decoration. `aria-label` is PROHIBITED on
 * an element with no role, because such an element maps to ARIA's `generic`,
 * and a screen reader may drop the label.
 */
export function StatusDot(props: StatusDotProps) {
  const dot = () => (
    <span
      class={props.class ? `${statusDot} ${props.class}` : statusDot}
      role="img"
      aria-label={props.label}
      data-status={props.status}
      data-testid={props.testId}
    />
  )
  return (
    <Show when={props.tooltip} fallback={dot()}>
      <Tooltip text={props.label} ariaLabel>
        {dot()}
      </Tooltip>
    </Show>
  )
}
