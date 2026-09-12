import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { FileEditDiffSource } from './fileEditDiff'
import { For, Show } from 'solid-js'
import { toolInputSummary, toolResultPrompt } from '../toolStyles.css'
import { fileEditCopyableText, FileEditDiffBody, FileEditDiffTitle, fileEditHasDiff } from './fileEditDiff'

/** Display proposed differences without claiming that the provider applied them. */
export function RequestedFileChanges(props: { sources: FileEditDiffSource[], context: RenderContext }): JSX.Element {
  return (
    <Show when={props.sources.length > 0}>
      <div class={toolResultPrompt}>Requested changes</div>
      <For each={props.sources}>
        {source => (
          <>
            <Show when={props.sources.length > 1 || !fileEditHasDiff(source)}>
              <div class={toolInputSummary}>
                <Show when={source.operation === 'delete'}>Delete </Show>
                <Show when={source.operation === 'add'}>Create </Show>
                <FileEditDiffTitle source={{ ...source, operation: undefined }} context={props.context} />
              </div>
            </Show>
            <Show when={fileEditHasDiff(source)}>
              <FileEditDiffBody source={source} view={props.context.diffView?.() ?? 'unified'} context={props.context} />
            </Show>
          </>
        )}
      </For>
    </Show>
  )
}

/** Preserve the proposed operation when no file content exists to construct a diff. */
export function requestedFileChangesCopyable(sources: FileEditDiffSource[]): string {
  if (sources.length === 0)
    return ''
  return ['Requested changes', ...sources.map((source) => {
    const diff = fileEditCopyableText({ ...source, operation: undefined })
    if (diff)
      return diff
    const verb = source.operation === 'delete' ? 'Delete' : source.operation === 'add' ? 'Create' : 'Change'
    return `${verb} ${source.filePath}`
  })].join('\n\n')
}
