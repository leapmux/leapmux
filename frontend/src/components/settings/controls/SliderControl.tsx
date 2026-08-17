import type { Component } from 'solid-js'
import { createEffect, createSignal, on, untrack } from 'solid-js'
import * as styles from '../SettingRow.css'

export interface SliderControlProps {
  value: number
  min: number
  max: number
  step: number
  unit?: string
  ariaLabel: string
  onChange: (value: number) => void
}

/**
 * A bounded numeric choice on a range input, with a live value readout.
 *
 * The readout tracks the drag locally (input events) but `onChange` commits
 * only on the change event — the release — because the row beneath may be
 * writing each commit to the account tier over an RPC, and a value per pixel
 * of drag would fire a hundred writes for one gesture.
 */
export const SliderControl: Component<SliderControlProps> = (props) => {
  const [draft, setDraft] = createSignal(untrack(() => props.value))
  // Sync from the binding when the value changes OUTSIDE this control (a
  // reset, a server echo, another device). Our own commit lands the same
  // number, so the sync is a no-op there.
  createEffect(on(() => props.value, v => setDraft(v)))

  return (
    <div class={styles.sliderRow}>
      <input
        type="range"
        aria-label={props.ariaLabel}
        min={props.min}
        max={props.max}
        step={props.step}
        value={draft()}
        onInput={e => setDraft(Number(e.currentTarget.value))}
        onChange={() => props.onChange(draft())}
      />
      <span class={styles.sliderValue}>
        {draft()}
        {props.unit ?? ''}
      </span>
    </div>
  )
}
