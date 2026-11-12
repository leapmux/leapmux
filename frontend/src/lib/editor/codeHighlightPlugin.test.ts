import type { SyntaxThemePair } from '~/lib/syntaxThemes'
import type { CachedToken } from '~/lib/tokenCache'
import { Schema } from '@milkdown/prose/model'
import { EditorState } from '@milkdown/prose/state'
import { EditorView } from '@milkdown/prose/view'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setSyntaxThemePair, syntaxThemePair } from '~/lib/shikiThemes'
import { CODE_HIGHLIGHT_REFRESH, codeHighlightKey, createCodeHighlightPlugin } from './codeHighlightPlugin'

/** The two node types the plugin reads, with the `toDOM` that an EditorView renders through. */
const schema = new Schema({
  nodes: {
    doc: { content: 'block+' },
    paragraph: {
      group: 'block',
      content: 'text*',
      toDOM: () => ['p', 0],
    },
    code_block: {
      group: 'block',
      content: 'text*',
      code: true,
      attrs: {
        language: { default: '' },
      },
      toDOM: () => [
        'pre',
        ['code', 0],
      ],
    },
    text: {},
  },
})

/** One tokenize call that the test answers by hand. */
interface TokenizeCall {
  lang: string
  code: string
  pair: SyntaxThemePair
  answer: (tokens: CachedToken[][] | null) => Promise<void>
}

function setup(blocks: Array<{ language: string, text: string }>) {
  const calls: TokenizeCall[] = []
  const tokenize = vi.fn((lang: string, code: string, pair: SyntaxThemePair) => new Promise<CachedToken[][] | null>((resolve) => {
    calls.push({
      lang,
      code,
      pair,
      answer: async (tokens) => {
        resolve(tokens)
        // The plugin stores the answer and dispatches its refresh in a `then`.
        await Promise.resolve()
        await Promise.resolve()
      },
    })
  }))
  const plugin = createCodeHighlightPlugin({
    languageExtractor: node => (node.attrs.language as string) || undefined,
    tokenize,
  })
  const doc = schema.nodes.doc!.create(null, blocks.map(block => schema.nodes.code_block!.create(
    { language: block.language },
    block.text === ''
      ? []
      : schema.text(block.text),
  )))
  const view = new EditorView(document.createElement('div'), {
    state: EditorState.create({
      doc,
      plugins: [plugin],
    }),
  })
  return { view, calls, tokenize }
}

/** Tokens for one line: the first word styled, the rest plain. */
function firstWordTokens(text: string, className: string): CachedToken[][] {
  const space = text.indexOf(' ')
  const word = space < 0
    ? text
    : text.slice(0, space)
  const rest = text.slice(word.length)
  return [
    rest === ''
      ? [
          { content: word, className },
        ]
      : [
          { content: word, className },
          { content: rest },
        ],
  ]
}

function styled(view: EditorView, className: string): string[] {
  return [
    ...view.dom.querySelectorAll(`.${className}`),
  ].map(el => el.textContent ?? '')
}

/** Insert `text` at the end of the first code block. */
function typeAtEnd(view: EditorView, text: string): void {
  const block = view.state.doc.firstChild!
  view.dispatch(view.state.tr.insertText(text, 1 + block.content.size))
}

describe('createCodeHighlightPlugin', () => {
  const views: EditorView[] = []
  afterEach(() => {
    for (const view of views.splice(0))
      view.destroy()
  })

  function track<T extends { view: EditorView }>(harness: T): T {
    views.push(harness.view)
    return harness
  }

  it('tokenizes a code block in the worker and decorates its styled tokens', async () => {
    const { view, calls } = track(setup([
      { language: 'typescript', text: 'const a = 1' },
    ]))

    expect(calls).toHaveLength(1)
    expect(calls[0]).toMatchObject({
      lang: 'typescript',
      code: 'const a = 1',
      pair: syntaxThemePair(),
    })
    expect(styled(view, 'sk-keyword')).toEqual([])

    await calls[0]!.answer(firstWordTokens('const a = 1', 'sk-keyword'))

    expect(styled(view, 'sk-keyword')).toEqual(['const'])
    expect(view.dom.querySelector('.sk-keyword')?.classList.contains('shiki')).toBe(true)
  })

  it('keeps the decorations of the old text while the new text tokenizes', async () => {
    const { view, calls } = track(setup([
      { language: 'typescript', text: 'const a' },
    ]))
    await calls[0]!.answer(firstWordTokens('const a', 'sk-old'))

    typeAtEnd(view, 'b')

    // The edit mapped the old decorations; the block did not flash to plain.
    expect(styled(view, 'sk-old')).toEqual(['const'])
    expect(calls).toHaveLength(2)
    expect(calls[1]!.code).toBe('const ab')

    await calls[1]!.answer(firstWordTokens('const ab', 'sk-new'))

    expect(styled(view, 'sk-old')).toEqual([])
    expect(styled(view, 'sk-new')).toEqual(['const'])
  })

  it('asks for one text at a time, then for the text that the block holds then', async () => {
    const { view, calls } = track(setup([
      { language: 'typescript', text: 'let x' },
    ]))

    typeAtEnd(view, '1')
    typeAtEnd(view, '2')
    typeAtEnd(view, '3')
    // The first request is still out, so the edits asked for nothing.
    expect(calls.map(call => call.code)).toEqual(['let x'])

    await calls[0]!.answer(firstWordTokens('let x', 'sk-a'))

    expect(calls.map(call => call.code)).toEqual(['let x', 'let x123'])
    await calls[1]!.answer(firstWordTokens('let x123', 'sk-b'))
    expect(styled(view, 'sk-b')).toEqual(['let'])
    expect(calls).toHaveLength(2)
  })

  it('leaves a block with no language plain, and asks for nothing', () => {
    const { view, calls } = track(setup([
      { language: '', text: 'plain text here' },
    ]))
    expect(calls).toHaveLength(0)
    expect(codeHighlightKey.getState(view.state)?.pending).toEqual([])
  })

  it('shows a block plain when the worker cannot tokenize it, without asking again', async () => {
    const { view, calls } = track(setup([
      { language: 'typescript', text: 'const a' },
    ]))

    await calls[0]!.answer(null)
    view.dispatch(view.state.tr.setMeta(CODE_HIGHLIGHT_REFRESH, true))

    expect(calls).toHaveLength(1)
    expect(view.dom.querySelector('.shiki')).toBeNull()
  })

  it('asks nothing again for a block whose text did not change', async () => {
    const { view, calls } = track(setup([
      { language: 'typescript', text: 'const a' },
      { language: 'python', text: 'def f' },
    ]))
    await calls[0]!.answer(firstWordTokens('const a', 'sk-ts'))
    await calls[1]!.answer(firstWordTokens('def f', 'sk-py'))

    // Edit only the first block.
    typeAtEnd(view, 'b')
    await calls[2]!.answer(firstWordTokens('const ab', 'sk-ts2'))

    expect(calls.map(call => call.code)).toEqual(['const a', 'def f', 'const ab'])
    expect(styled(view, 'sk-py')).toEqual(['def'])
  })

  it('repaints under a new theme with the old colours until the new tokens arrive', async () => {
    const original = syntaxThemePair()
    try {
      const { view, calls } = track(setup([
        { language: 'typescript', text: 'const a' },
      ]))
      await calls[0]!.answer(firstWordTokens('const a', 'sk-light'))

      const other = { light: 'nord', dark: 'nord' }
      setSyntaxThemePair(other)
      view.dispatch(view.state.tr.setMeta(CODE_HIGHLIGHT_REFRESH, true))

      expect(calls).toHaveLength(2)
      expect(calls[1]!.pair).toEqual(other)
      expect(styled(view, 'sk-light')).toEqual(['const'])

      await calls[1]!.answer(firstWordTokens('const a', 'sk-nord'))
      expect(styled(view, 'sk-nord')).toEqual(['const'])
    }
    finally {
      setSyntaxThemePair(original)
    }
  })

  it('waits for an IME composition to end before it repaints', async () => {
    const { view, calls } = track(setup([
      { language: 'typescript', text: 'const a' },
    ]))
    Object.defineProperty(view, 'composing', {
      configurable: true,
      get: () => true,
    })

    await calls[0]!.answer(firstWordTokens('const a', 'sk-late'))
    expect(styled(view, 'sk-late')).toEqual([])

    Object.defineProperty(view, 'composing', {
      configurable: true,
      get: () => false,
    })
    view.dom.dispatchEvent(new Event('compositionend'))

    expect(styled(view, 'sk-late')).toEqual(['const'])
  })

  it('drops the decorations of a block whose language label is removed', async () => {
    const { view, calls } = track(setup([
      { language: 'typescript', text: 'const a' },
    ]))
    await calls[0]!.answer(firstWordTokens('const a', 'sk-gone'))

    view.dispatch(view.state.tr.setNodeMarkup(0, undefined, { language: '' }))

    expect(styled(view, 'sk-gone')).toEqual([])
  })
})
