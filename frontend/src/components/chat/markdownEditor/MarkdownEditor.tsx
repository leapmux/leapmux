import type { Editor } from '@milkdown/core'
import type { Ctx } from '@milkdown/ctx'
import type { Node as ProseMirrorNode } from '@milkdown/prose/model'
import type { Component, JSX } from 'solid-js'
import type { EnterKeyMode } from '~/lib/browserPreferences'
import type { TrailingDebounced } from '~/lib/debounce'
import type { LinkRange } from '~/lib/editor/linkPlugin'
import { editorViewCtx, serializerCtx } from '@milkdown/core'
import { replaceAll } from '@milkdown/utils'
import { children, createEffect, createSignal, getOwner, on, onCleanup, onMount, runWithOwner, Show } from 'solid-js'
import { isTauriApp, readClipboardImage } from '~/api/platformBridge'
import { usePreferences } from '~/context/PreferencesContext'
import { loadDraft } from '~/lib/editor/draftPersistence'
import { createLogger } from '~/lib/logger'
import { dismissSoftKeyboard, isSoftKeyboardVisible } from '~/lib/softKeyboard'
import { syntaxThemeGeneration } from '~/lib/syntaxThemeStore'
import { errorText } from '~/styles/shared.css'
import { CodeLanguagePopover } from './CodeLanguagePopover'
import { clearDraft, createDraftSwapper, restoreCursor, saveDraftFromEditor } from './draftManagement'
import { applyCodeBlockLanguage, applyLinkHref, removeLinkRange } from './editorCommands'
import { createEditorLayout } from './editorLayout'
import { setupEditorRefHandlers } from './editorRefHandlers'
import { buildEditor, computeDocStats, refreshEditorHighlight } from './editorSetup'
import { LinkPopover } from './LinkPopover'
import * as styles from './MarkdownEditor.css'
import { decidePasteHandling } from './pasteDecision'
import { decideSendFocus } from './sendFocus'

const logger = createLogger('MarkdownEditor')

export { clearDraft }

/**
 * What an editor IS. The one thing a host has to say about itself.
 *
 * ONE prop rather than a test id plus a flag, because the two facts below are
 * one fact. `data-chat-input` decides whether the app's chat keybindings treat
 * focus inside this box as the composer -- `useShortcuts` reads it for the
 * `chatInputFocused` context, and `$mod+j` maps to `chat.sendMessage` there --
 * and the test ids decide which box an E2E locator resolves to. A caller given
 * two props can change the id and forget the marker, and the symptom is Cmd+J
 * inside a dialog sending a chat message.
 */
export type MarkdownEditorSurface = 'chat' | 'goal'

/**
 * Every surface's markers.
 *
 * A `Record` over the union, so a new surface fails to compile until it states
 * both. An array of pairs type-checked with any subset, which is how a new
 * surface would ship with the composer's own test id.
 *
 * ONE prefix per surface, from which `testIds` derives every id this component
 * writes. Spelling them out separately let `chat` disagree with itself --
 * `chat-editor` beside `composer-box` -- and made a third surface four strings
 * to copy rather than one to choose.
 */
const SURFACE_MARKERS: Record<MarkdownEditorSurface, {
  /** Leads the `data-testid` of the box, the editor and every layout slot. */
  testIdPrefix: string
  /** Whether `chatInputFocused` treats focus here as the message composer. */
  chatInput: boolean
}> = {
  chat: { testIdPrefix: 'composer', chatInput: true },
  goal: { testIdPrefix: 'goal', chatInput: false },
}

/**
 * The six `data-testid` values of one surface.
 *
 * DERIVED, never declared per surface, so a surface cannot carry another one's
 * id for a single element: a spec with the goal dialog open would otherwise
 * resolve two `composer-footer-slot` elements and fail Playwright's strict
 * mode.
 *
 * The prefixes must stay distinct from any id the rest of the app writes. The
 * work panel's rule above its task rows is `goal-card-separator` for exactly
 * that reason -- it and the goal editor are on screen together while the dialog
 * is open.
 */
function testIds(prefix: string) {
  return {
    box: `${prefix}-box`,
    editor: `${prefix}-editor`,
    buildFailed: `${prefix}-build-failed`,
    plusSlot: `${prefix}-plus-slot`,
    separator: `${prefix}-separator`,
    footerSlot: `${prefix}-footer-slot`,
  }
}

/**
 * Identifies the stored draft key. Only one of `key` or
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
  sendRef?: (send: () => void | Promise<void>) => void
  focusRef?: (focus: () => void) => void
  contentRef?: (get: () => string, set: (text: string) => void) => void
  insertRef?: (insert: (text: string) => void) => void
  /** Called once the editor is fully initialized with draft content. */
  onReady?: () => void
}

interface MarkdownEditorProps {
  /**
   * What this editor IS -- see {@link MarkdownEditorSurface}. Required, because
   * a default would silently hand a new host the composer's identity.
   */
  surface: MarkdownEditorSurface
  /**
   * Whether the composer must NOT take the keyboard when it finishes building.
   * Absent means "go ahead", for a caller with no competing focus.
   *
   * The shell sets it while an inline tab rename is open: the rename input is in
   * the tab strip, and a composer that grabbed focus would send the user's next
   * keystrokes into the message box instead. Same contract as
   * `TerminalView.tabEditing`.
   */
  suppressAutoFocus?: () => boolean
  draftKey?: MarkdownEditorDraftKey
  attachments?: MarkdownEditorAttachments
  imperative?: MarkdownEditorImperative
  onSend: (markdown: string) => boolean | void | Promise<boolean | void>
  onAfterSend?: () => void
  onDraftKeyChanged?: (key: string | null) => void
  disabled?: boolean
  /**
   * A height the box HOLDS once the content passes it: the composer's
   * drag-to-resize.
   *
   * It is a floor while the content is shorter and a fixed height once the
   * content is taller, which is what a dragged handle means -- the reader
   * chose that height, so the box keeps it and scrolls. `maxHeight` cannot
   * bind alongside it, because the box never grows past this value.
   */
  pinnedHeight?: number
  /**
   * A height the box OPENS at and then grows past, up to `maxHeight`.
   *
   * For a host that wants a comfortable starting size rather than a fixed one.
   * Use this with `maxHeight` to state a floor and a ceiling; use
   * `pinnedHeight` only where the reader picked the height.
   */
  minHeight?: number
  maxHeight?: number
  /**
   * The `id` of the element whose text names this editor.
   *
   * A contenteditable takes no `<label for>`, so a host that shows a caption
   * beside the box states the connection here instead. It lands on the
   * ProseMirror element, which is the one the caret enters.
   */
  ariaLabelledBy?: string
  /**
   * The text the editor OPENS with.
   *
   * Data rather than an imperative seed: a host that wrote the text through
   * `contentRef` had to hold a nullable setter and call it from `onReady`, which
   * depends on the order of two lines inside this component. It also replaced
   * the document one frame after the build, so the editor briefly held an empty
   * one.
   *
   * It WINS over a stored draft. A host that states the starting text is editing
   * a specific document, and a draft is the text it abandoned last time.
   */
  initialMarkdown?: string
  onContentHeightChange?: (height: number) => void
  onContentChange?: (hasContent: boolean) => void
  /**
   * The document's markdown, on every change.
   *
   * Beside `onContentChange` rather than folded into it: that one answers
   * "is there anything here", which a host reads to arm a Send button, and this
   * one carries the text, which a host reads to measure it. The goal dialog
   * measures the objective's UTF-8 length against the worker's cap while the
   * user types.
   */
  onMarkdownChange?: (markdown: string) => void
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
  /** Set when the asynchronous build failed -- see the `catch` on `onMount`. */
  const [buildFailed, setBuildFailed] = createSignal(false)
  // Owns the generation token that drops a read a newer swap superseded. See
  // `createDraftSwapper` for why the token lives there and not here. Declared
  // with the other per-editor state, because `onMount` runs its own initial
  // replace through it: every path to a document replacement takes one token.
  const swapDraft = createDraftSwapper()

  /**
   * The box's expand/collapse decision and the three DOM measurements it needs.
   * Every probe, observer, and threshold lives in that one unit — see
   * `./editorLayout`.
   */
  const layout = createEditorLayout({
    editorRoot: () => editorRef,
    row: () => editorRowEl,
    actionSlot: () => footerSlotEl,
    firstBlock: () => editorRef?.querySelector('.ProseMirror > *'),
  })

  // A full-width action row (a control request) forces the expanded layout, so
  // its two-zone row always renders below the text with the separator above it.
  const isExpanded = () => layout.contentExpanded() || props.actions?.layout === 'fullWidth'

  /** Compute the stored draft key, incorporating controlRequestId when present. */
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

  /**
   * Editor wrapper sizing.
   *
   * `pinnedHeight` is a floor while the content is shorter and a fixed height
   * once it is taller, so a dragged handle keeps the height the reader chose.
   * That fixed height also stops `maxHeight` from ever binding, which is why a
   * host that wants a floor AND a ceiling states `minHeight` instead: it stays
   * a floor at every content size, and the box grows to `maxHeight`.
   */
  const editorWrapperStyle = (): JSX.CSSProperties => {
    const style: JSX.CSSProperties = {}
    const pinned = props.pinnedHeight
    if (pinned != null) {
      const overflowing = contentHeight() > 0 && pinned < contentHeight()
      style[overflowing ? 'height' : 'min-height'] = `${pinned}px`
    }
    else if (props.minHeight != null) {
      style['min-height'] = `${props.minHeight}px`
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
  let onMarkdownChangeRef: MarkdownEditorProps['onMarkdownChange']

  /**
   * Reports the document to the host: its markdown, and whether it holds
   * anything.
   *
   * ONE function for both callbacks, because `hasContent` is DERIVED from the
   * markdown and every site that reported one owed the other. Reporting them
   * separately meant each new document-replacement path had to remember two
   * calls, and the draft-load path already forgot: it announced `hasContent`
   * and never announced the text, so a host with `onMarkdownChange` learned
   * that content existed and never learned what it was until the user typed.
   *
   * Not a signal. This component never reads the text back -- it asks the
   * serializer whenever it needs one -- so storing it would be a second copy
   * that only drifts.
   */
  const emitDocument = (markdown: string) => {
    onMarkdownChangeRef?.(markdown)
    onContentChangeRef?.(markdown.trim().length > 0)
  }

  // Repaint the composer's code blocks when the syntax theme changes.
  //
  // Shiki bakes each token's colour in at tokenize time, so an already-decorated
  // block keeps the abandoned theme until something recomputes it -- and
  // prosemirror-highlight recomputes only on a document change. Without this the
  // composer visibly disagreed with the chat beside it, which the user had just
  // repainted, for the rest of the session.
  createEffect(on(syntaxThemeGeneration, () => {
    if (!editorInstance)
      return
    try {
      editorInstance.action((ctx: Ctx) => {
        refreshEditorHighlight(ctx.get(editorViewCtx))
      })
    }
    catch {
      // The editor is mid-teardown, or the view is gone. A repaint is not worth
      // failing a theme change over: the next mount highlights from scratch.
    }
  }, { defer: true }))

  const editorHasFocus = (): boolean => {
    if (!editorInstance)
      return false
    let focused = false
    try {
      editorInstance.action((ctx: Ctx) => {
        focused = ctx.get(editorViewCtx).hasFocus()
      })
    }
    catch {
      // The editor is mid-teardown. Nothing holds a caret worth repairing.
    }
    return focused
  }

  /**
   * Keep or release the caret after a send. It never takes a caret back. See
   * `decideSendFocus` for which of the three outcomes each send gets, and why.
   */
  const applySendFocus = (hadFocus: boolean, sent: boolean) => {
    // Decide from the caret's CURRENT owner, not the one the send started with.
    // A send now resolves after an await, and only the USER can move the caret
    // while that request runs: keepFocusOnPress stops every send control from
    // taking it, and `replaceAll` dispatches a transaction that keeps the
    // contenteditable node and its focus. So a caret that left the editor left
    // on purpose, and the send must leave it there -- taking it back raises the
    // on-screen keyboard over the transcript the user just uncovered, which is
    // the one thing this module must never do.
    const action = decideSendFocus({
      hadFocus: hadFocus && editorHasFocus(),
      sent,
      softKeyboardVisible: isSoftKeyboardVisible(),
    })
    // `restore` moves nothing: the editor already holds the caret. It stays a
    // separate outcome because it must NOT release the keyboard.
    if (action === 'release')
      dismissSoftKeyboard()
  }

  const handleSend = async () => {
    if (props.disabled || !editorInstance)
      return
    // Read the caret BEFORE anything moves it: `onSendRef` can open a dialog,
    // and `replaceAll` rebuilds the document under the selection.
    const hadFocus = editorHasFocus()
    const initialDraftKey = getDraftKey()
    const sentDraftIsCurrent = () => getDraftKey() === initialDraftKey
    // Read markdown directly from ProseMirror's document state rather than
    // the `markdown` signal, which is updated by a debounced listener (200ms)
    // and may be stale when Enter is pressed immediately after typing.
    let text = ''
    let initialDocument: ProseMirrorNode | undefined
    try {
      editorInstance.action((ctx: Ctx) => {
        const serializer = ctx.get(serializerCtx)
        const view = ctx.get(editorViewCtx)
        initialDocument = view.state.doc
        text = serializer(view.state.doc).trim()
      })
    }
    catch {
      return
    }
    const sentContentIsCurrent = () => {
      if (!sentDraftIsCurrent() || !initialDocument)
        return false
      let matches = false
      const submittedDocument = initialDocument
      try {
        editorInstance?.action((ctx: Ctx) => {
          matches = submittedDocument.eq(ctx.get(editorViewCtx).state.doc)
        })
      }
      catch { /* The editor can close before the send response arrives. */ }
      return matches
    }
    if (!text) {
      // Allow sending empty text only when explicitly enabled (e.g. Enter-to-approve for control requests).
      if (allowEmptySendRef) {
        let emptySendResult: boolean | void
        try {
          emptySendResult = await onSendRef('')
        }
        catch {
          applySendFocus(hadFocus && sentContentIsCurrent(), false)
          return
        }
        if (emptySendResult === false) {
          applySendFocus(hadFocus && sentContentIsCurrent(), false)
          return
        }
        if (initialDraftKey && (!sentDraftIsCurrent() || sentContentIsCurrent()))
          clearDraft(initialDraftKey)
        props.onAfterSend?.()
      }
      // An empty draft commits only under `allowEmptySend`; otherwise nothing
      // left the composer and the caret stays for the user to type into.
      applySendFocus(hadFocus && sentContentIsCurrent(), allowEmptySendRef)
      return
    }
    let sendResult: boolean | void
    try {
      sendResult = await onSendRef(text)
    }
    catch {
      applySendFocus(hadFocus && sentContentIsCurrent(), false)
      return
    }
    if (sendResult === false) {
      applySendFocus(hadFocus && sentContentIsCurrent(), false)
      return
    }
    // The send committed. Everything below runs AFTER the await, so a throw
    // here becomes an unhandled rejection at every call site -- each one
    // discards the promise this async handler answers with. Report it and keep
    // it inside the handler.
    //
    // The caret repair stays LAST, after `replaceAll`, because `replaceAll` is
    // what rebuilds the document under the selection. It cannot move before the
    // await: iOS Safari refuses a programmatic `view.focus()` that raises the
    // soft keyboard outside a user gesture, and the RPC ends the gesture -- but
    // an earlier call repairs nothing, because focus is still on the editor
    // there and `applySendFocus` skips a view that already holds it.
    // The host may have unmounted this editor from inside its own `onSend`:
    // `SetGoalDialog` closes on a successful submit, which disposes the whole
    // subtree synchronously. `onCleanup` then set `disposed` and destroyed the
    // editor before this continuation resumed, so rebuilding the document below
    // would dispatch into a detached view. The draft still has to be cleared --
    // `onCleanup` saved the SENT text as a draft on its way out, and leaving it
    // there resurrects a sent message in the composer on the next open.
    if (disposed) {
      if (initialDraftKey)
        clearDraft(initialDraftKey)
      props.onAfterSend?.()
      return
    }
    try {
      const clearSubmittedContent = sentContentIsCurrent()
      if (clearSubmittedContent) {
        editorInstance.action(replaceAll(''))
        emitDocument('')
      }
      if (initialDraftKey && (!sentDraftIsCurrent() || clearSubmittedContent))
        clearDraft(initialDraftKey)
      props.onAfterSend?.()
      applySendFocus(hadFocus && clearSubmittedContent, true)
    }
    catch (error) {
      logger.warn('Failed to reset the composer after a send:', error)
    }
  }

  // Enter key mode reference for ProseMirror plugin (closures capture signal)
  let enterModeRef: EnterKeyMode = 'cmd-enter-sends'
  createEffect(() => {
    enterModeRef = enterMode()
  })
  let disabledRef = false
  let placeholderRef = 'Send a message...'
  let disabledPlaceholderRef = ''
  let onTogglePlanModeRef: (() => void) | undefined
  createEffect(() => {
    onTogglePlanModeRef = props.onTogglePlanMode
  })
  createEffect(() => {
    onSendRef = props.onSend
    allowEmptySendRef = props.allowEmptySend ?? false
    onContentChangeRef = props.onContentChange
    onMarkdownChangeRef = props.onMarkdownChange
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

  // ONE effect for the three decoration-visible props, so a change that moves
  // two of them -- switching to a read-only subagent flips `disabled` and
  // `disabledPlaceholder` together -- dispatches one empty transaction instead
  // of three. Each dispatch re-applies every plugin's state and re-diffs the
  // decorations (placeholder, code-language labels, syntax highlight), and the
  // earlier ones ran against refs the later effects had not assigned yet.
  createEffect(() => {
    disabledRef = props.disabled ?? false
    placeholderRef = props.placeholder ?? 'Send a message...'
    disabledPlaceholderRef = props.disabledPlaceholder ?? ''
    forceDecorationUpdate()
  })

  const applyEditorState = (editor: Editor) => {
    try {
      const disabled = disabledRef
      // The composer takes the keyboard when it finishes building, and this
      // diff moved that moment behind an awaited draft read -- so it can now
      // land while something else already owns the keyboard. `suppressAutoFocus`
      // is how the shell says so; the editor is still made editable either way,
      // because only the FOCUS is in question.
      const suppressed = props.suppressAutoFocus?.() === true
      editor.action((ctx: Ctx) => {
        const view = ctx.get(editorViewCtx)
        view.setProps({ editable: () => !disabled })
        if (!disabled && !suppressed) {
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
  let prevDraftKey: string | null | undefined
  createEffect(() => {
    const key = getDraftKey()
    if (key)
      latestDraftKey = key
  })

  /**
   * Build the editor and attach everything that depends on it.
   *
   * A plain function rather than the `onMount` callback itself, so the caller
   * below can attach a `catch` -- see there.
   */
  const attachEditor = async () => {
    if (!editorRef)
      return

    const owner = getOwner()
    // Before the await, so the layout's observers register their cleanup on a
    // LIVE owner. They measure the row and the action slot, neither of which
    // waits on the editor -- and a cleanup registered after the await would
    // never run when the component unmounts while `buildEditor` is pending.
    layout.observe()

    const initialDraftKey = getDraftKey()
    // Awaited here, ahead of `buildEditor`, so the editor is still constructed
    // with its content in hand. The draft read is asynchronous now, and this
    // `onMount` already awaits `buildEditor`, so it costs no extra round trip.
    const initialDraft = initialDraftKey ? await loadDraft(initialDraftKey) : { content: '', cursor: -1 }
    // `initialMarkdown` WINS over a stored draft -- see the prop. A host that
    // states the starting text is editing a specific document, and a draft is
    // the text it abandoned last time; showing the draft would silently edit
    // the wrong thing.
    const seed = props.initialMarkdown ?? initialDraft.content
    const seedCursor = props.initialMarkdown != null ? -1 : initialDraft.cursor

    const editor = await buildEditor({
      editorRoot: editorRef,
      initialContent: seed,
      ariaLabelledBy: props.ariaLabelledBy,
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
      onDocument: emitDocument,
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
    // `buildEditor` was handed `initialDraft`, so THIS is the key whose document
    // the editor holds. It moves again below only if a replace actually lands.
    prevDraftKey = initialDraftKey ?? null
    // A one-time snapshot ON PURPOSE: the question is which key the editor
    // should be showing at the instant the build finished, and the swap effect
    // owns every change after that.
    const initialReadyDraftKey = getDraftKey()
    let readyDraft = initialDraft
    if (initialReadyDraftKey !== initialDraftKey) {
      // THROUGH THE SWAPPER, like every other document replacement. The key
      // changed while `buildEditor` was pending, and the swap effect may already
      // be reading for a THIRD key -- so this replace has to take a token too,
      // or the mount's older prose lands last. Routing it here is also what
      // makes `createDraftSwapper`'s stated invariant true.
      await swapDraft(initialReadyDraftKey ?? null, (draft) => {
        // The read is another await, so it re-opens the window the check above
        // closed: dispatching into an editor `onCleanup` already destroyed
        // throws out of ProseMirror as an unhandled rejection.
        if (disposed)
          return
        readyDraft = draft
        editor.action(replaceAll(draft.content))
        emitDocument(draft.content)
        prevDraftKey = initialReadyDraftKey ?? null
      })
      // Checked AGAIN, for the same reason it is checked after `buildEditor`:
      // nothing below this line would ever be torn down.
      if (disposed) {
        editor.destroy()
        return
      }
    }
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
    // Notify parent if we loaded a draft with content, and restore cursor
    // position. `prevDraftKey` is the key whose document the editor now holds,
    // whichever of the two reads above produced it.
    // A seeded document is announced HERE or nowhere: Milkdown's
    // `markdownUpdated` listener never fires for `defaultValueCtx`, so a host
    // with `onMarkdownChange` would otherwise learn nothing until the reader
    // typed.
    if (props.initialMarkdown != null ? seed !== '' : Boolean(prevDraftKey && readyDraft.content)) {
      // Both halves. Milkdown's `markdownUpdated` listener never fires for a
      // document seeded this way, so this is the only announcement a host with
      // `onMarkdownChange` gets for a loaded draft.
      //
      // Report what the DOCUMENT holds, not the stored draft string. The round
      // trip through ProseMirror is not the identity -- it escapes `*`, `_` and
      // `&`, and refolds a code block -- so echoing the input hands the host a
      // string that differs from the one a send would submit. `editorRefHandlers`
      // re-serializes for the same reason; this is the same path for a draft.
      try {
        editor.action((ctx: Ctx) => {
          emitDocument(ctx.get(serializerCtx)(ctx.get(editorViewCtx).state.doc))
        })
      }
      catch {
        // The editor is not ready to serialize, so the stored text is the best
        // answer available. A host that hears nothing at all is worse.
        emitDocument(readyDraft.content)
      }
      try {
        // `-1` for a seed, which `restoreCursor` reads as the document END --
        // a reader who opens Replace continues the objective rather than
        // typing in front of it.
        restoreCursor(editor, props.initialMarkdown != null ? seedCursor : readyDraft.cursor)
      }
      catch { /* editor may not be ready */ }
    }

    setupEditorRefHandlers({
      editor,
      onDocument: emitDocument,
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
  }

  /**
   * The build is asynchronous and it can fail: a plugin factory that throws, or
   * `Editor.create()` rejecting.
   *
   * Without this `catch` the rejection is unhandled and the box is simply DEAD
   * -- `onReady` never fires, the imperative refs are never installed, and a
   * host that drives its own submit button through `sendRef` has no route to
   * the action at all. That is a grey rectangle with no message. Say so
   * instead, and log the cause.
   */
  onMount(() => {
    void attachEditor().catch((error) => {
      logger.error('The editor failed to build:', error)
      setBuildFailed(true)
    })
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

      // Close both popovers BEFORE the document is replaced. Each holds absolute
      // positions into the OUTGOING document, and this component is reused across
      // draft keys, so a popover left open would act on a document its positions
      // no longer describe -- writing an href onto unrelated text, or throwing.
      setLinkPopoverOpen(false)
      setLinkRange(null)
      setCodeLangPopoverOpen(false)
      setCodeLangNodePos(-1)

      // Load the draft for the new key and replace the editor content.
      //
      // `prevDraftKey` MOVES WITH THE DOCUMENT, inside `apply` below, never
      // here. It identifies the key whose text the editor holds, and the read is
      // asynchronous -- so until it lands the editor still shows the OUTGOING
      // key's prose. Moving the pointer up front made a second swap arriving
      // during this read save that outgoing prose under the incoming key, which
      // destroys the incoming key's saved draft. The `save` above is correct
      // either way, because it always saves what is on screen under the key that
      // owns it.
      const swapTarget = editorInstance
      // Captured before the await. `props` is reactive, and reading a prop off
      // it inside the callback below would read it outside any tracked scope --
      // which is what solid/reactivity flags, and it is right: the handler this
      // swap belongs to is the one that was installed when the swap started.
      //
      // The document report does NOT go through a capture like this one. It
      // goes through `emitDocument`, which reads the mirrored refs and so
      // reports the LATEST handlers. That was already true of the markdown half
      // before the two were folded together, so the pair used to disagree here:
      // one announcement reached the current host and the other reached the
      // host that was current when the read began.
      const notifyDraftKeyChanged = props.onDraftKeyChanged
      void swapDraft(newDraftKey, (draft) => {
        // The component unmounted while the read was in flight. `onCleanup`
        // destroyed `swapTarget` and cancelled the draft debounce, so replacing
        // the document now would re-arm that debounce against a disposed editor
        // -- and the notify below would hand a destroyed editor to
        // `AgentEditorPanel`, which answers it by writing into one.
        if (disposed)
          return
        try {
          swapTarget.action(replaceAll(draft.content))
          restoreCursor(swapTarget, draft.cursor)
          emitDocument(draft.content)
          prevDraftKey = newDraftKey
        }
        catch { /* editor may not be ready */ }
        // AFTER the document is replaced, never before. `AgentEditorPanel`
        // answers this by writing the queue-edit text into the editor, and the
        // replace above would then wipe what it just wrote -- the saved draft
        // for a freshly opened queue edit is empty. The read is asynchronous,
        // so "notify first" is no longer the same instant as "document ready";
        // it was, when the draft load was synchronous.
        notifyDraftKeyChanged?.(newDraftKey)
      })
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

  const markers = () => SURFACE_MARKERS[props.surface]
  const ids = () => testIds(markers().testIdPrefix)
  // Resolved ONCE. `props.plus` is a prop getter, so reading it twice -- once to
  // insert it and once to ask whether it exists -- builds the menu twice and
  // mounts two copies of it. `children` memoizes the resolved node, which is
  // what makes the question below safe to ask.
  const plusNode = children(() => props.plus)

  /**
   * Whether the `[+]` slot renders anything at all.
   *
   * Through `toArray`, never as a bare truthiness test on `plusNode()`.
   * `children()` resolves a fragment to an ARRAY, and `[]` is TRUTHY -- so
   * `plus={<></>}`, a `<For>` over an empty list, or `<>{a && <A/>}{b && <B/>}</>`
   * (which resolves to `[false, false]`) all read as "a button is there" and
   * reserve a ~40px left column for nothing. A caller that writes
   * `plus={cond && <X/>}` hands this a bare `false`, which `toArray` wraps, so
   * one test covers both shapes.
   */
  const hasPlus = () =>
    plusNode.toArray().some(node => node != null && node !== false && node !== '')

  return (
    <div
      class={styles.container}
      // The box whose layout mode `data-expanded` states. A test that asserts
      // the collapsed-versus-expanded decision has to address this element, and
      // the class name is a build-mode-dependent hash.
      data-testid={ids().box}
      data-expanded={isExpanded() ? '' : undefined}
      // Whether a `[+]` button is rendered at all. The stylesheet reserves the
      // left column for it, and a box without one starts its text 40px in for
      // no reason -- see `--editor-left-pad` in the stylesheet.
      data-plus={hasPlus() ? '' : undefined}
      style={{
        '--editor-right-pad': `${layout.rightPad()}px`,
        // Omitted until measured, so the stylesheet's own fallback applies.
        ...(layout.actionsHeight() > 0 ? { '--editor-actions-h': `${layout.actionsHeight()}px` } : {}),
      }}
    >
      {props.banner}
      {/* The box is unusable when the build failed, and it looks identical to
          an empty one. Say what happened, so the reader knows to reload rather
          than retyping into a field that will never send. */}
      <Show when={buildFailed()}>
        <div class={errorText} role="alert" data-testid={ids().buildFailed}>
          The editor failed to load. Reload the page to try again.
        </div>
      </Show>
      <div class={styles.editorRow} ref={editorRowEl}>
        <div class={styles.plusSlot} data-testid={ids().plusSlot}>{plusNode()}</div>
        <div
          class={styles.editorWrapper}
          ref={editorRef}
          data-testid={ids().editor}
          // Only the message composer claims it. `useShortcuts` reads it for the
          // `chatInputFocused` context, and `$mod+j` sends the chat message
          // there -- so a goal editor carrying it would send a message from
          // inside a dialog. See `MarkdownEditorSurface`.
          data-chat-input={markers().chatInput ? '' : undefined}
          style={editorWrapperStyle()}
        />
        {/* Separator between text area and button row in expanded mode. Positioned
            at the top of the button reservation (the editor row's padding-bottom
            area) so it sits right between the text and the buttons. */}
        <div class={styles.editorSeparator} data-testid={ids().separator} />
        {/* The action cluster: compact Interrupt/Send (actions) when composing,
            or the full-width control-request actions (footer) when a request is
            active. `data-full-width` distinguishes the two so the expanded
            layout can stretch the control-request footer across the box. */}
        <div class={styles.footerSlot} ref={footerSlotEl} data-testid={ids().footerSlot} data-full-width={props.actions?.layout === 'fullWidth' ? '' : undefined}>{props.actions?.node()}</div>
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
