import { describe, expect, it } from 'vitest'
import { classSelector } from './composedClass'

describe('classSelector', () => {
  it('builds a plain selector for one class', () => {
    expect(classSelector('itemTitle_abc')).toBe('.itemTitle_abc')
  })

  // The case this helper exists for: a composed style exports two names, and
  // `.${style}` would be a descendant selector that matches nothing.
  it('joins a composed class list into one compound selector', () => {
    expect(classSelector('taskTitle_abc clippedText_def'))
      .toBe('.taskTitle_abc.clippedText_def')
  })

  it('ignores the surrounding and repeated whitespace a class list can carry', () => {
    expect(classSelector('  a_1   b_2  ')).toBe('.a_1.b_2')
  })

  // `''.split(/\s+/)` gives `['']`, which builds the selector `.` --
  // `querySelector` then raises a SyntaxError that names neither this helper
  // nor its caller.
  it('throws a named error for an empty class name', () => {
    expect(() => classSelector('')).toThrow('classSelector: the class name is empty')
    expect(() => classSelector('   ')).toThrow('classSelector: the class name is empty')
  })
})
