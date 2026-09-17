import { describe, expect, it } from 'vitest'
import { acpFileEditFromToolCallContent, acpFileEditFromToolCallRawInput, acpFileEditsFromToolCallRawInput } from './fileEdit'

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

  it('skips empty diff entries and takes the next non-empty one', () => {
    expect(acpFileEditFromToolCallContent([
      { type: 'diff' },
      { type: 'diff', path: '/x.ts', oldText: 'a', newText: 'b' },
    ])).toEqual({
      filePath: '/x.ts',
      structuredPatch: null,
      oldStr: 'a',
      newStr: 'b',
    })
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

// One call can ask for several substitutions in one file, and no single pair describes
// a list of them. Three agents in this repository send that list under `edits`, and the
// reader that took one root pair alone stated NOTHING for it -- so a `multi_edit` opened
// with an empty change list and a header that could name no file.
describe('acpFileEditsFromToolCallRawInput', () => {
  it('states one change for each entry of an `edits` list', () => {
    expect(acpFileEditsFromToolCallRawInput('edit', {
      path: '/p/file.ts',
      edits: [{ old_string: 'firstBefore', new_string: 'firstAfter' }, { old_string: 'secondBefore', new_string: 'secondAfter' }],
    })).toStrictEqual([
      { filePath: '/p/file.ts', structuredPatch: null, oldStr: 'firstBefore', newStr: 'firstAfter' },
      { filePath: '/p/file.ts', structuredPatch: null, oldStr: 'secondBefore', newStr: 'secondAfter' },
    ])
  })

  // The FILE stays at the root in every agent that sends the list, and a listed entry
  // spells its two sides under the same keys a root pair uses.
  it.each([
    ['oldText', 'newText'],
    ['oldString', 'newString'],
    ['old_string', 'new_string'],
  ])('reads a listed entry spelled %s and %s', (from, to) => {
    expect(acpFileEditsFromToolCallRawInput('edit', { filePath: '/p/a.ts', edits: [{ [from]: 'before', [to]: 'after' }] }))
      .toStrictEqual([{ filePath: '/p/a.ts', structuredPatch: null, oldStr: 'before', newStr: 'after' }])
  })

  it('states half a listed entry when the entry states one side alone', () => {
    expect(acpFileEditsFromToolCallRawInput('edit', { path: '/p/a.ts', edits: [{ newText: 'only the new side' }] }))
      .toStrictEqual([{ filePath: '/p/a.ts', structuredPatch: null, oldStr: '', newStr: 'only the new side' }])
  })

  // An entry that states neither side describes no change at all, and a non-object
  // entry describes nothing this reader can read.
  it('skips a listed entry that states neither side', () => {
    expect(acpFileEditsFromToolCallRawInput('edit', {
      path: '/p/a.ts',
      edits: ['a string', null, { note: 'no sides here' }, { oldText: 'kept', newText: 'also kept' }],
    })).toStrictEqual([{ filePath: '/p/a.ts', structuredPatch: null, oldStr: 'kept', newStr: 'also kept' }])
  })

  // Pi normalizes its own arguments into exactly this order, so a call that sends both
  // draws them in it.
  it('states the listed substitutions before a root pair', () => {
    expect(acpFileEditsFromToolCallRawInput('edit', {
      path: '/p/a.ts',
      edits: [{ oldText: 'in the list', newText: 'first' }],
      oldText: 'at the root',
      newText: 'second',
    }).map(change => change.oldStr)).toStrictEqual(['in the list', 'at the root'])
  })

  // The write body answers only where neither the list nor the root pair stated a
  // change, so a call that carries both is read as the edit it is.
  it('reads the write body only when nothing else stated a change', () => {
    expect(acpFileEditsFromToolCallRawInput('write', { path: '/p/a.ts', content: 'package main\n' }))
      .toStrictEqual([{ filePath: '/p/a.ts', structuredPatch: null, oldStr: '', newStr: 'package main\n' }])
    expect(acpFileEditsFromToolCallRawInput('edit', {
      path: '/p/a.ts',
      edits: [{ oldText: 'before', newText: 'after' }],
      content: 'never read',
    })).toStrictEqual([{ filePath: '/p/a.ts', structuredPatch: null, oldStr: 'before', newStr: 'after' }])
  })

  it('states nothing for an input that carries no file, no list and no shape', () => {
    expect(acpFileEditsFromToolCallRawInput('edit', null)).toStrictEqual([])
    expect(acpFileEditsFromToolCallRawInput('edit', { edits: [{ oldText: 'a', newText: 'b' }] })).toStrictEqual([])
    expect(acpFileEditsFromToolCallRawInput('edit', { path: '/p/a.ts', edits: 'not a list' })).toStrictEqual([])
    expect(acpFileEditsFromToolCallRawInput('edit', { path: '/p/a.ts', somethingElse: 'value' })).toStrictEqual([])
  })
})

describe('acpFileEditFromToolCallRawInput', () => {
  // The single reader is the list reader's first entry, so a caller that draws one
  // change draws the first substitution rather than nothing.
  it('answers the first entry of an `edits` list', () => {
    expect(acpFileEditFromToolCallRawInput('edit', {
      path: '/p/a.ts',
      edits: [{ oldText: 'first', newText: 'one' }, { oldText: 'second', newText: 'two' }],
    })).toStrictEqual({ filePath: '/p/a.ts', structuredPatch: null, oldStr: 'first', newStr: 'one' })
  })

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
