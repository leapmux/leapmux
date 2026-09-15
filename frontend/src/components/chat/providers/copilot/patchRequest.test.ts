import { describe, expect, it } from 'vitest'
import { diffStatsFromHunks } from '../../diff'
import { fileEditDiffHunks } from '../../results/fileEditDiff'
import { copilotPatchRequest } from './patchRequest'

describe('copilot patch requests', () => {
  it('preserves file paths and content with spaces, blank lines, and patch-like text', () => {
    const files = copilotPatchRequest('*** Begin Patch\r\n*** Add File: a b.txt\r\n+first\r\n+\r\n+*** End Patch\r\n*** End Patch\r\n')
    expect(files).toHaveLength(1)
    expect(files![0]).toMatchObject({ filePath: 'a b.txt', operation: 'add', newStr: 'first\n\n*** End Patch\n' })
  })

  it('keeps every update section without inventing line numbers', () => {
    const files = copilotPatchRequest('*** Begin Patch\n*** Update File: src/code.ts\n@@ function first\n context\n-removed\n+added\n@@ function second\n-another removal\n+another addition\n+extra line\n*** End of File\n*** Delete File: removed.ts\n*** End Patch')
    expect(files).toHaveLength(2)
    expect(files![0].showLineNumbers).toBe(false)
    expect(diffStatsFromHunks(fileEditDiffHunks(files![0]))).toEqual({ added: 3, deleted: 2 })
    expect(files![1]).toMatchObject({ filePath: 'removed.ts', operation: 'delete' })
  })

  // A blank context line arrives as the EMPTY string, with no marker after it. The
  // hunk loop used to stop there, the file-header pattern then rejected the same line,
  // and the whole patch fell back to its raw text.
  it('reads a blank context line as part of its hunk', () => {
    const files = copilotPatchRequest('*** Begin Patch\n*** Update File: src/code.ts\n@@ function first\n context\n\n+added\n*** End Patch')
    expect(files).toHaveLength(1)
    expect(files![0]).toMatchObject({ filePath: 'src/code.ts', operation: 'edit' })
    expect(fileEditDiffHunks(files![0])[0].lines).toEqual([' context', ' ', '+added'])
    expect(diffStatsFromHunks(fileEditDiffHunks(files![0]))).toEqual({ added: 1, deleted: 0 })
  })

  // A blank line that SEPARATES two sections is not a context line. Consuming it
  // folds a row the agent never proposed into the preview and raises both counts.
  it('stops a hunk at a blank line before the next file header', () => {
    const files = copilotPatchRequest('*** Begin Patch\n*** Update File: a.ts\n@@\n+one\n\n*** Update File: b.ts\n@@\n+two\n*** End Patch')
    expect(files).toHaveLength(2)
    expect(fileEditDiffHunks(files![0])[0].lines).toEqual(['+one'])
    expect(fileEditDiffHunks(files![0])[0]).toMatchObject({ oldLines: 0, newLines: 1 })
    expect(fileEditDiffHunks(files![1])[0].lines).toEqual(['+two'])
  })

  it('stops a hunk at a trailing blank line before the end of the patch', () => {
    const files = copilotPatchRequest('*** Begin Patch\n*** Update File: a.ts\n@@\n+one\n\n*** End Patch')
    expect(fileEditDiffHunks(files![0])[0].lines).toEqual(['+one'])
    expect(fileEditDiffHunks(files![0])[0]).toMatchObject({ oldLines: 0, newLines: 1 })
  })

  it('stops a hunk at a blank line before the end-of-file marker', () => {
    const files = copilotPatchRequest('*** Begin Patch\n*** Update File: a.ts\n@@\n+one\n\n*** End of File\n*** End Patch')
    expect(fileEditDiffHunks(files![0])[0].lines).toEqual(['+one'])
  })

  it('keeps a blank context line that another hunk line follows', () => {
    const files = copilotPatchRequest('*** Begin Patch\n*** Update File: a.ts\n@@\n+one\n\n\n context\n*** End Patch')
    expect(fileEditDiffHunks(files![0])[0].lines).toEqual(['+one', ' ', ' ', ' context'])
  })

  it('preserves a move with changed content', () => {
    const files = copilotPatchRequest('*** Begin Patch\n*** Update File: old.ts\n*** Move to: new.ts\n@@\n-old\n+new\n*** End Patch')
    expect(files?.[0]).toMatchObject({ filePath: 'new.ts', previousPath: 'old.ts', operation: 'move' })
    expect(diffStatsFromHunks(fileEditDiffHunks(files![0]))).toEqual({ added: 1, deleted: 1 })
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
    const files = copilotPatchRequest(patch)
    expect(files).toHaveLength(1)
    expect(files![0]).toMatchObject({ filePath: 'a.ts', operation: 'add' })
    expect(fileEditDiffHunks(files![0])[0].lines).toEqual(['+one'])
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
    expect(copilotPatchRequest(patch)).toBeNull()
  })
})
