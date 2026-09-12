import type { StructuredPatchHunk } from '../../diff'
import type { FileEditDiffSource } from '../../results/fileEditDiff'

/** Read the file operations from Copilot's text patch argument. These changes describe a request. */
export function copilotPatchRequest(patch: string): FileEditDiffSource[] | null {
  const lines = patch.replace(/\r\n/g, '\n').split('\n')
  if (lines.at(-1) === '')
    lines.pop()
  if (lines[0] !== '*** Begin Patch' || lines.at(-1) !== '*** End Patch')
    return null
  const files: FileEditDiffSource[] = []
  let index = 1
  while (index < lines.length - 1) {
    const header = /^\*\*\* (Add|Delete|Update) File: (.+)$/.exec(lines[index++])
    if (!header || !header[2].trim() || header[2].includes('\0'))
      return null
    const source: FileEditDiffSource = {
      filePath: header[2],
      operation: header[1] === 'Add' ? 'add' : header[1] === 'Delete' ? 'delete' : 'edit',
      oldStr: '',
      newStr: '',
      structuredPatch: null,
      showLineNumbers: false,
    }
    if (header[1] === 'Add') {
      const content: string[] = []
      while (index < lines.length - 1 && lines[index].startsWith('+'))
        content.push(lines[index++].slice(1))
      if (content.length === 0)
        return null
      source.newStr = `${content.join('\n')}\n`
    }
    else if (header[1] === 'Update') {
      if (lines[index]?.startsWith('*** Move to: ')) {
        const path = lines[index++].slice('*** Move to: '.length)
        if (!path.trim() || path.includes('\0'))
          return null
        source.previousPath = source.filePath
        source.filePath = path
        source.operation = 'move'
      }
      const hunks: StructuredPatchHunk[] = []
      while (index < lines.length - 1 && /^@@(?: .*)?$/.test(lines[index])) {
        index++
        // Text patches identify context without line numbers. Request titles use only the counts.
        const hunk: StructuredPatchHunk = { oldStart: 0, newStart: 0, oldLines: 0, newLines: 0, lines: [] }
        while (index < lines.length - 1 && /^[ +\-]/.test(lines[index])) {
          const line = lines[index++]
          hunk.lines.push(line)
          if (line[0] !== '+')
            hunk.oldLines++
          if (line[0] !== '-')
            hunk.newLines++
        }
        if (hunk.lines.length === 0)
          return null
        hunks.push(hunk)
        if (lines[index] === '*** End of File') {
          index++
          break
        }
      }
      if (hunks.length === 0 && !source.previousPath)
        return null
      source.structuredPatch = hunks.length > 0 ? hunks : null
    }
    files.push(source)
  }
  return files.length > 0 ? files : null
}
