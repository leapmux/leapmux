import type { Component, JSX } from 'solid-js'
import * as styles from './ButtonGroup.css'

interface ButtonGroupProps {
  children: JSX.Element
  class?: string
}

/**
 * A connected button group that joins adjacent buttons visually.
 * Wraps Oat's `<menu class="buttons">` which handles stripping inner
 * border-radii and adding divider borders between children.
 */
export const ButtonGroup: Component<ButtonGroupProps> = (props) => (
  <menu class={`buttons ${styles.group} ${props.class ?? ''}`}>
    {props.children}
  </menu>
)
