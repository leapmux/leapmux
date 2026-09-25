import { describe, expect, it } from 'vitest'
import { parseMiMoRead } from './read'

describe('parseMiMoRead', () => {
  it('reads a whole file with its end notice', () => {
    expect(parseMiMoRead('<path>/p/a.ts</path>\n<type>file</type>\n<content>\n1: alpha\n2: \n\n(End of file - total 2 lines)\n</content>')).toEqual({
      type: 'file',
      path: '/p/a.ts',
      lines: [{ num: 1, text: 'alpha' }, { num: 2, text: '' }],
      notice: 'End of file - total 2 lines',
      trailing: [],
    })
  })

  it('reads an empty file', () => {
    expect(parseMiMoRead('<path>/p/e.ts</path>\n<type>file</type>\n<content>\n\n\n(End of file - total 0 lines)\n</content>')).toMatchObject({ type: 'file', lines: [] })
  })

  it('keeps a line that reads like a notice when its number says it is file text', () => {
    const body = parseMiMoRead('<path>/p/a.ts</path>\n<type>file</type>\n<content>\n1: (not a notice)\n</content>')
    expect(body).toMatchObject({ lines: [{ num: 1, text: '(not a notice)' }] })
    expect(body && 'notice' in body ? body.notice : undefined).toBeUndefined()
  })

  it('reads the reminder MiMo appends after the body', () => {
    const body = parseMiMoRead('<path>/p/a.ts</path>\n<type>file</type>\n<content>\n1: a\n</content>\n\n<system-reminder>\nFollow AGENTS.md.\n</system-reminder>')
    expect(body).toMatchObject({ trailing: [{ label: 'System Reminder', text: 'Follow AGENTS.md.' }] })
  })

  it('reads a whole directory and a partial one', () => {
    expect(parseMiMoRead('<path>/p</path>\n<type>directory</type>\n<entries>\na.ts\nsub/\n\n(2 entries)\n</entries>'))
      .toEqual({ type: 'directory', path: '/p', entries: ['a.ts', 'sub/'], truncated: false })
    expect(parseMiMoRead('<path>/p</path>\n<type>directory</type>\n<entries>\nc.ts\n\n(Showing 1 of 9 entries. Use \'offset\' parameter to read beyond entry 3)\n</entries>'))
      .toEqual({ type: 'directory', path: '/p', entries: ['c.ts'], totalEntries: 9, offset: 2, truncated: true })
  })

  it('reads each reminder after the body, in order, with its label', () => {
    const body = parseMiMoRead('<path>/p/a.ts</path>\n<type>file</type>\n<content>\n1: a\n</content>\n<system-reminder>\nFirst.\n</system-reminder>\n\n<file_notes>\nSecond.\n</file_notes>')
    expect(body).toMatchObject({ trailing: [{ label: 'System Reminder', text: 'First.' }, { label: 'File Notes', text: 'Second.' }] })
  })

  it('reads a file with no notice', () => {
    expect(parseMiMoRead('<path>/p/a.ts</path>\n<type>file</type>\n<content>\n7: seven\n8: eight\n</content>')).toEqual({
      type: 'file',
      path: '/p/a.ts',
      lines: [{ num: 7, text: 'seven' }, { num: 8, text: 'eight' }],
      trailing: [],
    })
  })

  // A line that reads like a notice is one only at the end of the body. Anywhere else
  // it breaks the format, and the caller draws the text as it stands.
  it('refuses a parenthesized line inside the body', () => {
    expect(parseMiMoRead('<path>/p/a.ts</path>\n<type>file</type>\n<content>\n1: a\n(not a notice)\n2: b\n</content>')).toBeNull()
  })

  it('reads an empty directory', () => {
    expect(parseMiMoRead('<path>/p</path>\n<type>directory</type>\n<entries>\n\n(0 entries)\n</entries>')).toEqual({ type: 'directory', path: '/p', entries: [], truncated: false })
  })

  // The notice counts the entries from one. A page that starts with the first entry
  // states no offset, and a page that starts later states the entry it starts at.
  it('states the offset of a partial directory only when the page skipped entries', () => {
    expect(parseMiMoRead('<path>/p</path>\n<type>directory</type>\n<entries>\na.ts\nb.ts\n\n(Showing 2 of 9 entries. Use \'offset\' parameter to read beyond entry 3)\n</entries>'))
      .toEqual({ type: 'directory', path: '/p', entries: ['a.ts', 'b.ts'], totalEntries: 9, truncated: true })
    expect(parseMiMoRead('<path>/p</path>\n<type>directory</type>\n<entries>\ng.ts\n\n(Showing 1 of 9 entries. Use \'offset\' parameter to read beyond entry 8)\n</entries>'))
      .toEqual({ type: 'directory', path: '/p', entries: ['g.ts'], totalEntries: 9, offset: 7, truncated: true })
  })

  it.each([
    ['plain text', 'hello'],
    ['an empty output', ''],
    ['a missing type', '<path>/p</path>\n<content>\n1: a\n</content>'],
    ['a type this build does not know', '<path>/p</path>\n<type>symlink</type>\n<content>\n1: a\n</content>'],
    ['a file with no body tag', '<path>/p</path>\n<type>file</type>\n1: a'],
    ['an unnumbered body line', '<path>/p</path>\n<type>file</type>\n<content>\nplain\n</content>'],
    ['an unclosed body', '<path>/p</path>\n<type>file</type>\n<content>\n1: a'],
    ['text after the body', '<path>/p</path>\n<type>file</type>\n<content>\n1: a\n</content>\ntrailing words'],
    ['an unclosed reminder after the body', '<path>/p</path>\n<type>file</type>\n<content>\n1: a\n</content>\n<system-reminder>\nFollow AGENTS.md.'],
    ['a directory with no count', '<path>/p</path>\n<type>directory</type>\n<entries>\na.ts\n</entries>'],
    ['a directory with no body tag', '<path>/p</path>\n<type>directory</type>\na.ts\n\n(1 entries)'],
    ['an unclosed directory', '<path>/p</path>\n<type>directory</type>\n<entries>\na.ts\n\n(1 entries)'],
  ])('refuses %s', (_name, output) => {
    expect(parseMiMoRead(output)).toBeNull()
  })
})
