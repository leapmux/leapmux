import { describe, expect, it } from 'vitest'
import { codexChangeKind } from './fileChange'

describe('codexChangeKind', () => {
  it('reads the bare word an older frame spells', () => {
    expect(codexChangeKind({ kind: 'add', path: 'a.ts' })).toBe('add')
    expect(codexChangeKind({ kind: 'delete', path: 'gone.ts' })).toBe('delete')
    expect(codexChangeKind({ kind: 'update', path: 'edit.ts' })).toBe('update')
  })

  it('unwraps the object a newer frame spells', () => {
    expect(codexChangeKind({ kind: { type: 'update' }, path: 'edit.ts' })).toBe('update')
    expect(codexChangeKind({ kind: { type: 'update', movePath: 'moved.ts' } })).toBe('update')
  })

  it('answers the empty string for a change that states no kind', () => {
    expect(codexChangeKind({})).toBe('')
    expect(codexChangeKind({ kind: null })).toBe('')
    expect(codexChangeKind({ kind: 7 })).toBe('')
    expect(codexChangeKind({ kind: {} })).toBe('')
    expect(codexChangeKind({ kind: { type: 3 } })).toBe('')
  })
})
