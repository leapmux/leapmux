import { describe, expect, it } from 'vitest'
import { ohMyPiHashlineChanges, ohMyPiPatchChanges, ohMyPiResultChanges, ohMyPiWriteChange } from './fileEdit'

describe('ohMyPiHashlineChanges', () => {
  it('states one change per file header', () => {
    expect(ohMyPiHashlineChanges('*** Begin Patch\n[src/a.ts#1A2B]\nPUT 3.=3:\n+x\n[src/b.ts#ffff]\nPUT >$:\n+y\n*** End Patch')).toEqual([
      { filePath: 'src/a.ts', operation: 'edit', oldStr: '', newStr: '', structuredPatch: null },
      { filePath: 'src/b.ts', operation: 'edit', oldStr: '', newStr: '', structuredPatch: null },
    ])
  })

  it('reads a removal and a move', () => {
    expect(ohMyPiHashlineChanges('[old.ts#1A2B]\nREM\n[a.ts#0000]\nMV b.ts')).toEqual([
      { filePath: 'old.ts', operation: 'delete', oldStr: '', newStr: '', structuredPatch: null },
      { filePath: 'b.ts', previousPath: 'a.ts', operation: 'move', oldStr: '', newStr: '', structuredPatch: null },
    ])
  })

  it('reads a header with no snapshot tag, which omp takes for a file the model did not read', () => {
    // omp 18.2.11's grammar (`hashline/tokenizer.rs`, `parse_header`) takes `[PATH]`
    // as well as `[PATH#TAG]`.
    expect(ohMyPiHashlineChanges('[src/new.ts]\nPUT >$:\n+export const x = 1\n[src/old.ts#1a2b]\nREM')).toEqual([
      { filePath: 'src/new.ts', operation: 'edit', oldStr: '', newStr: '', structuredPatch: null },
      { filePath: 'src/old.ts', operation: 'delete', oldStr: '', newStr: '', structuredPatch: null },
    ])
  })

  it('states nothing for a patch with no header', () => {
    expect(ohMyPiHashlineChanges('PUT 1.=1:\n+x')).toEqual([])
    // Each row that omp's grammar refuses as a header: a tag that is not four hex
    // digits, a second `#`, an unbalanced bracket, an empty path.
    expect(ohMyPiHashlineChanges('[a.ts#NOTHEX]\nPUT 1.=1:')).toEqual([])
    expect(ohMyPiHashlineChanges('[a#b.ts]\nPUT 1.=1:')).toEqual([])
    expect(ohMyPiHashlineChanges('[a#b#1A2B]\nPUT 1.=1:')).toEqual([])
    expect(ohMyPiHashlineChanges('[a]b]\nPUT 1.=1:')).toEqual([])
    expect(ohMyPiHashlineChanges('[]\nPUT 1.=1:')).toEqual([])
    expect(ohMyPiHashlineChanges('[#1A2B]\nPUT 1.=1:')).toEqual([])
  })

  it('reads no file from a bracketed envelope marker, and reads the header behind one', () => {
    expect(ohMyPiHashlineChanges('[*** Begin Patch] [src/a.ts#1A2B]\nPUT 1.=1:\n+x\n[*** End Patch]')).toEqual([
      { filePath: 'src/a.ts', operation: 'edit', oldStr: '', newStr: '', structuredPatch: null },
    ])
    expect(ohMyPiHashlineChanges('[*** Abort]')).toEqual([])
  })

  it('reads a path that holds a balanced bracket', () => {
    expect(ohMyPiHashlineChanges('[src/[id]/page.tsx#0F0F]\nPUT 1.=1:\n+x')[0]?.filePath).toBe('src/[id]/page.tsx')
  })

  it('reads a patch with Windows line endings and indented headers', () => {
    expect(ohMyPiHashlineChanges('  [a.ts#1A2B]  \r\nPUT 1.=1:\r\n+x\r\n[b.ts#2B3C]\r\n  REM  \r\n')).toEqual([
      { filePath: 'a.ts', operation: 'edit', oldStr: '', newStr: '', structuredPatch: null },
      { filePath: 'b.ts', operation: 'delete', oldStr: '', newStr: '', structuredPatch: null },
    ])
  })

  it('reads an operation keyword only below a header, and only as the whole row', () => {
    // A REM or an MV above the first header belongs to no file; a row that only
    // starts with REM is a line edit.
    expect(ohMyPiHashlineChanges('REM\nMV b.ts\n[a.ts#1A2B]\nREMOVE this\n+REM')).toEqual([
      { filePath: 'a.ts', operation: 'edit', oldStr: '', newStr: '', structuredPatch: null },
    ])
  })

  it('reads a bracketed envelope marker that closes no bracket, and one with spaces inside it', () => {
    expect(ohMyPiHashlineChanges('[*** End Patch\n[ *** Begin Patch ] [a.ts#1A2B]\nPUT 1.=1:')).toEqual([
      { filePath: 'a.ts', operation: 'edit', oldStr: '', newStr: '', structuredPatch: null },
    ])
  })

  it('states nothing for an empty patch', () => {
    expect(ohMyPiHashlineChanges('')).toEqual([])
  })
})

describe('ohMyPiPatchChanges', () => {
  it('reads an apply-patch envelope', () => {
    const changes = ohMyPiPatchChanges({ input: '*** Begin Patch\n*** Update File: a.ts\n@@\n-old\n+new\n*** End Patch' })
    expect(changes?.[0]?.filePath).toBe('a.ts')
  })

  it('reads a hashline patch', () => {
    expect(ohMyPiPatchChanges({ input: '[notes.txt#C789]\nPUT 2.=2:\n+beta TWO' })?.[0]?.filePath).toBe('notes.txt')
  })

  it('answers null for a call with no patch text', () => {
    expect(ohMyPiPatchChanges({ path: 'a.ts', old_string: 'a', new_string: 'b' })).toBeNull()
    expect(ohMyPiPatchChanges({ input: '   ' })).toBeNull()
  })
})

describe('ohMyPiResultChanges', () => {
  it('reads the one file of a single edit from its snapshots', () => {
    // omp 18.2.11's own details (probe), path shortened.
    expect(ohMyPiResultChanges({
      diff: ' 1|alpha one\n-2|beta two\n+2|beta TWO',
      op: 'update',
      path: '/p/notes.txt',
      oldText: 'alpha one\nbeta two\n',
      newText: 'alpha one\nbeta TWO\n',
    }, 'notes.txt')).toEqual([
      { filePath: '/p/notes.txt', operation: 'edit', oldStr: 'alpha one\nbeta two\n', newStr: 'alpha one\nbeta TWO\n', structuredPatch: null },
    ])
  })

  it('reads each file of a multi-file edit and skips a file that failed', () => {
    expect(ohMyPiResultChanges({
      perFileResults: [
        { path: '/p/new.ts', op: 'create', newText: 'x\n' },
        { path: '/p/gone.ts', op: 'delete', oldText: 'y\n' },
        { path: '/p/renamed.ts', sourcePath: '/p/old.ts', oldText: 'a', newText: 'a' },
        { path: '/p/bad.ts', isError: true, errorText: 'stale tag' },
        'not a record',
      ],
    }, '')).toEqual([
      { filePath: '/p/new.ts', operation: 'add', oldStr: '', newStr: 'x\n', structuredPatch: null },
      { filePath: '/p/gone.ts', operation: 'delete', oldStr: 'y\n', newStr: '', structuredPatch: null },
      { filePath: '/p/renamed.ts', previousPath: '/p/old.ts', operation: 'move', oldStr: 'a', newStr: 'a', structuredPatch: null },
    ])
  })

  it('keeps each file of a multi-file edit whose snapshots omp pruned, with a notice in place of its diff', () => {
    // omp 18.2.11 (`edit/index.ts`, `capPerFileSnapshots`) drops both texts of each
    // later file once the snapshots of one edit pass 32,768 characters.
    expect(ohMyPiResultChanges({
      perFileResults: [
        { path: '/p/a.ts', op: 'update', diff: '-1|a\n+1|A', oldText: 'a\n', newText: 'A\n' },
        { path: '/p/b.ts', op: 'update', diff: '-1|b\n+1|B', snapshotsPruned: true },
        { path: '/p/c.ts', op: 'create', diff: '+1|c', snapshotsPruned: true },
      ],
    }, '')).toEqual([
      { filePath: '/p/a.ts', operation: 'edit', oldStr: 'a\n', newStr: 'A\n', structuredPatch: null },
      { filePath: '/p/b.ts', operation: 'edit', oldStr: '', newStr: '', structuredPatch: null, notice: 'omp kept no snapshot of this file, so no diff is available.' },
      { filePath: '/p/c.ts', operation: 'add', oldStr: '', newStr: '', structuredPatch: null, notice: 'omp kept no snapshot of this file, so no diff is available.' },
    ])
  })

  it('takes the requested path when the result states none', () => {
    expect(ohMyPiResultChanges({ oldText: 'a', newText: 'b' }, 'a.ts')[0]?.filePath).toBe('a.ts')
  })

  it('never takes the requested path for a file of a multi-file edit', () => {
    // Each file of a multi-file edit states its own path. The requested path is the
    // path of the first file, so it would identify the wrong file for any other one.
    expect(ohMyPiResultChanges({ perFileResults: [{ oldText: 'a', newText: 'b' }, { path: '/p/b.ts', oldText: 'c', newText: 'd' }] }, 'a.ts')).toEqual([
      { filePath: '/p/b.ts', operation: 'edit', oldStr: 'c', newStr: 'd', structuredPatch: null },
    ])
  })

  it('reads the top-level fields of an edit whose multi-file list is empty', () => {
    expect(ohMyPiResultChanges({ perFileResults: [], path: '/p/a.ts', oldText: 'a', newText: 'b' }, '')).toEqual([
      { filePath: '/p/a.ts', operation: 'edit', oldStr: 'a', newStr: 'b', structuredPatch: null },
    ])
  })

  it('reads a file that moves and changes, and states no earlier path for a source path that is the same file', () => {
    expect(ohMyPiResultChanges({ path: '/p/new.ts', sourcePath: '/p/old.ts', oldText: 'a', newText: 'b' }, '')).toEqual([
      { filePath: '/p/new.ts', previousPath: '/p/old.ts', operation: 'move', oldStr: 'a', newStr: 'b', structuredPatch: null },
    ])
    expect(ohMyPiResultChanges({ path: '/p/a.ts', sourcePath: '/p/a.ts', op: 'update', oldText: 'a', newText: 'b' }, '')[0]).not.toHaveProperty('previousPath')
  })

  it('reads a text of the wrong type as no text', () => {
    expect(ohMyPiResultChanges({ path: '/p/a.ts', oldText: 3, newText: 'b' }, '')).toEqual([
      { filePath: '/p/a.ts', operation: 'edit', oldStr: '', newStr: 'b', structuredPatch: null },
    ])
    expect(ohMyPiResultChanges({ path: '/p/a.ts', oldText: null, newText: ['b'] }, '')).toEqual([])
  })

  it('states nothing when omp kept no snapshot', () => {
    expect(ohMyPiResultChanges({ diff: '-a\n+b', path: '/p/a.ts', snapshotsPruned: true }, '')).toEqual([])
    expect(ohMyPiResultChanges({}, '')).toEqual([])
  })
})

describe('ohMyPiWriteChange', () => {
  it('states the whole content as added, at the resolved path', () => {
    expect(ohMyPiWriteChange({ path: 'new.txt', content: 'fresh line\n' }, '/p/new.txt')).toEqual({
      filePath: '/p/new.txt',
      operation: 'add',
      oldStr: '',
      newStr: 'fresh line\n',
      structuredPatch: null,
    })
  })

  it('takes the argument path when no resolved path exists, and states nothing with neither', () => {
    expect(ohMyPiWriteChange({ path: 'new.txt' }, '')?.filePath).toBe('new.txt')
    expect(ohMyPiWriteChange({}, '')).toBeNull()
  })

  it('states an empty file for a write with no content', () => {
    expect(ohMyPiWriteChange({ path: 'empty.txt' }, '')).toEqual({ filePath: 'empty.txt', operation: 'add', oldStr: '', newStr: '', structuredPatch: null })
    expect(ohMyPiWriteChange({ path: 'empty.txt', content: 42 }, '')?.newStr).toBe('')
  })
})
