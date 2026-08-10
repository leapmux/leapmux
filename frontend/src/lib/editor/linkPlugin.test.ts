import { Schema } from '@milkdown/prose/model'
import { EditorState } from '@milkdown/prose/state'
import { describe, expect, it } from 'vitest'
import { linkRangeAt } from './linkPlugin'

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
    link: { attrs: { href: {} } },
    strong: {},
  },
})

const link = (href: string) => schema.marks.link!.create({ href })
const strong = schema.marks.strong!.create()

function stateOf(...content: ReturnType<typeof schema.text>[]) {
  return EditorState.create({
    doc: schema.nodes.doc!.create(null, schema.nodes.paragraph!.create(null, content)),
  })
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

    expect(range).toEqual({ from: 1, to: 5, href: 'https://x.test' })
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

    expect(fromBoldHalf).toEqual({ from: 1, to: 10, href })
    expect(fromPlainHalf).toEqual(fromBoldHalf)
  })

  it('stops at a neighbouring link with a different href', () => {
    const state = stateOf(
      schema.text('one', [link('https://one.test')]),
      schema.text('two', [link('https://two.test')]),
    )

    expect(linkRangeAt(state, 2)).toEqual({ from: 1, to: 4, href: 'https://one.test' })
    expect(linkRangeAt(state, 5)).toEqual({ from: 4, to: 7, href: 'https://two.test' })
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
