import type { Component, JSX } from 'solid-js'
import { For, Show } from 'solid-js'
import * as styles from './ThinkingIndicator.css'

export interface ThinkingStatusCounter {
  show: () => boolean
  render: () => JSX.Element
}

export const ThinkingStatusRow: Component<{
  verb: JSX.Element
  counters: ThinkingStatusCounter[]
}> = props => (
  <span class={styles.verbRow}>
    {props.verb}
    <For each={props.counters}>
      {(counter, index) => (
        <Show when={counter.show()}>
          <Show when={props.counters.slice(0, index()).some(earlier => earlier.show())}>
            <span class={styles.countSeparator} aria-hidden="true">·</span>
          </Show>
          {counter.render()}
        </Show>
      )}
    </For>
  </span>
)
