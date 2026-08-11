import type { Editor } from '@milkdown/core'
import type { Ctx } from '@milkdown/ctx'
import type { Component, JSX } from 'solid-js'
import type { EnterKeyMode } from '~/lib/browserStorage'
import type { TrailingDebounced } from '~/lib/debounce'
import type { LinkRange } from '~/lib/editor/linkPlugin'
import { editorViewCtx, serializerCtx } from '@milkdown/core'
import { replaceAll } from '@milkdown/utils'
import { createEffect, createSignal, getOwner, on, onCleanup, onMount, runWithOwner } from 'solid-js'
import { isTauriApp, readClipboardImage } from '~/api/platformBridge'
import { usePreferences } from '~/context/PreferencesContext'
import { loadDraft } from '~/lib/editor/draftPersistence'
import { DEFAULT_DISABLED_PLACEHOLDER } from '~/lib/editor/keyboardPlugins'
import { CodeLanguagePopover } from './CodeLanguagePopover'
import { createComposerLayout } from './composerLayout'
import { clearDraft, restoreCursor, saveDraftFromEditor } from './draftManagement'
import { applyCodeBlockLanguage, applyLinkHref, removeLinkRange } from './editorCommands'
import { setupEditorRefHandlers } from './editorRefHandlers'
import { buildEditor, computeDocStats } from './editorSetup'
import { LinkPopover } from './LinkPopover'
import * as styles from './MarkdownEditor.css'
import { decidePasteHandling } from './pasteDecision'

export { clearDraft }

/**
 * Identifies the localStorage draft key. Only one of `key` or
 * (`agentId` + optional `controlRequestId`) needs to be set; if `key` is set
 * it takes precedence.
 */
export interface MarkdownEditorDraftKey {
  /** Agent ID for per-tab draft persistence. */
  agentId?: string
  /** Full draft key override. When set, takes precedence over agentId/controlRequestId. */
  key?: string
  /** When set, drafts are stored under a control-request-specific key instead of the agentId key. */
  controlRequestId?: string
}

/**
 * File-attachment hooks the editor itself owns. Both fire from a DOM event on
 * the editor root.
 *
 * The attach affordance is NOT here: it lives in the composer's `[+]` menu,
 * which the parent renders and wires to its own file input. The editor has no
 * attach button to route, so it exposes no hook for one.
 */
export interface MarkdownEditorAttachments {
  /** Called when files are pasted from clipboard. Prevents ProseMirror from inserting inline images. */
  onPaste?: (files: File[]) => void
  /** Called when files are dropped onto the editor. Prevents ProseMirror from inserting inline content. */
  onDrop?: (dataTransfer: DataTransfer) => void
}

/** Imperative escape hatches for the editor (refs and the ready callback). */
export interface MarkdownEditorImperative {
  sendRef?: (send: () => void) => void
  focusRef?: (focus: () => void) => void
  contentRef?: (get: () => string, set: (text: string) => void) => void
  insertRef?: (insert: (text: string) => void) => void
  /** Called once the editor is fully initialized with draft content. */
  onReady?: () => void
}

interface MarkdownEditorProps {
  draftKey?: MarkdownEditorDraftKey
  attachments?: MarkdownEditorAttachments
  imperative?: MarkdownEditorImperative
  onSend: (markdown: string) => boolean | void
  disabled?: boolean
  requestedHeight?: number
  maxHeight?: number
  onContentHeightChange?: (height: number) => void
  onContentChange?: (hasContent: boolean) => void
  banner?: JSX.Element
  /**
   * The action row for the box, and the layout it needs.
   *
   * ONE value, not a slot plus a flag. Only one action row can ever render, and
   * its layout is a property OF that row: `corner` is the compact Interrupt +
   * Send cluster hugging the bottom-right, `fullWidth` is a control request's
   * two-zone row, which also forces the expanded layout so it always sits below
   * the text with the separator above it. A separate boolean let the flag and
   * the row that actually rendered disagree, and both were set from the same
   * test at the one call site anyway.
   *
   * `node` is a THUNK. Solid re-evaluates a prop's value on every read, so an
   * element built directly into this object would be reconstructed each time the
   * layout is consulted; the thunk is called once, where the row is inserted.
   */
  actions?: { layout: 'corner' | 'fullWidth', node: () => JSX.Element }
  /**
   * The `[+]` button (and its menu), rendered at the top-left of the box when
   * the editor is a single line, dropping to the bottom-left when the content
   * expands past one line. The editor's left padding makes room for it.
   */
  plus?: JSX.Element
  placeholder?: string
  /**
   * Placeholder shown while `disabled` is set. Defaults to the lost-connection
   * wording; pass the actual reason when there is a more specific one (e.g. a
   * subagent whose provider accepts no input).
   */
  disabledPlaceholder?: string
  /** When true, pressing Enter with an empty editor calls onSend('') instead of doing nothing. */
  allowEmptySend?: boolean
  /** Called when Shift+Tab is pressed in a plain paragraph (indent level 0). */
  onTogglePlanMode?: () => void
}

export const MarkdownEditor: Component<MarkdownEditorProps> = (props) => {
  let editorRef: HTMLDivElement | undefined
  // The editor row and the action-cluster slot are measured for the
  // expand/collapse threshold. They are captured as refs, not looked up by
  // `data-testid`: a test id is not a layout contract, and renaming one would
  // silently leave the measurement at its fallback.
  let editorRowEl: HTMLDivElement | undefined
  let footerSlotEl: HTMLDivElement | undefined
  let editorInstance: Editor | undefined
  const preferences = usePreferences()
  const enterMode = preferences.enterKeyMode
  const [_markdown, setMarkdown] = createSignal('')
  const [contentHeight, setContentHeight] = createSignal(0)
  /**
   * Set by this component's own `onCleanup`. `buildEditor` is asynchronous, so
   * the component can unmount while it is still pending. Solid then runs the
   * cleanup with `editorInstance` still undefined (so `destroy()` is skipped),
   * and a cleanup registered afterwards through `runWithOwner` is pushed onto an
   * owner that already ran its cleanups and never runs again. The continuation
   * reads this flag and tears down what it built instead of leaking the editor,
   * its ResizeObservers, and its paste/drop listeners.
   */
  let disposed = false

  /**
   * The box's expand/collapse decision and the three DOM measurements it needs.
   * Every probe, observer, and threshold lives in that one unit — see
   * `./composerLayout`.
   */
  const layout = createComposerLayout({
    editorRoot: () => editorRef,
    row: () => editorRowEl,
    actionSlot: () => footerSlotEl,
    firstBlock: () => editorRef?.querySelector('.ProseMirror > *'),
  })

  // A full-width action row (a control request) forces the expanded layout, so
  // its two-zone row always renders below the text with the separator above it.
  const isExpanded = () => layout.contentExpanded() || props.actions?.layout === 'fullWidth'

  /** Compute the localStorage draft key, incorporating controlRequestId when present. */
  const getDraftKey = () => {
    const dk = props.draftKey
    if (dk?.key)
      return dk.key
    if (!dk?.agentId)
      return undefined
    return dk.controlRequestId
      ? `${dk.agentId}-ctrl-${dk.controlRequestId}`
      : dk.agentId
  }

  // Editor wrapper sizing: the explicit `requestedHeight` becomes a hard cap
  // when the content already overflows it, otherwise it acts as a min-height
  // floor so empty/short editors still render at the expected size.
  const editorWrapperStyle = (): JSX.CSSProperties => {
    const style: JSX.CSSProperties = {}
    const requested = props.requestedHeight
    if (requested != null) {
      const overflowing = contentHeight() > 0 && requested < contentHeight()
      style[overflowing ? 'height' : 'min-height'] = `${requested}px`
    }
    if (props.maxHeight)
      style['max-height'] = `${props.maxHeight}px`
    return style
  }

  // Enter mode tooltip state was part of the deleted formatting toolbar; the
  // Enter-key mode toggle now lives in the composer's `[+]` menu, which reads
  // and writes the preference directly, so the editor no longer needs a local
  // toggle or a pinned-tooltip signal.

  // Code block language popover state
  const [codeLangPopoverOpen, setCodeLangPopoverOpen] = createSignal(false)
  const [codeLangNodePos, setCodeLangNodePos] = createSignal(-1)
  const [codeLangAnchorEl, setCodeLangAnchorEl] = createSignal<HTMLElement | undefined>(undefined)
  const [codeLangFilter, setCodeLangFilter] = createSignal('')

  // Link edit popover state. Clicking a link opens it; it is the only way to
  // change or remove a URL, because editing a link's visible text keeps the old
  // href (the mark is inclusive, so it survives a delete-and-retype too).
  const [linkPopoverOpen, setLinkPopoverOpen] = createSignal(false)
  const [linkRange, setLinkRange] = createSignal<LinkRange | null>(null)
  // Mirror callback/flag props used from DOM-event handlers into plain refs so
  // Solid does not create lazy prop computations outside a component root.
  let onSendRef: MarkdownEditorProps['onSend'] = () => undefined
  let allowEmptySendRef = false
  let onContentChangeRef: MarkdownEditorProps['onContentChange']

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
  let disabledPlaceholderRef = DEFAULT_DISABLED_PLACEHOLDER
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
  createEffect(() => {
    disabledPlaceholderRef = props.disabledPlaceholder || DEFAULT_DISABLED_PLACEHOLDER
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
    // Before the await, so the layout's observers register their cleanup on a
    // LIVE owner. They measure the row and the action slot, neither of which
    // waits on the editor -- and a cleanup registered after the await would
    // never run when the component unmounts while `buildEditor` is pending.
    layout.observe()

    const initialDraftKey = getDraftKey()
    const initialDraft = initialDraftKey ? loadDraft(initialDraftKey) : { content: '', cursor: -1 }

    const editor = await buildEditor({
      editorRoot: editorRef,
      initialContent: initialDraft.content,
      pluginRefs: {
        getDisabled: () => disabledRef,
        getEnterMode: () => enterModeRef,
        getPlaceholder: () => placeholderRef,
        getDisabledPlaceholder: () => disabledPlaceholderRef,
        onSend: handleSend,
      },
      getOnTogglePlanMode: () => onTogglePlanModeRef,
      codeLangHandlers: {
        setCodeLangNodePos,
        setCodeLangAnchorEl,
        setCodeLangPopoverOpen,
        getCodeLangPopoverOpen: codeLangPopoverOpen,
        getCodeLangNodePos: codeLangNodePos,
      },
      linkClickHandlers: {
        setLinkRange,
        setLinkPopoverOpen,
        getLinkPopoverOpen: linkPopoverOpen,
        getLinkRange: linkRange,
      },
      setMarkdown,
      onContentChange: hasContent => props.onContentChange?.(hasContent),
      onDocTransaction: layout.setDocStats,
      getDraftKey,
      draftSaveDebounce,
      getEditorInstance: () => editorInstance,
    })

    // The component unmounted while `buildEditor` was pending. Nothing below
    // would ever be torn down (see `disposed`), so destroy the editor here and
    // register nothing.
    if (disposed) {
      editor.destroy()
      return
    }

    editorInstance = editor
    // Seed docStats from the parsed draft so the expand/collapse decision is
    // correct before any transaction fires. Without this a multi-line draft
    // starts collapsed until the user types. It classifies the real
    // ProseMirror document, so the mount decision and every later decision use
    // one classifier and cannot disagree.
    try {
      editor.action((ctx: Ctx) => {
        layout.setDocStats(computeDocStats(ctx.get(editorViewCtx).state.doc))
      })
    }
    catch { /* editor may not be ready; the first transaction re-computes */ }
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
      sendRef: props.imperative?.sendRef,
      focusRef: props.imperative?.focusRef,
      contentRef: props.imperative?.contentRef,
      insertRef: props.imperative?.insertRef,
      handleSend,
    })

    // Signal that the editor is fully initialized with draft content.
    props.imperative?.onReady?.()

    // Intercept paste/drop file events before ProseMirror processes them.
    // This keeps files in the attachment flow instead of inserting inline
    // content into the editor body.
    const handlePaste = (e: ClipboardEvent) => {
      const onPaste = props.attachments?.onPaste
      if (!onPaste)
        return
      const dt = e.clipboardData
      if (!dt)
        return
      const action = decidePasteHandling(dt, isTauriApp())
      if (action.kind === 'forward') {
        e.preventDefault()
        e.stopPropagation()
        onPaste(action.files)
        return
      }
      if (action.kind === 'defer')
        return
      // Exhaustiveness guard — a new PasteAction variant must be handled
      // explicitly above instead of silently falling into this branch.
      action satisfies { kind: 'tauri-clipboard' }
      // WebKitGTK (Tauri on Linux) delivers an entirely empty DataTransfer
      // for image pastes even though the OS clipboard holds a PNG. Bypass
      // the web layer via the Tauri clipboard plugin.
      e.preventDefault()
      e.stopPropagation()
      void readClipboardImage().then((file) => {
        if (file)
          onPaste([file])
      })
    }
    const handleDrop = (e: DragEvent) => {
      const onDrop = props.attachments?.onDrop
      if (!onDrop)
        return
      if (e.dataTransfer?.files.length) {
        e.preventDefault()
        e.stopPropagation()
        onDrop(e.dataTransfer)
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
    disposed = true
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

  // The link/code-block/inline-code toolbar handlers were part of the deleted
  // formatting toolbar. The code-language popover below (for editing a code
  // block's language label) is independent and remains.

  const applyCodeLang = (langId: string) => {
    applyCodeBlockLanguage(editorInstance, codeLangNodePos(), langId, () => {
      setCodeLangPopoverOpen(false)
      setCodeLangNodePos(-1)
    })
  }

  return (
    <div
      class={styles.container}
      data-expanded={isExpanded() ? '' : undefined}
      style={{
        '--composer-right-pad': `${layout.rightPad()}px`,
        // Omitted until measured, so the stylesheet's own fallback applies.
        ...(layout.actionsHeight() > 0 ? { '--composer-actions-h': `${layout.actionsHeight()}px` } : {}),
      }}
    >
      {props.banner}
      <div class={styles.editorRow} ref={editorRowEl}>
        <div class={styles.plusSlot} data-testid="composer-plus-slot">{props.plus}</div>
        <div
          class={styles.editorWrapper}
          ref={editorRef}
          data-testid="chat-editor"
          data-chat-input
          style={editorWrapperStyle()}
        />
        {/* Separator between text area and button row in expanded mode. Positioned
            at the top of the button reservation (the editor row's padding-bottom
            area) so it sits right between the text and the buttons. */}
        <div class={styles.editorSeparator} data-testid="composer-separator" />
        {/* The action cluster: compact Interrupt/Send (actions) when composing,
            or the full-width control-request actions (footer) when a request is
            active. `data-full-width` distinguishes the two so the expanded
            layout can stretch the control-request footer across the box. */}
        <div class={styles.footerSlot} ref={footerSlotEl} data-testid="composer-footer-slot" data-full-width={props.actions?.layout === 'fullWidth' ? '' : undefined}>{props.actions?.node()}</div>
      </div>
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
      <LinkPopover
        open={linkPopoverOpen}
        setOpen={setLinkPopoverOpen}
        range={linkRange}
        // The editor WRAPPER, not the clicked link. ProseMirror owns the `<a>`
        // and redraws it whenever the document changes -- including the mark
        // rewrite this popover performs -- which leaves a detached anchor and a
        // popover positioned from a zero-sized rect. The wrapper is outside the
        // contenteditable and never moves.
        anchorRef={() => editorRef}
        onApply={(href) => {
          const range = linkRange()
          if (range)
            applyLinkHref(editorInstance, range, href)
        }}
        onRemove={() => {
          const range = linkRange()
          if (range)
            removeLinkRange(editorInstance, range)
        }}
      />
    </div>
  )
}
