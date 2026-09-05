import type { Component } from 'solid-js'
import type { FileAttachment, PendingAttachmentFile } from './attachments'
import type { EditorContentRef } from './controls/types'
import type { BypassController, ProviderSettingChangeHandler } from './providerSettings'
import type { WorkingTreeInfo } from '~/components/common/WorkingTree'
import type { BranchMenuActions } from '~/components/workspace/branchActions'
import type { AgentInfo, AgentInputQueueSnapshot, Attachment, QueuedAgentInput } from '~/generated/proto/leapmux/v1/agent_pb'
import type { AgentSessionInfo } from '~/stores/agentSession.store'
import type { ControlRequest } from '~/stores/control.store'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { Tab } from '~/stores/tab.types'
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
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { clearDraft, loadDraft } from '~/lib/editor/draftPersistence'
import { EDITOR_MIN_HEIGHT } from '~/lib/editor/editorMinHeight'
import { keepFocusOnPress } from '~/lib/focusRetention'
import { randomUUID } from '~/lib/idGenerator'
import { flavorFromOs } from '~/lib/paths'
import { formatResetTimestamp, getResetsAt } from '~/lib/rateLimitUtils'
import { dismissSoftKeyboard } from '~/lib/softKeyboard'
import { requestInstanceId } from '~/stores/control.store'
import { registerEditorRef, unregisterEditorRef } from '~/stores/editorRef.store'
import { registerPanelSend, unregisterPanelSend } from '~/stores/focusedChatSend.store'
import { repoGitView } from '~/stores/repoGit'
import { optionValuesFromGroups } from '~/stores/tab.helpers'
import { workerInfoStore } from '~/stores/workerInfo.store'
import { iconSize } from '~/styles/tokens'
import { useAgentInfoCard } from './AgentInfoCard'
import { AgentInputQueue } from './AgentInputQueue'
import { AttachmentStrip } from './AttachmentStrip'
import * as styles from './ChatView.css'
import { ComposerPlusMenu } from './composer/ComposerPlusMenu'
import { ComposerStatusBar } from './composer/ComposerStatusBar'
import { ControlRequestActions, ControlRequestContent } from './ControlRequestBanner'
import { useControlResponseHandling } from './controlResponseHandling'
import { createControlAnswerState } from './controls/types'
import { MarkdownEditor } from './markdownEditor/MarkdownEditor'
import { providerFor } from './providers/registry'
import { permissionPresetAvailable } from './providerSettings'
import {
  OPTION_ID_MODEL,
  optionGroup,
  selectedModelContextWindow,
} from './settingsGroups'
import { useChatAttachments } from './useChatAttachments'
import { useEditorMinHeight } from './useEditorMinHeight'
import { ContextUsageGrid } from './widgets/ContextUsageGrid'

export interface AgentEditorPanelProps {
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
  onBeginQueueEdit?: (item: QueuedAgentInput, takeover: boolean) => Promise<{ snapshot?: AgentInputQueueSnapshot, attachments: Attachment[], text?: string }>
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
  /** Ref to expose the triggerSend function for external callers. */
  triggerSendRef?: (fn: () => void) => void
}

const AgentInputQueuePauseButton: Component<{
  paused: boolean
  onSetPaused?: (paused: boolean) => Promise<void>
}> = props => (
  <button
    type="button"
    class="outline"
    onMouseDown={keepFocusOnPress}
    onClick={() => { void props.onSetPaused?.(!props.paused).catch(() => {}) }}
    data-testid="queue-pause-button"
  >
    {props.paused ? 'Resume Queue' : 'Pause Queue'}
  </button>
)

export const AgentEditorPanel: Component<AgentEditorPanelProps> = (props) => {
  let panelRef: HTMLDivElement | undefined
  const [_editorContentHeight, setEditorContentHeight] = createSignal(0)
  const [hasContent, setHasContent] = createSignal(false)
  let fileInputRef: HTMLInputElement | undefined
  const { loading: sending, start: startSending, stop: stopSending } = createLoadingSignal()
  const interruptLoading = createLoadingSignal()

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
  const [editingInput, setEditingInput] = createSignal<QueuedAgentInput>()
  const [uncertainRetry, setUncertainRetry] = createSignal<QueuedAgentInput>()
  const [queueUpdateInFlight, setQueueUpdateInFlight] = createSignal(false)
  let finishQueueEditAfterSend = false
  let pendingQueueEditText: string | undefined
  let pendingQueueEditAttachments: FileAttachment[] | undefined
  let normalAttachmentRestore: { key: string, attachments: FileAttachment[] } | undefined
  let queueEditRequestId = ''
  const attachmentDraftKey = () => editingInput() ? `${props.agentId}-queue-${editingInput()!.id}` : props.agentId

  const att = useChatAttachments({
    agentId: attachmentDraftKey,
    agentProvider: () => props.agent?.agentProvider ?? AgentProvider.CLAUDE_CODE,
    providerLabel: currentProviderLabel,
  })
  const attachments = att.attachments
  const acceptAttribute = att.acceptAttribute
  const addFiles = att.addFiles
  const removeAttachment = att.removeAttachment
  const clearAllAttachments = att.clearAllAttachments
  const replaceAttachments = att.replaceAttachments
  const handleFileInputChange = () => att.handleFileInputChange(fileInputRef)

  const loadQueueEdit = (item: QueuedAgentInput, takeover: boolean, restoreDraft: boolean) => {
    if (queueEditRequestId === item.id)
      return
    const beginEdit = props.onBeginQueueEdit
    if (!beginEdit)
      return
    queueEditRequestId = item.id
    const request = beginEdit(item, takeover)
    void request.then((response) => {
      const edited = response.snapshot?.items.find(candidate => candidate.id === item.id) ?? item
      const queueDraftKey = `${props.agentId}-queue-${item.id}`
      pendingQueueEditText = restoreDraft && loadDraft(queueDraftKey).content
        ? undefined
        : (response.text ?? edited.text)
      pendingQueueEditAttachments = response.attachments.map(attachment => ({
        id: randomUUID(),
        file: new File([new Uint8Array(attachment.data).buffer], attachment.filename, { type: attachment.mimeType }),
        filename: attachment.filename,
        mimeType: attachment.mimeType,
        data: attachment.data,
        size: attachment.data.byteLength,
      }))
      normalAttachmentRestore = { key: untrack(() => props.agentId), attachments: [...untrack(attachments)] }
      setEditingInput(edited)
    }).catch(() => {}).finally(() => {
      if (queueEditRequestId === item.id)
        queueEditRequestId = ''
    })
  }

  createEffect(() => {
    if (editingInput() || queueEditRequestId || !props.queueClientId)
      return
    const owned = props.inputQueue?.items.find(item => item.editOwnerClientId === props.queueClientId)
    if (owned)
      loadQueueEdit(owned, false, true)
  })

  createEffect(() => {
    const editing = editingInput()
    const activeAttachmentKey = att.activeDraftKey()
    if (editing && activeAttachmentKey === `${props.agentId}-queue-${editing.id}` && pendingQueueEditAttachments !== undefined) {
      replaceAttachments(pendingQueueEditAttachments)
      pendingQueueEditAttachments = undefined
      return
    }
    if (!editing && normalAttachmentRestore?.key === activeAttachmentKey) {
      replaceAttachments(normalAttachmentRestore.attachments)
      normalAttachmentRestore = undefined
    }
  })

  createEffect(() => {
    const initialEditing = editingInput()
    const snapshot = props.inputQueue
    if (!initialEditing || !snapshot)
      return
    let current: QueuedAgentInput | undefined
    for (const item of snapshot.items) {
      if (item.id === initialEditing.id) {
        current = item
        break
      }
    }
    if ((!current || current.editOwnerClientId !== props.queueClientId) && !queueUpdateInFlight()) {
      pendingQueueEditText = undefined
      pendingQueueEditAttachments = undefined
      clearAllAttachments()
      setEditingInput()
      queueMicrotask(() => clearDraft(`${props.agentId}-queue-${initialEditing.id}`))
    }
  })

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
        unregisterPanelSend(panelRef)
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
  const bypass = createMemo<BypassController | undefined>(() => {
    const settings = props.agent?.agentProvider
      ? providerFor(props.agent.agentProvider)?.permissionPresets?.bypass
      : undefined
    return permissionPresetAvailable(settings, props.agent?.optionGroups) && props.onSettingChange
      ? { settings, apply: props.onSettingChange }
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
      startSending()
      try {
        const editing = editingInput()
        if (editing && props.onUpdateQueueItem) {
          setQueueUpdateInFlight(true)
          try {
            await props.onUpdateQueueItem(editing, content, fileAttachments ?? [])
          }
          catch (error) {
            setQueueUpdateInFlight(false)
            throw error
          }
          finishQueueEditAfterSend = true
        }
        else {
          await props.onSendMessage(content, fileAttachments)
        }
        clearAllAttachments()
      }
      finally {
        stopSending()
      }
    },
  )

  // Clear interrupt loading when the button hides.
  createEffect(on(ctrl.showInterrupt, (show) => {
    if (!show) {
      interruptLoading.stop()
    }
  }))

  // Expose addFiles for external callers (e.g. ChatDropZone).
  // eslint-disable-next-line solid/reactivity -- one-time ref registration, addFiles is stable
  props.addFilesRef?.(addFiles)
  // eslint-disable-next-line solid/reactivity -- one-time ref registration, handler is stable
  props.addDropDataTransferRef?.(att.addDroppedDataTransfer)

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
      return editingInput() ? `${props.agentId}-queue-${editingInput()!.id}` : props.agentId
    // Keyed on the request INSTANCE, so a re-ask that reuses the id opens an
    // empty editor rather than the text the user typed for the instance that
    // went away. `cleanupControlRequestDrafts` composes the same key.
    const pageSuffix = ctrl.isAskUserQuestion() ? `-q-${answerState.currentPage()}` : ''
    return `${props.agentId}-ctrl-${requestInstanceId(request)}${pageSuffix}`
  })
  let triggerSend: (() => void) | undefined

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
        <AgentInputQueue
          snapshot={props.inputQueue}
          clientId={props.queueClientId ?? ''}
          activeEditInputId={editingInput()?.id}
          supportsSteering={props.agent?.supportsSteering ?? false}
          onEdit={(item, takeover) => {
            loadQueueEdit(item, takeover, false)
          }}
          onDelete={(item) => {
            // The RPC completion must inspect edit ownership after newer snapshots apply.
            // eslint-disable-next-line solid/reactivity
            void props.onDeleteQueueItem?.(item).then(() => {
              clearDraft(`${props.agentId}-queue-${item.id}`)
              if (untrack(editingInput)?.id === item.id) {
                clearAllAttachments()
                setEditingInput()
              }
            }).catch(() => {})
          }}
          onCancelEdit={(item) => {
            // The RPC completion must inspect edit ownership after newer snapshots apply.
            // eslint-disable-next-line solid/reactivity
            void props.onCancelQueueEdit?.(item).then(() => {
              clearDraft(`${props.agentId}-queue-${item.id}`)
              if (untrack(editingInput)?.id === item.id) {
                clearAllAttachments()
                setEditingInput()
              }
            }).catch(() => {})
          }}
          onMove={(item, beforeInputId) => { void props.onMoveQueueItem?.(item, beforeInputId).catch(() => {}) }}
          onRetry={(item, confirmUncertain) => {
            if (confirmUncertain)
              setUncertainRetry(item)
            else
              void props.onRetryQueueItem?.(item, false).catch(() => {})
          }}
          onSteer={(item) => { void props.onSteerQueueItem?.(item).catch(() => {}) }}
        />
        <Show when={!ctrl.activeControlRequest()}>
          <AttachmentStrip attachments={attachments} onRemove={removeAttachment} />
        </Show>
        <input
          ref={fileInputRef}
          type="file"
          multiple
          accept={acceptAttribute()}
          style={{ display: 'none' }}
          onChange={handleFileInputChange}
          data-testid="file-input"
        />
        <MarkdownEditor
          draftKey={{
            agentId: props.agentId,
            key: activeDraftKey(),
            controlRequestId: ctrl.activeControlRequest()?.requestId,
          }}
          onSend={ctrl.activeControlRequest() ? ctrl.handleControlSend : ctrl.handleSend}
          onAfterSend={() => {
            if (finishQueueEditAfterSend) {
              finishQueueEditAfterSend = false
              setEditingInput()
              setQueueUpdateInFlight(false)
            }
          }}
          onDraftKeyChanged={(key) => {
            const editing = editingInput()
            if (!editing || key !== `${props.agentId}-queue-${editing.id}`)
              return
            if (pendingQueueEditText !== undefined) {
              editorContentRef?.set(pendingQueueEditText)
              pendingQueueEditText = undefined
            }
          }}
          disabled={disabled()}
          disabledPlaceholder={props.disabledReason}
          onTogglePlanMode={ctrl.togglePlanMode}
          requestedHeight={editorMinHeightSignal()}
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
                registerPanelSend(panelRef, fn)
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
              tryRegisterEditorRef(props.agentId)
            },
          }}
          // The ONE place that decides whether a control request blocks an
          // attachment. `MarkdownEditor` reads these handlers at event time, so
          // an absent `attachments` refuses the paste and the drop by itself.
          // `addFiles`'s second argument marks a pasted image, which renames it.
          attachments={!ctrl.activeControlRequest()
            ? {
                onPaste: files => addFiles(files, true),
                onDrop: dataTransfer => void att.addDroppedDataTransfer(dataTransfer),
              }
            : undefined}
          placeholder={ctrl.isAskUserQuestion() ? 'Type a custom answer...' : ctrl.activeControlRequest() ? 'Type a rejection reason...' : undefined}
          allowEmptySend={(!!ctrl.activeControlRequest() && !ctrl.isAskUserQuestion()) || attachments().length > 0}
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
              canAttach={!ctrl.activeControlRequest()}
              disabledReason={props.disabledReason}
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
                        <AgentInputQueuePauseButton paused={props.inputQueue?.paused ?? false} onSetPaused={props.onSetQueuePaused} />
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
                        onTriggerSend={() => triggerSend?.()}
                        editorContentRef={() => editorContentRef}
                        bypass={bypass()}
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
                      <AgentInputQueuePauseButton paused={props.inputQueue?.paused ?? false} onSetPaused={props.onSetQueuePaused} />
                      <Show when={ctrl.showInterrupt()}>
                        <button
                          class="outline"
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
                          <span class={styles.actionLabel}>{interruptLoading.loading() ? 'Interrupting...' : 'Interrupt'}</span>
                        </button>
                      </Show>
                      <button
                        type="button"
                        disabled={(!hasContent() && attachments().length === 0) || disabled() || sending()}
                        onMouseDown={keepFocusOnPress}
                        onClick={() => {
                          triggerSend?.()
                        }}
                        data-testid="send-button"
                      >
                        <Show when={sending()} fallback={<Icon icon={SendHorizontal} size="sm" />}>
                          <Spinner />
                        </Show>
                        <span class={styles.actionLabel}>Send</span>
                      </button>
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
              void props.onRetryQueueItem?.(retryItem, true).catch(() => {})
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
