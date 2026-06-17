import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { bodyTextMetrics, countLines, firstToolResultBlockText, isToolResultError, messageContentBlocks, monoBody, monoReadBody, rowCarriesDiff, toLineLengths, toMarkdownLineLengths, toolResultBlockText } from './chatHeightShared'

function parsed(over: Partial<ParsedMessageContent>): ParsedMessageContent {
  return { rawText: '', topLevel: null, parentObject: undefined, wrapper: null, ...over }
}

describe('chatheightshared countLines', () => {
  it('counts hard lines, with 0 for empty', () => {
    expect(countLines('')).toBe(0)
    expect(countLines('a')).toBe(1)
    expect(countLines('a\nb\nc')).toBe(3)
    expect(countLines('a\n')).toBe(2) // trailing newline opens a (blank) second line
  })
})

describe('chatheightshared toLineLengths', () => {
  it('returns per-line char counts (blank lines = 0)', () => {
    expect(toLineLengths('')).toEqual([])
    expect(toLineLengths('ab\n\ncde')).toEqual([2, 0, 3])
  })

  it('folds a >MAX_LINE_SAMPLES tail into one trailing virtual line', () => {
    // 2001 single-char lines: head keeps 2000 entries, the rest folds into one.
    const text = Array.from({ length: 2001 }).fill('x').join('\n')
    const lens = toLineLengths(text)
    expect(lens.length).toBe(2001) // 2000 head entries + 1 fold entry
    // The fold entry carries the remaining chars (1 'x' + its leading newline).
    expect(lens[2000]).toBe(2)
  })

  it('emits a trailing blank line for a trailing newline (split parity)', () => {
    expect(toLineLengths('a\n')).toEqual([1, 0])
    expect(toLineLengths('a')).toEqual([1])
    expect(toLineLengths('\n')).toEqual([0, 0])
  })

  it('matches the reference split-based form across cap boundaries', () => {
    // Reference is the prior split('\n')-based implementation; the bounded scan
    // must produce byte-identical output without allocating the full split array.
    const ref = (text: string): number[] => {
      if (!text)
        return []
      const parts = text.split('\n')
      if (parts.length <= 2000)
        return parts.map(l => l.length)
      const head = parts.slice(0, 2000).map(l => l.length)
      let rest = 0
      for (let i = 2000; i < parts.length; i++)
        rest += parts[i].length + 1
      head.push(rest)
      return head
    }
    for (const lineCount of [1, 1999, 2000, 2001, 2500]) {
      // Vary line lengths so the fold sum exercises more than uniform widths.
      const text = Array.from({ length: lineCount }, (_, i) => 'y'.repeat(i % 7)).join('\n')
      expect(toLineLengths(text)).toEqual(ref(text))
    }
  })
})

describe('chatheightshared toMarkdownLineLengths', () => {
  it('matches toLineLengths for prose with no code fences', () => {
    expect(toMarkdownLineLengths('ab\n\ncde')).toEqual([2, 0, 3])
  })

  it('encodes fenced code lines (delimiters + body) as negative -(len+1)', () => {
    // a | ``` | const x = 1 | ``` | b  (the two fence delimiters are code rows too)
    expect(toMarkdownLineLengths('a\n```\nconst x = 1\n```\nb')).toEqual([1, -4, -12, -4, 1])
  })

  it('handles ~~~ fences and an info string on the opening fence', () => {
    expect(toMarkdownLineLengths('```json\n{}\n```')).toEqual([-8, -3, -4])
    expect(toMarkdownLineLengths('~~~\nx\n~~~')).toEqual([-4, -2, -4])
  })

  it('does not treat a 4-space-indented ``` as a fence (CommonMark indent rule)', () => {
    expect(toMarkdownLineLengths('    ```')).toEqual([7])
  })

  it('omits folded CODE chars from the >MAX_LINE_SAMPLES tail (no phantom prose wrap)', () => {
    // A fenced code block longer than the sample cap: every folded tail line is a code
    // line. Summing their chars into the trailing prose entry would char-wrap the whole
    // code tail into thousands of phantom rows. The fold entry must stay 0 (code lines
    // never wrap; the logicalLineCount floor charges them ~1 row each instead).
    const body = Array.from({ length: 2010 }, () => 'x'.repeat(100)).join('\n')
    const text = `\`\`\`\n${body}\n\`\`\``
    const lens = toMarkdownLineLengths(text)
    expect(lens.length).toBe(2001) // 2000 head entries + 1 fold entry
    // Head entries are all fenced code (negative); the folded tail is code-only -> 0.
    expect(lens.slice(0, 2000).every(v => v < 0)).toBe(true)
    expect(lens[2000]).toBe(0)
  })

  it('sums only folded PROSE chars, omitting interleaved folded code', () => {
    // 2000 prose head lines, then a fold mixing a fenced code block with prose. Only the
    // prose chars in the fold contribute to the trailing sum; the code lines do not.
    const head = Array.from({ length: 2000 }).fill('p').join('\n') // 2000 prose lines
    const foldTail = '\n```\ncode line that is long\n```\nq' // fence + 1 code line + fence + 'q'
    const lens = toMarkdownLineLengths(head + foldTail)
    expect(lens.length).toBe(2001)
    // Only the trailing 'q' (1 char) plus its leading newline survives; the fenced code
    // body and the two fence delimiters are omitted from the prose sum.
    expect(lens[2000]).toBe(2)
  })
})

describe('chatheightshared rowCarriesDiff', () => {
  it('is true when either diff-row field is present (including 0 rows)', () => {
    expect(rowCarriesDiff({ diffUnifiedRows: 5 })).toBe(true)
    expect(rowCarriesDiff({ diffSplitRows: 3 })).toBe(true)
    expect(rowCarriesDiff({ diffUnifiedRows: 5, diffSplitRows: 3 })).toBe(true)
    // 0 is a present count (an empty-but-real diff), not "absent" -- the != null
    // guard must keep it a diff row rather than falling through to plain text.
    expect(rowCarriesDiff({ diffUnifiedRows: 0 })).toBe(true)
    expect(rowCarriesDiff({ diffSplitRows: 0 })).toBe(true)
  })

  it('is false when neither diff-row field is present', () => {
    expect(rowCarriesDiff({})).toBe(false)
    expect(rowCarriesDiff({ diffUnifiedRows: undefined, diffSplitRows: undefined })).toBe(false)
  })
})

describe('chatheightshared bodyTextMetrics', () => {
  it('builds the {textLength, logicalLineCount, lineLengths} shape per mode', () => {
    // 'mono' uses toLineLengths, 'markdown' the fence-aware toMarkdownLineLengths;
    // both carry the same length + hard-line count.
    expect(bodyTextMetrics('ab\n\ncde', 'mono')).toEqual({ textLength: 7, logicalLineCount: 3, lineLengths: [2, 0, 3] })
    expect(bodyTextMetrics('ab\n\ncde', 'markdown')).toEqual({ textLength: 7, logicalLineCount: 3, lineLengths: [2, 0, 3] })
  })

  it('differs only in lineLengths for fenced code (mono char-counts, markdown marks code rows)', () => {
    const text = 'a\n```\nx\n```'
    expect(bodyTextMetrics(text, 'mono').lineLengths).toEqual(toLineLengths(text))
    expect(bodyTextMetrics(text, 'markdown').lineLengths).toEqual(toMarkdownLineLengths(text))
    // The two diverge: the fenced lines are negative (code rows) in markdown only.
    expect(bodyTextMetrics(text, 'mono').lineLengths).not.toEqual(bodyTextMetrics(text, 'markdown').lineLengths)
  })

  it('returns the empty shape for empty text', () => {
    expect(bodyTextMetrics('', 'markdown')).toEqual({ textLength: 0, logicalLineCount: 0, lineLengths: [] })
  })
})

describe('chatheightshared monoReadBody', () => {
  it('joins parsed cat-n lines with \\n and sizes them as a mono body', () => {
    const lines = [{ text: 'const a = 1' }, { text: '' }, { text: 'return a' }]
    // Equivalent to monoBody of the lines joined -- the rule the Claude and ACP read
    // hooks now share so it can't drift between them.
    expect(monoReadBody(lines)).toEqual(monoBody('const a = 1\n\nreturn a'))
  })

  it('returns the empty mono shape for no lines', () => {
    expect(monoReadBody([])).toEqual(monoBody(''))
  })
})

describe('chatheightshared toolResultBlockText', () => {
  it('joins an array-content tool_result block, returns a string block verbatim', () => {
    expect(toolResultBlockText({ type: 'tool_result', content: [{ type: 'text', text: 'hello' }, { type: 'text', text: 'world' }] })).toContain('hello')
    expect(toolResultBlockText({ type: 'tool_result', content: 'raw output' })).toBe('raw output')
  })

  it('returns "" for a non-tool_result block or an unrecognized inner shape', () => {
    expect(toolResultBlockText({ type: 'text', text: 'x' })).toBe('')
    expect(toolResultBlockText({ type: 'tool_result', content: 42 })).toBe('')
    expect(toolResultBlockText(null)).toBe('')
    expect(toolResultBlockText('not a block')).toBe('')
  })
})

describe('chatheightshared messageContentBlocks', () => {
  it('reads the parent object content when present', () => {
    const p = parsed({ parentObject: { message: { content: [{ type: 'text', text: 'hi' }] } } })
    expect(messageContentBlocks(p)).toEqual([{ type: 'text', text: 'hi' }])
  })

  it('falls back to the top-level object when the parent has none', () => {
    // A tool_result envelope that parsed with parentObject undefined but topLevel
    // carrying the blocks: extractText and countImages MUST read the same source.
    const p = parsed({ parentObject: undefined, topLevel: { message: { content: [{ type: 'text', text: 'tl' }] } } })
    expect(messageContentBlocks(p)).toEqual([{ type: 'text', text: 'tl' }])
  })

  it('returns null when neither carries content', () => {
    expect(messageContentBlocks(parsed({ parentObject: { foo: 1 }, topLevel: null }))).toBeNull()
  })
})

describe('chatheightshared firstToolResultBlockText', () => {
  it('returns the FIRST tool_result block text, skipping non-tool_result blocks', () => {
    const blocks = [
      { type: 'text', text: 'prose' },
      { type: 'tool_result', content: 'first result' },
      { type: 'tool_result', content: 'second result' },
    ] as never
    expect(firstToolResultBlockText(blocks)).toBe('first result')
  })

  it('joins an array-content tool_result block', () => {
    const blocks = [{ type: 'tool_result', content: [{ type: 'text', text: 'a' }, { type: 'text', text: 'b' }] }] as never
    expect(firstToolResultBlockText(blocks)).toContain('a')
  })

  it('returns "" when no block is a tool_result with a recognized inner shape', () => {
    expect(firstToolResultBlockText([{ type: 'text', text: 'x' }] as never)).toBe('')
    expect(firstToolResultBlockText([{ type: 'tool_result', content: 42 }] as never)).toBe('')
    expect(firstToolResultBlockText([])).toBe('')
  })
})

describe('chatheightshared isToolResultError', () => {
  it('detects an is_error tool_result block', () => {
    const p = parsed({ parentObject: { message: { content: [{ type: 'tool_result', is_error: true, content: 'boom' }] } } })
    expect(isToolResultError(p)).toBe(true)
  })

  it('is false for a successful result or a non-tool_result shape', () => {
    expect(isToolResultError(parsed({ parentObject: { message: { content: [{ type: 'tool_result', content: 'ok' }] } } }))).toBe(false)
    expect(isToolResultError(parsed({ parentObject: { foo: 1 } }))).toBe(false)
    expect(isToolResultError(parsed({}))).toBe(false)
  })
})
