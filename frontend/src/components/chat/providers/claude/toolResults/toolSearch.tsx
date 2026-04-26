import type { JSX } from 'solid-js'
import { For, Show } from 'solid-js'
import { toolMessage, toolResultContentPre } from '../../../toolStyles.css'

/** ToolSearch result view showing matched tool names. */
export function ToolSearchResultView(props: {
  matches: string[]
}): JSX.Element {
  return (
    <div class={toolMessage}>
      <Show
        when={props.matches.length > 0}
        fallback={<div class={toolResultContentPre}>No tools found</div>}
      >
        <div class={toolResultContentPre}>
          <For each={props.matches}>
            {(name, i) => (
              <>
                {i() > 0 && '\n'}
                {name}
              </>
            )}
          </For>
        </div>
      </Show>
    </div>
  )
}
