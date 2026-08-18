import type { Component } from 'solid-js'

export interface ToggleControlProps {
  value: boolean
  onChange: (value: boolean) => void
  ariaLabel: string
}

/**
 * A boolean setting as a native switch. The row renders the visible label;
 * the input carries its own accessible name so the pairing survives the row's
 * layout (the label element is not an ancestor of the control here).
 */
export const ToggleControl: Component<ToggleControlProps> = (props) => {
  return (
    <input
      type="checkbox"
      role="switch"
      aria-label={props.ariaLabel}
      checked={props.value}
      onChange={e => props.onChange(e.currentTarget.checked)}
    />
  )
}
