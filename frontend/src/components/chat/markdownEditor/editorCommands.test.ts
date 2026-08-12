import type { Editor } from '@milkdown/core'
import type { EditorView } from '@milkdown/prose/view'
import { editorViewCtx } from '@milkdown/core'
import { Schema } from '@milkdown/prose/model'
import { EditorState } from '@milkdown/prose/state'
import { describe, expect, it, vi } from 'vitest'
import { applyCodeBlockLanguage, applyLinkHref, removeLinkRange } from './editorCommands'

/**
 * A schema mirroring the Milkdown declarations these commands read: `link`
 * carries `href` AND `title`, and `code_block` carries a `language` attribute.
 */
const schema = new Schema({
  nodes: {
    doc: { content: 'block+' },
    paragraph: { group: 'block', content: 'inline*' },
    code_block: { group: 'block', content: 'text*', attrs: { language: { default: null } } },
    text: { group: 'inline', inline: true },
  },
  marks: {
    link: { attrs: { href: {}, title: { default: null } } },
  },
})

/**
 * A minimal stand-in for the Milkdown editor: `action` hands the callback a ctx
 * whose only entry is the view, which is what every command reads. Real Milkdown
 * would need a DOM host and a full plugin stack for behaviour these pure
 * transactions do not depend on.
 */
function fakeEditor(state: EditorState) {
  const view = {
    state,
    dispatch: vi.fn((tr) => { view.state = view.state.apply(tr) }),
    focus: vi.fn(),
  }
  const editor = {
    action: (fn: (ctx: unknown) => void) => fn({ get: (key: unknown) => (key === editorViewCtx ? view : undefined) }),
  }
  return { editor: editor as unknown as Editor, view: view as unknown as EditorView & { state: EditorState } }
}

function paragraphState(text: string, marks: ReturnType<typeof schema.marks.link.create>[] = []) {
  return EditorState.create({
    doc: schema.nodes.doc!.create(null, schema.nodes.paragraph!.create(null, schema.text(text, marks))),
  })
}

function linkAt(state: EditorState, pos: number) {
  return state.doc.resolve(pos).nodeAfter?.marks.find(m => m.type.name === 'link')
}

describe('applyLinkHref', () => {
  it('rewrites the href in place rather than adding a second mark', () => {
    const { editor, view } = fakeEditor(
      paragraphState('docs', [schema.marks.link!.create({ href: 'https://old.test' })]),
    )

    applyLinkHref(editor, { from: 1, to: 5 }, 'https://new.test')

    expect(linkAt(view.state, 1)?.attrs.href).toBe('https://new.test')
  })

  it('links previously unmarked text, which is what mod+K over a selection does', () => {
    const { editor, view } = fakeEditor(paragraphState('design doc'))

    applyLinkHref(editor, { from: 1, to: 11 }, 'https://x.test')

    expect(linkAt(view.state, 1)?.attrs.href).toBe('https://x.test')
  })

  // The schema declares `title` beside `href`, and a pasted
  // `[docs](url "The docs")` round-trips one. Rebuilding the mark from the href
  // alone dropped it with nothing said.
  it('carries the mark\'s other attributes through the edit', () => {
    const { editor, view } = fakeEditor(paragraphState('docs'))

    applyLinkHref(
      editor,
      { from: 1, to: 5, attrs: { href: 'https://old.test', title: 'The docs' } },
      'https://new.test',
    )

    expect(linkAt(view.state, 1)?.attrs).toEqual({ href: 'https://new.test', title: 'The docs' })
  })

  // The persistent editor replaces its whole document on a draft swap, so a
  // popover left open across one holds positions that describe nothing. Past the
  // end they throw out of the submit handler; inside it they would write the
  // href onto unrelated text.
  it('refuses a range past the end of the current document', () => {
    const { editor, view } = fakeEditor(paragraphState('short'))

    expect(() => applyLinkHref(editor, { from: 1, to: 400 }, 'https://x.test')).not.toThrow()
    expect(view.dispatch).not.toHaveBeenCalled()
  })

  it('refuses an empty range and a blank href', () => {
    const { editor, view } = fakeEditor(paragraphState('docs'))

    applyLinkHref(editor, { from: 3, to: 3 }, 'https://x.test')
    applyLinkHref(editor, { from: 1, to: 5 }, '   ')

    expect(view.dispatch).not.toHaveBeenCalled()
  })
})

describe('removeLinkRange', () => {
  it('strips the mark and leaves the text', () => {
    const { editor, view } = fakeEditor(
      paragraphState('docs', [schema.marks.link!.create({ href: 'https://old.test' })]),
    )

    removeLinkRange(editor, { from: 1, to: 5 })

    expect(linkAt(view.state, 1)).toBeUndefined()
    expect(view.state.doc.textContent).toBe('docs')
  })
})

describe('applyCodeBlockLanguage', () => {
  function codeBlockState(text: string) {
    return EditorState.create({
      doc: schema.nodes.doc!.create(null, schema.nodes.code_block!.create(null, schema.text(text))),
    })
  }

  it('sets the language on the block at the position', () => {
    const { editor, view } = fakeEditor(codeBlockState('print(1)'))
    const close = vi.fn()

    applyCodeBlockLanguage(editor, 0, 'python', close)

    expect(view.state.doc.child(0).attrs.language).toBe('python')
    expect(close).toHaveBeenCalledOnce()
  })

  it('clears the language for an empty selection', () => {
    const { editor, view } = fakeEditor(codeBlockState('print(1)'))

    applyCodeBlockLanguage(editor, 0, '', () => {})

    expect(view.state.doc.child(0).attrs.language).toBeNull()
  })

  // Same stale-position hazard as the link popover: the draft swap replaces the
  // document, and `nodeAt` past the end throws.
  it('refuses a position past the end of the current document, and still closes', () => {
    const { editor, view } = fakeEditor(codeBlockState('print(1)'))
    const close = vi.fn()

    expect(() => applyCodeBlockLanguage(editor, 400, 'python', close)).not.toThrow()
    expect(view.dispatch).not.toHaveBeenCalled()
    // The popover closes even when the write is refused, so a stale position
    // cannot strand it open.
    expect(close).toHaveBeenCalledOnce()
  })

  it('leaves a node that is not a code block alone', () => {
    const { editor, view } = fakeEditor(paragraphState('not code'))

    applyCodeBlockLanguage(editor, 0, 'python', () => {})

    expect(view.dispatch).not.toHaveBeenCalled()
  })
})
