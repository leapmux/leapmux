import type { JSX } from 'solid-js'
import { For } from 'solid-js'
import { EFFORT_LABELS, MODEL_LABELS, PERMISSION_MODE_LABELS } from '~/utils/controlResponse'
import * as styles from './ChatView.css'

export const PERMISSION_MODES = Object.entries(PERMISSION_MODE_LABELS).map(([value, label]) => ({ label, value }))
export const MODELS = Object.entries(MODEL_LABELS).map(([value, label]) => ({ label, value }))
export const EFFORTS = Object.entries(EFFORT_LABELS).map(([value, label]) => ({ label, value }))

export function modeLabel(mode: string): string {
  return PERMISSION_MODE_LABELS[mode as keyof typeof PERMISSION_MODE_LABELS] ?? 'Default'
}

export function modelLabel(model: string): string {
  return MODELS.find(m => m.value === model)?.label ?? 'Sonnet'
}

export function RadioGroup(props: {
  label: string
  items: { label: string, value: string }[]
  testIdPrefix: string
  name: string
  current: string
  onChange: (value: string) => void
}): JSX.Element {
  return (
    <fieldset>
      <legend class={styles.settingsGroupLabel}>{props.label}</legend>
      <For each={props.items}>
        {item => (
          <label
            role="menuitemradio"
            class={styles.settingsRadioItem}
            data-testid={`${props.testIdPrefix}-${item.value}`}
          >
            <input
              type="radio"
              name={props.name}
              value={item.value}
              checked={props.current === item.value}
              onChange={() => props.onChange(item.value)}
            />
            {item.label}
          </label>
        )}
      </For>
    </fieldset>
  )
}
