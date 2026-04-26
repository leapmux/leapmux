import type { JSX } from 'solid-js'
import { Show } from 'solid-js'
import { toolInputSummary } from '../../../toolStyles.css'

/** Build a title element with a display name and optional status badge. */
export function codexStatusTitle(displayName: string, status: string): JSX.Element {
  return (
    <>
      <span class={toolInputSummary}>{displayName}</span>
      <Show when={status}>
        <span class={toolInputSummary}>{status}</span>
      </Show>
    </>
  )
}
