import type { Component } from 'solid-js'
import type { FileAttachment, PendingAttachmentFile } from './attachments'
import type { EditorContentRef } from './controls/types'
import type { ProviderSettingChange } from './providers/registry'
import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import type { AgentSessionInfo } from '~/stores/agentSession.store'
import type { ControlRequest } from '~/stores/control.store'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { Tab } from '~/stores/tab.types'
import type { PermissionMode } from '~/utils/controlResponse'
import SendHorizontal from 'lucide-solid/icons/send-horizontal'
import Square from 'lucide-solid/icons/square'
import { createEffect, createMemo, createSignal, on, onCleanup, onMount, Show } from 'solid-js'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { Spinner } from '~/components/common/Spinner'
import { Tooltip } from '~/components/common/Tooltip'
import { usePreferences } from '~/context/PreferencesContext'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { EDITOR_MIN_HEIGHT } from '~/lib/editor/editorMinHeight'
import { keepFocusOnPress } from '~/lib/focusRetention'
import { formatResetTimestamp, getResetsAt } from '~/lib/rateLimitUtils'
import { dismissSoftKeyboard } from '~/lib/softKeyboard'
import { registerEditorRef, unregisterEditorRef } from '~/stores/editorRef.store'
import { registerPanelSend, unregisterPanelSend } from '~/stores/focusedChatSend.store'
import { repoGitView } from '~/stores/repoGit'
import { optionValuesFromGroups } from '~/stores/tab.helpers'
import { iconSize } from '~/styles/tokens'
import { useAgentInfoCard } from './AgentInfoCard'
import { AttachmentStrip } from './AttachmentStrip'
import * as styles from './ChatView.css'
import { ComposerPlusMenu } from './composer/ComposerPlusMenu'
import { ComposerStatusBar } from './composer/ComposerStatusBar'
import { ControlRequestActions, ControlRequestContent } from './ControlRequestBanner'
import { useControlResponseHandling } from './controlResponseHandling'
import { MarkdownEditor } from './markdownEditor/MarkdownEditor'
import { providerFor } from './providers/registry'
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
  onSendMessage: (content: string, attachments?: FileAttachment[]) => void
  focusRef?: (focus: () => void) => void
  controlRequests?: ControlRequest[]
  onControlResponse?: (agentId: string, content: Uint8Array, claimToken?: string) => Promise<void>
  /** Single dispatcher for all settings panel changes (model/effort/permissionMode/optionGroup). */
  onSettingChange?: (change: ProviderSettingChange) => void
  /**
   * Bypass-permission shortcut from control-request actions. Stays separate
   * from `onSettingChange` because control approval has different semantics
   * (it's tied to an active control request, not a free-form settings edit).
   */
  onPermissionModeChange?: (mode: PermissionMode) => void
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
   * Open the "Change branch..." dialog for the agent's repo. Wired from the
   * shell's branch-dialog state; the panel supplies the BranchRef from the
   * agent's git status + worker id.
   */
  onChangeBranch?: () => void
  /**
   * Open the "Delete branch..." dialog for the agent's repo. Same wiring as
   * onChangeBranch.
   */
  onDeleteBranch?: () => void
  /**
   * Why the branch actions are unusable (e.g. worker offline), or undefined
   * when usable. Both actions need the Worker, so one reason covers both.
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

export const AgentEditorPanel: Component<AgentEditorPanelProps> = (props) => {
  let panelRef: HTMLDivElement | undefined
  const [_editorContentHeight, setEditorContentHeight] = createSignal(0)
  const [hasContent, setHasContent] = createSignal(false)
  let fileInputRef: HTMLInputElement | undefined
  const { loading: sending, start: startSending } = createLoadingSignal()
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

  const att = useChatAttachments({
    agentId: () => props.agentId,
    agentProvider: () => props.agent?.agentProvider ?? AgentProvider.CLAUDE_CODE,
    providerLabel: currentProviderLabel,
  })
  const attachments = att.attachments
  const acceptAttribute = att.acceptAttribute
  const addFiles = att.addFiles
  const removeAttachment = att.removeAttachment
  const clearAllAttachments = att.clearAllAttachments
  const handleFileInputChange = () => att.handleFileInputChange(fileInputRef)

  const editorHeight = useEditorMinHeight({
    agentId: () => props.agentId,
    containerHeight: () => props.containerHeight,
    panelRef: () => panelRef,
  })
  const editorMinHeightSignal = editorHeight.editorMinHeight
  const isDragging = editorHeight.isDragging
  const handleResizeStart = editorHeight.handleResizeStart
  const resetEditorHeight = editorHeight.resetEditorHeight

  // Shared state for AskUserQuestion selections
  const [askSelections, setAskSelections] = createSignal<Record<number, string[]>>({})
  const [askCustomTexts, setAskCustomTexts] = createSignal<Record<number, string>>({})
  const [askCurrentPage, setAskCurrentPage] = createSignal(0)
  const askState = {
    selections: askSelections,
    setSelections: setAskSelections,
    customTexts: askCustomTexts,
    setCustomTexts: setAskCustomTexts,
    currentPage: askCurrentPage,
    setCurrentPage: setAskCurrentPage,
  }

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
      get settingsLoading() { return props.settingsLoading },
      get agentWorking() { return props.agentWorking },
      get canInterrupt() { return props.canInterrupt },
    },
    askState,
    () => editorContentRef,
    editorHeight.resetEditorHeight,
    () => attachments(),
    (content, fileAttachments) => {
      props.onSendMessage(content, fileAttachments)
      clearAllAttachments()
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

  const handlePasteFiles = (files: File[]) => {
    if (ctrl.activeControlRequest())
      return
    addFiles(files, true)
  }

  const handleDropDataTransfer = (dataTransfer: DataTransfer) => {
    if (ctrl.activeControlRequest())
      return
    void att.addDroppedDataTransfer(dataTransfer)
  }

  const branchGitView = createMemo(() => {
    const tab = props.gitTab ?? {}
    return repoGitView(tab, props.repoGitStore, tab)
  })
  const branchName = () => branchGitView()?.branchLabel
  const info = useAgentInfoCard({
    get agent() { return props.agent },
    get agentSessionInfo() { return props.agentSessionInfo },
    get branchName() { return branchGitView()?.branchLabel },
    get gitView() { return branchGitView() },
  })
  const modelContextWindow = createMemo(() =>
    selectedModelContextWindow(props.agent?.optionGroups, currentModel()) || undefined,
  )
  const activeDraftKey = createMemo(() => {
    if (!props.agentId)
      return undefined
    const request = ctrl.activeControlRequest()
    if (!request)
      return props.agentId
    const pageSuffix = ctrl.isAskUserQuestion() ? `-q-${askCurrentPage()}` : ''
    return `${props.agentId}-ctrl-${request.requestId}${pageSuffix}`
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
              const page = askCurrentPage()
              setAskSelections(prev => (prev[page] ?? []).length > 0 ? { ...prev, [page]: [] } : prev)
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
          attachments={!ctrl.activeControlRequest()
            ? {
                onPaste: handlePasteFiles,
                onDrop: handleDropDataTransfer,
              }
            : undefined}
          placeholder={ctrl.isAskUserQuestion() ? 'Type a custom answer...' : ctrl.activeControlRequest() ? 'Type a rejection reason...' : undefined}
          allowEmptySend={(!!ctrl.activeControlRequest() && !ctrl.isAskUserQuestion()) || attachments().length > 0}
          banner={
            ctrl.activeControlRequest()
              ? (
                  <ControlRequestContent
                    request={ctrl.activeControlRequest()!}
                    askState={askState}
                    optionsDisabled={hasContent()}
                    agentProvider={props.agent?.agentProvider}
                  />
                )
              : undefined
          }
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
              branchName={branchName()}
              onChangeBranch={() => props.onChangeBranch?.()}
              onDeleteBranch={() => props.onDeleteBranch?.()}
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
          actions={
            ctrl.activeControlRequest()
              ? {
                  layout: 'fullWidth',
                  node: () => (
                    <ControlRequestActions
                      request={ctrl.activeControlRequest()!}
                      askState={askState}
                      agentProvider={props.agent?.agentProvider}
                      onRespond={(agentId, content) => {
                      // Capture the per-instance claim token from the request being answered NOW,
                      // before removeRequest can drop it, so the worker's idempotency claim keys on
                      // the answered instance even in a double-submit / answer-after-cancel race.
                        const active = ctrl.activeControlRequest()
                        if (active?.requestId)
                          ctrl.cleanupControlRequestDrafts(active.requestId)
                        editorHeight.resetEditorHeight()
                        return props.onControlResponse?.(agentId, content, active?.claimToken) ?? Promise.resolve()
                      }}
                      hasEditorContent={hasContent()}
                      onTriggerSend={() => triggerSend?.()}
                      editorContentRef={() => editorContentRef}
                      bypassPermissionMode={props.agent?.agentProvider ? providerFor(props.agent.agentProvider)?.bypassPermissionMode : undefined}
                      onPermissionModeChange={props.onPermissionModeChange}
                      contextUsage={props.agentSessionInfo?.contextUsage}
                      modelContextWindow={modelContextWindow()}
                    />
                  ),
                }
              : {
                  layout: 'corner',
                  node: () => (
                    <div class={styles.actionCluster} data-testid="composer-actions">
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
                          startSending()
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
          }
        />
      </div>
      <Show when={preferences.showComposerStatusBar()}>
        <ComposerStatusBar
          agent={props.agent}
          branchName={branchName()}
          optionValues={currentOptionValues()}
          onSettingChange={props.onSettingChange}
          onChangeBranch={() => props.onChangeBranch?.()}
          onDeleteBranch={() => props.onDeleteBranch?.()}
          branchDisabledReason={props.branchDisabledReason}
          disabledReason={props.disabledReason}
          infoTrigger={info.showInfoTrigger() ? renderAgentInfoTrigger : undefined}
        />
      </Show>
    </div>
  )
}
