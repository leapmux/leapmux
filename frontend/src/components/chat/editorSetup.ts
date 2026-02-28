import type { Ctx } from '@milkdown/ctx'
import type { Setter } from 'solid-js'
import type { PluginRefs } from '~/lib/editor/keyboardPlugins'
import type { ToolbarStateSetters } from '~/lib/editor/toolbarState'
import { defaultValueCtx, Editor, editorViewCtx, editorViewOptionsCtx, rootCtx } from '@milkdown/core'
import { clipboard } from '@milkdown/plugin-clipboard'
import { highlight, highlightPluginConfig } from '@milkdown/plugin-highlight'
import { createParser as createShikiParser } from '@milkdown/plugin-highlight/shiki'
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
  createSendOnEnterPlugin,
  createSuppressTextSubstitutionPlugin,
  createTabKeyPlugin,
} from '~/lib/editor/keyboardPlugins'
import { createAutoDetectLanguageExtractor, createCodeLangPlugin, createToolbarStatePlugin } from '~/lib/editor/toolbarState'
import { shikiHighlighter } from '~/lib/renderMarkdown'

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
  /** Toolbar formatting state setters. */
  toolbarSetters: ToolbarStateSetters
  /** Code-language popover state setters. */
  codeLangSetters: {
    setCodeLangNodePos: Setter<number>
    setCodeLangAnchorEl: Setter<HTMLElement | undefined>
    setCodeLangPopoverOpen: Setter<boolean>
  }
  /** Markdown signal setter (called on every document change). */
  setMarkdown: Setter<string>
  /** Optional callback when content changes (has content / empty). */
  onContentChange?: (hasContent: boolean) => void
  /** Returns the current draft key, or undefined if drafts are disabled. */
  getDraftKey: () => string | undefined
  /** Mutable ref holding the current draft-save timeout ID. */
  draftSaveTimer: { current: ReturnType<typeof setTimeout> | undefined }
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
  const toolbarStatePlugin = createToolbarStatePlugin(opts.toolbarSetters)
  const codeLangPlugin = createCodeLangPlugin(opts.codeLangSetters)
  const listDeleteFixPlugin = createListDeleteFixPlugin()
  const codeSpanEscapePlugin = createCodeSpanEscapePlugin()
  const markdownPastePlugin = createMarkdownPastePlugin()
  const linkBoundaryPlugin = createLinkBoundaryPlugin()
  const selectionWrapPlugin = createSelectionWrapPlugin()
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

  const shikiParser = createShikiParser(shikiHighlighter, {
    themes: { light: 'github-light', dark: 'github-dark' },
    defaultColor: false,
  })
  const languageExtractor = createAutoDetectLanguageExtractor(shikiHighlighter)

  return Editor.make()
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
      ctx.get(listenerCtx).markdownUpdated((_ctx, md) => {
        opts.setMarkdown(md)
        opts.onContentChange?.(md.trim().length > 0)
        // Debounced draft save
        const draftKey = opts.getDraftKey()
        if (draftKey) {
          clearTimeout(opts.draftSaveTimer.current)
          opts.draftSaveTimer.current = setTimeout(() => {
            let cursor = -1
            try {
              opts.getEditorInstance()?.action((c: Ctx) => {
                cursor = c.get(editorViewCtx).state.selection.from
              })
            }
            catch { /* ignore */ }
            saveDraft(draftKey, md.trim(), cursor)
          }, 500)
        }
      })
    })
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
    .use(toolbarStatePlugin)
    .use(codeLangPlugin)
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
    .create()
}
