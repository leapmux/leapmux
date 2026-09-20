import type { JSX } from 'solid-js'
import FileSymlink from 'lucide-solid/icons/file-symlink'
import { relativizePath } from '~/lib/paths'
import { toolInputPath, toolInputText } from '../../toolStyles.css'
import { fileChangeRenderer } from './fileChanges'

/**
 * A move's title while it runs: the source, an arrow, and the destination.
 *
 * Each span carries its class, as `renderReadTitle` gives its own path one.
 * `ToolUseLayout` wraps a STRING title in `toolInputText` and leaves a JSX title
 * alone, so unclassed spans reached the header with no monospace face and no
 * one-line clip -- and two long paths then wrapped the header onto extra rows.
 */
export function renderMoveTitle(source: string | undefined, destination: string | undefined, context?: { workingDir?: string | undefined, homeDir?: string | undefined }): JSX.Element | null {
  if (!source && !destination)
    return null
  const show = (path: string) => relativizePath(path, context?.workingDir, context?.homeDir)
  if (!source || !destination)
    return <span class={toolInputPath}>{show(source || destination!)}</span>
  return (
    <>
      <span class={toolInputPath}>{show(source)}</span>
      <span class={toolInputText}>{' → '}</span>
      <span class={toolInputPath}>{show(destination)}</span>
    </>
  )
}

export const moveRenderer = fileChangeRenderer<'move'>({
  icon: FileSymlink,
  label: 'Move',
  // Both ends of the move, while it RUNS. Once the result lands, the shared title
  // states what the file operation became, which is what the reader then needs.
  title(call, context) {
    if (call.result)
      return null
    const single = call.request.changes[0]
    return renderMoveTitle(single?.previousPath, single?.filePath, context)
  },
})
