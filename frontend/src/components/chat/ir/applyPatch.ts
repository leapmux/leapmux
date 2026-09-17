import type { StructuredPatchHunk } from '../diff/diffTypes'
import type { FileEditDiff, FileEditFacts } from './fileEditDiff'
import { fileEditContent } from './fileEditDiff'

/**
 * The `*** Begin Patch` envelope, which belongs to the `apply_patch` TOOL rather than
 * to any one runtime.
 *
 * An `apply_patch` call states its whole change in one text argument: a `Begin Patch`
 * header, one section for each file, and an `End Patch` footer. Two plugins send that
 * text today and the dialect is the same in both, so the reader sits in layer 2 where
 * each of them reaches it. What DOES differ is the argument key -- Copilot spells it
 * `input`, ZCode spells it `patch` -- and the plugin that knows its own key reads that
 * half and hands the text here.
 *
 * It refuses rather than mis-renders: a section this reader cannot follow answers null
 * for the WHOLE patch, and the row then draws the patch text as the tool sent it.
 */

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
    // The bound above keeps `next` in range; `?? ''` is the type-level guard alone.
    const line = lines[next] ?? ''
    if (line === '')
      continue
    return /^[ +\-]/.test(line)
  }
  return false
}

/** Read the file operations from an apply-patch text argument. These changes describe a request. */
export function applyPatchFileChanges(patch: string): FileEditDiff[] | null {
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
  const files: FileEditDiff[] = []
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
    const header = /^\*\*\* (Add|Delete|Update) File: (.+)$/.exec(lines[index++] ?? '')
    // Both groups are captured by the pattern above; the guards keep the indexed
    // reads honest rather than restating what the regex already proved.
    const [, action, path] = header ?? []
    if (!header || !path?.trim() || path.includes('\0'))
      return null
    // The facts of this section, and the two halves it can produce. Only one is ever
    // filled, so the source is built once, at the end.
    const facts: FileEditFacts = {
      filePath: path,
      operation: action === 'Add' ? 'add' : action === 'Delete' ? 'delete' : 'edit',
      showLineNumbers: false,
    }
    let newStr = ''
    let patchHunks: StructuredPatchHunk[] = []
    if (action === 'Add') {
      const content: string[] = []
      while (index < lines.length - 1) {
        const line = lines[index]
        if (line === undefined || !line.startsWith('+'))
          break
        content.push(line.slice(1))
        index++
      }
      if (content.length === 0)
        return null
      newStr = `${content.join('\n')}\n`
    }
    else if (action === 'Update') {
      const moveLine = lines[index]
      if (moveLine !== undefined && moveLine.startsWith('*** Move to: ')) {
        const movePath = moveLine.slice('*** Move to: '.length)
        index++
        if (!movePath.trim() || movePath.includes('\0'))
          return null
        facts.previousPath = facts.filePath
        facts.filePath = movePath
        facts.operation = 'move'
      }
      const hunks: StructuredPatchHunk[] = []
      while (index < lines.length - 1 && /^@@(?: .*)?$/.test(lines[index] ?? '')) {
        index++
        // Text patches identify context without line numbers. Request titles use only the counts.
        const hunk: StructuredPatchHunk = { oldStart: 0, newStart: 0, oldLines: 0, newLines: 0, lines: [] }
        while (index < lines.length - 1 && (hunkBodyLine(lines, index) || /^[ +\-]/.test(lines[index] ?? ''))) {
          // A blank context line reaches the patch as the EMPTY string, because a
          // trailing space does not survive every producer. The hunk still owns that
          // line, so restore its marker. Without the restore, the hunk loop stops at
          // that line, the file-header patterns then reject the same line, and the
          // whole patch falls back to its raw text.
          const line = lines[index++] || ' '
          hunk.lines.push(line)
          if (!line.startsWith('+'))
            hunk.oldLines++
          if (!line.startsWith('-'))
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
      if (hunks.length === 0 && !facts.previousPath)
        return null
      patchHunks = hunks
    }
    files.push({ ...facts, ...fileEditContent(patchHunks, '', newStr) })
    skipBlankLines()
  }
  return files.length > 0 ? files : null
}
