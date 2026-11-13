import type { Node as ProseNode } from '@milkdown/prose/model'
import type { EditorView } from '@milkdown/prose/view'
import type { CodeTokenCache, CodeTokens } from './codeTokenCache'
import type { SyntaxThemePair } from '~/lib/syntaxThemes'
import type { CachedToken } from '~/lib/tokenCache'
import { Plugin, PluginKey } from '@milkdown/prose/state'
import { Mapping } from '@milkdown/prose/transform'
import { Decoration, DecorationSet } from '@milkdown/prose/view'
import { resolveBundledLang } from '~/lib/bundledLanguage'
import { syntaxThemeKey, syntaxThemePair } from '~/lib/shikiThemes'
import { codeTokenKey, createCodeTokenCache } from './codeTokenCache'

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
//
// New tokens for a block replace its decorations through one
// `DecorationSet.create` over the decorations of every block, which costs
// O(n log n) for n decorations. `DecorationSet.remove` cannot do this job: it
// compares each removed decoration with every decoration of the block, so it
// took 467 ms for the 9,000 decorations of a block of 1,300 lines.
// ---------------------------------------------------------------------------

/**
 * The transaction meta that makes the plugin highlight again without a document
 * change: tokens arrived, or the syntax theme changed. Dispatch it through
 * `refreshCodeHighlight`, which keeps it out of an IME composition.
 */
export const CODE_HIGHLIGHT_REFRESH = 'code-highlight-refresh'

/**
 * How many times the plugin asks the worker for the tokens of one text before
 * it shows the text plain. A grammar or theme chunk that fails to load, or a
 * worker crash, fails one request, and a later request can succeed. A text that
 * fails each time must not make the plugin ask without end.
 */
const MAX_TOKENIZE_ATTEMPTS = 3

/**
 * The longest total key length of the texts that an editor shows plain after
 * their last failed attempt: 64 Ki characters. Such a text costs its key and no
 * tokens.
 */
const PLAIN_TEXTS_MAX_KEY_LENGTH = 64 * 1024

export interface CodeHighlightOptions {
  /** The language of a code block: its label, or a guess from its text. */
  languageExtractor: (node: { attrs: Record<string, unknown>, textContent: string }) => string | undefined
  /**
   * Tokenize `code` under `pair`, off the main thread. Null, or a rejection,
   * when it could not. The plugin then asks again, MAX_TOKENIZE_ATTEMPTS times
   * in all for one text, and shows the text plain after the last failure.
   */
  tokenize: (lang: string, code: string, pair: SyntaxThemePair) => Promise<CachedToken[][] | null>
  /**
   * The tokens of the texts that the worker answered. Give every editor of the
   * session the same cache: an editor that mounts again then paints its code
   * blocks from it, without waiting for the worker.
   */
  tokenCache: CodeTokenCache
}

/** A code block of the document. */
interface TrackedBlock {
  /**
   * The node, compared by identity: ProseMirror keeps the node object of a
   * block that a transaction did not change.
   */
  node: ProseNode
  /**
   * Whether its decorations show its text under the theme of the state. False
   * while the block waits for the tokens of its text.
   */
  current: boolean
}

/** A block that shows older decorations while it waits for the tokens of its text. */
export interface PendingBlock {
  lang: string
  code: string
  /** The key of the text in the token cache, under the theme of the state. */
  key: string
}

export interface CodeHighlightState {
  decorations: DecorationSet
  /** The syntax theme (a `syntaxThemeKey`) that the decorations of the current blocks show. */
  themeKey: string
  /** Every code block, by position. */
  blocks: Map<number, TrackedBlock>
  /** The blocks that wait for tokens, in document order. */
  pending: PendingBlock[]
}

/** What a code block node asks of the worker under one syntax theme: its text, or nothing when it has no language. */
interface NodeText {
  themeKey: string
  text: PendingBlock | undefined
}

export const codeHighlightKey = new PluginKey<CodeHighlightState>('code-highlight')

/** One inline decoration per styled token, from the first character of the block. */
function tokenDecorations(blockPos: number, tokens: CodeTokens): Decoration[] {
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

/** The views whose refresh waits for the end of an IME composition. */
const refreshAfterComposition = new WeakSet<EditorView>()

/**
 * Make the highlight plugin of `view` highlight again: tokens arrived, or the
 * syntax theme changed.
 *
 * The plugin keys each block on the theme as well as its text, so a refresh
 * after a theme change finds every block out of date. Each block keeps the
 * colours of the abandoned theme until the worker answers for the new one, so
 * nothing flashes to plain.
 *
 * A transaction during an IME composition disturbs it, so the refresh waits for
 * the composition to end. It waits rather than drops: a dropped refresh after a
 * theme change would leave every code block in the abandoned theme's colours.
 * However many refreshes ask during one composition, the view refreshes once at
 * its end.
 */
export function refreshCodeHighlight(view: EditorView): void {
  if (view.isDestroyed)
    return
  if (view.composing) {
    if (refreshAfterComposition.has(view))
      return
    refreshAfterComposition.add(view)
    // On the view's own DOM, so that the listener goes away with the view.
    // ProseMirror's own `compositionend` handler runs first, because the view
    // registers it when it is created, and it ends `view.composing`.
    view.dom.addEventListener('compositionend', () => {
      refreshAfterComposition.delete(view)
      refreshCodeHighlight(view)
    }, { once: true })
    return
  }
  view.dispatch(view.state.tr.setMeta(CODE_HIGHLIGHT_REFRESH, true))
}

/** The state before the first document: no block, and no decoration. */
function emptyState(): CodeHighlightState {
  return {
    decorations: DecorationSet.empty,
    themeKey: '',
    blocks: new Map(),
    pending: [],
  }
}

/** Create the plugin. `options.tokenize` runs in the token worker. */
export function createCodeHighlightPlugin(options: CodeHighlightOptions): Plugin<CodeHighlightState> {
  // A node never changes, so what it asks of the worker holds for as long as
  // the theme does. A block that waits through many transactions then costs
  // one lookup in each, rather than a language guess over its whole text and a
  // copy of that text into a new key.
  const textByNode = new WeakMap<ProseNode, NodeText>()

  // The texts that failed MAX_TOKENIZE_ATTEMPTS times, which this editor shows
  // plain. They stay out of the shared cache, because the failure can be
  // transient: an editor that mounts again asks for them again.
  const plainTexts = createCodeTokenCache(PLAIN_TEXTS_MAX_KEY_LENGTH)

  /** The tokens of the text of `key`: the worker's answer, none after its last failure, or undefined while it waits. */
  const answerOf = (key: string): CodeTokens | undefined => options.tokenCache.get(key) ?? plainTexts.get(key)

  /** Whether the text of `key` waits for no answer. */
  const isAnswered = (key: string): boolean => options.tokenCache.has(key) || plainTexts.has(key)

  function textOf(node: ProseNode, themeKey: string): PendingBlock | undefined {
    const memo = textByNode.get(node)
    if (memo?.themeKey === themeKey)
      return memo.text
    const label = options.languageExtractor(node)
    const lang = label === undefined
      ? undefined
      : resolveBundledLang(label)
    let text: PendingBlock | undefined
    if (lang !== undefined) {
      const code = node.textContent
      text = {
        lang,
        code,
        key: codeTokenKey(themeKey, lang, code),
      }
    }
    textByNode.set(node, { themeKey, text })
    return text
  }

  function highlight(doc: ProseNode, previous: CodeHighlightState, mapping: Mapping): CodeHighlightState {
    const themeKey = syntaxThemeKey()
    const mapped = previous.decorations.map(mapping, doc)
    // A block that the transaction removed, or turned into another node, can
    // leave its mapped decorations on text that no code block holds: Backspace
    // at the start of a code block joins its text to the paragraph before it.
    // Building the set again from the code blocks drops them.
    let rebuild = false

    // The positions of the up-to-date blocks that kept their node and their
    // theme. They keep their decorations.
    const kept = new Set<number>()
    for (const [pos, block] of previous.blocks) {
      const result = mapping.mapResult(pos)
      const node = result.deleted
        ? null
        : doc.nodeAt(result.pos)
      if (node?.type.name !== 'code_block') {
        rebuild = true
        continue
      }
      // By identity, not with `eq`: a step that replaces part of the text with
      // the same text makes an equal node, but the mapping dropped the
      // decorations of the replaced range.
      if (block.current && node === block.node && previous.themeKey === themeKey)
        kept.add(result.pos)
    }

    const blocks = new Map<number, TrackedBlock>()
    const pending: PendingBlock[] = []
    // The new decorations of the blocks that change them, by position.
    const fresh = new Map<number, Decoration[]>()
    doc.descendants((node, pos) => {
      if (node.type.name !== 'code_block') {
        // A textblock holds inline content only, so no code block below it.
        return !node.isTextblock
      }
      if (kept.has(pos)) {
        blocks.set(pos, { node, current: true })
        return false
      }
      // A block with no language shows no tokens.
      let tokens: CodeTokens = []
      const text = textOf(node, themeKey)
      if (text !== undefined) {
        const cached = answerOf(text.key)
        if (cached === undefined) {
          // Keep the mapped decorations of the older text until these arrive.
          blocks.set(pos, { node, current: false })
          pending.push(text)
          return false
        }
        tokens = cached
      }
      blocks.set(pos, { node, current: true })
      const decorations = tokenDecorations(pos, tokens)
      // A block that shows no decoration before and after needs no new set,
      // such as a block with no language after a keystroke.
      if (decorations.length > 0 || mapped.find(pos, pos + node.nodeSize).length > 0) {
        fresh.set(pos, decorations)
        rebuild = true
      }
      return false
    })

    if (!rebuild) {
      return {
        decorations: mapped,
        themeKey,
        blocks,
        pending,
      }
    }
    const all: Decoration[] = []
    for (const [pos, block] of blocks) {
      // A loop, not `push(...)`: a block of thousands of decorations would pass
      // each one as an argument.
      for (const decoration of fresh.get(pos) ?? mapped.find(pos, pos + block.node.nodeSize))
        all.push(decoration)
    }
    return {
      decorations: DecorationSet.create(doc, all),
      themeKey,
      blocks,
      pending,
    }
  }

  /** The plugin view: it asks the worker for the tokens of one waiting block at a time. */
  function requestTokens(view: EditorView) {
    let inFlight = false
    let destroyed = false
    // The text whose answer failed last, and how many answers for it failed in
    // a row. The plugin asks for the first waiting block of the document, so it
    // asks again for a text that failed until it gets an answer, gives up, or
    // the block holds another text.
    let failed: { key: string, attempts: number } | undefined

    const requestNext = () => {
      if (inFlight || destroyed)
        return
      const state = codeHighlightKey.getState(view.state)
      // The keys of the waiting blocks hold the theme of the state. A theme
      // change waits for its refresh, which keys the blocks on the new theme.
      if (state === undefined || state.themeKey !== syntaxThemeKey())
        return
      // A text whose refresh waits, such as one that an IME composition holds
      // back, has its answer already.
      const next = state.pending.find(block => !isAnswered(block.key))
      if (next === undefined)
        return
      inFlight = true
      void options.tokenize(next.lang, next.code, syntaxThemePair())
        .catch(() => null)
        .then((answer) => {
          inFlight = false
          if (answer !== null) {
            options.tokenCache.set(next.key, answer)
          }
          else {
            const attempts = (failed?.key === next.key
              ? failed.attempts
              : 0) + 1
            if (attempts < MAX_TOKENIZE_ATTEMPTS) {
              failed = { key: next.key, attempts }
              requestNext()
              return
            }
            // The last attempt failed: the text shows plain, rather than the
            // plugin asking for it without end.
            plainTexts.set(next.key, [])
          }
          if (failed?.key === next.key)
            failed = undefined
          if (!destroyed)
            refreshCodeHighlight(view)
          // A refresh that waits for a composition asks for nothing, so ask for
          // the next block here. A dispatched refresh already asked through the
          // update of the view, and then this call does nothing.
          requestNext()
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
      init: (_config, state) => highlight(state.doc, emptyState(), new Mapping()),
      apply: (tr, previous) => {
        if (!tr.docChanged && tr.getMeta(CODE_HIGHLIGHT_REFRESH) === undefined)
          return previous
        return highlight(tr.doc, previous, tr.mapping)
      },
    },
    view: requestTokens,
    props: {
      decorations: state => codeHighlightKey.getState(state)?.decorations,
    },
  })
}
