import type { Component } from 'solid-js'
import { Show } from 'solid-js'
import * as styles from '../SettingRow.css'

export interface NumberControlProps {
  value: number | undefined
  min?: number
  max?: number
  step?: number
  unit?: string
  ariaLabel: string
  onChange: (value: number) => void
}

/**
 * A numeric input with optional bounds and a unit rendered as adjacent text
 * (the unit is a label, not part of the value the field edits).
 */
export const NumberControl: Component<NumberControlProps> = (props) => {
  return (
    <span class={styles.numberRow}>
      <input
        type="number"
        class={styles.numberInput}
        aria-label={props.ariaLabel}
        min={props.min}
        max={props.max}
        step={props.step}
        // An unset/empty field must not coerce to 0 on the wire; undefined
        // keeps the control controlled-but-empty until a number is typed.
        value={props.value ?? ''}
        // `valueAsNumber` is NaN for a blank or unparseable field, where
        // `Number('')` is 0. Several keys read 0 as a real setting — a
        // per-user cap of 0 means unlimited, a queue budget of 0 means
        // auto-size — so committing the blank field would silently remove
        // the cap. A typed 0 still commits; only a blank field commits
        // nothing.
        onChange={(e) => {
          const next = e.currentTarget.valueAsNumber
          if (!Number.isNaN(next)) {
            props.onChange(next)
            return
          }
          // The commit is dropped, so put the stored number back. Solid
          // assigns `value` only when the tracked expression CHANGES, and
          // `props.value` did not change — so without this the field keeps
          // showing a blank the store never accepted, for the life of the
          // dialog, with no error to explain it.
          e.currentTarget.value = props.value === undefined ? '' : String(props.value)
        }}
      />
      <Show when={props.unit}><span class={styles.unitLabel}>{props.unit}</span></Show>
    </span>
  )
}
