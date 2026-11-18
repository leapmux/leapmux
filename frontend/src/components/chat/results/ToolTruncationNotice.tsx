import { Show } from 'solid-js'
import { toolInputSummary } from '../toolStyles.css'
import { TRUNCATION_NOTICE } from '../truncationNotice'

/** The notice for output whose earlier content the Worker did not retain. */
export function ToolTruncationNotice(props: { when: boolean | undefined }) {
  return (
    <Show when={props.when}>
      <div class={toolInputSummary}>{TRUNCATION_NOTICE}</div>
    </Show>
  )
}
