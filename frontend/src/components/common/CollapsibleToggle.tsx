import type { Component } from 'solid-js'
import { collapsibleToggle } from './CollapsibleToggle.css'

export interface CollapsibleToggleProps {
  /** Whether the block this control opens is open now. */
  'expanded': boolean
  /** Open it, or close it. */
  'onToggle': () => void
  /** What the control says while the block is collapsed. */
  'moreLabel': string
  /** What it says while the block is open. Defaults to "Show less". */
  'lessLabel'?: string
  /**
   * The `id` of the block this control opens.
   *
   * Optional, because a caller that expands a LIST has no single element to
   * point at. `aria-expanded` still applies in that case, so the state is
   * announced either way.
   */
  'controls'?: string
  'data-testid'?: string
}

/**
 * The "Show more" / "Show less" control that opens a clipped block.
 *
 * One component rather than one shared class, because the paint is the smallest
 * part of what the three call sites share. They also share the label pairing,
 * the `type="button"` -- a bare `<button>` inside a form defaults to `submit`,
 * which reloads the page -- and the two ARIA attributes a disclosure owes a
 * screen reader. A shared class left every one of those to be remembered per
 * site, and each site is where it went wrong.
 *
 * `aria-expanded` is the whole point of the extraction. Without it a screen
 * reader announces "Show more, button" with no state, and activating it
 * announces nothing at all -- the label is the only signal, and a virtual-cursor
 * user who moved on never receives it.
 */
export const CollapsibleToggle: Component<CollapsibleToggleProps> = props => (
  <button
    type="button"
    class={collapsibleToggle}
    aria-expanded={props.expanded}
    aria-controls={props.controls}
    data-testid={props['data-testid']}
    onClick={() => props.onToggle()}
  >
    {props.expanded ? (props.lessLabel ?? 'Show less') : props.moreLabel}
  </button>
)
