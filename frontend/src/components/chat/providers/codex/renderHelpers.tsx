import type { JSX } from 'solid-js'
import type { CommandStreamSegment } from '~/stores/chat.store'
import { For } from 'solid-js'
import { isObject } from '~/lib/jsonPick'
import { commandStreamContainer, toolResultContentPre } from '../../toolStyles.css'

/**
 * Codex emits items either wrapped (`{item: {...}, threadId, turnId}`) or
 * unwrapped (the item is the top-level object, for `item/completed`-style
 * messages stored directly). Resolves to the inner item or null.
 */
export function extractItem(parsed: unknown): Record<string, unknown> | null {
  if (!isObject(parsed))
    return null
  const item = parsed.item as Record<string, unknown> | undefined
  if (isObject(item))
    return item
  if (parsed.type && typeof parsed.type === 'string')
    return parsed
  return null
}

/** Plain stream-of-segments output area used by command-execution and file-change in-progress views. */
export function LiveStreamOutput(props: { stream: () => CommandStreamSegment[] }): JSX.Element {
  return (
    <div class={commandStreamContainer}>
      <For each={props.stream()}>
        {segment => (
          <div class={toolResultContentPre}>{segment.text}</div>
        )}
      </For>
    </div>
  )
}
