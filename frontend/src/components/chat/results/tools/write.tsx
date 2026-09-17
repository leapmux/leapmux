import type { JSX } from 'solid-js'
import FilePlus from 'lucide-solid/icons/file-plus'
import { relativizePath } from '~/lib/paths'
import { pluralize } from '~/lib/plural'
import { countLines } from '../../ir/collapse'
import { toolInputPath, toolInputText } from '../../toolStyles.css'
import { fileChangeRenderer } from './fileChanges'

/** The "(N lines)" form a one-file write keeps: the file, and how much of it the call states. */
export function renderWriteTitle(path: string | undefined, content: string | undefined, cwd?: string, homeDir?: string): JSX.Element | null {
  if (!path)
    return null
  const lineCount = content ? countLines(content) : 0
  const lineStr = lineCount > 0 ? ` (${pluralize(lineCount, 'line')})` : ''
  return (
    <>
      <span class={toolInputPath}>{relativizePath(path, cwd, homeDir)}</span>
      <span class={toolInputText}>{lineStr}</span>
    </>
  )
}

export const writeRenderer = fileChangeRenderer<'write'>({
  icon: FilePlus,
  label: 'Write',
  // A write states how much of the file it carries, which no other file-change kind
  // does. Only an `add` states it: the count comes from the new text, and a change
  // that replaces part of a file has no whole-file count to report.
  title(call, context) {
    const single = call.request.changes[0]
    return single?.operation === 'add'
      ? renderWriteTitle(single.filePath, single.newStr, context?.workingDir, context?.homeDir)
      : null
  },
})
