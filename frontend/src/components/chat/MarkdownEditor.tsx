import type { Ctx } from '@milkdown/ctx'
import type { Component, JSX } from 'solid-js'
import { defaultValueCtx, Editor, editorViewCtx, editorViewOptionsCtx, rootCtx, serializerCtx } from '@milkdown/core'
import { clipboard } from '@milkdown/plugin-clipboard'
import { highlight, highlightPluginConfig } from '@milkdown/plugin-highlight'
import { createParser as createShikiParser } from '@milkdown/plugin-highlight/shiki'
import { history } from '@milkdown/plugin-history'
import { listener, listenerCtx } from '@milkdown/plugin-listener'
import {
  commonmark,
  createCodeBlockCommand,
  createCodeBlockInputRule as milkdownCreateCodeBlockInputRule,
  emphasisStarInputRule as milkdownEmphasisStarInputRule,
  emphasisUnderscoreInputRule as milkdownEmphasisUnderscoreInputRule,
  inlineCodeInputRule as milkdownInlineCodeInputRule,
  insertHrInputRule as milkdownInsertHrInputRule,
  strongInputRule as milkdownStrongInputRule,
  toggleInlineCodeCommand,
} from '@milkdown/preset-commonmark'
import { gfm, strikethroughInputRule as milkdownStrikethroughInputRule } from '@milkdown/preset-gfm'
import { TextSelection } from '@milkdown/prose/state'
import { callCommand, replaceAll } from '@milkdown/utils'
import { createEffect, createSignal, on, onCleanup, onMount } from 'solid-js'
import { clearDraft, loadDraft, saveDraft } from '~/lib/editor/draftPersistence'
import { createBulletListAfterHardBreakInputRule, createCodeBlockInputRule, createEmphasisStarInputRule, createEmphasisUnderscoreInputRule, createHrInputRule, createInlineCodeInputRule, createLinkInputRule, createOrderedListAfterHardBreakInputRule, createStrikethroughInputRule, createStrongInputRule } from '~/lib/editor/inputRules'
import {
  createBlockquoteBackspacePlugin,
  createCodeBlockBackspacePlugin,
  createCodeBlockEnterPlugin,
  createCodeBlockEscapePlugin,
  createCodeSpanEscapePlugin,
  createLinkBoundaryPlugin,
  createListDeleteFixPlugin,
  createListItemEnterPlugin,
  createMarkdownPastePlugin,
  createPlaceholderPlugin,
  createSelectionWrapPlugin,
  createSendOnEnterPlugin,
  createSuppressTextSubstitutionPlugin,
  createTabKeyPlugin,
} from '~/lib/editor/plugins'
import { createAutoDetectLanguageExtractor, createCodeLangPlugin, createToolbarStatePlugin } from '~/lib/editor/toolbarState'
import { shikiHighlighter } from '~/lib/renderMarkdown'
import { safeGetString, safeSetString } from '~/lib/safeStorage'
import { CodeLanguagePopover } from './CodeLanguagePopover'
import { EditorToolbar } from './EditorToolbar'
import * as styles from './MarkdownEditor.css'

type EnterKeyMode = 'enter-sends' | 'cmd-enter-sends'

interface MarkdownEditorProps {
  /** Agent ID for per-tab draft persistence. */
  agentId?: string
  /** When set, drafts are stored under a control-request-specific key instead of the agentId key. */
  controlRequestId?: string
  onSend: (markdown: string) => boolean | void
  disabled?: boolean
  requestedHeight?: number
  maxHeight?: number
  onContentHeightChange?: (height: number) => void
  onContentChange?: (hasContent: boolean) => void
  sendRef?: (send: () => void) => void
  focusRef?: (focus: () => void) => void
  banner?: JSX.Element
  footer?: JSX.Element
  contentRef?: (get: () => string, set: (text: string) => void) => void
  placeholder?: string
  /** When true, pressing Enter with an empty editor calls onSend('') instead of doing nothing. */
  allowEmptySend?: boolean
  /** Called when Shift+Tab is pressed in a plain paragraph (indent level 0). */
  onTogglePlanMode?: () => void
}

const ENTER_KEY_MODE_KEY = 'leapmux-enter-key-mode'

function getEnterKeyMode(): EnterKeyMode {
  const stored = safeGetString(ENTER_KEY_MODE_KEY)
  return stored === 'enter-sends' ? 'enter-sends' : 'cmd-enter-sends'
}

export { clearDraft }

export const MarkdownEditor: Component<MarkdownEditorProps> = (props) => {
  let editorRef: HTMLDivElement | undefined
  let editorInstance: Editor | undefined
  const [enterMode, setEnterMode] = createSignal<EnterKeyMode>(getEnterKeyMode())
  const [_markdown, setMarkdown] = createSignal('')
  const [contentHeight, setContentHeight] = createSignal(0)

  /** Compute the localStorage draft key, incorporating controlRequestId when present. */
  const getDraftKey = () => {
    if (!props.agentId)
      return undefined
    return props.controlRequestId
      ? `${props.agentId}-ctrl-${props.controlRequestId}`
      : props.agentId
  }

  // Active format state signals
  const [activeBold, setActiveBold] = createSignal(false)
  const [activeItalic, setActiveItalic] = createSignal(false)
  const [activeStrikethrough, setActiveStrikethrough] = createSignal(false)
  const [activeCode, setActiveCode] = createSignal(false)
  const [activeCodeBlock, setActiveCodeBlock] = createSignal(false)
  const [activeBlockquote, setActiveBlockquote] = createSignal(false)
  const [activeLink, setActiveLink] = createSignal(false)
  const [activeHeadingLevel, setActiveHeadingLevel] = createSignal(0)
  const [activeBulletList, setActiveBulletList] = createSignal(false)
  const [activeOrderedList, setActiveOrderedList] = createSignal(false)
  const [activeTaskList, setActiveTaskList] = createSignal(false)

  // Enter mode tooltip: controlled so it stays open on click and updates content
  const [enterTooltipOpen, setEnterTooltipOpen] = createSignal(false)

  // Code block language popover state
  const [codeLangPopoverOpen, setCodeLangPopoverOpen] = createSignal(false)
  const [codeLangNodePos, setCodeLangNodePos] = createSignal(-1)
  const [codeLangAnchorEl, setCodeLangAnchorEl] = createSignal<HTMLElement | undefined>(undefined)
  const [codeLangFilter, setCodeLangFilter] = createSignal('')

  // Persist enter key mode
  const toggleEnterMode = () => {
    const next = enterMode() === 'enter-sends' ? 'cmd-enter-sends' : 'enter-sends'
    setEnterMode(next)
    safeSetString(ENTER_KEY_MODE_KEY, next)
  }

  const focusEditor = () => {
    if (!editorInstance)
      return
    try {
      editorInstance.action((ctx: Ctx) => {
        const view = ctx.get(editorViewCtx)
        view.focus()
      })
    }
    catch {
      // ignore
    }
  }

  const handleSend = () => {
    if (props.disabled || !editorInstance)
      return
    // Read markdown directly from ProseMirror's document state rather than
    // the `markdown` signal, which is updated by a debounced listener (200ms)
    // and may be stale when Enter is pressed immediately after typing.
    let text = ''
    try {
      editorInstance.action((ctx: Ctx) => {
        const serializer = ctx.get(serializerCtx)
        const view = ctx.get(editorViewCtx)
        text = serializer(view.state.doc).trim()
      })
    }
    catch {
      return
    }
    if (!text) {
      // Allow sending empty text only when explicitly enabled (e.g. Enter-to-approve for control requests).
      if (props.allowEmptySend) {
        props.onSend('')
      }
      focusEditor()
      return
    }
    if (props.onSend(text) === false) {
      focusEditor()
      return
    }
    editorInstance.action(replaceAll(''))
    setMarkdown('')
    props.onContentChange?.(false)
    const key = getDraftKey()
    if (key) {
      clearDraft(key)
    }
    focusEditor()
  }

  // Enter key mode reference for ProseMirror plugin (closures capture signal)
  let enterModeRef: EnterKeyMode = 'cmd-enter-sends'
  createEffect(() => {
    enterModeRef = enterMode()
  })
  let disabledRef = false
  // eslint-disable-next-line solid/reactivity -- initial value; tracked by createEffect below
  let placeholderRef = props.placeholder ?? 'Send a message...'
  let onTogglePlanModeRef: (() => void) | undefined
  createEffect(() => {
    onTogglePlanModeRef = props.onTogglePlanMode
  })

  // Force ProseMirror to re-render decorations when disabled or placeholder changes.
  const forceDecorationUpdate = () => {
    if (editorInstance) {
      try {
        editorInstance.action((ctx: Ctx) => {
          const view = ctx.get(editorViewCtx)
          view.dispatch(view.state.tr)
        })
      }
      catch {
        // Editor might not be ready yet
      }
    }
  }

  createEffect(() => {
    disabledRef = props.disabled ?? false
    forceDecorationUpdate()
  })
  createEffect(() => {
    placeholderRef = props.placeholder ?? 'Send a message...'
    forceDecorationUpdate()
  })

  const applyEditorState = (editor: Editor) => {
    try {
      const disabled = disabledRef
      editor.action((ctx: Ctx) => {
        const view = ctx.get(editorViewCtx)
        view.setProps({ editable: () => !disabled })
        if (!disabled) {
          view.focus()
        }
      })
    }
    catch {
      // Editor might not be fully ready yet
    }
  }

  let draftSaveTimeout: ReturnType<typeof setTimeout> | undefined

  onMount(async () => {
    if (!editorRef)
      return

    const initialDraftKey = getDraftKey()
    const initialDraft = initialDraftKey ? loadDraft(initialDraftKey) : { content: '', cursor: -1 }

    const createEditor = () => {
      const pluginRefs = {
        getDisabled: () => disabledRef,
        getEnterMode: () => enterModeRef,
        getPlaceholder: () => placeholderRef,
        onSend: handleSend,
      }

      const placeholderPlugin = createPlaceholderPlugin(pluginRefs)
      const sendPlugin = createSendOnEnterPlugin(pluginRefs)
      const blockquoteBackspacePlugin = createBlockquoteBackspacePlugin()
      const tabKeyPlugin = createTabKeyPlugin({
        onShiftTabInParagraph: () => onTogglePlanModeRef?.(),
      })
      const codeBlockBackspacePlugin = createCodeBlockBackspacePlugin()
      const codeBlockEnterPlugin = createCodeBlockEnterPlugin()
      const codeBlockEscapePlugin = createCodeBlockEscapePlugin()
      const suppressTextSubstitutionPlugin = createSuppressTextSubstitutionPlugin()
      const listItemEnterPlugin = createListItemEnterPlugin(pluginRefs)
      const toolbarStatePlugin = createToolbarStatePlugin({
        setActiveBold,
        setActiveItalic,
        setActiveStrikethrough,
        setActiveCode,
        setActiveCodeBlock,
        setActiveBlockquote,
        setActiveLink,
        setActiveHeadingLevel,
        setActiveBulletList,
        setActiveOrderedList,
        setActiveTaskList,
      })
      const codeLangPlugin = createCodeLangPlugin({
        setCodeLangNodePos,
        setCodeLangAnchorEl,
        setCodeLangPopoverOpen,
      })
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
          ctx.set(rootCtx, editorRef!)
          ctx.set(defaultValueCtx, initialDraft.content)
          ctx.set(highlightPluginConfig.key, { parser: shikiParser, languageExtractor })
          ctx.update(editorViewOptionsCtx, prev => ({
            ...prev,
            attributes: {
              spellcheck: 'false',
              autocorrect: 'off',
              autocapitalize: 'off',
            },
          }))
          // eslint-disable-next-line solid/reactivity -- callback invoked from Milkdown listener, not a tracked scope
          ctx.get(listenerCtx).markdownUpdated((_ctx, md) => {
            setMarkdown(md)
            props.onContentChange?.(md.trim().length > 0)
            // Debounced draft save
            const draftKey = getDraftKey()
            if (draftKey) {
              clearTimeout(draftSaveTimeout)
              draftSaveTimeout = setTimeout(() => {
                let cursor = -1
                try {
                  editorInstance?.action((c: Ctx) => {
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

    const editor = await createEditor()
    editorInstance = editor
    // Apply editable state and auto-focus — the createEffect on `disabled`
    // may have fired before the editor was created, so set it explicitly.
    applyEditorState(editor)
    // Track content height via ResizeObserver for adaptive height behavior.
    // We use requestAnimationFrame to coalesce observations and avoid a
    // feedback loop: the observed height feeds into the wrapper's inline
    // style (height / min-height), which can resize the observed element,
    // re-triggering the observer.  By deferring the signal update to the
    // next animation frame we let the browser settle before committing.
    const proseMirrorEl = editorRef?.querySelector('.ProseMirror')
    if (proseMirrorEl) {
      let rafId = 0
      const resizeObserver = new ResizeObserver((entries) => {
        const entry = entries[entries.length - 1]
        if (!entry)
          return
        const h = entry.borderBoxSize?.[0]?.blockSize
          ?? entry.target.getBoundingClientRect().height
        cancelAnimationFrame(rafId)
        rafId = requestAnimationFrame(() => {
          // Only update when the value actually changed to avoid
          // re-triggering the style/layout cycle.
          if (Math.abs(contentHeight() - h) >= 1) {
            setContentHeight(h)
            props.onContentHeightChange?.(h)
          }
        })
      })
      resizeObserver.observe(proseMirrorEl)
      onCleanup(() => {
        cancelAnimationFrame(rafId)
        resizeObserver.disconnect()
      })
    }
    // Notify parent if we loaded a draft with content, and restore cursor position
    if (initialDraftKey && initialDraft.content) {
      props.onContentChange?.(true)
      try {
        editor.action((ctx: Ctx) => {
          const view = ctx.get(editorViewCtx)
          const { doc } = view.state
          const maxPos = doc.content.size - 1
          const pos = initialDraft.cursor >= 0 ? Math.min(initialDraft.cursor, maxPos) : maxPos
          if (pos > 0) {
            const tr = view.state.tr.setSelection(TextSelection.create(doc, pos))
            view.dispatch(tr)
          }
        })
      }
      catch { /* editor may not be ready */ }
    }
    // Expose get/set content functions to parent component (e.g. for save/restore per-question text)
    props.contentRef?.(
      () => {
        let text = ''
        try {
          editor.action((ctx: Ctx) => {
            const serializer = ctx.get(serializerCtx)
            const view = ctx.get(editorViewCtx)
            text = serializer(view.state.doc).trim()
          })
        }
        catch { /* editor may not be ready */ }
        return text
      },
      // eslint-disable-next-line solid/reactivity -- setter is invoked from event handlers, not tracked scopes
      (text: string) => {
        try {
          editor.action(replaceAll(text))
          setMarkdown(text)
          props.onContentChange?.(text.trim().length > 0)
        }
        catch { /* editor may not be ready */ }
      },
    )
    // Expose handleSend to parent component (e.g. for external send button)
    props.sendRef?.(handleSend)
    // Expose focus function to parent component (e.g. for tab selection)
    props.focusRef?.(() => {
      try {
        editor.action((ctx: Ctx) => {
          ctx.get(editorViewCtx).focus()
        })
      }
      catch { /* editor may not be ready */ }
    })
  })

  onCleanup(() => {
    clearTimeout(draftSaveTimeout)
    // Save draft for the current agent/control-request before cleanup
    const cleanupKey = getDraftKey()
    if (editorInstance && cleanupKey) {
      try {
        editorInstance.action((ctx: Ctx) => {
          const serializer = ctx.get(serializerCtx)
          const view = ctx.get(editorViewCtx)
          const text = serializer(view.state.doc).trim()
          const cursor = view.state.selection.from
          saveDraft(cleanupKey, text, cursor)
        })
      }
      catch { /* editor may not be ready */ }
    }
    if (editorInstance) {
      editorInstance.destroy()
    }
  })

  // Swap editor content when agentId changes (per-tab draft isolation)
  let prevAgentId: string | undefined
  let prevCtrlReqIdForAgentSwap: string | undefined
  createEffect(on(
    () => props.agentId,
    (newAgentId) => {
      // On first run, prevAgentId is undefined — just record the initial agentId.
      // onMount already loaded the draft for this agentId, so no swap needed.
      // Note: editorInstance may not exist yet (onMount is async), so we must
      // set prevAgentId unconditionally to avoid skipping the first real swap.
      if (prevAgentId === undefined) {
        prevAgentId = newAgentId
        prevCtrlReqIdForAgentSwap = props.controlRequestId
        return
      }
      if (newAgentId === prevAgentId)
        return
      if (!editorInstance)
        return

      // Save current content as draft for the old agent (under composite key)
      if (prevAgentId) {
        const oldKey = prevCtrlReqIdForAgentSwap
          ? `${prevAgentId}-ctrl-${prevCtrlReqIdForAgentSwap}`
          : prevAgentId
        try {
          editorInstance.action((ctx: Ctx) => {
            const serializer = ctx.get(serializerCtx)
            const view = ctx.get(editorViewCtx)
            const text = serializer(view.state.doc).trim()
            const cursor = view.state.selection.from
            saveDraft(oldKey, text, cursor)
          })
        }
        catch { /* editor may not be ready */ }
      }

      // Load draft for the new agent and replace editor content
      const newKey = getDraftKey()
      const draft = newKey ? loadDraft(newKey) : { content: '', cursor: -1 }
      try {
        editorInstance.action(replaceAll(draft.content))
        // Restore cursor position
        editorInstance.action((ctx: Ctx) => {
          const view = ctx.get(editorViewCtx)
          const { doc } = view.state
          const maxPos = doc.content.size - 1
          const pos = draft.cursor >= 0 ? Math.min(draft.cursor, maxPos) : maxPos
          if (pos > 0) {
            const tr = view.state.tr.setSelection(TextSelection.create(doc, pos))
            view.dispatch(tr)
          }
        })
        setMarkdown(draft.content)
        props.onContentChange?.(draft.content.trim().length > 0)
      }
      catch { /* editor may not be ready */ }

      prevAgentId = newAgentId
      prevCtrlReqIdForAgentSwap = props.controlRequestId
    },
  ))

  // Swap editor content when controlRequestId changes (control request draft isolation)
  let prevControlRequestId: string | null | undefined // sentinel for first run
  createEffect(on(
    () => props.controlRequestId,
    (newCtrlId) => {
      const newCtrlIdNorm = newCtrlId ?? null
      // On first run, just record the initial value. onMount handles initial draft.
      if (prevControlRequestId === undefined) {
        prevControlRequestId = newCtrlIdNorm
        return
      }
      if (newCtrlIdNorm === prevControlRequestId)
        return
      if (!editorInstance || !props.agentId)
        return

      // Save current content under the old key
      const oldKey = prevControlRequestId
        ? `${props.agentId}-ctrl-${prevControlRequestId}`
        : props.agentId
      try {
        editorInstance.action((ctx: Ctx) => {
          const serializer = ctx.get(serializerCtx)
          const view = ctx.get(editorViewCtx)
          const text = serializer(view.state.doc).trim()
          const cursor = view.state.selection.from
          saveDraft(oldKey, text, cursor)
        })
      }
      catch { /* editor may not be ready */ }

      // Load draft for the new key
      const newKey = newCtrlIdNorm
        ? `${props.agentId}-ctrl-${newCtrlIdNorm}`
        : props.agentId
      const draft = loadDraft(newKey)
      try {
        editorInstance.action(replaceAll(draft.content))
        editorInstance.action((ctx: Ctx) => {
          const view = ctx.get(editorViewCtx)
          const { doc } = view.state
          const maxPos = doc.content.size - 1
          const pos = draft.cursor >= 0 ? Math.min(draft.cursor, maxPos) : maxPos
          if (pos > 0) {
            const tr = view.state.tr.setSelection(TextSelection.create(doc, pos))
            view.dispatch(tr)
          }
        })
        setMarkdown(draft.content)
        props.onContentChange?.(draft.content.trim().length > 0)
      }
      catch { /* editor may not be ready */ }

      prevControlRequestId = newCtrlIdNorm
    },
  ))

  // Disable/enable the editor view when disabled prop changes
  createEffect(on(
    () => props.disabled,
    (disabled) => {
      if (editorInstance) {
        try {
          editorInstance.action((ctx: Ctx) => {
            const view = ctx.get(editorViewCtx)
            view.setProps({ editable: () => !disabled })
          })
        }
        catch {
          // Editor might not be ready yet
        }
      }
    },
  ))

  // Link popover state
  const [linkPopoverOpen, setLinkPopoverOpen] = createSignal(false)
  const [linkUrl, setLinkUrl] = createSignal('')

  const handleLinkSubmit = () => {
    const href = linkUrl().trim()
    if (href && editorInstance) {
      editorInstance.action((ctx: Ctx) => {
        const view = ctx.get(editorViewCtx)
        const { state } = view
        const { from, to } = state.selection
        const linkMark = state.schema.marks.link.create({ href })
        if (from === to) {
          // No text selected — insert URL as both text and link
          const tr = state.tr
            .insertText(href, from)
            .addMark(from, from + href.length, linkMark)
            .removeStoredMark(state.schema.marks.link)
          view.dispatch(tr)
        }
        else {
          // Text is selected — apply link mark to selection
          const tr = state.tr
            .addMark(from, to, linkMark)
            .removeStoredMark(state.schema.marks.link)
          view.dispatch(tr)
        }
        view.focus()
      })
    }
    setLinkPopoverOpen(false)
    setLinkUrl('')
  }

  const handleLinkRemove = () => {
    if (!editorInstance)
      return
    editorInstance.action((ctx: Ctx) => {
      const view = ctx.get(editorViewCtx)
      const { state } = view
      const { from, to, $from } = state.selection
      const linkMark = $from.marks().find(m => m.type.name === 'link')
      if (linkMark) {
        let linkStart = from
        let linkEnd = to
        state.doc.nodesBetween($from.start(), from, (node, pos) => {
          if (node.isText && linkMark.isInSet(node.marks)) {
            linkStart = pos
          }
        })
        state.doc.nodesBetween(from, $from.end(), (node, pos) => {
          if (node.isText && linkMark.isInSet(node.marks)) {
            linkEnd = pos + node.nodeSize
          }
        })
        const tr = state.tr.removeMark(linkStart, linkEnd, state.schema.marks.link)
        view.dispatch(tr)
      }
      view.focus()
    })
  }

  const handleCodeBlockClick = () => {
    if (!editorInstance)
      return

    // If already inside a code block, toggle it back to paragraph (only if empty)
    let inCodeBlock = false
    try {
      editorInstance.action((ctx: Ctx) => {
        const view = ctx.get(editorViewCtx)
        const { state } = view
        const { $from } = state.selection
        if ($from.parent.type.name === 'code_block') {
          inCodeBlock = true
          if ($from.parent.content.size === 0) {
            const pos = $from.before($from.depth)
            const tr = state.tr.setNodeMarkup(pos, state.schema.nodes.paragraph)
            view.dispatch(tr)
          }
          view.focus()
        }
      })
    }
    catch {
      // ignore
    }
    if (inCodeBlock)
      return

    let listItemDepth = -1
    try {
      editorInstance.action((ctx: Ctx) => {
        const view = ctx.get(editorViewCtx)
        const { $from } = view.state.selection
        for (let d = $from.depth; d >= 1; d--) {
          if ($from.node(d).type.name === 'list_item') {
            listItemDepth = d
            break
          }
        }
      })
    }
    catch {
      // ignore
    }
    if (listItemDepth < 0) {
      editorInstance.action(callCommand(createCodeBlockCommand.key))
      focusEditor()
      return
    }
    editorInstance.action((ctx: Ctx) => {
      const view = ctx.get(editorViewCtx)
      const { state } = view
      const { $from } = state.selection
      const codeBlock = state.schema.nodes.code_block.create()
      const afterParagraph = $from.after($from.depth)
      const tr = state.tr.insert(afterParagraph, codeBlock)
      tr.setSelection(TextSelection.create(tr.doc, afterParagraph + 1))
      view.dispatch(tr)
      view.focus()
    })
  }

  const handleInlineCodeClick = () => {
    if (!editorInstance)
      return
    editorInstance.action((ctx: Ctx) => {
      const view = ctx.get(editorViewCtx)
      const { state } = view
      const { from, empty } = state.selection

      if (!empty) {
        // Range selection: delegate to Milkdown's toggle command
        editorInstance!.action(callCommand(toggleInlineCodeCommand.key))
        view.focus()
        return
      }

      // Empty selection: toggle stored mark
      const codeMarkType = state.schema.marks.inlineCode
      if (!codeMarkType)
        return

      const marks = state.storedMarks ?? state.selection.$from.marks()
      const hasCode = marks.some(m => m.type === codeMarkType)

      if (hasCode) {
        // Inside code: find the code mark range and remove it
        const $from = state.selection.$from
        const parent = $from.parent
        const parentStart = $from.start($from.depth)
        let rangeFrom = -1
        let rangeTo = -1
        let found = false

        parent.forEach((child, offset) => {
          if (found)
            return
          const childStart = parentStart + offset
          const childEnd = childStart + child.nodeSize
          if (child.isText && codeMarkType.isInSet(child.marks)) {
            if (rangeFrom < 0)
              rangeFrom = childStart
            rangeTo = childEnd
          }
          else {
            if (rangeFrom >= 0 && from >= rangeFrom && from <= rangeTo) {
              found = true
              return
            }
            rangeFrom = -1
            rangeTo = -1
          }
        })
        if (!found && rangeFrom >= 0 && from >= rangeFrom && from <= rangeTo) {
          found = true
        }

        const tr = state.tr
        if (found && rangeTo - rangeFrom > 0) {
          tr.removeMark(rangeFrom, rangeTo, codeMarkType)
        }
        tr.removeStoredMark(codeMarkType)
        view.dispatch(tr)
      }
      else {
        // Outside code: add code mark
        const tr = state.tr.addStoredMark(codeMarkType.create())
        view.dispatch(tr)
      }
      view.focus()
    })
  }

  const applyCodeLang = (langId: string) => {
    const pos = codeLangNodePos()
    if (editorInstance && pos >= 0) {
      editorInstance.action((ctx: Ctx) => {
        const view = ctx.get(editorViewCtx)
        const { state } = view
        const node = state.doc.nodeAt(pos)
        if (node && node.type.name === 'code_block') {
          const tr = state.tr.setNodeMarkup(pos, undefined, { ...node.attrs, language: langId || null })
          view.dispatch(tr)
        }
        view.focus()
      })
    }
    setCodeLangPopoverOpen(false)
    setCodeLangNodePos(-1)
  }

  return (
    <div class={styles.container}>
      <EditorToolbar
        editorInstance={() => editorInstance}
        focusEditor={focusEditor}
        enterMode={enterMode}
        toggleEnterMode={toggleEnterMode}
        enterTooltipOpen={enterTooltipOpen}
        setEnterTooltipOpen={setEnterTooltipOpen}
        activeBold={activeBold}
        activeItalic={activeItalic}
        activeStrikethrough={activeStrikethrough}
        activeCode={activeCode}
        activeCodeBlock={activeCodeBlock}
        activeBlockquote={activeBlockquote}
        activeLink={activeLink}
        activeHeadingLevel={activeHeadingLevel}
        activeBulletList={activeBulletList}
        activeOrderedList={activeOrderedList}
        activeTaskList={activeTaskList}
        linkPopoverOpen={linkPopoverOpen}
        setLinkPopoverOpen={setLinkPopoverOpen}
        linkUrl={linkUrl}
        setLinkUrl={setLinkUrl}
        handleLinkSubmit={handleLinkSubmit}
        handleLinkRemove={handleLinkRemove}
        handleCodeBlockClick={handleCodeBlockClick}
        handleInlineCodeClick={handleInlineCodeClick}
      />
      {props.banner}
      <div
        class={styles.editorWrapper}
        ref={editorRef}
        data-testid="chat-editor"
        style={{
          ...(props.requestedHeight != null && contentHeight() > 0 && props.requestedHeight < contentHeight()
            ? { height: `${props.requestedHeight}px` }
            : props.requestedHeight != null
              ? { 'min-height': `${props.requestedHeight}px` }
              : {}),
          ...(props.maxHeight ? { 'max-height': `${props.maxHeight}px` } : {}),
        }}
      />
      <CodeLanguagePopover
        open={codeLangPopoverOpen}
        setOpen={setCodeLangPopoverOpen}
        nodePos={codeLangNodePos}
        setNodePos={setCodeLangNodePos}
        filter={codeLangFilter}
        setFilter={setCodeLangFilter}
        anchorRef={codeLangAnchorEl}
        onApply={applyCodeLang}
      />
      {props.footer}
    </div>
  )
}
