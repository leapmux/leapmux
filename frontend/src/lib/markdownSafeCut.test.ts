import type { Blockquote, Code, Root } from 'mdast'
import { describe, expect, it } from 'vitest'
import { createMarkdownParser } from './markdownParse'
import { markdownSafeCut, PREVIEW_ELLIPSIS } from './markdownSafeCut'

// Re-parse a cut result with the SAME parser markdownSafeCut uses, to assert the
// output actually re-parses to the intended structure -- the core guarantee, which
// exact-string assertions alone do not prove.
const reparser = createMarkdownParser()
function reparse(md: string): Root {
  return reparser.parse(md) as Root
}

describe('markdownSafeCut', () => {
  describe('clean cuts', () => {
    it('cuts plain text mid-word at limitOffset', () => {
      expect(markdownSafeCut('hello world', 8)).toBe(`hello wo${PREVIEW_ELLIPSIS}`)
    })

    it('trims trailing spaces from the chosen prefix', () => {
      expect(markdownSafeCut('hello world   ', 8)).toBe(`hello wo${PREVIEW_ELLIPSIS}`)
      // Candidate at a word end with spaces before limit: trimEnd eats them.
      expect(markdownSafeCut('hello     world', 10)).toBe(`hello${PREVIEW_ELLIPSIS}`)
    })

    it('picks the largest candidate at or before the limit', () => {
      // Both "aa " (end of first text) and mid-word in "more" are ≤ limit; mid-word wins.
      expect(markdownSafeCut('aa more text', 7)).toBe(`aa more${PREVIEW_ELLIPSIS}`)
    })

    it('at limitOffset === length (scan-cap shape) keeps content and appends inline ellipsis', () => {
      // text/paragraph/root all end at length; inline must beat block so we get
      // `hello…` not `hello\n\n…`.
      expect(markdownSafeCut('hello', 5)).toBe(`hello${PREVIEW_ELLIPSIS}`)
    })

    it('cuts inside a second paragraph at a text-safe offset', () => {
      expect(markdownSafeCut('first para\n\nsecond para here', 20))
        .toBe(`first para\n\nsecond p${PREVIEW_ELLIPSIS}`)
    })

    it('cuts inside a single-line blockquote', () => {
      expect(markdownSafeCut('> quote line one and more', 15))
        .toBe(`> quote line on${PREVIEW_ELLIPSIS}`)
    })

    it('cuts inside a list item', () => {
      expect(markdownSafeCut('- item one and more text', 12))
        .toBe(`- item one a${PREVIEW_ELLIPSIS}`)
    })

    it('keeps an ATX heading a heading when cutting its interior', () => {
      const out = markdownSafeCut('# Heading text here\n\nbody', 12)
      expect(out.startsWith('# ')).toBe(true)
      expect(out).toBe(`# Heading te${PREVIEW_ELLIPSIS}`)
    })

    it('cuts before a straddling link when that boundary is at/above FLOOR', () => {
      const text = `${'x'.repeat(60)} [label](https://example.com/very/long/path) end`
      const out = markdownSafeCut(text, 80)
      expect(out).toBe(`${'x'.repeat(60)}${PREVIEW_ELLIPSIS}`)
      expect(out.includes('[')).toBe(false)
    })

    it('keeps a complete emphasis/strong/inline-code construct before the limit', () => {
      expect(markdownSafeCut('aa *em text* bb more', 14)).toBe(`aa *em text* b${PREVIEW_ELLIPSIS}`)
      expect(markdownSafeCut('aa **bold text** bb more', 18)).toBe(`aa **bold text** b${PREVIEW_ELLIPSIS}`)
      expect(markdownSafeCut('aa `code` bb more', 12)).toBe(`aa \`code\` bb${PREVIEW_ELLIPSIS}`)
    })
  })

  describe('block ellipsis placement', () => {
    it('puts block ellipsis after a complete fenced code block', () => {
      // best = code-block end → `\n\n…`, never inline (which would un-close the fence).
      expect(markdownSafeCut('before\n```\nx\n```\n\nafter more', 16))
        .toBe(`before\n\`\`\`\nx\n\`\`\`\n\n${PREVIEW_ELLIPSIS}`)
    })

    it('puts block ellipsis after a setext heading', () => {
      expect(markdownSafeCut('Heading\n=======\n\nbody text', 15))
        .toBe(`Heading\n=======\n\n${PREVIEW_ELLIPSIS}`)
    })

    it('puts block ellipsis after a thematic break', () => {
      expect(markdownSafeCut('aaa\n\n---\n\nbbb more', 8))
        .toBe(`aaa\n\n---\n\n${PREVIEW_ELLIPSIS}`)
    })
  })

  describe('fence fallback', () => {
    it('closes a straddling fenced block at the cut', () => {
      const text = 'before\n```js\nconst x = 1\nconst y = 2\n```\nafter'
      expect(markdownSafeCut(text, 25))
        .toBe(`before\n\`\`\`js\nconst x = 1\n${PREVIEW_ELLIPSIS}\n\`\`\``)
    })

    it('matches a ~~~ opener', () => {
      expect(markdownSafeCut('~~~\nlong content here\n~~~', 10))
        .toBe(`~~~\nlong c${PREVIEW_ELLIPSIS}\n~~~`)
    })

    it('matches a 4+-backtick fence run', () => {
      expect(markdownSafeCut('````\ncode with ``` inside\n````', 15))
        .toBe(`\`\`\`\`\ncode with ${PREVIEW_ELLIPSIS}\n\`\`\`\``)
    })

    it('still closes an unterminated fence in the input', () => {
      expect(markdownSafeCut('before\n```js\nconst x = 1\nstill going', 25))
        .toBe(`before\n\`\`\`js\nconst x = 1\n${PREVIEW_ELLIPSIS}\n\`\`\``)
    })

    it('falls through when the limit is inside the opening fence line', () => {
      const text = 'before\n```js\nconst x = 1\nconst y = 2\n```\nafter'
      expect(markdownSafeCut(text, 10)).toBe(`before${PREVIEW_ELLIPSIS}`)
    })

    it('clamps to content end when the limit is inside the closing fence line', () => {
      const text = 'before\n```js\nconst x = 1\nconst y = 2\n```\nafter'
      const out = markdownSafeCut(text, 38)
      expect(out).toBe(`before\n\`\`\`js\nconst x = 1\nconst y = 2${PREVIEW_ELLIPSIS}\n\`\`\``)
      // No doubled fence.
      expect(out.match(/```/g)?.length).toBe(2)
    })

    it('preserves the blockquote prefix on a nested fence closer', () => {
      expect(markdownSafeCut('> ```\n> long code content here\n> ```', 20))
        .toBe(`> \`\`\`\n> long code co${PREVIEW_ELLIPSIS}\n> \`\`\``)
    })

    it('keeps the blockquote prefix on the ellipsis line at a content-line boundary', () => {
      // Cut lands right after the first content line's newline (offset 29). The
      // `…` opens a fresh code line, so it must carry the `> ` prefix -- otherwise
      // it escapes the blockquote and the closing fence re-parses as a second,
      // empty code block.
      expect(markdownSafeCut('> ```\n> aaaaaaaaaaaaaaaaaaaa\n> bbbbbbbbbbbbbbbbbbbb\n> ```', 29))
        .toBe(`> \`\`\`\n> aaaaaaaaaaaaaaaaaaaa\n> ${PREVIEW_ELLIPSIS}\n> \`\`\``)
    })

    it('keeps the nested blockquote prefix on the ellipsis line at a boundary', () => {
      expect(markdownSafeCut('> > ```\n> > code line one here\n> > code line two here\n> > ```', 31))
        .toBe(`> > \`\`\`\n> > code line one here\n> > ${PREVIEW_ELLIPSIS}\n> > \`\`\``)
    })

    it('does not add a prefix at a mid-line cut inside plain fence content', () => {
      // Non-container fence: the opener prefix is empty, so a boundary cut just
      // starts the `…` on its own (unprefixed) code line -- unchanged behavior.
      expect(markdownSafeCut('```\naaaaaaaaaaaaaaaaaaaa\nbbbbbbbbbbbbbbbbbbbb\n```', 25))
        .toBe(`\`\`\`\naaaaaaaaaaaaaaaaaaaa\n${PREVIEW_ELLIPSIS}\n\`\`\``)
    })
  })

  describe('delimiter fallback', () => {
    it('closes straddling strong with …**', () => {
      expect(markdownSafeCut('**bold text that continues**', 10))
        .toBe(`**bold tex${PREVIEW_ELLIPSIS}**`)
    })

    it('reads __ openers/closers from the source', () => {
      expect(markdownSafeCut('__bold text here__', 10))
        .toBe(`__bold tex${PREVIEW_ELLIPSIS}__`)
    })

    it('closes nested emphasis inside strong innermost-first', () => {
      expect(markdownSafeCut('**a *b c d e* f**', 8))
        .toBe(`**a *b c${PREVIEW_ELLIPSIS}***`)
    })

    it('closes ~~ and single-~ delete markers from the source', () => {
      expect(markdownSafeCut('~~strike text~~', 8)).toBe(`~~strike${PREVIEW_ELLIPSIS}~~`)
      expect(markdownSafeCut('~strike text~', 8)).toBe(`~strike ${PREVIEW_ELLIPSIS}~`)
    })

    it('closes multi-backtick inline code with a matching run', () => {
      expect(markdownSafeCut('`` code ` tick here ``', 12))
        .toBe(`\`\` code \` ti${PREVIEW_ELLIPSIS}\`\``)
    })

    it('cuts at the construct start when the limit is inside the opening run', () => {
      expect(markdownSafeCut('xx **bold text**', 4)).toBe(`xx${PREVIEW_ELLIPSIS}`)
    })

    it('closes still-open outer constructs when the limit is inside a nested opening run', () => {
      // Limit inside the inner `**` opening run: the inner construct is dropped
      // (cut at its start) and the outer emphasis still gets its closer.
      expect(markdownSafeCut('*aa **bold text here** bb*', 5))
        .toBe(`*aa${PREVIEW_ELLIPSIS}*`)
    })

    it('clamps to content end when the limit is in the closing region', () => {
      expect(markdownSafeCut('**bold text**', 11))
        .toBe(`**bold text${PREVIEW_ELLIPSIS}**`)
    })

    it('hard-cuts when a link straddles inside bold (no synthesizable closer)', () => {
      const out = markdownSafeCut('**a [link](http://x.com) b**', 15)
      expect(out).toBe(`**a [link](http${PREVIEW_ELLIPSIS}`)
      expect(out.endsWith('**')).toBe(false)
    })
  })

  describe('table', () => {
    it('drops the table when the limit falls in the header, delimiter, or first body row', () => {
      const prefix = `${'p'.repeat(40)}\n\n`
      const table = '| a | b |\n| - | - |\n| 1 | 2 |\n| 3 | 4 |\n'
      const text = prefix + table
      // Header cell.
      expect(markdownSafeCut(text, prefix.length + 5)).toBe(`${'p'.repeat(40)}${PREVIEW_ELLIPSIS}`)
      // Delimiter row (not an AST node -- sits between header and first body row).
      expect(markdownSafeCut(text, prefix.length + 15)).toBe(`${'p'.repeat(40)}${PREVIEW_ELLIPSIS}`)
      // First body row (its end is past the limit, so it is not yet a candidate).
      expect(markdownSafeCut(text, prefix.length + 25)).toBe(`${'p'.repeat(40)}${PREVIEW_ELLIPSIS}`)
    })

    it('cuts after the first body row when the limit is in the second', () => {
      const table = '| a | b |\n| - | - |\n| 1 | 2 |\n| 3 | 4 |\n'
      expect(markdownSafeCut(table, 35))
        .toBe(`| a | b |\n| - | - |\n| 1 | 2 |\n\n${PREVIEW_ELLIPSIS}`)
    })

    it('keeps a table that ends fully before the limit', () => {
      const text = `| a | b |\n| - | - |\n| 1 | 2 |\n\n${'z'.repeat(30)}`
      const limit = text.length - 5
      const out = markdownSafeCut(text, limit)
      expect(out.startsWith('| a | b |\n| - | - |\n| 1 | 2 |')).toBe(true)
      expect(out.endsWith(PREVIEW_ELLIPSIS)).toBe(true)
    })
  })

  describe('misc', () => {
    it('hard-cuts a straddling html node when no earlier candidate clears FLOOR', () => {
      expect(markdownSafeCut('<span>hello world content</span>', 15))
        .toBe(`<span>hello wor${PREVIEW_ELLIPSIS}`)
    })

    it('puts block ellipsis after a complete block-html section', () => {
      // A block HTML section runs until a blank line, so an inline `…` glued to
      // `</div>` would be absorbed into the block and dropped by the renderer
      // (raw HTML is stripped). The blank line keeps the ellipsis a paragraph.
      expect(markdownSafeCut('<div>hello</div>\n\n**bold text straddles here**', 30))
        .toBe(`<div>hello</div>\n\n${PREVIEW_ELLIPSIS}`)
    })

    it('keeps the ellipsis inline after inline html inside a paragraph', () => {
      // `<x>`/`</x>` are inline html nodes inside the paragraph; the trailing
      // strong straddles the limit, so the cut lands right after the inline
      // html (at the following text node's end; trimEnd makes them coincide)
      // and the ellipsis must stay inline, not become its own paragraph.
      expect(markdownSafeCut('aa <x>y</x> **bold text straddling**', 20))
        .toBe(`aa <x>y</x>${PREVIEW_ELLIPSIS}`)
    })

    it('hard-cuts a straddling autolink with a bounded garble', () => {
      expect(markdownSafeCut('go https://example.com/foo end', 15))
        .toBe(`go https://exam${PREVIEW_ELLIPSIS}`)
    })

    it('falls back to a hard cut when a chosen cut would trim to empty', () => {
      // Opening-run cut at offset 0 for a leading delimiter → empty prefix → hard cut.
      expect(markdownSafeCut('**bold**', 1)).toBe(`*${PREVIEW_ELLIPSIS}`)
    })

    it('hard-cuts defensively at a zero or negative limitOffset', () => {
      // truncatePreview never passes ≤ 0, but the exported API guards it: keep
      // at least one character so the result is never a bare ellipsis.
      expect(markdownSafeCut('abc', 0)).toBe(`a${PREVIEW_ELLIPSIS}`)
      expect(markdownSafeCut('abc', -5)).toBe(`a${PREVIEW_ELLIPSIS}`)
    })

    it('hard-cuts defensively at a NaN limitOffset instead of emitting a bare ellipsis', () => {
      // Every comparison against NaN is false, which would skip the ≤0 guard,
      // void FLOOR, and slice(0, NaN) -- leaving nothing but the ellipsis.
      // A NaN limit must degrade exactly like limitOffset 0.
      expect(markdownSafeCut('abc', Number.NaN)).toBe(`a${PREVIEW_ELLIPSIS}`)
      expect(markdownSafeCut('```js\nconst x = 1\n```\nafter', Number.NaN)).toBe(`\`${PREVIEW_ELLIPSIS}`)
    })

    it('keeps a leading astral character whole at a zero limitOffset', () => {
      // The 1-code-unit floor of the defensive hard cut must not bisect a
      // surrogate pair into a lone (malformed) high surrogate.
      expect(markdownSafeCut('😀bc', 0)).toBe(`😀${PREVIEW_ELLIPSIS}`)
    })
  })

  describe('pathological input', () => {
    it('degrades to a bounded hard cut on pathologically deep nesting instead of overflowing the stack', () => {
      // ~4000 nested blockquote markers parse fine (micromark iterates), but a
      // recursive AST walk would overflow the call stack; the analysis must
      // degrade to the bounded hard cut instead of throwing.
      const out = markdownSafeCut(`${'>'.repeat(3990)} x`, 3900)
      expect(out.endsWith(PREVIEW_ELLIPSIS)).toBe(true)
      expect(out.length).toBeGreaterThan(1)
    })
  })

  describe('entity atomicity', () => {
    it('snaps past a named entity straddling the limit', () => {
      // Cut at offset 7 lands inside `&amp;` (span [4, 9)); the entity is kept
      // whole so the tooltip shows `Tom &` instead of a literal `Tom &am`.
      expect(markdownSafeCut('Tom &amp; Jerry forever', 7)).toBe(`Tom &amp;${PREVIEW_ELLIPSIS}`)
    })

    it('snaps past a numeric (hex) entity straddling the limit', () => {
      expect(markdownSafeCut('star &#x2B50; twinkle', 8)).toBe(`star &#x2B50;${PREVIEW_ELLIPSIS}`)
    })

    it('does not snap on a bare ampersand that is not an entity', () => {
      // `&T ` has no terminating `;`, so the cut stays a plain mid-word cut.
      expect(markdownSafeCut('AT&T stock is up today', 4)).toBe(`AT&T${PREVIEW_ELLIPSIS}`)
    })

    it('does not snap when the entity ends exactly at the limit', () => {
      // `&amp;` spans [2, 7); a cut AT 7 does not split it -- no snap needed.
      expect(markdownSafeCut('a &amp; b more words here', 7)).toBe(`a &amp;${PREVIEW_ELLIPSIS}`)
    })
  })

  describe('dangling references', () => {
    it('rewrites a full link reference whose definition is past the cut', () => {
      const out = markdownSafeCut(
        'See [the docs][ref] and much more here\n\n[ref]: https://example.com/docs',
        30,
      )
      expect(out).toBe(`See the docs and much m${PREVIEW_ELLIPSIS}`)
    })

    it('keeps a link reference whose definition survives before the cut', () => {
      const out = markdownSafeCut(
        '[ref]: https://example.com\n\nSee [docs][ref] and more content words',
        55,
      )
      expect(out).toContain('[docs][ref]')
    })

    it('leaves a reference with no definition anywhere literal, matching the full render', () => {
      const out = markdownSafeCut('Go [here][nowhere] then keep reading this text', 30)
      expect(out).toBe(`Go [here][nowhere] then keep r${PREVIEW_ELLIPSIS}`)
    })

    it('strips a dangling image reference', () => {
      const out = markdownSafeCut(
        'Look ![alt pic][img] here and more after\n\n[img]: https://x.com/p.png',
        30,
      )
      // The doubled space where the image sat collapses in HTML rendering.
      expect(out).toBe(`Look  here and${PREVIEW_ELLIPSIS}`)
    })

    it('rewrites a shortcut-form reference to its label text', () => {
      const out = markdownSafeCut(
        'Read [manual] plus other helpful words\n\n[manual]: https://m.io',
        25,
      )
      expect(out).toBe(`Read manual plus other${PREVIEW_ELLIPSIS}`)
    })

    it('rewrites a collapsed-form reference to its label text', () => {
      const out = markdownSafeCut(
        'Read [docs][] and other stuff here\n\n[docs]: https://d.io',
        25,
      )
      expect(out).toBe(`Read docs and other s${PREVIEW_ELLIPSIS}`)
    })

    it('rewrites every dangling reference in the prefix', () => {
      const out = markdownSafeCut(
        'A [one][1] B [two][2] C plus tail words\n\n[1]: https://a.io\n\n[2]: https://b.io',
        30,
      )
      expect(out).toBe(`A one B two C plus t${PREVIEW_ELLIPSIS}`)
    })

    it('leaves a reference straddling the cut to the bounded hard-cut garble', () => {
      // The clean boundary before the ref sits below FLOOR and an atomic inline
      // has no synthesizable closer, so the hard cut slices mid-ref; the partial
      // ref must stay raw (rewriting applies only to refs fully inside the cut).
      expect(markdownSafeCut('go [label here][ref] x\n\n[ref]: https://r.io', 15))
        .toBe(`go [label here]${PREVIEW_ELLIPSIS}`)
    })

    it('strips a dangling footnote reference', () => {
      const out = markdownSafeCut(
        'Fact[^1] and further discussion text\n\n[^1]: the source',
        20,
      )
      expect(out).toBe(`Fact and further${PREVIEW_ELLIPSIS}`)
    })

    it('keeps nested formatting inside a rewritten link label', () => {
      const out = markdownSafeCut(
        'See [**bold** docs][ref] plus more trailing words\n\n[ref]: https://e.com',
        35,
      )
      expect(out).toBe(`See **bold** docs plus more${PREVIEW_ELLIPSIS}`)
    })

    it('forward-scans to the next real content when the rewrite empties the prefix', () => {
      // Everything before the limit is a dangling image reference, so the
      // rewritten prefix is empty; the hard cut must scan past the reference
      // (and its definition) to the prose instead of dumping raw `![…][…]`.
      expect(markdownSafeCut('![alt][img] tail\n\n[img]: u', 12)).toBe(`tail${PREVIEW_ELLIPSIS}`)
    })

    it('previews the prose after a reference-style image gallery', () => {
      const out = markdownSafeCut(
        '![aa][r0]\n![aa][r1]\n\nReal prose here after gallery\n\n[r0]: https://a.io\n\n[r1]: https://b.io',
        10,
      )
      expect(out).toBe(`Real prose${PREVIEW_ELLIPSIS}`)
    })

    it('falls back to the bounded raw slice when no renderable content exists anywhere', () => {
      // The whole text is one dangling image reference plus its definition:
      // nothing renderable remains after the rewrite, so the raw slice is the
      // accepted last resort (a bare ellipsis stays forbidden).
      expect(markdownSafeCut('![alt][img]\n\n[img]: https://x.io', 11))
        .toBe(`![alt][img]${PREVIEW_ELLIPSIS}`)
    })

    it('re-parses a rewritten prefix with no reference nodes left', () => {
      const out = markdownSafeCut(
        'See [the docs][ref] and much more here\n\n[ref]: https://example.com/docs',
        30,
      )
      expect(JSON.stringify(reparse(out))).not.toContain('linkReference')
    })
  })

  describe('re-parse invariant', () => {
    it('re-parses a straddling blockquote fence to a single closed code block', () => {
      // Pre-fix, the boundary cut dropped the `> ` on the ellipsis line, so this
      // re-parsed to blockquote / paragraph / blockquote -- the ellipsis escaped
      // the quote and the closing fence opened a second, empty code block.
      const out = markdownSafeCut('> ```\n> aaaaaaaaaaaaaaaaaaaa\n> bbbbbbbbbbbbbbbbbbbb\n> ```', 29)
      const tree = reparse(out)
      expect(tree.children.map(c => c.type)).toEqual(['blockquote'])
      const bq = tree.children[0] as Blockquote
      expect(bq.children.map(c => c.type)).toEqual(['code'])
      expect((bq.children[0] as Code).value).toContain(PREVIEW_ELLIPSIS)
    })

    it('keeps the block ellipsis inside a blockquote that continues past the cut', () => {
      // Best boundary = the first (closed) fence's end INSIDE the quote; the
      // quote continues with a second fence past the limit. The ellipsis
      // paragraph must carry the `> ` marker so it stays inside the quote,
      // mirroring the fence fallback's prefix preservation.
      const text = [
        '> ```',
        '> aaaaaaaaaaaaaaaaaaaa',
        '> aaaaaaaaaaaaaaaaaaaa',
        '> aaaaaaaaaaaaaaaaaaaa',
        '> ```',
        '> ```',
        '> cccccccccccccccccccc',
        '> cccccccccccccccccccc',
        '> ```',
      ].join('\n')
      const out = markdownSafeCut(text, 100)
      const tree = reparse(out)
      expect(tree.children.map(c => c.type)).toEqual(['blockquote'])
      const bq = tree.children[0] as Blockquote
      expect(bq.children.map(c => c.type)).toEqual(['code', 'paragraph'])
      expect(JSON.stringify(bq.children[1])).toContain(PREVIEW_ELLIPSIS)
    })

    it('carries every open blockquote level onto the block ellipsis line', () => {
      // Doubly-nested quote: the ellipsis line needs `> > `, not a single `> `
      // (which would strand it in the outer quote, one level out from the
      // content it truncates).
      const text = [
        '> > ```',
        '> > aaaaaaaaaaaaaaaaaaaa',
        '> > aaaaaaaaaaaaaaaaaaaa',
        '> > ```',
        '> > ```',
        '> > cccccccccccccccccccc',
        '> > ```',
      ].join('\n')
      const tree = reparse(markdownSafeCut(text, 80))
      expect(tree.children.map(c => c.type)).toEqual(['blockquote'])
      const outer = tree.children[0] as Blockquote
      expect(outer.children.map(c => c.type)).toEqual(['blockquote'])
      const inner = outer.children[0] as Blockquote
      expect(inner.children.map(c => c.type)).toEqual(['code', 'paragraph'])
      expect(JSON.stringify(inner.children[1])).toContain(PREVIEW_ELLIPSIS)
    })

    it('leaves the block ellipsis outside a blockquote that ends before the cut', () => {
      // The quote completes before the boundary; the truncated remainder sits
      // outside it, so the ellipsis paragraph must NOT gain a `> ` marker.
      const text = [
        '> ```',
        '> aaaaaaaaaaaaaaaaaaaa',
        '> aaaaaaaaaaaaaaaaaaaa',
        '> ```',
        '',
        '```',
        'cccccccccccccccccccc',
        'cccccccccccccccccccc',
        'cccccccccccccccccccc',
        '```',
      ].join('\n')
      const out = markdownSafeCut(text, 80)
      const tree = reparse(out)
      expect(tree.children.map(c => c.type)).toEqual(['blockquote', 'paragraph'])
      expect(JSON.stringify(tree.children[1])).toContain(PREVIEW_ELLIPSIS)
    })

    it('re-parses each delimiter fallback to a single closed construct', () => {
      // Every synthesized-closer output must re-parse so the delimiter run is a
      // real strong/emphasis/delete/inlineCode node -- not literal stray markers.
      const cases: [string, number, string][] = [
        ['**bold text that continues**', 10, 'strong'],
        ['__bold text here__', 10, 'strong'],
        ['*emphasis text that continues*', 10, 'emphasis'],
        ['~~strike text here~~', 8, 'delete'],
        ['`` code ` tick here ``', 12, 'inlineCode'],
      ]
      for (const [input, limit, nodeType] of cases) {
        const out = markdownSafeCut(input, limit)
        const tree = reparse(out)
        expect(tree.children.map(c => c.type)).toEqual(['paragraph'])
        const para = tree.children[0] as { children: { type: string }[] }
        // The paragraph holds exactly the closed construct (no trailing text node
        // carrying a stray `*`/`` ` ``/`~` marker).
        expect(para.children.map(c => c.type)).toEqual([nodeType])
      }
    })
  })

  describe('collect prune', () => {
    it('is unaffected by markdown structure past the cut', () => {
      // The `start >= limitOffset` prune skips subtrees beyond the cut; the chosen
      // boundary must not change when trailing blocks are appended.
      const base = 'first para\n\nsecond para here'
      const trailer = `\n\n${'| a | b |\n| - | - |\n| 1 | 2 |\n'.repeat(20)}`
      expect(markdownSafeCut(base + trailer, 20)).toBe(markdownSafeCut(base, 20))
      expect(markdownSafeCut(base + trailer, 20)).toBe(`first para\n\nsecond p${PREVIEW_ELLIPSIS}`)
    })
  })
})
