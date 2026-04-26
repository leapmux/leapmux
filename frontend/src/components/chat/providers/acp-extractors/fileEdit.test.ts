import { describe, expect, it } from 'vitest'
import { acpFileEditFromToolCallContent, acpFileEditFromToolCallRawInput } from './fileEdit'

describe('acpFileEditFromToolCallContent', () => {
  it('returns null for non-array input', () => {
    expect(acpFileEditFromToolCallContent(null)).toBeNull()
    expect(acpFileEditFromToolCallContent(undefined)).toBeNull()
    expect(acpFileEditFromToolCallContent('not an array')).toBeNull()
  })

  it('returns null when no diff entry is present', () => {
    expect(acpFileEditFromToolCallContent([
      { type: 'content', content: { text: 'hi' } },
    ])).toBeNull()
  })

  it('extracts the first { type: "diff", path, oldText, newText } entry', () => {
    expect(acpFileEditFromToolCallContent([
      { type: 'content', content: { text: 'noise' } },
      { type: 'diff', path: '/x.ts', oldText: 'before', newText: 'after' },
      { type: 'diff', path: '/ignored.ts', oldText: '', newText: 'second' },
    ])).toEqual({
      filePath: '/x.ts',
      structuredPatch: null,
      oldStr: 'before',
      newStr: 'after',
    })
  })

  it('handles a diff entry with missing optional fields', () => {
    expect(acpFileEditFromToolCallContent([
      { type: 'diff', path: '/x.ts' },
    ])).toEqual({
      filePath: '/x.ts',
      structuredPatch: null,
      oldStr: '',
      newStr: '',
    })
  })

  it('returns null when the diff entry is empty (no path / oldText / newText)', () => {
    expect(acpFileEditFromToolCallContent([{ type: 'diff' }])).toBeNull()
  })

  it('skips non-object entries', () => {
    expect(acpFileEditFromToolCallContent([
      'string',
      42,
      null,
      { type: 'diff', path: '/x.ts', oldText: 'a', newText: 'b' },
    ])).toEqual({
      filePath: '/x.ts',
      structuredPatch: null,
      oldStr: 'a',
      newStr: 'b',
    })
  })
})

describe('acpFileEditFromToolCallRawInput', () => {
  it('returns null for null/undefined input', () => {
    expect(acpFileEditFromToolCallRawInput('edit', null)).toBeNull()
    expect(acpFileEditFromToolCallRawInput('edit', undefined)).toBeNull()
  })

  it('returns null when no filePath/path is present', () => {
    expect(acpFileEditFromToolCallRawInput('edit', { oldText: 'a', newText: 'b' })).toBeNull()
  })

  it('extracts edit-style { filePath, oldText, newText }', () => {
    expect(acpFileEditFromToolCallRawInput('edit', {
      filePath: '/tmp/a.ts',
      oldText: 'before',
      newText: 'after',
    })).toEqual({
      filePath: '/tmp/a.ts',
      structuredPatch: null,
      oldStr: 'before',
      newStr: 'after',
    })
  })

  it('accepts snake_case and camelCase variants', () => {
    expect(acpFileEditFromToolCallRawInput('edit', {
      file_path: '/tmp/a.ts',
      old_string: 'before',
      new_string: 'after',
    })).toEqual({
      filePath: '/tmp/a.ts',
      structuredPatch: null,
      oldStr: 'before',
      newStr: 'after',
    })

    expect(acpFileEditFromToolCallRawInput('edit', {
      path: '/tmp/a.ts',
      oldString: 'before',
      newString: 'after',
    })).toEqual({
      filePath: '/tmp/a.ts',
      structuredPatch: null,
      oldStr: 'before',
      newStr: 'after',
    })
  })

  it('treats partial edit-style inputs as edit shape (defaulting missing half to empty)', () => {
    expect(acpFileEditFromToolCallRawInput('edit', {
      filePath: '/tmp/a.ts',
      newText: 'only-new',
    })).toEqual({
      filePath: '/tmp/a.ts',
      structuredPatch: null,
      oldStr: '',
      newStr: 'only-new',
    })
  })

  it('extracts write-style { filePath, content } as a new-file write fallback', () => {
    expect(acpFileEditFromToolCallRawInput('edit', {
      filePath: '/tmp/a.ts',
      content: 'package main\n',
    })).toEqual({
      filePath: '/tmp/a.ts',
      structuredPatch: null,
      oldStr: '',
      newStr: 'package main\n',
    })
  })

  it('does not treat read/search/execute kinds with content/path as a write fallback', () => {
    expect(acpFileEditFromToolCallRawInput('read', {
      filePath: '/tmp/a.ts',
      content: 'whatever',
    })).toBeNull()
    expect(acpFileEditFromToolCallRawInput('search', {
      path: '/tmp',
      content: 'matches',
    })).toBeNull()
    expect(acpFileEditFromToolCallRawInput('execute', {
      path: '/tmp/a.ts',
      content: 'ignored',
    })).toBeNull()
  })

  it('returns null when input has only an unrecognized shape', () => {
    expect(acpFileEditFromToolCallRawInput('edit', {
      filePath: '/tmp/a.ts',
      somethingElse: 'value',
    })).toBeNull()
  })
})
