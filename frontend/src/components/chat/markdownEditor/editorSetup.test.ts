import type { Node } from '@milkdown/prose/model'
import type { Transaction } from '@milkdown/prose/state'
import type { EditorView } from '@milkdown/prose/view'
import { Schema } from '@milkdown/prose/model'
import { EditorState } from '@milkdown/prose/state'
import { describe, expect, it, vi } from 'vitest'
import { CODE_HIGHLIGHT_REFRESH } from '~/lib/editor/codeHighlightPlugin'
import { computeDocStats } from './editorSetup'

/**
 * A REAL ProseMirror schema, not a structural stand-in.
 *
 * `computeDocStats` leans on two ProseMirror behaviours that only the real
 * classes have: node-type identity (it compares against `schema.nodes.paragraph`
 * rather than a name string) and `textBetween`'s fallback to a leaf's own
 * `leafText`, which is what turns a hard break into a newline. A hand-written
 * fake answers both from its own literals, so it would agree with the classifier
 * no matter what the classifier did.
 *
 * The node declarations mirror Milkdown's commonmark preset where it matters:
 * `hardbreak` carries `leafText: () => '\n'`, and `image` is an inline atom with
 * no `leafText` (so it contributes no text at all).
 */
const schema = new Schema({
  nodes: {
    doc: { content: 'block+' },
    paragraph: { group: 'block', content: 'inline*' },
    heading: { group: 'block', content: 'inline*' },
    blockquote: { group: 'block', content: 'block+' },
    code_block: { group: 'block', content: 'text*', code: true },
    bullet_list: { group: 'block', content: 'paragraph+' },
    ordered_list: { group: 'block', content: 'paragraph+' },
    table: { group: 'block', content: 'paragraph+' },
    text: { group: 'inline', inline: true },
    hardbreak: {
      group: 'inline',
      inline: true,
      selectable: false,
      leafText: () => '\n',
    },
    image: { group: 'inline', inline: true, atom: true },
  },
})

const t = (text: string) => schema.text(text)
const para = (...content: Node[]) => schema.nodes.paragraph!.create(null, content)
const doc = (...blocks: Node[]) => schema.nodes.doc!.create(null, blocks)

describe('computeDocStats', () => {
  it('treats a single paragraph as single-line and returns its text', () => {
    expect(computeDocStats(doc(para(t('hello'))))).toEqual({ multiLine: false, text: 'hello' })
  })

  it('treats an empty document as single-line', () => {
    expect(computeDocStats(doc(para())).multiLine).toBe(false)
  })

  it('reports multi-line for more than one top-level block', () => {
    expect(computeDocStats(doc(para(t('a')), para(t('b')))).multiLine).toBe(true)
  })

  it('reports multi-line for a non-paragraph first block', () => {
    expect(computeDocStats(doc(schema.nodes.heading!.create(null, t('h')))).multiLine).toBe(true)
    expect(computeDocStats(doc(schema.nodes.code_block!.create(null, t('x')))).multiLine).toBe(true)
    expect(computeDocStats(doc(schema.nodes.blockquote!.create(null, para(t('q'))))).multiLine).toBe(true)
    expect(computeDocStats(doc(schema.nodes.bullet_list!.create(null, para(t('i'))))).multiLine).toBe(true)
    expect(computeDocStats(doc(schema.nodes.ordered_list!.create(null, para(t('i'))))).multiLine).toBe(true)
    expect(computeDocStats(doc(schema.nodes.table!.create(null, para(t('c'))))).multiLine).toBe(true)
  })

  it('reports multi-line for a hard break inside one paragraph', () => {
    // Shift+Enter. The classifier does NOT walk for the node type: Milkdown
    // declares `leafText: () => '\n'` on hardbreak, and `textBetween` applies a
    // leaf's own `leafText`, so the break reaches the newline test as a real
    // newline. This assertion is what pins that chain -- the old code matched
    // the node NAME, and matching the wrong one (`hard_break`, which is
    // prosemirror-schema-basic's spelling, not Milkdown's) failed silently.
    const d = doc(para(t('a'), schema.nodes.hardbreak!.create(), t('b')))
    expect(d.textBetween(0, d.content.size)).toBe('a\nb')
    expect(computeDocStats(d)).toEqual({ multiLine: true, text: '' })
  })

  it('reports multi-line for a literal newline inside one paragraph', () => {
    // `tr.insertText` can put a raw "\n" into a single paragraph (the
    // code-delimiter strip on a paste inside inline code). `white-space:
    // pre-wrap` renders it as a line break, so the collapsed layout would show
    // a two-line document with its second line under the overlaid buttons.
    expect(computeDocStats(doc(para(t('a\nb'))))).toEqual({ multiLine: true, text: '' })
  })

  it('does not treat an inline atom as a line break', () => {
    // An image is `inline: true, atom: true` with no `leafText`, so it
    // contributes no text and must not expand the box on its own.
    expect(computeDocStats(doc(para(t('hello'), schema.nodes.image!.create()))).multiLine).toBe(false)
  })

  it('skips the text serialization for a multi-line document', () => {
    // The width is never consulted once the document is multi-line, so
    // serializing the whole document on every keystroke would be wasted work.
    const d = doc(para(t('a')), para(t('b')))
    const textBetween = vi.spyOn(d, 'textBetween')

    expect(computeDocStats(d).text).toBe('')
    expect(textBetween).not.toHaveBeenCalled()
  })

  it('classifies against the schema, not a node name', () => {
    // A schema whose paragraph type is spelled differently must not silently
    // classify its blocks as single-line paragraphs. The safe direction is
    // "always expanded", never "never expanded" -- the latter hides text under
    // the composer's overlaid buttons.
    const renamed = new Schema({
      nodes: {
        doc: { content: 'block+' },
        para: { group: 'block', content: 'inline*' },
        text: { group: 'inline', inline: true },
      },
    })
    const d = renamed.nodes.doc!.create(null, renamed.nodes.para!.create(null, renamed.text('hello')))

    expect(computeDocStats(d).multiLine).toBe(true)
  })
})

describe('refreshEditorHighlight', () => {
  /** A view stand-in: a real state, and a `dispatch` that records each transaction. */
  function stubView(composing: boolean) {
    const dom = document.createElement('div')
    const dispatched: Transaction[] = []
    const view = {
      composing,
      dom,
      state: EditorState.create({
        doc: doc(para(t('x'))),
      }),
      dispatch: (tr: Transaction) => {
        dispatched.push(tr)
      },
    }
    return { view, dom, dispatched }
  }

  // The highlight plugin keys each block on the theme, so one refresh finds
  // every block out of date, and each keeps its old colours until the worker
  // answers.
  it('dispatches the refresh of the highlight plugin', async () => {
    const { refreshEditorHighlight } = await import('./editorSetup')
    const { view, dispatched } = stubView(false)

    refreshEditorHighlight(view as unknown as EditorView)

    expect(dispatched).toHaveLength(1)
    expect(dispatched[0]!.getMeta(CODE_HIGHLIGHT_REFRESH)).toBe(true)
    expect(dispatched[0]!.docChanged).toBe(false)
  })

  // A transaction during an IME composition would disturb it, so the guard
  // DEFERS the repaint over the composition and re-runs it at `compositionend`.
  it('defers the repaint while an IME composition is in flight, then runs it once', async () => {
    const { refreshEditorHighlight } = await import('./editorSetup')
    const { view, dom, dispatched } = stubView(true)

    refreshEditorHighlight(view as unknown as EditorView)
    expect(dispatched).toHaveLength(0)

    // The composition ends, and the repaint the guard deferred runs. Dropping it
    // would leave every code block in the abandoned theme's colours.
    view.composing = false
    dom.dispatchEvent(new Event('compositionend'))
    expect(dispatched).toHaveLength(1)

    // ONCE. A second `compositionend` with no new theme change must not repaint
    // again -- the listener is registered `{ once: true }`.
    dom.dispatchEvent(new Event('compositionend'))
    expect(dispatched).toHaveLength(1)
  })
})
