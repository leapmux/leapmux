import type { StructuredPatchHunk } from '../../diff'
import type { FileEditDiffSource } from '../../results/fileEditDiff'

/**
 * Whether the EMPTY line at `index` is a blank context line of the hunk, rather
 * than a separator before the next section.
 *
 * It decides by lookahead, and the lookahead is what keeps the parser's "refuse
 * rather than mis-render" rule. An empty line that any later line of the same
 * hunk follows belongs to the hunk. An empty line before `*** Update File:`,
 * before `*** End of File` or before `*** End Patch` does not, and treating it
 * as context would fold a blank row the agent never proposed into the preview
 * and raise both line counts by one. A trailing blank that nothing follows stays
 * ambiguous, so it ends the hunk, which is the answer that refuses.
 */
function hunkBodyLine(lines: string[], index: number): boolean {
  if (lines[index] !== '')
    return false
  for (let next = index + 1; next < lines.length - 1; next++) {
    if (lines[next] === '')
      continue
    return /^[ +\-]/.test(lines[next])
  }
  return false
}

/** Read the file operations from Copilot's text patch argument. These changes describe a request. */
export function copilotPatchRequest(patch: string): FileEditDiffSource[] | null {
  const lines = patch.replace(/\r\n/g, '\n').split('\n')
  // EVERY trailing blank, not one. The patch is a model-written tool argument, so a
  // second trailing newline is ordinary output -- and a single `pop` left one empty
  // string as the last line, which failed the `*** End Patch` test below and refused
  // the WHOLE patch. The row then drew the raw patch text instead of a diff, for one
  // extra newline byte.
  while (lines.at(-1) === '')
    lines.pop()
  if (lines[0] !== '*** Begin Patch' || lines.at(-1) !== '*** End Patch')
    return null
  const files: FileEditDiffSource[] = []
  let index = 1
  // A blank line BETWEEN two sections is a separator, not part of either one.
  // Skipping it here is what lets the hunk loop below treat an empty line as a
  // context line without swallowing the separator that ends its hunk.
  const skipBlankLines = () => {
    while (index < lines.length - 1 && lines[index] === '')
      index++
  }
  skipBlankLines()
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
        while (index < lines.length - 1 && (hunkBodyLine(lines, index) || /^[ +\-]/.test(lines[index]))) {
          // A blank context line reaches the patch as the EMPTY string, because a
          // trailing space does not survive every producer. The hunk still owns that
          // line, so restore its marker. Without the restore, the hunk loop stops at
          // that line, the file-header patterns then reject the same line, and the
          // whole patch falls back to its raw text.
          const line = lines[index++] || ' '
          hunk.lines.push(line)
          if (line[0] !== '+')
            hunk.oldLines++
          if (line[0] !== '-')
            hunk.newLines++
        }
        if (hunk.lines.length === 0)
          return null
        hunks.push(hunk)
        skipBlankLines()
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
    skipBlankLines()
  }
  return files.length > 0 ? files : null
}
