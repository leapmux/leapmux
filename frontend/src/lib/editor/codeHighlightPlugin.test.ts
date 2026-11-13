import type { Node as ProseNode } from '@milkdown/prose/model'
import type { Transaction } from '@milkdown/prose/state'
import type { CodeHighlightOptions } from './codeHighlightPlugin'
import type { CodeTokenCache } from './codeTokenCache'
import type { SyntaxThemePair } from '~/lib/syntaxThemes'
import type { CachedToken } from '~/lib/tokenCache'
import { Schema } from '@milkdown/prose/model'
import { EditorState, TextSelection } from '@milkdown/prose/state'
import { Decoration, EditorView } from '@milkdown/prose/view'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setSyntaxThemePair, syntaxThemeKey, syntaxThemePair } from '~/lib/shikiThemes'
import { CODE_HIGHLIGHT_REFRESH, codeHighlightKey, createCodeHighlightPlugin, refreshCodeHighlight } from './codeHighlightPlugin'
import { codeTokenKey, createCodeTokenCache } from './codeTokenCache'

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
  /** Resolve the call, then let the plugin store the answer and repaint. */
  answer: (tokens: CachedToken[][] | null) => Promise<void>
  /** Reject the call, then let the plugin handle the failure. */
  fail: (error: Error) => Promise<void>
}

/** A code block, or a paragraph. */
type BlockSpec = { language: string, text: string } | { paragraph: string }

interface SetupOptions {
  /** The cache of the plugin. A fresh one by default, so that tests do not share answers. */
  tokenCache?: CodeTokenCache
  languageExtractor?: CodeHighlightOptions['languageExtractor']
}

/** Run every reaction that a settled promise queued, the plugin's included. */
function settle(): Promise<void> {
  return new Promise(resolve => setTimeout(resolve))
}

function setup(blocks: BlockSpec[], opts: SetupOptions = {}) {
  const calls: TokenizeCall[] = []
  const tokenize = vi.fn((lang: string, code: string, pair: SyntaxThemePair) => new Promise<CachedToken[][] | null>((resolve, reject) => {
    calls.push({
      lang,
      code,
      pair,
      answer: async (tokens) => {
        resolve(tokens)
        await settle()
      },
      fail: async (error) => {
        reject(error)
        await settle()
      },
    })
  }))
  const plugin = createCodeHighlightPlugin({
    languageExtractor: opts.languageExtractor ?? (node => (node.attrs.language as string) || undefined),
    tokenize,
    tokenCache: opts.tokenCache ?? createCodeTokenCache(),
  })
  const doc = schema.nodes.doc!.create(null, blocks.map((block) => {
    const text = 'paragraph' in block
      ? block.paragraph
      : block.text
    const content = text === ''
      ? []
      : schema.text(text)
    return 'paragraph' in block
      ? schema.nodes.paragraph!.create(null, content)
      : schema.nodes.code_block!.create({ language: block.language }, content)
  }))
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

/** Tokens for each line of `text`: every word styled, every space plain. */
function everyWordTokens(text: string, className: string): CachedToken[][] {
  return text.split('\n').map(line => line.split(/( )/).filter(part => part !== '').map(part => (part === ' '
    ? { content: part }
    : { content: part, className })))
}

function styled(view: EditorView, className: string): string[] {
  return [
    ...view.dom.querySelectorAll(`.${className}`),
  ].map(el => el.textContent ?? '')
}

/** The position before the top-level block at `index`. */
function blockPos(view: EditorView, index: number): number {
  let pos = 0
  for (let i = 0; i < index; i++)
    pos += view.state.doc.child(i).nodeSize
  return pos
}

/** Insert `text` at the end of the top-level block at `index`. */
function typeAtEnd(view: EditorView, text: string, index = 0): void {
  const block = view.state.doc.child(index)
  view.dispatch(view.state.tr.insertText(text, blockPos(view, index) + 1 + block.content.size))
}

/** Pretend that an IME composition starts (`true`) or ends (`false`). */
function setComposing(view: EditorView, composing: boolean): void {
  Object.defineProperty(view, 'composing', {
    configurable: true,
    get: () => composing,
  })
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

  it('decorates each line of a multi-line block at the characters of its tokens', async () => {
    const code = 'function f() {\n  return 42\n}'
    const { view, calls } = track(setup([
      { language: 'typescript', text: code },
    ]))

    await calls[0]!.answer([
      [
        { content: 'function', className: 'sk-k' },
        { content: ' f() {' },
      ],
      [
        { content: '  ' },
        { content: 'return', className: 'sk-k' },
        { content: ' ' },
        { content: '42', className: 'sk-n' },
      ],
      [
        { content: '}' },
      ],
    ])

    // The block opens at 0, so its text starts at 1. Each newline takes one
    // position: line 2 starts at 16 and line 3 at 28.
    const ranges = codeHighlightKey.getState(view.state)!.decorations.find().map(decoration => ({
      from: decoration.from,
      to: decoration.to,
      text: view.state.doc.textBetween(decoration.from, decoration.to),
    }))
    expect(ranges).toEqual([
      { from: 1, to: 9, text: 'function' },
      { from: 18, to: 24, text: 'return' },
      { from: 25, to: 27, text: '42' },
    ])
    expect(styled(view, 'sk-k')).toEqual(['function', 'return'])
    expect(styled(view, 'sk-n')).toEqual(['42'])
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

  it('leaves a block whose language Shiki does not bundle plain, and asks for nothing', () => {
    const { view, calls } = track(setup([
      { language: 'not-a-language', text: 'plain text here' },
    ]))
    expect(calls).toHaveLength(0)
    expect(codeHighlightKey.getState(view.state)?.pending).toEqual([])
  })

  it('asks for the bundled id of a language label in another case', () => {
    const { calls } = track(setup([
      { language: 'TypeScript', text: 'const a' },
    ]))
    expect(calls.map(call => call.lang)).toEqual(['typescript'])
  })

  it.each([
    ['tokens', true],
    ['a failure', false],
  ])('asks for nothing more once the editor is destroyed, when %s lands', async (_label, answered) => {
    const tokenCache = createCodeTokenCache()
    const { view, calls } = setup([
      { language: 'typescript', text: 'const a' },
      { language: 'python', text: 'def f' },
    ], { tokenCache })
    view.destroy()

    await calls[0]!.answer(answered
      ? firstWordTokens('const a', 'sk-late')
      : null)

    expect(calls).toHaveLength(1)
    // The tokens still serve the next editor that shows the text.
    expect(tokenCache.has(codeTokenKey(syntaxThemeKey(), 'typescript', 'const a'))).toBe(answered)
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
      refreshCodeHighlight(view)

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

  it('drops the decorations of a block whose language label is removed', async () => {
    const { view, calls } = track(setup([
      { language: 'typescript', text: 'const a' },
    ]))
    await calls[0]!.answer(firstWordTokens('const a', 'sk-gone'))

    view.dispatch(view.state.tr.setNodeMarkup(0, undefined, { language: '' }))

    expect(styled(view, 'sk-gone')).toEqual([])
  })

  it('colours again a span that a paste replaced with the same text', async () => {
    const code = 'abc def ghi'
    const { view, calls } = track(setup([
      { language: 'typescript', text: code },
    ]))
    await calls[0]!.answer(everyWordTokens(code, 'sk-word'))
    expect(styled(view, 'sk-word')).toEqual(['abc', 'def', 'ghi'])

    // Replace "c de" (positions 3 to 7) with the same text. The mapping drops
    // the decorations inside the replaced range, and the block looks unchanged.
    view.dispatch(view.state.tr.insertText('c de', 3, 7))

    expect(view.state.doc.firstChild!.textContent).toBe(code)
    expect(styled(view, 'sk-word')).toEqual(['abc', 'def', 'ghi'])
    // The tokens of the text were known, so the worker was not asked again.
    expect(calls).toHaveLength(1)
  })

  it('drops the decorations of a block whose text joins a paragraph', async () => {
    const { view, calls } = track(setup([
      { paragraph: 'x' },
      { language: 'typescript', text: 'const a' },
    ]))
    await calls[0]!.answer(firstWordTokens('const a', 'sk-joined'))
    expect(styled(view, 'sk-joined')).toEqual(['const'])

    // Backspace at the start of the code block joins its text to the paragraph.
    view.dispatch(view.state.tr.join(blockPos(view, 1)))

    expect(view.state.doc.childCount).toBe(1)
    expect(view.state.doc.firstChild!.type.name).toBe('paragraph')
    expect(view.dom.querySelector('.shiki')).toBeNull()
  })

  it('paints the blocks of a new editor from the tokens that an earlier editor received', async () => {
    const tokenCache = createCodeTokenCache()
    const first = track(setup([
      { language: 'typescript', text: 'const a' },
    ], { tokenCache }))
    await first.calls[0]!.answer(firstWordTokens('const a', 'sk-warm'))

    const second = track(setup([
      { language: 'typescript', text: 'const a' },
    ], { tokenCache }))

    expect(styled(second.view, 'sk-warm')).toEqual(['const'])
    expect(second.calls).toHaveLength(0)
  })

  it('detects the language of a waiting block once while the block does not change', async () => {
    const extracted: ProseNode[] = []
    const { view, calls } = track(setup([
      { language: 'typescript', text: 'const a' },
      { language: 'python', text: 'def f' },
    ], {
      languageExtractor: (node) => {
        extracted.push(node as ProseNode)
        return (node.attrs.language as string) || undefined
      },
    }))
    const waiting = view.state.doc.child(1)

    // The first block's request is out, so the second block waits through
    // each of these edits.
    typeAtEnd(view, '1')
    typeAtEnd(view, '2')
    typeAtEnd(view, '3')

    expect(view.state.doc.child(1)).toBe(waiting)
    expect(extracted.filter(node => node === waiting)).toHaveLength(1)
    expect(calls).toHaveLength(1)
  })

  it('replaces the decorations of a large block in time linear in their count', async () => {
    const lines = 500
    const code = Array.from({ length: lines }).fill('aa bb cc').join('\n')
    const decorationCount = lines * 3
    const { view, calls } = track(setup([
      { language: 'typescript', text: code },
    ]))
    await calls[0]!.answer(everyWordTokens(code, 'sk-old'))
    typeAtEnd(view, ' dd')
    const edited = `${code} dd`

    // `Decoration.eq` is internal to ProseMirror, so the test counts its calls
    // through the prototype. A count rather than a spy: a spy would record
    // millions of argument lists against the quadratic code.
    const prototype = Decoration.prototype as unknown as { eq: (this: Decoration, ...args: unknown[]) => boolean }
    const eq = prototype.eq
    let eqCalls = 0
    prototype.eq = function (this: Decoration, ...args: unknown[]) {
      eqCalls++
      return eq.apply(this, args)
    }
    try {
      await calls[1]!.answer(everyWordTokens(edited, 'sk-new'))
    }
    finally {
      prototype.eq = eq
    }

    expect(styled(view, 'sk-new')).toHaveLength(decorationCount + 1)
    expect(styled(view, 'sk-old')).toEqual([])
    // A removal that compares each old decoration with every other one makes
    // about decorationCount² calls: 2,250,000 here.
    expect(eqCalls).toBeLessThan(decorationCount * 10)
  })

  describe('a failed answer', () => {
    it('asks again, and colours the block when an answer arrives', async () => {
      const { view, calls } = track(setup([
        { language: 'typescript', text: 'const a' },
      ]))

      await calls[0]!.answer(null)

      expect(calls.map(call => call.code)).toEqual(['const a', 'const a'])
      await calls[1]!.answer(firstWordTokens('const a', 'sk-retry'))
      expect(styled(view, 'sk-retry')).toEqual(['const'])
      expect(calls).toHaveLength(2)
    })

    it('shows a text plain after three failed answers, and asks nothing more for it', async () => {
      const { view, calls } = track(setup([
        { language: 'typescript', text: 'const a' },
      ]))
      await calls[0]!.answer(firstWordTokens('const a', 'sk-old'))
      typeAtEnd(view, 'b')

      await calls[1]!.answer(null)
      await calls[2]!.answer(null)
      // The block keeps the colours of its old text while the plugin asks again.
      expect(styled(view, 'sk-old')).toEqual(['const'])
      await calls[3]!.answer(null)

      expect(calls.slice(1).map(call => call.code)).toEqual(['const ab', 'const ab', 'const ab'])
      expect(view.dom.querySelector('.shiki')).toBeNull()
      refreshCodeHighlight(view)
      expect(calls).toHaveLength(4)
    })

    it('gives a new text of the block its own three attempts', async () => {
      const { view, calls } = track(setup([
        { language: 'typescript', text: 'const a' },
      ]))
      await calls[0]!.answer(null)
      await calls[1]!.answer(null)
      await calls[2]!.answer(null)
      expect(calls).toHaveLength(3)

      typeAtEnd(view, 'b')

      expect(calls.map(call => call.code)).toEqual([
        'const a',
        'const a',
        'const a',
        'const ab',
      ])
      await calls[3]!.answer(null)
      expect(calls).toHaveLength(5)
      await calls[4]!.answer(firstWordTokens('const ab', 'sk-later'))
      expect(styled(view, 'sk-later')).toEqual(['const'])
    })

    it('lets a new editor ask again for a text that an earlier editor shows plain', async () => {
      const tokenCache = createCodeTokenCache()
      const first = track(setup([
        { language: 'typescript', text: 'const a' },
      ], { tokenCache }))
      await first.calls[0]!.answer(null)
      await first.calls[1]!.answer(null)
      await first.calls[2]!.answer(null)
      expect(first.calls).toHaveLength(3)

      // A failure can be transient, so it stays with the editor that saw it.
      expect(tokenCache.has(codeTokenKey(syntaxThemeKey(), 'typescript', 'const a'))).toBe(false)
      const second = track(setup([
        { language: 'typescript', text: 'const a' },
      ], { tokenCache }))

      expect(second.calls).toHaveLength(1)
      await second.calls[0]!.answer(firstWordTokens('const a', 'sk-second'))
      expect(styled(second.view, 'sk-second')).toEqual(['const'])
    })

    it('keeps asking after a tokenize that rejects', async () => {
      const { view, calls } = track(setup([
        { language: 'typescript', text: 'const a' },
      ]))

      await calls[0]!.fail(new Error('the worker failed'))

      expect(calls).toHaveLength(2)
      await calls[1]!.answer(firstWordTokens('const a', 'sk-after'))
      expect(styled(view, 'sk-after')).toEqual(['const'])
    })
  })

  describe('an IME composition', () => {
    it('waits for the composition to end before it repaints', async () => {
      const { view, calls } = track(setup([
        { language: 'typescript', text: 'const a' },
      ]))
      setComposing(view, true)

      await calls[0]!.answer(firstWordTokens('const a', 'sk-late'))
      expect(styled(view, 'sk-late')).toEqual([])

      setComposing(view, false)
      view.dom.dispatchEvent(new Event('compositionend'))

      expect(styled(view, 'sk-late')).toEqual(['const'])
    })

    // The keys of the waiting blocks hold the theme of the state until the refresh
    // of a theme change re-keys them. A request under the new pair before that would
    // file the tokens of the new theme under a key of the old one.
    it('asks for nothing under a theme whose refresh waits for the end of the composition', async () => {
      const original = syntaxThemePair()
      try {
        const { view, calls } = track(setup([
          { language: 'typescript', text: 'const a' },
          { language: 'python', text: 'def f' },
        ]))
        setComposing(view, true)
        const other = { light: 'nord', dark: 'nord' }
        setSyntaxThemePair(other)
        refreshCodeHighlight(view)

        await calls[0]!.answer(firstWordTokens('const a', 'sk-old-theme'))

        expect(calls.map(call => call.code)).toEqual(['const a'])

        setComposing(view, false)
        view.dom.dispatchEvent(new Event('compositionend'))

        // The refresh keys every block on the new theme, and the plugin asks again from the first.
        expect(calls.slice(1).map(call => ({ code: call.code, pair: call.pair }))).toEqual([
          { code: 'const a', pair: other },
        ])
        await calls[1]!.answer(firstWordTokens('const a', 'sk-new-theme'))
        expect(calls.slice(2).map(call => ({ code: call.code, pair: call.pair }))).toEqual([
          { code: 'def f', pair: other },
        ])
        expect(styled(view, 'sk-new-theme')).toEqual(['const'])
      }
      finally {
        setSyntaxThemePair(original)
      }
    })

    it('asks nothing again for a text whose tokens wait for the end of the composition', async () => {
      const { view, calls } = track(setup([
        { language: 'typescript', text: 'const a' },
        { language: 'python', text: 'def f' },
      ]))
      setComposing(view, true)
      await calls[0]!.answer(firstWordTokens('const a', 'sk-ts'))

      // A transaction that changes no document, as a composition dispatches.
      view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, 2)))

      // The tokens of the first block wait in the cache, so the plugin asked
      // for the second block, and not for the first block again.
      expect(calls.map(call => call.code)).toEqual(['const a', 'def f'])

      setComposing(view, false)
      view.dom.dispatchEvent(new Event('compositionend'))
      await calls[1]!.answer(firstWordTokens('def f', 'sk-py'))

      expect(styled(view, 'sk-ts')).toEqual(['const'])
      expect(styled(view, 'sk-py')).toEqual(['def'])
      expect(calls).toHaveLength(2)
    })
  })
})

describe('refreshCodeHighlight', () => {
  /** A view stand-in: a real state, and a `dispatch` that records each transaction. */
  function stubView(composing: boolean) {
    const dom = document.createElement('div')
    const dispatched: Transaction[] = []
    const view = {
      composing,
      isDestroyed: false,
      dom,
      state: EditorState.create({ schema }),
      dispatch: (tr: Transaction) => {
        dispatched.push(tr)
      },
    }
    return { view, dom, dispatched }
  }

  it('dispatches the refresh of the highlight plugin', () => {
    const { view, dispatched } = stubView(false)

    refreshCodeHighlight(view as unknown as EditorView)

    expect(dispatched).toHaveLength(1)
    expect(dispatched[0]!.getMeta(CODE_HIGHLIGHT_REFRESH)).toBe(true)
    expect(dispatched[0]!.docChanged).toBe(false)
  })

  // A transaction during an IME composition would disturb it, so the refresh
  // waits over the composition and runs at `compositionend`. A dropped refresh
  // would leave every code block in the colours of the abandoned theme.
  it('defers the refresh while an IME composition is in flight, then runs it once', () => {
    const { view, dom, dispatched } = stubView(true)

    refreshCodeHighlight(view as unknown as EditorView)
    expect(dispatched).toHaveLength(0)

    view.composing = false
    dom.dispatchEvent(new Event('compositionend'))
    expect(dispatched).toHaveLength(1)

    // A second `compositionend` with no new refresh does not refresh again.
    dom.dispatchEvent(new Event('compositionend'))
    expect(dispatched).toHaveLength(1)
  })

  it('refreshes once for several refreshes during one composition', () => {
    const { view, dom, dispatched } = stubView(true)

    refreshCodeHighlight(view as unknown as EditorView)
    refreshCodeHighlight(view as unknown as EditorView)
    refreshCodeHighlight(view as unknown as EditorView)

    view.composing = false
    dom.dispatchEvent(new Event('compositionend'))
    expect(dispatched).toHaveLength(1)

    // The next composition defers its own refresh again.
    view.composing = true
    refreshCodeHighlight(view as unknown as EditorView)
    expect(dispatched).toHaveLength(1)
    view.composing = false
    dom.dispatchEvent(new Event('compositionend'))
    expect(dispatched).toHaveLength(2)
  })

  it('does nothing for a destroyed view', () => {
    const { view, dom, dispatched } = stubView(false)
    view.isDestroyed = true

    refreshCodeHighlight(view as unknown as EditorView)
    expect(dispatched).toHaveLength(0)

    // Nor when the view is destroyed while the refresh waits for a composition.
    view.isDestroyed = false
    view.composing = true
    refreshCodeHighlight(view as unknown as EditorView)
    view.isDestroyed = true
    view.composing = false
    dom.dispatchEvent(new Event('compositionend'))
    expect(dispatched).toHaveLength(0)
  })
})
