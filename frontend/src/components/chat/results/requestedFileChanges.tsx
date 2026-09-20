import type { JSX } from 'solid-js'
import type { FileEditDiff } from '../model/fileEditDiff'
import type { ToolResultRenderContext } from '../renderContext'
import { For, Show } from 'solid-js'
import { fileEditDrawsDiff, fileEditHasDiff } from '../model/fileEditDiff'
import { toolInputSummary, toolResultPrompt } from '../toolStyles.css'
import { FileEditDiffBody, FileEditDiffTitle } from './fileEditDiff'

/** Display proposed differences without claiming that the provider applied them. */
export function RequestedFileChanges(props: { sources: FileEditDiff[], context?: ToolResultRenderContext }): JSX.Element {
  return (
    <Show when={props.sources.length > 0}>
      <div class={toolResultPrompt}>Requested changes</div>
      <For each={props.sources}>
        {(source) => {
          // The REQUEST card words its own operation line above, so the title must
          // not restate it as an applied fact; the key leaves rather than turning
          // undefined, which the facts' optional member refuses.
          const facts: FileEditDiff = { ...source }
          delete facts.operation
          return (
            <>
              <Show when={props.sources.length > 1 || !fileEditDrawsDiff(source)}>
                <div class={toolInputSummary}>
                  <Show when={source.operation === 'delete'}>Delete </Show>
                  <Show when={source.operation === 'add'}>Create </Show>
                  <FileEditDiffTitle source={facts} {...(props.context !== undefined ? { context: props.context } : {})} />
                </div>
              </Show>
              <Show when={fileEditHasDiff(source)}>
                <FileEditDiffBody source={source} diff={{ view: () => props.context?.diffView?.() ?? 'unified' }} {...(props.context !== undefined ? { context: props.context } : {})} />
              </Show>
            </>
          )
        }}
      </For>
    </Show>
  )
}
