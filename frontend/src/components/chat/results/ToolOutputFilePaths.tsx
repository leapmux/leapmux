import type { JSX } from 'solid-js'
import { Show } from 'solid-js'
import { ToolMetadata } from './ToolMetadata'

/** Display native file paths beside the result. The view reads no file. */
export function ToolOutputFilePaths(props: { paths: readonly string[] | undefined }): JSX.Element {
  return (
    <Show when={props.paths?.length}>
      <div data-testid="tool-output-file-paths">
        <ToolMetadata items={props.paths?.map(value => ({ label: 'Output file', value }))} />
      </div>
    </Show>
  )
}
