import type { Component } from 'solid-js'
import type { FileAttachment, PendingAttachmentFile } from './attachments'
import type { EditorContentRef } from './controls/types'
import type { ProviderSettingChange } from './providers/registry'
import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import type { AgentSessionInfo } from '~/stores/agentSession.store'
import type { ControlRequest } from '~/stores/control.store'
import type { PermissionMode } from '~/utils/controlResponse'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import SendHorizontal from 'lucide-solid/icons/send-horizontal'
import Square from 'lucide-solid/icons/square'
import { createEffect, createMemo, createSignal, on, onCleanup, onMount, Show } from 'solid-js'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { EDITOR_MIN_HEIGHT } from '~/lib/editor/editorMinHeight'
import { formatResetTimestamp, getResetsAt } from '~/lib/rateLimitUtils'
import { registerEditorRef, unregisterEditorRef } from '~/stores/editorRef.store'
import { spinner } from '~/styles/animations.css'
import { iconSize } from '~/styles/tokens'
import { useAgentInfoCard } from './AgentInfoCard'
import { AttachmentStrip } from './AttachmentStrip'
import * as styles from './ChatView.css'
import { ControlRequestActions, ControlRequestContent } from './ControlRequestBanner'
import { useControlResponseHandling } from './controlResponseHandling'
import { EditorSettingsDropdown } from './markdownEditor/EditorSettingsDropdown'
import { MarkdownEditor } from './markdownEditor/MarkdownEditor'
import { providerFor } from './providers/registry'
import { useChatAttachments } from './useChatAttachments'
import { useEditorMinHeight } from './useEditorMinHeight'
import { ContextUsageGrid } from './widgets/ContextUsageGrid'

export interface AgentEditorPanelProps {
  agentId: string
  agent?: AgentInfo
  disabled?: boolean
  onSendMessage: (content: string, attachments?: FileAttachment[]) => void
  focusRef?: (focus: () => void) => void
  controlRequests?: ControlRequest[]
  onControlResponse?: (agentId: string, content: Uint8Array) => Promise<void>
  /** Single dispatcher for all settings panel changes (model/effort/permissionMode/optionGroup). */
  onSettingChange?: (change: ProviderSettingChange) => void
  /**
   * Bypass-permission shortcut from control-request actions. Stays separate
   * from `onSettingChange` because control approval has different semantics
   * (it's tied to an active control request, not a free-form settings edit).
   */
  onPermissionModeChange?: (mode: PermissionMode) => void
  onInterrupt?: () => void
  settingsLoading?: boolean
  agentSessionInfo?: AgentSessionInfo
  agentWorking?: boolean
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
      registerEditorRef(agentId, { get: editorContentRef.get, set: editorContentRef.set, focus: editorFocusFn, insert: text => editorInsertFn?.(text) })
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

  const ctrl = useControlResponseHandling(
    props,
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

  const info = useAgentInfoCard(props)
  const modelContextWindow = createMemo(() =>
    Number(props.agent?.availableModels?.find(m => m.id === props.agent?.model)?.contextWindow) || undefined,
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

  return (
    <div
      ref={panelRef}
      class={styles.editorPanelWrapper}
      data-testid="agent-editor-panel"
    >
      <div
        class={`${styles.editorResizeHandle} ${isDragging() ? styles.editorResizeHandleActive : ''}`}
        data-testid="editor-resize-handle"
        on:mousedown={handleResizeStart}
        on:dblclick={resetEditorHeight}
      />
      <div class={styles.inputArea}>
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
          disabled={props.disabled}
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
                onUpload: () => fileInputRef?.click(),
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
          footer={
            ctrl.activeControlRequest()
              ? (
                  <ControlRequestActions
                    request={ctrl.activeControlRequest()!}
                    askState={askState}
                    agentProvider={props.agent?.agentProvider}
                    onRespond={(agentId, content) => {
                      const reqId = ctrl.activeControlRequest()?.requestId
                      if (reqId)
                        ctrl.cleanupControlRequestDrafts(reqId)
                      editorHeight.resetEditorHeight()
                      return props.onControlResponse?.(agentId, content) ?? Promise.resolve()
                    }}
                    hasEditorContent={hasContent()}
                    onTriggerSend={() => triggerSend?.()}
                    editorContentRef={editorContentRef}
                    bypassPermissionMode={props.agent?.agentProvider ? providerFor(props.agent.agentProvider)?.bypassPermissionMode : undefined}
                    onPermissionModeChange={props.onPermissionModeChange}
                    contextUsage={props.agentSessionInfo?.contextUsage}
                    modelContextWindow={modelContextWindow()}
                    infoTrigger={
                      info.showInfoTrigger()
                        ? (
                            <DropdownMenu
                              as="div"
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
                              class="card"
                              data-testid="agent-info-popover"
                            >
                              <div class={styles.infoRows}>
                                {info.infoHoverCardContent()}
                              </div>
                            </DropdownMenu>
                          )
                        : undefined
                    }
                  />
                )
              : (
                  <div class={styles.footerBar}>
                    <div class={styles.footerBarLeft}>
                      <EditorSettingsDropdown
                        disabled={props.disabled}
                        settingsLoading={props.settingsLoading}
                        model={props.agent?.model}
                        effort={props.agent?.effort}
                        permissionMode={props.agent?.permissionMode}
                        extraSettings={props.agent?.extraSettings}
                        availableModels={props.agent?.availableModels}
                        availableOptionGroups={props.agent?.availableOptionGroups}
                        agentProvider={props.agent?.agentProvider}
                        onChange={props.onSettingChange}
                      />
                      <Show when={info.showInfoTrigger()}>
                        <DropdownMenu
                          as="div"
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
                          class="card"
                          data-testid="agent-info-popover"
                        >
                          <div class={styles.infoRows}>
                            {info.infoHoverCardContent()}
                          </div>
                        </DropdownMenu>
                      </Show>
                    </div>
                    <div class={styles.footerBarRight}>
                      <Show when={ctrl.showInterrupt()}>
                        <button
                          class="outline"
                          onClick={() => {
                            interruptLoading.start()
                            props.onInterrupt?.()
                          }}
                          disabled={interruptLoading.loading()}
                          data-testid="interrupt-button"
                        >
                          <Show when={interruptLoading.loading()} fallback={<Icon icon={Square} size="sm" />}>
                            <Icon icon={LoaderCircle} size="sm" class={spinner} />
                          </Show>
                          {interruptLoading.loading() ? 'Interrupting...' : 'Interrupt'}
                        </button>
                      </Show>
                      <button
                        type="button"
                        disabled={(!hasContent() && attachments().length === 0) || props.disabled || sending()}
                        onClick={() => {
                          startSending()
                          triggerSend?.()
                        }}
                        data-testid="send-button"
                      >
                        <Show when={sending()} fallback={<Icon icon={SendHorizontal} size="sm" />}>
                          <Icon icon={LoaderCircle} size="sm" class={spinner} />
                        </Show>
                        Send
                      </button>
                    </div>
                  </div>
                )
          }
        />
      </div>
    </div>
  )
}
