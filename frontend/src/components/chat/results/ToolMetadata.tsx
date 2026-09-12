import type { JSX } from 'solid-js'
import { For, Show } from 'solid-js'
import { toolMetaLabel, toolMetaList, toolMetaRow, toolMetaValue } from '../toolStyles.css'

export interface ToolMetadataItem { label: string, value: string }

/** Display structured tool details with the common labels and spacing. */
export function ToolMetadata(props: { items: ToolMetadataItem[] | undefined }): JSX.Element {
  return (
    <Show when={props.items?.length}>
      <div class={toolMetaList}>
        <For each={props.items}>
          {item => (
            <div class={toolMetaRow}>
              <span class={toolMetaLabel}>{`${item.label}:`}</span>
              <span class={toolMetaValue}>{item.value}</span>
            </div>
          )}
        </For>
      </div>
    </Show>
  )
}
