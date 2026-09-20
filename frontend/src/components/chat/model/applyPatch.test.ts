import type { StructuredPatchHunk } from '../diff/diffTypes'
import type { FileEditDiff } from './fileEditDiff'
import { describe, expect, it } from 'vitest'
import { diffStatsFromHunks } from '../diff'
import { applyPatchFileChanges } from './applyPatch'
import { fileEditDiffHunks } from './fileEditDiff'

// The reader answers null or a list; each test states which section it means, and
// these guards turn that statement into a value the typed helpers below can take.
function fileAt(files: FileEditDiff[] | null, index = 0): FileEditDiff {
  const file = files?.[index]
  if (file === undefined)
    throw new Error(`expected a file section at ${index}`)
  return file
}

function firstHunkOf(file: FileEditDiff): StructuredPatchHunk {
  const hunk = fileEditDiffHunks(file)[0]
  if (hunk === undefined)
    throw new Error('expected at least one hunk')
  return hunk
}

describe('applyPatchFileChanges', () => {
  it('preserves file paths and content with spaces, blank lines, and patch-like text', () => {
    const files = applyPatchFileChanges('*** Begin Patch\r\n*** Add File: a b.txt\r\n+first\r\n+\r\n+*** End Patch\r\n*** End Patch\r\n')
    expect(files).toHaveLength(1)
    expect(files![0]).toMatchObject({ filePath: 'a b.txt', operation: 'add', newStr: 'first\n\n*** End Patch\n' })
  })

  it('keeps every update section without inventing line numbers', () => {
    const files = applyPatchFileChanges('*** Begin Patch\n*** Update File: src/code.ts\n@@ function first\n context\n-removed\n+added\n@@ function second\n-another removal\n+another addition\n+extra line\n*** End of File\n*** Delete File: removed.ts\n*** End Patch')
    expect(files).toHaveLength(2)
    expect(fileAt(files).showLineNumbers).toBe(false)
    expect(diffStatsFromHunks(fileEditDiffHunks(fileAt(files, 0)))).toEqual({ added: 3, deleted: 2 })
    expect(files![1]).toMatchObject({ filePath: 'removed.ts', operation: 'delete' })
  })

  // A blank context line arrives as the EMPTY string, with no marker after it. The
  // hunk loop used to stop there, the file-header pattern then rejected the same line,
  // and the whole patch fell back to its raw text.
  it('reads a blank context line as part of its hunk', () => {
    const files = applyPatchFileChanges('*** Begin Patch\n*** Update File: src/code.ts\n@@ function first\n context\n\n+added\n*** End Patch')
    expect(files).toHaveLength(1)
    expect(files![0]).toMatchObject({ filePath: 'src/code.ts', operation: 'edit' })
    expect(firstHunkOf(fileAt(files, 0)).lines).toEqual([' context', ' ', '+added'])
    expect(diffStatsFromHunks(fileEditDiffHunks(fileAt(files, 0)))).toEqual({ added: 1, deleted: 0 })
  })

  // A blank line that SEPARATES two sections is not a context line. Consuming it
  // folds a row the agent never proposed into the preview and raises both counts.
  it('stops a hunk at a blank line before the next file header', () => {
    const files = applyPatchFileChanges('*** Begin Patch\n*** Update File: a.ts\n@@\n+one\n\n*** Update File: b.ts\n@@\n+two\n*** End Patch')
    expect(files).toHaveLength(2)
    expect(firstHunkOf(fileAt(files, 0)).lines).toEqual(['+one'])
    expect(firstHunkOf(fileAt(files))).toMatchObject({ oldLines: 0, newLines: 1 })
    expect(firstHunkOf(fileAt(files, 1)).lines).toEqual(['+two'])
  })

  it('stops a hunk at a trailing blank line before the end of the patch', () => {
    const files = applyPatchFileChanges('*** Begin Patch\n*** Update File: a.ts\n@@\n+one\n\n*** End Patch')
    expect(firstHunkOf(fileAt(files, 0)).lines).toEqual(['+one'])
    expect(firstHunkOf(fileAt(files))).toMatchObject({ oldLines: 0, newLines: 1 })
  })

  it('stops a hunk at a blank line before the end-of-file marker', () => {
    const files = applyPatchFileChanges('*** Begin Patch\n*** Update File: a.ts\n@@\n+one\n\n*** End of File\n*** End Patch')
    expect(firstHunkOf(fileAt(files, 0)).lines).toEqual(['+one'])
  })

  it('keeps a blank context line that another hunk line follows', () => {
    const files = applyPatchFileChanges('*** Begin Patch\n*** Update File: a.ts\n@@\n+one\n\n\n context\n*** End Patch')
    expect(firstHunkOf(fileAt(files, 0)).lines).toEqual(['+one', ' ', ' ', ' context'])
  })

  it('preserves a move with changed content', () => {
    const files = applyPatchFileChanges('*** Begin Patch\n*** Update File: old.ts\n*** Move to: new.ts\n@@\n-old\n+new\n*** End Patch')
    expect(files?.[0]).toMatchObject({ filePath: 'new.ts', previousPath: 'old.ts', operation: 'move' })
    expect(diffStatsFromHunks(fileEditDiffHunks(fileAt(files, 0)))).toEqual({ added: 1, deleted: 1 })
  })

  // A move that changes no content is the whole section: a header, a `Move to`,
  // and nothing else. The hunk count of zero must not read as an empty section,
  // which the reader refuses.
  it('preserves a move that changes no content', () => {
    const files = applyPatchFileChanges('*** Begin Patch\n*** Update File: old.ts\n*** Move to: new.ts\n*** End Patch')
    expect(files).toHaveLength(1)
    expect(files![0]).toMatchObject({ filePath: 'new.ts', previousPath: 'old.ts', operation: 'move', structuredPatch: null, oldStr: '', newStr: '' })
  })

  // The patch is a model-written tool argument, so a second trailing newline is
  // ordinary output. Trimming ONE left an empty string as the last line, the
  // `*** End Patch` test failed, and the whole patch was refused: the row drew the raw
  // patch text instead of a diff, for one extra newline byte.
  it.each([
    ['one trailing newline', '*** Begin Patch\n*** Add File: a.ts\n+one\n*** End Patch\n'],
    ['two trailing newlines', '*** Begin Patch\n*** Add File: a.ts\n+one\n*** End Patch\n\n'],
    ['four trailing newlines', '*** Begin Patch\n*** Add File: a.ts\n+one\n*** End Patch\n\n\n\n'],
    ['two trailing CRLFs', '*** Begin Patch\r\n*** Add File: a.ts\r\n+one\r\n*** End Patch\r\n\r\n'],
  ])('parses a patch that ends with %s', (_name, patch) => {
    const files = applyPatchFileChanges(patch)
    expect(files).toHaveLength(1)
    expect(files![0]).toMatchObject({ filePath: 'a.ts', operation: 'add' })
    expect(firstHunkOf(fileAt(files, 0)).lines).toEqual(['+one'])
  })

  it.each([
    '',
    '*** Begin Patch\n*** End Patch',
    '*** Begin Patch\n*** Add File: a.ts\n*** End Patch',
    '*** Begin Patch\n*** Update File: a.ts\n*** End Patch',
    '*** Begin Patch\n*** Update File: a.ts\n@@\n*** End Patch',
    '*** Begin Patch\n*** Update File: a.ts\nmissing prefix\n*** End Patch',
    '*** Begin Patch\n*** Add File: a.ts\n+partial',
    '*** Begin Patch\n*** Delete File: \n*** End Patch',
    '*** Begin Patch\n*** Add File: bad\0path\n+text\n*** End Patch',
    '*** Begin Patch\n*** Update File: a.ts\n*** Move to: \n*** End Patch',
    '*** Begin Patch\n*** Update File: a.ts\n@@\n+text\n*** End of File\n@@\n+unexpected\n*** End Patch',
  ])('rejects incomplete or unsupported patch syntax (%j)', (patch) => {
    expect(applyPatchFileChanges(patch)).toBeNull()
  })
})
