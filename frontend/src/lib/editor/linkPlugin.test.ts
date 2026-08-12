import { Schema } from '@milkdown/prose/model'
import { EditorState, TextSelection } from '@milkdown/prose/state'
import { describe, expect, it } from 'vitest'
import { linkRangeAt, linkShortcutTarget } from './linkPlugin'

/**
 * A real schema, mirroring Milkdown's commonmark declarations for the two marks
 * that matter here: `link` carries an href, and `strong` is what splits a marked
 * run into several text nodes.
 */
const schema = new Schema({
  nodes: {
    doc: { content: 'block+' },
    paragraph: { group: 'block', content: 'inline*' },
    text: { group: 'inline', inline: true },
  },
  marks: {
    link: { attrs: { href: {}, title: { default: null } } },
    strong: {},
  },
})

const link = (href: string, title: string | null = null) => schema.marks.link!.create({ href, title })
const strong = schema.marks.strong!.create()

function stateOf(...content: ReturnType<typeof schema.text>[]) {
  return EditorState.create({
    doc: schema.nodes.doc!.create(null, schema.nodes.paragraph!.create(null, content)),
  })
}

/** A state whose doc holds two paragraphs, for the cross-block selection case. */
function twoParagraphState(a: string, b: string) {
  return EditorState.create({
    doc: schema.nodes.doc!.create(null, [
      schema.nodes.paragraph!.create(null, schema.text(a)),
      schema.nodes.paragraph!.create(null, schema.text(b)),
    ]),
  })
}

/** `state` with the selection set to [from, to). */
function withSelection(state: EditorState, from: number, to: number) {
  return state.apply(state.tr.setSelection(TextSelection.create(state.doc, from, to)))
}

describe('linkRangeAt', () => {
  it('returns null outside a link', () => {
    const state = stateOf(schema.text('plain text'))
    expect(linkRangeAt(state, 3)).toBeNull()
  })

  it('finds the run and its href from a click inside it', () => {
    // doc(1) > paragraph(1) so the text starts at position 1.
    const state = stateOf(schema.text('docs', [link('https://x.test')]))
    const range = linkRangeAt(state, 3)

    expect(range).toEqual({ from: 1, to: 5, href: 'https://x.test', attrs: { href: 'https://x.test', title: null } })
  })

  it('covers the whole link when another mark splits it into several text nodes', () => {
    // `[**bold** rest](url)` is TWO text nodes under one link mark. Editing or
    // removing only the clicked node would leave half the link behind, still
    // carrying the old href.
    const href = 'https://x.test'
    const state = stateOf(
      schema.text('bold', [link(href), strong]),
      schema.text(' rest', [link(href)]),
    )

    const fromBoldHalf = linkRangeAt(state, 3)
    const fromPlainHalf = linkRangeAt(state, 7)

    expect(fromBoldHalf).toEqual({ from: 1, to: 10, href, attrs: { href, title: null } })
    expect(fromPlainHalf).toEqual(fromBoldHalf)
  })

  it('stops at a neighbouring link with a different href', () => {
    const state = stateOf(
      schema.text('one', [link('https://one.test')]),
      schema.text('two', [link('https://two.test')]),
    )

    expect(linkRangeAt(state, 2)).toEqual({ from: 1, to: 4, href: 'https://one.test', attrs: { href: 'https://one.test', title: null } })
    expect(linkRangeAt(state, 5)).toEqual({ from: 4, to: 7, href: 'https://two.test', attrs: { href: 'https://two.test', title: null } })
  })

  // `$pos.marks()` reports the marks of the node BEFORE the position whenever
  // the position sits on a text-node boundary. Resolving from that made the
  // feature work or not depending on whether text preceded the link, and made a
  // click in the empty space past a trailing link open its popover.
  it('finds a link at its leading edge when plain text precedes it', () => {
    const state = stateOf(
      schema.text('see '),
      schema.text('docs', [link('https://x.test')]),
    )
    // Position 5 is the boundary: the end of 'see ' and the first character of
    // the link. Clicking the left half of the 'd' resolves here.
    expect(linkRangeAt(state, 5)).toEqual({
      from: 5,
      to: 9,
      href: 'https://x.test',
      attrs: { href: 'https://x.test', title: null },
    })
  })

  it('returns null past the end of a trailing link', () => {
    const state = stateOf(
      schema.text('read the '),
      schema.text('docs', [link('https://x.test')]),
    )
    // The paragraph end, which is also the link run's `to`. A click in the blank
    // space to the right of the text lands here.
    expect(linkRangeAt(state, 14)).toBeNull()
  })

  it('picks the RIGHT link at the join between two different links', () => {
    // The boundary belongs to the link the caret would enter, not the one it
    // leaves. Reading the preceding node answered with the left link.
    const state = stateOf(
      schema.text('one', [link('https://one.test')]),
      schema.text('two', [link('https://two.test')]),
    )
    expect(linkRangeAt(state, 4)?.href).toBe('https://two.test')
  })

  it('carries the whole attribute set, so an edit cannot drop the title', () => {
    // Milkdown's link schema declares `title` beside `href` and serializes it,
    // so `[docs](url "The docs")` round-trips one. Rebuilding the mark from the
    // href alone lost it with nothing said.
    const state = stateOf(schema.text('docs', [link('https://x.test', 'The docs')]))

    expect(linkRangeAt(state, 3)?.attrs).toEqual({ href: 'https://x.test', title: 'The docs' })
  })

  it('returns null for a schema with no link mark', () => {
    const bare = new Schema({
      nodes: {
        doc: { content: 'block+' },
        paragraph: { group: 'block', content: 'inline*' },
        text: { group: 'inline', inline: true },
      },
    })
    const state = EditorState.create({
      doc: bare.nodes.doc!.create(null, bare.nodes.paragraph!.create(null, bare.text('hi'))),
    })

    expect(linkRangeAt(state, 2)).toBeNull()
  })
})

describe('linkShortcutTarget', () => {
  // Mod-K over a SELECTION means "link this text", so the popover opens empty.
  it('offers an empty href for a selection, so the user types a new URL', () => {
    const state = withSelection(stateOf(schema.text('the design doc')), 5, 11)

    expect(linkShortcutTarget(state)).toEqual({ from: 5, to: 11, href: '', attrs: {} })
  })

  // The range is the SELECTION, not the link inside it. applyLinkHref clears the
  // range before it marks it, so applying overrides the old link rather than
  // leaving the selection carrying two.
  it('takes the selection, not the link inside it, so applying overrides', () => {
    const state = withSelection(
      stateOf(schema.text('see '), schema.text('docs', [link('https://old.test')])),
      1,
      9,
    )

    expect(linkShortcutTarget(state)).toEqual({ from: 1, to: 9, href: '', attrs: {} })
  })

  // Mod-K with a bare CARET on a link means "edit this link", so the popover
  // opens on its current URL and the range covers the whole run.
  it('offers the link under a caret, with its current href', () => {
    const state = withSelection(stateOf(schema.text('docs', [link('https://x.test', 'The docs')])), 3, 3)

    expect(linkShortcutTarget(state)).toEqual({
      from: 1,
      to: 5,
      href: 'https://x.test',
      attrs: { href: 'https://x.test', title: 'The docs' },
    })
  })

  it('covers the whole run when another mark splits the link', () => {
    const href = 'https://x.test'
    const state = withSelection(
      stateOf(schema.text('bold', [link(href), strong]), schema.text(' rest', [link(href)])),
      3,
      3,
    )

    expect(linkShortcutTarget(state)).toMatchObject({ from: 1, to: 10, href })
  })

  it('does nothing for a caret outside a link', () => {
    expect(linkShortcutTarget(withSelection(stateOf(schema.text('plain text')), 4, 4))).toBeNull()
  })

  // One mark cannot span a block boundary, so linking part of the selection
  // would be a silent half-answer.
  it('does nothing for a selection that crosses a block boundary', () => {
    const state = twoParagraphState('first', 'second')
    expect(linkShortcutTarget(withSelection(state, 2, 9))).toBeNull()
  })
})
