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

  it('preserves a move with changed content', () => {
    const files = copilotPatchRequest('*** Begin Patch\n*** Update File: old.ts\n*** Move to: new.ts\n@@\n-old\n+new\n*** End Patch')
    expect(files?.[0]).toMatchObject({ filePath: 'new.ts', previousPath: 'old.ts', operation: 'move' })
    expect(diffStatsFromHunks(fileEditDiffHunks(files![0]))).toEqual({ added: 1, deleted: 1 })
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
