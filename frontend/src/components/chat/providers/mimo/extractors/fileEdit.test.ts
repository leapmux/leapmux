import type { MiMoToolPart } from './toolCommon'
import { describe, expect, it } from 'vitest'
import { MIMO_TOOL } from '~/generated/contracts/mimo-protocol'
import { mimoLandedChanges, mimoRequestedChanges } from './fileEdit'

/** One finished file tool call. */
function filePart(tool: string, metadata: Record<string, unknown>): MiMoToolPart {
  return { callId: 'call-1', tool, status: 'completed', input: {}, output: '', error: '', title: '', metadata, attachments: [] }
}

/** A unified diff of one line of one file. */
function lineDiff(path: string, line: number, before: string, after: string): string {
  return `--- ${path}\n+++ ${path}\n@@ -${line},1 +${line},1 @@\n-${before}\n+${after}\n`
}

describe('mimoRequestedChanges', () => {
  it('reads one replacement of an edit', () => {
    expect(mimoRequestedChanges(MIMO_TOOL.Edit, { file_path: '/p/a.ts', old_string: 'a', new_string: 'b' }))
      .toEqual([{ filePath: '/p/a.ts', operation: 'edit', oldStr: 'a', newStr: 'b', structuredPatch: null }])
  })

  it('reads each replacement of a multiedit, and skips an entry that is not an object', () => {
    expect(mimoRequestedChanges(MIMO_TOOL.MultiEdit, { file_path: '/p/a.ts', edits: [{ old_string: 'a', new_string: 'b' }, 'noise', { old_string: 'c', new_string: 'd' }] }))
      .toEqual([
        { filePath: '/p/a.ts', operation: 'edit', oldStr: 'a', newStr: 'b', structuredPatch: null },
        { filePath: '/p/a.ts', operation: 'edit', oldStr: 'c', newStr: 'd', structuredPatch: null },
      ])
  })

  it('reads the new source of a notebook cell', () => {
    expect(mimoRequestedChanges(MIMO_TOOL.NotebookEdit, { notebook_path: '/p/n.ipynb', cell_id: 'c1', new_source: 'print(2)' }))
      .toEqual([{ filePath: '/p/n.ipynb', operation: 'edit', oldStr: '', newStr: 'print(2)', structuredPatch: null }])
  })

  // A delete of a cell states no new source, and the change still names the notebook.
  it('reads a cell delete as a change of the notebook with no new text', () => {
    expect(mimoRequestedChanges(MIMO_TOOL.NotebookEdit, { notebook_path: '/p/n.ipynb', cell_id: 'c1', edit_mode: 'delete' }))
      .toEqual([{ filePath: '/p/n.ipynb', operation: 'edit', oldStr: '', newStr: '', structuredPatch: null }])
  })

  it('reads a write as an add of the whole body', () => {
    expect(mimoRequestedChanges(MIMO_TOOL.Write, { file_path: '/p/new.ts', content: 'x\n' }))
      .toEqual([{ filePath: '/p/new.ts', operation: 'add', oldStr: '', newStr: 'x\n', structuredPatch: null }])
  })

  it('reads each file of a patch envelope', () => {
    const patch = '*** Begin Patch\n*** Add File: /p/new.ts\n+fresh\n*** Delete File: /p/old.ts\n*** Update File: /p/a.ts\n@@\n-alpha\n+omega\n*** End Patch'
    expect(mimoRequestedChanges(MIMO_TOOL.ApplyPatch, { patch_text: patch }).map(change => [change.filePath, change.operation])).toEqual([
      ['/p/new.ts', 'add'],
      ['/p/old.ts', 'delete'],
      ['/p/a.ts', 'edit'],
    ])
  })

  // An edit states no old text when it writes into an empty file.
  it('reads an edit with no old text as an empty old text', () => {
    expect(mimoRequestedChanges(MIMO_TOOL.Edit, { file_path: '/p/a.ts', new_string: 'b' }))
      .toEqual([{ filePath: '/p/a.ts', operation: 'edit', oldStr: '', newStr: 'b', structuredPatch: null }])
  })

  it('reads an empty edit list of a multiedit as no change', () => {
    expect(mimoRequestedChanges(MIMO_TOOL.MultiEdit, { file_path: '/p/a.ts', edits: [] })).toEqual([])
  })

  it.each([
    ['a patch that is not an envelope', MIMO_TOOL.ApplyPatch, { patch_text: '--- a\n+++ b\n' }],
    ['an edit with no file', MIMO_TOOL.Edit, { old_string: 'a', new_string: 'b' }],
    ['a multiedit with no file', MIMO_TOOL.MultiEdit, { edits: [{ old_string: 'a', new_string: 'b' }] }],
    ['a multiedit with no edit list', MIMO_TOOL.MultiEdit, { file_path: '/p/a.ts', edits: 'a->b' }],
    ['a notebook edit with no notebook', MIMO_TOOL.NotebookEdit, { cell_id: 'c1', new_source: 'x' }],
    ['a write with no file', MIMO_TOOL.Write, { content: 'x' }],
    ['a patch with no patch text', MIMO_TOOL.ApplyPatch, {}],
  ])('reads no change from %s', (_name, tool, input) => {
    expect(mimoRequestedChanges(tool, input)).toEqual([])
  })
})

describe('mimoLandedChanges', () => {
  it('reads the diff of an edit from its file diff', () => {
    const patch = lineDiff('/p/a.ts', 1, 'a', 'b')
    const changes = mimoLandedChanges(filePart(MIMO_TOOL.Edit, { diff: patch, filediff: { file: '/p/a.ts', patch } }), '/p/a.ts')
    expect(changes?.map(change => [change.filePath, change.structuredPatch?.length])).toEqual([['/p/a.ts', 1]])
  })

  it('reads the diff of a notebook edit on the notebook the call gave', () => {
    const changes = mimoLandedChanges(filePart(MIMO_TOOL.NotebookEdit, { diff: lineDiff('/p/n.ipynb', 4, 'a', 'b'), edit_mode: 'replace', cell_id: 'c1' }), '/p/n.ipynb')
    expect(changes?.map(change => [change.filePath, change.structuredPatch?.[0]?.oldStart])).toEqual([['/p/n.ipynb', 4]])
  })

  it('reads the diff that each replacement of a multiedit landed, in order', () => {
    const first = lineDiff('/p/a.ts', 1, 'a', 'b')
    const second = lineDiff('/p/a.ts', 3, 'c', 'd')
    const changes = mimoLandedChanges(filePart(MIMO_TOOL.MultiEdit, {
      results: [{ diff: first, filediff: { file: '/p/a.ts', patch: first } }, { diff: second, filediff: { file: '/p/a.ts', patch: second } }],
    }), '/p/a.ts')
    expect(changes?.map(change => [change.filePath, change.structuredPatch?.[0]?.oldStart])).toEqual([['/p/a.ts', 1], ['/p/a.ts', 3]])
  })

  // An entry that states only the top-level diff still lands, and an entry that
  // states no diff is skipped rather than drawn as an empty change.
  it('reads the diff of a multiedit entry from either field, and skips an entry with none', () => {
    const first = lineDiff('/p/a.ts', 1, 'a', 'b')
    const changes = mimoLandedChanges(filePart(MIMO_TOOL.MultiEdit, { results: [{ diff: first }, {}, 'noise'] }), '/p/a.ts')
    expect(changes?.map(change => change.structuredPatch?.[0]?.oldStart)).toEqual([1])
  })

  it('reads no landed change from a multiedit whose entries state no diff', () => {
    expect(mimoLandedChanges(filePart(MIMO_TOOL.MultiEdit, { results: [{}, { diff: '' }] }), '/p/a.ts')).toBeNull()
    expect(mimoLandedChanges(filePart(MIMO_TOOL.MultiEdit, { results: [] }), '/p/a.ts')).toBeNull()
  })

  it('reads a write of a new file as an add, and of an existing file as an edit', () => {
    const patch = lineDiff('/p/a.ts', 1, 'x', 'y')
    expect(mimoLandedChanges(filePart(MIMO_TOOL.Write, { diff: patch, filepath: '/p/a.ts', exists: false }), '/p/a.ts')?.[0]?.operation).toBe('add')
    expect(mimoLandedChanges(filePart(MIMO_TOOL.Write, { diff: patch, filepath: '/p/a.ts', exists: true }), '/p/a.ts')?.[0]?.operation).toBe('edit')
  })

  it('reads each file of a patch, a moved file included', () => {
    const changes = mimoLandedChanges(filePart(MIMO_TOOL.ApplyPatch, {
      files: [{ filePath: '/p/a.ts', type: 'update', patch: lineDiff('/p/a.ts', 1, 'a', 'b') }, { filePath: '/p/b.ts', movePath: '/p/c.ts', type: 'move' }, { type: 'add' }],
    }), '')
    expect(changes?.map(change => [change.filePath, change.operation, change.previousPath])).toEqual([['/p/a.ts', 'edit', undefined], ['/p/c.ts', 'move', '/p/b.ts']])
  })

  it('reads no landed change from a call that states none', () => {
    expect(mimoLandedChanges(filePart(MIMO_TOOL.Edit, {}), '/p/a.ts')).toBeNull()
    expect(mimoLandedChanges(filePart(MIMO_TOOL.Edit, { diff: 'not a diff' }), '/p/a.ts')).toBeNull()
  })

  // `filediff.patch` is read first. A file diff that changes no line leaves the
  // top-level `diff` to state the change.
  it('reads the top-level diff when the file diff changes no line', () => {
    const patch = lineDiff('/p/a.ts', 5, 'a', 'b')
    const changes = mimoLandedChanges(filePart(MIMO_TOOL.Edit, { diff: patch, filediff: { file: '/p/a.ts', patch: 'Index: /p/a.ts\n' } }), '/p/a.ts')
    expect(changes?.map(change => change.structuredPatch?.[0]?.oldStart)).toEqual([5])
  })

  // The file the diff lands on: the file diff's own file, then the metadata's path,
  // then the path the call asked for.
  it('states the diff on the file the metadata gives, before the requested path', () => {
    const patch = lineDiff('/p/a.ts', 1, 'a', 'b')
    expect(mimoLandedChanges(filePart(MIMO_TOOL.Edit, { diff: patch, filediff: { file: '/p/real.ts' }, filepath: '/p/meta.ts' }), '/p/asked.ts')?.[0]?.filePath).toBe('/p/real.ts')
    expect(mimoLandedChanges(filePart(MIMO_TOOL.Edit, { diff: patch, filepath: '/p/meta.ts' }), '/p/asked.ts')?.[0]?.filePath).toBe('/p/meta.ts')
    expect(mimoLandedChanges(filePart(MIMO_TOOL.Edit, { diff: patch }), '/p/asked.ts')?.[0]?.filePath).toBe('/p/asked.ts')
  })

  it('reads a write whose metadata states nothing about the file as an add', () => {
    expect(mimoLandedChanges(filePart(MIMO_TOOL.Write, { diff: lineDiff('/p/a.ts', 1, 'x', 'y') }), '/p/a.ts')?.[0]?.operation).toBe('add')
    expect(mimoLandedChanges(filePart(MIMO_TOOL.Write, { diff: lineDiff('/p/a.ts', 1, 'x', 'y'), exists: 'yes' }), '/p/a.ts')?.[0]?.operation).toBe('add')
  })

  it('reads the add, the delete and the diff field of each file of a patch', () => {
    const changes = mimoLandedChanges(filePart(MIMO_TOOL.ApplyPatch, {
      files: [
        { filePath: '/p/new.ts', type: 'add', diff: '--- /dev/null\n+++ /p/new.ts\n@@ -0,0 +1,1 @@\n+fresh\n' },
        { filePath: '/p/old.ts', type: 'delete' },
      ],
    }), '')
    expect(changes?.map(change => [change.filePath, change.operation])).toEqual([['/p/new.ts', 'add'], ['/p/old.ts', 'delete']])
    expect(changes?.[0]?.structuredPatch).toHaveLength(1)
  })

  // A patch whose file list states no file falls back to the diff of the whole call,
  // rather than drawing an empty change.
  it('reads the call\'s own diff when no file of the patch states a path', () => {
    const patch = lineDiff('/p/a.ts', 2, 'a', 'b')
    const changes = mimoLandedChanges(filePart(MIMO_TOOL.ApplyPatch, { files: [{ type: 'update' }, 'noise'], diff: patch }), '/p/a.ts')
    expect(changes?.map(change => [change.filePath, change.structuredPatch?.[0]?.oldStart])).toEqual([['/p/a.ts', 2]])
    expect(mimoLandedChanges(filePart(MIMO_TOOL.ApplyPatch, { files: [] }), '')).toBeNull()
  })

  // Only a multiedit reads `results`. Another tool's stray list is no statement of
  // what it landed.
  it('reads no results list on a tool that is not a multiedit', () => {
    expect(mimoLandedChanges(filePart(MIMO_TOOL.Edit, { results: [{ diff: lineDiff('/p/a.ts', 1, 'a', 'b') }] }), '/p/a.ts')).toBeNull()
  })
})
