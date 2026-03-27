import type { Component } from 'solid-js'
import type { FileAttachment } from './attachments'
import type { EditorContentRef } from './controls/types'
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
import { showWarnToast } from '~/components/common/Toast'
import { Tooltip } from '~/components/common/Tooltip'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { formatResetTimestamp, getResetsAt } from '~/lib/rateLimitUtils'
import { safeGetString, safeRemoveItem, safeSetString } from '~/lib/safeStorage'
import { registerEditorRef, unregisterEditorRef } from '~/stores/editorRef.store'
import { spinner } from '~/styles/animations.css'
import { iconSize } from '~/styles/tokens'
import { useAgentInfoCard } from './AgentInfoCard'
import {
  buildAcceptAttribute,
  clearAttachments,
  describeUnsupportedAttachment,
  getAttachments,
  inferAttachmentDetails,
  isAttachmentSupported,
  MAX_TOTAL_ATTACHMENT_SIZE,
  nextPastedImageName,
  readFileAsAttachment,
  setAttachments as setAttachmentsCache,
  totalAttachmentSize,
} from './attachments'
import { AttachmentStrip } from './AttachmentStrip'
import * as styles from './ChatView.css'
import { ContextUsageGrid } from './ContextUsageGrid'
import { ControlRequestActions, ControlRequestContent } from './ControlRequestBanner'
import { useControlResponseHandling } from './controlResponseHandling'
import { EditorSettingsDropdown } from './EditorSettingsDropdown'
import { MarkdownEditor } from './MarkdownEditor'
import { getProviderPlugin } from './providers/registry'

export interface AgentEditorPanelProps {
  agentId: string
  agent?: AgentInfo
  disabled?: boolean
  onSendMessage: (content: string, attachments?: FileAttachment[]) => void
  focusRef?: (focus: () => void) => void
  controlRequests?: ControlRequest[]
  onControlResponse?: (agentId: string, content: Uint8Array) => Promise<void>
  onPermissionModeChange?: (mode: PermissionMode) => void
  onOptionGroupChange?: (key: string, value: string) => void
  onModelChange?: (model: string) => void
  onEffortChange?: (effort: string) => void
  onInterrupt?: () => void
  settingsLoading?: boolean
  agentSessionInfo?: AgentSessionInfo
  agentWorking?: boolean
  /** Height of the parent container, used for max editor height calculation. */
  containerHeight?: number
  /** Ref to expose the addFiles function for external callers (e.g. ChatDropZone). */
  addFilesRef?: (fn: (files: FileList | File[]) => Promise<number>) => void
  /** Ref to expose the triggerSend function for external callers. */
  triggerSendRef?: (fn: () => void) => void
}

// Per-agent editor height state
const EDITOR_MIN_HEIGHT = 38 // px - minimum height of the markdown editor wrapper
const EDITOR_MIN_HEIGHT_KEY_PREFIX = 'leapmux-editor-min-height-'

function editorMinHeightKey(agentId: string): string {
  return `${EDITOR_MIN_HEIGHT_KEY_PREFIX}${agentId}`
}

function getStoredEditorMinHeight(agentId: string): number | undefined {
  const stored = safeGetString(editorMinHeightKey(agentId))
  if (stored) {
    const val = Number.parseInt(stored, 10)
    if (!Number.isNaN(val) && val >= EDITOR_MIN_HEIGHT)
      return val
  }
  return undefined
}

// In-memory cache of per-agent heights (avoids localStorage reads on every render).
const editorMinHeightCache = new Map<string, number | undefined>()

export const AgentEditorPanel: Component<AgentEditorPanelProps> = (props) => {
  let panelRef: HTMLDivElement | undefined
  const [isDragging, setIsDragging] = createSignal(false)
  const [_editorContentHeight, setEditorContentHeight] = createSignal(0)
  const [hasContent, setHasContent] = createSignal(false)
  const [attachments, setAttachments] = createSignal<FileAttachment[]>([])
  let fileInputRef: HTMLInputElement | undefined
  const { loading: sending, start: startSending } = createLoadingSignal()
  const interruptLoading = createLoadingSignal()

  // Swap attachments on agentId change (same pattern as editor height cache).
  createEffect(on(() => props.agentId, (agentId, prevAgentId) => {
    if (prevAgentId) {
      setAttachmentsCache(prevAgentId, attachments())
    }
    setAttachments(agentId ? getAttachments(agentId) : [])
  }))

  const attachmentCapabilities = createMemo(() =>
    getProviderPlugin(props.agent?.agentProvider ?? AgentProvider.CLAUDE_CODE)?.attachments)
  const acceptAttribute = createMemo(() => buildAcceptAttribute(attachmentCapabilities()))
  const currentProviderLabel = () => agentProviderLabel(props.agent?.agentProvider)

  /** Validate and add files as attachments. */
  const addFiles = async (files: FileList | File[], isPastedImage?: boolean): Promise<number> => {
    const currentAttachments = attachments()
    let currentSize = totalAttachmentSize(currentAttachments)

    const accepted: FileAttachment[] = []
    const rejectionReasons = new Map<string, number>()
    let sizeLimitHit = false
    for (const file of [...files]) {
      if (currentSize + file.size > MAX_TOTAL_ATTACHMENT_SIZE) {
        sizeLimitHit = true
        break
      }
      const filename = isPastedImage
        ? `${nextPastedImageName(props.agentId)}.${file.type.split('/')[1] || 'png'}`
        : undefined
      const attachment = await readFileAsAttachment(file, filename)
      const details = inferAttachmentDetails(attachment.filename, attachment.mimeType, attachment.data)
      if (!isAttachmentSupported(details.kind, attachmentCapabilities())) {
        const reason = describeUnsupportedAttachment(details.kind, currentProviderLabel())
        rejectionReasons.set(reason, (rejectionReasons.get(reason) ?? 0) + 1)
        continue
      }
      accepted.push(attachment)
      currentSize += file.size
    }

    for (const [reason, count] of rejectionReasons) {
      showWarnToast(count === 1 ? reason : `${reason} (${count} files)`)
    }
    if (sizeLimitHit)
      showWarnToast('Total attachment size exceeds 10 MB')

    if (accepted.length === 0)
      return 0

    const updated = [...currentAttachments, ...accepted]
    setAttachments(updated)
    if (props.agentId)
      setAttachmentsCache(props.agentId, updated)
    return accepted.length
  }

  const removeAttachment = (id: string) => {
    const updated = attachments().filter(a => a.id !== id)
    setAttachments(updated)
    if (props.agentId)
      setAttachmentsCache(props.agentId, updated)
  }

  const clearAllAttachments = () => {
    setAttachments([])
    if (props.agentId)
      clearAttachments(props.agentId)
  }

  const handleFileInputChange = () => {
    if (fileInputRef?.files?.length) {
      addFiles(fileInputRef.files)
      fileInputRef.value = ''
    }
  }

  // Per-agent editor min height: reactive signal that switches when agentId changes.
  const [editorMinHeightSignal, setEditorMinHeightSignal] = createSignal<number | undefined>(undefined)
  // Load per-agent height when agentId changes.
  createEffect(on(() => props.agentId, (agentId) => {
    if (!agentId)
      return
    if (!editorMinHeightCache.has(agentId)) {
      editorMinHeightCache.set(agentId, getStoredEditorMinHeight(agentId))
    }
    setEditorMinHeightSignal(editorMinHeightCache.get(agentId))
  }))
  const setEditorMinHeight = (val: number | undefined) => {
    setEditorMinHeightSignal(val)
    if (props.agentId)
      editorMinHeightCache.set(props.agentId, val)
  }

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
      registerEditorRef(agentId, { get: editorContentRef.get, set: editorContentRef.set, focus: editorFocusFn })
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

  const resetEditorHeight = () => {
    setEditorMinHeight(undefined)
    if (props.agentId)
      safeRemoveItem(editorMinHeightKey(props.agentId))
  }

  // Control response handling (extracted module)
  const ctrl = useControlResponseHandling(
    props,
    askState,
    () => editorContentRef,
    resetEditorHeight,
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

  const handlePasteFiles = (files: File[]) => {
    if (ctrl.activeControlRequest())
      return
    addFiles(files, true)
  }

  const handleDropFiles = (files: File[]) => {
    if (ctrl.activeControlRequest())
      return
    addFiles(files)
  }

  // Agent info card (extracted module)
  const info = useAgentInfoCard(props)
  const modelContextWindow = createMemo(() =>
    Number(props.agent?.availableModels?.find(m => m.id === props.agent?.model)?.contextWindow) || undefined,
  )

  let triggerSend: (() => void) | undefined

  const maxEditorHeight = () => {
    const h = props.containerHeight ?? 0
    return h > 0 ? Math.floor(h * 0.5) : 200
  }

  const handleResizeStart = (e: MouseEvent) => {
    e.preventDefault()
    setIsDragging(true)
    const startY = e.clientY
    const maxHeight = maxEditorHeight()
    // Use the current visual height of the editor wrapper as the drag starting
    // point so the drag feels anchored to the handle's visual position.
    const editorWrapperEl = panelRef?.querySelector('[data-testid="chat-editor"]') as HTMLElement | null
    const startHeight = editorWrapperEl?.getBoundingClientRect().height
      ?? editorMinHeightSignal()
      ?? EDITOR_MIN_HEIGHT
    document.body.style.cursor = 'row-resize'

    const onMouseMove = (moveEvent: MouseEvent) => {
      const delta = startY - moveEvent.clientY
      const newMin = Math.max(EDITOR_MIN_HEIGHT, Math.min(maxHeight, startHeight + delta))
      setEditorMinHeight(newMin)
    }

    const onMouseUp = () => {
      setIsDragging(false)
      document.body.style.cursor = ''
      document.removeEventListener('mousemove', onMouseMove)
      document.removeEventListener('mouseup', onMouseUp)
      const val = editorMinHeightSignal()
      if (props.agentId) {
        if (val !== undefined && val > EDITOR_MIN_HEIGHT) {
          safeSetString(editorMinHeightKey(props.agentId), String(val))
        }
        else {
          safeRemoveItem(editorMinHeightKey(props.agentId))
        }
      }
    }

    document.addEventListener('mousemove', onMouseMove)
    document.addEventListener('mouseup', onMouseUp)
  }

  const handleResizeReset = () => {
    setEditorMinHeight(undefined)
    if (props.agentId)
      safeRemoveItem(editorMinHeightKey(props.agentId))
  }

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
        on:dblclick={handleResizeReset}
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
          agentId={props.agentId}
          controlRequestId={ctrl.activeControlRequest()?.requestId}
          onSend={ctrl.activeControlRequest() ? ctrl.handleControlSend : ctrl.handleSend}
          disabled={props.disabled}
          onTogglePlanMode={ctrl.togglePlanMode}
          requestedHeight={editorMinHeightSignal()}
          maxHeight={maxEditorHeight()}
          onContentHeightChange={setEditorContentHeight}
          onContentChange={(has) => {
            setHasContent(has)
            // When the editor becomes empty and the manual height override
            // is at (or below) the minimum, clear it so the editor snaps
            // back to its natural single-line size.
            if (!has) {
              const h = editorMinHeightSignal()
              if (h !== undefined && h <= EDITOR_MIN_HEIGHT)
                resetEditorHeight()
            }
            if (has && ctrl.isAskUserQuestion()) {
              const page = askCurrentPage()
              setAskSelections(prev => (prev[page] ?? []).length > 0 ? { ...prev, [page]: [] } : prev)
            }
          }}
          sendRef={(fn) => {
            triggerSend = fn
            props.triggerSendRef?.(fn)
          }}
          focusRef={(fn) => {
            editorFocusFn = fn
            props.focusRef?.(fn)
          }}
          contentRef={(get, set) => {
            editorContentRef = { get, set }
          }}
          onReady={() => {
            editorReady = true
            tryRegisterEditorRef(props.agentId)
          }}
          onPasteFiles={!ctrl.activeControlRequest() ? handlePasteFiles : undefined}
          onDropFiles={!ctrl.activeControlRequest() ? handleDropFiles : undefined}
          onUploadClick={!ctrl.activeControlRequest() ? () => fileInputRef?.click() : undefined}
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
                      resetEditorHeight()
                      return props.onControlResponse?.(agentId, content) ?? Promise.resolve()
                    }}
                    hasEditorContent={hasContent()}
                    onTriggerSend={() => triggerSend?.()}
                    editorContentRef={editorContentRef}
                    bypassPermissionMode={getProviderPlugin(props.agent?.agentProvider)?.bypassPermissionMode}
                    onPermissionModeChange={props.onPermissionModeChange}
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
                        onModelChange={props.onModelChange}
                        onEffortChange={props.onEffortChange}
                        onPermissionModeChange={props.onPermissionModeChange}
                        onOptionGroupChange={props.onOptionGroupChange}
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
