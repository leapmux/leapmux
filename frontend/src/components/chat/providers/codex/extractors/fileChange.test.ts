import { describe, expect, it } from 'vitest'
import { buildFileChangeShape } from './fileChange'

describe('buildFileChangeShape', () => {
  it('treats a non-array changes payload as empty (no throw)', () => {
    const shape = buildFileChangeShape({ changes: {} as unknown })
    expect(shape.changes).toEqual([])
    expect(shape.simpleAdd).toBeNull()
    expect(shape.simpleDelete).toBeNull()
  })

  it('filters non-object change elements before detection', () => {
    const valid = { path: 'a.txt', diff: '@@ -1,1 +1,1 @@\n-old\n+new' }
    const shape = buildFileChangeShape({ changes: [null, 5, valid] as unknown[] })
    expect(shape.changes).toEqual([valid])
    expect(shape.parsedDiffs.get(valid)).not.toBeNull()
  })

  it('detects a single simple-add (filtering a trailing junk element first)', () => {
    // A [validAdd, null] payload filters to ONE change, so it renders as the
    // single new-file add. The pre-filter code kept length 2 and took the
    // multi-change path, which then deref'd `null.diff` and threw in the
    // no-try/catch estimate memo.
    const add = { kind: 'add', path: 'new.ts', diff: 'line one\nline two' }
    const shape = buildFileChangeShape({ changes: [add, null] as unknown[] })
    expect(shape.changes).toEqual([add])
    expect(shape.simpleAdd).toBe(add)
    expect(shape.simpleAddPath).toBe('new.ts')
    expect(shape.simpleAddContent).toBe('line one\nline two')
    expect(shape.simpleDelete).toBeNull()
  })

  it('detects a single simple-delete', () => {
    const del = { kind: 'delete', path: 'gone.ts' }
    const shape = buildFileChangeShape({ changes: [del] })
    expect(shape.simpleDelete).toBe(del)
    expect(shape.simpleDeletePath).toBe('gone.ts')
    expect(shape.simpleAdd).toBeNull()
  })

  it('does not collapse a genuine multi-change list to a simple add/delete', () => {
    const changes = [
      { kind: 'add', path: 'a.ts', diff: 'x' },
      { kind: 'delete', path: 'b.ts' },
    ]
    const shape = buildFileChangeShape({ changes })
    expect(shape.changes).toHaveLength(2)
    expect(shape.simpleAdd).toBeNull()
    expect(shape.simpleDelete).toBeNull()
  })

  it('detects a single simple-update with a parsed diff (so the renderer stays a pure consumer)', () => {
    const update = { kind: 'update', path: 'edit.ts', diff: '@@ -1,1 +1,1 @@\n-old\n+new' }
    const shape = buildFileChangeShape({ changes: [update] })
    expect(shape.simpleUpdate).toBe(update)
    expect(shape.simpleUpdatePath).toBe('edit.ts')
    expect(shape.simpleUpdateDiff).not.toBeNull()
    expect(shape.simpleUpdateDiff!.oldText).toContain('old')
    expect(shape.simpleUpdateDiff!.newText).toContain('new')
    expect(shape.simpleAdd).toBeNull()
    expect(shape.simpleDelete).toBeNull()
  })

  it('does not treat an update without a diff as a simple update', () => {
    const update = { kind: 'update', path: 'edit.ts', diff: '' }
    const shape = buildFileChangeShape({ changes: [update] })
    expect(shape.simpleUpdate).toBeNull()
    expect(shape.simpleUpdateDiff).toBeNull()
  })

  it('does not collapse a multi-change update list to a simple update', () => {
    const changes = [
      { kind: 'update', path: 'a.ts', diff: '@@ -1,1 +1,1 @@\n-x\n+y' },
      { kind: 'update', path: 'b.ts', diff: '@@ -1,1 +1,1 @@\n-x\n+y' },
    ]
    const shape = buildFileChangeShape({ changes })
    expect(shape.simpleUpdate).toBeNull()
    expect(shape.simpleUpdateDiff).toBeNull()
  })
})
