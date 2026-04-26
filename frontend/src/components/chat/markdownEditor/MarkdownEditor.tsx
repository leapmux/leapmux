import type { Editor } from '@milkdown/core'
import type { Ctx } from '@milkdown/ctx'
import type { Component, JSX } from 'solid-js'
import type { EnterKeyMode } from '~/lib/browserStorage'
import type { TrailingDebounced } from '~/lib/debounce'
import { editorViewCtx, serializerCtx } from '@milkdown/core'
import { replaceAll } from '@milkdown/utils'
import { createEffect, createSignal, getOwner, on, onCleanup, onMount, runWithOwner } from 'solid-js'
import { usePreferences } from '~/context/PreferencesContext'
import { loadDraft } from '~/lib/editor/draftPersistence'
import { CodeLanguagePopover } from './CodeLanguagePopover'
import { clearDraft, restoreCursor, saveDraftFromEditor } from './draftManagement'
import { applyCodeBlockLanguage, applyLinkSubmit, removeLinkAtSelection, toggleCodeBlock, toggleInlineCode } from './editorCommands'
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
  // Mirror callback/flag props used from DOM-event handlers into plain refs so
  // Solid does not create lazy prop computations outside a component root.
  let onSendRef: MarkdownEditorProps['onSend'] = () => undefined
  let allowEmptySendRef = false
  let onContentChangeRef: MarkdownEditorProps['onContentChange']

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
      if (allowEmptySendRef) {
        onSendRef('')
      }
      focusEditor()
      return
    }
    if (onSendRef(text) === false) {
      focusEditor()
      return
    }
    editorInstance.action(replaceAll(''))
    setMarkdown('')
    onContentChangeRef?.(false)
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
  let placeholderRef = 'Send a message...'
  let onTogglePlanModeRef: (() => void) | undefined
  createEffect(() => {
    onTogglePlanModeRef = props.onTogglePlanMode
  })
  createEffect(() => {
    onSendRef = props.onSend
    allowEmptySendRef = props.allowEmptySend ?? false
    onContentChangeRef = props.onContentChange
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

  const draftSaveDebounce: { current: TrailingDebounced | undefined } = { current: undefined }
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

    const owner = getOwner()
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
      draftSaveDebounce,
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
      runWithOwner(owner, () => onCleanup(() => {
        cancelAnimationFrame(rafId)
        resizeObserver.disconnect()
      }))
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
    runWithOwner(owner, () => onCleanup(() => {
      editorRef?.removeEventListener('paste', handlePaste, true)
      editorRef?.removeEventListener('drop', handleDrop, true)
    }))
  })

  onCleanup(() => {
    draftSaveDebounce.current?.cancel()
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
    applyLinkSubmit(editorInstance, linkUrl(), () => {
      setLinkPopoverOpen(false)
      setLinkUrl('')
    })
  }

  const handleLinkRemove = () => removeLinkAtSelection(editorInstance)

  const handleCodeBlockClick = () => toggleCodeBlock(editorInstance, focusEditor)

  const handleInlineCodeClick = () => toggleInlineCode(editorInstance)

  const applyCodeLang = (langId: string) => {
    applyCodeBlockLanguage(editorInstance, codeLangNodePos(), langId, () => {
      setCodeLangPopoverOpen(false)
      setCodeLangNodePos(-1)
    })
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
