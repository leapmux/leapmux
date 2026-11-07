import { describe, expect, it } from 'vitest'
import { codexChangeKind } from './fileChange'

describe('codexChangeKind', () => {
  it('rejects a bare kind outside the current tagged union', () => {
    expect(codexChangeKind({ kind: 'add', path: 'a.ts' })).toBe('')
    expect(codexChangeKind({ kind: 'delete', path: 'gone.ts' })).toBe('')
    expect(codexChangeKind({ kind: 'update', path: 'edit.ts' })).toBe('')
  })

  it('reads the current tagged union', () => {
    expect(codexChangeKind({
      kind: { type: 'update' },
      path: 'edit.ts',
    })).toBe('update')
    expect(codexChangeKind({
      kind: { type: 'update', move_path: 'moved.ts' },
    })).toBe('update')
  })

  it('answers the empty string for a change that states no kind', () => {
    expect(codexChangeKind({})).toBe('')
    expect(codexChangeKind({ kind: null })).toBe('')
    expect(codexChangeKind({ kind: 7 })).toBe('')
    expect(codexChangeKind({ kind: {} })).toBe('')
    expect(codexChangeKind({
      kind: { type: 3 },
    })).toBe('')
  })
})
