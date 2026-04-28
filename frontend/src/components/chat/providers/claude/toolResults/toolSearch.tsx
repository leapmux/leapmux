import type { JSX } from 'solid-js'
import { toolMessage, toolResultContentPre } from '../../../toolStyles.css'

/** ToolSearch result view showing matched tool names. */
export function ToolSearchResultView(props: {
  matches: string[]
}): JSX.Element {
  return (
    <div class={toolMessage}>
      <div class={toolResultContentPre}>
        {props.matches.length > 0 ? props.matches.join('\n') : 'No tools found'}
      </div>
    </div>
  )
}
