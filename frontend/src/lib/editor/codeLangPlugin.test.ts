import { describe, expect, it } from 'vitest'
import { createAutoDetectLanguageExtractor } from './codeLangPlugin'

const NBSP = String.fromCharCode(0xA0)
const LINE_SEPARATOR = String.fromCharCode(0x2028)

/**
 * Large enough that a scan quadratic in the run of blank lines runs for over a minute,
 * so the vitest timeout fails it; a linear scan takes a few milliseconds.
 */
const BLANK_LINE_COUNT = 50_000

function detect(code: string, language = ''): string | undefined {
  const node = {
    attrs: { language },
    textContent: code,
  }
  return createAutoDetectLanguageExtractor()(node)
}

describe('createAutoDetectLanguageExtractor', () => {
  it.each([
    ['typescript', 'import { x } from \'y\'\nconst a = 1'],
    ['javascript', 'function foo() {\n  return 1\n}'],
    ['python', 'def foo():\n    return 1'],
    ['rust', 'fn main() {\n    println!("hi");\n}'],
    ['go', 'package main\n\nfunc main() {}'],
    ['java', 'public class Foo {\n}'],
    ['cpp', '#include <stdio.h>\nint main() {}'],
    ['sql', 'select * from users where id = 1'],
    ['html', '<div class="a">hi</div>'],
    ['json', '{\n  "a": 1\n}'],
    ['yaml', 'server:\n  port: 80'],
    ['toml', '[package]\nname = "x"'],
    ['bash', '#!/bin/bash\necho hi'],
    ['css', '.foo {\n  color: red;\n}'],
    ['diff', 'diff --git a/x b/x\n--- a/x'],
  ])('detects %s', (language, code) => {
    expect(detect(code)).toBe(language)
  })

  it('passes an explicit language through', () => {
    expect(detect('def foo():\n    return 1', 'rust')).toBe('rust')
  })

  it('detects the language of a block labeled plaintext', () => {
    expect(detect('def foo():\n    return 1', 'plaintext')).toBe('python')
  })

  it('detects nothing in a block shorter than ten characters', () => {
    expect(detect('def f():')).toBeUndefined()
  })

  it('detects nothing in prose', () => {
    expect(detect('hello world, nothing to see here')).toBeUndefined()
  })

  describe('indentation', () => {
    it('finds a keyword after blank lines and indentation', () => {
      expect(detect('\n\n\t  def foo():\n')).toBe('python')
    })

    it('counts a no-break space as indentation', () => {
      expect(detect(`${NBSP}${NBSP}def foo():\n    return 1`)).toBe('python')
    })

    it('finds a keyword after CRLF line breaks', () => {
      expect(detect('x\r\n\r\n  def foo():\r\n')).toBe('python')
    })

    it('finds a keyword after a line separator', () => {
      expect(detect(`x = 1${LINE_SEPARATOR}def foo():`)).toBe('python')
    })

    it('finds no keyword that follows other text on its line', () => {
      expect(detect('the def foo(): keyword is mid-line')).toBeUndefined()
    })
  })

  describe('a long run of blank lines', () => {
    it('detects nothing in linear time', () => {
      expect(detect('    \n'.repeat(BLANK_LINE_COUNT))).toBeUndefined()
    })

    it('finds a keyword after the run', () => {
      expect(detect(`${'    \n'.repeat(BLANK_LINE_COUNT)}def foo():`)).toBe('python')
    })

    it('detects nothing in a run of line separators in linear time', () => {
      expect(detect(`${LINE_SEPARATOR}    `.repeat(BLANK_LINE_COUNT))).toBeUndefined()
    })
  })
})
