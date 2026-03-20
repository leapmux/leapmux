import type { JSX } from 'solid-js'
import { For } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import { CLAUDE_PERMISSION_MODE_LABELS } from '~/utils/controlResponse'
import * as styles from './ChatView.css'

export const PERMISSION_MODES = Object.entries(CLAUDE_PERMISSION_MODE_LABELS).map(([value, label]) => ({ label, value }))

export function modeLabel(mode: string): string {
  return CLAUDE_PERMISSION_MODE_LABELS[mode as keyof typeof CLAUDE_PERMISSION_MODE_LABELS] ?? 'Default'
}

export function RadioGroup(props: {
  label: string
  items: { label: string, value: string, tooltip?: string }[]
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
          <Tooltip text={item.tooltip}>
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
          </Tooltip>
        )}
      </For>
    </fieldset>
  )
}
