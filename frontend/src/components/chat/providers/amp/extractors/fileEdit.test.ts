import { describe, expect, it } from 'vitest'
import { fileEditDiffHunks } from '../../../model/fileEditDiff'
import { ampCreateFileChange, ampEditFileChange, ampEditFileResultChange, ampFilePathFromUri, ampPatchRequestChanges, ampPatchResultChanges } from './fileEdit'

const UNIFIED = 'Index: /work/a.ts\n===================================================================\n--- /work/a.ts\n+++ /work/a.ts\n@@ -1,1 +1,1 @@\n-old\n+new\n'

describe('ampFilePathFromUri', () => {
  it('reads the path of a file URI', () => {
    expect(ampFilePathFromUri('file:///work/a.ts')).toBe('/work/a.ts')
    expect(ampFilePathFromUri('file:///work/my%20dir/a.ts')).toBe('/work/my dir/a.ts')
  })

  it('drops the slash before a Windows drive letter', () => {
    expect(ampFilePathFromUri('file:///C:/work/a.ts')).toBe('C:/work/a.ts')
    expect(ampFilePathFromUri('file:///c:/work/a.ts')).toBe('c:/work/a.ts')
  })

  it('keeps text that is not a file URI', () => {
    expect(ampFilePathFromUri('/work/a.ts')).toBe('/work/a.ts')
    expect(ampFilePathFromUri('')).toBe('')
  })

  // `%zz` is no escape, so the path cannot decode, and the URI is the best text left.
  it('keeps a file URI whose escape does not decode', () => {
    expect(ampFilePathFromUri('file:///work/a%zz.ts')).toBe('file:///work/a%zz.ts')
  })
})

describe('ampPatchRequestChanges', () => {
  it('reads the file operations of the patch text', () => {
    const changes = ampPatchRequestChanges({ patchText: '*** Begin Patch\n*** Update File: /work/a.ts\n@@\n-old\n+new\n*** End Patch' })
    expect(changes?.map(change => [change.filePath, change.operation])).toEqual([['/work/a.ts', 'edit']])
  })

  it('answers null for a patch it cannot follow, or no patch', () => {
    expect(ampPatchRequestChanges({ patchText: 'not a patch' })).toBeNull()
    expect(ampPatchRequestChanges({})).toBeNull()
  })
})

describe('ampPatchResultChanges', () => {
  it('reads the diff of each file that landed', () => {
    const changes = ampPatchResultChanges(JSON.stringify({
      summary: 'update: /work/a.ts (+1/-1)',
      files: [{ uri: 'file:///work/a.ts', type: 'update', additions: 1, deletions: 1, diff: UNIFIED }],
    }))
    expect(changes).toHaveLength(1)
    expect(changes?.[0]?.filePath).toBe('/work/a.ts')
    expect(changes?.[0]?.operation).toBe('edit')
    expect(fileEditDiffHunks(changes![0]!)).toHaveLength(1)
  })

  it('keeps an add, a delete and a move that state no diff', () => {
    const changes = ampPatchResultChanges(JSON.stringify({
      summary: '',
      files: [
        { uri: 'file:///work/new.ts', type: 'add', diff: '' },
        { uri: 'file:///work/gone.ts', type: 'delete', diff: '' },
        { uri: 'file:///work/moved.ts', type: 'move', diff: '' },
        { uri: 'file:///work/same.ts', type: 'update', diff: '' },
        { uri: '', type: 'update', diff: UNIFIED },
        'not an entry',
      ],
    }))
    expect(changes?.map(change => [change.filePath, change.operation])).toEqual([
      ['/work/new.ts', 'add'],
      ['/work/gone.ts', 'delete'],
      ['/work/moved.ts', 'move'],
    ])
  })

  it('answers null for a result that is not the record', () => {
    expect(ampPatchResultChanges('Applied.')).toBeNull()
    expect(ampPatchResultChanges('{"summary":"x"}')).toBeNull()
    expect(ampPatchResultChanges('[]')).toBeNull()
  })

  it('reads an operation it does not know as an edit', () => {
    const changes = ampPatchResultChanges(JSON.stringify({ summary: '', files: [{ uri: 'file:///work/a.ts', type: 'rename', diff: UNIFIED }] }))
    expect(changes?.map(change => [change.filePath, change.operation])).toEqual([['/work/a.ts', 'edit']])
  })

  it('reads a record that lists no file as no change', () => {
    expect(ampPatchResultChanges(JSON.stringify({ summary: '', files: [] }))).toEqual([])
  })
})

describe('ampEditFileChange', () => {
  it('reads the two sides and the file', () => {
    expect(ampEditFileChange({ path: '/work/a.ts', old_str: 'old', new_str: 'new' })).toEqual({
      filePath: '/work/a.ts',
      structuredPatch: null,
      oldStr: 'old',
      newStr: 'new',
      operation: 'edit',
    })
  })

  it('answers null for a call that states no file', () => {
    expect(ampEditFileChange({ old_str: 'a', new_str: 'b' })).toBeNull()
  })
})

describe('ampEditFileResultChange', () => {
  const requested = ampEditFileChange({ path: '/work/a.ts', old_str: 'old', new_str: 'new' })

  it('draws the diff Amp states, fenced or not', () => {
    for (const diff of [UNIFIED, `\`\`\`diff\n${UNIFIED}\`\`\``]) {
      const change = ampEditFileResultChange(JSON.stringify({ diff, lineRange: [1, 1] }), requested)
      expect(change?.structuredPatch, diff).toHaveLength(1)
      expect(change?.operation).toBe('edit')
    }
  })

  it('keeps the requested change for a diff it cannot read', () => {
    expect(ampEditFileResultChange(JSON.stringify({ diff: 'no hunks here', lineRange: [1, 1] }), requested)).toBe(requested)
    expect(ampEditFileResultChange('Edited.', requested)).toBe(requested)
    expect(ampEditFileResultChange('Edited.', null)).toBeNull()
  })

  // The diff states no file of its own that the row can trust, so a call that
  // stated no file draws no change, whatever the diff holds.
  it('answers null for a diff when the call stated no file', () => {
    expect(ampEditFileResultChange(JSON.stringify({ diff: UNIFIED, lineRange: [1, 1] }), null)).toBeNull()
  })
})

describe('ampCreateFileChange', () => {
  it('reads the whole file as added', () => {
    expect(ampCreateFileChange({ path: '/work/n.txt', content: 'x\n' })).toMatchObject({ filePath: '/work/n.txt', oldStr: '', newStr: 'x\n', operation: 'add' })
    expect(ampCreateFileChange({ content: 'x' })).toBeNull()
  })
})
