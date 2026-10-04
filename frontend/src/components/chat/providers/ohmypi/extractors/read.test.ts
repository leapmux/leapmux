import { describe, expect, it } from 'vitest'
import { ohMyPiReadIsUrl, ohMyPiReadResult } from './read'

/**
 * One structural summary of a 120-line `big.ts`, as omp 18.2.11 states it
 * (`formatReadSummary`). Lines 4 to 39 are one elided span, and lines 42 to 120 are
 * a brace pair that omp folds into one row.
 */
const SUMMARY_DISPLAY = [
  'import { a } from \'./a\'',
  '',
  '// the first helper',
  '…',
  '}',
  '',
  'export function second() { … }',
].join('\n')
const SUMMARY_FOOTER = '[…113ln elided; re-read needed ranges with big.ts:4-39,42-120]'
const SUMMARY_DETAILS = {
  displayContent: { text: SUMMARY_DISPLAY, startLine: 1 },
  summary: { lines: 7, elidedSpans: 2, elidedLines: 113 },
}

describe('ohMyPiReadResult', () => {
  it('reads the plain text omp states in its details', () => {
    // omp 18.2.11's own result with the `replace` edit: the bare file text.
    const result = ohMyPiReadResult('alpha one\nbeta two', { displayContent: { text: 'alpha one\nbeta two', startLine: 1, lineNumbers: [1, 2] } })
    expect(result.lines).toEqual([{ num: 1, text: 'alpha one' }, { num: 2, text: 'beta two' }])
  })

  it('reads the numbers of each line from the list omp states', () => {
    // A ranged read of lines 50 and 51 inside a function that starts at line 10:
    // omp adds the enclosing header and a gap row, and states the real numbers.
    const result = ohMyPiReadResult('[a.ts#0000]\n10:function f() {\n…\n50:  const x = 1\n51:  return x', {
      displayContent: { text: 'function f() {\n…\n  const x = 1\n  return x', startLine: 10, lineNumbers: [10, null, 50, 51] },
    })
    // The gap row stays, with no number, so the reader sees where lines are left out.
    expect(result.lines).toEqual([
      { num: 10, text: 'function f() {' },
      { num: null, text: '…' },
      { num: 50, text: '  const x = 1' },
      { num: 51, text: '  return x' },
    ])
  })

  it('numbers the lines of a partial read from its first line when omp lists no numbers', () => {
    const result = ohMyPiReadResult('[a.ts#0000]\n50:x\n51:y', { displayContent: { text: 'x\ny', startLine: 50 } })
    expect(result.lines).toEqual([{ num: 50, text: 'x' }, { num: 51, text: 'y' }])
  })

  it('draws the text without numbers when the list of numbers does not match the text', () => {
    const tooShort = ohMyPiReadResult('x\ny', { displayContent: { text: 'x\ny', startLine: 1, lineNumbers: [1] } })
    expect(tooShort).toEqual({ lines: null, fallbackContent: 'x\ny' })
    const notANumber = ohMyPiReadResult('x\ny', { displayContent: { text: 'x\ny', startLine: 1, lineNumbers: [1, 'two'] } })
    expect(notANumber.lines).toBeNull()
    const zero = ohMyPiReadResult('x', { displayContent: { text: 'x', startLine: 1, lineNumbers: [0] } })
    expect(zero.lines).toBeNull()
  })

  it('reads the numbers of a summary from the rows omp numbers in hashline mode', () => {
    const text = [
      '[big.ts#1A2B]',
      '1:import { a } from \'./a\'',
      '2:',
      '3:// the first helper',
      '…',
      '40:}',
      '41:',
      '42-120:export function second() { … }',
      '',
      SUMMARY_FOOTER,
    ].join('\n')
    const result = ohMyPiReadResult(text, SUMMARY_DETAILS)
    // Each row keeps its real number, and the elision row, which has none, marks the
    // elided span. A folded brace pair takes the number of its first line.
    expect(result.lines).toEqual([
      { num: 1, text: 'import { a } from \'./a\'' },
      { num: 2, text: '' },
      { num: 3, text: '// the first helper' },
      { num: null, text: '…' },
      { num: 40, text: '}' },
      { num: 41, text: '' },
      { num: 42, text: 'export function second() { … }' },
    ])
    expect(result.trailing).toEqual([{ label: 'Notice', text: '…113ln elided; re-read needed ranges with big.ts:4-39,42-120' }])
  })

  it('reads the numbers of a summary from the rows omp numbers with `readLineNumbers`', () => {
    const text = ['1|import { a } from \'./a\'', '2|', '3|// the first helper', '…', '40|}', '41|', '42-120|export function second() { … }', '', SUMMARY_FOOTER].join('\n')
    expect(ohMyPiReadResult(text, SUMMARY_DETAILS).lines?.map(line => line.num)).toEqual([1, 2, 3, null, 40, 41, 42])
  })

  it('draws a summary without numbers when omp numbers no row', () => {
    // The plain modes with `readLineNumbers` off: the text holds no number, and no
    // field of the details states one.
    const result = ohMyPiReadResult(`${SUMMARY_DISPLAY}\n\n${SUMMARY_FOOTER}`, SUMMARY_DETAILS)
    expect(result.lines).toBeNull()
    expect(result.fallbackContent).toBe(SUMMARY_DISPLAY)
    expect(result.trailing).toEqual([{ label: 'Notice', text: '…113ln elided; re-read needed ranges with big.ts:4-39,42-120' }])
  })

  it('draws a summary without numbers when a numbered row is not the row it draws', () => {
    // A file whose own lines look numbered: `12:` is the file's text, not omp's prefix.
    const display = '12:ports\n…\n13:hosts'
    const result = ohMyPiReadResult(`${display}\n\n[…3ln elided; re-read needed ranges with c.yml:2-4]`, { displayContent: { text: display, startLine: 1 }, summary: { lines: 3 } })
    expect(result.lines).toBeNull()
    expect(result.fallbackContent).toBe(display)
  })

  it('reads the numbered text when the details state no plain text', () => {
    const result = ohMyPiReadResult('[a.ts#1A2B]\n1:first\n2:second: with a colon\n\n[Showing lines 1-2 of 90. Use :3 to continue]', {})
    expect(result.lines).toEqual([{ num: 1, text: 'first' }, { num: 2, text: 'second: with a colon' }])
    expect(result.trailing).toEqual([{ label: 'Notice', text: 'Showing lines 1-2 of 90. Use :3 to continue' }])
  })

  it('keeps an elision row of the numbered text, with no number', () => {
    const result = ohMyPiReadResult('[a.ts#1A2B]\n10:function f() {\n…\n50:  return x', {})
    expect(result.lines).toEqual([{ num: 10, text: 'function f() {' }, { num: null, text: '…' }, { num: 50, text: '  return x' }])
  })

  it('draws a text of elision rows alone as it is', () => {
    expect(ohMyPiReadResult('…', {})).toEqual({ lines: null, fallbackContent: '…' })
  })

  it('reads the `N|text` rows omp prints with `readLineNumbers` when the details state no plain text', () => {
    expect(ohMyPiReadResult('7|alpha\n8|beta | gamma', {}).lines).toEqual([{ num: 7, text: 'alpha' }, { num: 8, text: 'beta | gamma' }])
  })

  it('keeps a notice beside the plain text', () => {
    const result = ohMyPiReadResult('[a.ts#1A2B]\n1:first\n[Truncated at 2000 lines]', { displayContent: { text: 'first', startLine: 1 } })
    expect(result.trailing).toEqual([{ label: 'Notice', text: 'Truncated at 2000 lines' }])
  })

  it('reads a last line that the file itself holds as a line, not as a notice', () => {
    const file = 'notes\n[Output truncated by the tool]'
    const result = ohMyPiReadResult(file, { displayContent: { text: file, startLine: 1 } })
    expect(result.trailing).toBeUndefined()
    expect(result.lines).toEqual([{ num: 1, text: 'notes' }, { num: 2, text: '[Output truncated by the tool]' }])
  })

  it('draws a text in another shape as it is', () => {
    const tree = 'src/\n├── a.ts\n└── b.ts'
    expect(ohMyPiReadResult(tree, { isDirectory: true })).toEqual({ lines: null, fallbackContent: tree })
  })

  it('reads a text with Windows line endings', () => {
    expect(ohMyPiReadResult('[a.ts#1A2B]\r\n1:first\r\n2:second\r\n', {}).lines).toEqual([{ num: 1, text: 'first' }, { num: 2, text: 'second' }])
    expect(ohMyPiReadResult('x\r\ny', { displayContent: { text: 'x\r\ny', startLine: 3 } }).lines).toEqual([{ num: 3, text: 'x' }, { num: 4, text: 'y' }])
  })

  it('draws the text without numbers when the list of numbers is not a list, or holds a number that is not a line', () => {
    for (const lineNumbers of [null, 'all', [1.5], [-2], [Number.MAX_SAFE_INTEGER + 2]])
      expect(ohMyPiReadResult('x', { displayContent: { text: 'x', startLine: 1, lineNumbers } }).lines, JSON.stringify(lineNumbers)).toBeNull()
  })

  it('numbers a very large file from its first line', () => {
    const result = ohMyPiReadResult('x', { displayContent: { text: 'x', startLine: 1_000_000 } })
    expect(result.lines).toEqual([{ num: 1_000_000, text: 'x' }])
  })

  it('reads the numbered text when the plain text in the details is not text', () => {
    expect(ohMyPiReadResult('1:first\n2:second', { displayContent: { text: 7, startLine: 1 } }).lines).toEqual([{ num: 1, text: 'first' }, { num: 2, text: 'second' }])
  })

  it('draws a summary without numbers when omp printed fewer rows than the summary holds', () => {
    const result = ohMyPiReadResult('1:a', { displayContent: { text: 'a\n…\nb', startLine: 1 }, summary: { lines: 2 } })
    expect(result).toEqual({ lines: null, fallbackContent: 'a\n…\nb' })
  })

  it('draws a numbered row whose text the file itself prints as a notice as a line, when omp states no plain text', () => {
    // A trailing notice is a notice only in brackets that omp writes; any other last
    // row is output.
    const result = ohMyPiReadResult('1:first\n2:[Showing is a word here]', {})
    expect(result.lines).toEqual([{ num: 1, text: 'first' }, { num: 2, text: '[Showing is a word here]' }])
    expect(result.trailing).toBeUndefined()
  })

  it('reads an empty file as zero lines', () => {
    expect(ohMyPiReadResult('[e.txt#0000]', { displayContent: { text: '', startLine: 1, lineNumbers: [] } }).lines).toEqual([])
    expect(ohMyPiReadResult('', { displayContent: { text: '', startLine: 1 } }).lines).toEqual([])
  })
})

describe('ohMyPiReadIsUrl', () => {
  it('reads the url kind', () => {
    expect(ohMyPiReadIsUrl({ kind: 'url', url: 'https://example.com' }, 'url')).toBe(true)
    expect(ohMyPiReadIsUrl({ kind: 'file' }, 'url')).toBe(false)
    expect(ohMyPiReadIsUrl({}, 'url')).toBe(false)
  })
})
