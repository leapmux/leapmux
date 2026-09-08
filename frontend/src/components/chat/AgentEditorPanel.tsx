import type { Component } from 'solid-js'
import type { FileAttachment, PendingAttachmentFile } from './attachments'
import type { EditorContentRef } from './controls/types'
import type { PermissionPresetController, ProviderSettingChangeHandler } from './providerSettings'
import type { BeginQueueEdit } from './queueEditSession'
import type { WorkingTreeInfo } from '~/components/common/WorkingTree'
import type { BranchMenuActions } from '~/components/workspace/branchActions'
import type { AgentInfo, AgentInputQueueSnapshot, QueuedAgentInput } from '~/generated/proto/leapmux/v1/agent_pb'
import type { AgentSessionInfo } from '~/stores/agentSession.store'
import type { ControlRequest } from '~/stores/control.store'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { Tab } from '~/stores/tab.types'
import Pause from 'lucide-solid/icons/pause'
import Play from 'lucide-solid/icons/play'
import SendHorizontal from 'lucide-solid/icons/send-horizontal'
import Square from 'lucide-solid/icons/square'
import { createEffect, createMemo, createSignal, on, onCleanup, onMount, Show, untrack } from 'solid-js'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { Spinner } from '~/components/common/Spinner'
import { Tooltip } from '~/components/common/Tooltip'
import { usePreferences } from '~/context/PreferencesContext'
import { AgentInputQueuePauseReason, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { EDITOR_MIN_HEIGHT } from '~/lib/editor/editorMinHeight'
import { keepFocusOnPress } from '~/lib/focusRetention'
import { flavorFromOs } from '~/lib/paths'
import { formatResetTimestamp, getResetsAt } from '~/lib/rateLimitUtils'
import { dismissSoftKeyboard } from '~/lib/softKeyboard'
import { requestInstanceId } from '~/stores/control.store'
import { registerEditorRef, unregisterEditorRef } from '~/stores/editorRef.store'
import { registerChatPanel, unregisterChatPanel } from '~/stores/focusedChatPanel.store'
import { repoGitView } from '~/stores/repoGit'
import { optionValuesFromGroups } from '~/stores/tab.helpers'
import { workerInfoStore } from '~/stores/workerInfo.store'
import { hideInNarrowComposer } from '~/styles/shared.css'
import { iconSize } from '~/styles/tokens'
import { useAgentInfoCard } from './AgentInfoCard'
import { AgentInputQueue } from './AgentInputQueue'
import { AgentInputQueuePauseBanner } from './AgentInputQueuePauseBanner'
import { clearAttachments as clearCachedAttachments } from './attachments'
import { AttachmentStrip } from './AttachmentStrip'
import * as styles from './ChatView.css'
import { ComposerPlusMenu } from './composer/ComposerPlusMenu'
import { ComposerStatusBar } from './composer/ComposerStatusBar'
import { ControlRequestActions, ControlRequestContent } from './ControlRequestBanner'
import { useControlResponseHandling } from './controlResponseHandling'
import { actionButtonClass } from './controls/ControlActionRow'
import { createControlAnswerState } from './controls/types'
import { MarkdownEditor } from './markdownEditor/MarkdownEditor'
import { providerFor } from './providers/registry'
import { activePermissionPreset, usablePresets } from './providerSettings'
import { createQueueEditSession } from './queueEditSession'
import {
  OPTION_ID_MODEL,
  optionGroup,
  selectedModelContextWindow,
} from './settingsGroups'
import { useChatAttachments } from './useChatAttachments'
import { useEditorMinHeight } from './useEditorMinHeight'
import { ContextUsageGrid } from './widgets/ContextUsageGrid'

export interface AgentEditorPanelProps {
  /** See `MarkdownEditorProps.suppressAutoFocus`. Forwarded unchanged. */
  suppressAutoFocus?: () => boolean
  agentId: string
  agent?: AgentInfo
  /**
   * Why the composer accepts no input, when it does not (e.g. a non-steerable
   * subagent). Its PRESENCE is what disables the composer, so a dead box with no
   * stated reason is unrepresentable -- and every surface that states it (the
   * editor's placeholder, the `[+]` menu's attach item, each settings submenu)
   * shows this one resolved string rather than inventing its own wording.
   */
  disabledReason?: string
  onSendMessage: (content: string, attachments?: FileAttachment[]) => void | Promise<void>
  onSendControlFeedback?: (content: string) => void | Promise<void>
  inputQueue?: AgentInputQueueSnapshot
  queueClientId?: string
  onBeginQueueEdit?: BeginQueueEdit
  onUpdateQueueItem?: (item: QueuedAgentInput, text: string, attachments: FileAttachment[]) => Promise<void>
  onCancelQueueEdit?: (item: QueuedAgentInput) => Promise<void>
  onDeleteQueueItem?: (item: QueuedAgentInput) => Promise<void>
  onMoveQueueItem?: (item: QueuedAgentInput, beforeInputId: string) => Promise<void>
  onRetryQueueItem?: (item: QueuedAgentInput, confirmUncertain: boolean) => Promise<void>
  onSteerQueueItem?: (item: QueuedAgentInput) => Promise<void>
  onSetQueuePaused?: (paused: boolean) => Promise<void>
  focusRef?: (focus: () => void) => void
  controlRequests?: ControlRequest[]
  onControlResponse?: (request: ControlRequest, content: Uint8Array) => Promise<void>
  /** Single dispatcher for all settings panel changes (model/effort/permissionMode/optionGroup). */
  onSettingChange?: ProviderSettingChangeHandler
  onInterrupt?: () => void
  /**
   * Whether Interrupt can target this agent alone. Omit (or pass true) for a
   * root agent; pass false for a subagent tab whose provider cannot interrupt a
   * single subagent, which hides the button instead of offering a click that
   * can only fail.
   */
  canInterrupt?: boolean
  settingsLoading?: boolean
  agentSessionInfo?: AgentSessionInfo
  agentWorking?: boolean
  /**
   * Every branch-menu action for the agent's repo, already bound to that
   * branch. Wired from the shell, which built them over the BranchRef the
   * agent's git status + worker id resolve to. Undefined leaves the branch
   * chip non-interactive.
   */
  branchActions?: BranchMenuActions
  /**
   * The Worker the agent's branch is checked out on. The branch menu lists
   * THAT Worker's agent providers and shells, not the ones already loaded for
   * whichever tab is focused.
   */
  branchWorkerId?: string
  /**
   * Why the branch actions are unusable (e.g. worker offline), or undefined
   * when usable. Every action needs the Worker, so one reason covers them all.
   */
  branchDisabledReason?: string
  /** Repo-keyed git store for branch label and info-card flags. */
  repoGitStore: ReturnType<typeof createRepoGitStore>
  /** Tab git identity for {@link repoGitView} (includes `workingDir` for file tabs). */
  gitTab?: Pick<Tab, 'workerId' | 'gitToplevel' | 'workingDir'>
  /** Height of the parent container, used for max editor height calculation. */
  containerHeight?: number
  /** Ref to expose the addFiles function for external callers (e.g. ChatDropZone). */
  addFilesRef?: (fn: (files: FileList | File[] | PendingAttachmentFile[]) => Promise<number>) => void
  /** Ref to expose directory-aware drop handling for external callers (e.g. ChatDropZone). */
  addDropDataTransferRef?: (fn: (dataTransfer: DataTransfer) => Promise<number>) => void
  /**
   * Ref to expose the triggerSend function for external callers. The send
   * awaits an RPC, so it answers a promise that the caller discards.
   */
  triggerSendRef?: (fn: () => void | Promise<void>) => void
}

/**
 * Swallows the rejection of a queue RPC.
 *
 * `runQueueRpc` in `~/lib/agentInputQueueOperations` already shows the failure
 * to the user and then rethrows, so every caller here owes the promise a
 * handler and owes the user nothing further. One helper, so the reason is
 * stated once rather than at each of the eight call sites.
 */
function fireQueueRpc(call: Promise<unknown> | undefined): void {
  void call?.catch(() => {})
}

/**
 * The queue's pause toggle, which shares the composer's action cluster with
 * Interrupt and Send.
 *
 * `hideInNarrowComposer` hides the word below `sm`, so the three buttons shrink to
 * icons together on a phone and the cluster stops crowding the `[+]` button.
 * The tooltip carries the name once the word is gone -- and, through
 * `ariaLabel`, so does the accessibility tree, which reads nothing from a
 * `display: none` label.
 */
const AgentInputQueuePauseButton: Component<{
  paused: boolean
  busy: boolean
  onToggle: () => void
}> = (props) => {
  const label = () => (props.paused ? 'Resume Queue' : 'Pause Queue')
  return (
    <Tooltip text={label()} ariaLabel>
      <button
        type="button"
        class={actionButtonClass(true)}
        disabled={props.busy}
        onMouseDown={keepFocusOnPress}
        onClick={() => props.onToggle()}
        data-testid="queue-pause-button"
      >
        <Icon icon={props.paused ? Play : Pause} size="sm" />
        <span class={hideInNarrowComposer}>{label()}</span>
      </button>
    </Tooltip>
  )
}

export const AgentEditorPanel: Component<AgentEditorPanelProps> = (props) => {
  let panelRef: HTMLDivElement | undefined
  const [_editorContentHeight, setEditorContentHeight] = createSignal(0)
  const [hasContent, setHasContent] = createSignal(false)
  let fileInputRef: HTMLInputElement | undefined
  // The spinner signal. `createLoadingSignal` holds it true for a debounce
  // window after `stop()`, so a spinner that appears never flashes away.
  const { loading: sending, start: startSending, stop: stopSending } = createLoadingSignal()
  // Whether the enqueue RPC is still in flight. Every attachment path reads
  // THIS, never `sending()`: the debounce window that steadies the spinner must
  // never refuse a paste, a drop, or the file picker, because the enqueue
  // usually finishes in milliseconds and the user gets a dead composer for the
  // rest of the second.
  const [enqueueInFlight, setEnqueueInFlight] = createSignal(false)
  const interruptLoading = createLoadingSignal()
  // A paused queue changes what Send DOES, and it is what the banner and the
  // two pause toggles state. ONE accessor answers the question for all four, so
  // a change to the source or to the default cannot reach three of them and
  // miss the fourth.
  const queuePaused = () => props.inputQueue?.paused ?? false
  // ONE in-flight mark for the pause RPC, shared by the two controls that fire
  // it: the banner's Resume and the composer's toggle. Neither updates
  // optimistically -- `queuePaused()` moves only when the Worker's snapshot
  // lands -- so both still read "Resume" for the whole round trip, which is
  // what invites a second press. The state converges either way, because the
  // RPC carries an absolute boolean; the cost is that `runQueueRpc` raises one
  // warn toast per failure, so two presses of one intent raise two toasts.
  //
  // A plain signal, NOT `createLoadingSignal`. That hook holds `loading` true
  // for a one-second debounce after `stop()`, which steadies a spinner but
  // would leave this toggle dead for a second after a flip that normally
  // settles in milliseconds.
  const [pauseInFlight, setPauseInFlight] = createSignal(false)
  const setQueuePaused = (paused: boolean) => {
    if (pauseInFlight())
      return
    const call = props.onSetQueuePaused?.(paused)
    if (!call)
      return
    setPauseInFlight(true)
    fireQueueRpc(call.finally(() => setPauseInFlight(false)))
  }

  const currentProviderLabel = () => agentProviderLabel(props.agent?.agentProvider)

  // The reason the composer is dead, resolved ONCE. Both surfaces that state it
  // -- the editor's placeholder and the [+] menu's attach item -- take this
  // resolved string rather than the raw prop, so an absent reason cannot become
  // two different defaults applied in two leaves.
  //
  // There is no separate note above the box. The placeholder sits INSIDE the
  // box the reason is about, so a note above it repeated the same sentence a
  // few pixels higher.
  const disabled = () => !!props.disabledReason
  const preferences = usePreferences()
  // The retry confirmation belongs to the dialog below, not to the queue-edit
  // session: it holds the input the user asked to retry, and no edit is open.
  const [uncertainRetry, setUncertainRetry] = createSignal<QueuedAgentInput>()
  // The queue-edit session owns the open edit and everything it holds. The
  // panel creates it BEFORE `useChatAttachments`, because `attachmentDraftKey`
  // is that hook's input, and calls `bindAttachments` below with that hook's
  // outputs. See `bindAttachments` for the rule.
  const queueEdit = createQueueEditSession({
    agentId: () => props.agentId,
    inputQueue: () => props.inputQueue,
    clientId: () => props.queueClientId,
    onBeginQueueEdit: () => props.onBeginQueueEdit,
  })

  const att = useChatAttachments({
    agentId: queueEdit.attachmentDraftKey,
    agentProvider: () => props.agent?.agentProvider ?? AgentProvider.CLAUDE_CODE,
    providerLabel: currentProviderLabel,
  })
  queueEdit.bindAttachments({
    attachments: att.attachments,
    activeDraftKey: att.activeDraftKey,
    replaceAttachments: att.replaceAttachments,
    clearAllAttachments: att.clearAllAttachments,
  })
  const attachments = att.attachments
  const acceptAttribute = att.acceptAttribute
  const addFiles = att.addFiles
  const removeAttachment = att.removeAttachment
  const clearAllAttachments = att.clearAllAttachments
  const addFilesWhenReady = (...args: Parameters<typeof addFiles>) => enqueueInFlight() ? Promise.resolve(0) : addFiles(...args)
  const addDropDataTransferWhenReady = (dataTransfer: DataTransfer) => enqueueInFlight() ? Promise.resolve(0) : att.addDroppedDataTransfer(dataTransfer)
  const handleFileInputChange = () => {
    if (enqueueInFlight()) {
      if (fileInputRef)
        fileInputRef.value = ''
      return
    }
    att.handleFileInputChange(fileInputRef)
  }

  const editorHeight = useEditorMinHeight({
    agentId: () => props.agentId,
    containerHeight: () => props.containerHeight,
    panelRef: () => panelRef,
  })
  const editorMinHeightSignal = editorHeight.editorMinHeight
  const isDragging = editorHeight.isDragging
  const handleResizeStart = editorHeight.handleResizeStart
  const resetEditorHeight = editorHeight.resetEditorHeight

  // The user's in-progress answer to the active control request. It lives HERE,
  // above both control slots, so a rebuild of a control component cannot discard
  // it; `controlResponseHandling` saves and restores it per request instance.
  const answerState = createControlAnswerState()

  // Editor content ref for programmatic get/set of editor markdown.
  let editorContentRef: EditorContentRef | undefined
  let editorFocusFn: (() => void) | undefined
  let editorInsertFn: ((text: string) => void) | undefined
  // Whether the MarkdownEditor has fully initialized (draft loaded, cursor restored).
  let editorReady = false

  // Track the agent ID for which the editor ref is registered.  props.agentId
  // is a reactive getter that may return null/undefined at cleanup time (e.g.
  // when the <Show> that controls this component unmounts because the focused
  // agent changed), so we must track the registered ID non-reactively.
  let registeredAgentId: string | null = null

  /** Register the editor ref if the editor is ready and both refs are available. */
  const tryRegisterEditorRef = (agentId: string) => {
    if (editorReady && editorContentRef && editorFocusFn) {
      // `writable` reads the SAME predicate that drives the disabled placeholder,
      // the send button, and the Enter-to-send plugin, so every surface agrees
      // about whether this composer takes input. It is passed as a thunk because
      // `disabledReason` is reactive: a subagent tab resolves it again once the
      // worker's authoritative acceptsMessages arrives.
      registerEditorRef(agentId, {
        get: editorContentRef.get,
        set: editorContentRef.set,
        focus: editorFocusFn,
        insert: text => editorInsertFn?.(text),
        writable: () => !disabled(),
      })
      registeredAgentId = agentId
    }
  }

  // Register/unregister editor refs with the global registry.
  onMount(() => {
    onCleanup(() => {
      if (registeredAgentId) {
        unregisterEditorRef(registeredAgentId)
        registeredAgentId = null
      }
      if (panelRef)
        unregisterChatPanel(panelRef)
    })
  })
  createEffect(on(() => props.agentId, (agentId, prevAgentId) => {
    if (prevAgentId) {
      unregisterEditorRef(prevAgentId)
      if (registeredAgentId === prevAgentId)
        registeredAgentId = null
    }
    tryRegisterEditorRef(agentId)
  }))

  // Current (optimistically-updated) selections, derived from the option-group
  // catalog the agent reports: each well-known axis's `currentValue`. The proto
  // AgentInfo no longer carries scalar model/effort/permissionMode fields, so the
  // settings dropdown and plan-mode toggle read them from here.
  const currentModel = () => optionGroup(props.agent?.optionGroups, OPTION_ID_MODEL)?.currentValue || ''
  // Every axis's confirmed value as one generic map keyed by group id, derived from
  // the catalog (the proto AgentInfo carries no scalar model/effort/permission fields).
  const currentOptionValues = () => optionValuesFromGroups(props.agent?.optionGroups)
  // The permission presets a control request's pill group may apply, offered
  // under the same rule as the composer `[+]` menu's permission items (`usablePresets`):
  // a preset is offered only when the live catalog carries every axis it sets. The one
  // `apply` handler is the shared `onSettingChange`, so a pill selection and the
  // menu item cannot diverge in what they switch.
  const permissionPresets = createMemo<PermissionPresetController | undefined>(() => {
    const presets = props.agent?.agentProvider
      ? providerFor(props.agent.agentProvider)?.permissionPresets
      : undefined
    const usable = usablePresets(presets, props.agent?.optionGroups)
    // The preset the session already has on. A control request's pill group
    // opens on it, and only this scope holds both halves it needs -- the live
    // catalog and the confirmed values.
    const active = activePermissionPreset(usable, props.agent?.optionGroups, currentOptionValues())
    return Object.keys(usable).length > 0
      ? { ...usable, apply: props.onSettingChange, active }
      : undefined
  })

  // The plan-mode toggle reads the current option values from its `agent` view,
  // derived here from the option groups.
  const ctrl = useControlResponseHandling(
    {
      get agentId() { return props.agentId },
      get agent() {
        return {
          optionValues: currentOptionValues(),
          agentProvider: props.agent?.agentProvider,
        }
      },
      get controlRequests() { return props.controlRequests },
      get onControlResponse() { return props.onControlResponse },
      get onSettingChange() { return props.onSettingChange },
      get onSendMessage() { return props.onSendMessage },
      get onSendControlFeedback() { return props.onSendControlFeedback },
      get settingsLoading() { return props.settingsLoading },
      get agentWorking() { return props.agentWorking },
      get canInterrupt() { return props.canInterrupt },
    },
    answerState,
    () => editorContentRef,
    editorHeight.resetEditorHeight,
    () => attachments(),
    async (content, fileAttachments) => {
      const sentAttachmentDraftKey = untrack(att.activeDraftKey)
      startSending()
      setEnqueueInFlight(true)
      try {
        const editing = queueEdit.activeEditingInput()
        if (editing && props.onUpdateQueueItem) {
          queueEdit.markUpdateStarted()
          try {
            await props.onUpdateQueueItem(editing, content, fileAttachments ?? [])
          }
          catch (error) {
            queueEdit.markUpdateFailed()
            throw error
          }
          queueEdit.markUpdateCompleted(editing)
        }
        else {
          await props.onSendMessage(content, fileAttachments)
        }
        if (untrack(att.activeDraftKey) === sentAttachmentDraftKey)
          clearAllAttachments()
        else if (sentAttachmentDraftKey)
          clearCachedAttachments(sentAttachmentDraftKey)
      }
      finally {
        stopSending()
        setEnqueueInFlight(false)
      }
    },
  )

  /**
   * Whether an EMPTY composer still submits something.
   *
   * One definition, read by the editor's own Enter handling and by the keyboard
   * layer's emptiness context. Two copies would eventually disagree, and the
   * disagreement has a name: `$mod+Enter` would steer the input queue at the
   * exact moment a control request waits for an approval this submits.
   */
  const allowEmptySend = () =>
    (!!ctrl.activeControlRequest() && !ctrl.isAskUserQuestion()) || attachments().length > 0

  /**
   * Whether submitting right now would send anything.
   *
   * Reads the LIVE ProseMirror document through `editorContentRef.get()`, not
   * the `hasContent` signal beside it. That signal is fed by Milkdown's
   * `markdownUpdated` listener, which is debounced 200 ms (see
   * `markdownEditor/editorSetup.ts`), so for a fifth of a second after every
   * keystroke it still reads empty -- and `handleSend` re-serializes the
   * document for exactly this reason. A context built on the signal would let
   * the steer shortcut claim `$mod+Enter` from a user who just typed, and
   * `preventDefault` would stop the message ever reaching the editor.
   *
   * `hasContent` is deliberately left alone: the Send button, the height reset
   * and the ask-user-question clearing all tolerate the lag.
   */
  const hasPendingInput = () => (editorContentRef?.get() ?? '') !== '' || allowEmptySend()

  // Clear interrupt loading when the button hides.
  createEffect(on(ctrl.showInterrupt, (show) => {
    if (!show) {
      interruptLoading.stop()
    }
  }))

  // Expose addFiles for external callers (e.g. ChatDropZone).
  // eslint-disable-next-line solid/reactivity -- one-time ref registration, addFilesWhenReady is stable
  props.addFilesRef?.(addFilesWhenReady)
  // eslint-disable-next-line solid/reactivity -- one-time ref registration, handler is stable
  props.addDropDataTransferRef?.(addDropDataTransferWhenReady)

  const branchGitView = createMemo(() => {
    const tab = props.gitTab ?? {}
    return repoGitView(tab, props.repoGitStore, tab)
  })
  /**
   * The agent's worker home directory, for every tilde-compressed path this
   * panel shows: the branch chip's tooltip, the `[+]` menu's branch row, and
   * the info card's Directory and plan-file rows.
   *
   * From the WORKER STORE, not from `props.agent.homeDir`. `agentTabToInfo`
   * builds the `AgentInfo` from a Tab row, which carries no home directory, so
   * that field is the empty string on every path that renders this panel --
   * `tildify` then returns the absolute path and the sidebar row beside the
   * chip shortens the SAME directory while the chip does not. `TileRenderer`
   * already reads this route for `ChatView`.
   */
  const workerHomeDir = () => workerInfoStore.getHomeDir(props.agent?.workerId ?? '')
  /**
   * The checkout that the branch chip and the `[+]` menu's branch row identify.
   *
   * ONE value for both, because a user switches between the two surfaces with
   * one preference toggle and must not read two different answers. It is
   * required whole at each boundary, so neither surface repairs a missing kind
   * and no third layer applies a second default.
   */
  const workingTree = createMemo<WorkingTreeInfo>(() => {
    const git = branchGitView()
    const os = workerInfoStore.getOs(props.agent?.workerId ?? '')
    return {
      isWorktree: git?.isWorktree ?? false,
      name: git?.branchLabel ?? '',
      directory: git?.toplevel ?? '',
      homeDir: workerHomeDir(),
      // Undefined rather than `flavorFromOs(undefined)`, which answers 'posix'
      // and would stop a Windows path compressing while the OS is unknown.
      flavor: os ? flavorFromOs(os) : undefined,
      stats: git?.diffStats,
    }
  })
  const info = useAgentInfoCard({
    get agent() { return props.agent },
    get agentSessionInfo() { return props.agentSessionInfo },
    get branchName() { return branchGitView()?.branchLabel },
    get gitView() { return branchGitView() },
    get homeDir() { return workerHomeDir() },
  })
  const modelContextWindow = createMemo(() =>
    selectedModelContextWindow(props.agent?.optionGroups, currentModel()) || undefined,
  )
  const activeDraftKey = createMemo(() => {
    if (!props.agentId)
      return undefined
    const request = ctrl.activeControlRequest()
    if (!request)
      return queueEdit.attachmentDraftKey()
    // Keyed on the request INSTANCE, so a re-ask that reuses the id opens an
    // empty editor rather than the text the user typed for the instance that
    // went away. `cleanupControlRequestDrafts` composes the same key.
    const pageSuffix = ctrl.isAskUserQuestion() ? `-q-${answerState.currentPage()}` : ''
    return `${props.agentId}-ctrl-${requestInstanceId(request)}${pageSuffix}`
  })
  let triggerSend: (() => void | Promise<void>) | undefined

  // The body of the agent-info card. Rendered by the status bar's info popover
  // and by the `[+]` menu's "Agent info" submenu, so it is written once: the
  // two surfaces must show the same rows.
  const agentInfoRows = () => <div class={styles.infoRows}>{info.infoHoverCardContent()}</div>

  // The context-usage / rate-limit info dropdown. Extracted so its call sites can't drift
  // on the trigger button, the rate-limit countdown, or the hover-card body. Closes over
  // `info`/`props`/`modelContextWindow`, so it needs no props of its own.
  const AgentInfoTrigger: Component = () => (
    <DropdownMenu
      // A card of labelled rows. `card` carries the surface with it, so this
      // and the `[+]` menu's copy of the same card cannot inset their rows
      // differently.
      as="card"
      trigger={triggerProps => (
        <button
          class={styles.infoTrigger}
          data-testid="agent-info-trigger"
          {...triggerProps}
        >
          <ContextUsageGrid contextUsage={props.agentSessionInfo?.contextUsage} modelContextWindow={modelContextWindow()} agentProvider={props.agent?.agentProvider} size={iconSize.xs} />
          <Show when={info.urgentRateLimit()}>
            {rl => (
              <Tooltip
                text={(() => {
                  const resetsAt = getResetsAt(rl().info)
                  return resetsAt ? formatResetTimestamp(resetsAt) : undefined
                })()}
              >
                <span class={styles.rateLimitCountdown}>
                  {rl().countdown}
                </span>
              </Tooltip>
            )}
          </Show>
        </button>
      )}
      data-testid="agent-info-popover"
    >
      {agentInfoRows()}
    </DropdownMenu>
  )

  // A stable reference the status bar can hold. Passing `<AgentInfoTrigger />`
  // through the prop instead would rebuild the trigger, and close its popover,
  // every time the panel's `agent` prop takes a new identity.
  const renderAgentInfoTrigger = () => <AgentInfoTrigger />

  return (
    <div
      ref={panelRef}
      class={styles.editorPanelWrapper}
      data-testid="agent-editor-panel"
      data-chat-panel
    >
      <div
        class={`${styles.editorResizeHandle} ${isDragging() ? styles.editorResizeHandleActive : ''}`}
        data-testid="editor-resize-handle"
        on:pointerdown={handleResizeStart}
        on:dblclick={resetEditorHeight}
      />
      <div
        class={styles.inputArea}
        data-no-status-bar={preferences.showComposerStatusBar() ? undefined : ''}
      >
        <AgentInputQueuePauseBanner
          paused={queuePaused()}
          busy={pauseInFlight()}
          reason={props.inputQueue?.pauseReason ?? AgentInputQueuePauseReason.UNSPECIFIED}
          onResume={() => setQueuePaused(false)}
        />
        <AgentInputQueue
          snapshot={props.inputQueue}
          clientId={props.queueClientId ?? ''}
          activeEditInputId={queueEdit.activeEditingInput()?.id}
          supportsSteering={props.agent?.supportsSteering ?? false}
          onEdit={(item, takeover) => {
            queueEdit.loadQueueEdit(item, takeover, false)
          }}
          onDelete={(item) => {
            fireQueueRpc(props.onDeleteQueueItem?.(item).then(() => queueEdit.clearQueueEditArtifacts(item)))
          }}
          onCancelEdit={(item) => {
            fireQueueRpc(props.onCancelQueueEdit?.(item).then(() => queueEdit.clearQueueEditArtifacts(item)))
          }}
          onMove={(item, beforeInputId) => fireQueueRpc(props.onMoveQueueItem?.(item, beforeInputId))}
          onRetry={(item, confirmUncertain) => {
            if (confirmUncertain)
              setUncertainRetry(item)
            else
              fireQueueRpc(props.onRetryQueueItem?.(item, false))
          }}
          onSteer={item => fireQueueRpc(props.onSteerQueueItem?.(item))}
        />
        <Show when={!ctrl.activeControlRequest()}>
          <AttachmentStrip attachments={attachments} onRemove={removeAttachment} />
        </Show>
        <input
          ref={fileInputRef}
          type="file"
          multiple
          accept={acceptAttribute()}
          disabled={enqueueInFlight()}
          style={{ display: 'none' }}
          onChange={handleFileInputChange}
          data-testid="file-input"
        />
        <MarkdownEditor
          surface="chat"
          suppressAutoFocus={props.suppressAutoFocus}
          draftKey={{
            agentId: props.agentId,
            key: activeDraftKey(),
            controlRequestId: ctrl.activeControlRequest()?.requestId,
          }}
          onSend={ctrl.activeControlRequest() ? ctrl.handleControlSend : ctrl.handleSend}
          onAfterSend={queueEdit.handleAfterSend}
          onDraftKeyChanged={(key) => {
            const pending = queueEdit.takePendingTextForDraftKey(key)
            if (pending !== undefined)
              editorContentRef?.set(pending)
          }}
          disabled={disabled()}
          disabledPlaceholder={props.disabledReason}
          onTogglePlanMode={ctrl.togglePlanMode}
          pinnedHeight={editorMinHeightSignal()}
          maxHeight={editorHeight.maxEditorHeight()}
          onContentHeightChange={setEditorContentHeight}
          onContentChange={(has) => {
            setHasContent(has)
            // When the editor becomes empty and the manual height override
            // is at (or below) the minimum, clear it so the editor snaps
            // back to its natural single-line size.
            if (!has) {
              const h = editorMinHeightSignal()
              if (h !== undefined && h <= EDITOR_MIN_HEIGHT)
                editorHeight.resetEditorHeight()
            }
            if (has && ctrl.isAskUserQuestion()) {
              const page = answerState.currentPage()
              answerState.setSelections(prev => (prev[page] ?? []).length > 0 ? { ...prev, [page]: [] } : prev)
            }
          }}
          imperative={{
            sendRef: (fn) => {
              triggerSend = fn
              props.triggerSendRef?.(fn)
              if (panelRef)
                registerChatPanel(panelRef, { send: fn, hasPendingInput })
            },
            focusRef: (fn) => {
              editorFocusFn = fn
              props.focusRef?.(fn)
            },
            contentRef: (get, set) => {
              editorContentRef = { get, set }
            },
            insertRef: (fn) => {
              editorInsertFn = fn
            },
            onReady: () => {
              editorReady = true
              const pending = queueEdit.takePendingText()
              if (pending !== undefined)
                editorContentRef?.set(pending)
              tryRegisterEditorRef(props.agentId)
            },
          }}
          // The ONE place that decides whether a control request blocks an
          // attachment. `MarkdownEditor` reads these handlers at event time, so
          // an absent `attachments` refuses the paste and the drop by itself.
          // `addFiles`'s second argument marks a pasted image, which changes its filename.
          attachments={!ctrl.activeControlRequest() && !enqueueInFlight()
            ? {
                onPaste: files => addFiles(files, true),
                onDrop: dataTransfer => void att.addDroppedDataTransfer(dataTransfer),
              }
            : undefined}
          placeholder={ctrl.isAskUserQuestion() ? 'Type a custom answer...' : ctrl.activeControlRequest() ? 'Type a rejection reason...' : undefined}
          allowEmptySend={allowEmptySend()}
          // The keyed owner is what reacts in this slot. `createComponent`
          // untracks the element that this prop getter builds, so the editor's
          // inserting effect never observes the request. The `<Show>` alone
          // rebuilds the banner, and it hands the component ONE request instance
          // for its whole life.
          //
          // A plain conditional passes `request` as a reactive prop instead.
          // Every memo in the component's body then re-runs against the removed
          // request. The question detection is one such memo, and a provider
          // plugin's payload parsing is another.
          banner={(
            <Show when={ctrl.activeControlRequest()} keyed>
              {request => (
                <ControlRequestContent
                  request={request}
                  answerState={answerState}
                  optionsDisabled={hasContent()}
                  agentProvider={props.agent?.agentProvider}
                />
              )}
            </Show>
          )}
          plus={(
            // The `[+]` menu stays available during control requests (settings/mode
            // remain adjustable); only "Attach file" is disabled inside it.
            <ComposerPlusMenu
              optionGroups={props.agent?.optionGroups}
              optionValues={currentOptionValues()}
              agentProvider={props.agent?.agentProvider}
              onSettingChange={props.onSettingChange}
              onAttachFile={() => fileInputRef?.click()}
              canAttach={!ctrl.activeControlRequest() && !enqueueInFlight()}
              disabledReason={props.disabledReason}
              attachmentDisabledReason={enqueueInFlight() ? 'Queueing input...' : undefined}
              settingsLoading={props.settingsLoading}
              workingTree={workingTree()}
              branchActions={props.branchActions}
              branchWorkerId={props.branchWorkerId}
              branchDisabledReason={props.branchDisabledReason}
              // The stable function, not a rendered element — see the prop's doc.
              agentInfo={info.showInfoTrigger() ? agentInfoRows : undefined}
              enterKeyMode={preferences.enterKeyMode}
              onToggleEnterMode={() => {
                const next = preferences.enterKeyMode() === 'enter-sends' ? 'cmd-enter-sends' : 'enter-sends'
                preferences.setEnterKeyMode(next)
              }}
              showStatusBar={preferences.showComposerStatusBar}
              onToggleStatusBar={() => preferences.setShowComposerStatusBar(!preferences.showComposerStatusBar())}
            />
          )}
          // One action row, carrying its own layout: a control request takes the
          // whole width for its two-zone [secondary | primary] row, while the
          // composer's own cluster hugs the corner.
          // ONE read of the active request decides both halves of this slot.
          // `MarkdownEditor` reads this getter inside the effect that owns the
          // row, so a new head rebuilds the whole slot; a second read inside
          // `node` would let the layout flag and the rendered row disagree,
          // which is exactly what the prop's own doc forbids. The captured
          // `request` is a plain value, so it stays the instance the user
          // answers even after the store drops it.
          actions={(() => {
            const request = ctrl.activeControlRequest()
            return request
              ? {
                  layout: 'fullWidth' as const,
                  node: () => (
                    <>
                      <div class={styles.actionCluster}>
                        <AgentInputQueuePauseButton paused={queuePaused()} busy={pauseInFlight()} onToggle={() => setQueuePaused(!queuePaused())} />
                      </div>
                      <ControlRequestActions
                        request={request}
                        answerState={answerState}
                        agentProvider={props.agent?.agentProvider}
                        onRespond={(content) => {
                          ctrl.finishAnswer(request)
                          return ctrl.respondTo(request)(content)
                        }}
                        hasEditorContent={hasContent()}
                        onTriggerSend={() => { void triggerSend?.() }}
                        editorContentRef={() => editorContentRef}
                        presets={permissionPresets()}
                        contextUsage={props.agentSessionInfo?.contextUsage}
                        modelContextWindow={modelContextWindow()}
                      />
                    </>
                  ),
                }
              : {
                  layout: 'corner' as const,
                  node: () => (
                    <div class={styles.actionCluster} data-testid="composer-actions">
                      <AgentInputQueuePauseButton paused={queuePaused()} busy={pauseInFlight()} onToggle={() => setQueuePaused(!queuePaused())} />
                      <Show when={ctrl.showInterrupt()}>
                        {/*
                          The tooltip is the ONLY name this button has below
                          `sm`, where `hideInNarrowComposer` hides the word: a
                          `display: none` label reaches neither a screen reader
                          nor a by-name lookup.
                        */}
                        <Tooltip text={interruptLoading.loading() ? 'Interrupting...' : 'Interrupt'} ariaLabel>
                          <button
                            class={actionButtonClass(true)}
                            onMouseDown={keepFocusOnPress}
                            onClick={() => {
                              interruptLoading.start()
                              props.onInterrupt?.()
                              // The press leaves the composer focused, so the
                              // keyboard would sit over the output the user just
                              // stopped the agent to read. `keepFocusOnPress`
                              // above is what makes the composer still the
                              // active element here on Chrome and on Firefox,
                              // which focus a pressed button; the send path
                              // reads the same state through `decideSendFocus`.
                              dismissSoftKeyboard()
                            }}
                            disabled={interruptLoading.loading()}
                            data-testid="interrupt-button"
                          >
                            <Show when={interruptLoading.loading()} fallback={<Icon icon={Square} size="sm" />}>
                              <Spinner />
                            </Show>
                            <span class={hideInNarrowComposer}>{interruptLoading.loading() ? 'Interrupting...' : 'Interrupt'}</span>
                          </button>
                        </Tooltip>
                      </Show>
                      {/*
                        A paused queue does not drain, so this press parks the
                        message rather than delivering it. The button says so at
                        the moment of the press, which is the only moment that
                        reaches a user who never looked at the banner.

                        The visible word stays INSIDE the accessible name
                        ("Queue" within "Add to queue"), because a name that
                        drops the visible label breaks both a voice-control user
                        and every by-name lookup.
                      */}
                      <Tooltip text={queuePaused() ? 'Add to queue' : 'Send'} ariaLabel>
                        <button
                          type="button"
                          class={actionButtonClass()}
                          disabled={(!hasContent() && attachments().length === 0) || disabled() || sending()}
                          onMouseDown={keepFocusOnPress}
                          onClick={() => { void triggerSend?.() }}
                          data-testid="send-button"
                        >
                          <Show when={sending()} fallback={<Icon icon={SendHorizontal} size="sm" />}>
                            <Spinner data-testid="send-spinner" />
                          </Show>
                          <span class={hideInNarrowComposer}>{queuePaused() ? 'Queue' : 'Send'}</span>
                        </button>
                      </Tooltip>
                    </div>
                  ),
                }
          })()}
        />
      </div>
      <Show when={preferences.showComposerStatusBar()}>
        <ComposerStatusBar
          agent={props.agent}
          workingTree={workingTree()}
          optionValues={currentOptionValues()}
          onSettingChange={props.onSettingChange}
          branchActions={props.branchActions}
          branchWorkerId={props.branchWorkerId}
          branchDisabledReason={props.branchDisabledReason}
          disabledReason={props.disabledReason}
          infoTrigger={info.showInfoTrigger() ? renderAgentInfoTrigger : undefined}
        />
      </Show>
      <Show when={uncertainRetry()}>
        {item => (
          <ConfirmDialog
            title="Retry uncertain input?"
            confirmLabel="Retry"
            onConfirm={() => {
              const retryItem = item()
              setUncertainRetry()
              fireQueueRpc(props.onRetryQueueItem?.(retryItem, true))
            }}
            onCancel={() => setUncertainRetry()}
            data-testid="retry-uncertain-input-dialog"
          >
            The provider can already have accepted this input. A retry can send it twice.
          </ConfirmDialog>
        )}
      </Show>
    </div>
  )
}
