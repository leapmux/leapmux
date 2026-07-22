import type { Root } from 'mdast'
import { describe, expect, it } from 'vitest'
import { createMarkdownParser } from './markdownParse'
import { __setGraphemeSegmenterForTest, truncatePreview } from './textTruncate'

const reparser = createMarkdownParser()

describe('truncate preview', () => {
  it('tidies horizontal whitespace but preserves newlines (for markdown structure)', () => {
    expect(truncatePreview('  a\n\n  b\t c  ')).toBe('a\n\nb c')
    expect(truncatePreview('one\n\n\n\ntwo')).toBe('one\n\ntwo') // blank-line runs cap at one
  })

  it('preserves bare \\r (classic-Mac) and \\r\\n line endings as newlines, not spaces', () => {
    expect(truncatePreview('a\rb')).toBe('a\nb') // a bare CR is a line break, not a space
    expect(truncatePreview('a\r\nb')).toBe('a\nb') // a CRLF grapheme is one newline
  })

  it('coalesces CRLF to a single newline on the no-Intl.Segmenter fallback path (not a paragraph break)', () => {
    // Force the code-point fallback (older engines / SSR). Without CRLF coalescing there, the
    // '\r' and '\n' each hit the newline branch and one CRLF double-counts into a paragraph
    // break, diverging from the Segmenter path asserted above.
    __setGraphemeSegmenterForTest(null)
    try {
      expect(truncatePreview('a\r\nb')).toBe('a\nb')
      expect(truncatePreview('a\rb')).toBe('a\nb') // a bare CR still one newline on the fallback
    }
    finally {
      __setGraphemeSegmenterForTest(undefined) // re-resolve the real segmenter for later tests
    }
  })

  it('bounds the graphemes scanned on a whitespace-dominated input (does not segment the whole prefix)', () => {
    // Whitespace graphemes never append, so the content cap can't stop the loop; the scan cap
    // must. A counting segmenter proves the loop stops near MAX_PREVIEW_SCAN, not at 1_000_004.
    let pulled = 0
    __setGraphemeSegmenterForTest({
      segment: (input: string) => ({
        * [Symbol.iterator]() {
          for (const ch of input) {
            pulled++
            yield { segment: ch }
          }
        },
      }),
    })
    try {
      truncatePreview(`${' '.repeat(1_000_000)}tail`)
      expect(pulled).toBeLessThan(5000) // ~MAX_PREVIEW_SCAN (4000), not the full 1_000_004
    }
    finally {
      __setGraphemeSegmenterForTest(undefined)
    }
  })

  it('preserves content that follows a whitespace run within the scan bound', () => {
    // Guards the scan bound against over-truncation: a moderate leading-whitespace run must not
    // drop the content that follows it.
    expect(truncatePreview(`${' '.repeat(50)}hello`)).toBe('hello')
  })

  it('marks the result truncated (trailing ellipsis) when the scan cap drops trailing content', () => {
    // Content, then a huge whitespace run that exhausts the scan cap BEFORE the trailing "world"
    // is reached -- so "world" is dropped. The result must carry the ellipsis affordance, not
    // silently render "hello" as if it were the whole message.
    expect(truncatePreview(`hello${' '.repeat(5000)}world`)).toBe('hello…')
  })

  it('returns null for empty / whitespace-only / nullish input', () => {
    expect(truncatePreview('')).toBeNull()
    expect(truncatePreview('   \n ')).toBeNull()
    expect(truncatePreview(undefined)).toBeNull()
    expect(truncatePreview(null)).toBeNull()
  })

  it('caps overlong text at the limit with a trailing ellipsis', () => {
    const out = truncatePreview('x'.repeat(500))!
    expect(out.endsWith('…')).toBe(true)
    expect(out.length).toBe(201) // 200 kept + the ellipsis
  })

  it('does not split a surrogate pair at the truncation boundary', () => {
    expect(truncatePreview(`${'x'.repeat(199)}😀tail`)).toBe(`${'x'.repeat(199)}😀…`)
  })

  it('does not split a combining-character grapheme at the truncation boundary', () => {
    expect(truncatePreview(`${'x'.repeat(199)}e\u0301tail`)).toBe(`${'x'.repeat(199)}e\u0301…`)
  })

  it('closes bold that straddles the limit (no dangling **)', () => {
    // 200-grapheme budget = `**` opener + 198 a's; the closer is read from the source.
    expect(truncatePreview(`**${'a'.repeat(250)}**`)).toBe(`**${'a'.repeat(198)}…**`)
  })

  it('closes a fenced code block that straddles the limit', () => {
    // 200-grapheme budget = fence line (4 graphemes incl. newline) + 196 x's;
    // the synthesized closing fence follows the in-code ellipsis.
    expect(truncatePreview(`\`\`\`\n${'x'.repeat(250)}\n\`\`\``))
      .toBe(`\`\`\`\n${'x'.repeat(196)}…\n\`\`\``)
  })

  it('cuts before a late link that straddles the limit', () => {
    expect(truncatePreview(`${'a'.repeat(180)} [label](https://example.com/path) more`))
      .toBe(`${'a'.repeat(180)}…`)
  })

  it('keeps an html entity whole at the truncation boundary', () => {
    // The 200-grapheme limit lands between `&` and `amp;`; the cut snaps past
    // the entity instead of splitting it into a literal `&a` fragment.
    expect(truncatePreview(`${'x'.repeat(198)}&amp;${'y'.repeat(30)}`))
      .toBe(`${'x'.repeat(198)}&amp;…`)
  })

  it('rewrites a link reference whose definition is truncated away', () => {
    const out = truncatePreview(`See [the docs][ref] ${'w'.repeat(250)}\n\n[ref]: https://example.com/docs`)!
    expect(out.startsWith('See the docs ')).toBe(true)
    expect(out.includes('[')).toBe(false)
  })

  it('truncates a long blockquote fenced code block to a single closed code block', () => {
    // The `> ` markers survive tidying, so the cut lands inside a blockquoted
    // fence; the synthesized closer (and any boundary ellipsis line) must keep the
    // `> ` prefix so the result re-parses as one blockquote, not a broken quote +
    // stray paragraph + second fence.
    const body = `> \`\`\`\n${Array.from({ length: 30 }, (_, i) => `> code content line number ${i}`).join('\n')}\n> \`\`\``
    const out = truncatePreview(body)!
    expect(out.endsWith('…') || out.endsWith('```')).toBe(true)
    const tree = reparser.parse(out) as Root
    expect(tree.children.map(c => c.type)).toEqual(['blockquote'])
  })

  it('previews the prose past a preview-filling dangling image reference', () => {
    // The first 200 tidied graphemes are one giant dangling image reference;
    // the rewrite empties the prefix, and the preview must surface the prose
    // that follows instead of a wall of literal reference markup.
    const body = `![${'a'.repeat(190)}][ref]\n\nNext paragraph of prose content here\n\n[ref]: https://example.com/image.png`
    const out = truncatePreview(body)!
    expect(out.startsWith('Next paragraph of prose content here')).toBe(true)
    expect(out.includes('[')).toBe(false)
  })

  it('survives pathologically deep blockquote nesting without a stack overflow', () => {
    // 5000 `>` markers tidy to a 4000-grapheme run that parses into ~4000
    // nested blockquotes; the recursive AST walks must degrade to the bounded
    // hard cut instead of throwing RangeError through the preview pipeline.
    const out = truncatePreview(`${'>'.repeat(5000)} hello`)!
    expect(out.endsWith('…')).toBe(true)
    expect(out.length).toBeLessThanOrEqual(250)
  })

  it('does not append an ellipsis for exactly-limit content followed by a trailing newline', () => {
    // Latent-bug fix: trailing-whitespace pop used to leave truncated=true from the
    // newline append even though the kept content is exactly MAX_PREVIEW_LEN.
    const out = truncatePreview(`${'b'.repeat(200)}\n`)!
    expect(out).toBe('b'.repeat(200))
    expect(out.endsWith('…')).toBe(false)
  })

  it('composes grapheme safety with markdown delimiter closing around an emoji', () => {
    // The 200th grapheme is the first `*` of the closing run, so the code-unit
    // limit lands inside it; the cut clamps back to the bold content's end
    // (keeping the surrogate-pair emoji whole) and closes with …**.
    expect(truncatePreview(`**${'c'.repeat(196)}😀**tail`)).toBe(`**${'c'.repeat(196)}😀…**`)
  })
})
