import type { Node as ProseNode } from '@milkdown/prose/model'
import type { Transaction } from '@milkdown/prose/state'
import type { EditorView } from '@milkdown/prose/view'
import type { SyntaxThemePair } from '~/lib/syntaxThemes'
import type { CachedToken } from '~/lib/tokenCache'
import { Plugin, PluginKey } from '@milkdown/prose/state'
import { Decoration, DecorationSet } from '@milkdown/prose/view'
import { lruGet, lruSet } from '~/lib/mapLru'
import { resolveBundledLang } from '~/lib/shikiLazyHighlighter'
import { syntaxThemeKey, syntaxThemePair } from '~/lib/shikiThemes'

// ---------------------------------------------------------------------------
// Code block highlighting in the composer
//
// The Shiki token worker tokenizes each code block; the main thread only turns
// tokens into decorations. Tokenizing on the main thread cost 36 ms per
// keystroke in a block of 500 lines and 120 ms in one of 1,300.
//
// Stale while revalidating: a block whose text or theme changed keeps its old
// decorations, mapped through the edit, until the tokens of its new text
// arrive. A block that dropped its decorations instead would flash to plain on
// every keystroke.
//
// One request at a time per editor: a block that takes the worker longer than
// a keystroke does not build a queue. When a reply comes back, the next request
// asks for the text that the block holds THEN.
// ---------------------------------------------------------------------------

/**
 * The transaction meta that makes the plugin highlight again without a document
 * change: tokens arrived, or the syntax theme changed.
 */
export const CODE_HIGHLIGHT_REFRESH = 'code-highlight-refresh'

/** How many tokenized texts the plugin keeps. Each keystroke makes a new text, and only the latest matter. */
const TOKEN_CACHE_SIZE = 32

export interface CodeHighlightOptions {
  /** The language of a code block: its label, or a guess from its text. */
  languageExtractor: (node: { attrs: Record<string, unknown>, textContent: string }) => string | undefined
  /**
   * Tokenize `code` under `pair`, off the main thread. Null when it could not,
   * which leaves the block plain.
   */
  tokenize: (lang: string, code: string, pair: SyntaxThemePair) => Promise<CachedToken[][] | null>
}

/** A block whose decorations match its text under the theme of `themeKey`. */
interface HighlightedBlock {
  node: ProseNode
  themeKey: string
}

/** A block that shows older decorations while it waits for the tokens of its text. */
export interface PendingBlock {
  lang: string
  code: string
}

export interface CodeHighlightState {
  decorations: DecorationSet
  /** The up-to-date blocks, by position. */
  highlighted: Map<number, HighlightedBlock>
  /** The blocks that wait for tokens, in document order. */
  pending: PendingBlock[]
}

export const codeHighlightKey = new PluginKey<CodeHighlightState>('code-highlight')

function isCodeBlock(node: ProseNode): boolean {
  return node.type.name === 'code_block'
}

/** One inline decoration per styled token, from the first character of the block. */
function tokenDecorations(blockPos: number, tokens: readonly (readonly CachedToken[])[]): Decoration[] {
  const decorations: Decoration[] = []
  let from = blockPos + 1
  for (const line of tokens) {
    for (const token of line) {
      const to = from + token.content.length
      if (token.className !== undefined && to > from)
        decorations.push(Decoration.inline(from, to, { class: `shiki ${token.className}` }))
      from = to
    }
    // The newline between two lines.
    from += 1
  }
  return decorations
}

/**
 * Create the plugin. `options.tokenize` runs in the token worker; the plugin
 * owns a small cache of its answers.
 */
export function createCodeHighlightPlugin(options: CodeHighlightOptions): Plugin<CodeHighlightState> {
  const tokensByText = new Map<string, CachedToken[][]>()
  const textKey = (themeKey: string, lang: string, code: string) => `${themeKey}\u0000${lang}\u0000${code}`

  const languageOf = (node: ProseNode): string | undefined => {
    const label = options.languageExtractor(node)
    return label === undefined
      ? undefined
      : resolveBundledLang(label)
  }

  function highlight(doc: ProseNode, previous: CodeHighlightState | undefined, tr: Transaction | undefined): CodeHighlightState {
    const themeKey = syntaxThemeKey()
    let decorations = previous === undefined
      ? DecorationSet.empty
      : tr === undefined
        ? previous.decorations
        : previous.decorations.map(tr.mapping, doc)

    // The blocks that kept their node and their theme keep their decorations.
    const kept = new Map<number, HighlightedBlock>()
    for (const [pos, block] of previous?.highlighted ?? []) {
      const mapped = tr?.mapping.mapResult(pos) ?? { pos, deleted: false }
      if (mapped.deleted || block.themeKey !== themeKey)
        continue
      if (doc.nodeAt(mapped.pos)?.eq(block.node) === true)
        kept.set(mapped.pos, block)
    }

    const highlighted = new Map<number, HighlightedBlock>()
    const pending: PendingBlock[] = []
    doc.descendants((node, pos) => {
      if (!isCodeBlock(node))
        return true
      const block = kept.get(pos)
      if (block !== undefined) {
        highlighted.set(pos, block)
        return false
      }
      const end = pos + node.nodeSize
      const lang = languageOf(node)
      if (lang === undefined) {
        decorations = decorations.remove(decorations.find(pos, end))
        highlighted.set(pos, { node, themeKey })
        return false
      }
      const code = node.textContent
      const tokens = lruGet(tokensByText, textKey(themeKey, lang, code))
      if (tokens === undefined) {
        // Keep the mapped decorations of the older text until these arrive.
        pending.push({ lang, code })
        return false
      }
      decorations = decorations.remove(decorations.find(pos, end)).add(doc, tokenDecorations(pos, tokens))
      highlighted.set(pos, { node, themeKey })
      return false
    })
    return { decorations, highlighted, pending }
  }

  /** The plugin view: it asks the worker for the tokens of one waiting block at a time. */
  function requestTokens(view: EditorView) {
    let inFlight = false
    let destroyed = false
    let refreshAfterComposition = false

    // A transaction during an IME composition disturbs it, so the refresh waits
    // for the composition to end.
    const refresh = () => {
      if (destroyed)
        return
      if (view.composing) {
        if (!refreshAfterComposition) {
          refreshAfterComposition = true
          view.dom.addEventListener('compositionend', () => {
            refreshAfterComposition = false
            refresh()
          }, { once: true })
        }
        return
      }
      view.dispatch(view.state.tr.setMeta(CODE_HIGHLIGHT_REFRESH, true))
    }

    const requestNext = () => {
      if (inFlight || destroyed)
        return
      const next = codeHighlightKey.getState(view.state)?.pending[0]
      if (next === undefined)
        return
      inFlight = true
      const pair = syntaxThemePair()
      const key = textKey(syntaxThemeKey(), next.lang, next.code)
      // A failed or empty answer stores no tokens, so the block shows plain
      // until its text changes, rather than asking again forever.
      void options.tokenize(next.lang, next.code, pair)
        .catch(() => null)
        .then((tokens) => {
          lruSet(tokensByText, key, tokens ?? [], TOKEN_CACHE_SIZE)
          inFlight = false
          refresh()
        })
    }

    requestNext()
    return {
      update: requestNext,
      destroy: () => {
        destroyed = true
      },
    }
  }

  return new Plugin<CodeHighlightState>({
    key: codeHighlightKey,
    state: {
      init: (_config, state) => highlight(state.doc, undefined, undefined),
      apply: (tr, previous) => {
        if (!tr.docChanged && tr.getMeta(CODE_HIGHLIGHT_REFRESH) === undefined)
          return previous
        return highlight(tr.doc, previous, tr)
      },
    },
    view: requestTokens,
    props: {
      decorations: state => codeHighlightKey.getState(state)?.decorations,
    },
  })
}
