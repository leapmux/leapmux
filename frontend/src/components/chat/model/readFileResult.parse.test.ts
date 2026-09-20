import { describe, expect, it } from 'vitest'
import { parseCatNContent, parseReadContent, readFileBodyText, readFileLines, readFileResultFromContent } from './readFileResult'

describe('parseCatNContent', () => {
  it('parses tab-delimited lines', () => {
    const result = parseCatNContent('  1\tfoo\n  2\tbar')
    expect(result).toEqual([
      { num: 1, text: 'foo' },
      { num: 2, text: 'bar' },
    ])
  })

  it('parses arrow-delimited lines', () => {
    const result = parseCatNContent('  1→foo\n  2→bar')
    expect(result).toEqual([
      { num: 1, text: 'foo' },
      { num: 2, text: 'bar' },
    ])
  })

  it('handles trailing empty line', () => {
    const result = parseCatNContent('  1\tfoo\n  2\tbar\n')
    expect(result).toEqual([
      { num: 1, text: 'foo' },
      { num: 2, text: 'bar' },
    ])
  })

  it('returns null for empty input', () => {
    expect(parseCatNContent('')).toBeNull()
  })

  it('returns null for invalid input', () => {
    expect(parseCatNContent('not a cat-n line')).toBeNull()
  })

  it('returns null when any line is invalid', () => {
    expect(parseCatNContent('  1\tfoo\ninvalid\n  3\tbaz')).toBeNull()
  })

  it('parses lines with no leading whitespace', () => {
    const result = parseCatNContent('1\tfoo\n2\tbar')
    expect(result).toEqual([
      { num: 1, text: 'foo' },
      { num: 2, text: 'bar' },
    ])
  })

  it('preserves content that contains tabs', () => {
    const result = parseCatNContent('  1\tfoo\tbar')
    expect(result).toEqual([
      { num: 1, text: 'foo\tbar' },
    ])
  })

  it('strips trailing [result-id: ...] metadata', () => {
    const result = parseCatNContent('1\tfoo\n2\tbar\n\n[result-id: r7]')
    expect(result).toEqual([
      { num: 1, text: 'foo' },
      { num: 2, text: 'bar' },
    ])
  })

  it('strips [result-id: ...] with only trailing newline', () => {
    const result = parseCatNContent('1\tfoo\n[result-id: abc123]\n')
    expect(result).toEqual([
      { num: 1, text: 'foo' },
    ])
  })

  it('strips trailing <system-reminder> block', () => {
    const result = parseCatNContent(
      '1\tfoo\n2\tbar\n\n<system-reminder>\nWhenever you read a file...\n</system-reminder>\n',
    )
    expect(result).toEqual([
      { num: 1, text: 'foo' },
      { num: 2, text: 'bar' },
    ])
  })

  it('strips multi-line <system-reminder> block with multiple body lines', () => {
    const result = parseCatNContent(
      '1\tfoo\n<system-reminder>\nline one\nline two\nline three\n</system-reminder>',
    )
    expect(result).toEqual([
      { num: 1, text: 'foo' },
    ])
  })

  it('strips <system-reminder> block followed by [result-id: ...]', () => {
    const result = parseCatNContent(
      '1\tfoo\n<system-reminder>\nreminder body\n</system-reminder>\n[result-id: r7]\n',
    )
    expect(result).toEqual([
      { num: 1, text: 'foo' },
    ])
  })

  it('strips multiple consecutive <system-reminder> blocks', () => {
    const result = parseCatNContent(
      '1\tfoo\n<system-reminder>\nfirst\n</system-reminder>\n<system-reminder>\nsecond\n</system-reminder>\n',
    )
    expect(result).toEqual([
      { num: 1, text: 'foo' },
    ])
  })

  it('returns null when </system-reminder> has no matching opening tag', () => {
    const result = parseCatNContent('1\tfoo\nstray text\n</system-reminder>\n')
    expect(result).toBeNull()
  })

  it('strips a LEADING single-line partial-view <system-reminder> (open+close on one line)', () => {
    // Claude Code PREPENDS this truncation notice to a partial Read; without
    // stripping it the cat-n parse fails and the renderer falls back to an
    // uncollapsible <pre> of the whole file (the massive estimate/measured delta).
    const result = parseCatNContent(
      '<system-reminder>[Truncated: PARTIAL view -- showing lines 1-2 of 9 total. Call Read with offset=3 for the next page.]</system-reminder>\n\n1\tfoo\n2\tbar',
    )
    expect(result).toEqual([
      { num: 1, text: 'foo' },
      { num: 2, text: 'bar' },
    ])
  })

  it('strips a LEADING multi-line <system-reminder> block before the cat-n body', () => {
    const result = parseCatNContent(
      '<system-reminder>\nPartial view, lines 1-2 of 9.\n</system-reminder>\n\n1\tfoo\n2\tbar',
    )
    expect(result).toEqual([
      { num: 1, text: 'foo' },
      { num: 2, text: 'bar' },
    ])
  })

  it('strips a LEADING reminder even with no blank line before the body', () => {
    const result = parseCatNContent('<system-reminder>[Truncated: PARTIAL view]</system-reminder>\n1\tfoo')
    expect(result).toEqual([{ num: 1, text: 'foo' }])
  })

  it('strips BOTH a leading partial-view reminder and a trailing reminder', () => {
    const result = parseCatNContent(
      '<system-reminder>[Truncated: PARTIAL view]</system-reminder>\n\n1\tfoo\n2\tbar\n\n<system-reminder>\ntrailing note\n</system-reminder>\n',
    )
    expect(result).toEqual([
      { num: 1, text: 'foo' },
      { num: 2, text: 'bar' },
    ])
  })

  it('returns null when a leading reminder leaves no cat-n body', () => {
    expect(parseCatNContent('<system-reminder>[Truncated: PARTIAL view]</system-reminder>\n')).toBeNull()
  })
})

describe('parseReadContent', () => {
  it('captures a leading single-line reminder (open+close on one line) alongside the body', () => {
    const r = parseReadContent(
      '<system-reminder>[Truncated: PARTIAL view -- lines 1-2 of 9]</system-reminder>\n\n1\tfoo\n2\tbar',
    )
    expect(r.leading).toEqual([
      { label: 'System Reminder', text: '[Truncated: PARTIAL view -- lines 1-2 of 9]', variant: undefined },
    ])
    expect(r.lines).toEqual([{ num: 1, text: 'foo' }, { num: 2, text: 'bar' }])
    expect(r.trailing).toEqual([])
  })

  it('captures a multi-line reminder, joining its body lines', () => {
    const r = parseReadContent('1\tfoo\n<system-reminder>\nline one\nline two\n</system-reminder>')
    expect(r.lines).toEqual([{ num: 1, text: 'foo' }])
    expect(r.trailing).toEqual([{ label: 'System Reminder', text: 'line one\nline two', variant: undefined }])
  })

  it('captures multiple leading and multiple trailing reminders in document order', () => {
    const r = parseReadContent(
      '<a-note>first</a-note>\n<b-note>second</b-note>\n\n1\tfoo\n\n<c-note>third</c-note>\n<d-note>fourth</d-note>\n',
    )
    expect(r.leading.map(x => x.text)).toEqual(['first', 'second'])
    expect(r.lines).toEqual([{ num: 1, text: 'foo' }])
    expect(r.trailing.map(x => x.text)).toEqual(['third', 'fourth'])
  })

  it('title-cases kebab and camelCase tag names into labels', () => {
    expect(parseReadContent('<system-reminder>x</system-reminder>\n1\ta').leading[0]?.label).toBe('System Reminder')
    expect(parseReadContent('<otherTag>x</otherTag>\n1\ta').leading[0]?.label).toBe('Other Tag')
  })

  it('infers the severity from words in the tag name', () => {
    const severity = (tag: string) => parseReadContent(`<${tag}>x</${tag}>\n1\ta`).leading[0]?.severity
    expect(severity('read-error')).toBe('error')
    expect(severity('save-success')).toBe('success')
    expect(severity('is-danger')).toBe('danger')
    expect(severity('warn-note')).toBe('warning')
    expect(severity('system-reminder')).toBeUndefined()
  })

  it('discards trailing [result-id: ...] metadata (not a tag, not a reminder)', () => {
    const r = parseReadContent('1\tfoo\n\n[result-id: r7]\n')
    expect(r.lines).toEqual([{ num: 1, text: 'foo' }])
    expect(r.trailing).toEqual([])
  })

  it('does not mistake a cat-n code line containing tags for a reminder block', () => {
    // A real file line like `5\t</div>` starts with its line number, so the
    // ^<-anchored matchers never treat it as a tag block.
    const r = parseReadContent('1\t<div>\n2\t</div>')
    expect(r.leading).toEqual([])
    expect(r.trailing).toEqual([])
    expect(r.lines).toEqual([{ num: 1, text: '<div>' }, { num: 2, text: '</div>' }])
  })
})

/**
 * The peel is for the wrapper AROUND a cat-n body, and the post-condition is what
 * keeps it there.
 *
 * The six Agent Client Protocol providers feed RAW file text through this parser, and
 * a raw body carries no line numbers -- so the `^<` anchor that protects a cat-n line
 * protects nothing here. `</html>` as the last line matches the close matcher, which
 * then scans BACKWARD to the file's own `<html>` and collapses the whole document
 * into one alert beside the `fallbackContent` that already drew it.
 */
describe('parseReadContent over a raw file body', () => {
  it('peels nothing from a plain HTML file, so the row draws the file once', () => {
    const r = parseReadContent('<html>\nhi\n</html>')
    expect(r.leading).toEqual([])
    expect(r.trailing).toEqual([])
    expect(r.lines).toBeNull()
  })

  it('peels nothing when a single-line element ENDS the body', () => {
    // `<p>hi</p>` matches the single-line tag form with no backward scan at all.
    const r = parseReadContent('plain first line\n<p>hi</p>')
    expect(r.leading).toEqual([])
    expect(r.trailing).toEqual([])
    expect(r.lines).toBeNull()
  })

  it('peels nothing when a single-line element OPENS the body', () => {
    const r = parseReadContent('<title>hi</title>\nplain last line')
    expect(r.leading).toEqual([])
    expect(r.trailing).toEqual([])
    expect(r.lines).toBeNull()
  })

  // The cost the rule states, recorded so a reader does not take it for a bug: a tag
  // block and NOTHING else reaches the reader as raw text. No shape tells a
  // truncation notice apart from a three-line HTML file, so one rule answers both.
  it('peels nothing when the block is the WHOLE output', () => {
    const r = parseReadContent('<system-reminder>[Truncated: PARTIAL view]</system-reminder>')
    expect(r.leading).toEqual([])
    expect(r.trailing).toEqual([])
    expect(r.lines).toBeNull()
  })

  it('still peels the blocks around a cat-n body whose own lines carry tags', () => {
    const r = parseReadContent('<system-reminder>a notice</system-reminder>\n1\t<html>\n2\t</html>')
    expect(r.leading.map(x => x.text)).toEqual(['a notice'])
    expect(r.lines).toEqual([{ num: 1, text: '<html>' }, { num: 2, text: '</html>' }])
  })
})

/**
 * ONE predicate for "are there drawable lines", because `[]` is truthy and
 * {@link readFileResultFromContent} builds `[]` on purpose for a file that WAS read
 * and holds nothing.
 */
describe('readFileLines', () => {
  it('answers null for an absent list', () => {
    expect(readFileLines({ lines: null, fallbackContent: 'the read was refused' })).toBeNull()
  })

  it('answers null for the EMPTY list an empty read produces', () => {
    const source = readFileResultFromContent({ content: '', fallbackContent: 'the read was refused' })
    expect(source.lines).toEqual([])
    expect(readFileLines(source)).toBeNull()
  })

  it('answers the list when the read produced lines', () => {
    expect(readFileLines({ lines: [{ num: 1, text: 'alpha' }], fallbackContent: 'raw' }))
      .toEqual([{ num: 1, text: 'alpha' }])
  })
})

describe('readFileBodyText', () => {
  it('yields to the fallback for the EMPTY list, so a refused read stays copyable', () => {
    expect(readFileBodyText(readFileResultFromContent({ content: '', fallbackContent: 'the read was refused' })))
      .toBe('the read was refused')
  })

  it('strips the wire line numbers by drawing the parsed lines', () => {
    expect(readFileBodyText({ lines: [{ num: 4, text: 'alpha' }, { num: 5, text: 'beta' }], fallbackContent: '4\talpha\n5\tbeta' }))
      .toBe('alpha\nbeta')
  })
})
