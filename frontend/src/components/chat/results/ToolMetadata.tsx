import type { JSX } from 'solid-js'
import type { ToolMetadataEntry } from '../model/toolMetadata'
import { For, Show } from 'solid-js'
import { relativizePath } from '~/lib/paths'
import { toolMetaLabel, toolMetaList, toolMetaRow, toolMetaValue } from '../toolStyles.css'

export function ToolMetadata(props: {
  items: ToolMetadataEntry[] | undefined
  workingDir?: string | undefined
  homeDir?: string | undefined
}): JSX.Element {
  const shown = (item: ToolMetadataEntry): string => item.path === true
    ? relativizePath(item.value, props.workingDir, props.homeDir)
    : item.value
  return (
    <Show when={props.items?.length}>
      <div class={toolMetaList}>
        <For each={props.items}>
          {item => (
            <div class={toolMetaRow}>
              <span class={toolMetaLabel}>{`${item.label}:`}</span>
              <span class={toolMetaValue}>{shown(item)}</span>
            </div>
          )}
        </For>
      </div>
    </Show>
  )
}
