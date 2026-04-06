import type { Editor } from '@milkdown/core'
import type { Ctx } from '@milkdown/ctx'
import type { Component, JSX } from 'solid-js'
import type { EnterKeyMode } from '~/lib/browserStorage'
import { editorViewCtx, serializerCtx } from '@milkdown/core'
import { createCodeBlockCommand, toggleInlineCodeCommand } from '@milkdown/preset-commonmark'
import { TextSelection } from '@milkdown/prose/state'
import { callCommand, replaceAll } from '@milkdown/utils'
import { createEffect, createSignal, on, onCleanup, onMount } from 'solid-js'
import { usePreferences } from '~/context/PreferencesContext'
import { loadDraft } from '~/lib/editor/draftPersistence'
import { CodeLanguagePopover } from './CodeLanguagePopover'
import { clearDraft, restoreCursor, saveDraftFromEditor } from './draftManagement'
import { setupEditorRefHandlers } from './editorRefHandlers'
import { buildEditor } from './editorSetup'
import { EditorToolbar } from './EditorToolbar'
import * as styles from './MarkdownEditor.css'

export { clearDraft }

interface MarkdownEditorProps {
  /** Agent ID for per-tab draft persistence. */
  agentId?: string
  /** Full draft key override. When set, takes precedence over agent/control request keys. */
  draftKey?: string
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
  insertRef?: (insert: (text: string) => void) => void
  /** Called once the editor is fully initialized with draft content. */
  onReady?: () => void
  placeholder?: string
  /** When true, pressing Enter with an empty editor calls onSend('') instead of doing nothing. */
  allowEmptySend?: boolean
  /** Called when Shift+Tab is pressed in a plain paragraph (indent level 0). */
  onTogglePlanMode?: () => void
  /** Called when files are pasted from clipboard. Prevents ProseMirror from inserting inline images. */
  onPasteFiles?: (files: File[]) => void
  /** Called when files are dropped onto the editor. Prevents ProseMirror from inserting inline content. */
  onDropDataTransfer?: (dataTransfer: DataTransfer) => void
  /** Called when the upload button in the toolbar is clicked. */
  onUploadClick?: () => void
}

export const MarkdownEditor: Component<MarkdownEditorProps> = (props) => {
  let editorRef: HTMLDivElement | undefined
  let editorInstance: Editor | undefined
  const preferences = usePreferences()
  const enterMode = preferences.enterKeyMode
  const [_markdown, setMarkdown] = createSignal('')
  const [contentHeight, setContentHeight] = createSignal(0)

  /** Compute the localStorage draft key, incorporating controlRequestId when present. */
  const getDraftKey = () => {
    if (props.draftKey)
      return props.draftKey
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

  const toggleEnterMode = () => {
    const next = enterMode() === 'enter-sends' ? 'cmd-enter-sends' : 'enter-sends'
    preferences.setEnterKeyMode(next)
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

  const draftSaveTimer: { current: ReturnType<typeof setTimeout> | undefined } = { current: undefined }
  // Track the last valid draft key so onCleanup can save the draft even when
  // reactive getters (props.agentId) return null during unmount.
  let latestDraftKey: string | undefined
  createEffect(() => {
    const key = getDraftKey()
    if (key)
      latestDraftKey = key
  })

  onMount(async () => {
    if (!editorRef)
      return

    const initialDraftKey = getDraftKey()
    const initialDraft = initialDraftKey ? loadDraft(initialDraftKey) : { content: '', cursor: -1 }

    const editor = await buildEditor({
      editorRoot: editorRef,
      initialContent: initialDraft.content,
      pluginRefs: {
        getDisabled: () => disabledRef,
        getEnterMode: () => enterModeRef,
        getPlaceholder: () => placeholderRef,
        onSend: handleSend,
      },
      getOnTogglePlanMode: () => onTogglePlanModeRef,
      toolbarSetters: {
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
      },
      codeLangSetters: {
        setCodeLangNodePos,
        setCodeLangAnchorEl,
        setCodeLangPopoverOpen,
      },
      setMarkdown,
      onContentChange: hasContent => props.onContentChange?.(hasContent),
      getDraftKey,
      draftSaveTimer,
      getEditorInstance: () => editorInstance,
    })

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
        const entry = entries.at(-1)
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
        restoreCursor(editor, initialDraft.cursor)
      }
      catch { /* editor may not be ready */ }
    }

    setupEditorRefHandlers({
      editor,
      setMarkdown,
      onContentChange: hasContent => props.onContentChange?.(hasContent),
      sendRef: props.sendRef,
      focusRef: props.focusRef,
      contentRef: props.contentRef,
      insertRef: props.insertRef,
      handleSend,
    })

    // Signal that the editor is fully initialized with draft content.
    props.onReady?.()

    // Intercept paste/drop file events before ProseMirror processes them.
    // This keeps files in the attachment flow instead of inserting inline
    // content into the editor body.
    const handlePaste = (e: ClipboardEvent) => {
      if (!props.onPasteFiles)
        return
      const files = [...(e.clipboardData?.files ?? [])]
      if (files.length > 0) {
        e.preventDefault()
        e.stopPropagation()
        props.onPasteFiles(files)
      }
    }
    const handleDrop = (e: DragEvent) => {
      if (!props.onDropDataTransfer)
        return
      if (e.dataTransfer?.files.length) {
        e.preventDefault()
        e.stopPropagation()
        props.onDropDataTransfer(e.dataTransfer)
      }
    }
    editorRef?.addEventListener('paste', handlePaste, true)
    editorRef?.addEventListener('drop', handleDrop, true)
    onCleanup(() => {
      editorRef?.removeEventListener('paste', handlePaste, true)
      editorRef?.removeEventListener('drop', handleDrop, true)
    })
  })

  onCleanup(() => {
    clearTimeout(draftSaveTimer.current)
    // Save draft for the current agent/control-request before cleanup.
    // Prefer the cached latestDraftKey over getDraftKey(): during disposal
    // reactive getters (props.agentId) may already reflect the NEW agent
    // (e.g. tab switch causes FocusedAgentEditorPanel to be recreated,
    // and focusedAgentId() has already changed by cleanup time).
    const cleanupKey = latestDraftKey ?? getDraftKey()
    if (editorInstance && cleanupKey) {
      try {
        saveDraftFromEditor(editorInstance, cleanupKey)
      }
      catch { /* editor may not be ready */ }
    }
    if (editorInstance) {
      editorInstance.destroy()
    }
  })

  // Swap editor content when the effective draft key changes. This covers
  // agent switches, control-request switches, and per-question draft scopes.
  let prevDraftKey: string | null | undefined
  createEffect(on(
    getDraftKey,
    (newDraftKeyRaw) => {
      const newDraftKey = newDraftKeyRaw ?? null
      // On first run, just record the initial key.
      // onMount already loaded the draft for this agentId, so no swap needed.
      if (prevDraftKey === undefined) {
        prevDraftKey = newDraftKey
        return
      }
      if (newDraftKey === prevDraftKey)
        return
      if (!editorInstance)
        return

      // Save current content under the previous draft key.
      if (prevDraftKey) {
        try {
          saveDraftFromEditor(editorInstance, prevDraftKey)
        }
        catch { /* editor may not be ready */ }
      }

      // Load draft for the new key and replace editor content.
      const draft = newDraftKey ? loadDraft(newDraftKey) : { content: '', cursor: -1 }
      try {
        editorInstance.action(replaceAll(draft.content))
        restoreCursor(editorInstance, draft.cursor)
        setMarkdown(draft.content)
        props.onContentChange?.(draft.content.trim().length > 0)
      }
      catch { /* editor may not be ready */ }

      prevDraftKey = newDraftKey
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
        onUploadClick={props.onUploadClick}
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
