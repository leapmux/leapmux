import type { Component } from 'solid-js'

import { For } from 'solid-js'
import { CompactSwitch } from '~/components/common/CompactSwitch'
import * as styles from '../ControlRequestBanner.css'

export interface ControlRequestSwitch {
  id: string
  label: string
  checked: boolean
  onChange: (checked: boolean) => void
  suffix?: string
}

/** Renders the compact option switches that sit beside control decisions. */
export const ControlRequestSwitches: Component<{
  items: ControlRequestSwitch[]
}> = props => (
  <div class={styles.controlRequestSwitches}>
    <For each={props.items}>
      {item => (
        <CompactSwitch
          checked={item.checked}
          onChange={item.onChange}
          data-testid={item.id}
          fontSize="var(--text-8)"
        >
          {item.label}
          {item.suffix}
        </CompactSwitch>
      )}
    </For>
  </div>
)
