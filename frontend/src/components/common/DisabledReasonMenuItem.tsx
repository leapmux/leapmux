import type { Component, JSX } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'

export interface DisabledReasonMenuItemProps {
  /**
   * Why the item is unusable, or undefined when it is usable.
   *
   * ONE prop drives both halves: the item is disabled exactly when a reason
   * exists. Two props let a caller disable an item and say nothing, which is
   * the state a user cannot act on.
   */
  'reason': string | undefined
  'onClick': () => void
  /** Extra class for the button, for a row that also has to look dangerous. */
  'class'?: string
  'data-testid'?: string
  'children': JSX.Element
}

/**
 * A menu item that can be disabled, with the reason on hover.
 *
 * Seven sites across four files spelled this shape by hand, and five of them
 * omitted `type="button"` -- which defaults to `submit`, so inside a form the
 * item also submitted it. Owning the button makes that impossible.
 *
 * The reason goes through `<Tooltip>`, never a `title`. `<Tooltip>` works on a
 * disabled control -- it gives its wrapper a real box and listens there,
 * because a disabled element dispatches no pointer event of its own -- and it
 * leaves the item its own accessible name. A `title` long enough to state a
 * reason BECOMES that name instead, so a screen reader announces a sentence of
 * remedy where "New agent..." belongs, and every `getByRole(..., { name })`
 * lookup stops matching.
 *
 * `reason` is read inside the JSX rather than captured, so a caller may pass a
 * value that changes: the prop compiles to a getter, and the disabled state
 * follows it.
 */
export const DisabledReasonMenuItem: Component<DisabledReasonMenuItemProps> = props => (
  <Tooltip text={props.reason}>
    <button
      type="button"
      role="menuitem"
      class={props.class}
      disabled={Boolean(props.reason)}
      data-testid={props['data-testid']}
      onClick={() => props.onClick()}
    >
      {props.children}
    </button>
  </Tooltip>
)
