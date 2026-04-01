import type { Component, JSX } from 'solid-js'

import * as styles from './CompactSwitch.css'

interface CompactSwitchProps {
  'checked': boolean
  'onChange': (checked: boolean) => void
  'children': JSX.Element
  'data-testid'?: string
  /** Controls the switch size via font-size (e.g. 'var(--text-8)'). */
  'fontSize'?: string
}

/** A compact toggle switch label. Scale via the fontSize prop. */
export const CompactSwitch: Component<CompactSwitchProps> = (props) => {
  return (
    <label
      class={styles.compactSwitch}
      data-testid={props['data-testid']}
      style={props.fontSize ? { 'font-size': props.fontSize } : undefined}
    >
      <input
        type="checkbox"
        role="switch"
        checked={props.checked}
        onChange={e => props.onChange(e.currentTarget.checked)}
      />
      {props.children}
    </label>
  )
}
