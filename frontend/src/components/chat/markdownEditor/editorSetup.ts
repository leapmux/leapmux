import type { Ctx } from '@milkdown/ctx'
import type { Node, NodeType, Schema } from '@milkdown/prose/model'
import type { EditorView } from '@milkdown/prose/view'
import type { Setter } from 'solid-js'
import type { TrailingDebounced } from '~/lib/debounce'
import type { CodeLangHandlers } from '~/lib/editor/codeLangPlugin'
import type { PluginRefs } from '~/lib/editor/keyboardPlugins'
import type { LinkClickHandlers } from '~/lib/editor/linkPlugin'
import { defaultValueCtx, Editor, editorViewCtx, editorViewOptionsCtx, rootCtx } from '@milkdown/core'
import { clipboard } from '@milkdown/plugin-clipboard'
import { highlight, highlightPluginConfig } from '@milkdown/plugin-highlight'
import { history } from '@milkdown/plugin-history'
import { listener, listenerCtx } from '@milkdown/plugin-listener'
import {
  commonmark,
  createCodeBlockInputRule as milkdownCreateCodeBlockInputRule,
  emphasisStarInputRule as milkdownEmphasisStarInputRule,
  emphasisUnderscoreInputRule as milkdownEmphasisUnderscoreInputRule,
  inlineCodeInputRule as milkdownInlineCodeInputRule,
  insertHrInputRule as milkdownInsertHrInputRule,
  strongInputRule as milkdownStrongInputRule,
} from '@milkdown/preset-commonmark'
import { gfm, strikethroughInputRule as milkdownStrikethroughInputRule } from '@milkdown/preset-gfm'
import { Plugin, PluginKey } from '@milkdown/prose/state'
import { $prose } from '@milkdown/utils'
import { createHighlightPlugin } from 'prosemirror-highlight'
import { trailingDebounce } from '~/lib/debounce'
import { createAutoDetectLanguageExtractor, createCodeLangPlugin } from '~/lib/editor/codeLangPlugin'
import { saveDraft } from '~/lib/editor/draftPersistence'
import { createLinkBoundaryPlugin, createListItemEnterPlugin, createMarkdownPastePlugin, createSelectionWrapPlugin } from '~/lib/editor/inputPlugins'
import { createBulletListAfterHardBreakInputRule, createCodeBlockInputRule, createEmphasisStarInputRule, createEmphasisUnderscoreInputRule, createHrInputRule, createInlineCodeInputRule, createLinkInputRule, createOrderedListAfterHardBreakInputRule, createStrikethroughInputRule, createStrongInputRule } from '~/lib/editor/inputRules'
import {
  createBlockquoteBackspacePlugin,
  createCodeBlockBackspacePlugin,
  createCodeBlockEnterPlugin,
  createCodeBlockEscapePlugin,
  createCodeSpanEscapePlugin,
  createListDeleteFixPlugin,
  createPlaceholderPlugin,
  createSelectAllPlugin,
  createSendOnEnterPlugin,
  createSuppressTextSubstitutionPlugin,
  createTabKeyPlugin,
} from '~/lib/editor/keyboardPlugins'
import { createLazyShikiParser } from '~/lib/editor/lazyShikiParser'
import { createLinkClickPlugin, createLinkShortcutPlugin } from '~/lib/editor/linkPlugin'
import { createLazyOnigurumaHighlighter } from '~/lib/shikiLazyHighlighter'

// One Oniguruma-backed highlighter shared across all editor mounts. Created
// lazily (cheap closure here; the WASM engine + grammars load only when the
// first code block is highlighted), so opening the composer isn't blocked.
let editorHighlighter: ReturnType<typeof createLazyOnigurumaHighlighter> | undefined
function getEditorHighlighter(): ReturnType<typeof createLazyOnigurumaHighlighter> {
  editorHighlighter ??= createLazyOnigurumaHighlighter()
  return editorHighlighter
}

/**
 * The node type whose presence as the only block keeps the composer collapsed.
 * Everything else — a code block, blockquote, heading, list, table — renders on
 * more than one line.
 *
 * Resolved from the document's own schema and compared by IDENTITY, not against
 * a string literal. A literal that stops matching fails in the dangerous
 * direction: every test returns false, the box never expands, and the text runs
 * under the overlaid buttons with nothing to say why — which is exactly how the
 * old `hard_break` literal (Milkdown spells it `hardbreak`) disabled Shift+Enter
 * expansion. An absent or renamed type here yields `undefined`, which no node
 * type equals, so the box expands ALWAYS: visible, and safe.
 */
function paragraphType(schema: Schema): NodeType | undefined {
  return schema.nodes.paragraph
}

/**
 * Stats computed from the transaction's new document at `state.apply` time,
 * so layout decisions can read post-transaction state without querying the
 * (still-stale) view.
 */
export interface DocStats {
  /**
   * Whether the document needs the expanded composer layout whatever its text
   * width: it has more than one top-level block, it contains a hard break
   * (Shift+Enter), or its first block is not a plain paragraph (code block,
   * blockquote, heading, list, table).
   */
  multiLine: boolean
  /**
   * The document's plain text, for width measurement. Empty when `multiLine`
   * is true: such a document always expands, so its width is never consulted,
   * and serializing the whole document on every keystroke would be wasted
   * work.
   */
  text: string
}

/**
 * Classify a document for the composer's expand/collapse decision.
 *
 * The cheap structural test runs first and short-circuits, so the
 * full-document serialization happens only for a single-paragraph document —
 * the one case whose text width actually decides the layout.
 */
export function computeDocStats(doc: Node): DocStats {
  const firstChild = doc.firstChild
  if (doc.childCount > 1 || (!!firstChild && firstChild.type !== paragraphType(doc.type.schema)))
    return { multiLine: true, text: '' }
  const text = doc.textBetween(0, doc.content.size)
  // One newline test covers BOTH ways a single paragraph occupies two lines,
  // because the editor sets `white-space: pre-wrap`:
  //
  //   - a hard break (Shift+Enter). Milkdown's `hardbreak` node declares
  //     `leafText: () => '\n'`, and this 2-argument `textBetween` form falls
  //     through to the node spec's own `leafText`, so the break arrives here as
  //     a newline character. A separate walk for the node type would only
  //     re-derive what the schema already states.
  //   - a literal newline typed through an insertText path.
  //
  // Without this the width probe measures one line, and a visibly two-line
  // document stays in the collapsed layout with its second line under the
  // overlaid buttons.
  if (text.includes('\n'))
    return { multiLine: true, text: '' }
  return { multiLine: false, text }
}

/** Options for building the Milkdown editor. */
export interface EditorSetupOptions {
  /** The DOM element to mount the editor into. */
  editorRoot: HTMLElement
  /** Initial markdown content for the editor. */
  initialContent: string
  /** Mutable-ref accessors used by keyboard/placeholder plugins. */
  pluginRefs: PluginRefs
  /** Called when Shift+Tab is pressed in a plain paragraph. */
  getOnTogglePlanMode: () => (() => void) | undefined
  /** Code-language popover state setters + getters (getters enable toggle-on-reclick). */
  codeLangHandlers: CodeLangHandlers
  /** Link-edit popover state setters + getters (getters enable toggle-on-reclick). */
  linkClickHandlers: LinkClickHandlers
  /** Markdown signal setter (called on every document change). */
  setMarkdown: Setter<string>
  /** Optional callback when content changes (has content / empty). */
  onContentChange?: (hasContent: boolean) => void
  /**
   * Synchronous callback fired on every ProseMirror document-changing
   * transaction (NOT debounced). Used for layout decisions that must land
   * before the browser paints (e.g. the composer's expand/collapse switch).
   * The Milkdown `markdownUpdated` listener is debounced 200ms, which is too
   * late for pre-paint decisions.
   *
   * Receives the stats of the transaction's new document (`tr.doc`, which is
   * already updated at `state.apply` time), so the callback sees the
   * post-transaction state without querying the view (whose state is stale at
   * this point in the dispatch cycle).
   */
  onDocTransaction?: (docStats: DocStats) => void
  /** Returns the current draft key, or undefined if drafts are disabled. */
  getDraftKey: () => string | undefined
  /** Mutable ref holding the debounced draft-save handle (cancellable on cleanup). */
  draftSaveDebounce: { current: TrailingDebounced | undefined }
  /** Getter for the current editor instance (used inside the listener for cursor saving). */
  getEditorInstance: () => Editor | undefined
}

/**
 * Build and create a fully configured Milkdown editor instance.
 *
 * Returns a `Promise<Editor>` that resolves once the editor is mounted.
 */
export function buildEditor(opts: EditorSetupOptions): Promise<Editor> {
  const placeholderPlugin = createPlaceholderPlugin(opts.pluginRefs)
  const selectAllPlugin = createSelectAllPlugin()
  const sendPlugin = createSendOnEnterPlugin(opts.pluginRefs)
  const blockquoteBackspacePlugin = createBlockquoteBackspacePlugin()
  const tabKeyPlugin = createTabKeyPlugin({
    onShiftTabInParagraph: () => opts.getOnTogglePlanMode()?.(),
  })
  const codeBlockBackspacePlugin = createCodeBlockBackspacePlugin()
  const codeBlockEnterPlugin = createCodeBlockEnterPlugin()
  const codeBlockEscapePlugin = createCodeBlockEscapePlugin()
  const suppressTextSubstitutionPlugin = createSuppressTextSubstitutionPlugin()
  const listItemEnterPlugin = createListItemEnterPlugin(opts.pluginRefs)
  const codeLangPlugin = createCodeLangPlugin(opts.codeLangHandlers)
  const linkClickPlugin = createLinkClickPlugin(opts.linkClickHandlers)
  const linkShortcutPlugin = createLinkShortcutPlugin(opts.linkClickHandlers)
  const listDeleteFixPlugin = createListDeleteFixPlugin()
  const codeSpanEscapePlugin = createCodeSpanEscapePlugin()
  const markdownPastePlugin = createMarkdownPastePlugin()
  const linkBoundaryPlugin = createLinkBoundaryPlugin()
  const selectionWrapPlugin = createSelectionWrapPlugin()
  // Synchronous document-change notifier: fires on every doc-changing
  // transaction (NOT debounced, unlike Milkdown's markdownUpdated listener
  // which waits 200ms). The composer's expand/collapse logic needs a pre-paint
  // signal so the layout mode can switch before the browser paints a two-line
  // wrap.
  //
  // It lives in a `view()` hook rather than in a plugin state field's `apply`.
  // ProseMirror treats `apply` as a pure function of the transaction and can
  // call it for a transaction that never reaches the view (any code that
  // previews a transaction with `state.apply` without dispatching it), which
  // would drive the layout from a document the user never sees. `view().update`
  // runs only for a committed state: `EditorView.updateStateInner` calls
  // `updatePluginViews` synchronously, so the callback still lands inside
  // `dispatch` and before the browser paints.
  const createDocTransactionPlugin = opts.onDocTransaction
    ? $prose(() => new Plugin({
        key: new PluginKey('doc-transaction-tick'),
        view: () => ({
          update: (view, prevState) => {
            // Fire only on a document change, matching the previous
            // `tr.docChanged` filter: ProseMirror reuses the doc object when a
            // transaction leaves it alone, so identity is the exact test.
            if (prevState.doc !== view.state.doc)
              opts.onDocTransaction?.(computeDocStats(view.state.doc))
          },
        }),
      }))
    : undefined
  const linkInputRule = createLinkInputRule()
  const codeBlockInputRule = createCodeBlockInputRule()
  const hrInputRule = createHrInputRule()
  const bulletListAfterHardBreakRule = createBulletListAfterHardBreakInputRule()
  const orderedListAfterHardBreakRule = createOrderedListAfterHardBreakInputRule()
  const strongInputRule = createStrongInputRule()
  const emphasisStarInputRule = createEmphasisStarInputRule()
  const emphasisUnderscoreInputRule = createEmphasisUnderscoreInputRule()
  const inlineCodeInputRule = createInlineCodeInputRule()
  const strikethroughInputRule = createStrikethroughInputRule()

  const shikiParser = createLazyShikiParser(getEditorHighlighter())
  const languageExtractor = createAutoDetectLanguageExtractor()

  const editor = Editor.make()
    .config((ctx: Ctx) => {
      ctx.set(rootCtx, opts.editorRoot)
      ctx.set(defaultValueCtx, opts.initialContent)
      ctx.set(highlightPluginConfig.key, { parser: shikiParser, languageExtractor })
      ctx.update(editorViewOptionsCtx, prev => ({
        ...prev,
        attributes: {
          spellcheck: 'false',
          autocorrect: 'off',
          autocapitalize: 'off',
        },
      }))
      let pendingMd = ''
      let pendingDraftKey = ''
      opts.draftSaveDebounce.current = trailingDebounce(() => {
        let cursor = -1
        try {
          opts.getEditorInstance()?.action((c: Ctx) => {
            cursor = c.get(editorViewCtx).state.selection.from
          })
        }
        catch { /* ignore */ }
        saveDraft(pendingDraftKey, pendingMd.trim(), cursor)
      }, 500)
      ctx.get(listenerCtx).markdownUpdated((_ctx, md) => {
        if (typeof md !== 'string')
          return
        opts.setMarkdown(md)
        opts.onContentChange?.(md.trim().length > 0)
        const draftKey = opts.getDraftKey()
        if (draftKey) {
          pendingMd = md
          pendingDraftKey = draftKey
          opts.draftSaveDebounce.current?.()
        }
        else {
          // Drafts disabled (or key cleared): drop any pending save so it
          // doesn't fire with a stale draft key.
          opts.draftSaveDebounce.current?.cancel()
        }
      })
    })
    .use(selectAllPlugin)
    .use(selectionWrapPlugin)
    .use(commonmark.filter(p =>
      p !== milkdownInsertHrInputRule
      && p !== milkdownCreateCodeBlockInputRule
      && p !== milkdownStrongInputRule
      && p !== milkdownEmphasisStarInputRule
      && p !== milkdownEmphasisUnderscoreInputRule
      && p !== milkdownInlineCodeInputRule,
    ))
    .use(gfm.filter(p => p !== milkdownStrikethroughInputRule))
    .use(history)
    .use(markdownPastePlugin)
    .use(clipboard)
    .use(highlight)
    .use(listener)
    .use(placeholderPlugin)
    .use(listItemEnterPlugin)
    .use(listDeleteFixPlugin)
    .use(tabKeyPlugin)
    .use(codeBlockEscapePlugin)
    .use(codeSpanEscapePlugin)
    .use(sendPlugin)
    .use(codeBlockEnterPlugin)
    .use(codeBlockBackspacePlugin)
    .use(blockquoteBackspacePlugin)
    .use(suppressTextSubstitutionPlugin)
    .use(codeLangPlugin)
    .use(linkClickPlugin)
    .use(linkShortcutPlugin)
    .use(linkBoundaryPlugin)
    .use(linkInputRule)
    .use(hrInputRule)
    .use(bulletListAfterHardBreakRule)
    .use(orderedListAfterHardBreakRule)
    .use(codeBlockInputRule)
    .use(strongInputRule)
    .use(emphasisStarInputRule)
    .use(emphasisUnderscoreInputRule)
    .use(inlineCodeInputRule)
    .use(strikethroughInputRule)

  if (createDocTransactionPlugin)
    editor.use(createDocTransactionPlugin)

  return editor.create()
}

/**
 * Repaint every highlighted code block in `view` under the CURRENT syntax theme.
 *
 * A fresh plugin INSTANCE, because nothing weaker repaints. prosemirror-highlight
 * keys its `DecorationCache` on the node, and `calculateDecoration` reads
 * through that cache -- so the supported `prosemirror-highlight-refresh` meta
 * recomputes nothing for a block whose node did not change, which is every
 * block when only the theme moved. `apply` also short-circuits unless the
 * transaction changed the document. A new plugin starts with an empty cache,
 * and `init` recomputes from scratch.
 *
 * This is plain ProseMirror `reconfigure`, not a reach into Milkdown: the
 * plugin is located by the key name `prosemirror-highlight` sets on itself, and
 * rebuilt from the same two inputs this module already owns.
 *
 * WHY REBUILDING IS SAFE HERE. A syntax-theme change reaches the app from the
 * Preferences dialog or the no-workspace empty state -- neither of which
 * coexists with an editable composer. The one path that
 * can fire while the composer is focused is an OS `prefers-color-scheme` flip
 * (or a cross-tab write) while the syntax theme is PINNED with `mode: 'system'`,
 * because that collapses the pair to one half. `reconfigure` preserves the
 * document and the selection, so the cost there is nil -- except during an IME
 * composition, which must not be interrupted.
 *
 * A composition DEFERS the repaint; it does not cancel it. Dropping it was
 * wrong for the reason the paragraph above gives: `DecorationCache` is keyed on
 * the NODE, so the composition-ending transaction recomputes only the block the
 * composition touched, and every OTHER code block in the composer kept the
 * abandoned theme's baked colours for the rest of the session. The
 * `compositionend` listener re-runs the repaint once, and it is registered on
 * the view's own DOM so it dies with the view.
 */
export function refreshEditorHighlight(view: EditorView): void {
  if (view.composing) {
    // Once, and only for this deferral: `{ once: true }` drops the listener
    // when it fires, so a burst of theme changes during one composition still
    // repaints a single time at its end.
    view.dom.addEventListener('compositionend', () => refreshEditorHighlight(view), { once: true })
    return
  }
  const plugins = view.state.plugins
  const index = plugins.findIndex(p => (p.spec.key as { key?: string } | undefined)?.key?.startsWith('prosemirror-highlight'))
  if (index < 0)
    return
  const next = plugins.slice()
  next[index] = createHighlightPlugin({
    parser: createLazyShikiParser(getEditorHighlighter()),
    languageExtractor: createAutoDetectLanguageExtractor(),
  })
  view.updateState(view.state.reconfigure({ plugins: next }))
}
