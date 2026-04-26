import type { StructuredPatchHunk } from '../../../diff'
import { describe, expect, it } from 'vitest'
import { codexFileEditFromAdd, codexFileEditFromHunks } from './fileEdit'

describe('codexFileEditFromAdd', () => {
  it('builds an all-added source with the file content as newStr', () => {
    expect(codexFileEditFromAdd('/tmp/new.ts', 'package main\n')).toEqual({
      filePath: '/tmp/new.ts',
      structuredPatch: null,
      oldStr: '',
      newStr: 'package main\n',
    })
  })

  it('preserves an empty path / empty content', () => {
    expect(codexFileEditFromAdd('', '')).toEqual({
      filePath: '',
      structuredPatch: null,
      oldStr: '',
      newStr: '',
    })
  })
})

describe('codexFileEditFromHunks', () => {
  const hunks: StructuredPatchHunk[] = [
    { oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-old', '+new'] },
  ]

  it('attaches pre-parsed hunks and leaves the string halves empty', () => {
    expect(codexFileEditFromHunks('/tmp/a.ts', hunks)).toEqual({
      filePath: '/tmp/a.ts',
      structuredPatch: hunks,
      oldStr: '',
      newStr: '',
    })
  })

  it('keeps the hunks reference identity (no defensive copy)', () => {
    const result = codexFileEditFromHunks('/tmp/a.ts', hunks)
    expect(result.structuredPatch).toBe(hunks)
  })
})
