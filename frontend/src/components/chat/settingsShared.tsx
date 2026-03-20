import type { JSX } from 'solid-js'
import { For } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import * as styles from './ChatView.css'

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
